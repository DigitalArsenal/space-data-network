//go:build unix

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The running-daemon tier reads a process table every account can read, and
// an argv is whatever its process says. These pin that another regular
// account's process is never the caller's node.

const (
	callerUID   = 1500 // a regular account, on Linux and on macOS
	strangerUID = 1501 // another regular account
)

// useOwners gives fixture pids owners (default: the caller) and fixes who is
// asking, so the tests do not depend on the account running them.
func useOwners(t *testing.T, owners map[int]int) {
	t.Helper()
	oldOwner, oldEUID := procOwner, currentEUID
	procOwner = func(dir string) (int, bool) {
		pid, err := strconv.Atoi(filepath.Base(dir))
		if err != nil {
			return 0, false
		}
		if uid, ok := owners[pid]; ok {
			return uid, true
		}
		return callerUID, true
	}
	currentEUID = func() int { return callerUID }
	t.Cleanup(func() { procOwner, currentEUID = oldOwner, oldEUID })
}

// The reported spoof: a stranger's process named spacedatanetwork, claiming
// `daemon --config /tmp/evil.yaml`, was the caller's "running daemon", so a
// plain `init` or `key fresh-password-file` loaded the stranger's config. It
// must be ignored, and resolution falls through as if nothing ran.
func TestAnotherAccountsDaemonIsNotTheCallersNode(t *testing.T) {
	isolateEnv(t)
	useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
	evil := filepath.Join(t.TempDir(), "evil.yaml")
	if err := os.WriteFile(evil, []byte("storage:\n  path: /tmp/evil/data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useProcFixture(t, map[int][]string{
		666: {"spacedatanetwork", "daemon", "--config", evil},
	})
	useOwners(t, map[int]int{666: strangerUID})

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path == evil || strings.HasPrefix(string(res.Source), SourceRunningDaemonPrefix) {
		t.Fatalf("resolved %q from %q: another account's process chose this caller's config", res.Path, res.Source)
	}
	if res.Source != SourceHome {
		t.Errorf("source = %q, want the home default (nothing of the caller's runs)", res.Source)
	}
	if got := FindRunningDaemons(); len(got) != 1 || got[0].UID != strangerUID || !got[0].OwnerKnown {
		t.Errorf("FindRunningDaemons must still list every node (the one-node-per-box check counts them all): %+v", got)
	}
}

// A stranger's process beside the caller's own daemon must not turn the
// caller's unambiguous answer into an ambiguity error either.
func TestAnotherAccountsDaemonDoesNotMakeTheCallersAmbiguous(t *testing.T) {
	isolateEnv(t)
	useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
	mine := "/home/me/.spacedatanetwork/config.yaml"
	useProcFixture(t, map[int][]string{
		100: {"/home/me/.spacedatanetwork/bin/spacedatanetwork", "daemon", "--config", mine},
		666: {"spacedatanetwork", "daemon", "--config", "/tmp/evil.yaml"},
	})
	useOwners(t, map[int]int{666: strangerUID})

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path != mine || !strings.Contains(string(res.Source), "pid 100") {
		t.Errorf("resolved %q from %q, want the caller's own daemon %q", res.Path, res.Source, mine)
	}
}

// Root and system accounts cannot be impersonated by a regular user, and the
// deployed shapes run there: host-01 (root), the .deb/.rpm unit (User=sdn).
func TestRootAndSystemAccountDaemonsStillAnswer(t *testing.T) {
	for name, uid := range map[string]int{"root": 0, "system account": 1, "the caller": callerUID} {
		t.Run(name, func(t *testing.T) {
			isolateEnv(t)
			useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
			cfg := "/etc/spacedatanetwork/config.yaml"
			useProcFixture(t, map[int][]string{
				321: {"/opt/spacedatanetwork/bin/spacedatanetwork", "daemon", "--config", cfg},
			})
			useOwners(t, map[int]int{321: uid})
			res, err := ResolvePath("")
			if err != nil {
				t.Fatalf("ResolvePath: %v", err)
			}
			if res.Path != cfg {
				t.Errorf("resolved %q from %q, want the uid-%d daemon's config %q", res.Path, res.Source, uid, cfg)
			}
		})
	}
}

// A process whose owner cannot be read is not trusted.
func TestDaemonOfUnknownOwnerIsIgnored(t *testing.T) {
	isolateEnv(t)
	useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
	useProcFixture(t, map[int][]string{
		7: {"spacedatanetwork", "daemon", "--config", "/tmp/who.yaml"},
	})
	old := procOwner
	procOwner = func(string) (int, bool) { return 0, false }
	t.Cleanup(func() { procOwner = old })

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path == "/tmp/who.yaml" {
		t.Fatalf("a daemon of unknown owner chose the config")
	}
}

// The darwin path (ps) carries the owner too.
func TestPSFallbackIgnoresAnotherAccountsDaemon(t *testing.T) {
	isolateEnv(t)
	useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
	oldRoot, oldPS, oldEUID := procRoot, psCommand, currentEUID
	procRoot = filepath.Join(t.TempDir(), "no-procfs")
	psCommand = func() ([]byte, error) {
		return []byte(fmt.Sprintf(
			" 9001 %d spacedatanetwork daemon --config /tmp/evil.yaml\n"+
				" 9002 %d /usr/local/bin/spacedatanetwork show-identity\n", strangerUID, callerUID)), nil
	}
	currentEUID = func() int { return callerUID }
	t.Cleanup(func() { procRoot, psCommand, currentEUID = oldRoot, oldPS, oldEUID })

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path == "/tmp/evil.yaml" {
		t.Fatalf("ps fallback: another account's process chose the config (%q)", res.Source)
	}
}

func TestFirstRegularUIDReadsLoginDefs(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS: regular accounts start at 501; there is no login.defs")
	}
	defs := filepath.Join(t.TempDir(), "login.defs")
	if err := os.WriteFile(defs, []byte("# comment\nUID_MIN\t\t 2000\nUID_MAX 60000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := loginDefsPath
	loginDefsPath = defs
	t.Cleanup(func() { loginDefsPath = old })
	if got := firstRegularUID(); got != 2000 {
		t.Errorf("firstRegularUID = %d, want UID_MIN 2000", got)
	}
	loginDefsPath = filepath.Join(t.TempDir(), "absent")
	if got := firstRegularUID(); got != 1000 {
		t.Errorf("firstRegularUID without login.defs = %d, want 1000", got)
	}
}
