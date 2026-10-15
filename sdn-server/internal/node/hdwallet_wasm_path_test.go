package node

// The HD-wallet wasm is what turns this node's mnemonic into its PEER IDENTITY.
// Resolving it out of ANOTHER install's directory is how a purge of a retired
// node silently re-identifies a live one, so the daemon's own install must come
// first in the search order.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

func TestExecutableRelativeWalletWasmPathsPreferOwnInstall(t *testing.T) {
	t.Parallel()

	paths := executableRelativeWalletWasmPaths()
	if len(paths) != 3 {
		t.Fatalf("expected three executable-relative candidates, got %v", paths)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)

	if paths[0] != filepath.Join(dir, "wasm", "hd-wallet-wasi.wasm") {
		t.Fatalf("first candidate = %q, want <exeDir>/wasm/hd-wallet-wasi.wasm", paths[0])
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			t.Fatalf("candidate %q is not absolute", p)
		}
		if !strings.HasSuffix(p, "hd-wallet-wasi.wasm") {
			t.Fatalf("candidate %q is not the WASI wallet artifact", p)
		}
	}
}

// TestLoadHDWalletRefusesPlantedModule is the SEC-040 audit repro: an
// hd-wallet module planted in the working directory's node_modules (the
// pinned build plus an appended section) must never serve identity
// derivation. A node with no mnemonic yet comes up without it; a node whose
// identity is HD-derived refuses to start.
func TestLoadHDWalletRefusesPlantedModule(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("HD_WALLET_WASM_PATH", "")
	t.Setenv(wasm.AllowUnpinnedHDWalletEnv, "")
	work := filepath.Join(root, "cwd")
	planted := filepath.Join(work, "node_modules", "hd-wallet-wasm", "dist", "hd-wallet-wasi.wasm")
	if err := os.MkdirAll(filepath.Dir(planted), 0o755); err != nil {
		t.Fatal(err)
	}
	name := []byte("sec040")
	body := append([]byte{byte(len(name))}, name...)
	body = append(body, []byte("attacker-controlled bytes")...)
	module := append(append([]byte(nil), wasm.EmbeddedHDWalletWasm()...), 0x00, byte(len(body)))
	module = append(module, body...)
	if err := os.WriteFile(planted, module, 0o644); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	n := &Node{ctx: context.Background(), config: &config.Config{}}
	n.config.Storage.Path = filepath.Join(root, "data", "store")
	if got := n.findHDWalletWasmPath(); got == "" {
		t.Skip("the search order no longer reaches a working-directory node_modules copy")
	}
	if err := n.loadHDWallet(); err != nil {
		t.Fatalf("loadHDWallet without a mnemonic: %v", err)
	}
	if n.hdwallet != nil {
		t.Fatal("the planted, unpinned module was loaded as the node's HD wallet")
	}

	mnemonic := filepath.Join(root, "data", "keys", "mnemonic")
	if err := os.MkdirAll(filepath.Dir(mnemonic), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mnemonic, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := n.loadHDWallet(); err == nil || !strings.Contains(err.Error(), "not a pinned release") {
		t.Fatalf("an HD-identity node with only a planted module: err = %v, want a refusal to start", err)
	}
	if n.hdwallet != nil {
		t.Fatal("the planted module was loaded for an HD-identity node")
	}
}
