package keys

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyManager(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Should not have identity initially
	if m.HasIdentity() {
		t.Error("Should not have identity initially")
	}
}

func TestGenerateIdentity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Generate identity
	identity, err := m.GenerateIdentity()
	if err != nil {
		t.Fatalf("Failed to generate identity: %v", err)
	}

	// Check signing key
	if identity.SigningKey == nil {
		t.Fatal("Signing key should not be nil")
	}
	if len(identity.SigningKey.PublicKey) != Ed25519PublicKeySize {
		t.Errorf("Signing public key wrong size: %d", len(identity.SigningKey.PublicKey))
	}
	if len(identity.SigningKey.PrivateKey) != Ed25519PrivateKeySize {
		t.Errorf("Signing private key wrong size: %d", len(identity.SigningKey.PrivateKey))
	}

	// Check encryption key
	if identity.EncryptionKey == nil {
		t.Fatal("Encryption key should not be nil")
	}
	if len(identity.EncryptionKey.PublicKey) != X25519KeySize {
		t.Errorf("Encryption public key wrong size: %d", len(identity.EncryptionKey.PublicKey))
	}
	if len(identity.EncryptionKey.PrivateKey) != X25519KeySize {
		t.Errorf("Encryption private key wrong size: %d", len(identity.EncryptionKey.PrivateKey))
	}

	// Check files were created
	keysDir := filepath.Join(tmpDir, "keys")
	files := []string{
		SigningPrivateKeyFile,
		SigningPublicKeyFile,
		EncryptionPrivateKeyFile,
		EncryptionPublicKeyFile,
	}
	for _, f := range files {
		path := filepath.Join(keysDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("Key file should exist: %s", f)
		}
	}

	// HasIdentity should return true
	if !m.HasIdentity() {
		t.Error("HasIdentity should return true after generation")
	}

	// Cannot generate again
	_, err = m.GenerateIdentity()
	if err != ErrKeyAlreadyExists {
		t.Errorf("Expected ErrKeyAlreadyExists, got: %v", err)
	}
}

func TestLoadIdentity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Generate identity
	m1, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	origIdentity, err := m1.GenerateIdentity()
	if err != nil {
		t.Fatalf("Failed to generate identity: %v", err)
	}

	// Create new manager and load
	m2, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create second manager: %v", err)
	}

	loadedIdentity, err := m2.LoadIdentity()
	if err != nil {
		t.Fatalf("Failed to load identity: %v", err)
	}

	// Compare keys
	if string(origIdentity.SigningKey.PublicKey) != string(loadedIdentity.SigningKey.PublicKey) {
		t.Error("Loaded signing public key doesn't match")
	}
	if string(origIdentity.SigningKey.PrivateKey) != string(loadedIdentity.SigningKey.PrivateKey) {
		t.Error("Loaded signing private key doesn't match")
	}
	if string(origIdentity.EncryptionKey.PublicKey) != string(loadedIdentity.EncryptionKey.PublicKey) {
		t.Error("Loaded encryption public key doesn't match")
	}
	if string(origIdentity.EncryptionKey.PrivateKey) != string(loadedIdentity.EncryptionKey.PrivateKey) {
		t.Error("Loaded encryption private key doesn't match")
	}
}

func TestSignAndVerify(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	_, err = m.GenerateIdentity()
	if err != nil {
		t.Fatalf("Failed to generate identity: %v", err)
	}

	// Sign some data
	data := []byte("test message to sign")
	signature, err := m.Sign(data)
	if err != nil {
		t.Fatalf("Failed to sign: %v", err)
	}

	// Verify signature
	if !m.Verify(m.identity.SigningKey.PublicKey, data, signature) {
		t.Error("Signature verification failed")
	}

	// Verify with wrong data should fail
	if m.Verify(m.identity.SigningKey.PublicKey, []byte("wrong data"), signature) {
		t.Error("Verification should fail with wrong data")
	}

	// Verify signature matches ed25519 standard
	if !ed25519.Verify(m.identity.SigningKey.PublicKey, data, signature) {
		t.Error("Signature should be valid ed25519 signature")
	}
}

func TestPublicKeyFingerprint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// No fingerprint before identity
	if fp := m.PublicKeyFingerprint(); fp != "" {
		t.Errorf("Fingerprint should be empty before identity, got: %s", fp)
	}

	m.GenerateIdentity()

	// Should have fingerprint after identity
	fp := m.PublicKeyFingerprint()
	if len(fp) != 16 { // 8 bytes = 16 hex chars
		t.Errorf("Fingerprint should be 16 chars, got: %d", len(fp))
	}
}

func TestExportPublicKeys(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	m.GenerateIdentity()

	signingKey, encryptionKey := m.ExportPublicKeys()

	// Should be hex-encoded
	if len(signingKey) != Ed25519PublicKeySize*2 {
		t.Errorf("Signing key hex wrong length: %d", len(signingKey))
	}
	if len(encryptionKey) != X25519KeySize*2 {
		t.Errorf("Encryption key hex wrong length: %d", len(encryptionKey))
	}
}

func TestDeleteIdentity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	m.GenerateIdentity()

	// Delete
	err = m.DeleteIdentity()
	if err != nil {
		t.Fatalf("Failed to delete identity: %v", err)
	}

	// Should not have identity
	if m.HasIdentity() {
		t.Error("Should not have identity after deletion")
	}

	// Check files were deleted
	keysDir := filepath.Join(tmpDir, "keys")
	files := []string{
		SigningPrivateKeyFile,
		SigningPublicKeyFile,
		EncryptionPrivateKeyFile,
		EncryptionPublicKeyFile,
	}
	for _, f := range files {
		path := filepath.Join(keysDir, f)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("Key file should be deleted: %s", f)
		}
	}
}
