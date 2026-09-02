package trust

// The rules engine — evaluates every active `$TRP` against every subject the
// node has an opinion about, on the configured interval (0.1 Hz by default)
// and early when an event arrives, and turns each evaluation into a `$TRV`
// verdict. A PASSED flip is fanned out on the trust topics as the signed
// `$TRV` bytes; the latest verdict per (policy, subject) is kept for the API.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ChainAddress is one attested address the subject controls.
type ChainAddress struct {
	ChainID string
	Address string
}

// BalanceSource answers the subject's holdings for its attested addresses.
// Implementations live in the host as connectors only: the chain queries run
// in the bond-attestation WASM module over the generic http capability.
type BalanceSource interface {
	Balances(ctx context.Context, addresses []ChainAddress) ([]Holding, error)
}

// BalanceSourceFunc adapts a function to BalanceSource.
type BalanceSourceFunc func(ctx context.Context, addresses []ChainAddress) ([]Holding, error)

// Balances implements BalanceSource.
func (f BalanceSourceFunc) Balances(ctx context.Context, addresses []ChainAddress) ([]Holding, error) {
	return f(ctx, addresses)
}

// AddressResolver maps a subject peer id to its attested chain addresses
// (the ChainProofs of its verified `$EPM`).
type AddressResolver func(peerID string) ([]ChainAddress, error)

// Engine drives the evaluations.
type Engine struct {
	policies      *PolicyStore
	verdicts      *VerdictStore
	svc           *Service
	balances      BalanceSource
	resolve       AddressResolver
	publish       PublishFunc
	key           ed25519.PrivateKey
	peerID        string
	nowMs         func() int64
	extraSubjects func() []string

	// overrideMs, when > 0, replaces every policy's own interval (the
	// runtime setting `trust.evaluation_interval_ms`).
	overrideMs atomic.Uint32
	trigger    chan string
	mu         sync.RWMutex
	latest     map[string]Verdict // key policy|subject
	lastRun    time.Time
	runs       atomic.Uint64
	running    atomic.Bool

	// policiesList / verdictPut default to the stores; tests substitute them.
	policiesList func() ([]Policy, error)
	verdictPut   func(v Verdict, signedFB []byte) error
}

// EngineConfig wires an Engine.
type EngineConfig struct {
	Policies *PolicyStore
	Verdicts *VerdictStore
	Service  *Service
	Balances BalanceSource
	Resolve  AddressResolver
	Publish  PublishFunc
	Key      ed25519.PrivateKey
	PeerID   string
	NowMs    func() int64
	// Subjects, when set, adds peers to evaluate beyond the trust graph's
	// nodes — the daemon passes the directory's node profiles, so a node that
	// has published no trust edge yet still gets an honest verdict per peer
	// it knows (FAIL with the reason recorded) instead of no verdicts at all.
	Subjects func() []string
}

// NewEngine validates the wiring.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.Service == nil {
		return nil, errors.New("trust: engine needs the trust service")
	}
	if len(cfg.Key) != ed25519.PrivateKeySize {
		return nil, errors.New("trust: engine needs the evaluator's ed25519 key")
	}
	now := cfg.NowMs
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	e := &Engine{
		policies: cfg.Policies, verdicts: cfg.Verdicts, svc: cfg.Service, balances: cfg.Balances, resolve: cfg.Resolve,
		publish: cfg.Publish, key: cfg.Key, peerID: strings.TrimSpace(cfg.PeerID), nowMs: now, extraSubjects: cfg.Subjects,
		trigger: make(chan string, 1), latest: map[string]Verdict{},
	}
	e.policiesList = func() ([]Policy, error) {
		if e.policies == nil {
			return nil, errors.New("trust: no policy store")
		}
		return e.policies.List()
	}
	e.verdictPut = func(v Verdict, fb []byte) error {
		if e.verdicts == nil {
			return errors.New("trust: no verdict store")
		}
		return e.verdicts.Put(v, fb)
	}
	return e, nil
}

// SetIntervalOverride sets the runtime interval (0 restores each policy's own).
func (e *Engine) SetIntervalOverride(ms uint32) {
	if ms != 0 && ms < MinEvaluationIntervalMs {
		ms = MinEvaluationIntervalMs
	}
	e.overrideMs.Store(ms)
	e.Trigger("interval-changed")
}

// IntervalOverride reports the runtime override (0 = none).
func (e *Engine) IntervalOverride() uint32 { return e.overrideMs.Load() }

// Trigger asks for an early evaluation. Events during a run coalesce into
// exactly one extra run; the channel holds one pending trigger.
func (e *Engine) Trigger(source string) {
	select {
	case e.trigger <- source:
	default:
	}
}

// Runs counts completed evaluation passes (tests and the status lane).
func (e *Engine) Runs() uint64 { return e.runs.Load() }

// LastRun is when the last pass finished.
func (e *Engine) LastRun() time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastRun
}

// currentInterval is the cadence in force: the override, else the shortest
// active policy interval, else the default.
func (e *Engine) currentInterval(policies []Policy) time.Duration {
	if ms := e.overrideMs.Load(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	best := uint32(0)
	for _, p := range policies {
		if !p.Active {
			continue
		}
		if best == 0 || p.IntervalMs() < best {
			best = p.IntervalMs()
		}
	}
	if best == 0 {
		best = DefaultEvaluationIntervalMs
	}
	if best < MinEvaluationIntervalMs {
		best = MinEvaluationIntervalMs
	}
	return time.Duration(best) * time.Millisecond
}

// Run evaluates until ctx ends: on the interval and on triggers.
func (e *Engine) Run(ctx context.Context) {
	policies, _ := e.policiesList()
	timer := time.NewTimer(e.currentInterval(policies))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case src := <-e.trigger:
			policies = e.RunOnce(ctx, src)
		case <-timer.C:
			policies = e.RunOnce(ctx, "interval")
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(e.currentInterval(policies))
	}
}

// subjects are every node the trust graph knows plus every subject a verdict
// already exists for, minus the evaluator itself.
func (e *Engine) subjects() []string {
	seen := map[string]bool{}
	for _, id := range e.svc.Nodes() {
		if id != "" && id != e.peerID {
			seen[id] = true
		}
	}
	if e.extraSubjects != nil {
		for _, id := range e.extraSubjects() {
			if id = strings.TrimSpace(id); id != "" && id != e.peerID {
				seen[id] = true
			}
		}
	}
	e.mu.RLock()
	for key := range e.latest {
		if i := strings.LastIndex(key, "|"); i >= 0 {
			if s := key[i+1:]; s != "" && s != e.peerID {
				seen[s] = true
			}
		}
	}
	e.mu.RUnlock()
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// facts gathers one subject's inputs; the holdings lane is asked once per
// subject per pass.
func (e *Engine) facts(ctx context.Context, subject string) SubjectFacts {
	f := SubjectFacts{Trusters: e.svc.TrustersOf(subject)}
	if e.resolve == nil || e.balances == nil {
		f.HoldingsErr = "no bond lane is wired on this node"
		return f
	}
	addrs, err := e.resolve(subject)
	if err != nil {
		f.HoldingsErr = err.Error()
		return f
	}
	if len(addrs) == 0 {
		f.HoldingsErr = "the subject's profile carries no attested chain address"
		return f
	}
	holdings, err := e.balances.Balances(ctx, addrs)
	if err != nil {
		f.HoldingsErr = err.Error()
		return f
	}
	f.Holdings = holdings
	return f
}

// RunOnce evaluates every active policy against every subject and returns
// the policies it read (so the caller can size the next interval).
func (e *Engine) RunOnce(ctx context.Context, trigger string) []Policy {
	if !e.running.CompareAndSwap(false, true) {
		return nil
	}
	defer e.running.Store(false)
	policies, err := e.policiesList()
	if err != nil {
		return nil
	}
	now := e.nowMs()
	subjects := e.subjects()
	factsBy := map[string]SubjectFacts{}
	for _, p := range policies {
		if !p.Active {
			continue
		}
		for _, subject := range subjects {
			f, ok := factsBy[subject]
			if !ok {
				f = e.facts(ctx, subject)
				factsBy[subject] = f
			}
			out := EvaluatePolicy(p, f, now)
			key := p.ID + "|" + subject
			e.mu.RLock()
			prev, had := e.latest[key]
			e.mu.RUnlock()
			v := Verdict{
				ID:              fmt.Sprintf("%s:%s:%d", p.ID, subject, now),
				PolicyID:        p.ID,
				SubjectID:       subject,
				Passed:          out.Passed,
				PreviousPassed:  had && prev.Passed,
				Score:           out.Score,
				PreviousScore:   prev.Score,
				Results:         out.Results,
				Trigger:         trigger,
				EvaluatedAtMs:   now,
				EvaluatorPeerID: e.peerID,
			}
			flipped := !had || prev.Passed != out.Passed
			if flipped {
				fb, _, err := SignVerdict(&v, e.key)
				if err == nil {
					_ = e.verdictPut(v, fb)
					if e.publish != nil {
						_ = e.publish(TrustTopic(subject), fb)
						if e.peerID != "" && e.peerID != subject {
							_ = e.publish(TrustTopic(e.peerID), fb)
						}
					}
				}
			}
			e.mu.Lock()
			e.latest[key] = v
			e.mu.Unlock()
		}
	}
	e.mu.Lock()
	e.lastRun = time.Now()
	e.mu.Unlock()
	e.runs.Add(1)
	return policies
}

// Latest returns the newest verdict per (policy, subject), filtered.
func (e *Engine) Latest(policyID, subject string) []Verdict {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Verdict, 0, len(e.latest))
	for _, v := range e.latest {
		if policyID != "" && v.PolicyID != policyID {
			continue
		}
		if subject != "" && v.SubjectID != subject {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PolicyID != out[j].PolicyID {
			return out[i].PolicyID < out[j].PolicyID
		}
		return out[i].SubjectID < out[j].SubjectID
	})
	return out
}
