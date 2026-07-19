package wasmrt

// Proof tests for the wasi-threads runtime host (wasithreads.go).
//
// These assert what the task demands: compiling != running. A real
// wasm32-wasip1-threads fixture, loaded through wasmrt's WithWASIThreads path,
// must INSTANTIATE under WasmEdge 0.14.1's shared memory AND spawn more than one
// OS thread doing real work — observably: distinct kernel threads, peak
// concurrency > 1, per-thread markers in shared memory, and near-independent
// wall-time scaling. If wasi.thread-spawn were unresolved (the pre-fix state)
// the module would trap at the guest's first pthread_create.

import (
	_ "embed"
	"runtime"
	"testing"
	"time"
)

//go:embed testdata/wasithreads_fixture.wasm
var wasiThreadsFixtureWasm []byte

// newThreadsFixture builds the fixture module on the wasi-threads backend.
func newThreadsFixture(t *testing.T) *Module {
	t.Helper()
	m, err := NewModule(wasiThreadsFixtureWasm, WithWASIThreads())
	if err != nil {
		t.Fatalf("NewModule(WithWASIThreads): %v", err)
	}
	// Reactor init: sets up libc + the main thread's TLS before any spawn.
	if m.HasFunction("_initialize") {
		if _, err := m.Execute("_initialize"); err != nil {
			m.Release()
			t.Fatalf("_initialize: %v", err)
		}
	}
	return m
}

// runWithWatchdog runs fn but fails the test if it does not return within
// timeout — a broken cross-thread atomic wait/notify (e.g. a deadlocked
// pthread_join) would otherwise hang forever. A hang here is a real blocker to
// surface, not to paper over.
func runWithWatchdog(t *testing.T, timeout time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Logf("GOROUTINE DUMP ON HANG:\n%s", buf[:n])
		t.Fatalf("wasi-threads run did not complete within %s — probable thread-spawn/join deadlock under WasmEdge", timeout)
	}
}

// TestWASIThreadsSpawnsRealOSThreads is the core proof.
func TestWASIThreadsSpawnsRealOSThreads(t *testing.T) {
	const nthreads = 4

	m := newThreadsFixture(t)
	defer m.Release()

	// Small post-barrier busy-work: the spin-barrier alone forces peak==nthreads;
	// a little work keeps the workers overlapping without being interpreter-slow.
	const workKilo = 2000
	var peak int32
	runWithWatchdog(t, 60*time.Second, func() {
		res, err := m.Execute("run", int32(nthreads), int32(workKilo))
		if err != nil {
			t.Errorf("run(%d): %v", nthreads, err)
			return
		}
		peak = ToInt32(res[0])
	})
	if t.Failed() {
		return
	}

	// Guest-side witnesses (read back through exported getters).
	guestPeak := execI32(t, m, "get_peak")
	joined := execI32(t, m, "get_joined")
	if joined != nthreads {
		t.Errorf("get_joined = %d, want %d (not all workers completed + were joined)", joined, nthreads)
	}
	if guestPeak < 2 {
		t.Errorf("get_peak = %d — the guest never observed >1 worker running at once (no real parallelism)", guestPeak)
	}
	if peak != guestPeak {
		t.Errorf("run() returned peak %d but get_peak() = %d", peak, guestPeak)
	}
	// Per-thread markers must be distinct + nonzero — proves each worker had its
	// OWN stack (aliased stacks would corrupt these).
	seen := map[int32]bool{}
	for i := 0; i < nthreads; i++ {
		mk := execI32(t, m, "get_marker", int32(i))
		if mk == 0 {
			t.Errorf("worker %d never ran (marker 0)", i)
		}
		if seen[mk] {
			t.Errorf("duplicate worker marker 0x%X — worker stacks aliased", mk)
		}
		seen[mk] = true
	}

	// Host-side witnesses.
	if got := m.ThreadSpawnCount(); got != nthreads {
		t.Errorf("ThreadSpawnCount = %d, want %d", got, nthreads)
	}
	if got := m.PeakConcurrentThreads(); got < 2 {
		t.Errorf("PeakConcurrentThreads = %d — host never saw >1 worker thread live at once", got)
	}
	if err := m.WorkerError(); err != nil {
		t.Errorf("a worker thread's wasi_thread_start failed: %v", err)
	}
	tids := m.WorkerOSThreadIDs()
	if len(tids) != nthreads {
		t.Errorf("WorkerOSThreadIDs len = %d, want %d", len(tids), nthreads)
	}
	distinct := map[int64]bool{}
	for _, id := range tids {
		distinct[id] = true
	}
	if len(distinct) != len(tids) {
		t.Errorf("worker OS thread ids not distinct: %v — workers were not real separate OS threads", tids)
	}

	t.Logf("wasi-threads PROOF: nthreads=%d guest_peak=%d host_peak=%d joined=%d os_tids=%v",
		nthreads, guestPeak, m.PeakConcurrentThreads(), joined, tids)
}

// TestWASIThreadsScaling is a quantitative parallelism witness: the fixture does
// a FIXED busy-work span PER worker, so if the workers truly run in parallel the
// wall time for N workers is ~the same as for 1 (each on its own core); if they
// were serialized it would be ~Nx. Requires >=N host cores to be meaningful.
func TestWASIThreadsScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("scaling probe skipped in -short")
	}
	const n = 4
	if runtime.NumCPU() < n {
		t.Skipf("need >=%d CPUs for the scaling ratio (have %d)", n, runtime.NumCPU())
	}

	m := newThreadsFixture(t)
	defer m.Release()

	// Enough post-barrier work per thread that wall time is dominated by the
	// work, so the 1-vs-N ratio reflects parallelism (not fixed overhead).
	const workKilo = 40000
	timeRun := func(threads int32) time.Duration {
		var d time.Duration
		runWithWatchdog(t, 120*time.Second, func() {
			start := time.Now()
			if _, err := m.Execute("run", threads, int32(workKilo)); err != nil {
				t.Errorf("run(%d): %v", threads, err)
			}
			d = time.Since(start)
		})
		return d
	}

	one := timeRun(1)
	many := timeRun(n)
	ratio := float64(many) / float64(one)
	t.Logf("wasi-threads SCALING: run(1)=%s run(%d)=%s  wall-ratio=%.2fx (parallel≈1x, serial≈%dx, gate<3.0x)", one, n, many, ratio, n)
	// A truly-parallel N-worker run stays near 1x (each worker on its own core);
	// a serialized one grows to ~Nx. The 3.0x gate cleanly separates the two
	// (measured ~2x in interpreter under Docker) while tolerating CI jitter.
	if ratio > 3.0 {
		t.Errorf("run(%d) took %.2fx run(1) — workers are NOT running in parallel (serial would be ~%dx)", n, ratio, n)
	}
}

func execI32(t *testing.T, m *Module, name string, params ...interface{}) int32 {
	t.Helper()
	res, err := m.Execute(name, params...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(res) == 0 {
		t.Fatalf("%s returned no value", name)
	}
	return ToInt32(res[0])
}
