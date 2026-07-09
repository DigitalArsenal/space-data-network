package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

func TestLoadOrCreateIdentityBundle_ReusesEncryptedMnemonic(t *testing.T) {
	n := newTestIdentityBundleNode(t)

	first, err := n.loadOrCreateIdentityBundle()
	if err != nil {
		t.Fatalf("first bundle: %v", err)
	}
	second, err := n.loadOrCreateIdentityBundle()
	if err != nil {
		t.Fatalf("second bundle: %v", err)
	}

	if got, want := first.PeerID.String(), second.PeerID.String(); got != want {
		t.Fatalf("peer id = %s, want %s", got, want)
	}
	if first.BitcoinAddress == "" {
		t.Fatal("bitcoin address missing")
	}
	if first.XPub == "" {
		t.Fatal("xpub missing")
	}
	if got, want := first.IdentityKeyPath, second.IdentityKeyPath; got != want {
		t.Fatalf("identity key path = %q, want %q", got, want)
	}
	if got, want := first.SigningKeyPath, second.SigningKeyPath; got != want {
		t.Fatalf("signing key path = %q, want %q", got, want)
	}
	if got, want := first.EncryptionKeyPath, second.EncryptionKeyPath; got != want {
		t.Fatalf("encryption key path = %q, want %q", got, want)
	}

	mnemonicPath := filepath.Join(filepath.Dir(n.config.Storage.Path), "keys", "mnemonic")
	mnemonicData, err := os.ReadFile(mnemonicPath)
	if err != nil {
		t.Fatalf("read mnemonic file at %s: %v", mnemonicPath, err)
	}
	if !keys.IsMnemonicEncrypted(mnemonicData) {
		t.Fatalf("mnemonic file at %s was not encrypted", mnemonicPath)
	}
}

// TestLoadOrCreateIdentityBundle_NodeKeyNeverPlaintextOnDisk guards the exact
// window the round-trip tests miss: the node.key written by
// loadOrCreateIdentityBundle itself, BEFORE loadOrCreateKey later overwrites
// the same path. A plaintext write here persists across a crash between the
// two writes and is not securely erased by the later overwrite.
func TestLoadOrCreateIdentityBundle_NodeKeyNeverPlaintextOnDisk(t *testing.T) {
	n := newTestIdentityBundleNode(t)

	bundle, err := n.loadOrCreateIdentityBundle()
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	if bundle.Identity == nil || bundle.Identity.IdentityPrivKey == nil {
		t.Fatal("bundle missing identity private key")
	}

	keyPath := filepath.Join(filepath.Dir(n.config.Storage.Path), "keys", "node.key")
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read node.key immediately after bundle creation: %v", err)
	}
	if !bytes.HasPrefix(raw, nodeKeyEncMagic) {
		t.Fatalf("node.key written by loadOrCreateIdentityBundle is not encrypted at rest")
	}
	keyData, err := bundle.Identity.MarshalPrivateKey()
	if err != nil {
		t.Fatalf("marshal identity key: %v", err)
	}
	if bytes.Contains(raw, keyData) {
		t.Fatalf("plaintext identity key material present in node.key")
	}

	// And it must round-trip through the standard reader.
	got, err := n.readNodeKeyFile(keyPath)
	if err != nil {
		t.Fatalf("readNodeKeyFile: %v", err)
	}
	if !got.Equals(bundle.Identity.IdentityPrivKey) {
		t.Fatalf("round-tripped node.key differs from bundle identity key")
	}
}

func newTestIdentityBundleNode(t *testing.T) *Node {
	t.Helper()

	hw := newTestHDWalletModule(t)
	basePath := t.TempDir()
	t.Setenv("SDN_KEY_PASSWORD", "test-password")

	return &Node{
		ctx: context.Background(),
		config: &config.Config{
			Storage: config.StorageConfig{
				Path: filepath.Join(basePath, "data"),
			},
		},
		hdwallet: hw,
	}
}

func newTestHDWalletModule(t *testing.T) *wasm.HDWalletModule {
	t.Helper()

	wasmPath := mustFindHDWalletWasmPath(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read hd wallet wasm %s: %v", wasmPath, err)
	}

	hw, err := wasm.NewHDWalletModuleFromBytes(context.Background(), wasmBytes)
	if err != nil {
		t.Fatalf("NewHDWalletModuleFromBytes: %v", err)
	}

	entropy := make([]byte, 64)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	if err := hw.InjectEntropy(context.Background(), entropy); err != nil {
		t.Fatalf("InjectEntropy: %v", err)
	}
	return hw
}

func mustFindHDWalletWasmPath(t *testing.T) string {
	t.Helper()

	candidates := []string{
		os.Getenv("HD_WALLET_WASM_PATH"),
		"sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../../sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	t.Skip("pure HD wallet WASI artifact not available in this checkout")
	return ""
}
