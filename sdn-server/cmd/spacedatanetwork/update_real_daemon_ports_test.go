package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreeLoopbackPortAndWaitRealReadyFailFast(t *testing.T) {
	seen := make(map[int]struct{}, 32)
	for i := 0; i < 32; i++ {
		port := freeLoopbackPort(t)
		if port < loopbackPortRangeBase || port >= loopbackPortRangeBase+loopbackPortRangeSpan {
			t.Fatalf("freeLoopbackPort returned %d, want non-ephemeral range [%d, %d)", port, loopbackPortRangeBase, loopbackPortRangeBase+loopbackPortRangeSpan)
		}
		if _, dup := seen[port]; dup {
			t.Fatalf("freeLoopbackPort returned duplicate port %d", port)
		}
		seen[port] = struct{}{}
	}

	logPath := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(logPath, []byte("admin server listen 127.0.0.1:20001: bind: address already in use\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreachable := fmt.Sprintf("http://127.0.0.1:%d", freeLoopbackPort(t))

	exited := make(chan struct{})
	close(exited)
	start := time.Now()
	err := pollRealReadyOrExit(unreachable, 10*time.Minute, logPath, exited)
	if err == nil || !strings.Contains(err.Error(), "exited before becoming ready") {
		t.Fatalf("expected fast exit failure, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("pollRealReadyOrExit took %s after process exit, want immediate failure", elapsed)
	}

	start = time.Now()
	err = pollRealReadyOrExit(unreachable, 10*time.Minute, logPath, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to bind its admin listener") {
		t.Fatalf("expected fast bind-error failure, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("pollRealReadyOrExit took %s after bind error, want immediate failure", elapsed)
	}
}
