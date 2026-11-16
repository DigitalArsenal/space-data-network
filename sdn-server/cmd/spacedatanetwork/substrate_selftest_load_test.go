package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The built command observes the runtime under bounded CPU contention.
func TestSubstrateSelftestUnderCPULoad(t *testing.T) {
	const loadBudget = 5 * time.Minute
	if os.Getenv("SDN_SUBSTRATE_CPU_HOG") == "1" {
		fmt.Fprintln(os.Stdout, "cpu-load-ready")
		until := time.Now().Add(loadBudget)
		state := uint64(1)
		for time.Now().Before(until) {
			// Spend the load budget on CPU work rather than clock calls.
			for i := 0; i < 1<<18; i++ {
				state = state*1664525 + 1013904223
			}
		}
		fmt.Fprintln(os.Stdout, state)
		return
	}
	if runtime.GOOS != "linux" {
		t.Skip("CPU load coverage runs on Linux; native macOS measurements use ambient load")
	}
	binary := os.Getenv("SDN_SUBSTRATE_SELFTEST_BINARY")
	if binary == "" {
		root, err := filepath.Abs(filepath.Join("..", "..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		binary = filepath.Join(t.TempDir(), "spacedatanetwork")
		buildCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		build := exec.CommandContext(buildCtx, "bash", filepath.Join(root, "scripts", "go-with-wasmedge.sh"),
			"build", "-p", "2", "-o", binary, "./cmd/spacedatanetwork")
		build.Dir = filepath.Join(root, "sdn-server")
		build.Env = append(os.Environ(), "SDM_MODULE_UNSIGNED_BUILD=1")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build command: %v\n%s", err, out)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), loadBudget)
	defer cancel()
	run := func() (substrateSelftestReport, error, string) {
		cmd := exec.CommandContext(ctx, binary, "substrate-selftest", "--require-patched")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		data := stdout.Bytes()
		// Native interruption logs can precede the command's JSON report.
		start := bytes.IndexByte(data, '{')
		var report substrateSelftestReport
		if start < 0 {
			return report, fmt.Errorf("missing JSON report: %v", err), stdout.String() + stderr.String()
		}
		if decodeErr := json.NewDecoder(bytes.NewReader(data[start:])).Decode(&report); decodeErr != nil {
			return report, decodeErr, stdout.String() + stderr.String()
		}
		return report, err, stdout.String() + stderr.String()
	}
	baseline, err, output := run()
	if baseline.RuntimeVersion != "" && baseline.ThreadsSpawned == 4 && baseline.AOTChecked &&
		!baseline.Patched && !baseline.Inconclusive && baseline.InterpretedStop.Outcome == "not_shared" && len(baseline.Errors) == 0 &&
		!baseline.StopReachesEveryThread && !baseline.AOTLoopStopChecks && !baseline.AOTAtomicStoreStack &&
		os.Getenv("SDN_WASM_REQUIRE_PATCHED") != "1" {
		t.Skipf("stock runtime: shared stop, AOT loop checks, and atomic stores absent; generated 0 CPU hogs\n%s", output)
	}
	if err != nil || !baseline.Patched || !baseline.NativeHostIO {
		t.Fatalf("initial self-test: %v\n%s", err, output)
	}
	hogs := min(2*runtime.GOMAXPROCS(0), 8)
	const runs = 12
	t.Logf("GOMAXPROCS=%d; starting %d CPU hog processes for %d command runs, bounded to %s",
		runtime.GOMAXPROCS(0), hogs, runs, loadBudget)
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	loadStart := time.Now()
	var children []*exec.Cmd
	t.Cleanup(func() {
		for _, child := range children {
			t.Logf("stopping CPU hog PID %d, command %q", child.Process.Pid, child.Args)
			_ = child.Process.Kill()
			_ = child.Wait()
			if child.ProcessState != nil {
				t.Logf("CPU hog PID %d used %s user and %s system CPU", child.Process.Pid,
					child.ProcessState.UserTime(), child.ProcessState.SystemTime())
			}
		}
	})
	for i := 0; i < hogs; i++ {
		child := exec.CommandContext(ctx, helper, "-test.run=^TestSubstrateSelftestUnderCPULoad$")
		child.Env = append(os.Environ(), "SDN_SUBSTRATE_CPU_HOG=1", "GOMAXPROCS=1")
		ready, err := child.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, child)
		line, err := bufio.NewReader(ready).ReadString('\n')
		if err != nil || line != "cpu-load-ready\n" {
			t.Fatalf("CPU hog readiness: %q, %v", line, err)
		}
	}
	t.Logf("all %d CPU hogs ready after %s", hogs, time.Since(loadStart))
	for i := 1; i <= runs; i++ {
		started := time.Now()
		report, err, output := run()
		if err != nil || !report.Patched || !report.NativeHostIO || len(report.Errors) != 0 {
			t.Fatalf("self-test %d/%d with %d CPU hogs: %v\n%s", i, runs, hogs, err, output)
		}
		t.Logf("self-test %d/%d passed in %s", i, runs, time.Since(started))
	}
	t.Logf("all %d command runs passed with %d CPU hog processes", runs, hogs)
}
