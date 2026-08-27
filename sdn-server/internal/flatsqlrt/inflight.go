package flatsqlrt

import (
	"strings"
	"sync/atomic"
	"time"
)

// THE POISON MESSAGE NAMED NOTHING, AND THAT WAS THE WHOLE INVESTIGATION.
//
// wasmrt abandons a dedicated execution thread by the label it dispatched
// under — "(batch)" for a whole statement, the export name for a single guest
// call. When the engine's uninterruptible per-call budget fires, that label is
// ALL the operator gets:
//
//	module poisoned ("flatsql"): dedicated execution thread abandoned mid-call
//	in "(batch)": wasm execution exceeded wall-clock timeout
//
// A node that answers nothing until a human intervenes, and no way to tell
// WHICH statement did it. accountQuery (flatsqlrt.go) logs the SQL, but only
// AFTER the statement returns, so the one statement that matters — the one
// that never returns — is the one it can never report.
//
// This file makes the in-flight statement observable WHILE it runs: the SQL is
// published before dispatch, an escalating watchdog logs it at 30s/1m/2m/4m,
// and a final ERROR fires just before the budget so the log names the
// statement one line above the poison. Cost is one atomic store and one timer
// per statement (1,454 statements in a 30-minute production window — noise).
//
// Phase is the caller's label for WHAT the node is doing (storage sets
// "boot: …" around each boot step), because "which statement" and "which boot
// phase" are different questions and a poisoned boot needs both.

// inFlightCall is the statement the engine is executing right now.
type inFlightCall struct {
	sql     string
	phase   string
	started time.Time
}

// inFlightWatchThresholds are the elapsed marks at which a still-running
// statement is logged. The last one is deliberately below any realistic
// budget so the escalation is visible before the abandon.
var inFlightWatchThresholds = []time.Duration{
	30 * time.Second,
	1 * time.Minute,
	2 * time.Minute,
	4 * time.Minute,
}

// SetPhase labels what the node is doing, for every log line this file emits
// until the next SetPhase. The empty string clears it. Safe on a nil runtime
// so callers never have to guard.
func (r *Runtime) SetPhase(phase string) {
	if r == nil {
		return
	}
	r.phase.Store(&phase)
}

// Phase returns the current phase label ("" when unset).
func (r *Runtime) Phase() string {
	if r == nil {
		return ""
	}
	if p := r.phase.Load(); p != nil {
		return *p
	}
	return ""
}

// InFlight reports the statement the engine is executing right now, how long
// it has been running, and the phase it belongs to. ok is false when the
// engine is idle. This is what turns a poison into an attribution.
func (r *Runtime) InFlight() (phase, sql string, elapsed time.Duration, ok bool) {
	if r == nil {
		return "", "", 0, false
	}
	c := r.inFlight.Load()
	if c == nil {
		return "", "", 0, false
	}
	return c.phase, c.sql, time.Since(c.started), true
}

// beginInFlight publishes sql as the in-flight statement and arms the
// escalating watchdog. The returned func clears both and MUST be deferred:
// leaving a stale in-flight entry would misattribute the next poison.
func (r *Runtime) beginInFlight(sql string) func() {
	if r == nil {
		return func() {}
	}
	call := &inFlightCall{sql: collapseSQL(sql), phase: r.Phase(), started: time.Now()}
	r.inFlight.Store(call)

	var timer *time.Timer
	var arm func(idx int)
	arm = func(idx int) {
		if idx >= len(inFlightWatchThresholds) {
			return
		}
		delay := inFlightWatchThresholds[idx]
		if idx > 0 {
			delay -= inFlightWatchThresholds[idx-1]
		}
		timer = time.AfterFunc(delay, func() {
			// Re-read: a finished statement clears the pointer, and a NEW
			// statement replaces it. Only keep barking about THIS one.
			if r.inFlight.Load() != call {
				return
			}
			log.Warnf("FlatSQL statement STILL RUNNING after %s%s — the engine is single-threaded and its per-call budget is uninterruptible; if this crosses %s the instance is abandoned and poisoned. SQL: %s",
				inFlightWatchThresholds[idx].Round(time.Second),
				phaseSuffix(call.phase),
				r.ExecBudget().Round(time.Second),
				call.sql)
			arm(idx + 1)
		})
	}
	arm(0)

	return func() {
		if timer != nil {
			timer.Stop()
		}
		r.inFlight.CompareAndSwap(call, nil)
	}
}

// ExecBudget is the wall-clock budget one engine call gets before the thread
// is abandoned. Reported with every watchdog line so the operator can see how
// much room is left without reading the source, and used by the storage layer
// to score each boot phase against the same number the engine enforces.
func (r *Runtime) ExecBudget() time.Duration {
	if r == nil || r.mod == nil {
		return DefaultEngineExecTimeout
	}
	return r.mod.ExecTimeout()
}

func phaseSuffix(phase string) string {
	if strings.TrimSpace(phase) == "" {
		return ""
	}
	return " in phase " + phase
}

// attributeExecErr decorates a failed engine call with the statement and phase
// that were in flight. THE POISON LINE IS THE ONE AN OPERATOR READS FIRST, so
// it has to carry the attribution — a "(batch)" label with no SQL cost this
// program a full investigation.
func (r *Runtime) attributeExecErr(err error) error {
	if r == nil || err == nil {
		return err
	}
	phase, sql, elapsed, ok := r.InFlight()
	if !ok {
		return err
	}
	log.Errorf("FlatSQL engine call FAILED after %s%s — this is the statement the runtime was executing. SQL: %s",
		elapsed.Round(time.Millisecond), phaseSuffix(phase), sql)
	return err
}

// inFlightState is embedded in Runtime (see flatsqlrt.go) — kept here so the
// whole mechanism is one file.
type inFlightState struct {
	inFlight atomic.Pointer[inFlightCall]
	phase    atomic.Pointer[string]
}
