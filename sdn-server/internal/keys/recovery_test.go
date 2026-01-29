package keys

import (
	"os"
	"testing"
)

func TestFullBackupRecoveryCycle(t *testing.T) {
	// Create original identity
	tmpDir1, err := os.MkdirTemp("", "sdn-keys-recovery1-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir1)

	m1, err := NewManager(tmpDir1)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	orig, err := m1.GenerateIdentity()
	if err != nil {
		t.Fatalf("Failed to generate identity: %v", err)
	}

	origFingerprint := m1.PublicKeyFingerprint()

	// Sign some data with original key
	testData := []byte("important space data message")
	signature, err := m1.Sign(testData)
	if err != nil {
		t.Fatalf("Failed to sign: %v", err)
	}

	// Export encrypted backup
	password := "backup-password-secure-123"
	backup, err := m1.ExportEncrypted(password)
	if err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Simulate server crash / new server - create fresh manager
	tmpDir2, err := os.MkdirTemp("", "sdn-keys-recovery2-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir2)

	m2, err := NewManager(tmpDir2)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Should not have identity
	if m2.HasIdentity() {
		t.Error("New manager should not have identity")
	}

	// Restore from backup
	err = m2.ImportEncrypted(backup, password)
	if err != nil {
		t.Fatalf("Failed to import backup: %v", err)
	}

	// Should have identity now
	if !m2.HasIdentity() {
		t.Error("Should have identity after restore")
	}

	// Fingerprint should match
	if m2.PublicKeyFingerprint() != origFingerprint {
		t.Errorf("Fingerprint mismatch: %s vs %s", m2.PublicKeyFingerprint(), origFingerprint)
	}

	// Should be able to verify old signatures
	if !m2.Verify(orig.SigningKey.PublicKey, testData, signature) {
		t.Error("Restored key should verify old signatures")
	}

	// Should be able to sign new data
	newData := []byte("new message after recovery")
	newSig, err := m2.Sign(newData)
	if err != nil {
		t.Fatalf("Failed to sign after recovery: %v", err)
	}

	// Original manager should verify new signature
	if !m1.Verify(orig.SigningKey.PublicKey, newData, newSig) {
		t.Error("Original key should verify signature from restored key")
	}
}

func TestQRCodeBackupRecovery(t *testing.T) {
	tmpDir1, err := os.MkdirTemp("", "sdn-keys-qr1-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir1)

	m1, err := NewManager(tmpDir1)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	m1.GenerateIdentity()
	origFingerprint := m1.PublicKeyFingerprint()

	// Export as QR data
	password := "qr-backup-pw"
	qrData, err := m1.GenerateQRData(password)
	if err != nil {
		t.Fatalf("Failed to generate QR data: %v", err)
	}

	// Restore on new server
	tmpDir2, err := os.MkdirTemp("", "sdn-keys-qr2-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir2)

	m2, err := NewManager(tmpDir2)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	err = m2.ImportFromQRData(qrData, password)
	if err != nil {
		t.Fatalf("Failed to import from QR: %v", err)
	}

	if m2.PublicKeyFingerprint() != origFingerprint {
		t.Error("QR restore should produce same fingerprint")
	}
}

func TestBackupWrongPasswordFails(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-wrongpw-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	m.GenerateIdentity()

	backup, err := m.ExportEncrypted("correct-password")
	if err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Try to import with wrong password on new manager
	tmpDir2, _ := os.MkdirTemp("", "sdn-keys-wrongpw2-*")
	defer os.RemoveAll(tmpDir2)

	m2, _ := NewManager(tmpDir2)

	err = m2.ImportEncrypted(backup, "wrong-password")
	if err != ErrDecryptionFailed {
		t.Errorf("Expected ErrDecryptionFailed, got: %v", err)
	}
}

func TestMnemonicExport(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-mnemonic-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	m.GenerateIdentity()

	// Export mnemonic
	mnemonic, err := m.ExportMnemonic()
	if err != nil {
		t.Fatalf("Failed to export mnemonic: %v", err)
	}

	// Verify it has 24 words
	words := splitWords(mnemonic)
	if len(words) != 24 {
		t.Errorf("Expected 24 words, got: %d", len(words))
	}

	// All words should be from BIP39 wordlist
	wordSet := make(map[string]bool)
	for _, w := range bip39Words {
		wordSet[w] = true
	}
	for _, w := range words {
		if !wordSet[w] {
			t.Errorf("Word not in BIP39 list: %s", w)
		}
	}

	// Same identity should produce same mnemonic
	mnemonic2, _ := m.ExportMnemonic()
	if mnemonic != mnemonic2 {
		t.Error("Same identity should produce same mnemonic")
	}
}

func TestExportMnemonicWithoutIdentity(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "sdn-keys-nomnemonic-*")
	defer os.RemoveAll(tmpDir)

	m, _ := NewManager(tmpDir)

	_, err := m.ExportMnemonic()
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound, got: %v", err)
	}
}

func TestKeyPersistenceAcrossRestarts(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-persist-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Generate identity
	m1, _ := NewManager(tmpDir)
	m1.GenerateIdentity()
	fp1 := m1.PublicKeyFingerprint()

	// "Restart" - new manager
	m2, _ := NewManager(tmpDir)
	identity, err := m2.LoadIdentity()
	if err != nil {
		t.Fatalf("Failed to load identity: %v", err)
	}

	// Verify same identity
	if m2.PublicKeyFingerprint() != fp1 {
		t.Error("Fingerprint should persist across restarts")
	}

	// Verify key types
	if identity.SigningKey.KeyType != "Ed25519" {
		t.Errorf("Wrong signing key type: %s", identity.SigningKey.KeyType)
	}
	if identity.EncryptionKey.KeyType != "X25519" {
		t.Errorf("Wrong encryption key type: %s", identity.EncryptionKey.KeyType)
	}
}
