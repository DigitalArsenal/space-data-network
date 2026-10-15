package config

import (
	"os"
	"path/filepath"
	"testing"
)

// admin.trusted_proxy_hops (SEC-062) and modules.egress_allow_internal
// (SEC-027) parse from YAML, and their defaults are the safe ones.
func TestSecurityHardeningKeysParse(t *testing.T) {
	if got := Default().Admin.EffectiveTrustedProxyHops(); got != 1 {
		t.Fatalf("default trusted_proxy_hops = %d, want 1", got)
	}
	if len(Default().Modules.EgressAllowInternal) != 0 {
		t.Fatalf("the default config allows internal egress: %v", Default().Modules.EgressAllowInternal)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`admin:
  trusted_proxy_hops: 0
modules:
  egress_allow_internal:
    - "10.20.0.0/16"
    - "127.0.0.1:5001"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Admin.EffectiveTrustedProxyHops(); got != 0 {
		t.Fatalf("trusted_proxy_hops: 0 parsed as %d", got)
	}
	if len(cfg.Modules.EgressAllowInternal) != 2 || cfg.Modules.EgressAllowInternal[1] != "127.0.0.1:5001" {
		t.Fatalf("egress_allow_internal = %v", cfg.Modules.EgressAllowInternal)
	}
	neg := -3
	cfg.Admin.TrustedProxyHops = &neg
	if got := cfg.Admin.EffectiveTrustedProxyHops(); got != 0 {
		t.Fatalf("a negative hop count = %d, want 0", got)
	}
}

// admin.allowed_hosts parses, and a wildcard entry stops the load instead of
// being ignored (NET-03 round 2).
func TestAdminAllowedHostsParseAndRefuseWildcards(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte("admin:\n  allowed_hosts:\n    - Records.Example.org:8443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(good)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Admin.AllowedHosts) != 1 || cfg.Admin.AllowedHosts[0] != "Records.Example.org:8443" {
		t.Fatalf("allowed_hosts = %v", cfg.Admin.AllowedHosts)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("admin:\n  allowed_hosts:\n    - \"*.example.org\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatalf("a wildcard admin.allowed_hosts entry loaded")
	}
}
