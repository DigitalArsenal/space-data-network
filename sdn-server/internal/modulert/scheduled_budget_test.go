package modulert

// Scheduled-invocation budget selection tests. The behavioral proof that an
// attached budget actually changes guest enforcement lives in
// internal/wasmrt (budget_test.go, using a real WASM hot-loop); here we prove
// the SELECTION seam — the exact ExecBudget modulert grants a scheduled call
// (cron ticker + run-now, both routed through Module.InvokeCron) and how the
// operator override flows in. Running a real guest that burns >30s of
// wall-clock is too slow for CI, so per the task we test the budget choice at
// the seam where WithExecBudget is applied. InvokeCron attaches
// scheduledBudget(); the interactive InvokeMethod / InvokeMethodFrames paths
// attach nothing, so they keep the tight defaultInvokeTimeout.

import (
	"testing"
	"time"
)

// TestInteractiveDefaultsUnchanged pins the interactive budget in effect so a
// future edit that relaxes the fail-closed default trips this test.
func TestInteractiveDefaultsUnchanged(t *testing.T) {
	if defaultInvokeTimeout != 30*time.Second {
		t.Fatalf("defaultInvokeTimeout = %s, want 30s (interactive fail-closed default)", defaultInvokeTimeout)
	}
	if defaultInvokeCostLimit != 4_000_000_000 {
		t.Fatalf("defaultInvokeCostLimit = %d, want 4_000_000_000", defaultInvokeCostLimit)
	}
}

// TestScheduledBudgetDefault: with no operator override, a scheduled call gets
// the built-in 10m wall-clock budget and a fuel budget scaled 20x (10m / 30s).
func TestScheduledBudgetDefault(t *testing.T) {
	mod := &Module{} // no NodeContext override → built-in defaults
	got := mod.scheduledBudget()

	if got.Timeout != 10*time.Minute {
		t.Fatalf("scheduled Timeout = %s, want 10m", got.Timeout)
	}
	if got.Timeout <= defaultInvokeTimeout {
		t.Fatalf("scheduled Timeout %s must exceed the interactive default %s", got.Timeout, defaultInvokeTimeout)
	}
	if got.CostLimit != defaultScheduledInvokeCostLimit {
		t.Fatalf("scheduled CostLimit = %d, want %d", got.CostLimit, defaultScheduledInvokeCostLimit)
	}
	if want := uint(defaultInvokeCostLimit) * 20; got.CostLimit != want {
		t.Fatalf("scheduled CostLimit = %d, want 20x interactive (%d)", got.CostLimit, want)
	}
}

// TestScheduledBudgetOperatorOverride: an operator-configured wall-clock
// budget (NodeContext.ScheduledInvokeTimeout → Module.scheduledInvokeTimeout,
// as NewModule copies it) is honored, and the fuel budget scales
// proportionally with it.
func TestScheduledBudgetOperatorOverride(t *testing.T) {
	mod := &Module{scheduledInvokeTimeout: 20 * time.Minute}
	got := mod.scheduledBudget()

	if got.Timeout != 20*time.Minute {
		t.Fatalf("scheduled Timeout = %s, want operator-configured 20m", got.Timeout)
	}
	// 20m / 30s = 40x the interactive fuel budget.
	if want := uint(defaultInvokeCostLimit) * 40; got.CostLimit != want {
		t.Fatalf("scheduled CostLimit = %d, want %d (fuel scaled ∝ 20m wall-clock)", got.CostLimit, want)
	}
}

// TestScheduledBudgetNonPositiveOverrideFallsBack: a zero/negative configured
// timeout is treated as "unset" and falls back to the built-in default (fail
// safe, never disabling enforcement).
func TestScheduledBudgetNonPositiveOverrideFallsBack(t *testing.T) {
	for _, d := range []time.Duration{0, -5 * time.Minute} {
		mod := &Module{scheduledInvokeTimeout: d}
		got := mod.scheduledBudget()
		if got.Timeout != defaultScheduledInvokeTimeout {
			t.Fatalf("override %s: Timeout = %s, want default %s", d, got.Timeout, defaultScheduledInvokeTimeout)
		}
		if got.CostLimit != defaultScheduledInvokeCostLimit {
			t.Fatalf("override %s: CostLimit = %d, want default %d", d, got.CostLimit, defaultScheduledInvokeCostLimit)
		}
	}
}
