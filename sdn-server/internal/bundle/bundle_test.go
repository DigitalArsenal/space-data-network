package bundle

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveFromExecutableInsideVMBundleRuntimeDir(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "runtime", "sdn", "spacedatanetwork")
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
}

func TestResolveFromExecutableOutsideRuntimeSdnDirIsNotABundle(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "other", "sdn", "spacedatanetwork")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if layout := ResolveFromExecutable(exe); layout.Root != "" {
		t.Fatalf("expected empty layout, got Root = %q", layout.Root)
	}
}

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
	kuboName := "ipfs"
	if runtime.GOOS == "windows" {
		kuboName = "ipfs.exe"
	}

	if layout.Root != root {
		t.Fatalf("Root = %q, want %q", layout.Root, root)
	}
	if layout.BinDir != filepath.Join(root, "bin") {
		t.Fatalf("BinDir = %q", layout.BinDir)
	}
	if layout.KuboBinary != filepath.Join(root, "runtime", "kubo", kuboName) {
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
	if layout.HDWalletWASM != filepath.Join(root, "runtime", "modules", "hd-wallet-wasi.wasm") {
		t.Fatalf("HDWalletWASM = %q", layout.HDWalletWASM)
	}
	if layout.WalletWASMDir != filepath.Join(root, "runtime", "ui", "wallet-wasm") {
		t.Fatalf("WalletWASMDir = %q", layout.WalletWASMDir)
	}
	if layout.WalletUIDir != filepath.Join(root, "runtime", "ui", "wallet-ui") {
		t.Fatalf("WalletUIDir = %q", layout.WalletUIDir)
	}
	if layout.ManifestPath != filepath.Join(root, "manifest.json") {
		t.Fatalf("ManifestPath = %q", layout.ManifestPath)
	}
}

func TestResolveFromExecutableInsideBundleWithoutManifestReturnsEmptyLayout(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "bin", "spacedatanetwork")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	layout := ResolveFromExecutable(exe)

	if layout != (Layout{}) {
		t.Fatalf("layout = %#v, want empty", layout)
	}
}

func TestResolveFromExecutableOutsideBundleReturnsEmptyOptionalPaths(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "spacedatanetwork")

	layout := ResolveFromExecutable(exe)

	if layout != (Layout{}) {
		t.Fatalf("layout = %#v, want empty", layout)
	}
}
