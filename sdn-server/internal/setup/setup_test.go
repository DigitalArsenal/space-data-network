package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupManager(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "sdn-setup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create manager
	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Test initial state
	if m.IsSetupComplete() {
		t.Error("Setup should not be complete initially")
	}

	if !m.IsSetupRequired() {
		t.Error("Setup should be required initially")
	}
}

func TestSetupToken(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-setup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Generate token
	token, err := m.StartSetupMode()
	if err != nil {
		t.Fatalf("Failed to start setup mode: %v", err)
	}

	// Check token format
	if !strings.HasPrefix(token, "SETUP-") {
		t.Errorf("Token should start with 'SETUP-', got: %s", token)
	}

	// Token should be 32 hex chars + prefix + dashes
	// Format: SETUP-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX
	parts := strings.Split(token, "-")
	if len(parts) != 9 {
		t.Errorf("Token should have 9 parts, got: %d", len(parts))
	}

	// Verify wrong token fails
	err = m.VerifyToken("SETUP-0000-0000-0000-0000-0000-0000-0000")
	if err != ErrSetupTokenInvalid {
		t.Errorf("Expected ErrSetupTokenInvalid, got: %v", err)
	}

	// Verify correct token succeeds
	err = m.VerifyToken(token)
	if err != nil {
		t.Errorf("Token verification should succeed: %v", err)
	}

	// Token should be marked as used
	err = m.VerifyToken(token)
	if err != ErrSetupTokenExpired {
		t.Errorf("Expected ErrSetupTokenExpired for reused token, got: %v", err)
	}
}

func TestSetupCompletion(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-setup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Start setup
	token, err := m.StartSetupMode()
	if err != nil {
		t.Fatalf("Failed to start setup mode: %v", err)
	}

	// Verify token
	err = m.VerifyToken(token)
	if err != nil {
		t.Fatalf("Token verification failed: %v", err)
	}

	// Complete setup
	err = m.CompleteSetup()
	if err != nil {
		t.Fatalf("Failed to complete setup: %v", err)
	}

	// Check state
	if !m.IsSetupComplete() {
		t.Error("Setup should be complete")
	}

	// Check marker file exists
	markerPath := filepath.Join(tmpDir, SetupStateFile)
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		t.Error("Setup marker file should exist")
	}

	// Cannot start setup again
	_, err = m.StartSetupMode()
	if err != ErrSetupAlreadyComplete {
		t.Errorf("Expected ErrSetupAlreadyComplete, got: %v", err)
	}
}

func TestSetupPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-setup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create and complete setup
	m1, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	token, _ := m1.StartSetupMode()
	m1.VerifyToken(token)
	m1.CompleteSetup()

	// Create new manager instance
	m2, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create second manager: %v", err)
	}

	// Should detect completed setup
	if !m2.IsSetupComplete() {
		t.Error("New manager should detect completed setup")
	}
}

func TestTokenExpiry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-setup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Start setup
	_, err = m.StartSetupMode()
	if err != nil {
		t.Fatalf("Failed to start setup mode: %v", err)
	}

	// Check remaining time
	remaining := m.RemainingTime()
	if remaining <= 0 || remaining > TokenExpiry {
		t.Errorf("Remaining time should be between 0 and %v, got: %v", TokenExpiry, remaining)
	}

	// Check expiry time
	expiry := m.GetTokenExpiry()
	if expiry.Before(time.Now()) || expiry.After(time.Now().Add(TokenExpiry+time.Second)) {
		t.Errorf("Expiry time is unexpected: %v", expiry)
	}
}

func TestFormatToken(t *testing.T) {
	// Test with known input
	data := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	token := formatToken(data)

	if !strings.HasPrefix(token, "SETUP-") {
		t.Errorf("Token should start with SETUP-, got: %s", token)
	}

	// Should contain only uppercase hex and dashes
	cleaned := strings.ReplaceAll(strings.TrimPrefix(token, "SETUP-"), "-", "")
	for _, c := range cleaned {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("Token contains invalid character: %c", c)
		}
	}
}

func TestPrintSetupBanner(t *testing.T) {
	// Just ensure it doesn't panic
	PrintSetupBanner("SETUP-test-token-here-abcd-1234-5678-9012", "localhost:5001")
}
