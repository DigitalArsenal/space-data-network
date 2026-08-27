package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// THE BUDGET IS THE ENGINE'S, NOT A NUMBER PICKED HERE. A phase scored against
// anything other than the budget the runtime actually enforces would report a
// margin the node does not have — and a phase must be VISIBLE while it runs,
// because the whole investigation turned on being able to ask the runtime what
// it was doing when a call went past the budget.
//
// ONE store for both facts on purpose: opening the engine costs ~12 s and this
// package's test binary already runs close to its 30-minute cap.
func TestBootPhaseBudgetIsTheEnginesAndStampsForItsDuration(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	engine, _ := store.EngineRuntime()
	if engine == nil {
		t.Fatal("store has no engine runtime")
	}
	budget := newBootPhaseBudget(engine)
	if budget.budget != engine.ExecBudget() {
		t.Fatalf("phase budget %s, engine per-call budget %s — they must be the same number",
			budget.budget, engine.ExecBudget())
	}
	if budget.budget <= 0 {
		t.Fatalf("phase budget is %s; a boot with no budget cannot warn about crossing one", budget.budget)
	}

	if got := engine.Phase(); got != "" {
		t.Fatalf("engine phase = %q before any phase started", got)
	}
	end := budget.phase("boot: unit-test phase")
	if got := engine.Phase(); got != "boot: unit-test phase" {
		t.Fatalf("engine phase = %q during the phase, want it stamped", got)
	}
	end()
	if got := engine.Phase(); got != "" {
		t.Fatalf("engine phase = %q after the phase ended, want cleared", got)
	}
	if budget.total <= 0 {
		t.Fatal("phase accounting recorded no elapsed time")
	}
}

// The warn threshold must be strictly inside the budget: a boot phase that only
// warns once it has ALREADY crossed cannot warn at all — the engine is poisoned
// by then and the process is not answering.
func TestBootPhaseWarnThresholdIsInsideTheBudget(t *testing.T) {
	if bootPhaseWarnFraction <= 0 || bootPhaseWarnFraction >= 1 {
		t.Fatalf("bootPhaseWarnFraction = %v, want a fraction strictly inside (0,1)", bootPhaseWarnFraction)
	}
	warnAt := time.Duration(float64(flatsqlrt.DefaultEngineExecTimeout) * bootPhaseWarnFraction)
	if warnAt >= flatsqlrt.DefaultEngineExecTimeout {
		t.Fatalf("warn at %s, budget %s", warnAt, flatsqlrt.DefaultEngineExecTimeout)
	}
}

// The page cache is a shipped default, not an accident: 0 must mean "leave
// SQLite alone" and a garbage override must not silently disable it.
func TestEnginePageCacheResolution(t *testing.T) {
	t.Setenv(enginePageCacheEnv, "")
	if got := resolveEnginePageCacheMiB(); got != defaultEnginePageCacheMiB {
		t.Fatalf("unset override = %d MiB, want the %d MiB default", got, defaultEnginePageCacheMiB)
	}
	t.Setenv(enginePageCacheEnv, "0")
	if got := resolveEnginePageCacheMiB(); got != 0 {
		t.Fatalf("explicit 0 = %d MiB, want 0 (SQLite's own default)", got)
	}
	t.Setenv(enginePageCacheEnv, "not-a-number")
	if got := resolveEnginePageCacheMiB(); got != defaultEnginePageCacheMiB {
		t.Fatalf("garbage override = %d MiB, want the %d MiB default", got, defaultEnginePageCacheMiB)
	}
	t.Setenv(enginePageCacheEnv, "-5")
	if got := resolveEnginePageCacheMiB(); got != defaultEnginePageCacheMiB {
		t.Fatalf("negative override = %d MiB, want the %d MiB default", got, defaultEnginePageCacheMiB)
	}
	t.Setenv(enginePageCacheEnv, "64")
	if got := resolveEnginePageCacheMiB(); got != 64 {
		t.Fatalf("override 64 = %d MiB", got)
	}
}
