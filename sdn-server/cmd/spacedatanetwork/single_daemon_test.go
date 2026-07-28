package main

import (
	"os"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// A box running one daemon is the supported shape; the daemon that IS this
// process must never count itself and refuse to boot.
func TestSingleDaemonCheckIgnoresThisProcess(t *testing.T) {
	if others := otherRunningDaemons(os.Getpid()); len(others) != 0 {
		// A real daemon on the developer's machine is not this test's business;
		// what matters is that OUR pid is never in the list.
		for _, d := range others {
			if d.PID == os.Getpid() {
				t.Fatalf("the checking process counted itself: %s", d.Describe())
			}
		}
	}
}

// The refusal names the other daemons and tells the operator what to do. This
// error is read by someone whose service just failed to start, so it has to
// carry the fix, not just the complaint.
func TestSingleDaemonRefusalIsActionable(t *testing.T) {
	// Build the message the way enforceSingleDaemonPerBox does, from a known
	// pair of daemons, so the assertion does not depend on the host's state.
	others := []config.DaemonProcess{
		{PID: 4242, ConfigPath: "/etc/space-data-network/config.sidecar.yaml"},
		{PID: 4343, ConfigPath: ""},
	}
	lines := make([]string, 0, len(others))
	for _, d := range others {
		lines = append(lines, d.Describe())
	}
	running := strings.Join(lines, "\n  ")

	if !strings.Contains(running, "4242") || !strings.Contains(running, "config.sidecar.yaml") {
		t.Fatalf("running-daemon list must identify the daemon and its config: %q", running)
	}
	// A daemon started without --config must still be identifiable by pid.
	if !strings.Contains(running, "4343") {
		t.Fatalf("a daemon without --config must still be listed: %q", running)
	}
}

// The override is development-only and must be OFF unless explicitly asked for,
// because the whole point of the law is that the unsupported shape cannot be
// reached by accident.
func TestMultiDaemonOverrideDefaultsOff(t *testing.T) {
	t.Setenv(allowMultiDaemonEnv, "")
	original := allowMultiDaemonFlag
	t.Cleanup(func() { allowMultiDaemonFlag = original })
	allowMultiDaemonFlag = false

	if enabled, how := multiDaemonOverrideEnabled(); enabled {
		t.Fatalf("override enabled by default via %q", how)
	}
}

func TestMultiDaemonOverrideRespectsFlagAndEnv(t *testing.T) {
	original := allowMultiDaemonFlag
	t.Cleanup(func() { allowMultiDaemonFlag = original })

	allowMultiDaemonFlag = true
	enabled, how := multiDaemonOverrideEnabled()
	if !enabled || !strings.Contains(how, "--allow-multi-daemon") {
		t.Fatalf("flag override not honoured: enabled=%v how=%q", enabled, how)
	}

	allowMultiDaemonFlag = false
	for _, value := range []string{"1", "true", "yes"} {
		t.Setenv(allowMultiDaemonEnv, value)
		enabled, how = multiDaemonOverrideEnabled()
		if !enabled {
			t.Fatalf("%s=%s did not enable the override", allowMultiDaemonEnv, value)
		}
		if !strings.Contains(how, allowMultiDaemonEnv) {
			t.Fatalf("override provenance %q does not name the env var", how)
		}
	}

	// An explicit falsey value means what it says.
	for _, value := range []string{"0", "false"} {
		t.Setenv(allowMultiDaemonEnv, value)
		if enabled, _ := multiDaemonOverrideEnabled(); enabled {
			t.Fatalf("%s=%s must NOT enable the override", allowMultiDaemonEnv, value)
		}
	}
}

func TestDaemonCommandExposesTheOverrideFlag(t *testing.T) {
	flag := daemonCmd.Flags().Lookup("allow-multi-daemon")
	if flag == nil {
		t.Fatal("daemon command is missing --allow-multi-daemon")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--allow-multi-daemon default = %q, want false", flag.DefValue)
	}
	if !strings.Contains(strings.ToUpper(flag.Usage), "DEVELOPMENT") {
		t.Fatalf("--allow-multi-daemon help must mark it development-only: %q", flag.Usage)
	}
}
