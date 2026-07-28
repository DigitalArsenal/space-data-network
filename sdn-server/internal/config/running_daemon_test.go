package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain neutralises process-table discovery for the whole package.
//
// Without this, every config test reads the DEVELOPER'S machine: a dev daemon
// running locally would resolve config for tests that are about the directory
// scan, and the suite would pass or fail depending on what happens to be
// running. Tests that exercise discovery opt back in via useProcFixture.
// An empty procRoot makes os.ReadDir fail, and the ps stub returns nothing.
func TestMain(m *testing.M) {
	procRoot = ""
	psCommand = func() ([]byte, error) { return nil, fmt.Errorf("process table disabled in tests") }
	os.Exit(m.Run())
}

// writeProcFixture builds a fake procfs: one directory per pid holding a
// NUL-separated cmdline, exactly as the kernel presents it.
func writeProcFixture(t *testing.T, procs map[int][]string) string {
	t.Helper()
	root := t.TempDir()
	for pid, args := range procs {
		dir := filepath.Join(root, fmt.Sprint(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		body := strings.Join(args, "\x00") + "\x00"
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(body), 0o644); err != nil {
			t.Fatalf("write cmdline: %v", err)
		}
	}
	return root
}

// useProcFixture points discovery at a fixture procfs and neutralises the ps
// fallback, so a test can never accidentally read the real machine.
func useProcFixture(t *testing.T, procs map[int][]string) {
	t.Helper()
	root := writeProcFixture(t, procs)
	oldRoot, oldPS := procRoot, psCommand
	procRoot = root
	psCommand = func() ([]byte, error) { return nil, fmt.Errorf("ps disabled in test") }
	t.Cleanup(func() { procRoot, psCommand = oldRoot, oldPS })
}

// isolateEnv clears the higher-priority tiers so a test exercises discovery.
func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SDN_CONFIG", "")
}

// useEmptySystemGlobs makes the directory scan find nothing, isolating the
// daemon tier; tests that want the fallthrough set their own globs.
func useSystemGlobs(t *testing.T, globs []string) {
	t.Helper()
	old := systemConfigGlobs
	systemConfigGlobs = globs
	t.Cleanup(func() { systemConfigGlobs = old })
}

// hostOneShape is the layout the owner hit: a real /etc directory holding TWO
// yaml files, and one daemon started with an explicit --config.
func hostOneShape(t *testing.T) (etcDir string, nodeConfig string) {
	t.Helper()
	etcDir = t.TempDir()
	nodeConfig = filepath.Join(etcDir, "node.yaml")
	sidecar := filepath.Join(etcDir, "celestrak.yaml")
	for _, p := range []string{nodeConfig, sidecar} {
		if err := os.WriteFile(p, []byte("storage_path: /var/lib/sdn\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	useSystemGlobs(t, []string{filepath.Join(etcDir, "*.yaml")})
	return etcDir, nodeConfig
}

// THE REPORTED BUG. One daemon, two yaml files in /etc. Before the daemon tier
// this errored as "found 2 configs"; it must now resolve silently to the config
// the daemon is actually running with.
func TestOneDaemonWithExplicitConfigBeatsAmbiguousDirectory(t *testing.T) {
	isolateEnv(t)
	_, nodeConfig := hostOneShape(t)
	useProcFixture(t, map[int][]string{
		4242: {"/usr/local/bin/spacedatanetwork", "daemon", "--config", nodeConfig},
	})

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath returned an error on the host-01 shape: %v", err)
	}
	if res.Path != nodeConfig {
		t.Errorf("resolved %q, want the running daemon's config %q", res.Path, nodeConfig)
	}
	if !strings.Contains(string(res.Source), "running daemon") ||
		!strings.Contains(string(res.Source), "4242") {
		t.Errorf("provenance = %q, want it to name the running daemon and its pid", res.Source)
	}
	if !res.Exists {
		t.Error("resolution reports the daemon's config does not exist")
	}
}

func TestDaemonConfigFlagSpellings(t *testing.T) {
	isolateEnv(t)
	useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
	cfg := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(cfg, []byte("storage_path: /var/lib/sdn\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	for name, argv := range map[string][]string{
		"--config <v>": {"/usr/local/bin/spacedatanetwork", "daemon", "--config", cfg},
		"--config=<v>": {"/usr/local/bin/spacedatanetwork", "daemon", "--config=" + cfg},
		"-c <v>":       {"/usr/local/bin/spacedatanetwork", "daemon", "-c", cfg},
		"-c=<v>":       {"/usr/local/bin/spacedatanetwork", "daemon", "-c=" + cfg},
		"flag first":   {"/usr/local/bin/spacedatanetwork", "--config", cfg, "daemon"},
		"sdn-server":   {"/opt/sdn/sdn-server", "daemon", "--config", cfg},
	} {
		t.Run(name, func(t *testing.T) {
			useProcFixture(t, map[int][]string{99: argv})
			res, err := ResolvePath("")
			if err != nil {
				t.Fatalf("ResolvePath: %v", err)
			}
			if res.Path != cfg {
				t.Errorf("resolved %q, want %q", res.Path, cfg)
			}
		})
	}
}

// The container shape: the daemon is pid 1 and procfs is the only source.
func TestContainerPid1DaemonIsFound(t *testing.T) {
	isolateEnv(t)
	cfg := "/app/config/node.yaml"
	useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
	useProcFixture(t, map[int][]string{
		1: {"/usr/local/bin/spacedatanetwork", "daemon", "--config", cfg},
	})

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path != cfg {
		t.Errorf("resolved %q, want the pid-1 daemon's config %q", res.Path, cfg)
	}
	if !strings.Contains(string(res.Source), "pid 1") {
		t.Errorf("provenance = %q, want it to name pid 1", res.Source)
	}
}

// A daemon started WITHOUT --config defaulted; we must default through the same
// code path rather than inventing an answer.
func TestDaemonWithoutExplicitConfigFallsThrough(t *testing.T) {
	isolateEnv(t)
	etcDir := t.TempDir()
	only := filepath.Join(etcDir, "node.yaml")
	if err := os.WriteFile(only, []byte("storage_path: /var/lib/sdn\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	useSystemGlobs(t, []string{filepath.Join(etcDir, "*.yaml")})
	useProcFixture(t, map[int][]string{
		77: {"/usr/local/bin/spacedatanetwork", "daemon"},
	})

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path != only {
		t.Errorf("resolved %q, want the directory-scan result %q", res.Path, only)
	}
	if res.Source != SourceSystem {
		t.Errorf("source = %q, want the system tier", res.Source)
	}
}

// Several daemons IS a real ambiguity — but it must be reported as daemons,
// not as a list of files.
func TestTwoDaemonsReportTheDaemons(t *testing.T) {
	isolateEnv(t)
	useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
	useProcFixture(t, map[int][]string{
		11: {"/usr/local/bin/spacedatanetwork", "daemon", "--config", "/etc/space-data-network/a.yaml"},
		22: {"/usr/local/bin/spacedatanetwork", "daemon", "--config", "/etc/space-data-network/b.yaml"},
	})

	_, err := ResolvePath("")
	if err == nil {
		t.Fatal("two running daemons must be an error, not a guess")
	}
	for _, want := range []string{"pid 11", "pid 22", "a.yaml", "b.yaml", "-c <config>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err)
		}
	}
}

// Zero daemons, one config: the directory scan still works.
func TestNoDaemonSingleConfigUsesDirectoryScan(t *testing.T) {
	isolateEnv(t)
	etcDir := t.TempDir()
	only := filepath.Join(etcDir, "node.yaml")
	if err := os.WriteFile(only, []byte("storage_path: /var/lib/sdn\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	useSystemGlobs(t, []string{filepath.Join(etcDir, "*.yaml")})
	useProcFixture(t, map[int][]string{})

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path != only || res.Source != SourceSystem {
		t.Errorf("resolved %q from %q, want %q from the system tier", res.Path, res.Source, only)
	}
}

// Zero daemons, two configs: genuinely ambiguous, still an error.
func TestNoDaemonTwoConfigsStillErrors(t *testing.T) {
	isolateEnv(t)
	hostOneShape(t)
	useProcFixture(t, map[int][]string{})

	if _, err := ResolvePath(""); err == nil {
		t.Fatal("no daemon + two configs is genuinely ambiguous and must error")
	}
}

// The CLI asking the question is itself a spacedatanetwork process. It must
// never be mistaken for a daemon.
func TestCLIProcessesAreNotDaemons(t *testing.T) {
	isolateEnv(t)
	etcDir := t.TempDir()
	only := filepath.Join(etcDir, "node.yaml")
	if err := os.WriteFile(only, []byte("storage_path: /var/lib/sdn\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	useSystemGlobs(t, []string{filepath.Join(etcDir, "*.yaml")})
	useProcFixture(t, map[int][]string{
		31: {"/usr/local/bin/spacedatanetwork", "show-identity"},
		32: {"/usr/local/bin/spacedatanetwork", "peers", "list", "--config", "/etc/space-data-network/other.yaml"},
		33: {"/usr/bin/grep", "daemon", "--config", "/etc/space-data-network/decoy.yaml"},
	})

	if got := FindRunningDaemons(); len(got) != 0 {
		t.Fatalf("found %d daemons among CLI/unrelated processes: %+v", len(got), got)
	}
	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path != only {
		t.Errorf("resolved %q, want the directory-scan result %q", res.Path, only)
	}
}

// Explicit operator intent still outranks discovery.
func TestFlagAndEnvStillOutrankRunningDaemon(t *testing.T) {
	useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
	useProcFixture(t, map[int][]string{
		55: {"/usr/local/bin/spacedatanetwork", "daemon", "--config", "/etc/space-data-network/daemon.yaml"},
	})

	t.Setenv("SDN_CONFIG", "")
	res, err := ResolvePath("/tmp/explicit.yaml")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path != "/tmp/explicit.yaml" || res.Source != SourceFlag {
		t.Errorf("flag lost to discovery: %q from %q", res.Path, res.Source)
	}

	t.Setenv("SDN_CONFIG", "/tmp/env.yaml")
	res, err = ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path != "/tmp/env.yaml" || res.Source != SourceEnv {
		t.Errorf("SDN_CONFIG lost to discovery: %q from %q", res.Path, res.Source)
	}
}

// An unreadable process table must never become a new way for the CLI to fail.
func TestUnreadableProcessTableFallsThrough(t *testing.T) {
	isolateEnv(t)
	etcDir := t.TempDir()
	only := filepath.Join(etcDir, "node.yaml")
	if err := os.WriteFile(only, []byte("storage_path: /var/lib/sdn\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	useSystemGlobs(t, []string{filepath.Join(etcDir, "*.yaml")})

	oldRoot, oldPS := procRoot, psCommand
	procRoot = filepath.Join(t.TempDir(), "does-not-exist")
	psCommand = func() ([]byte, error) { return nil, fmt.Errorf("ps unavailable") }
	t.Cleanup(func() { procRoot, psCommand = oldRoot, oldPS })

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath must not fail when the process table is unreadable: %v", err)
	}
	if res.Path != only {
		t.Errorf("resolved %q, want %q", res.Path, only)
	}
}

// The darwin path: discovery must work off `ps` output, not just procfs.
func TestPSFallbackFindsDaemon(t *testing.T) {
	isolateEnv(t)
	useSystemGlobs(t, []string{filepath.Join(t.TempDir(), "*.yaml")})
	cfg := "/etc/space-data-network/node.yaml"

	oldRoot, oldPS := procRoot, psCommand
	procRoot = filepath.Join(t.TempDir(), "no-procfs")
	psCommand = func() ([]byte, error) {
		return []byte(
			"  501 /usr/sbin/cupsd -l\n" +
				" 8080 /usr/local/bin/spacedatanetwork daemon --config " + cfg + "\n" +
				" 8081 /usr/local/bin/spacedatanetwork show-identity\n"), nil
	}
	t.Cleanup(func() { procRoot, psCommand = oldRoot, oldPS })

	res, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if res.Path != cfg {
		t.Errorf("resolved %q, want %q from the ps fallback", res.Path, cfg)
	}
	if !strings.Contains(string(res.Source), "8080") {
		t.Errorf("provenance = %q, want the daemon pid", res.Source)
	}
}
