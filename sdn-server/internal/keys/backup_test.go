package keys

import (
	"os"
	"testing"
)

func TestEncryptedBackup(t *testing.T) {
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
	origIdentity, err := m.GenerateIdentity()
	if err != nil {
		t.Fatalf("Failed to generate identity: %v", err)
	}

	// Export encrypted backup
	password := "test-password-123"
	backup, err := m.ExportEncrypted(password)
	if err != nil {
		t.Fatalf("Failed to export backup: %v", err)
	}

	// Backup should be JSON
	if backup[0] != '{' {
		t.Error("Backup should be JSON")
	}

	// Create new manager to import
	tmpDir2, err := os.MkdirTemp("", "sdn-keys-test2-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir2)

	m2, err := NewManager(tmpDir2)
	if err != nil {
		t.Fatalf("Failed to create second manager: %v", err)
	}

	// Import with wrong password should fail
	err = m2.ImportEncrypted(backup, "wrong-password")
	if err != ErrDecryptionFailed {
		t.Errorf("Expected ErrDecryptionFailed, got: %v", err)
	}

	// Import with correct password
	err = m2.ImportEncrypted(backup, password)
	if err != nil {
		t.Fatalf("Failed to import backup: %v", err)
	}

	// Verify keys match
	if string(m2.identity.SigningKey.PublicKey) != string(origIdentity.SigningKey.PublicKey) {
		t.Error("Imported signing public key doesn't match")
	}
	if string(m2.identity.SigningKey.PrivateKey) != string(origIdentity.SigningKey.PrivateKey) {
		t.Error("Imported signing private key doesn't match")
	}
	if string(m2.identity.EncryptionKey.PublicKey) != string(origIdentity.EncryptionKey.PublicKey) {
		t.Error("Imported encryption public key doesn't match")
	}
	if string(m2.identity.EncryptionKey.PrivateKey) != string(origIdentity.EncryptionKey.PrivateKey) {
		t.Error("Imported encryption private key doesn't match")
	}
}

func TestInvalidBackup(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Import invalid JSON
	err = m.ImportEncrypted("not valid json", "password")
	if err != ErrInvalidBackup {
		t.Errorf("Expected ErrInvalidBackup, got: %v", err)
	}

	// Import valid JSON but wrong format
	err = m.ImportEncrypted(`{"version": 99}`, "password")
	if err == nil {
		t.Error("Expected error for wrong version")
	}
}

func TestExportWithoutIdentity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-keys-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Export without identity should fail
	_, err = m.ExportEncrypted("password")
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound, got: %v", err)
	}
}

func TestMnemonic(t *testing.T) {
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

	// Export mnemonic
	mnemonic, err := m.ExportMnemonic()
	if err != nil {
		t.Fatalf("Failed to export mnemonic: %v", err)
	}

	// Should have 24 words
	words := len(splitWords(mnemonic))
	if words != 24 {
		t.Errorf("Mnemonic should have 24 words, got: %d", words)
	}
}

func splitWords(s string) []string {
	var words []string
	word := ""
	for _, c := range s {
		if c == ' ' {
			if word != "" {
				words = append(words, word)
				word = ""
			}
		} else {
			word += string(c)
		}
	}
	if word != "" {
		words = append(words, word)
	}
	return words
}

func TestQRData(t *testing.T) {
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

	// Generate QR data
	password := "test-password"
	qrData, err := m.GenerateQRData(password)
	if err != nil {
		t.Fatalf("Failed to generate QR data: %v", err)
	}

	// QR data should be base64 encoded
	if len(qrData) == 0 {
		t.Error("QR data should not be empty")
	}

	// Create new manager and import from QR
	tmpDir2, err := os.MkdirTemp("", "sdn-keys-test2-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir2)

	m2, err := NewManager(tmpDir2)
	if err != nil {
		t.Fatalf("Failed to create second manager: %v", err)
	}

	err = m2.ImportFromQRData(qrData, password)
	if err != nil {
		t.Fatalf("Failed to import from QR data: %v", err)
	}

	// Verify keys match
	if string(m2.identity.SigningKey.PublicKey) != string(m.identity.SigningKey.PublicKey) {
		t.Error("Imported signing key doesn't match")
	}
}
