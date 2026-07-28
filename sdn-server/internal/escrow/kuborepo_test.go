package escrow

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

// writeKuboRepo builds a kubo repo config carrying a real Ed25519 identity
// plus unrelated settings, so tests can prove those settings survive recovery.
func writeKuboRepo(t *testing.T) (repoPath string, id peer.ID) {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	marshaled, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	id, err = peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}

	repoPath = t.TempDir()
	cfg := map[string]any{
		"Identity": map[string]any{
			"PeerID":  id.String(),
			"PrivKey": base64.StdEncoding.EncodeToString(marshaled),
		},
		"Datastore": map[string]any{"StorageMax": "200GB"},
		"Addresses": map[string]any{"Swarm": []any{"/ip4/0.0.0.0/tcp/4001"}},
	}
	blob, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "config"), blob, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return repoPath, id
}

// TestKuboRepoDestroyAndRecover is the kubo-side proof: escrow a repo identity,
// DESTROY the repo, recover into a fresh one, and prove the PeerID that kubo
// would boot with is identical — and still plaintext, since kubo cannot read
// anything else.
func TestKuboRepoDestroyAndRecover(t *testing.T) {
	recPriv, recPub := recoveryKeypair(t)
	repoPath, originalID := writeKuboRepo(t)

	blob, err := SealKuboRepo(repoPath, Recipient{KeyPath: "m/44'/0'/0'/1/0"}, recPub, ecies.Secp256k1, "kubo-od-producer")
	if err != nil {
		t.Fatalf("seal kubo repo: %v", err)
	}
	if blob.Subject.PeerID != originalID.String() {
		t.Fatalf("escrow recorded %s, want %s", blob.Subject.PeerID, originalID)
	}

	escrowFile := filepath.Join(t.TempDir(), "kubo.escrow.json")
	if err := blob.WriteFile(escrowFile); err != nil {
		t.Fatalf("write escrow: %v", err)
	}

	// DESTROY the repo — the rebuild that ate vm-orbit-det-01.
	if err := os.RemoveAll(repoPath); err != nil {
		t.Fatalf("destroy repo: %v", err)
	}

	// Re-init a fresh repo with a DIFFERENT identity, as `ipfs init` would.
	freshRepo, freshID := writeKuboRepo(t)
	if freshID == originalID {
		t.Fatal("fresh repo should have a different identity")
	}

	loaded, err := ReadFile(escrowFile)
	if err != nil {
		t.Fatalf("read escrow: %v", err)
	}

	// Recovery into a repo holding a different identity must be refused...
	if _, err := RecoverKuboRepo(loaded, recPriv, freshRepo, false); err == nil {
		t.Fatal("recovery must refuse to clobber a different live identity without force")
	}
	// ...and succeed when the operator says so explicitly.
	recoveredID, err := RecoverKuboRepo(loaded, recPriv, freshRepo, true)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recoveredID != originalID {
		t.Fatalf("PEER IDENTITY NOT RECOVERED: %s != %s", recoveredID, originalID)
	}

	// The repo on disk must now boot as the original peer.
	ident, err := ReadKuboIdentity(freshRepo)
	if err != nil {
		t.Fatalf("read recovered identity: %v", err)
	}
	if ident.PeerID != originalID.String() {
		t.Fatalf("config PeerID = %s, want %s", ident.PeerID, originalID)
	}
	// kubo does a bare base64 decode — the key must be plaintext, and it must
	// reproduce the identity.
	if strings.HasPrefix(ident.PrivKey, encryptedIdentityPrefix) {
		t.Fatal("recovered PrivKey must be PLAINTEXT base64; kubo cannot read an encrypted value")
	}
	raw, err := base64.StdEncoding.DecodeString(ident.PrivKey)
	if err != nil {
		t.Fatalf("recovered PrivKey is not base64 (kubo would fail to boot): %v", err)
	}
	priv, err := crypto.UnmarshalPrivateKey(raw)
	if err != nil {
		t.Fatalf("recovered PrivKey does not unmarshal: %v", err)
	}
	bootID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("boot peer id: %v", err)
	}
	if bootID != originalID {
		t.Fatalf("repo would boot as %s, want %s", bootID, originalID)
	}

	// Unrelated settings must survive untouched.
	rawCfg, err := os.ReadFile(filepath.Join(freshRepo, "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	ds, ok := cfg["Datastore"].(map[string]any)
	if !ok || ds["StorageMax"] != "200GB" {
		t.Fatalf("recovery clobbered unrelated config: %v", cfg["Datastore"])
	}
	t.Logf("kubo repo recovered and would boot as %s (identical)", bootID)
}

// TestSealRefusesEncryptedIdentity guards the hazard that an sdnenc1: value in
// Identity.PrivKey is unreadable by kubo — escrowing it would preserve a
// bricked identity and mask the real problem.
func TestSealRefusesEncryptedIdentity(t *testing.T) {
	_, recPub := recoveryKeypair(t)
	repoPath, _ := writeKuboRepo(t)

	cfgPath := filepath.Join(repoPath, "config")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ident := cfg["Identity"].(map[string]any)
	ident["PrivKey"] = encryptedIdentityPrefix + "b3BhcXVl"
	out, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, out, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = SealKuboRepo(repoPath, Recipient{KeyPath: "m/44'/0'/0'/1/0"}, recPub, ecies.Secp256k1, "test")
	if err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("expected a refusal naming the unreadable key, got %v", err)
	}
}

// TestRecoverIntoMatchingRepoNeedsNoForce — re-recovering the SAME identity is
// a no-risk repair and must not demand force.
func TestRecoverIntoMatchingRepoNeedsNoForce(t *testing.T) {
	recPriv, recPub := recoveryKeypair(t)
	repoPath, originalID := writeKuboRepo(t)

	blob, err := SealKuboRepo(repoPath, Recipient{KeyPath: "m/44'/0'/0'/1/0"}, recPub, ecies.Secp256k1, "test")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := RecoverKuboRepo(blob, recPriv, repoPath, false)
	if err != nil {
		t.Fatalf("re-recovering the same identity must not require force: %v", err)
	}
	if got != originalID {
		t.Fatalf("got %s, want %s", got, originalID)
	}
}
