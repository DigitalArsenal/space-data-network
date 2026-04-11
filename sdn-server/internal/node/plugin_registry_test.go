package node

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/license"
)

func TestLoadPluginRegistryUsesStoragePathAsPluginRootBase(t *testing.T) {
	t.Parallel()

	storagePath := filepath.Join(t.TempDir(), "data", "dev")
	pluginRoot := filepath.Join(storagePath, "license", "plugins")
	if err := os.MkdirAll(pluginRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(pluginRoot) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "sensor-shaders.wasm.enc"), []byte("encrypted-bundle"), 0o600); err != nil {
		t.Fatalf("WriteFile(encrypted bundle) failed: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(pluginRoot, "sensor-shaders.key"),
		[]byte(base64.RawStdEncoding.EncodeToString(make([]byte, 32))),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(bundle key) failed: %v", err)
	}

	catalog := license.PluginCatalogFile{
		Plugins: []license.PluginCatalogEntry{
			{
				ID:            "com.orbpro.sensor-shaders",
				Version:       "local-dev",
				RequiredScope: "orbpro:base",
				EncryptedPath: "sensor-shaders.wasm.enc",
				KeyPath:       "sensor-shaders.key",
				ContentType:   "application/wasm+encrypted",
			},
		},
	}
	rawCatalog, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("json.Marshal(catalog) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "catalog.json"), rawCatalog, 0o600); err != nil {
		t.Fatalf("WriteFile(catalog) failed: %v", err)
	}

	n := &Node{
		config: &config.Config{
			Storage: config.StorageConfig{
				Path: storagePath,
			},
		},
	}

	registry, err := n.loadPluginRegistry()
	if err != nil {
		t.Fatalf("loadPluginRegistry failed: %v", err)
	}
	if registry == nil {
		t.Fatal("expected plugin registry")
	}
	if registry.Count() != 1 {
		t.Fatalf("expected 1 plugin entry, got %d", registry.Count())
	}
	if _, ok := registry.Get("com.orbpro.sensor-shaders"); !ok {
		t.Fatal("expected sensor-shaders entry from storage.path/license/plugins")
	}
}
