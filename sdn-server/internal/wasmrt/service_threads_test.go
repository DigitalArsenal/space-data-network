package wasmrt

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// requirePatchedRuntime skips (or fails, under SDN_WASM_REQUIRE_PATCHED=1)
// when the linked libwasmedge lacks the SDN runtime patches.
func requirePatchedRuntime(t *testing.T) SubstrateReport {
	t.Helper()
	rep := RunSubstrateSelfTest(SubstrateOptions{SkipAOT: true})
	if !rep.Patched() {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
			t.Fatalf("runtime is not patched: %+v", rep)
		}
		t.Skipf("linked libwasmedge lacks the SDN runtime patches: %+v", rep)
	}
	return rep
}

// The self-test measures the runtime; on the release build (patched static
// prefix) every check must pass, and on any build it must not error.
func TestSubstrateSelfTest(t *testing.T) {
	rep := RunSubstrateSelfTest(SubstrateOptions{})
	t.Logf("%+v", rep)
	if len(rep.Errors) != 0 {
		t.Fatalf("self-test errors: %v", rep.Errors)
	}
	if rep.ThreadsSpawned != 4 {
		t.Fatalf("spawned %d spinners", rep.ThreadsSpawned)
	}
	if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" && !rep.Patched() {
		t.Fatalf("release runtime is not patched: %+v", rep)
	}
}

// stopDeadline is how long the A21 test gives ONE stop to end every spinner:
// 100 ms for each round of the host's CPUs that the spinners fill. A spinner
// ends only when the scheduler runs it after the stop, so on a host with fewer
// CPUs than spinners the last one waits for the others' time slices. On a
// 4-CPU Intel Mac runner (i7-8700B) the last of 16 ended 19 to 140 ms after
// the stop in five rounds, and none was still running at 10 s; a fixed 100 ms
// failed there about one run in five without any thread failing to stop.
func stopDeadline(spinners, cpus int) time.Duration {
	if cpus < 1 {
		cpus = 1
	}
	rounds := (spinners + cpus - 1) / cpus
	return time.Duration(rounds) * 100 * time.Millisecond
}

func TestStopDeadlineScalesWithSpinnersPerCPU(t *testing.T) {
	for _, c := range []struct {
		spinners, cpus int
		want           time.Duration
	}{
		{16, 16, 100 * time.Millisecond},
		{16, 32, 100 * time.Millisecond},
		{16, 4, 400 * time.Millisecond},
		{16, 3, 600 * time.Millisecond},
		{16, 0, 1600 * time.Millisecond},
	} {
		if got := stopDeadline(c.spinners, c.cpus); got != c.want {
			t.Errorf("stopDeadline(%d, %d) = %s, want %s", c.spinners, c.cpus, got, c.want)
		}
	}
}

// A21: 16 AOT threads in compute loops (Interruptible, no stop-word check,
// no calls out) all stop after ONE stop, within 100 ms for each round of the
// host's CPUs that they fill (100 ms on a host with 16 or more CPUs).
func TestOneStopEndsSixteenInterruptibleAOTThreads(t *testing.T) {
	requirePatchedRuntime(t)
	compiled, err := compileProbeAOT()
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewModule(compiled, WithMaxThreads(17), WithExecTimeout(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Release()
	v, err := m.Execute("spawn_spinners", int32(16))
	if err != nil || ToInt32(v[0]) != 16 {
		t.Fatalf("spawn_spinners: %v %v", v, err)
	}
	mem, err := m.SharedMemory()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < 16; i++ {
		for mem.Load32(probeSpinnerBase+uint32(64*i)) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("spinners did not start")
			}
			time.Sleep(time.Millisecond)
		}
	}
	workers := m.threads.live()
	m.threads.mu.Lock()
	m.threads.interrupting = true
	m.threads.mu.Unlock()
	limit := stopDeadline(16, runtime.NumCPU())
	start := time.Now()
	workers[0].async.Cancel()
	left := waitAll(workers, start.Add(limit))
	took := time.Since(start)
	t.Logf("one stop ended %d of 16 AOT spinners in %s (limit %s on %d CPUs)", 16-left, took, limit, runtime.NumCPU())
	if left != 0 {
		t.Fatalf("%d AOT threads still running %s after the stop", left, limit)
	}
	if m.Poisoned() {
		t.Fatalf("a deliberate stop poisoned the module: %v", m.PoisonCause())
	}
}

func TestThreadCapIsConfigurable(t *testing.T) {
	bytes, err := os.ReadFile("testdata/wasi-threads.wasm")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewModule(bytes, WithMaxMemoryPages(2), WithExecTimeout(2*time.Second), WithMaxThreads(3))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Release()
	v, err := m.Execute("capacity")
	if err != nil {
		t.Fatal(err)
	}
	// capacity() tries 33 spawns while every worker blocks: 3 run, 30 refused.
	if got := ToInt32(v[0]); got != 30 {
		t.Fatalf("refused %d spawns under a cap of 3, want 30", got)
	}
	if st := m.ThreadStats(); st.Max != 3 || st.Refused != 30 || st.Spawned != 3 {
		t.Fatalf("thread stats %+v", st)
	}
}

// A29: statistics take WasmEdge's Timer and cost locks, which every thread of
// an instance would share; a service-thread instance refuses them.
func TestServiceThreadInstanceRefusesStatistics(t *testing.T) {
	bytes, err := os.ReadFile("testdata/wasi-threads.wasm")
	if err != nil {
		t.Fatal(err)
	}
	for name, opt := range map[string]Option{"cost": WithCostLimit(1000), "counting": WithInstructionCounting()} {
		if _, err := NewModule(bytes, WithMaxMemoryPages(2), WithServiceThreads(), opt); err == nil ||
			!strings.Contains(err.Error(), "service-thread") {
			t.Fatalf("%s: service-thread instance accepted statistics: %v", name, err)
		}
	}
}

// The C handles the native host I/O module and the Interruptible compiler
// setting reach are where the binding's layout says.
func TestBindingLayoutAssertions(t *testing.T) {
	conf, err := NewThreadedCompilerConfig()
	if err != nil {
		t.Fatal(err)
	}
	conf.Release()
	mod := wasmedge.NewModule("env")
	defer mod.Release()
	if p, err := ModuleInstanceContext(mod); err != nil || p == nil {
		t.Fatalf("module handle: %v", err)
	}
	if _, err := ModuleInstanceContext(nil); err == nil {
		t.Fatal("nil module accepted")
	}
}
