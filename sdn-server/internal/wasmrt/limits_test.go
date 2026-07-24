package wasmrt

// Loop B3 — defensive hardening acceptance tests: WASM guest execution must
// be bounded by wall-clock, fuel/cost, and memory limits, must fail closed
// with a typed error on breach, and must leave the runtime usable for
// subsequent invocations.

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// hotLoopFixtureHex is a hand-assembled WAT module compiled with
// `wat2wasm hotloop.wat -o hotloop.wasm` (wabt 1.0.39). Source:
//
//	(module
//	  (memory (export "memory") 1)
//	  (func (export "hot_loop") (result i32)
//	    (local $i i32)
//	    (local.set $i (i32.const 0))
//	    (loop $continue
//	      (local.set $i (i32.add (local.get $i) (i32.const 1)))
//	      (br $continue)
//	    )
//	    (local.get $i)
//	  )
//	  (func (export "grow") (param $delta i32) (result i32)
//	    (memory.grow (local.get $delta))
//	  )
//	  (func (export "normal") (result i32)
//	    (i32.const 42)
//	  )
//	)
//
// hot_loop never terminates on its own (an infinite br loop with no host
// calls) — it exists purely to exercise the B3 resource-limit enforcement
// paths below. grow exercises the memory ceiling: per the wasm spec,
// memory.grow returns -1 (not a trap) when the requested growth would
// exceed the configured max. The module declares no imports, so it needs
// neither WASI nor any host module registration.
const hotLoopFixtureHex = "0061736d01000000010a026000017f60017f017f0304030001000503010001072504066d656d6f7279020008686f745f6c6f6f7000000467726f770001066e6f726d616c00020a24031601017f410021000340200041016a21000c000b20000b0600200040000b0400412a0b"

func hotLoopFixtureWasm(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(hotLoopFixtureHex)
	if err != nil {
		t.Fatalf("decode hot-loop fixture hex: %v", err)
	}
	return b
}

func TestHasFunctionReportsExportPresence(t *testing.T) {
	mod, err := NewModule(hotLoopFixtureWasm(t))
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	if !mod.HasFunction("normal") {
		t.Fatal("HasFunction did not report an exported function")
	}
	if mod.HasFunction("missing") {
		t.Fatal("HasFunction reported a function that is not exported")
	}
}

// runWithSafetyValve calls fn on a goroutine and fails the test — rather
// than hanging the whole `go test` run — if it does not return within
// margin. It exists so that a broken B3 enforcement mechanism surfaces as a
// clear test failure instead of an indefinite hang.
func runWithSafetyValve(t *testing.T, margin time.Duration, fn func() ([]interface{}, error)) ([]interface{}, error, time.Duration) {
	t.Helper()
	type result struct {
		values []interface{}
		err    error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		values, err := fn()
		done <- result{values: values, err: err}
	}()
	select {
	case r := <-done:
		return r.values, r.err, time.Since(start)
	case <-time.After(margin):
		t.Fatalf("Execute did not return within safety margin %s — resource-limit enforcement appears to hang the runtime", margin)
		return nil, nil, time.Since(start)
	}
}

// TestHotLoopFuelExhaustion: WithCostLimit alone must deterministically
// abort an infinite guest loop, report the typed ErrFuelExhausted error,
// and leave the runtime usable for a subsequent normal invocation.
func TestHotLoopFuelExhaustion(t *testing.T) {
	mod, err := NewModule(hotLoopFixtureWasm(t),
		WithCostLimit(5_000_000),
		WithExecTimeout(10*time.Second), // generous backstop only
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	_, err, elapsed := runWithSafetyValve(t, 15*time.Second, func() ([]interface{}, error) {
		return mod.Execute("hot_loop")
	})
	if err == nil {
		t.Fatal("hot_loop: expected fuel-exhaustion error, got nil (infinite loop returned a value?)")
	}
	if !errors.Is(err, ErrFuelExhausted) {
		t.Fatalf("hot_loop error = %v, want errors.Is(_, ErrFuelExhausted)", err)
	}
	if !IsResourceLimitExceeded(err) {
		t.Fatalf("IsResourceLimitExceeded(%v) = false, want true", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hot_loop took %s to hit the fuel limit — expected well under the 10s backstop", elapsed)
	}
	t.Logf("hot_loop aborted by fuel limit in %s: %v", elapsed, err)

	// The runtime must still be usable for a normal subsequent invocation.
	res, err := mod.Execute("normal")
	if err != nil {
		t.Fatalf("normal invocation after fuel exhaustion: %v", err)
	}
	if got := ToInt32(res[0]); got != 42 {
		t.Fatalf("normal() = %d, want 42", got)
	}
}

// TestHotLoopWallClockTimeout: WithExecTimeout alone (no fuel limit) must
// hard-interrupt an infinite guest loop via WasmEdge's async-execute +
// cancel path, report the typed ErrExecutionTimeout error, and leave the
// runtime usable afterward.
func TestHotLoopWallClockTimeout(t *testing.T) {
	mod, err := NewModule(hotLoopFixtureWasm(t),
		WithExecTimeout(300*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	_, err, elapsed := runWithSafetyValve(t, 15*time.Second, func() ([]interface{}, error) {
		return mod.Execute("hot_loop")
	})
	if err == nil {
		t.Fatal("hot_loop: expected wall-clock timeout error, got nil")
	}
	if !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("hot_loop error = %v, want errors.Is(_, ErrExecutionTimeout)", err)
	}
	if !IsResourceLimitExceeded(err) {
		t.Fatalf("IsResourceLimitExceeded(%v) = false, want true", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hot_loop took %s to be interrupted — expected close to the 300ms budget", elapsed)
	}
	t.Logf("hot_loop hard-interrupted by wall-clock timeout in %s: %v", elapsed, err)

	res, err := mod.Execute("normal")
	if err != nil {
		t.Fatalf("normal invocation after wall-clock interrupt: %v", err)
	}
	if got := ToInt32(res[0]); got != 42 {
		t.Fatalf("normal() = %d, want 42", got)
	}
}

// TestExecuteContextNarrowsTimeout: a caller-supplied ctx deadline shorter
// than the module's configured WithExecTimeout default must win — this is
// the mechanism modulert.Module.InvokeMethodFrames relies on to bind a
// request-scoped timeout to the guest call.
func TestExecuteContextNarrowsTimeout(t *testing.T) {
	mod, err := NewModule(hotLoopFixtureWasm(t),
		WithExecTimeout(30*time.Second),
	)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err, elapsed := runWithSafetyValve(t, 15*time.Second, func() ([]interface{}, error) {
		return mod.ExecuteContext(ctx, "hot_loop")
	})
	if !errors.Is(err, ErrExecutionTimeout) {
		t.Fatalf("hot_loop error = %v, want errors.Is(_, ErrExecutionTimeout)", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hot_loop took %s — the 300ms ctx deadline should have won over the 30s module default", elapsed)
	}
}

// TestMemoryCeiling: a guest that grows memory beyond the host-configured
// page cap (WithMaxMemoryPages) fails cleanly — memory.grow returns -1 per
// the wasm spec — without the host erroring or crashing.
func TestMemoryCeiling(t *testing.T) {
	mod, err := NewModule(hotLoopFixtureWasm(t), WithMaxMemoryPages(2))
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	// Starts at 1 page (the fixture's declared min); growing by 1 stays
	// within the 2-page host cap and must succeed, returning the previous
	// page count.
	res, err := mod.Execute("grow", int32(1))
	if err != nil {
		t.Fatalf("grow(1): %v", err)
	}
	if got := ToInt32(res[0]); got != 1 {
		t.Fatalf("grow(1) = %d, want 1 (previous page count)", got)
	}

	// Now at the 2-page cap. Growing further must fail cleanly.
	res, err = mod.Execute("grow", int32(1))
	if err != nil {
		t.Fatalf("grow(1) at cap: %v", err)
	}
	if got := ToInt32(res[0]); got != -1 {
		t.Fatalf("grow(1) at cap = %d, want -1 (growth refused)", got)
	}

	// The module must remain fully usable and report the capped size.
	stats, err := mod.MemoryStats()
	if err != nil {
		t.Fatalf("MemoryStats: %v", err)
	}
	if stats.Pages != 2 {
		t.Fatalf("MemoryStats.Pages = %d, want 2", stats.Pages)
	}
	if stats.MaxPages != 2 {
		t.Fatalf("MemoryStats.MaxPages = %d, want 2", stats.MaxPages)
	}
}

// TestNoLimitsConfiguredIsUnchanged: a Module created without
// WithExecTimeout/WithCostLimit (every existing wasmrt caller prior to B3)
// must behave exactly as before — no new enforcement kicks in silently.
func TestNoLimitsConfiguredIsUnchanged(t *testing.T) {
	mod, err := NewModule(hotLoopFixtureWasm(t))
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	defer mod.Release()

	res, err := mod.Execute("normal")
	if err != nil {
		t.Fatalf("normal(): %v", err)
	}
	if got := ToInt32(res[0]); got != 42 {
		t.Fatalf("normal() = %d, want 42", got)
	}
}
