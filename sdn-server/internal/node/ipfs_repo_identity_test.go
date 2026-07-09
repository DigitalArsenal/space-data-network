package node

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestEnsureManagedIPFSRepoIdentity_WritesEncryptedIdentity(t *testing.T) {
	bundle := mustTestIdentityBundle(t)
	repoPath := t.TempDir()

	if err := EnsureManagedIPFSRepoIdentity(repoPath, bundle); err != nil {
		t.Fatalf("sync repo identity: %v", err)
	}

	cfg := mustReadKuboConfig(t, repoPath)
	if got, want := cfg.Identity.PeerID, bundle.PeerID.String(); got != want {
		t.Fatalf("peer id = %s, want %s", got, want)
	}
	if cfg.Identity.PrivKey == "" {
		t.Fatal("private key missing from config")
	}

	// The stored value must be an SDN-encrypted envelope, not a plaintext
	// base64-encoded libp2p private key.
	if !isEncryptedIdentityKeyField(cfg.Identity.PrivKey) {
		t.Fatalf("expected PrivKey to be an encrypted envelope, got %q", cfg.Identity.PrivKey)
	}
	assertNotPlaintextLibp2pKey(t, cfg.Identity.PrivKey)

	// Loading it back through the real (encrypted) path must yield the same
	// PeerID and key material.
	privKey, pid, err := LoadManagedIPFSRepoIdentity(repoPath, bundle.keyPassword)
	if err != nil {
		t.Fatalf("LoadManagedIPFSRepoIdentity: %v", err)
	}
	if pid != bundle.PeerID {
		t.Fatalf("loaded peer id = %s, want %s", pid, bundle.PeerID)
	}
	wantRaw := mustBundlePrivateKeyBytes(t, bundle)
	gotRaw, err := crypto.MarshalPrivateKey(privKey)
	if err != nil {
		t.Fatalf("marshal loaded private key: %v", err)
	}
	if string(gotRaw) != string(wantRaw) {
		t.Fatal("loaded private key bytes do not match the original")
	}
}

func TestEnsureManagedIPFSRepoIdentity_MigratesLegacyPlaintextConfig(t *testing.T) {
	bundle := mustTestIdentityBundle(t)
	repoPath := t.TempDir()

	// Simulate a pre-fix on-disk config: plaintext base64 PrivKey.
	rawKey := mustBundlePrivateKeyBytes(t, bundle)
	legacyCfg := map[string]any{
		"Identity": map[string]any{
			"PeerID":  bundle.PeerID.String(),
			"PrivKey": base64.StdEncoding.EncodeToString(rawKey),
		},
	}
	writeKuboConfig(t, repoPath, legacyCfg)

	if err := EnsureManagedIPFSRepoIdentity(repoPath, bundle); err != nil {
		t.Fatalf("sync repo identity (migration): %v", err)
	}

	cfg := mustReadKuboConfig(t, repoPath)
	if got, want := cfg.Identity.PeerID, bundle.PeerID.String(); got != want {
		t.Fatalf("peer id changed across migration: got %s, want %s (PeerID must never change)", got, want)
	}
	if !isEncryptedIdentityKeyField(cfg.Identity.PrivKey) {
		t.Fatalf("expected PrivKey to be migrated to an encrypted envelope, got %q", cfg.Identity.PrivKey)
	}
	assertNotPlaintextLibp2pKey(t, cfg.Identity.PrivKey)

	// The migrated file must still load back to the same PeerID.
	_, pid, err := LoadManagedIPFSRepoIdentity(repoPath, bundle.keyPassword)
	if err != nil {
		t.Fatalf("LoadManagedIPFSRepoIdentity after migration: %v", err)
	}
	if pid != bundle.PeerID {
		t.Fatalf("post-migration peer id = %s, want %s", pid, bundle.PeerID)
	}
}

func TestEnsureManagedIPFSRepoIdentity_RefusesToClobberMismatchedPeerID(t *testing.T) {
	bundle := mustTestIdentityBundle(t)
	repoPath := t.TempDir()

	otherPriv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate throwaway key: %v", err)
	}
	otherPeerID, err := peer.IDFromPrivateKey(otherPriv)
	if err != nil {
		t.Fatalf("peer id from throwaway key: %v", err)
	}
	otherRaw, err := crypto.MarshalPrivateKey(otherPriv)
	if err != nil {
		t.Fatalf("marshal throwaway key: %v", err)
	}

	writeKuboConfig(t, repoPath, map[string]any{
		"Identity": map[string]any{
			"PeerID":  otherPeerID.String(),
			"PrivKey": base64.StdEncoding.EncodeToString(otherRaw),
		},
	})

	if err := EnsureManagedIPFSRepoIdentity(repoPath, bundle); err == nil {
		t.Fatal("expected EnsureManagedIPFSRepoIdentity to refuse overwriting a mismatched identity")
	}
}

func TestLoadManagedIPFSRepoIdentity_CorruptedCiphertextFailsClosed(t *testing.T) {
	bundle := mustTestIdentityBundle(t)
	repoPath := t.TempDir()

	if err := EnsureManagedIPFSRepoIdentity(repoPath, bundle); err != nil {
		t.Fatalf("sync repo identity: %v", err)
	}

	cfgPath := filepath.Join(repoPath, "config")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	identity := raw["Identity"].(map[string]any)
	privKeyField := identity["PrivKey"].(string)

	// Corrupt the envelope by truncating it — must fail closed, never
	// silently regenerate an identity.
	corrupted := privKeyField
	if len(corrupted) > 10 {
		corrupted = corrupted[:len(corrupted)-10]
	}
	identity["PrivKey"] = corrupted
	raw["Identity"] = identity
	corruptedData, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		t.Fatalf("marshal corrupted config: %v", err)
	}
	if err := os.WriteFile(cfgPath, corruptedData, 0o600); err != nil {
		t.Fatalf("write corrupted config: %v", err)
	}

	if _, _, err := LoadManagedIPFSRepoIdentity(repoPath, bundle.keyPassword); err == nil {
		t.Fatal("expected LoadManagedIPFSRepoIdentity to fail on corrupted ciphertext, not silently succeed")
	}
}

func TestLoadManagedIPFSRepoIdentity_WrongPasswordFailsClosed(t *testing.T) {
	bundle := mustTestIdentityBundle(t)
	repoPath := t.TempDir()

	if err := EnsureManagedIPFSRepoIdentity(repoPath, bundle); err != nil {
		t.Fatalf("sync repo identity: %v", err)
	}

	if _, _, err := LoadManagedIPFSRepoIdentity(repoPath, "definitely-the-wrong-password"); err == nil {
		t.Fatal("expected LoadManagedIPFSRepoIdentity to fail with the wrong password, not silently succeed")
	}
}

type managedKuboConfig struct {
	Identity struct {
		PeerID  string `json:"PeerID"`
		PrivKey string `json:"PrivKey,omitempty"`
	} `json:"Identity"`
}

func mustTestIdentityBundle(t *testing.T) *IdentityBundle {
	t.Helper()

	n := newTestIdentityBundleNode(t)
	bundle, err := n.loadOrCreateIdentityBundle()
	if err != nil {
		t.Fatalf("loadOrCreateIdentityBundle: %v", err)
	}
	return bundle
}

func mustReadKuboConfig(t *testing.T, repoPath string) *managedKuboConfig {
	t.Helper()

	cfgPath := filepath.Join(repoPath, "config")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read kubo config %s: %v", cfgPath, err)
	}
	var cfg managedKuboConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal kubo config %s: %v", cfgPath, err)
	}
	return &cfg
}

func writeKuboConfig(t *testing.T, repoPath string, cfg map[string]any) {
	t.Helper()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal kubo config: %v", err)
	}
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "config"), data, 0o600); err != nil {
		t.Fatalf("write kubo config: %v", err)
	}
}

func mustBundlePrivateKeyBytes(t *testing.T, bundle *IdentityBundle) []byte {
	t.Helper()

	raw, err := bundle.Identity.MarshalPrivateKey()
	if err != nil {
		t.Fatalf("marshal bundle private key: %v", err)
	}
	return raw
}

// assertNotPlaintextLibp2pKey fails the test if field decodes to a valid
// base64 libp2p private key — i.e. it is NOT still a plaintext key exposed
// under a thin wrapper.
func assertNotPlaintextLibp2pKey(t *testing.T, field string) {
	t.Helper()

	decoded, err := base64.StdEncoding.DecodeString(field)
	if err != nil {
		// Not even valid base64 on its own (expected: it's magic-prefixed) —
		// definitely not a bare plaintext key.
		return
	}
	if _, err := crypto.UnmarshalPrivateKey(decoded); err == nil {
		t.Fatalf("PrivKey field decodes directly to a valid libp2p private key (still plaintext): %q", field)
	}
}
