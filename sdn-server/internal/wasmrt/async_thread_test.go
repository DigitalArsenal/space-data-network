//go:build linux

package wasmrt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const asyncThreadChildEnv = "SDN_WASMRT_ASYNC_THREAD_CHILD"

// The runtime must never call pthread_detach. In glibc through at least 2.42
// (sourceware bug 19951, fixed on glibc master, December 2025) that call races
// the exit of the thread it detaches: it marks the thread detached, the thread
// exits and its stack is released and can be unmapped, and pthread_detach
// then reads the thread descriptor inside that stack. Engine CI job
// 135535855695 died that way: SIGSEGV at libpthread.so.0+0x99a0
// (__pthread_detach+32, the read of cancelhandling), which the signal
// forwarder handed to WasmEdge's handler and which then faulted again at 0xd0
// (stack issue 876).
//
// Two places in the runtime detached a running thread:
//   - WasmEdge's Async, for every call with a deadline and every WASI thread
//     spawn (patch 08-async-detached-thread starts the thread detached);
//   - lld's unlinkAsync, once per AOT compile, because the link's output file
//     already exists (patch 09-lld-single-thread links with --threads=1).
//
// The child compiles a module and makes deadline calls with a preloaded
// pthread_detach that ends the process.
func TestAsyncCallThreadsStartDetached(t *testing.T) {
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	probe := filepath.Join(t.TempDir(), "detach-probe.so")
	build := exec.Command(cc, "-shared", "-fPIC", "-O2", "-o", probe, filepath.Join("testdata", "detach-probe.c"))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the pthread_detach probe: %v\n%s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAsyncThreadChild$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), asyncThreadChildEnv+"=1", "LD_PRELOAD="+probe)
	b, err := cmd.CombinedOutput()
	out := string(b)
	if ctx.Err() != nil {
		t.Fatalf("the child did not finish:\n%s", out)
	}
	if !strings.Contains(out, "DETACH-PROBE-LOADED") {
		t.Fatalf("the pthread_detach probe was not loaded into the child:\n%s", out)
	}
	if strings.Contains(out, "DETACH-PROBE-CALLED") {
		t.Fatalf("the runtime called pthread_detach:\n%s", out)
	}
	if err != nil || !strings.Contains(out, "ASYNC-CALLS-DONE") {
		t.Fatalf("child err=%v:\n%s", err, out)
	}
	t.Log(lastLines(out, "ASYNC-CALLS-DONE"))
}

// TestAsyncThreadChild is the child process of the test above; inert otherwise.
func TestAsyncThreadChild(t *testing.T) {
	if os.Getenv(asyncThreadChildEnv) == "" {
		t.Skip("child process only")
	}
	if os.Getenv("LD_PRELOAD") == "" {
		t.Fatal("the child runs only with the pthread_detach probe preloaded")
	}
	// Printed as each phase starts, so a probe exit names the phase.
	fmt.Fprintln(os.Stderr, "ASYNC-CHILD AOT compile")
	m := newSignalFaultModule(t, WithExecTimeout(10*time.Second))
	defer m.Release()
	const calls = 200
	fmt.Fprintf(os.Stderr, "ASYNC-CHILD %d deadline calls\n", calls)
	for i := 0; i < calls; i++ {
		v, err := m.Execute("answer")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if ToInt32(v[0]) != 42 {
			t.Fatalf("call %d: answer returned %v", i, v)
		}
	}
	t.Logf("ASYNC-CALLS-DONE %d deadline calls", calls)
}
