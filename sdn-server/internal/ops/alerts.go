// Package ops is the node's OPERATIONAL ALERT registry: the small, in-memory
// record of what is currently wrong with this node, kept so an operator does
// not have to be reading the logs at the moment a thing breaks.
//
// WHY IT EXISTS. On 2026-09-13..15 the production fleet spent 31 hours
// rejecting every dataset publication (2,122 signature-type rejections on
// host-01) while its CelesTrak lanes failed 48 times in a row, and nothing
// surfaced it. Every one of those failures was already written down — in the
// logs, and in the retrieval ledger (internal/sourcemetrics, app_attempts).
// What was missing was a place where a CURRENT failure stays visible until it
// stops. That is all this package is.
//
// SHAPE. An alert is identified by (kind, subject): the class of failure and
// the thing that is failing — an app id, a producer peer id, a plugin id. A
// repeat of the same failure bumps a counter and the last error; it does not
// restart the clock, because how long a thing has been broken is the number an
// operator acts on. A success clears it.
//
// NOT A METRIC. Counters and histograms live in internal/metrics and answer
// "how much". This answers "what is broken right now", which is a much smaller
// set and has to survive a restart in the only way it can: by being rebuilt at
// boot from the durable state that recorded the failure (see the node's
// lane_failing seeding from app_attempts).
//
// NO DEPENDENCIES. Every subsystem that raises an alert (sourcemetrics, node,
// modulert, the update lane) imports this package, so it imports nothing of
// theirs beyond the standard library and the shared logger.
package ops

import (
	"sort"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("ops")

// Alert kinds. Each names a class of failure the node can detect for itself;
// the subject names which instance of it is failing.
const (
	// KindLaneFailing: a retrieval lane (app id) has consecutive failures.
	KindLaneFailing = "lane_failing"
	// KindPublicationRejected: a dataset publication from a producer peer
	// would not verify or would not materialize.
	KindPublicationRejected = "publication_rejected"
	// KindFlowCapabilityDenied: a flow or module (plugin id) asked for a
	// sensitive capability with no operator approval, so it did not load.
	KindFlowCapabilityDenied = "flow_capability_denied"
	// KindEnginePoisoned: the FlatSQL engine trapped and is being replaced.
	KindEnginePoisoned = "engine_poisoned"
	// KindEngineRebuilding: the read gate is answering ErrEngineRebuilding.
	KindEngineRebuilding = "engine_rebuilding"
	// KindUpdateFailed: a pushed update did not install on this box.
	KindUpdateFailed = "update_failed"
)

// Severities. Two levels only: something an operator should look at, and
// something an operator should act on.
const (
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// Status values reported on the anonymous health surface.
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
)

// Alert is one currently-failing thing.
type Alert struct {
	// Kind is the class of failure (one of the Kind* constants).
	Kind string `json:"kind"`
	// Subject is what is failing: an app id, a producer peer id (short
	// form), a plugin id, or "" for a node-wide condition.
	Subject string `json:"subject"`
	// Severity is "warning" or "error".
	Severity string `json:"severity"`
	// Since is when this alert first went active, and is NOT reset by a
	// repeat: how long a thing has been broken is the number an operator
	// acts on.
	Since time.Time `json:"since"`
	// Count is how many times the failure has been reported since Since.
	Count int `json:"count"`
	// LastError is the most recent error text, verbatim.
	LastError string `json:"last_error,omitempty"`
	// LastAt is when the failure was most recently reported.
	LastAt time.Time `json:"last_at"`
}

// KindCount is one (kind, severity) pair and how many alerts of it are
// active — the shape the Prometheus gauge is built from.
type KindCount struct {
	Kind     string
	Severity string
	Count    int
}

// Counts is the severity breakdown of the active set.
type Counts struct {
	Error   int `json:"error"`
	Warning int `json:"warning"`
}

// Snapshot is the registry's whole state as a JSON-ready value.
type Snapshot struct {
	// Status is "degraded" while ANY alert is active, "ok" otherwise. A
	// warning counts: the fleet outage this package exists for began as two
	// consecutive lane failures, which is a warning.
	Status string  `json:"status"`
	Counts Counts  `json:"counts"`
	Alerts []Alert `json:"alerts"`
}

type alertKey struct {
	kind    string
	subject string
}

// Registry holds the active alerts. The zero value is not usable; call
// NewRegistry. Every method tolerates a nil receiver so a subsystem that was
// never handed a registry needs no nil check at each call site.
type Registry struct {
	mu     sync.Mutex
	active map[alertKey]*Alert
	hooks  []func(Alert, bool)

	// now is injectable for tests.
	now func() time.Time
}

// NewRegistry returns an empty registry that logs every transition with the
// stable "OPS ALERT" prefix, so `journalctl | grep "OPS ALERT"` is a complete
// operator view without any further wiring.
func NewRegistry() *Registry {
	r := &Registry{active: make(map[alertKey]*Alert), now: time.Now}
	r.OnTransition(logTransition)
	return r
}

// SetClock replaces the registry's clock. Test seam.
func (r *Registry) SetClock(now func() time.Time) {
	if r == nil || now == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// OnTransition registers a hook called whenever an alert goes active
// (active=true) or is cleared (active=false). A severity change on an
// already-active alert also fires an active=true transition — an escalation
// from warning to error is news. A plain repeat does not fire: it only bumps
// Count/LastAt/LastError.
//
// Hooks run OUTSIDE the registry lock, so a hook may call back into the
// registry without deadlocking.
func (r *Registry) OnTransition(fn func(Alert, bool)) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, fn)
}

// Raise reports that (kind, subject) is failing. The first report activates the
// alert; every later one bumps Count, LastAt and LastError WITHOUT resetting
// Since. An unrecognized severity is treated as a warning rather than dropped:
// a mislabelled alert is still an alert.
func (r *Registry) Raise(kind, subject, severity, errText string) {
	if r == nil || kind == "" {
		return
	}
	if severity != SeverityError {
		severity = SeverityWarning
	}
	key := alertKey{kind: kind, subject: subject}

	r.mu.Lock()
	if r.active == nil {
		r.active = make(map[alertKey]*Alert)
	}
	now := r.now()
	transitioned := false
	existing, ok := r.active[key]
	if !ok {
		existing = &Alert{
			Kind:     kind,
			Subject:  subject,
			Severity: severity,
			Since:    now,
		}
		r.active[key] = existing
		transitioned = true
	} else if existing.Severity != severity {
		existing.Severity = severity
		transitioned = true
	}
	existing.Count++
	existing.LastAt = now
	if errText != "" {
		existing.LastError = errText
	}
	snapshot := *existing
	hooks := append([]func(Alert, bool){}, r.hooks...)
	r.mu.Unlock()

	if transitioned {
		for _, fn := range hooks {
			fn(snapshot, true)
		}
	}
}

// Clear reports that (kind, subject) is working again. Clearing an alert that
// is not active is a no-op, so a success path may call it unconditionally.
func (r *Registry) Clear(kind, subject string) {
	if r == nil || kind == "" {
		return
	}
	key := alertKey{kind: kind, subject: subject}

	r.mu.Lock()
	existing, ok := r.active[key]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.active, key)
	snapshot := *existing
	hooks := append([]func(Alert, bool){}, r.hooks...)
	r.mu.Unlock()

	for _, fn := range hooks {
		fn(snapshot, false)
	}
}

// Active returns the active alerts in a stable order: errors before warnings,
// then by kind, then by subject. The slice and its Alerts are copies.
func (r *Registry) Active() []Alert {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	out := make([]Alert, 0, len(r.active))
	for _, a := range r.active {
		out = append(out, *a)
	}
	r.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			// "error" sorts before "warning"; both are known values, and
			// any other value has already been normalized by Raise.
			return out[i].Severity == SeverityError
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

// Snapshot returns the whole registry state, JSON-ready.
func (r *Registry) Snapshot() Snapshot {
	alerts := r.Active()
	snap := Snapshot{Status: StatusOK, Alerts: alerts}
	for _, a := range alerts {
		if a.Severity == SeverityError {
			snap.Counts.Error++
		} else {
			snap.Counts.Warning++
		}
	}
	if len(alerts) > 0 {
		snap.Status = StatusDegraded
	}
	return snap
}

// KindCounts returns how many alerts are active per (kind, severity), in the
// same stable order as Active. It is what the sdn_alerts_active gauge reports.
func (r *Registry) KindCounts() []KindCount {
	type kindSeverity struct{ kind, severity string }
	alerts := r.Active()
	index := make(map[kindSeverity]int, len(alerts))
	out := make([]KindCount, 0, len(alerts))
	for _, a := range alerts {
		key := kindSeverity{kind: a.Kind, severity: a.Severity}
		if at, ok := index[key]; ok {
			out[at].Count++
			continue
		}
		index[key] = len(out)
		out = append(out, KindCount{Kind: a.Kind, Severity: a.Severity, Count: 1})
	}
	return out
}

// logTransition is the default hook: one line per transition, ERROR on raise
// and INFO on clear, always prefixed "OPS ALERT".
func logTransition(a Alert, active bool) {
	subject := a.Subject
	if subject == "" {
		subject = "-"
	}
	if active {
		log.Errorf("OPS ALERT raised kind=%s subject=%s severity=%s count=%d error=%q",
			a.Kind, subject, a.Severity, a.Count, a.LastError)
		return
	}
	log.Infof("OPS ALERT cleared kind=%s subject=%s severity=%s count=%d after=%s",
		a.Kind, subject, a.Severity, a.Count, a.LastAt.Sub(a.Since).Round(time.Second))
}

// Sink is the narrow view a subsystem needs to report alerts: raise a failure,
// clear it when the thing works again. *Registry implements it. Subsystems
// take a Sink rather than a *Registry so the node hands them the registry
// explicitly instead of any of them reaching for a package global.
type Sink interface {
	Raise(kind, subject, severity, errText string)
	Clear(kind, subject string)
}

var _ Sink = (*Registry)(nil)
