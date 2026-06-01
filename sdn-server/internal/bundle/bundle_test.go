package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveFromExecutableInsideBundle(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "bin", "spacedatanetwork")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"schema":"org.spacedatanetwork.bundle.v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	layout := ResolveFromExecutable(exe)

	if layout.Root != root {
		t.Fatalf("Root = %q, want %q", layout.Root, root)
	}
	if layout.BinDir != filepath.Join(root, "bin") {
		t.Fatalf("BinDir = %q", layout.BinDir)
	}
	if layout.KuboBinary != filepath.Join(root, "runtime", "kubo", "ipfs") {
		t.Fatalf("KuboBinary = %q", layout.KuboBinary)
	}
	if layout.SDNUIPath != filepath.Join(root, "runtime", "ui", "sdn") {
		t.Fatalf("SDNUIPath = %q", layout.SDNUIPath)
	}
	if layout.WebUIPath != filepath.Join(root, "runtime", "ui", "webui") {
		t.Fatalf("WebUIPath = %q", layout.WebUIPath)
	}
	if layout.UpdaterWASM != filepath.Join(root, "runtime", "modules", "org.spacedatanetwork.updater.wasm") {
		t.Fatalf("UpdaterWASM = %q", layout.UpdaterWASM)
	}
	if layout.ManifestPath != filepath.Join(root, "manifest.json") {
		t.Fatalf("ManifestPath = %q", layout.ManifestPath)
	}
}

func TestResolveFromExecutableOutsideBundleReturnsEmptyOptionalPaths(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "spacedatanetwork")

	layout := ResolveFromExecutable(exe)

	if layout.Root != "" {
		t.Fatalf("Root = %q, want empty", layout.Root)
	}
	if layout.KuboBinary != "" || layout.SDNUIPath != "" || layout.WebUIPath != "" || layout.UpdaterWASM != "" {
		t.Fatalf("runtime paths should be empty outside a bundle: %#v", layout)
	}
}
