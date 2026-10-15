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
