//go:build !windows

package wasmrt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// signalFaultWasm:
//
//	(module
//	  (memory 1)
//	  (func (export "oob") (result i32) i32.const 0x7ffffff0 i32.load)
//	  (func (export "answer") (result i32) i32.const 42))
//
// As AOT code "oob" reaches WasmEdge's guard pages: a SIGSEGV its handler turns
// into a trap.
var signalFaultWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, 0x01, 0x05, 0x01, 0x60,
	0x00, 0x01, 0x7f, 0x03, 0x03, 0x02, 0x00, 0x00, 0x05, 0x03, 0x01, 0x00,
	0x01, 0x07, 0x10, 0x02, 0x03, 0x6f, 0x6f, 0x62, 0x00, 0x00, 0x06, 0x61,
	0x6e, 0x73, 0x77, 0x65, 0x72, 0x00, 0x01, 0x0a, 0x12, 0x02, 0x0b, 0x00,
	0x41, 0xf0, 0xff, 0xff, 0xff, 0x07, 0x28, 0x02, 0x00, 0x0b, 0x04, 0x00,
	0x41, 0x2a, 0x0b,
}

// signalRecurseWasm:
//
//	(module
//	  (memory 1)
//	  (func $f (export "recurse") (param i32)
//	    local.get 0 i32.const 1 i32.add call $f
//	    i32.const 0 local.get 0 i32.store))
//
// The store after the call keeps it from being a tail call, so as AOT code it
// recurses until the native stack runs out.
var signalRecurseWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, 0x01, 0x05, 0x01, 0x60,
	0x01, 0x7f, 0x00, 0x03, 0x02, 0x01, 0x00, 0x05, 0x03, 0x01, 0x00, 0x01,
	0x07, 0x0b, 0x01, 0x07, 0x72, 0x65, 0x63, 0x75, 0x72, 0x73, 0x65, 0x00,
	0x00, 0x0a, 0x12, 0x01, 0x10, 0x00, 0x20, 0x00, 0x41, 0x01, 0x6a, 0x10,
	0x00, 0x41, 0x00, 0x20, 0x00, 0x36, 0x02, 0x00, 0x0b,
}

const signalChildEnv = "SDN_WASMRT_SIGNAL_CHILD"

// runSignalChild runs one scenario in a child process, because the failure
// being tested is the death of the process.
func runSignalChild(t *testing.T, scenario string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalChild$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), signalChildEnv+"="+scenario)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runSignalChildWithin is runSignalChild with a deadline, for scenarios whose
// failure is a process that never ends.
func runSignalChildWithin(t *testing.T, scenario string, limit time.Duration) (out string, err error, timedOut bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSignalChild$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), signalChildEnv+"="+scenario)
	b, err := cmd.CombinedOutput()
	return string(b), err, ctx.Err() != nil
}

func requireChildMarker(t *testing.T, scenario, marker string) {
	t.Helper()
	out, err := runSignalChild(t, scenario)
	if err != nil || !strings.Contains(out, marker) {
		t.Fatalf("%s: child err=%v, want %q in:\n%s", scenario, err, marker, out)
	}
	t.Logf("%s: %s", scenario, lastLines(out, marker))
}

func lastLines(out, marker string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, marker) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// The defect, reproduced: without the pin, one compiled call leaves SIGSEGV at
// SIG_DFL and a recovered nil dereference kills the process. If this starts to
// pass, WasmEdge changed and the pin may no longer be needed.
func TestWasmEdgeResetsGoFaultHandlerWithoutPin(t *testing.T) {
	out, err := runSignalChild(t, "no-pin")
	var exit *exec.ExitError
	if err == nil || !errorsAs(err, &exit) {
		t.Fatalf("unpinned child survived a nil dereference after a compiled call (err=%v):\n%s", err, out)
	}
	if ws, ok := exit.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGSEGV {
		t.Fatalf("unpinned child did not die of SIGSEGV (err=%v):\n%s", err, out)
	}
	if strings.Contains(out, "RECOVERED") {
		t.Fatalf("unpinned child recovered:\n%s", out)
	}
}

func TestGoPanicRecoveredAfterCompiledCall(t *testing.T) {
	requireChildMarker(t, "after-call", "RECOVERED after compiled call")
}

func TestGoPanicRecoveredDuringCompiledCall(t *testing.T) {
	requireChildMarker(t, "during-call", "RECOVERED during compiled call")
}

func TestWasmFaultStillTrapsUnderGoHandler(t *testing.T) {
	// Repeated: the failures this guards (a signal frame left behind by the
	// trap; a darwin trap jump that leaves the thread marked as on its signal
	// stack) each show up in some processes and not in others.
	for i := 0; i < 5; i++ {
		requireChildMarker(t, "wasm-trap", "TRAPPED")
	}
}

// A synchronous call that returns with its thread marked as running on the
// signal stack (what darwin's longjmp leaves after a WasmEdge trap, in some
// processes) clears the mark before Go code runs on it again. Deterministic:
// the child sets the mark itself before every call.
func TestSynchronousCallClearsAStaleSignalStackMark(t *testing.T) {
	out, err := runSignalChild(t, "stale-mark")
	if strings.Contains(out, "NO SIGNAL-STACK MARK") {
		t.Skip("this system keeps no signal-stack mark a jump can leave behind (darwin does)")
	}
	if err != nil || !strings.Contains(out, "CLEARED") {
		t.Fatalf("stale-mark: child err=%v:\n%s", err, out)
	}
	t.Logf("stale-mark: %s", lastLines(out, "CLEARED"))
}

// Goroutines that trapped in WasmEdge (on their own thread, on WasmEdge's
// async threads, and one parked inside a host function of a compiled call
// after a trap on its thread) leave no foreign return address behind: a
// traceback of every goroutine completes, and so does the crash report of a
// panic with GOTRACEBACK=all. A foreign return address makes the report
// fault inside itself (crash_watchdog.go); on darwin it never ends.
func TestTrappedGoroutinesTraceBackCleanly(t *testing.T) {
	out, err, timedOut := runSignalChildWithin(t, "trap-park-traceback", 2*time.Minute)
	if timedOut {
		t.Fatalf("the crash report never ended:\n%s", out)
	}
	var exit *exec.ExitError
	if !errorsAs(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("child did not end with a Go panic's exit status 2 (err=%v):\n%s", err, out)
	}
	if !strings.Contains(out, "STACKS COMPLETE") || !strings.Contains(out, "panic: sdn-trap-park-traceback") {
		t.Fatalf("child did not complete both tracebacks:\n%s", out)
	}
	if strings.Contains(out, "unexpected return pc") {
		t.Fatalf("a goroutine's stack holds a return address outside Go code:\n%s", out)
	}
	for _, fn := range []string{"trapThenParkSync", "trapThenParkAsync", "trapThenParkInHostCall"} {
		if !strings.Contains(out, "wasmrt."+fn) {
			t.Fatalf("the panic's report lacks the goroutine in %s:\n%s", fn, out)
		}
	}
	t.Logf("trap-park-traceback: %s", lastLines(out, "STACKS COMPLETE"))
}

// A fault in native code is not a guest trap: it is named and takes its
// default action, rather than being turned into an engine error (or looping
// through WasmEdge's handler).
func TestNativeFaultIsNotTakenForAGuestTrap(t *testing.T) {
	out, err := runSignalChild(t, "native-crash")
	var exit *exec.ExitError
	if err == nil || !errorsAs(err, &exit) {
		t.Fatalf("child survived a native bad store (err=%v):\n%s", err, out)
	}
	ws, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || (ws.Signal() != syscall.SIGSEGV && ws.Signal() != syscall.SIGBUS) {
		t.Fatalf("native crash did not end in SIGSEGV/SIGBUS (err=%v):\n%s", err, out)
	}
	if !strings.Contains(out, "[wasmrt] FATAL signal") || !strings.Contains(out, "in native code (not guest AOT code)") {
		t.Fatalf("native crash was not named (err=%v):\n%s", err, out)
	}
	if strings.Contains(out, "RETURNED") {
		t.Fatalf("native crash returned:\n%s", out)
	}
}

// Guest recursion that exhausts the native stack is not redirected: there is
// no room to resume in, and on x86-64 the redirect would store to that stack
// from inside the signal handler, where a second fault is never delivered
// (darwin retries it forever). The process ends by the signal, naming it.
func TestGuestStackExhaustionIsNamedNotRedirected(t *testing.T) {
	out, err, timedOut := runSignalChildWithin(t, "stack-exhaustion", 2*time.Minute)
	if timedOut {
		t.Fatalf("the child never ended:\n%s", out)
	}
	var exit *exec.ExitError
	if !errorsAs(err, &exit) {
		t.Fatalf("child survived exhausting its stack (err=%v):\n%s", err, out)
	}
	ws, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || (ws.Signal() != syscall.SIGSEGV && ws.Signal() != syscall.SIGBUS) {
		t.Fatalf("stack exhaustion did not end in SIGSEGV/SIGBUS (err=%v):\n%s", err, out)
	}
	if !strings.Contains(out, "on the thread's own stack") {
		t.Fatalf("stack exhaustion was not named (err=%v):\n%s", err, out)
	}
	if strings.Contains(out, "RETURNED") {
		t.Fatalf("stack exhaustion returned:\n%s", out)
	}
}

func errorsAs(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// spinSink keeps spinWithoutCalls from being optimized away.
var spinSink uint64

// spinWithoutCalls runs Go code with no calls in it, so only an asynchronous
// preemption signal (SIGURG) interrupts it, on whatever stack the kernel
// picks for the thread.
func spinWithoutCalls(n uint64) uint64 {
	var x uint64
	for i := uint64(0); i < n; i++ {
		x += i ^ (x >> 3)
	}
	return x
}

// trapThenParkSync traps on its own thread, then parks there.
func trapThenParkSync(m *Module, traps int, ready chan<- error, park <-chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for i := 0; i < traps; i++ {
		if _, err := m.Execute("oob"); err == nil {
			ready <- fmt.Errorf("sync trap %d did not trap", i)
			return
		}
		m.poisoned.Store(false)
	}
	ready <- nil
	<-park
}

// trapThenParkAsync traps on WasmEdge's async threads, then parks.
func trapThenParkAsync(m *Module, traps int, ready chan<- error, park <-chan struct{}) {
	for i := 0; i < traps; i++ {
		if _, err := m.Execute("oob"); err == nil {
			ready <- fmt.Errorf("async trap %d did not trap", i)
			return
		}
		m.poisoned.Store(false)
	}
	ready <- nil
	<-park
}

// trapThenParkInHostCall traps on its own thread, then parks inside a host
// function of a compiled call on that thread.
func trapThenParkInHostCall(trap, hold *Module, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := trap.Execute("oob"); err == nil {
		ready <- fmt.Errorf("trap before the host call did not trap")
		return
	}
	trap.poisoned.Store(false)
	if _, err := hold.Execute("pin"); err != nil {
		ready <- fmt.Errorf("pin export: %v", err)
	}
}

// derefNil dereferences a nil pointer inside recover and reports the panic.
func derefNil() (recovered interface{}) {
	defer func() { recovered = recover() }()
	var p *int
	return *p
}

func newSignalFaultModule(t *testing.T, opts ...Option) *Module {
	t.Helper()
	aot, err := compileAOTBytes(signalFaultWasm)
	if err != nil {
		t.Fatalf("compile fault module: %v", err)
	}
	m, err := NewModule(aot, opts...)
	if err != nil {
		t.Fatalf("NewModule: %v", err)
	}
	return m
}

// TestSignalChild is the child process of the tests above; inert otherwise.
func TestSignalChild(t *testing.T) {
	scenario := os.Getenv(signalChildEnv)
	if scenario == "" {
		t.Skip("child process only")
	}
	switch scenario {
	case "no-pin":
		signalPinDisabled = true
		m := newSignalFaultModule(t)
		defer m.Release()
		if v, err := m.Execute("answer"); err != nil || ToInt32(v[0]) != 42 {
			t.Fatalf("answer: %v %v", v, err)
		}
		fmt.Printf("RECOVERED without pin: %v\n", derefNil())

	case "after-call":
		started := time.Now()
		if err := EnsureGoSignalHandling(); err != nil {
			t.Fatalf("pin: %v", err)
		}
		pinTook := time.Since(started)
		m := newSignalFaultModule(t)
		defer m.Release()
		for i := 0; i < 3; i++ {
			if v, err := m.Execute("answer"); err != nil || ToInt32(v[0]) != 42 {
				t.Fatalf("answer: %v %v", v, err)
			}
		}
		if !GoFaultHandlerInstalled() {
			t.Fatal("Go's SIGSEGV handler is not installed after a compiled call")
		}
		p := derefNil()
		if p == nil {
			t.Fatal("no panic")
		}
		fmt.Printf("RECOVERED after compiled call: %v (pin took %s)\n", p, pinTook.Round(time.Millisecond))

	case "during-call":
		if err := EnsureGoSignalHandling(); err != nil {
			t.Fatalf("pin: %v", err)
		}
		aot, err := compileAOTBytes(signalPinWasm)
		if err != nil {
			t.Fatal(err)
		}
		inside := make(chan struct{})
		release := make(chan struct{})
		m, err := NewModule(aot, WithHostModule("sdn_signal_pin", []HostFunc{{
			Name: "hold",
			Func: func(interface{}, *wasmedge.CallingFrame, []interface{}) ([]interface{}, wasmedge.Result) {
				close(inside)
				<-release
				return nil, wasmedge.Result_Success
			},
		}}))
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Execute("pin"); err != nil {
				t.Errorf("pin export: %v", err)
			}
		}()
		<-inside
		p := derefNil()
		close(release)
		wg.Wait()
		m.Release()
		if p == nil {
			t.Fatal("no panic")
		}
		fmt.Printf("RECOVERED during compiled call: %v\n", p)

	case "wasm-trap":
		if err := EnsureGoSignalHandling(); err != nil {
			t.Fatalf("pin: %v", err)
		}
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		direct := newSignalFaultModule(t)
		defer direct.Release()
		const syncTraps = 50
		for i := 0; i < syncTraps; i++ {
			if _, err := direct.Execute("oob"); err == nil {
				t.Fatal("out-of-bounds load did not trap")
			}
			// The trap came back through Go's handler (all signals blocked)
			// and WasmEdge's longjmp: this thread must not be left deaf, nor
			// marked as running on its signal stack (darwin's longjmp can
			// leave that mark; see sigstack.go).
			for _, sig := range []syscall.Signal{syscall.SIGURG, syscall.SIGBUS, syscall.SIGSEGV, syscall.SIGPROF} {
				if threadBlocksSignal(sig) {
					t.Fatalf("trap %d left %v blocked on the calling thread", i, sig)
				}
			}
			if signalStackMarked() {
				t.Fatalf("trap %d left the calling thread marked as running on its signal stack", i)
			}
			direct.poisoned.Store(false) // the trap poisons; this test re-enters on purpose
		}
		// The async path runs AOT code on a thread Go did not create.
		async := newSignalFaultModule(t, WithExecTimeout(5*time.Second))
		defer async.Release()
		for i := 0; i < 20; i++ {
			if _, err := async.Execute("oob"); err == nil {
				t.Fatal("out-of-bounds load did not trap on the async path")
			}
			async.poisoned.Store(false)
		}
		if v, err := async.Execute("answer"); err != nil || ToInt32(v[0]) != 42 {
			t.Fatalf("answer after traps: %v %v", v, err)
		}
		if !GoFaultHandlerInstalled() {
			t.Fatal("Go's SIGSEGV handler is not installed after traps")
		}
		// The trapping thread keeps taking signals on the right stack:
		// spin here (locked to it) long enough for many preemption
		// signals (SIGURG) to land while Go code runs.
		spinUntil := time.Now().Add(300 * time.Millisecond)
		spinners := make(chan struct{})
		for k := 0; k < 2*runtime.GOMAXPROCS(0); k++ {
			go func() {
				for time.Now().Before(spinUntil) {
				}
				spinners <- struct{}{}
			}()
		}
		for time.Now().Before(spinUntil) {
		}
		for k := 0; k < 2*runtime.GOMAXPROCS(0); k++ {
			<-spinners
		}
		// time.Now runs on the system stack on darwin, where a signal is
		// taken even on the wrong stack; a loop with no calls is only ever
		// interrupted on this goroutine's own stack.
		spinSink = spinWithoutCalls(1 << 28)
		if p := derefNil(); p == nil {
			t.Fatal("no panic after traps")
		}
		fmt.Printf("TRAPPED %d sync + 20 async out-of-bounds loads; Go handler kept, nil dereference recovered; %d stale signal-stack marks cleared\n",
			syncTraps, SignalStackRepairs())

	case "stale-mark":
		if err := EnsureGoSignalHandling(); err != nil {
			t.Fatalf("pin: %v", err)
		}
		m := newSignalFaultModule(t)
		defer m.Release()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		before := SignalStackRepairs()
		const calls = 50
		for i := 0; i < calls; i++ {
			// SIGURG and SIGPROF wait while the mark stands, so none lands on
			// this goroutine's stack before the call has cleared it.
			if !markSignalStackQuietly() {
				unquietSignalStack()
				fmt.Println("NO SIGNAL-STACK MARK on this system")
				return
			}
			v, err := m.Execute("answer")
			marked := signalStackMarked()
			unquietSignalStack()
			if err != nil || ToInt32(v[0]) != 42 {
				t.Fatalf("answer: %v %v", v, err)
			}
			if marked {
				t.Fatalf("call %d returned with its thread still marked as running on the signal stack", i)
			}
		}
		if got := SignalStackRepairs() - before; got != calls {
			t.Fatalf("%d marks cleared, want %d", got, calls)
		}
		spinSink = spinWithoutCalls(1 << 28)
		if p := derefNil(); p == nil {
			t.Fatal("no panic after the calls")
		}
		fmt.Printf("CLEARED %d stale signal-stack marks, one per call; preemption and a nil dereference after them handled\n", calls)

	case "trap-park-traceback":
		if err := EnsureGoSignalHandling(); err != nil {
			t.Fatalf("pin: %v", err)
		}
		syncMod := newSignalFaultModule(t)
		asyncMod := newSignalFaultModule(t, WithExecTimeout(5*time.Second))
		hostTrapMod := newSignalFaultModule(t)
		aot, err := compileAOTBytes(signalPinWasm)
		if err != nil {
			t.Fatal(err)
		}
		inside := make(chan struct{})
		holdMod, err := NewModule(aot, WithHostModule("sdn_signal_pin", []HostFunc{{
			Name: "hold",
			Func: func(interface{}, *wasmedge.CallingFrame, []interface{}) ([]interface{}, wasmedge.Result) {
				close(inside)
				select {}
			},
		}}))
		if err != nil {
			t.Fatal(err)
		}
		ready := make(chan error, 3)
		park := make(chan struct{})
		go trapThenParkSync(syncMod, 20, ready, park)
		go trapThenParkAsync(asyncMod, 10, ready, park)
		go trapThenParkInHostCall(hostTrapMod, holdMod, ready)
		for i := 0; i < 2; i++ {
			if err := <-ready; err != nil {
				t.Fatal(err)
			}
		}
		select {
		case <-inside:
		case err := <-ready:
			t.Fatalf("host call: %v", err)
		}
		buf := make([]byte, 8<<20)
		n := runtime.Stack(buf, true)
		dump := string(buf[:n])
		if strings.Contains(dump, "unexpected return pc") {
			t.Fatalf("runtime.Stack met a return address outside Go code:\n%s", dump)
		}
		for _, fn := range []string{"trapThenParkSync", "trapThenParkAsync", "trapThenParkInHostCall"} {
			if !strings.Contains(dump, "wasmrt."+fn) {
				t.Fatalf("runtime.Stack lacks the goroutine in %s:\n%s", fn, dump)
			}
		}
		fmt.Printf("STACKS COMPLETE: %d bytes for every goroutine; %d stale signal-stack marks cleared\n", n, SignalStackRepairs())
		debug.SetTraceback("all")
		panic("sdn-trap-park-traceback")

	case "native-crash":
		if err := EnsureGoSignalHandling(); err != nil {
			t.Fatalf("pin: %v", err)
		}
		if signalNativeRanges == 0 {
			t.Fatal("no native text ranges recorded for WasmEdge's image")
		}
		m := newSignalFaultModule(t)
		if _, err := m.Execute("answer"); err != nil {
			t.Fatal(err)
		}
		crashInNativeCode()
		fmt.Println("RETURNED from a native bad store")

	case "stack-exhaustion":
		if err := EnsureGoSignalHandling(); err != nil {
			t.Fatalf("pin: %v", err)
		}
		aot, err := compileAOTBytes(signalRecurseWasm)
		if err != nil {
			t.Fatal(err)
		}
		m, err := NewModule(aot)
		if err != nil {
			t.Fatal(err)
		}
		runtime.LockOSThread()
		_, err = m.Execute("recurse", int32(0))
		fmt.Printf("RETURNED from exhausting the stack: %v\n", err)

	case "stalled-report":
		stalledReportChild(t, true)

	case "stalled-report-unwatched":
		stalledReportChild(t, false)

	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
}
