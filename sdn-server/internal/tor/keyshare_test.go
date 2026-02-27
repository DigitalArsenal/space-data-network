package tor

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestDeriveClusterSeedDeterministic(t *testing.T) {
	secret := []byte("my-cluster-secret")

	a := DeriveClusterSeed(secret)
	b := DeriveClusterSeed(secret)
	if a != b {
		t.Fatal("expected deterministic seed for identical cluster secret")
	}

	c := DeriveClusterSeed([]byte("different-secret"))
	if a == c {
		t.Fatal("expected different seed for different cluster secret")
	}
}

func TestDeriveClusterSeedDiffersFromNodeSeed(t *testing.T) {
	// The cluster derivation must use a different domain separator than the
	// per-node derivation so the same key material doesn't produce the same
	// .onion address in both modes.
	material := []byte("shared-material")

	clusterSeed := DeriveClusterSeed(material)
	nodeSeed := deriveHiddenServiceSeed(material)

	if clusterSeed == nodeSeed {
		t.Fatal("cluster seed must differ from per-node seed for same input")
	}
}

func TestNewKeyBundleFromClusterSecret(t *testing.T) {
	secret := []byte("test-secret-mnemonic-phrase")
	clusterID := "test-cluster-1"
	createdBy := "12D3KooWTestPeerID"

	bundle, err := NewKeyBundleFromClusterSecret(secret, clusterID, createdBy)
	if err != nil {
		t.Fatalf("NewKeyBundleFromClusterSecret failed: %v", err)
	}

	// Check key sizes.
	if len(bundle.SecretKey) != ed25519.PrivateKeySize {
		t.Fatalf("secret key size = %d, want %d", len(bundle.SecretKey), ed25519.PrivateKeySize)
	}
	if len(bundle.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("public key size = %d, want %d", len(bundle.PublicKey), ed25519.PublicKeySize)
	}

	// Check .onion format.
	if ok := regexp.MustCompile(`^[a-z2-7]{56}\.onion$`).MatchString(bundle.OnionHost); !ok {
		t.Fatalf("unexpected onion host format: %q", bundle.OnionHost)
	}

	if bundle.ClusterID != clusterID {
		t.Fatalf("cluster ID = %q, want %q", bundle.ClusterID, clusterID)
	}
	if bundle.CreatedBy != createdBy {
		t.Fatalf("created by = %q, want %q", bundle.CreatedBy, createdBy)
	}
	if bundle.Version != 1 {
		t.Fatalf("version = %d, want 1", bundle.Version)
	}
}

func TestKeyBundleDeterminism(t *testing.T) {
	secret := []byte("reproducible-secret")

	b1, err := NewKeyBundleFromClusterSecret(secret, "c1", "peer-a")
	if err != nil {
		t.Fatalf("bundle 1: %v", err)
	}
	b2, err := NewKeyBundleFromClusterSecret(secret, "c1", "peer-b")
	if err != nil {
		t.Fatalf("bundle 2: %v", err)
	}

	// Two different nodes with the same secret produce the same .onion.
	if b1.OnionHost != b2.OnionHost {
		t.Fatalf("onion hosts differ: %q vs %q", b1.OnionHost, b2.OnionHost)
	}
	if !bytes.Equal(b1.SecretKey, b2.SecretKey) {
		t.Fatal("secret keys differ for same cluster secret")
	}
	if !bytes.Equal(b1.PublicKey, b2.PublicKey) {
		t.Fatal("public keys differ for same cluster secret")
	}
}

func TestKeyBundleDifferentSecretsDifferentOnion(t *testing.T) {
	b1, err := NewKeyBundleFromClusterSecret([]byte("secret-alpha"), "c1", "p1")
	if err != nil {
		t.Fatalf("bundle 1: %v", err)
	}
	b2, err := NewKeyBundleFromClusterSecret([]byte("secret-beta"), "c1", "p1")
	if err != nil {
		t.Fatalf("bundle 2: %v", err)
	}

	if b1.OnionHost == b2.OnionHost {
		t.Fatal("different secrets must produce different onion addresses")
	}
}

func TestKeyBundleValidate(t *testing.T) {
	secret := []byte("validate-test")
	bundle, err := NewKeyBundleFromClusterSecret(secret, "c1", "p1")
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	if err := bundle.Validate(); err != nil {
		t.Fatalf("valid bundle failed validation: %v", err)
	}

	// Tamper with the public key.
	tampered := *bundle
	tampered.PublicKey = make(ed25519.PublicKey, ed25519.PublicKeySize)
	if err := tampered.Validate(); err == nil {
		t.Fatal("expected validation error for mismatched public key")
	}

	// Empty cluster ID.
	noCluster := *bundle
	noCluster.ClusterID = ""
	if err := noCluster.Validate(); err == nil {
		t.Fatal("expected validation error for empty cluster_id")
	}

	// Bad onion host.
	badHost := *bundle
	badHost.OnionHost = "wrong.onion"
	if err := badHost.Validate(); err == nil {
		t.Fatal("expected validation error for wrong onion host")
	}

	// Wrong key size.
	badKey := *bundle
	badKey.SecretKey = []byte("too-short")
	if err := badKey.Validate(); err == nil {
		t.Fatal("expected validation error for wrong key size")
	}
}

func TestEncryptDecryptKeyBundle(t *testing.T) {
	secret := []byte("encryption-test-secret")
	bundle, err := NewKeyBundleFromClusterSecret(secret, "cluster-enc", "peer-1")
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	encrypted, err := EncryptKeyBundle(bundle, secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Encrypted output must be longer than plaintext (nonce + tag + header).
	if len(encrypted) < 50 {
		t.Fatalf("encrypted blob suspiciously short: %d bytes", len(encrypted))
	}

	// Decrypt with correct secret.
	decrypted, err := DecryptKeyBundle(encrypted, secret, "cluster-enc")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if decrypted.OnionHost != bundle.OnionHost {
		t.Fatalf("onion host mismatch: %q vs %q", decrypted.OnionHost, bundle.OnionHost)
	}
	if !bytes.Equal(decrypted.SecretKey, bundle.SecretKey) {
		t.Fatal("secret key mismatch after round-trip")
	}
	if !bytes.Equal(decrypted.PublicKey, bundle.PublicKey) {
		t.Fatal("public key mismatch after round-trip")
	}
	if decrypted.ClusterID != bundle.ClusterID {
		t.Fatalf("cluster ID mismatch: %q vs %q", decrypted.ClusterID, bundle.ClusterID)
	}
	if decrypted.Version != bundle.Version {
		t.Fatalf("version mismatch: %d vs %d", decrypted.Version, bundle.Version)
	}
}

func TestDecryptWithWrongSecret(t *testing.T) {
	secret := []byte("correct-secret")
	bundle, err := NewKeyBundleFromClusterSecret(secret, "c1", "p1")
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	encrypted, err := EncryptKeyBundle(bundle, secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	_, err = DecryptKeyBundle(encrypted, []byte("wrong-secret"), "c1")
	if err == nil {
		t.Fatal("expected decryption failure with wrong secret")
	}
}

func TestDecryptWithWrongClusterID(t *testing.T) {
	secret := []byte("cluster-id-test")
	bundle, err := NewKeyBundleFromClusterSecret(secret, "cluster-real", "p1")
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	encrypted, err := EncryptKeyBundle(bundle, secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Different cluster ID produces a different derived key, so decrypt fails.
	_, err = DecryptKeyBundle(encrypted, secret, "cluster-fake")
	if err == nil {
		t.Fatal("expected decryption failure with wrong cluster ID")
	}
}

func TestDecryptTruncatedBlob(t *testing.T) {
	_, err := DecryptKeyBundle([]byte{0, 0, 0, 1}, []byte("s"), "c")
	if err == nil {
		t.Fatal("expected error for truncated blob")
	}

	_, err = DecryptKeyBundle([]byte{0, 0}, []byte("s"), "c")
	if err == nil {
		t.Fatal("expected error for too-short blob")
	}
}

func TestDecryptBadEnvelopeVersion(t *testing.T) {
	// Version 99 is unsupported.
	bad := []byte{0, 0, 0, 99, 0, 0, 0, 0}
	_, err := DecryptKeyBundle(bad, []byte("s"), "c")
	if err == nil {
		t.Fatal("expected error for unsupported envelope version")
	}
}

func TestEncryptNonDeterministic(t *testing.T) {
	secret := []byte("nonce-test")
	bundle, err := NewKeyBundleFromClusterSecret(secret, "c1", "p1")
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	enc1, err := EncryptKeyBundle(bundle, secret)
	if err != nil {
		t.Fatalf("encrypt 1: %v", err)
	}
	enc2, err := EncryptKeyBundle(bundle, secret)
	if err != nil {
		t.Fatalf("encrypt 2: %v", err)
	}

	// Two encryptions of the same bundle must produce different ciphertexts
	// (random nonce).
	if bytes.Equal(enc1, enc2) {
		t.Fatal("two encryptions of the same bundle should not be identical (nonce reuse)")
	}

	// But both must decrypt to the same bundle.
	d1, _ := DecryptKeyBundle(enc1, secret, "c1")
	d2, _ := DecryptKeyBundle(enc2, secret, "c1")
	if d1.OnionHost != d2.OnionHost {
		t.Fatal("both ciphertexts must decrypt to the same onion host")
	}
}

func TestSaveAndLoadKeyBundleDir(t *testing.T) {
	secret := []byte("disk-roundtrip-test")
	bundle, err := NewKeyBundleFromClusterSecret(secret, "c-disk", "p-disk")
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	dir := t.TempDir()
	hsDir := filepath.Join(dir, "hs_keys")

	if err := SaveKeyBundleToDir(hsDir, bundle); err != nil {
		t.Fatalf("save to dir: %v", err)
	}

	// Verify files exist.
	for _, name := range []string{"hs_ed25519_secret_key", "hs_ed25519_public_key", "hostname"} {
		p := filepath.Join(hsDir, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected file %s: %v", name, err)
		}
	}

	// Verify hostname file content.
	hostnameData, err := os.ReadFile(filepath.Join(hsDir, "hostname"))
	if err != nil {
		t.Fatalf("read hostname: %v", err)
	}
	if got := string(bytes.TrimSpace(hostnameData)); got != bundle.OnionHost {
		t.Fatalf("hostname file = %q, want %q", got, bundle.OnionHost)
	}

	// Load back.
	loaded, err := LoadKeyBundleFromDir(hsDir, "c-disk", "p-disk")
	if err != nil {
		t.Fatalf("load from dir: %v", err)
	}

	if loaded.OnionHost != bundle.OnionHost {
		t.Fatalf("loaded onion host = %q, want %q", loaded.OnionHost, bundle.OnionHost)
	}
	if !bytes.Equal(loaded.SecretKey, bundle.SecretKey) {
		t.Fatal("loaded secret key differs")
	}
	if !bytes.Equal(loaded.PublicKey, bundle.PublicKey) {
		t.Fatal("loaded public key differs")
	}
}

func TestLoadKeyBundleFromDir_MissingFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadKeyBundleFromDir(dir, "c1", "p1")
	if err == nil {
		t.Fatal("expected error when key files are missing")
	}
}

func TestKeyBundleSignVerify(t *testing.T) {
	// Verify the Ed25519 keys in the bundle actually work for sign/verify.
	secret := []byte("sign-verify-test")
	bundle, err := NewKeyBundleFromClusterSecret(secret, "c1", "p1")
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}

	message := []byte("hello from the cluster")
	sig := ed25519.Sign(bundle.SecretKey, message)

	if !ed25519.Verify(bundle.PublicKey, message, sig) {
		t.Fatal("signature verification failed with bundle keys")
	}

	// Wrong message should fail.
	if ed25519.Verify(bundle.PublicKey, []byte("tampered"), sig) {
		t.Fatal("signature verified with wrong message")
	}
}
