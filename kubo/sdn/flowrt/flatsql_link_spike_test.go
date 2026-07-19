//go:build linux

package flowrt_test

// flatsql_link_spike_test.go — SPIKE (guardian-widened): proves the COMPOSITION
// MECHANISM for the in-wasm FlatSQL linked store under wasi-threads.
//
// An AOT wasi-threads reactor's main thread (after joining its own std::thread
// workers) calls a Go HOST TRAMPOLINE that reads a record from the reactor's
// SHARED memory and marshals INSERT + SELECT + ExportData->LoadAndRebuild into a
// DEDICATED-THREAD flatsqlrt engine (single-thread SQLite, its OWN memory, its
// OWN OS thread) under the engine module lock — then returns the row count back
// into the reactor. This is the exact shape the linked OD store needs (the
// engine can NOT join the WithWASIThreads executor — mutually exclusive — so it
// runs on its dedicated thread and flatsql.* resolve as host trampolines).
//
// GATES: (a) the engine executes on its OWN thread (not the composed executor) —
// proven by the round-trip completing with no nested-AOT corruption/trap; (b) the
// reactor's std::thread workers still spawn (peak>=2); (c) the opaque
// ExportData->LoadAndRebuild round-trip preserves the stored row (count stable).
// A trap/deadlock here is a BLOCKER.

import (
	"os"
	"sync/atomic"
	"testing"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/wasmrt"
	"github.com/second-state/WasmEdge-go/wasmedge"
)

const spikeSchema = "table SPK { v:string; }"

func TestFlatSQLLinkCompositionSpike(t *testing.T) {
	reactorPath := os.Getenv("SDN_FSL_SPIKE_REACTOR")
	if reactorPath == "" {
		t.Skip("set SDN_FSL_SPIKE_REACTOR to the AOT wasi-threads spike reactor wasm")
	}
	reactor, err := os.ReadFile(reactorPath)
	if err != nil {
		t.Fatalf("read reactor: %v", err)
	}

	// ── Dedicated-thread flatsqlrt engine (AOT) — the single-thread store. ──
	rt, err := flatsqlrt.New(flatsqlrt.WithAOTCache(t.TempDir()))
	if err != nil {
		t.Fatalf("flatsqlrt.New: %v", err)
	}
	defer rt.Close()
	db, err := rt.CreateDatabase(spikeSchema, "spike")
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	defer db.Destroy()
	if _, err := db.Query("CREATE TABLE t(v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	var engineCalls int32
	var lastErr error
	count := func() (int64, error) {
		r, e := db.Query("SELECT COUNT(*) FROM t")
		if e != nil {
			return 0, e
		}
		return r.Rows[0][0].(int64), nil
	}

	// ── Host trampoline: reactor shared mem -> dedicated-thread engine. ──
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	storeAndQuery := wasmrt.HostFunc{
		Name:    "store_and_query",
		Params:  []*wasmedge.ValType{i32(), i32()},
		Returns: []*wasmedge.ValType{i32()},
		Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			atomic.AddInt32(&engineCalls, 1)
			ptr := uint32(params[0].(int32))
			ln := uint32(params[1].(int32))
			mem := cf.GetMemoryByIndex(0) // the reactor's SHARED memory
			if mem == nil {
				lastErr = errString("no reactor memory in calling frame")
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			rec, e := mem.GetData(uint(ptr), uint(ln))
			if e != nil {
				lastErr = e
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			// INSERT the opaque record bytes (as a string param) into the engine.
			if _, e := db.Query("INSERT INTO t(v) VALUES(?)", string(rec)); e != nil {
				lastErr = e
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			c1, e := count()
			if e != nil {
				lastErr = e
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			// Opaque persistence round-trip: pull the whole-arena snapshot + rebuild.
			blob, e := db.ExportData()
			if e != nil {
				lastErr = e
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			if e := db.LoadAndRebuild(blob); e != nil {
				lastErr = e
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			c2, e := count()
			if e != nil {
				lastErr = e
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			if c1 != c2 {
				lastErr = errString("export/rebuild changed the row count")
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			return []interface{}{int32(c2)}, wasmedge.Result_Success // rows back to the reactor
		},
	}

	// ── AOT wasi-threads reactor + the trampoline host module. ──
	m, err := wasmrt.NewModule(reactor,
		wasmrt.WithWASIThreads(),
		wasmrt.WithHostModule("spikehost", []wasmrt.HostFunc{storeAndQuery}),
		wasmrt.WithWASIArgs([]string{"spike"}, nil, nil),
		wasmrt.WithMaxMemoryPages(32768),
	)
	if err != nil {
		t.Fatalf("NewModule(reactor, WithWASIThreads): %v", err)
	}
	defer m.Release()
	if m.HasFunction("_initialize") {
		if _, err := m.Execute("_initialize"); err != nil {
			t.Fatalf("_initialize: %v", err)
		}
	}

	res, err := m.Execute("run", int32(4))
	if err != nil {
		t.Fatalf("BLOCKER: run() trapped/failed (nested-AOT corruption or deadlock?): %v", err)
	}
	packed := wasmrt.ToInt32(res[0])
	peak := (packed >> 16) & 0xffff
	rows := packed & 0xffff

	t.Logf("SPIKE: reactor peak=%d, engine rows-returned=%d, host trampoline calls=%d, host_peak=%d, worker_tids=%v",
		peak, rows, atomic.LoadInt32(&engineCalls), m.PeakConcurrentThreads(), m.WorkerOSThreadIDs())
	if lastErr != nil {
		t.Fatalf("engine round-trip error: %v", lastErr)
	}
	if peak < 2 || m.PeakConcurrentThreads() < 2 {
		t.Fatalf("GATE (b) FAIL: reactor std::threads did not run >1 at once (guest peak=%d host peak=%d)", peak, m.PeakConcurrentThreads())
	}
	if rows < 1 {
		t.Fatalf("GATE (a/c) FAIL: engine returned %d rows — the trampoline round-trip into the dedicated-thread engine did not store+read+rebuild", rows)
	}
	t.Logf("★★★ COMPOSITION MECHANISM PROVEN: AOT wasi-threads reactor (peak %d threads) called a host trampoline that stored+queried a dedicated-thread flatsqlrt engine (rows=%d) with an opaque ExportData->LoadAndRebuild round-trip — no trap/deadlock, engine ran off the composed executor.", peak, rows)
}

type errString string

func (e errString) Error() string { return string(e) }
