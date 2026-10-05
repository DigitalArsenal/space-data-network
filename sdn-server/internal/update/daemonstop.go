package update

// STOPPING THE DAEMON BEFORE THE SWAP, AND KNOWING THAT IT STOPPED.
//
// The helper used to ask the daemon to shut down, sleep two seconds, and swap.
// A daemon's shutdown is not bounded by two seconds — 15 of 22 stops on
// 2026-08-08/09 hung for the full 90 s systemd allows — so the swap raced the
// process it was replacing: renames under a live binary, a supervisor
// respawning the old build mid-swap, two daemons fighting over the store's
// single-writer lock (expert review PLAT-01, SD-5).
//
// The ladder below replaces the sleep with the only acceptable evidence, the
// pid exiting, and bounds the wait so a wedged daemon cannot hold an update
// forever:
//
//	rung 0  the graceful stop (the update shutdown handshake, a supervisor
//	        stop, or SIGTERM), then wait up to Grace for the pid to exit;
//	rung 1  escalate: a stop through the supervisor connector when there is
//	        one, else SIGTERM; wait up to Escalated;
//	rung 2  SIGKILL; wait up to Killed;
//	        and if the pid is STILL there, give up: nothing is swapped.
//
// Grace defaults to 90 s because the daemon's own shutdown watchdog kills it
// at 75 s (cmd/spacedatanetwork shutdownWatchdogTimeout) and systemd's
// TimeoutStopSec on the fleet is 90 s: a daemon that is stopping at all exits
// inside it. Every rung is a deploy-ledger line, written before the rung acts.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Default stop bounds; see the ladder above.
const (
	DefaultStopGrace     = 90 * time.Second
	DefaultStopEscalated = 30 * time.Second
	DefaultStopKilled    = 10 * time.Second
)

// stopPollInterval is how often a non-child pid is checked for exit.
const stopPollInterval = 100 * time.Millisecond

// StopBounds bound the rungs of the stop ladder. Zero fields take the
// defaults.
type StopBounds struct {
	// Grace is the wait after the graceful stop, before escalating.
	Grace time.Duration
	// Escalated is the wait after the escalation, before SIGKILL.
	Escalated time.Duration
	// Killed is the wait after SIGKILL, before giving up.
	Killed time.Duration
}

func (b StopBounds) withDefaults() StopBounds {
	if b.Grace <= 0 {
		b.Grace = DefaultStopGrace
	}
	if b.Escalated <= 0 {
		b.Escalated = DefaultStopEscalated
	}
	if b.Killed <= 0 {
		b.Killed = DefaultStopKilled
	}
	return b
}

// StopLadder stops one daemon process and records every rung in the deploy
// ledger of the bundle at Paths.
type StopLadder struct {
	Paths Paths
	// UpdateID and Version name the update the stop is for, on every line.
	UpdateID string
	Version  string
	// PID is the daemon process. Zero means no process is known: Run makes
	// the graceful request, records that it could not wait, and returns.
	PID int
	// Exited, when non-nil, is closed once the process has exited and been
	// reaped — a child of this process. Nil polls the pid, treating a zombie
	// as exited.
	Exited <-chan struct{}
	// Request makes the graceful stop. Nil when it was already made: the
	// update shutdown handshake, which is how the helper learns the pid.
	Request func() error
	// How describes the graceful stop for the ledger and the output.
	How string
	// Escalate is rung 1: a stop through the supervisor connector. Nil sends
	// SIGTERM.
	Escalate    func() error
	EscalateHow string
	Bounds      StopBounds
	// Out receives one key=value line per rung. Nil discards them.
	Out io.Writer
}

// StopResult reports how the process stopped.
type StopResult struct {
	// Escalated is true when rung 1 was taken, Killed when rung 2 was.
	Escalated bool
	Killed    bool
	// Elapsed is the time from the graceful stop to the observed exit.
	Elapsed time.Duration
}

// DaemonStopError is the ladder's refusal: the process outlived every rung,
// so nothing may be swapped under it.
type DaemonStopError struct {
	PID     int
	Elapsed time.Duration
}

func (e *DaemonStopError) Error() string {
	return fmt.Sprintf("daemon pid %d did not exit within %s, through the graceful stop, the escalation and SIGKILL: nothing was swapped",
		e.PID, e.Elapsed.Round(time.Second))
}

// Run takes the ladder until the process exits or every rung is spent.
func (l StopLadder) Run(ctx context.Context) (StopResult, error) {
	var result StopResult
	out := l.Out
	if out == nil {
		out = io.Discard
	}
	bounds := l.Bounds.withDefaults()
	how := l.How
	if how == "" {
		how = "graceful stop"
	}

	if l.PID <= 0 {
		if err := l.record("stop-requested", how+"; no daemon pid is known, so there is no exit to wait for"); err != nil {
			return result, err
		}
		if l.Request != nil {
			if err := l.Request(); err != nil {
				fmt.Fprintf(out, "daemon_stop=request-failed error=%q\n", err.Error())
			}
		}
		return result, nil
	}

	// The first line says whether this ladder makes the graceful request or
	// the caller already made it (the handshake, which named the pid) and
	// recorded that.
	action, made := "stop-requested", ""
	if l.Request == nil {
		action, made = "stop-waiting", " made"
	}
	if err := l.record(action, fmt.Sprintf("%s%s; waiting up to %s for pid %d to exit", how, made, bounds.Grace, l.PID)); err != nil {
		return result, err
	}
	start := time.Now()
	requested := true
	if l.Request != nil {
		if err := l.Request(); err != nil {
			// A stop that could not be requested is not a stop to wait for:
			// go straight to the escalation.
			fmt.Fprintf(out, "daemon_stop=request-failed pid=%d error=%q\n", l.PID, err.Error())
			requested = false
		}
	}
	fmt.Fprintf(out, "daemon_stop=waiting pid=%d how=%q bound=%s\n", l.PID, how, bounds.Grace)
	if requested {
		exited, err := l.wait(ctx, bounds.Grace)
		if err != nil {
			return result, err
		}
		if exited {
			return l.exited(out, start, result)
		}
	}

	result.Escalated = true
	escalateHow := l.EscalateHow
	escalate := l.Escalate
	if escalate == nil {
		escalateHow = "SIGTERM"
		pid := l.PID
		escalate = func() error { return terminateProcess(pid) }
	}
	if err := l.record("stop-escalated", fmt.Sprintf("pid %d still running %s after the graceful stop: %s; waiting up to %s",
		l.PID, time.Since(start).Round(time.Second), escalateHow, bounds.Escalated)); err != nil {
		return result, err
	}
	fmt.Fprintf(out, "daemon_stop=escalated pid=%d via=%q bound=%s\n", l.PID, escalateHow, bounds.Escalated)
	if err := escalate(); err != nil {
		fmt.Fprintf(out, "daemon_stop=escalation-failed pid=%d error=%q\n", l.PID, err.Error())
	}
	exited, err := l.wait(ctx, bounds.Escalated)
	if err != nil {
		return result, err
	}
	if exited {
		return l.exited(out, start, result)
	}

	result.Killed = true
	if err := l.record("stop-killed", fmt.Sprintf("pid %d still running %s after the graceful stop and the escalation: SIGKILL; waiting up to %s",
		l.PID, time.Since(start).Round(time.Second), bounds.Killed)); err != nil {
		return result, err
	}
	fmt.Fprintf(out, "daemon_stop=killed pid=%d bound=%s\n", l.PID, bounds.Killed)
	if err := killProcess(l.PID); err != nil {
		fmt.Fprintf(out, "daemon_stop=kill-failed pid=%d error=%q\n", l.PID, err.Error())
	}
	exited, err = l.wait(ctx, bounds.Killed)
	if err != nil {
		return result, err
	}
	if exited {
		return l.exited(out, start, result)
	}

	result.Elapsed = time.Since(start)
	stopErr := &DaemonStopError{PID: l.PID, Elapsed: result.Elapsed}
	_ = l.record("stop-failed", stopErr.Error())
	fmt.Fprintf(out, "daemon_stop=failed pid=%d after=%s\n", l.PID, result.Elapsed.Round(time.Millisecond))
	return result, stopErr
}

func (l StopLadder) exited(out io.Writer, start time.Time, result StopResult) (StopResult, error) {
	result.Elapsed = time.Since(start)
	fmt.Fprintf(out, "daemon_exited pid=%d after=%s\n", l.PID, result.Elapsed.Round(time.Millisecond))
	if err := l.record("daemon-exited", fmt.Sprintf("pid %d exited %s after the stop was requested", l.PID, result.Elapsed.Round(time.Millisecond))); err != nil {
		return result, err
	}
	return result, nil
}

// wait reports whether the process exited within d.
func (l StopLadder) wait(ctx context.Context, d time.Duration) (bool, error) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	if l.Exited != nil {
		select {
		case <-l.Exited:
			return true, nil
		case <-timer.C:
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	ticker := time.NewTicker(stopPollInterval)
	defer ticker.Stop()
	for {
		if !processRunning(l.PID) {
			return true, nil
		}
		select {
		case <-timer.C:
			return !processRunning(l.PID), nil
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l StopLadder) record(action, reason string) error {
	return RecordDeployLedgerEntry(l.Paths, DeployLedgerEntry{
		Action:    action,
		UpdateID:  l.UpdateID,
		Version:   l.Version,
		DaemonPID: l.PID,
		Reason:    reason,
	})
}

// processRunning (per platform) reports whether pid is a live process. A
// zombie — exited and not yet collected by its parent — is not: it holds no
// files and no locks and will never serve again.

// errNoProcess is returned by the platform helpers for a pid <= 0.
var errNoProcess = errors.New("no process")
