package node

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
)

func TestEnsureManagedIPFSRepoIdentity_WritesDerivedSecp256k1Identity(t *testing.T) {
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

	keyBytes, err := base64.StdEncoding.DecodeString(cfg.Identity.PrivKey)
	if err != nil {
		t.Fatalf("decode private key: %v", err)
	}
	if got, want := keyBytes, mustBundlePrivateKeyBytes(t, bundle); !bytes.Equal(got, want) {
		t.Fatalf("private key bytes mismatch\n got: %x\nwant: %x", got, want)
	}

	privKey, err := crypto.UnmarshalPrivateKey(keyBytes)
	if err != nil {
		t.Fatalf("unmarshal private key: %v", err)
	}
	if raw, err := privKey.Raw(); err != nil {
		t.Fatalf("raw private key: %v", err)
	} else if len(raw) == 0 {
		t.Fatal("decoded private key raw bytes are empty")
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

func mustBundlePrivateKeyBytes(t *testing.T, bundle *IdentityBundle) []byte {
	t.Helper()

	raw, err := bundle.Identity.MarshalPrivateKey()
	if err != nil {
		t.Fatalf("marshal bundle private key: %v", err)
	}
	return raw
}
