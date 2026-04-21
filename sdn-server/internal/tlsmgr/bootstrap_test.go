package tlsmgr

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateBootstrapCert_PersistsStableIdentity(t *testing.T) {
	dir := t.TempDir()
	mgr := &Manager{mode: ModeManaged}

	first, err := mgr.loadOrCreateBootstrapCert(dir, BootstrapIdentityInput{
		PeerID:                     "12D3KooWTestPeer",
		EncryptionPath:             "m/44'/0'/0'/1'/0'",
		EncryptionX25519PublicKey:  bytes.Repeat([]byte{1}, 32),
		EncryptionProofEd25519Seed: bytes.Repeat([]byte{2}, 32),
		Hosts:                      []string{"localhost", "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("loadOrCreateBootstrapCert() first error = %v", err)
	}

	second, err := mgr.loadOrCreateBootstrapCert(dir, BootstrapIdentityInput{
		PeerID:                     "12D3KooWTestPeer",
		EncryptionPath:             "m/44'/0'/0'/1'/0'",
		EncryptionX25519PublicKey:  bytes.Repeat([]byte{1}, 32),
		EncryptionProofEd25519Seed: bytes.Repeat([]byte{2}, 32),
		Hosts:                      []string{"localhost", "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("loadOrCreateBootstrapCert() second error = %v", err)
	}

	if !bytes.Equal(first.Leaf.Raw, second.Leaf.Raw) {
		t.Fatal("bootstrap certificate changed across reload")
	}

	raw, err := os.ReadFile(filepath.Join(dir, bootstrapCertFileName))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("pem.Decode() returned nil")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}

	var bindingFound bool
	for _, ext := range leaf.Extensions {
		if ext.Id.Equal(BootstrapBindingOID) {
			bindingFound = true
			if _, err := VerifyBootstrapBinding(ext.Value, extSPKIHash(t, leaf)); err != nil {
				t.Fatalf("VerifyBootstrapBinding() error = %v", err)
			}
		}
	}
	if !bindingFound {
		t.Fatal("bootstrap binding extension not found")
	}
}

func extSPKIHash(t *testing.T, leaf *x509.Certificate) []byte {
	t.Helper()
	pubDER, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	hash := sha256Sum(pubDER)
	return hash[:]
}

func sha256Sum(raw []byte) [32]byte {
	return sha256.Sum256(raw)
}
