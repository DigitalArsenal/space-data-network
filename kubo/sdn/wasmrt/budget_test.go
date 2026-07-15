package wasmrt

// Per-call ExecBudget override acceptance tests. A trusted HOST caller (e.g.
// modulert's scheduled/cron seam) must be able to RAISE the wall-clock/fuel
// budget for one Execute call above the module's tight configured default,
// without weakening that default for calls that don't opt in. A guest cannot
// reach this (Go context values are host-only), and a ctx deadline still
// narrows the resulting wall-clock budget. These reuse the hotLoopFixtureWasm
// / runWithSafetyValve helpers from limits_test.go (same package).

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestWithExecBudgetRoundTrip: the context helpers plumb an ExecBudget through
// unchanged, and a bare context carries none.
func TestWithExecBudgetRoundTrip(t *testing.T) {
	if _, ok := execBudgetFrom(context.Background()); ok {
		t.Fatal("bare context reported an ExecBudget; want none")
	}
	want := ExecBudget{Timeout: 7 * time.Minute, CostLimit: 123}
	ctx := WithExecBudget(context.Background(), want)
	got, ok := execBudgetFrom(ctx)
	if !ok {
		t.Fatal("execBudgetFrom(ctx) reported no budget after WithExecBudget")
	}
	if got != want {
		t.Fatalf("ExecBudget round-trip = %+v, want %+v", got, want)
	}
}

// TestExecBudgetRaisesWallClockBudget is the core proof: with a tight 300ms
// module default, an interactive call (no budget on ctx) is interrupted at
// ~300ms, while a call carrying a larger per-call ExecBudget runs to ~2s
// before the interrupt — i.e. the host RAISED the ceiling above the module
// default. (A ctx deadline alone could never do this: effectiveTimeout only
// narrows, never raises, the default.)
func TestExecBudgetRaisesWallClockBudget(t *testing.T) {
	mod, err := NewModule(hotLoopFixtureWasm(t),
		WithExecTimeout(300*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	// Interactive: no ExecBudget on the context → tight 300ms default holds.
	_, err, interactiveElapsed := runWithSafetyValve(t, 15*time.Second, func() ([]interface{}, error) {
		return mod.ExecuteContext(context.Background(), "hot_loop")
	})
	if !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("interactive hot_loop error = %v, want ErrExecutionTimeout", err)
	}
	if interactiveElapsed > 1*time.Second {
		t.Fatalf("interactive hot_loop took %s — the 300ms default should have bitten", interactiveElapsed)
	}

	// Scheduled: a 2s per-call ExecBudget replaces the 300ms default.
	ctx := WithExecBudget(context.Background(), ExecBudget{Timeout: 2 * time.Second})
	_, err, scheduledElapsed := runWithSafetyValve(t, 15*time.Second, func() ([]interface{}, error) {
		return mod.ExecuteContext(ctx, "hot_loop")
	})
	if !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("scheduled hot_loop error = %v, want ErrExecutionTimeout", err)
	}
	if scheduledElapsed < 1500*time.Millisecond {
		t.Fatalf("scheduled hot_loop took only %s — the 2s ExecBudget should have raised the ceiling above the 300ms default", scheduledElapsed)
	}
	t.Logf("interactive interrupted in %s (300ms default), scheduled in %s (2s budget)", interactiveElapsed, scheduledElapsed)

	// Runtime still usable afterward.
	res, err := mod.Execute("normal")
	if err != nil {
		t.Fatalf("normal after budgeted interrupts: %v", err)
	}
	if got := ToInt32(res[0]); got != 42 {
		t.Fatalf("normal() = %d, want 42", got)
	}
}

// TestExecBudgetStillNarrowedByCtxDeadline proves ExecuteContext semantics are
// preserved (requirement 4): even under a large per-call ExecBudget, a sooner
// ctx deadline wins. The ~700ms result (not ~300ms) also confirms the budget
// raised the base above the module default before the deadline narrowed it.
func TestExecBudgetStillNarrowedByCtxDeadline(t *testing.T) {
	mod, err := NewModule(hotLoopFixtureWasm(t),
		WithExecTimeout(300*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	base := WithExecBudget(context.Background(), ExecBudget{Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(base, 700*time.Millisecond)
	defer cancel()

	_, err, elapsed := runWithSafetyValve(t, 15*time.Second, func() ([]interface{}, error) {
		return mod.ExecuteContext(ctx, "hot_loop")
	})
	if !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("hot_loop error = %v, want ErrExecutionTimeout", err)
	}
	if elapsed < 500*time.Millisecond {
		t.Fatalf("hot_loop took only %s — the 5s ExecBudget should have raised the base above the 300ms module default", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("hot_loop took %s — the 700ms ctx deadline should have won over the 5s ExecBudget", elapsed)
	}
	t.Logf("interrupted in %s: 700ms ctx deadline correctly narrowed the 5s budget", elapsed)
}

// TestExecBudgetRaisesCostBudget proves the fuel side of the override
// deterministically by OUTCOME: with a small 5M fuel default and a 1s
// wall-clock backstop, an interactive hot_loop is aborted by fuel; the same
// call carrying a huge per-call fuel ExecBudget outlives the fuel limit and is
// instead aborted by the 1s wall-clock — the abort reason flips, so the
// per-call cost override provably took effect.
func TestExecBudgetRaisesCostBudget(t *testing.T) {
	mod, err := NewModule(hotLoopFixtureWasm(t),
		WithCostLimit(5_000_000),
		WithExecTimeout(1*time.Second),
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	_, err, _ = runWithSafetyValve(t, 15*time.Second, func() ([]interface{}, error) {
		return mod.ExecuteContext(context.Background(), "hot_loop")
	})
	if !errors.Is(err, ErrFuelExhausted) {
		t.Fatalf("interactive hot_loop error = %v, want ErrFuelExhausted (5M fuel should bite before the 1s backstop)", err)
	}

	// A per-call fuel budget far larger than could be spent in 1s → the
	// wall-clock backstop must be what aborts the guest now.
	ctx := WithExecBudget(context.Background(), ExecBudget{CostLimit: 100_000_000_000})
	_, err, _ = runWithSafetyValve(t, 15*time.Second, func() ([]interface{}, error) {
		return mod.ExecuteContext(ctx, "hot_loop")
	})
	if !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("scheduled hot_loop error = %v, want ErrExecutionTimeout (raised fuel budget should outlast 1s, leaving wall-clock to abort)", err)
	}
}
