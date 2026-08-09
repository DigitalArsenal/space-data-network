package flatsqlrt

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// controlShapeSQL builds a result of the shape that measured a flat ~0.9 s on
// host-01: a small, indexed control table (30 rows) with a normal number of
// columns. It is a recursive CTE rather than a real table on purpose — the
// whole point of this task's finding is that the ROWS are not the work and the
// SQL is not the work, so the fixture deliberately removes storage from the
// measurement and leaves only result materialization.
func controlShapeSQL(rows, cols int) string {
	sql := "WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < " +
		strconv.Itoa(rows) + ") SELECT n AS c0"
	for c := 1; c < cols; c++ {
		sql += fmt.Sprintf(", 'value-'||n||'-%d' AS c%d", c, c)
	}
	return sql + " FROM seq"
}

// TestQueryCostsOneExecThreadDispatch is the regression guard for this task's
// fix. It does not assert a duration — durations are not reproducible on a
// laptop — it asserts the STRUCTURAL property that produced the duration:
// materializing a result must cost ONE cross-OS-thread handoff, not one per
// result cell.
//
// If someone later reintroduces a per-call dispatch inside a statement, the
// ratio collapses and this fails with the numbers that explain why.
func TestQueryCostsOneExecThreadDispatch(t *testing.T) {
	if !wasmrt.ExecBatchEnabled() {
		t.Skip("SDN_WASMRT_EXEC_BATCH=0: per-call dispatch mode, nothing to assert")
	}
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "percall-dispatch")

	const rows, cols = 30, 15
	before := rt.WasmModule().DispatchStats()
	res, err := db.Query(controlShapeSQL(rows, cols))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	after := rt.WasmModule().DispatchStats()

	if len(res.Rows) != rows || len(res.Columns) != cols {
		t.Fatalf("fixture shape wrong: %d rows x %d cols", len(res.Rows), len(res.Columns))
	}

	calls := after.Calls - before.Calls
	dispatches := after.Dispatches - before.Dispatches
	batches := after.Batches - before.Batches

	// The engine has no bulk-result export for trusted callers, so the call
	// count is inherently O(rows x cols) — that is the architectural finding,
	// recorded in the task, not something this fix changes.
	wantCalls := int64(2 + cols + 2*rows*cols)
	if calls < wantCalls {
		t.Fatalf("guest calls = %d, expected at least %d (2 + cols + 2*rows*cols) — "+
			"did the readout shape change?", calls, wantCalls)
	}
	if batches != 1 {
		t.Fatalf("batches = %d, want exactly 1 per statement", batches)
	}
	if dispatches != 1 {
		t.Fatalf("thread handoffs = %d for %d guest calls, want exactly 1 — "+
			"a guest call inside Query is dispatching individually again", dispatches, calls)
	}
	t.Logf("30x15 result: %d guest calls amortized onto %d thread handoff(s) (%.0fx)",
		calls, dispatches, float64(calls)/float64(dispatches))
}

// TestExecScopeRefusesUseAfterBatch pins the one way this primitive can be
// misused into breaking the invariant it preserves: retaining the GuestCaller
// and calling it later, from an arbitrary goroutine, which would execute the
// guest off the dedicated thread.
func TestExecScopeRefusesUseAfterBatch(t *testing.T) {
	if !wasmrt.ExecBatchEnabled() {
		t.Skip("SDN_WASMRT_EXEC_BATCH=0: no scope is created in per-call mode")
	}
	rt := newTestRuntime(t)

	var escaped wasmrt.GuestCaller
	if err := rt.WasmModule().RunOnExecThread(nil, func(inv wasmrt.GuestCaller) error {
		escaped = inv
		return nil
	}); err != nil {
		t.Fatalf("RunOnExecThread: %v", err)
	}
	if _, err := escaped.Execute("flatsql_get_error"); err == nil {
		t.Fatal("a retained ExecScope executed the guest after its batch returned")
	}
	if rt.Poisoned() {
		t.Fatal("refusing a stale scope must not poison the engine")
	}
}

// TestQueryDispatchLatencyAB is the MEASUREMENT, not an assertion. It
// reproduces host-01's shape as closely as a laptop can — GOMAXPROCS pinned
// low, a background lane holding the engine continuously — and prints the
// probe distribution plus the dispatch account.
//
// Run BOTH sides to get the A/B at one binary and one engine pin:
//
//	PERCALL_AB=1 SDN_WASMRT_EXEC_BATCH=0 go test ./internal/flatsqlrt -run DispatchLatencyAB -v
//	PERCALL_AB=1 SDN_WASMRT_EXEC_BATCH=1 go test ./internal/flatsqlrt -run DispatchLatencyAB -v
func TestQueryDispatchLatencyAB(t *testing.T) {
	if os.Getenv("PERCALL_AB") == "" {
		t.Skip("set PERCALL_AB=1 to run the dispatch-latency A/B")
	}
	procs := 2
	if v := os.Getenv("PERCALL_AB_PROCS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			procs = n
		}
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(procs))

	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "percall-ab")

	const rows, cols = 30, 15
	probeSQL := controlShapeSQL(rows, cols)
	// The background lane is a SECOND, larger statement: on host-01 the engine
	// is never idle, and a probe's cost is only honest when it is measured
	// against that.
	laneSQL := controlShapeSQL(200, 15)

	var stop atomic.Bool
	var laneReads atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if _, err := db.Query(laneSQL); err != nil {
					return
				}
				laneReads.Add(1)
			}
		}()
	}
	time.Sleep(250 * time.Millisecond) // let the lane saturate

	const n = 40
	samples := make([]time.Duration, 0, n)
	before := rt.WasmModule().DispatchStats()
	for i := 0; i < n; i++ {
		start := time.Now()
		if _, err := db.Query(probeSQL); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		samples = append(samples, time.Since(start))
	}
	after := rt.WasmModule().DispatchStats()
	stop.Store(true)
	wg.Wait()

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	pct := func(p float64) time.Duration { return samples[int(float64(len(samples)-1)*p)] }

	mode := "BATCHED (one handoff per statement)"
	if !wasmrt.ExecBatchEnabled() {
		mode = "PER-CALL (one handoff per guest call)"
	}
	calls := after.Calls - before.Calls
	dispatches := after.Dispatches - before.Dispatches
	t.Logf("mode=%s GOMAXPROCS=%d probes=%d 30x15 rows, background lane reads=%d",
		mode, procs, n, laneReads.Load())
	t.Logf("probe min=%s p50=%s p90=%s p95=%s max=%s",
		samples[0].Round(time.Microsecond), pct(0.50).Round(time.Microsecond),
		pct(0.90).Round(time.Microsecond), pct(0.95).Round(time.Microsecond),
		samples[len(samples)-1].Round(time.Microsecond))
	t.Logf("dispatch account over the probe window: %d guest calls, %d thread handoffs (%.1f calls/handoff)",
		calls, dispatches, float64(calls)/float64(max64(dispatches, 1)))
	t.Logf("busiest exports: %s", after.Top(6))
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
