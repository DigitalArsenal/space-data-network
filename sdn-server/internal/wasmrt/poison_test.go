package wasmrt

// Regression gate for `sdn-wasmrt-exec-no-deadline` (Hermes, 2026-07-30).
//
// THE DEFECT THESE TESTS PIN, in the exact two shapes it was observed in
// production. Both are host-survival properties: the guest is NOT saved in
// either case, but the HOST must always keep running.
//
//  1. host-01 / sdn.spaceaware.io — RE-ENTRY OF A TRAPPED VM.
//     The flatsql engine trapped ("unreachable" in flatsql_query_params) and
//     database/sql's cleanup then called Rollback, which issued another query
//     into the SAME trapped VM. That call never returned: it entered a
//     five-instruction infinite loop in the AOT artifact's corrupted allocator
//     inside an unpreemptible cgo call. Because it held the FlatSQLStore lock,
//     and that lock is reachable from the libp2p connection gater, ALL peering
//     stopped — /p2p/<peerid> returned 502 and spaceaware.io/beta lost its
//     catalogue entirely. Deterministic, every boot.
//
//  2. host-02 / celestrak.eth — AN UNBOUNDED DEDICATED-THREAD CALL.
//     One WasmEdge_VMExecuteRegistered ran for 612 MINUTES, pinning the box's
//     only vCPU at 99% with zero flow activity and nothing alerting.
//
// Neither could be fixed with a context deadline: the cgo call is not
// preemptible by the Go runtime, so no context, timer or watchdog can unwind it.
// What CAN be bounded is the caller, and what MUST be prevented is re-entry.

import (
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// trapFixtureHex is `wat2wasm trapmod.wat` (wabt via homebrew). Source:
//
//	(module
//	  (memory (export "memory") 1)
//	  (func (export "trap") (result i32) (unreachable))
//	  (func (export "hot_loop") (result i32) ... infinite br loop ...)
//	  (func (export "normal") (result i32) (i32.const 42))
//	  (func (export "malloc") (param i32) (result i32) (i32.const 1024))
//	  (func (export "free") (param i32))
//	)
//
// `trap` reproduces shape (1) — a real wasm trap, the same class as the
// production `unreachable`. `hot_loop` reproduces shape (2) — a guest that
// never returns and makes no host calls, so nothing but the runtime itself
// could ever interrupt it. malloc/free exist so the allocator paths
// (Allocate/Deallocate) can be exercised against a poisoned module.
const trapFixtureHex = "0061736d01000000010e036000017f60017f017f60017f0003060500000001020503010001073506066d656d6f727902000474726170000008686f745f6c6f6f700001066e6f726d616c0002066d616c6c6f630003046672656500040a2a050300000b1601017f410021000340200041016a21000c000b20000b0400412a0b05004180080b02000b"

func trapFixtureWasm(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(trapFixtureHex)
	if err != nil {
		t.Fatalf("decode trap fixture hex: %v", err)
	}
	return b
}

// TestTrapPoisonsModuleAndRefusesReEntry is the host-01 outage, reduced.
//
// The critical assertion is the SECOND call: before this fix a post-trap entry
// was dispatched to the guest and hung forever inside cgo. It must now be
// refused, instantly, with a typed error — so a caller unwinding from the trap
// (sql.Tx.Rollback in production) completes its cleanup and releases whatever
// host locks it holds.
func TestTrapPoisonsModuleAndRefusesReEntry(t *testing.T) {
	mod, err := NewModule(trapFixtureWasm(t))
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	if mod.Poisoned() {
		t.Fatal("a freshly created module reports poisoned")
	}

	// A normal call must work before the trap, so the test cannot pass
	// vacuously on a module that never ran at all.
	if res, err := mod.Execute("normal"); err != nil {
		t.Fatalf("normal() before trap: %v", err)
	} else if got := ToInt32(res[0]); got != 42 {
		t.Fatalf("normal() = %d, want 42", got)
	}

	if _, err := mod.Execute("trap"); err == nil {
		t.Fatal("trap(): expected a wasm trap error, got nil")
	}

	if !mod.Poisoned() {
		t.Fatal("module is not poisoned after a trap — every later entry will be dispatched into a corrupted guest")
	}
	if mod.PoisonCause() == nil {
		t.Fatal("PoisonCause() is nil after a trap; the original trap must be preserved for diagnosis")
	}

	// THE REGRESSION: re-entry must fail fast, not execute and not hang.
	_, err, elapsed := runWithSafetyValve(t, 10*time.Second, func() ([]interface{}, error) {
		return mod.Execute("normal")
	})
	if err == nil {
		t.Fatal("re-entry into a trapped module SUCCEEDED — the poison gate is not being consulted")
	}
	if !errors.Is(err, ErrModulePoisoned) {
		t.Fatalf("re-entry error = %v, want errors.Is(_, ErrModulePoisoned)", err)
	}
	if !IsPoisoned(err) {
		t.Fatalf("IsPoisoned(%v) = false, want true", err)
	}
	if elapsed > time.Second {
		t.Fatalf("re-entry took %s; a poisoned module must be refused without entering the guest at all", elapsed)
	}
	t.Logf("re-entry refused in %s: %v", elapsed, err)
}

// TestPoisonedModuleRefusesAllocatorEntries pins the paths that are reached by
// `defer`, which is what made the original wedge so hard to see: the trapping
// call's own deferred cleanup called free() on the guest whose allocator the
// trap had just corrupted, and that free never returned — so the trap error
// never even propagated to the caller.
func TestPoisonedModuleRefusesAllocatorEntries(t *testing.T) {
	mod, err := NewModule(trapFixtureWasm(t))
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	if _, err := mod.Execute("trap"); err == nil {
		t.Fatal("trap(): expected a wasm trap error, got nil")
	}

	// Allocation must be refused rather than dispatched.
	_, err, elapsed := runWithSafetyValve(t, 10*time.Second, func() ([]interface{}, error) {
		ptr, aerr := mod.AllocateSize(64)
		return []interface{}{ptr}, aerr
	})
	if err == nil {
		t.Fatal("AllocateSize on a poisoned module succeeded; it must be refused")
	}
	if !errors.Is(err, ErrModulePoisoned) {
		t.Fatalf("AllocateSize error = %v, want errors.Is(_, ErrModulePoisoned)", err)
	}
	if elapsed > time.Second {
		t.Fatalf("AllocateSize took %s on a poisoned module; must be immediate", elapsed)
	}

	// Deallocate returns nothing, so the property under test is purely that it
	// does not hang (it must not enter the guest).
	done := make(chan struct{})
	go func() {
		mod.Deallocate(1024)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Deallocate on a poisoned module did not return — the deferred-free re-entry wedge is back")
	}
}

// TestDedicatedThreadCallIsBoundedAndPoisons is the host-02 outage, reduced.
//
// A dedicated-thread module keeps the plain synchronous WasmEdge call (its
// nested-AOT thread-affinity invariant forbids the async-cancel path), so the
// GUEST cannot be interrupted. Before this fix that meant the Go caller waited
// on it forever; 612 minutes was the measured record. The caller must now
// return on its budget.
//
// NOTE: this test deliberately leaks one spinning OS thread for the remainder
// of the test binary's life — that is the documented, accepted cost of escaping
// an uninterruptible runtime, and asserting it is the whole point.
func TestDedicatedThreadCallIsBoundedAndPoisons(t *testing.T) {
	mod, err := NewModule(trapFixtureWasm(t),
		WithDedicatedThread(),
		WithExecTimeout(400*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release() // must not block on the abandoned worker

	if res, err := mod.Execute("normal"); err != nil {
		t.Fatalf("normal() on a dedicated-thread module: %v", err)
	} else if got := ToInt32(res[0]); got != 42 {
		t.Fatalf("normal() = %d, want 42", got)
	}

	_, err, elapsed := runWithSafetyValve(t, 20*time.Second, func() ([]interface{}, error) {
		return mod.Execute("hot_loop")
	})
	if err == nil {
		t.Fatal("hot_loop on a dedicated thread returned a value — the fixture is wrong")
	}
	if !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("hot_loop error = %v, want errors.Is(_, ErrExecutionTimeout)", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("dedicated-thread call took %s to abandon a 400ms budget — the caller is still unbounded", elapsed)
	}
	t.Logf("dedicated-thread runaway abandoned in %s: %v", elapsed, err)

	if !mod.Poisoned() {
		t.Fatal("module not poisoned after abandoning its execution thread; the next call would block forever on the unbuffered handoff")
	}

	// THE SECOND HALF OF THE FIX: the worker is gone, so a further call must be
	// refused rather than sent to a channel nobody is reading.
	_, err, elapsed = runWithSafetyValve(t, 10*time.Second, func() ([]interface{}, error) {
		return mod.Execute("normal")
	})
	if err == nil {
		t.Fatal("call after thread abandonment succeeded; impossible unless the gate is missing")
	}
	if !errors.Is(err, ErrModulePoisoned) {
		t.Fatalf("post-abandonment error = %v, want errors.Is(_, ErrModulePoisoned)", err)
	}
	if elapsed > time.Second {
		t.Fatalf("post-abandonment call took %s; must be refused immediately", elapsed)
	}
}

// TestReleaseDoesNotBlockOnAbandonedThread pins the shutdown half: Release
// normally closes the exec channel, joins the worker and frees the VM. With the
// worker still inside an uninterruptible guest call, joining would hang and
// freeing the VM under live C++ code would segfault the whole daemon. Release
// must instead leak and return.
func TestReleaseDoesNotBlockOnAbandonedThread(t *testing.T) {
	mod, err := NewModule(trapFixtureWasm(t),
		WithDedicatedThread(),
		WithExecTimeout(300*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}

	if _, err := mod.Execute("hot_loop"); !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("hot_loop error = %v, want ErrExecutionTimeout", err)
	}

	done := make(chan struct{})
	go func() {
		mod.Release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Release blocked on the abandoned execution thread — shutdown would hang instead of leaking")
	}
}

// TestFuelExhaustionDoesNotPoison guards the boundary from the other side: a
// fuel/cost abort is a DEFINED stop at an instruction boundary and WasmEdge
// documents the VM as reusable afterward. Poisoning on it would throw away a
// healthy engine — turning a bounded query into an outage of its own, which is
// exactly the regression a nervous fix would introduce here.
func TestFuelExhaustionDoesNotPoison(t *testing.T) {
	mod, err := NewModule(trapFixtureWasm(t),
		WithCostLimit(2_000_000),
		WithExecTimeout(10*time.Second),
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	_, err, _ = runWithSafetyValve(t, 20*time.Second, func() ([]interface{}, error) {
		return mod.Execute("hot_loop")
	})
	if !errors.Is(err, ErrFuelExhausted) {
		t.Fatalf("hot_loop error = %v, want errors.Is(_, ErrFuelExhausted)", err)
	}
	if mod.Poisoned() {
		t.Fatal("a fuel-limit abort poisoned the module; the VM is documented reusable and must stay usable")
	}
	if res, err := mod.Execute("normal"); err != nil {
		t.Fatalf("normal() after fuel exhaustion: %v", err)
	} else if got := ToInt32(res[0]); got != 42 {
		t.Fatalf("normal() = %d, want 42", got)
	}
}
