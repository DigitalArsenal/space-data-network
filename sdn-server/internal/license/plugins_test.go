package license

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPluginRegistryNormalizesDomainPolicy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pluginDir := filepath.Join(root, "module")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "bundle.wasm.enc"), []byte("encrypted"), 0o600); err != nil {
		t.Fatalf("WriteFile(bundle) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "bundle.key"), []byte("c2VjcmV0"), 0o600); err != nil {
		t.Fatalf("WriteFile(key) failed: %v", err)
	}

	catalog := PluginCatalogFile{
		Plugins: []PluginCatalogEntry{
			{
				ID:                "com.example.module",
				Version:           "1.0.0",
				EncryptedPath:     "module/bundle.wasm.enc",
				KeyPath:           "module/bundle.key",
				AllowedDomains:    []string{"App.Example.com", "example.com", "app.example.com"},
				MaxGrantTimeoutMs: 180_000,
			},
		},
	}
	rawCatalog, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, defaultPluginCatalogFile), rawCatalog, 0o600); err != nil {
		t.Fatalf("WriteFile(catalog) failed: %v", err)
	}

	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	asset, ok := reg.Get("com.example.module")
	if !ok {
		t.Fatal("expected loaded asset")
	}
	if got := asset.GrantTimeoutLimitMs(); got != 180_000 {
		t.Fatalf("GrantTimeoutLimitMs = %d", got)
	}
	if !asset.AllowsDomain("api.example.com") {
		t.Fatal("expected subdomain to satisfy allowed_domains policy")
	}
	if asset.AllowsDomain("evil.example.net") {
		t.Fatal("unexpected domain policy match")
	}
}
