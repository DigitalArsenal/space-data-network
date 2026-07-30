package hostsvc

// The two parsers this package's honesty rests on: which unit is MINE, and what
// did systemd actually say. Both are driven with real captured output rather than
// with a live systemd, so they hold on a laptop, in CI and in a container — none
// of which have the supervisor these lines describe.

import (
	"context"
	"strings"
	"testing"
)

func TestUnitFromCgroupResolvesOnlyAServiceIOwn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			// cgroup v2, host-01's real shape.
			name: "cgroup v2 system slice",
			in:   "0::/system.slice/space-data-network-module-delivery.service\n",
			want: "space-data-network-module-delivery.service",
		},
		{
			name: "cgroup v1 multi-controller",
			in: "12:pids:/system.slice/sdn-retriever.service\n" +
				"11:memory:/system.slice/sdn-retriever.service\n" +
				"1:name=systemd:/system.slice/sdn-retriever.service\n",
			want: "sdn-retriever.service",
		},
		{
			// A .scope is a transient unit (a login session, systemd-run).
			// "Restart my login scope" is not a meaningful operation.
			name: "a scope is not a unit we control",
			in:   "0::/user.slice/user-0.slice/session-3.scope\n",
			want: "",
		},
		{
			// Docker: no systemd unit anywhere in the path, so no supervisor.
			name: "docker container",
			in:   "0::/docker/4f2c9a1b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170\n",
			want: "",
		},
		{
			name: "a nested service takes the innermost",
			in:   "0::/system.slice/outer.service/inner.service\n",
			want: "inner.service",
		},
		{"empty file", "", ""},
		{"garbage", "not a cgroup file at all", ""},
	} {
		if got := unitFromCgroup(tc.in); got != tc.want {
			t.Fatalf("%s: unitFromCgroup = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseShowOutputReadsKeyedLines(t *testing.T) {
	t.Parallel()

	// Real `systemctl show <unit> -p …` shape, including a property systemd
	// answers as empty and one whose value contains an '=' of its own.
	out := strings.Join([]string{
		"ActiveState=active",
		"SubState=running",
		"UnitFileState=enabled",
		"Restart=always",
		"MainPID=755063",
		"Environment=LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge/lib",
		"EmptyProperty=",
		"",
	}, "\n")

	props := parseShowOutput(out)
	for key, want := range map[string]string{
		"ActiveState":   "active",
		"SubState":      "running",
		"UnitFileState": "enabled",
		"Restart":       "always",
		"MainPID":       "755063",
		// Only the FIRST '=' separates: a value carrying '=' survives intact.
		"Environment":   "LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge/lib",
		"EmptyProperty": "",
	} {
		if got := props[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if _, present := props["NotAsked"]; present {
		t.Fatalf("parse invented a key: %v", props)
	}
}

// TestProbeIsFailClosedWhereThereIsNoSystemd: on the machines this test runs on
// (macOS, CI containers) there is no systemd unit owning this process, and the
// probe must therefore report NO supervisor — not a partially filled state that a
// caller could mistake for permission.
func TestProbeIsFailClosedWhereThereIsNoSystemd(t *testing.T) {
	t.Parallel()

	state := Probe(context.Background())
	if state.Detected {
		// The one legitimate way this can be true is a developer running the
		// suite from inside the daemon's own systemd unit; say so instead of
		// asserting a falsehood.
		if state.Supervisor != "systemd" || state.Unit == "" {
			t.Fatalf("detected with an incoherent state: %+v", state)
		}
		t.Skipf("this test process really is under systemd unit %q", state.Unit)
	}
	if state.Supervisor != "" || state.Unit != "" || state.Autostart != "" {
		t.Fatalf("an undetected supervisor must report nothing at all: %+v", state)
	}
}

// TestSystemctlPathIsAbsolute is the Seal Council's ship-time condition as a test
// (Hephaestus, 2026-07-30: "allow-list absolute /usr/bin/systemctl"). A PATH
// lookup on a process-control path is a lookup something else can influence.
func TestSystemctlPathIsAbsolute(t *testing.T) {
	t.Parallel()

	if SystemctlPath != "/usr/bin/systemctl" {
		t.Fatalf("SystemctlPath = %q; the Council allow-listed /usr/bin/systemctl", SystemctlPath)
	}
	if !strings.HasPrefix(SystemctlPath, "/") {
		t.Fatalf("SystemctlPath must be absolute, got %q", SystemctlPath)
	}
}
