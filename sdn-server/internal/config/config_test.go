package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigDoesNotRequireLocalTorRuntime(t *testing.T) {
	cfg := Default()
	if cfg.Tor.Enabled {
		t.Fatal("default config should not start a local tor runtime")
	}
	if cfg.Tor.HiddenServiceEnabled {
		t.Fatal("default config should not publish a tor hidden service")
	}
}

// TestLoadMissingFileReturnsValidatedDefault locks in that Load's
// file-not-found fallback path still runs validate() — the default config's
// flows.mounts entry ("/api/v1/data/") must satisfy the same /api/ prefix
// rule a loaded file is held to, so the guarantee applies unconditionally.
func TestLoadMissingFileReturnsValidatedDefault(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load(missing) = %v, want default config with no error", err)
	}
	if len(cfg.Flows.Mounts) == 0 {
		t.Fatal("expected default config to declare the built-in data-retrieval flow mount")
	}
	for _, m := range cfg.Flows.Mounts {
		if !strings.HasPrefix(m.Path, "/api/") {
			t.Fatalf("default flow mount path %q does not begin with /api/", m.Path)
		}
	}
}

// TestLoadRejectsFlowMountPathOutsideAPIPrefix is the load-bearing guarantee
// behind gap B10.2: a flows.mounts[].path outside /api/ would be registered
// verbatim by RegisterLazyFlowMounts with no auth-wall coverage (the wall's
// isAPIOrPlugin check in cmd/spacedatanetwork/main.go only inspects /api/
// and /orbpro-key-broker/ paths), so such a config must fail to load rather
// than silently create an unauthenticated surface.
func TestLoadRejectsFlowMountPathOutsideAPIPrefix(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yamlDoc := "flows:\n  mounts:\n    - path: /unsafe/mount/\n      flow: com.example.test-flow\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Load with a non-/api/ flow mount path succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "/unsafe/mount/") || !strings.Contains(err.Error(), "/api/") {
		t.Fatalf("Load error = %q, want it to name the offending path and the /api/ requirement", err.Error())
	}
}

// TestLoadAcceptsFlowMountPathUnderAPIPrefix is the positive counterpart to
// TestLoadRejectsFlowMountPathOutsideAPIPrefix: a properly namespaced mount
// must still load cleanly.
func TestLoadAcceptsFlowMountPathUnderAPIPrefix(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yamlDoc := "flows:\n  mounts:\n    - path: /api/v1/custom/\n      flow: com.example.test-flow\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load with an /api/-prefixed flow mount path failed: %v", err)
	}
	if len(cfg.Flows.Mounts) != 1 || cfg.Flows.Mounts[0].Path != "/api/v1/custom/" {
		t.Fatalf("unexpected mounts after load: %+v", cfg.Flows.Mounts)
	}
}
