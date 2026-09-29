//go:build !windows

package wasmrt

import (
	"fmt"
	"os/exec"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// A parked goroutine with a return address outside Go code makes Go's own
// crash report fault inside itself (runtime/traceback.go:459): the storage
// test hang's signature. With the watchdog armed the child ends by SIGABRT
// with every thread's native state printed; the kernel ends it first where it
// does not retry a blocked fault (Linux kills with SIGSEGV).
func TestStalledCrashReportEndsWithNativeThreadState(t *testing.T) {
	out, err, timedOut := runSignalChildWithin(t, "stalled-report", 90*time.Second)
	if timedOut {
		t.Fatalf("the child never ended:\n%s", out)
	}
	requirePlanted(t, out)
	var exit *exec.ExitError
	if !errorsAs(err, &exit) {
		t.Fatalf("child did not fail (err=%v):\n%s", err, out)
	}
	ws, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		t.Fatalf("child was not ended by a signal (err=%v):\n%s", err, out)
	}
	if strings.Contains(out, "REPORT COMPLETED") {
		t.Fatalf("the report completed; the foreign return address was not reached:\n%s", out)
	}
	if runtime.GOOS == "darwin" {
		if ws.Signal() != syscall.SIGABRT || !strings.Contains(out, "[wasmrt] FATAL the Go crash report has printed nothing") ||
			!strings.Contains(out, "last fault") {
			t.Fatalf("the watchdog did not end the stalled report (err=%v):\n%s", err, out)
		}
	}
	t.Logf("stalled report ended by %v", ws.Signal())
}

// The negative control: the same child without the watchdog does not end on
// darwin, which re-executes a blocked fault forever.
func TestStalledCrashReportHangsWithoutTheWatchdog(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only darwin retries a fault raised with the signal blocked")
	}
	out, err, timedOut := runSignalChildWithin(t, "stalled-report-unwatched", 8*time.Second)
	requirePlanted(t, out)
	if !timedOut {
		t.Fatalf("the unwatched child ended by itself (err=%v); the stall this watchdog exists for did not happen:\n%s", err, out)
	}
}

func requirePlanted(t *testing.T, out string) {
	t.Helper()
	if !strings.Contains(out, "PLANTED") {
		t.Fatalf("the child did not plant a foreign return address:\n%s", out)
	}
}

// foreignPC is a return address outside Go code (and outside any mapping).
const foreignPC = uintptr(0x1000)

//go:noinline
func growStack(n int) int {
	var pad [1024]byte
	pad[n%len(pad)] = byte(n)
	if n == 0 {
		return int(pad[0])
	}
	return growStack(n-1) + int(pad[n%len(pad)])
}

// foreignReturnCaller is the frame parkWithForeignReturn returns to; name
// is its own function name.
//
//go:noinline
func foreignReturnCaller(name string, ready chan<- bool, park <-chan struct{}) {
	growStack(32) // nothing after this may copy the stack: a copy would walk it
	parkWithForeignReturn(name, ready, park)
}

// parkWithForeignReturn overwrites its own saved return address with
// foreignPC and parks, as a stack overwritten by a signal frame and a throw
// run on the wrong stack does (sigstack.go).
//
//go:noinline
func parkWithForeignReturn(caller string, ready chan<- bool, park <-chan struct{}) {
	var anchor [2]uintptr
	anchor[0] = 1
	base := unsafe.Pointer(&anchor[0])
	planted := false
	for d := 8; d <= 512 && !planted; d += 8 {
		for _, off := range [2]int{-d, d} {
			p := (*uintptr)(unsafe.Add(base, off))
			v := *p
			if f := runtime.FuncForPC(v); f == nil || f.Name() != caller {
				continue
			}
			*p = foreignPC
			if !callersReach(caller) {
				planted = true // the slot the unwinder reads
				break
			}
			*p = v // a copy of the address, not the slot
		}
	}
	ready <- planted
	<-park
	runtime.KeepAlive(&anchor)
}

// callersReach reports whether the calling goroutine's stack walk reaches fn.
func callersReach(fn string) bool {
	var pcs [16]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(1, pcs[:])])
	for {
		f, more := frames.Next()
		if f.Function == fn {
			return true
		}
		if !more {
			return false
		}
	}
}

// stalledReportChild plants the foreign return address and asks for every
// goroutine's stack.
func stalledReportChild(t *testing.T, watch bool) {
	if watch {
		crashReportStall = 2 * time.Second
		if err := startCrashWatchdog(); err != nil {
			t.Fatal(err)
		}
	}
	// No collection may walk the planted stack before the report does.
	debug.SetGCPercent(-1)
	ready := make(chan bool)
	caller := runtime.FuncForPC(reflect.ValueOf(foreignReturnCaller).Pointer()).Name()
	go foreignReturnCaller(caller, ready, make(chan struct{}))
	if !<-ready {
		t.Fatal("no saved return address found to overwrite")
	}
	fmt.Println("PLANTED a foreign return address in a parked goroutine")
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	fmt.Printf("REPORT COMPLETED (%d bytes)\n", n)
}
