package wasmrt

// The runtime substrate self-test (design A21, A30, A31). It MEASURES the
// linked libwasmedge instead of trusting a version string: upstream 0.16.4 and
// the patched build both call themselves 0.16.4.
//
//   AtomicNotifyWithoutStore  a notify wakes a waiter whose word never
//                             changed (01-atomic-wait).
//   StopReachesEveryThread    ONE executor stop ends every spinning thread,
//                             interpreted (02-stop-token).
//   InterruptibleAOT          the same, for AOT code compiled Interruptible
//                             with THREADS, and that code really runs native
//                             (the interpreter's instruction counter does not
//                             move while it spins).
//
// AOTSpeedup is reported, not judged: on an unpatched runtime Interruptible
// AOT code exchanges the shared stop token at every loop iteration, and the
// cache-line traffic alone holds four spinning threads to a few times the
// interpreter (measured 2.8x unpatched against 17x patched on an M-series
// Mac Studio, 2026-09-27).
//
// A31 made this a start-up metric, not a gate: the release binary always links
// the patched runtime, so format 2 does not refuse on it. The partition-store
// gate is A30's (AOT + THREADS + Interruptible + the C host I/O module).

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

//go:embed substrate/substrate-probe.wasm
var substrateProbeWasm []byte

// SubstrateProbeWasm returns the embedded probe (tests and tooling).
func SubstrateProbeWasm() []byte { return append([]byte(nil), substrateProbeWasm...) }

// SubstrateReport is the self-test's result.
type SubstrateReport struct {
	RuntimeVersion           string        `json:"runtime_version"`
	AtomicNotifyWithoutStore bool          `json:"atomic_notify_without_store"`
	StopReachesEveryThread   bool          `json:"stop_reaches_every_thread"`
	InterruptibleAOT         bool          `json:"interruptible_aot"`
	AOTChecked               bool          `json:"aot_checked"`
	ThreadsStoppedBySingle   int           `json:"threads_stopped_by_one_stop"`
	ThreadsSpawned           int           `json:"threads_spawned"`
	AOTNative                bool          `json:"aot_native"`
	AOTSpeedup               float64       `json:"aot_speedup"`
	Elapsed                  time.Duration `json:"elapsed_ns"`
	Errors                   []string      `json:"errors,omitempty"`
}

// Patched reports whether the runtime behaves like the patched build.
func (r SubstrateReport) Patched() bool {
	return r.AtomicNotifyWithoutStore && r.StopReachesEveryThread && (!r.AOTChecked || r.InterruptibleAOT)
}

// Tag names the runtime behaviour for AOT cache keys of Interruptible
// artifacts: code compiled by one stop-token semantics must not be loaded by
// the other.
func (r SubstrateReport) Tag() string {
	if r.AtomicNotifyWithoutStore && r.StopReachesEveryThread {
		return "sdn2"
	}
	return "up"
}

// SubstrateOptions shapes a self-test run.
type SubstrateOptions struct {
	// SkipAOT leaves the AOT check out (no LLVM compiler needed). The probe is
	// otherwise compiled fresh into a temporary directory on every run, so a
	// cached artifact from another runtime can never answer for this one.
	SkipAOT bool
	// Spinners is how many threads the stop check spins (default 4).
	Spinners int
}

const probeSpinnerBase = 64

// RunSubstrateSelfTest runs the substrate checks in fresh, short-lived VMs.
func RunSubstrateSelfTest(opts SubstrateOptions) SubstrateReport {
	start := time.Now()
	rep := SubstrateReport{RuntimeVersion: wasmedge.GetVersion()}
	if opts.Spinners <= 0 {
		opts.Spinners = 4
	}
	fail := func(format string, args ...interface{}) {
		rep.Errors = append(rep.Errors, fmt.Sprintf(format, args...))
	}

	if ok, err := probeNotifyWithoutStore(substrateProbeWasm); err != nil {
		fail("atomic notify: %v", err)
	} else {
		rep.AtomicNotifyWithoutStore = ok
	}

	interp, err := probeStop(substrateProbeWasm, opts.Spinners, false)
	if err != nil {
		fail("interpreted stop: %v", err)
	} else {
		rep.ThreadsSpawned = interp.spawned
		rep.ThreadsStoppedBySingle = interp.stoppedBySingle
		rep.StopReachesEveryThread = interp.spawned > 0 && interp.stoppedBySingle == interp.spawned
	}

	if !opts.SkipAOT {
		rep.AOTChecked = true
		compiled, cerr := compileProbeAOT()
		if cerr != nil {
			fail("interruptible AOT compile: %v", cerr)
		} else if aot, aerr := probeStop(compiled, opts.Spinners, true); aerr != nil {
			fail("interruptible AOT stop: %v", aerr)
		} else {
			if interp.rate > 0 {
				rep.AOTSpeedup = aot.rate / interp.rate
			}
			// A probe that silently fell back to interpreting cannot prove the
			// AOT path: the interpreter's counter must not move while it spins.
			rep.AOTNative = aot.interpreted == 0
			if !rep.AOTNative {
				fail("AOT probe interpreted %d instructions while spinning; the AOT section was not used", aot.interpreted)
			}
			rep.InterruptibleAOT = rep.AOTNative && aot.spawned > 0 && aot.stoppedBySingle == aot.spawned
		}
	}
	rep.Elapsed = time.Since(start)
	return rep
}

var (
	substrateOnce   sync.Once
	substrateReport SubstrateReport
)

// SubstrateStatus runs the self-test once per process (with the AOT check)
// and returns the cached report thereafter.
func SubstrateStatus() SubstrateReport {
	substrateOnce.Do(func() {
		substrateReport = RunSubstrateSelfTest(SubstrateOptions{})
		level := "INFO"
		if !substrateReport.Patched() {
			level = "WARN"
		}
		fmt.Fprintf(os.Stderr, "[wasmrt] %s substrate self-test: runtime %s notify-without-store=%t stop-reaches-every-thread=%t interruptible-aot=%t (%.1fx) in %s %v\n",
			level, substrateReport.RuntimeVersion, substrateReport.AtomicNotifyWithoutStore,
			substrateReport.StopReachesEveryThread, substrateReport.InterruptibleAOT, substrateReport.AOTSpeedup,
			substrateReport.Elapsed.Round(time.Millisecond), substrateReport.Errors)
	})
	return substrateReport
}

func probeNotifyWithoutStore(wasm []byte) (bool, error) {
	m, err := NewModule(wasm, WithMaxThreads(4), WithExecTimeout(3*time.Second))
	if err != nil {
		return false, err
	}
	defer m.Release()
	v, err := m.Execute("notify_without_store")
	if err != nil {
		return false, err
	}
	if len(v) != 1 {
		return false, errors.New("unexpected result arity")
	}
	switch ToInt32(v[0]) {
	case 0:
		return true, nil
	case 2:
		return false, nil
	default:
		return false, fmt.Errorf("notify_without_store returned %d", ToInt32(v[0]))
	}
}

type stopProbe struct {
	spawned, stoppedBySingle int
	rate                     float64 // increments per second across spinners
	interpreted              uint64  // interpreter instructions during the rate window
}

// probeStop starts n spinners, measures their rate, issues exactly ONE stop
// and counts the threads it ended, then stops the rest however many rounds it
// takes (an unpatched runtime needs one per thread).
func probeStop(wasm []byte, n int, countInstructions bool) (stopProbe, error) {
	var out stopProbe
	opts := []Option{WithMaxThreads(n + 1), WithExecTimeout(3 * time.Second)}
	if countInstructions {
		opts = append(opts, WithInstructionCounting())
	}
	m, err := NewModule(wasm, opts...)
	if err != nil {
		return out, err
	}
	released := false
	defer func() {
		if !released {
			m.Release()
		}
	}()
	v, err := m.Execute("spawn_spinners", int32(n))
	if err != nil {
		return out, err
	}
	out.spawned = int(ToInt32(v[0]))
	if out.spawned != n {
		return out, fmt.Errorf("spawned %d of %d spinners", out.spawned, n)
	}
	mem, err := m.SharedMemory()
	if err != nil {
		return out, err
	}
	sum := func() uint64 {
		var s uint64
		for i := 0; i < n; i++ {
			s += uint64(mem.Load32(probeSpinnerBase + uint32(64*i)))
		}
		return s
	}
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < n; i++ {
		for mem.Load32(probeSpinnerBase+uint32(64*i)) == 0 {
			if time.Now().After(deadline) {
				return out, errors.New("spinners did not start")
			}
			time.Sleep(time.Millisecond)
		}
	}
	t0, s0, i0 := time.Now(), sum(), m.InstructionCount()
	time.Sleep(100 * time.Millisecond)
	out.rate = float64(sum()-s0) / time.Since(t0).Seconds()
	out.interpreted = m.InstructionCount() - i0

	workers := m.threads.live()
	if len(workers) == 0 {
		return out, errors.New("no live spinners")
	}
	m.threads.mu.Lock()
	m.threads.interrupting = true
	m.threads.mu.Unlock()
	workers[0].async.Cancel()
	still := waitAll(workers, time.Now().Add(300*time.Millisecond))
	out.stoppedBySingle = len(workers) - still
	if left := m.InterruptThreads(time.Now().Add(3 * time.Second)); left > 0 {
		// Never free memory beneath running threads: retain the VM.
		released = true
		m.MarkPoisoned(fmt.Errorf("substrate probe: %d spinners did not stop", left))
		return out, fmt.Errorf("%d spinners did not stop", left)
	}
	return out, nil
}

func compileProbeAOT() ([]byte, error) {
	dir, err := os.MkdirTemp("", "sdn-substrate-aot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "substrate-probe.aot.wasm")
	conf, err := NewThreadedCompilerConfig()
	if err != nil {
		return nil, err
	}
	defer conf.Release()
	compiler := wasmedge.NewCompilerWithConfig(conf)
	if compiler == nil {
		return nil, errors.New("WasmEdge AOT compiler unavailable")
	}
	defer compiler.Release()
	if err := compiler.CompileBuffer(substrateProbeWasm, path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
