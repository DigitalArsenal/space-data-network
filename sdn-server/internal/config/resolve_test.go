package config

// Locks the CLI/daemon config + key resolution (owner 2026-07-27: "CLI needs to
// actually work with the new architecture").
//
// The reported failure was `show-identity` reading $HOME while the daemon ran
// from /etc. These tests pin the resolution order, the system-discovery tier
// that removes the need to read systemd unit files, and the shared key
// derivation that stops the CLI and the daemon disagreeing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withSystemGlobs points system discovery at a fixture root for the duration of
// a test.
func withSystemGlobs(t *testing.T, globs ...string) {
	t.Helper()
	original := systemConfigGlobs
	systemConfigGlobs = globs
	t.Cleanup(func() { systemConfigGlobs = original })
}

func writeConfig(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("storage:\n  path: /srv/sdn/store\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestResolutionOrder locks the documented precedence end to end.
func TestResolutionOrder(t *testing.T) {
	dir := t.TempDir()
	flagCfg := writeConfig(t, filepath.Join(dir, "flag.yaml"))
	envCfg := writeConfig(t, filepath.Join(dir, "env.yaml"))
	sysCfg := writeConfig(t, filepath.Join(dir, "etc", "node.yaml"))
	withSystemGlobs(t, filepath.Join(dir, "etc", "*.yaml"))

	t.Setenv("SDN_CONFIG", envCfg)

	// 1. flag beats everything
	res, err := ResolvePath(flagCfg)
	if err != nil || res.Path != flagCfg || res.Source != SourceFlag {
		t.Fatalf("flag did not win: %+v (%v)", res, err)
	}

	// 2. env beats system
	res, err = ResolvePath("")
	if err != nil || res.Path != envCfg || res.Source != SourceEnv {
		t.Fatalf("SDN_CONFIG did not win over system: %+v (%v)", res, err)
	}

	// 3. system beats home — THE FIX for the reported bug
	t.Setenv("SDN_CONFIG", "")
	res, err = ResolvePath("")
	if err != nil {
		t.Fatalf("system discovery failed: %v", err)
	}
	if res.Path != sysCfg || res.Source != SourceSystem {
		t.Fatalf("system config not discovered: %+v — an operator would be back to reading unit files", res)
	}

	// 4. home is the fallback when nothing else exists
	withSystemGlobs(t, filepath.Join(dir, "nonexistent", "*.yaml"))
	res, err = ResolvePath("")
	if err != nil {
		t.Fatalf("home fallback failed: %v", err)
	}
	if res.Source != SourceHome {
		t.Fatalf("expected home fallback, got %+v", res)
	}
}

// TestAmbiguousSystemConfigIsAnErrorNotAGuess locks the multi-node case. On a
// host running several profiles, silently picking one would point every command
// at the wrong node — the exact class of bug this resolver exists to end.
func TestAmbiguousSystemConfigIsAnErrorNotAGuess(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, "etc", "node-a.yaml"))
	writeConfig(t, filepath.Join(dir, "etc", "node-b.yaml"))
	withSystemGlobs(t, filepath.Join(dir, "etc", "*.yaml"))
	t.Setenv("SDN_CONFIG", "")

	_, err := ResolvePath("")
	if err == nil {
		t.Fatal("two system configs were silently disambiguated")
	}
	for _, want := range []string{"node-a.yaml", "node-b.yaml", "-c", "SDN_CONFIG"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguity error must list both configs and the override; missing %q in: %v", want, err)
		}
	}
}

// TestKeyDerivationMatchesTheDaemon locks the shared derivation. internal/node
// computes filepath.Dir(storage.path)/keys; if this ever diverges, the CLI looks
// where the daemon does not write — which is the host-01 failure.
func TestKeyDerivationMatchesTheDaemon(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Storage.Path = "/app/data/store"

	if got, want := KeyDir(cfg), "/app/data/keys"; got != want {
		t.Fatalf("KeyDir = %q, want %q (the container shape from deployment/remote)", got, want)
	}
	if got, want := MnemonicPath(cfg), "/app/data/keys/mnemonic"; got != want {
		t.Fatalf("MnemonicPath = %q, want %q", got, want)
	}

	cfg.Storage.Path = "/srv/sdn/store"
	if got, want := KeyDir(cfg), "/srv/sdn/keys"; got != want {
		t.Fatalf("KeyDir = %q, want %q (the host shape)", got, want)
	}

	if KeyDir(nil) != "" || MnemonicPath(nil) != "" {
		t.Fatal("nil config must not produce a path")
	}
}

// TestErrorsNameTheConfigAndTheOverride locks item 2 of the owner's brief: a
// bare ENOENT is useless on a host with several possible nodes.
func TestErrorsNameTheConfigAndTheOverride(t *testing.T) {
	t.Parallel()

	res := Resolution{Path: "/etc/space-data-network/node.yaml", Source: SourceSystem, Exists: true}
	err := DescribeMissingNodeState("node identity (mnemonic)", "/srv/sdn/keys/mnemonic", res)

	for _, want := range []string{
		"/srv/sdn/keys/mnemonic",            // what was missing
		"/etc/space-data-network/node.yaml", // which config produced that path
		"system location",                   // how it was resolved
		"-c", "SDN_CONFIG",                  // how to override
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing-state error must contain %q; got:\n%v", want, err)
		}
	}
}

// TestPermissionErrorNamesTheOwnerAndRefusesToLoosen locks item 3. The key dir
// is 0700 owned by the service user and that is CORRECT — the at-rest
// encryption is hardware-derived, so permissions are what keep another local
// user out. The CLI must never respond by loosening them.
func TestPermissionErrorNamesTheOwnerAndRefusesToLoosen(t *testing.T) {
	t.Parallel()

	res := Resolution{Path: "/etc/space-data-network/node.yaml", Source: SourceSystem, Exists: true}
	err := DescribePermissionDenied("the node mnemonic", "/srv/sdn/keys/mnemonic", "sdn", res)
	msg := err.Error()

	if !strings.Contains(msg, "sdn") {
		t.Fatalf("permission error must name the owning user; got: %s", msg)
	}
	if !strings.Contains(msg, "0700") {
		t.Fatalf("permission error should explain the mode is intentional; got: %s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "do not loosen") {
		t.Fatalf("permission error must tell the operator NOT to weaken permissions; got: %s", msg)
	}

	// Unknown owner still yields a usable message.
	if !strings.Contains(DescribePermissionDenied("x", "/p", "", res).Error(), "the service user") {
		t.Fatal("unknown owner should fall back to a readable phrase")
	}
}

// TestResolutionDescribesNonExistentFiles locks that a resolution can succeed
// while the file is absent, and says so — `config` prints a warning in that
// case rather than implying a node is configured.
func TestResolutionDescribesNonExistentFiles(t *testing.T) {
	t.Parallel()

	missing := Resolution{Path: "/nope/config.yaml", Source: SourceHome, Exists: false}
	if !strings.Contains(missing.Describe(), "DOES NOT EXIST") {
		t.Fatalf("Describe must flag a missing file: %s", missing.Describe())
	}
	present := Resolution{Path: "/etc/x.yaml", Source: SourceSystem, Exists: true}
	if strings.Contains(present.Describe(), "DOES NOT EXIST") {
		t.Fatalf("Describe wrongly flagged an existing file: %s", present.Describe())
	}
}
