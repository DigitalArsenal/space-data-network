package admin

import (
	"os"
	"testing"
)

func TestTOTPEnrollmentFlow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-totp-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	// Create admin
	m.CreateAdmin("admin", "Password123456")

	// Authenticate without TOTP
	token, err := m.Authenticate("admin", "Password123456", "127.0.0.1", "test", false)
	if err != nil {
		t.Fatalf("Auth failed: %v", err)
	}
	session, _ := m.ValidateSession(token)
	adminID := session.AdminID

	// Generate TOTP secret
	secret, uri, err := GenerateTOTPSetup("admin")
	if err != nil {
		t.Fatalf("Failed to generate TOTP setup: %v", err)
	}
	if uri == "" {
		t.Error("URI should not be empty")
	}

	// Generate a valid TOTP code from the secret
	code, err := GenerateTOTPCode(secret)
	if err != nil {
		t.Fatalf("Failed to generate TOTP code: %v", err)
	}

	// Verify code is valid before enabling
	if !ValidateTOTP(secret, code) {
		t.Fatal("Generated code should validate")
	}

	// Enable TOTP
	err = m.EnableTOTP(adminID, secret)
	if err != nil {
		t.Fatalf("Failed to enable TOTP: %v", err)
	}

	// Verify TOTP is enabled
	adm, _ := m.GetAdmin(adminID)
	if !adm.TOTPEnabled {
		t.Error("TOTP should be enabled")
	}

	// Now auth without TOTP should return ErrTOTPRequired
	_, err = m.Authenticate("admin", "Password123456", "127.0.0.1", "test", false)
	if err != ErrTOTPRequired {
		t.Errorf("Expected ErrTOTPRequired, got: %v", err)
	}

	// Auth with TOTP code should succeed
	newCode, _ := GenerateTOTPCode(secret)
	token2, err := m.AuthenticateWithTOTP("admin", "Password123456", newCode, "127.0.0.1", "test", false)
	if err != nil {
		t.Fatalf("Auth with TOTP failed: %v", err)
	}
	if token2 == "" {
		t.Error("Token should not be empty")
	}

	// Auth with wrong TOTP code should fail
	_, err = m.AuthenticateWithTOTP("admin", "Password123456", "000000", "127.0.0.1", "test", false)
	if err != ErrTOTPInvalid {
		t.Errorf("Expected ErrTOTPInvalid, got: %v", err)
	}

	// Auth with wrong password but valid TOTP should still fail
	validCode, _ := GenerateTOTPCode(secret)
	_, err = m.AuthenticateWithTOTP("admin", "wrongpassword", validCode, "127.0.0.1", "test", false)
	if err != ErrInvalidCredentials {
		t.Errorf("Expected ErrInvalidCredentials, got: %v", err)
	}

	// Disable TOTP
	err = m.DisableTOTP(adminID)
	if err != nil {
		t.Fatalf("Failed to disable TOTP: %v", err)
	}

	// Auth without TOTP should work again
	token3, err := m.Authenticate("admin", "Password123456", "127.0.0.1", "test", false)
	if err != nil {
		t.Fatalf("Auth after TOTP disable failed: %v", err)
	}
	if token3 == "" {
		t.Error("Token should not be empty after TOTP disable")
	}
}

func TestTOTPWithPasswordChange(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-totp-pwchange-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("admin", "OldPassword123")
	token, _ := m.Authenticate("admin", "OldPassword123", "127.0.0.1", "test", false)
	session, _ := m.ValidateSession(token)

	// Enable TOTP
	secret, _, _ := GenerateTOTPSetup("admin")
	m.EnableTOTP(session.AdminID, secret)

	// Change password
	err = m.ChangePassword(session.AdminID, "OldPassword123", "NewPassword123")
	if err != nil {
		t.Fatalf("Password change failed: %v", err)
	}

	// Old session should be revoked
	_, err = m.ValidateSession(token)
	if err != ErrSessionExpired {
		t.Errorf("Old session should be revoked: %v", err)
	}

	// Should still require TOTP with new password
	_, err = m.Authenticate("admin", "NewPassword123", "127.0.0.1", "test", false)
	if err != ErrTOTPRequired {
		t.Errorf("Should still require TOTP after password change: %v", err)
	}

	// Auth with new password + TOTP should work
	code, _ := GenerateTOTPCode(secret)
	newToken, err := m.AuthenticateWithTOTP("admin", "NewPassword123", code, "127.0.0.1", "test", false)
	if err != nil {
		t.Fatalf("Auth with new password + TOTP failed: %v", err)
	}
	if newToken == "" {
		t.Error("Token should not be empty")
	}
}

func TestSessionSecurityProperties(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-session-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("admin", "Password123456")

	// Short session (no remember me)
	token1, _ := m.Authenticate("admin", "Password123456", "127.0.0.1", "agent1", false)
	session1, _ := m.ValidateSession(token1)

	// Long session (remember me)
	token2, _ := m.Authenticate("admin", "Password123456", "127.0.0.1", "agent2", true)
	session2, _ := m.ValidateSession(token2)

	// Long session should expire later
	if !session2.ExpiresAt.After(session1.ExpiresAt) {
		t.Error("Remember-me session should expire later than short session")
	}

	// Sessions should have different tokens
	if token1 == token2 {
		t.Error("Each session should have a unique token")
	}

	// Session should track IP and user agent
	if session1.IPAddress != "127.0.0.1" {
		t.Errorf("Session should track IP: %s", session1.IPAddress)
	}
	if session1.UserAgent != "agent1" {
		t.Errorf("Session should track user agent: %s", session1.UserAgent)
	}

	// Revoking one session should not affect the other
	m.RevokeSession(token1)
	_, err1 := m.ValidateSession(token1)
	_, err2 := m.ValidateSession(token2)
	if err1 != ErrSessionExpired {
		t.Error("Revoked session should be expired")
	}
	if err2 != nil {
		t.Error("Other session should still be valid")
	}
}
