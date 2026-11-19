//go:build linux

package wasmrt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	asyncThreadChildEnv = "SDN_WASMRT_ASYNC_THREAD_CHILD"
	detachProbeArmedEnv = "SDN_DETACH_PROBE_ARMED"
)

// A call with a deadline runs on a thread WasmEdge's Async starts. Upstream
// starts it joinable and then calls pthread_detach on it. On glibc before 2.34
// (the release container's 2.31) that call races the thread's exit: it marks
// the thread detached, the thread exits and its stack is released and can be
// unmapped, and pthread_detach then reads the thread descriptor inside that
// stack. Engine CI job 135535855695 died that way: SIGSEGV at
// libpthread.so.0+0x99a0 (__pthread_detach+32, the read of cancelhandling),
// which the signal forwarder handed to WasmEdge's handler and which then
// faulted again at 0xd0 (stack issue 876). The runtime must start the thread
// detached and never call pthread_detach. The child runs deadline calls with
// a preloaded pthread_detach that ends the process once armed.
func TestAsyncCallThreadsStartDetached(t *testing.T) {
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	probe := filepath.Join(t.TempDir(), "detach-probe.so")
	build := exec.Command(cc, "-shared", "-fPIC", "-O2", "-o", probe, filepath.Join("testdata", "detach-probe.c"), "-ldl")
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
	if strings.Contains(out, "DETACH-PROBE") {
		t.Fatalf("the runtime called pthread_detach on a thread it started:\n%s", out)
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
	// The AOT compile calls pthread_detach inside LLVM, not through Async, so
	// it runs before the probe is armed: only the calls are under test.
	m := newSignalFaultModule(t, WithExecTimeout(10*time.Second))
	defer m.Release()
	// With cgo, os.Setenv sets the C environment the probe reads.
	if err := os.Setenv(detachProbeArmedEnv, "1"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv(detachProbeArmedEnv)
	const calls = 200
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
