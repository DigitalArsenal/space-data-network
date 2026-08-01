package node

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// newDerivedKeyTestNode builds a *Node that uses the MACHINE-DERIVED key
// password (no explicit password), which is the configuration in which the
// derivation-migration ladder is allowed to run.
func newDerivedKeyTestNode(t *testing.T) (*Node, string) {
	t.Helper()
	t.Setenv("SDN_KEY_PASSWORD", "")
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "store.db")
	cfg.Security.KeyPassword = ""
	n := &Node{config: cfg}
	return n, filepath.Join(dir, "keys", "node.key")
}

func writeSealedNodeKey(t *testing.T, keyPath string, keyData []byte, password string) {
	t.Helper()
	enc, err := keys.EncryptSecret(keyData, password)
	if err != nil {
		t.Fatalf("seal node key: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := append(append([]byte{}, nodeKeyEncMagic...), enc...)
	if err := os.WriteFile(keyPath, body, 0o600); err != nil {
		t.Fatalf("write node key: %v", err)
	}
}

// TestPeerIDSurvivesDerivationMigration is THE requirement: a node.key sealed
// under the old (v2) machine derivation must still load after the upgrade to
// the rebuild/resize-stable v3 derivation, and the resulting PeerID must be
// byte-identical. A changed PeerID here means the fleet loses its identities.
func TestPeerIDSurvivesDerivationMigration(t *testing.T) {
	n, keyPath := newDerivedKeyTestNode(t)

	priv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyData, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	before, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer id before: %v", err)
	}

	// Simulate an existing node whose key was sealed under v2.
	writeSealedNodeKey(t, keyPath, keyData, keys.DeriveV2Password())

	loaded, err := n.readNodeKeyFile(keyPath)
	if err != nil {
		t.Fatalf("v2-sealed node key must still load after the v3 upgrade: %v", err)
	}
	after, err := peer.IDFromPrivateKey(loaded)
	if err != nil {
		t.Fatalf("peer id after: %v", err)
	}
	if before != after {
		t.Fatalf("PEER IDENTITY CHANGED across derivation migration: %s -> %s", before, after)
	}

	// The file must have been re-sealed under v3, so a second boot opens it
	// directly without walking the ladder.
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("re-read node key: %v", err)
	}
	current, err := keys.DeriveDefaultPassword()
	if err != nil {
		t.Fatalf("current machine derivation must be available: %v", err)
	}
	if _, err := keys.DecryptSecret(raw[len(nodeKeyEncMagic):], current); err != nil {
		t.Fatalf("node key must be re-sealed under the current derivation: %v", err)
	}

	// And the identity is still the same after that re-seal.
	reloaded, err := n.readNodeKeyFile(keyPath)
	if err != nil {
		t.Fatalf("reload after re-seal: %v", err)
	}
	final, err := peer.IDFromPrivateKey(reloaded)
	if err != nil {
		t.Fatalf("peer id final: %v", err)
	}
	if final != before {
		t.Fatalf("PEER IDENTITY CHANGED after re-seal: %s -> %s", before, final)
	}
}

// TestForeignMachineKeyStillFailsClosed proves the migration ladder did NOT
// weaken the fail-closed guarantee: a key sealed on another machine must be
// refused, not silently replaced with a fresh identity.
func TestForeignMachineKeyStillFailsClosed(t *testing.T) {
	n, keyPath := newDerivedKeyTestNode(t)

	priv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyData, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	writeSealedNodeKey(t, keyPath, keyData, "some-other-machines-derived-key")

	if _, err := n.readNodeKeyFile(keyPath); err == nil {
		t.Fatal("a foreign machine's node key must be refused — fail closed")
	}
}

// TestMigrationSkippedWhenPassphraseConfigured proves an operator passphrase
// takes precedence: the ladder must not silently open a machine-derived file
// when the operator has pinned an explicit password.
func TestMigrationSkippedWhenPassphraseConfigured(t *testing.T) {
	t.Setenv("SDN_KEY_PASSWORD", "explicit-operator-secret")
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "store.db")
	n := &Node{config: cfg}
	keyPath := filepath.Join(dir, "keys", "node.key")

	priv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyData, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	writeSealedNodeKey(t, keyPath, keyData, keys.DeriveV2Password())

	if _, err := n.readNodeKeyFile(keyPath); err == nil {
		t.Fatal("machine-derived ladder must not run when an explicit passphrase is configured")
	}
}
