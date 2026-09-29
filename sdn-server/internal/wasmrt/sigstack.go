package wasmrt

// A WASMEDGE TRAP LEAVES NO SIGNAL-STACK MARK ON A GO THREAD.
//
// WasmEdge leaves a trapping call with longjmp. On darwin that longjmp can
// leave the thread marked as running on its alternate signal stack
// (sigstack_native.c has the mechanism and the measurements). Go runs every
// signal handler on that stack, so while the mark stands the kernel delivers
// the next signal on the thread (a preemption SIGURG, a SIGPROF) on the
// current stack instead: a goroutine stack. Go throws there ("signal 16
// received but handler not on signal stack") from inside the handler, with
// every signal blocked, on a stack it takes to be 32 KiB of system stack below
// the goroutine's SP (runtime/cgocall.go callbackUpdateSystemStack). The
// throw's frames overwrite the stacks of the goroutines parked below; its
// report then walks one of them, meets a return address that is not Go code,
// and faults on gp.m.incgo of a goroutine with no M (runtime/traceback.go:459
// in Go 1.26). With the fault blocked, darwin re-executes the load forever:
// one thread at 100% CPU, no report, no exit: the storage test hang's
// signature, instruction for instruction. Reproduced end to end with the
// upstream darwin library (2026-09-29): of 16 processes, 3 had traps leave
// the mark, and 2 of those hung this way.
//
// The static release build carries WasmEdge patch 03-fault-jmp and never sets
// the mark. For every other runtime a synchronous call clears a stale mark on
// its own thread before returning (Module.runSync); crash_watchdog.go ends any
// report that still stalls.

/*
#include "sigstack_native.h"
*/
import "C"

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
)

// signalStackRepairOn is whether a native call can leave a Go thread marked
// as running on its signal stack: darwin's longjmp can, glibc's cannot.
const signalStackRepairOn = runtime.GOOS == "darwin"

var (
	signalStackRepairs    atomic.Int64
	signalStackRepairNote sync.Once
	signalStackRepairFail sync.Once
)

// repairSignalStack clears a stale signal-stack mark on the calling thread.
// The caller must be locked to the thread that ran the native code.
func repairSignalStack() {
	switch C.sdn_signal_stack_repair() {
	case 1:
		signalStackRepairs.Add(1)
		signalStackRepairNote.Do(func() {
			fmt.Fprintln(os.Stderr, "[wasmrt] WARN a WasmEdge trap left its thread marked as running on the signal stack (darwin longjmp; this runtime lacks WasmEdge patch 03-fault-jmp): cleared after every synchronous call")
		})
	case -1:
		signalStackRepairFail.Do(func() {
			fmt.Fprintln(os.Stderr, "[wasmrt] ERROR a thread stays marked as running on the signal stack after a WasmEdge call: the next signal there lands on a goroutine stack")
		})
	}
}

// SignalStackRepairs is how many stale signal-stack marks synchronous calls
// have cleared in this process.
func SignalStackRepairs() int64 { return signalStackRepairs.Load() }

// signalStackMarked reports whether the calling thread is marked as running
// on its signal stack (tests; the caller must be locked to its thread).
func signalStackMarked() bool { return C.sdn_signal_stack_marked() == 1 }

// markSignalStackQuietly marks the calling thread as darwin/arm64's longjmp
// can, with SIGURG and SIGPROF blocked on it until unquietSignalStack (tests;
// the caller must be locked to its thread). False, with nothing blocked, where
// the mark does not exist.
func markSignalStackQuietly() bool { return C.sdn_signal_stack_mark(1) == 1 }

func unquietSignalStack() { C.sdn_signal_stack_unquiet() }
