package update

// A detached helper respawns its daemon from argv, so it carries the daemon's
// whole environment (2026-10-07, local nodes on the lane: a provider's
// HTTPS_PROXY and GOMEMLIMIT would otherwise be gone after its first in-place
// upgrade).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDetachedSelfUpgradeKeepsTheDaemonsEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in helper is a shell script")
	}
	t.Setenv("INVOCATION_ID", "") // not under systemd: the detached path
	t.Setenv("HTTPS_PROXY", "socks5://127.0.0.1:17890")
	t.Setenv("GOMEMLIMIT", "2GiB")
	root := t.TempDir()
	paths := PathsFor(root)
	if err := os.MkdirAll(paths.Updates, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "helper-env.txt")
	helper := filepath.Join(root, "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nenv > "+out+".tmp && mv "+out+".tmp "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	launch, err := LaunchSelfUpgrade(paths, SelfUpgradeOptions{UpdateID: "sdn-cli-bundle-test", SourceExecutable: helper})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Mode != "detached-session" {
		t.Fatalf("launch mode = %q, want detached-session", launch.Mode)
	}
	var env []byte
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if env, err = os.ReadFile(out); err == nil {
			break
		}
	}
	for _, want := range []string{"HTTPS_PROXY=socks5://127.0.0.1:17890", "GOMEMLIMIT=2GiB"} {
		if !strings.Contains(string(env), want+"\n") {
			t.Fatalf("the helper, which respawns the daemon, started without %s", want)
		}
	}
}
