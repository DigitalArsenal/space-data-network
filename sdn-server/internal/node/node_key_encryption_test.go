package node

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// newKeyTestNode builds a minimal *Node with just enough config for the
// node.key persistence helpers (writeEncryptedNodeKey / readNodeKeyFile).
func newKeyTestNode(t *testing.T) (*Node, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "store.db")
	// Pin an explicit key password so the test is independent of the
	// machine-derived default.
	cfg.Security.KeyPassword = "test-node-key-password"
	n := &Node{config: cfg}
	keyPath := filepath.Join(dir, "keys", "node.key")
	return n, keyPath
}

func TestNodeKeyWrittenEncryptedAtRest(t *testing.T) {
	n, keyPath := newKeyTestNode(t)

	priv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyData, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	if err := n.writeEncryptedNodeKey(keyPath, keyData); err != nil {
		t.Fatalf("writeEncryptedNodeKey: %v", err)
	}

	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.HasPrefix(raw, nodeKeyEncMagic) {
		t.Fatalf("on-disk key missing encryption marker")
	}
	// The plaintext marshaled key bytes must not appear anywhere in the file.
	if bytes.Contains(raw, keyData) {
		t.Fatalf("plaintext key material found in on-disk node.key")
	}

	// Round-trips back to the same key.
	got, err := n.readNodeKeyFile(keyPath)
	if err != nil {
		t.Fatalf("readNodeKeyFile: %v", err)
	}
	if !got.Equals(priv) {
		t.Fatalf("round-tripped key differs from original")
	}
}

func TestNodeKeyLegacyPlaintextMigratesInPlace(t *testing.T) {
	n, keyPath := newKeyTestNode(t)

	priv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyData, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	// Write a legacy plaintext node.key (no marker), like older builds did.
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(keyPath, keyData, 0o600); err != nil {
		t.Fatalf("write legacy key: %v", err)
	}

	got, err := n.readNodeKeyFile(keyPath)
	if err != nil {
		t.Fatalf("readNodeKeyFile (legacy): %v", err)
	}
	if !got.Equals(priv) {
		t.Fatalf("migrated key differs (PeerID would change)")
	}

	// The file must now be encrypted at rest.
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read back after migration: %v", err)
	}
	if !bytes.HasPrefix(raw, nodeKeyEncMagic) {
		t.Fatalf("legacy key was not migrated to encrypted-at-rest")
	}
	if bytes.Contains(raw, keyData) {
		t.Fatalf("plaintext key material still present after migration")
	}
}

func TestNodeKeyUndecryptableFailsClosed(t *testing.T) {
	n, keyPath := newKeyTestNode(t)

	// Craft a marker-prefixed file whose body is not valid ciphertext for our
	// password (encrypted under a different password).
	priv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyData, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	enc, err := keys.EncryptSecret(keyData, "a-different-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := append(append([]byte{}, nodeKeyEncMagic...), enc...)
	if err := os.WriteFile(keyPath, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = n.readNodeKeyFile(keyPath)
	if err == nil {
		t.Fatalf("expected failure decrypting with wrong password")
	}
	if !errors.Is(err, errNodeKeyUndecryptable) {
		t.Fatalf("expected errNodeKeyUndecryptable, got %v", err)
	}
}
