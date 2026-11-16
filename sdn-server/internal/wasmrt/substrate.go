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
//   AOTAtomicMemargOffset     AOT memory.atomic.notify and wait32 act on the
//                             operand PLUS the instruction's memarg offset
//                             (04-atomic-memarg-offset). The unpatched AOT
//                             compiler dropped the offset, so a wasi-libc
//                             thread-list-lock wakeup (`i32.const 0; notify
//                             offset=<lock>`) went to address 0 and thread
//                             exit/join hung; a wait on a stack local compared
//                             another word and returned at once.
//   AOTLoopStopChecks         Interruptible AOT code checks the stop token at
//                             loop headers and function entries only
//                             (05-loop-stop-checks): a function of empty
//                             blocks compiles to no more code than one
//                             without them. The unpatched compiler put a check
//                             at every block, 182 of them in front of each
//                             SQLite VDBE opcode dispatch.
//
//   AOTAtomicStoreStack       Every atomic store consumes both operands
//                             (07-atomic-store-stack).
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

//go:embed substrate/atomic-store-probe.wasm
var atomicStoreProbeWasm []byte

// SubstrateProbeWasm returns the embedded probe (tests and tooling).
func SubstrateProbeWasm() []byte { return append([]byte(nil), substrateProbeWasm...) }

// SubstrateReport is the self-test's result.
type SubstrateReport struct {
	RuntimeVersion           string          `json:"runtime_version"`
	AtomicNotifyWithoutStore bool            `json:"atomic_notify_without_store"`
	StopReachesEveryThread   bool            `json:"stop_reaches_every_thread"`
	InterruptibleAOT         bool            `json:"interruptible_aot"`
	AOTAtomicMemargOffset    bool            `json:"aot_atomic_memarg_offset"`
	AOTLoopStopChecks        bool            `json:"aot_loop_stop_checks"`
	AOTAtomicStoreStack      bool            `json:"aot_atomic_store_stack"`
	AOTChecked               bool            `json:"aot_checked"`
	ThreadsStoppedBySingle   int             `json:"threads_stopped_by_one_stop"`
	ThreadsSpawned           int             `json:"threads_spawned"`
	AOTNative                bool            `json:"aot_native"`
	AOTSpeedup               float64         `json:"aot_speedup"`
	Elapsed                  time.Duration   `json:"elapsed_ns"`
	Errors                   []string        `json:"errors,omitempty"`
	Inconclusive             bool            `json:"inconclusive"`
	InterpretedStop          StopObservation `json:"interpreted_stop"`
	AOTStop                  StopObservation `json:"aot_stop"`
	NotifyAttempts           int             `json:"notify_attempts"`
	MemargNotifyAttempts     int             `json:"memarg_notify_attempts"`
}

// Patched reports whether the runtime behaves like the patched build.
func (r SubstrateReport) Patched() bool {
	return !r.Inconclusive && len(r.Errors) == 0 && r.AtomicNotifyWithoutStore && r.StopReachesEveryThread &&
		(!r.AOTChecked || (r.InterruptibleAOT && r.AOTAtomicMemargOffset && r.AOTLoopStopChecks && r.AOTAtomicStoreStack))
}

// StopObservation records evidence from one stop request, before cleanup.
// Outcomes are shared, not_shared, inconclusive, or not_checked.
type StopObservation struct {
	Outcome          string   `json:"outcome"`
	Started          int      `json:"started"`
	Stopped          int      `json:"stopped"`
	PostStopProgress []uint32 `json:"post_stop_progress,omitempty"`
	Detail           string   `json:"detail,omitempty"`
}

// FailureReason distinguishes an observed mismatch from missing evidence.
func (r SubstrateReport) FailureReason() string {
	if r.Patched() {
		return "SDN runtime patches confirmed"
	}
	verdict := "runtime failed SDN patch checks"
	if r.Inconclusive {
		verdict = "runtime self-test inconclusive"
	}
	return fmt.Sprintf("%s: interpreted stop=%s (%d started, %d stopped), AOT stop=%s (%d started, %d stopped), notify=%t memarg=%t loop=%t store=%t native=%t; errors=%v",
		verdict, r.InterpretedStop.Outcome, r.InterpretedStop.Started, r.InterpretedStop.Stopped,
		r.AOTStop.Outcome, r.AOTStop.Started, r.AOTStop.Stopped, r.AtomicNotifyWithoutStore,
		r.AOTAtomicMemargOffset, r.AOTLoopStopChecks, r.AOTAtomicStoreStack, r.AOTNative, r.Errors)
}

// Tag names the runtime behavior for AOT cache keys of Interruptible
// artifacts: code compiled by one stop-token semantics must not be loaded by
// the other.
//
// "sdn3" adds AOTAtomicMemargOffset: artifacts compiled before the
// atomic-memarg-offset patch carry the broken wait/notify addressing, so the
// key changes and every host recompiles them.
//
// "sdn4" adds AOTLoopStopChecks: artifacts compiled before 05-loop-stop-checks
// carry a stop check at every block and run the engine 2-3x slower, so the key
// changes and every host recompiles them.
// "sdn5" adds AOTAtomicStoreStack: artifacts made by a compiler that leaves
// the address on its value stack after an atomic store are recompiled.
func (r SubstrateReport) Tag() string {
	if r.AtomicNotifyWithoutStore && r.StopReachesEveryThread {
		if r.AOTAtomicMemargOffset && r.AOTLoopStopChecks {
			if r.AOTAtomicStoreStack {
				return "sdn5"
			}
			return "sdn4"
		}
		if r.AOTAtomicMemargOffset {
			return "sdn3"
		}
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
	rep := SubstrateReport{RuntimeVersion: wasmedge.GetVersion(), AOTStop: StopObservation{Outcome: "not_checked"}}
	if opts.Spinners <= 0 {
		opts.Spinners = 4
	}
	conclusiveFailure := false
	fail := func(format string, args ...interface{}) {
		rep.Errors = append(rep.Errors, fmt.Sprintf(format, args...))
	}

	interp, err := probeStop(substrateProbeWasm, opts.Spinners, false)
	rep.ThreadsSpawned = interp.spawned
	rep.ThreadsStoppedBySingle = interp.observation.Stopped
	rep.InterpretedStop = interp.observation
	rep.StopReachesEveryThread = interp.observation.Outcome == "shared"
	conclusiveFailure = interp.observation.Outcome == "not_shared"
	if err != nil {
		fail("interpreted stop: %v", err)
	}
	failedConclusively := func() bool { return conclusiveFailure }

	ok, attempts, err := probeNotifyWithoutStore(substrateProbeWasm, failedConclusively)
	rep.NotifyAttempts = attempts
	if err != nil {
		fail("atomic notify: %v", err)
	} else {
		rep.AtomicNotifyWithoutStore = ok
	}

	if !opts.SkipAOT {
		rep.AOTChecked = true
		rep.AOTStop = StopObservation{Outcome: "inconclusive", Detail: "AOT probe did not run"}
		if interpreted, err := probeAtomicStoreStack(atomicStoreProbeWasm); err != nil {
			fail("interpreted atomic store stack: %v", err)
		} else if compiled, err := compileThreadedAOT(atomicStoreProbeWasm); err != nil {
			fail("AOT atomic store stack compile: %v", err)
		} else if ok, err := probeAtomicStoreStack(compiled); err != nil {
			fail("AOT atomic store stack: %v", err)
		} else {
			rep.AOTAtomicStoreStack = interpreted && ok
			conclusiveFailure = conclusiveFailure || !rep.AOTAtomicStoreStack
		}
		compiled, cerr := compileProbeAOT()
		if cerr != nil {
			fail("interruptible AOT compile: %v", cerr)
		} else {
			aot, aerr := probeStop(compiled, opts.Spinners, true)
			rep.AOTStop = aot.observation
			conclusiveFailure = conclusiveFailure || aot.observation.Outcome == "not_shared"
			if aerr != nil {
				fail("interruptible AOT stop: %v", aerr)
			}
			if interp.rate > 0 {
				rep.AOTSpeedup = aot.rate / interp.rate
			}
			// Preserve the measured execution mode even if completion is delayed.
			rep.AOTNative = aot.measured && aot.interpreted == 0
			if aot.measured && !rep.AOTNative {
				conclusiveFailure = true
				fail("AOT probe interpreted %d instructions while spinning; the AOT section was not used", aot.interpreted)
			}
			rep.InterruptibleAOT = rep.AOTNative && aot.observation.Outcome == "shared"
			memarg, err := probeMemargOffset(compiled, failedConclusively)
			rep.MemargNotifyAttempts = memarg.notifyAttempts
			conclusiveFailure = conclusiveFailure || memarg.waitMismatch
			if err != nil {
				fail("AOT atomic memarg offset: %v", err)
			} else {
				rep.AOTAtomicMemargOffset = memarg.ok
			}
		}
		if ok, err := probeLoopStopChecks(); err != nil {
			fail("AOT loop stop checks: %v", err)
		} else {
			rep.AOTLoopStopChecks = ok
			conclusiveFailure = conclusiveFailure || !ok
		}
	}
	// A notify that timed out on every attempt, and a missing observation,
	// cannot establish that a patch is absent. Only an observed behavioral
	// mismatch can do that.
	rep.Inconclusive = !conclusiveFailure && !rep.Patched()
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
		fmt.Fprintf(os.Stderr, "[wasmrt] %s substrate self-test: runtime %s notify-without-store=%t stop-reaches-every-thread=%t interruptible-aot=%t (%.1fx) aot-atomic-memarg-offset=%t aot-loop-stop-checks=%t aot-atomic-store-stack=%t in %s; %s\n",
			level, substrateReport.RuntimeVersion, substrateReport.AtomicNotifyWithoutStore,
			substrateReport.StopReachesEveryThread, substrateReport.InterruptibleAOT, substrateReport.AOTSpeedup,
			substrateReport.AOTAtomicMemargOffset, substrateReport.AOTLoopStopChecks, substrateReport.AOTAtomicStoreStack,
			substrateReport.Elapsed.Round(time.Millisecond), substrateReport.FailureReason())
	})
	return substrateReport
}

// A notify probe's waiter gives up after 500 ms. Without the patch under test
// it is never woken, so it times out on every attempt and can never report a
// wake. With the patch, a timeout only means the notifier thread did not run
// within those 500 ms. A timed-out attempt is therefore repeated, in a fresh
// module so no notifier outlives its attempt, until a wake, the observation
// limit, or another check's conclusive failure.

// probeNotifyWithoutStore returns whether a notify woke a waiter whose word
// never changed, and how many attempts it took.
func probeNotifyWithoutStore(wasm []byte, failedConclusively func() bool) (bool, int, error) {
	deadline := time.Now().Add(substrateObservationLimit)
	for attempts := 1; ; attempts++ {
		r, err := runProbeExport(wasm, 4, "notify_without_store")
		if err != nil {
			return false, attempts, err
		}
		switch r {
		case 0:
			return true, attempts, nil
		case 2:
			if failedConclusively() || !time.Now().Before(deadline) {
				return false, attempts, nil
			}
		default:
			return false, attempts, fmt.Errorf("notify_without_store returned %d", r)
		}
	}
}

// runProbeExport runs one export returning i32 in a fresh module.
func runProbeExport(wasm []byte, threads int, name string) (int32, error) {
	m, err := NewModule(wasm, WithMaxThreads(threads), WithExecTimeout(substrateObservationLimit))
	if err != nil {
		return 0, err
	}
	defer m.Release()
	v, err := m.Execute(name)
	if err != nil {
		return 0, err
	}
	if len(v) != 1 {
		return 0, fmt.Errorf("%s: unexpected result arity", name)
	}
	return ToInt32(v[0]), nil
}

type memargProbe struct {
	ok             bool
	waitMismatch   bool // the wait compared the word without its offset
	notifyAttempts int
}

// probeMemargOffset runs the two memarg-offset checks on the AOT-compiled
// probe. A wait addressed through its offset must compare the word at 32 (7),
// not the word at 0: the wrong address returns not-equal (1) at once, every
// time, which is a conclusive mismatch. A notify addressed that way must wake
// a waiter on address 32; it is repeated as described above.
func probeMemargOffset(compiled []byte, failedConclusively func() bool) (memargProbe, error) {
	var out memargProbe
	wait, err := runProbeExport(compiled, 4, "memarg_offset_wait")
	if err != nil {
		return out, err
	}
	switch wait {
	case 1:
		out.waitMismatch = true
		return out, nil
	case 2:
	default:
		return out, fmt.Errorf("memarg_offset_wait returned %d", wait)
	}
	deadline := time.Now().Add(substrateObservationLimit)
	for {
		out.notifyAttempts++
		notify, err := runProbeExport(compiled, 4, "memarg_offset_notify")
		if err != nil {
			return out, err
		}
		switch notify {
		case 0:
			out.ok = true
			return out, nil
		case 2:
			if failedConclusively() || !time.Now().Before(deadline) {
				return out, nil
			}
		default:
			return out, fmt.Errorf("memarg_offset_notify returned %d", notify)
		}
	}
}

type stopProbe struct {
	spawned     int
	rate        float64 // increments per second across spinners
	interpreted uint64  // interpreter instructions during the rate window
	measured    bool
	observation StopObservation
}

// An observation deadline limits waiting; expiration does not prove a missing patch.
const substrateObservationLimit = 30 * time.Second

// probeStop issues one stop after every spinner has started. All completions
// prove a shared token. Continued execution after synchronizing the counters
// proves a consumed token. A deadline is only an inconclusive observation.
func probeStop(wasm []byte, n int, countInstructions bool) (out stopProbe, err error) {
	out.observation.Outcome = "inconclusive"
	opts := []Option{WithMaxThreads(n + 1)}
	if countInstructions {
		opts = append(opts, WithInstructionCounting())
	}
	m, err := NewModule(wasm, opts...)
	if err != nil {
		return out, err
	}
	defer func() {
		// Cleanup is separate from the single-stop observation, including on
		// readiness failures. Never free memory beneath an unfinished worker.
		if left := m.InterruptThreads(time.Now().Add(substrateObservationLimit)); left > 0 {
			cleanupErr := fmt.Errorf("cleanup: %d spinners remain; retained probe VM", left)
			m.MarkPoisoned(cleanupErr)
			err = errors.Join(err, cleanupErr)
			return
		}
		m.Release()
	}()
	v, err := m.Execute("spawn_spinners", int32(n))
	if err != nil {
		return out, err
	}
	if len(v) != 1 {
		return out, errors.New("unexpected spinner result arity")
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
		var total uint64
		for i := 0; i < n; i++ {
			total += uint64(mem.Load32(probeSpinnerBase + uint32(64*i)))
		}
		return total
	}
	deadline := time.Now().Add(substrateObservationLimit)
	for out.observation.Started < n {
		out.observation.Started = 0
		for i := 0; i < n; i++ {
			if mem.Load32(probeSpinnerBase+uint32(64*i)) != 0 {
				out.observation.Started++
			}
		}
		if out.observation.Started == n {
			break
		}
		if !time.Now().Before(deadline) {
			out.observation.Detail = fmt.Sprintf("readiness deadline: %d of %d spinners observed running", out.observation.Started, n)
			return out, errors.New(out.observation.Detail)
		}
		time.Sleep(time.Millisecond)
	}
	t0, s0, i0 := time.Now(), sum(), m.InstructionCount()
	time.Sleep(100 * time.Millisecond)
	out.rate = float64(sum()-s0) / time.Since(t0).Seconds()
	out.interpreted = m.InstructionCount() - i0
	out.measured = true

	workers := m.threads.cancelOne()
	if len(workers) != n {
		return out, fmt.Errorf("found %d live spinners, expected %d", len(workers), n)
	}
	// Cancel synchronously stores the stop token. A guest atomic RMW that
	// reads this reset synchronizes with that store. It may finish its current
	// iteration, but a second increment requires another loop-header stop
	// check. A sticky token therefore permits at most one post-reset increment.
	// The guest drops the counter value; this cannot terminate its loop.
	for i := 0; i < n; i++ {
		mem.Store32(probeSpinnerBase+uint32(64*i), 0)
	}
	out.observation.PostStopProgress = make([]uint32, n)
	deadline = time.Now().Add(substrateObservationLimit)
	for {
		continued := false
		for i := 0; i < n; i++ {
			progress := mem.Load32(probeSpinnerBase + uint32(64*i))
			out.observation.PostStopProgress[i] = progress
			continued = continued || progress > 1
		}
		out.observation.Stopped = n - unfinishedWorkers(workers)
		if continued {
			out.observation.Outcome = "not_shared"
			out.observation.Detail = "spinner continued through a loop-header stop check after the synchronized reset"
			return out, nil
		}
		if out.observation.Stopped == n {
			out.observation.Outcome = "shared"
			return out, nil
		}
		if !time.Now().Before(deadline) {
			out.observation.Detail = fmt.Sprintf("completion deadline: %d of %d spinners completed after one stop; no continued execution observed", out.observation.Stopped, n)
			return out, errors.New(out.observation.Detail)
		}
		time.Sleep(time.Millisecond)
	}
}

func compileProbeAOT() ([]byte, error) {
	return compileThreadedAOT(substrateProbeWasm)
}

// probeBlocks is how many empty blocks the loop-stop-check probe compiles. A
// stop check is a load and a branch: at least 6 bytes of code per block on
// arm64 and x86-64.
const probeBlocks = 256

// probeLoopStopChecks compiles one function of probeBlocks empty blocks and the
// same function without them. With 05-loop-stop-checks a block compiles to no
// code, so the two artifacts differ by the blocks' wasm bytes alone; without
// it, by a stop check per block on top.
func probeLoopStopChecks() (bool, error) {
	withWasm, withoutWasm := blocksProbeWasm(probeBlocks), blocksProbeWasm(0)
	with, err := compileThreadedAOT(withWasm)
	if err != nil {
		return false, err
	}
	without, err := compileThreadedAOT(withoutWasm)
	if err != nil {
		return false, err
	}
	code := (len(with) - len(without)) - (len(withWasm) - len(withoutWasm))
	return code < probeBlocks, nil
}

// blocksProbeWasm is a module with one function, () -> (), whose body is n
// empty blocks.
func blocksProbeWasm(n int) []byte {
	leb := func(b []byte, v int) []byte {
		for {
			c := byte(v & 0x7f)
			v >>= 7
			if v == 0 {
				return append(b, c)
			}
			b = append(b, c|0x80)
		}
	}
	section := func(b []byte, id byte, content []byte) []byte {
		return append(leb(append(b, id), len(content)), content...)
	}
	body := []byte{0x00} // no locals
	for i := 0; i < n; i++ {
		body = append(body, 0x02, 0x40, 0x0b) // block (empty type) end
	}
	body = append(body, 0x0b)
	code := append(leb([]byte{0x01}, len(body)), body...)
	m := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	m = section(m, 0x01, []byte{0x01, 0x60, 0x00, 0x00}) // type 0: () -> ()
	m = section(m, 0x03, []byte{0x01, 0x00})             // func 0: type 0
	return section(m, 0x0a, code)
}

// compileThreadedAOT compiles wasm with the threaded Interruptible
// configuration into a temporary file and returns the artifact.
func compileThreadedAOT(wasm []byte) ([]byte, error) {
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
	if err := compiler.CompileBuffer(wasm, path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// probeAtomicStoreStack checks all seven store opcodes, including narrow stores.
// The defective compiler returns -2 instead of 98 after leaving address 0 above 100.
func probeAtomicStoreStack(wasm []byte) (bool, error) {
	m, err := NewModule(wasm, WithMaxThreads(1), WithExecTimeout(substrateObservationLimit))
	if err != nil {
		return false, err
	}
	defer m.Release()
	ok := true
	for _, name := range atomicStoreProbeExports {
		v, err := m.Execute(name)
		if err != nil {
			return false, fmt.Errorf("%s: %w", name, err)
		}
		if len(v) != 1 {
			return false, fmt.Errorf("%s: unexpected result arity", name)
		}
		ok = ok && ToInt32(v[0]) == 98
	}
	return ok, nil
}

var atomicStoreProbeExports = []string{
	"i32.atomic.store", "i64.atomic.store", "i32.atomic.store8", "i32.atomic.store16",
	"i64.atomic.store8", "i64.atomic.store16", "i64.atomic.store32",
}
