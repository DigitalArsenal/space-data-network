//go:build !windows

package wasmrt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	// Repeated: the failure this guards (a signal frame left behind by the
	// trap) showed up in about half of single runs.
	for i := 0; i < 5; i++ {
		requireChildMarker(t, "wasm-trap", "TRAPPED")
	}
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

// runtimeCarriesFaultJmpPatch reports whether the linked WasmEdge jumps out
// of a fault without touching signal state: always on Linux (glibc's longjmp
// keeps no signal-stack flag), and on darwin only with the static build's
// patch 03 (its prefix lists the series it carries).
func runtimeCarriesFaultJmpPatch() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	list, err := os.ReadFile(filepath.Join(os.Getenv("WASMEDGE_DIR"), "sdn-runtime-patches.txt"))
	return err == nil && strings.Contains(string(list), "03-fault-jmp.patch")
}

func errorsAs(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
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
		syncTraps := 50
		if !runtimeCarriesFaultJmpPatch() {
			// darwin's longjmp sets or clears the thread's on-signal-stack
			// flag from a jmp_buf word setjmp never writes: without patch 03
			// a trap on a Go thread can leave the flag set, and Go's next
			// signal there lands on a goroutine stack. That is upstream
			// WasmEdge on darwin, pinned or not; Linux has no such flag.
			fmt.Println("SKIP sync traps: this darwin runtime lacks WasmEdge patch 03-fault-jmp")
			syncTraps = 0
		}
		for i := 0; i < syncTraps; i++ {
			if _, err := direct.Execute("oob"); err == nil {
				t.Fatal("out-of-bounds load did not trap")
			}
			// The trap came back through Go's handler (all signals blocked)
			// and WasmEdge's longjmp: this thread must not be left deaf.
			for _, sig := range []syscall.Signal{syscall.SIGURG, syscall.SIGBUS, syscall.SIGSEGV, syscall.SIGPROF} {
				if threadBlocksSignal(sig) {
					t.Fatalf("trap %d left %v blocked on the calling thread", i, sig)
				}
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
		if p := derefNil(); p == nil {
			t.Fatal("no panic after traps")
		}
		fmt.Printf("TRAPPED %d sync + 20 async out-of-bounds loads; Go handler kept, nil dereference recovered\n", syncTraps)

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

	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
}
