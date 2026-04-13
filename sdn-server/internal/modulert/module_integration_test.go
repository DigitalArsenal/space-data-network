package modulert

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewModuleLoadsUnifiedLicensingArtifact(t *testing.T) {
	t.Parallel()

	wasmPath := filepath.Clean(filepath.Join(
		"..", "..", "..", "..",
		"space-data-network-plugins",
		"packages",
		"licensing",
		"dist",
		"isomorphic",
		"module.wasm",
	))
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	mod, err := NewModule(wasmBytes, nil, &NodeContext{})
	if err != nil {
		t.Fatalf("NewModule(unified licensing artifact) failed: %v", err)
	}
	defer func() {
		if closeErr := mod.Close(); closeErr != nil {
			t.Fatalf("Close() failed: %v", closeErr)
		}
	}()

	manifest := mod.Manifest()
	if manifest == nil {
		t.Fatal("expected manifest to be available")
	}
	if manifest.PluginID != "licensing" {
		t.Fatalf("expected plugin id licensing, got %q", manifest.PluginID)
	}
	if len(manifest.Methods) == 0 {
		t.Fatal("expected unified licensing module to expose methods")
	}
	if len(manifest.Protocols) == 0 {
		t.Fatal("expected unified licensing module to expose protocols")
	}
}
