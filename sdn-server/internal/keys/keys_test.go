package keys

import (
	"crypto/ed25519"
	"crypto/rand"
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

// TestGenerateIdentity_IsRandomNotXPubBound documents and asserts the F1
// legacy-scope finding for this manager: GenerateIdentity's key material is
// NOT deterministically tied to any hd-wallet xpub/seed — two managers
// generating "identity" independently produce unrelated keys. This is
// exactly why GenerateIdentity must not be used to mint a persistent
// "node identity" or "user identity" going forward (those come from the
// hd-wallet xpub path — see internal/auth.UserStore and
// internal/node.IdentityBundle). It remains fine for the narrow legacy,
// non-identity uses documented on GenerateIdentity.
func TestGenerateIdentity_IsRandomNotXPubBound(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	m1, err := NewManager(dir1)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	m2, err := NewManager(dir2)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	id1, err := m1.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity (m1): %v", err)
	}
	id2, err := m2.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity (m2): %v", err)
	}

	if string(id1.SigningKey.PublicKey) == string(id2.SigningKey.PublicKey) {
		t.Fatal("two independent GenerateIdentity calls produced the same signing key; expected random, unrelated keys")
	}
	if string(id1.EncryptionKey.PublicKey) == string(id2.EncryptionKey.PublicKey) {
		t.Fatal("two independent GenerateIdentity calls produced the same encryption key; expected random, unrelated keys")
	}
}

// TestGenerateIdentityFromSeed_DeterministicBindToXPub asserts the
// bind-to-xpub path GenerateIdentity's doc comment points callers to: given
// the same seed (as would be derived from an hd-wallet xpub/mnemonic), the
// resulting identity is always the same — the opposite of GenerateIdentity's
// randomness — so this manager's on-disk identity can be made a function of
// the single hd-wallet identity instead of a divergent random one.
func TestGenerateIdentityFromSeed_DeterministicBindToXPub(t *testing.T) {
	var seed [Ed25519SeedSize]byte
	if _, err := rand.Read(seed[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	dirA := t.TempDir()
	mA, err := NewManager(dirA)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	idA, err := mA.GenerateIdentityFromSeed(seed)
	if err != nil {
		t.Fatalf("GenerateIdentityFromSeed (A): %v", err)
	}

	dirB := t.TempDir()
	mB, err := NewManager(dirB)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	idB, err := mB.GenerateIdentityFromSeed(seed)
	if err != nil {
		t.Fatalf("GenerateIdentityFromSeed (B): %v", err)
	}

	if string(idA.SigningKey.PublicKey) != string(idB.SigningKey.PublicKey) {
		t.Error("same seed produced different signing public keys; GenerateIdentityFromSeed must be deterministic")
	}
	if string(idA.SigningKey.PrivateKey) != string(idB.SigningKey.PrivateKey) {
		t.Error("same seed produced different signing private keys; GenerateIdentityFromSeed must be deterministic")
	}
	if string(idA.EncryptionKey.PublicKey) != string(idB.EncryptionKey.PublicKey) {
		t.Error("same seed produced different encryption public keys; GenerateIdentityFromSeed must be deterministic")
	}
	if string(idA.EncryptionKey.PrivateKey) != string(idB.EncryptionKey.PrivateKey) {
		t.Error("same seed produced different encryption private keys; GenerateIdentityFromSeed must be deterministic")
	}

	// Sanity: the encryption key must not just be the signing seed bytes
	// reused verbatim under a different algorithm (domain separation).
	if string(idA.EncryptionKey.PrivateKey) == string(seed[:]) {
		t.Error("encryption private key must not equal the raw seed (expected domain-separated derivation)")
	}

	// A different seed must yield a different identity.
	var otherSeed [Ed25519SeedSize]byte
	if _, err := rand.Read(otherSeed[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	dirC := t.TempDir()
	mC, err := NewManager(dirC)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	idC, err := mC.GenerateIdentityFromSeed(otherSeed)
	if err != nil {
		t.Fatalf("GenerateIdentityFromSeed (C): %v", err)
	}
	if string(idA.SigningKey.PublicKey) == string(idC.SigningKey.PublicKey) {
		t.Error("different seeds produced the same signing public key")
	}

	// GenerateIdentityFromSeed round-trips through LoadIdentity exactly like
	// GenerateIdentity does.
	mALoaded, err := NewManager(dirA)
	if err != nil {
		t.Fatalf("NewManager (reload): %v", err)
	}
	loaded, err := mALoaded.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if string(loaded.SigningKey.PrivateKey) != string(idA.SigningKey.PrivateKey) {
		t.Error("LoadIdentity did not round-trip a GenerateIdentityFromSeed identity")
	}
}

func TestGenerateIdentityFromSeed_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.GenerateIdentity(); err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	var seed [Ed25519SeedSize]byte
	if _, err := rand.Read(seed[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	if _, err := m.GenerateIdentityFromSeed(seed); err != ErrKeyAlreadyExists {
		t.Errorf("GenerateIdentityFromSeed after GenerateIdentity: error = %v, want ErrKeyAlreadyExists", err)
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
