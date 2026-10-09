package node

// A node never loses its wallet identity or its sealed-transport key for want
// of the wallet module on disk (owner 2026-10-09: "it shouldn't lose the
// encryption key, even for a mac"). The darwin cli-bundle carries only the
// binary; a Mac node restarted without HD_WALLET_WASM_PATH used to come up on
// its legacy keys/node.key, with the same peer ID and no sealed-transport key.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

func TestNodeKeepsItsSealedKeyWithoutAWalletModuleOnDisk(t *testing.T) {
	// The node's first start, with the module on disk.
	first := newTestIdentityBundleNode(t)
	firstKey, err := first.loadOrCreateKey()
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	firstSealed, err := first.updateEnvelopeKey()
	if err != nil {
		t.Fatalf("first start has no sealed-transport key: %v", err)
	}

	// Restarted where no copy of the module can be found: no
	// HD_WALLET_WASM_PATH, a working directory and home with no module.
	restart := func(t *testing.T) *Node {
		bare := t.TempDir()
		t.Setenv("HD_WALLET_WASM_PATH", "")
		t.Setenv("HOME", bare)
		t.Chdir(bare)
		return &Node{ctx: context.Background(), config: &config.Config{Storage: config.StorageConfig{Path: first.config.Storage.Path}}}
	}

	t.Run("restart keeps the identity and the sealed key", func(t *testing.T) {
		n := restart(t)
		if path := n.findHDWalletWasmPath(); path != "" {
			t.Fatalf("a wallet module is on disk at %s; the restart must have none", path)
		}
		if err := n.loadHDWallet(); err != nil {
			t.Fatalf("load: %v", err)
		}
		key, err := n.loadOrCreateKey()
		if err != nil {
			t.Fatalf("restart: %v", err)
		}
		if !key.Equals(firstKey) {
			t.Fatal("the restart came up with a different peer identity")
		}
		sealed, err := n.updateEnvelopeKey()
		if err != nil {
			t.Fatalf("the restart has no sealed-transport key: %v", err)
		}
		if !bytes.Equal(sealed.KeyID, firstSealed.KeyID) || !bytes.Equal(sealed.Private, firstSealed.Private) {
			t.Fatal("the restart derived a different sealed-transport key")
		}
	})

	t.Run("a module that cannot load refuses start", func(t *testing.T) {
		n := restart(t)
		broken := filepath.Join(t.TempDir(), "hd-wallet-wasi.wasm")
		if err := os.WriteFile(broken, []byte("not a wasm module"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HD_WALLET_WASM_PATH", broken)
		err := n.loadHDWallet()
		if err == nil || !strings.Contains(err.Error(), "refusing to start") {
			t.Fatalf("load = %v, want a refusal for a node holding a mnemonic", err)
		}
	})
}
