package admin

import (
	"os"
	"testing"
	"time"
)

func TestAdminManager(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	// Should not have admin initially
	if m.HasAdmin() {
		t.Error("Should not have admin initially")
	}
}

func TestCreateAdmin(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
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
	err = m.CreateAdmin("testadmin", "testpassword123")
	if err != nil {
		t.Fatalf("Failed to create admin: %v", err)
	}

	// Should have admin now
	if !m.HasAdmin() {
		t.Error("Should have admin after creation")
	}

	// Cannot create duplicate
	err = m.CreateAdmin("testadmin", "anotherpassword")
	if err != ErrAdminExists {
		t.Errorf("Expected ErrAdminExists, got: %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
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
	m.CreateAdmin("testadmin", "testpassword123")

	// Authenticate with wrong password
	_, err = m.Authenticate("testadmin", "wrongpassword", "127.0.0.1", "test-agent", false)
	if err != ErrInvalidCredentials {
		t.Errorf("Expected ErrInvalidCredentials, got: %v", err)
	}

	// Authenticate with wrong username
	_, err = m.Authenticate("wronguser", "testpassword123", "127.0.0.1", "test-agent", false)
	if err != ErrInvalidCredentials {
		t.Errorf("Expected ErrInvalidCredentials, got: %v", err)
	}

	// Authenticate with correct credentials
	token, err := m.Authenticate("testadmin", "testpassword123", "127.0.0.1", "test-agent", false)
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	if token == "" {
		t.Error("Token should not be empty")
	}
}

func TestSessionValidation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("testadmin", "testpassword123")

	// Get session token
	token, _ := m.Authenticate("testadmin", "testpassword123", "127.0.0.1", "test-agent", false)

	// Validate session
	session, err := m.ValidateSession(token)
	if err != nil {
		t.Fatalf("Session validation failed: %v", err)
	}

	if session.AdminID == 0 {
		t.Error("Session should have admin ID")
	}
	if session.IPAddress != "127.0.0.1" {
		t.Errorf("Session IP wrong: %s", session.IPAddress)
	}

	// Invalid token should fail
	_, err = m.ValidateSession("invalid-token")
	if err != ErrSessionNotFound {
		t.Errorf("Expected ErrSessionNotFound, got: %v", err)
	}
}

func TestSessionRevocation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("testadmin", "testpassword123")
	token, _ := m.Authenticate("testadmin", "testpassword123", "127.0.0.1", "test-agent", false)

	// Revoke session
	err = m.RevokeSession(token)
	if err != nil {
		t.Fatalf("Failed to revoke session: %v", err)
	}

	// Session should be invalid
	_, err = m.ValidateSession(token)
	if err != ErrSessionExpired {
		t.Errorf("Expected ErrSessionExpired, got: %v", err)
	}
}

func TestChangePassword(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("testadmin", "oldpassword123")
	token, _ := m.Authenticate("testadmin", "oldpassword123", "127.0.0.1", "test-agent", false)

	// Get admin ID
	session, _ := m.ValidateSession(token)
	adminID := session.AdminID

	// Change password with wrong old password
	err = m.ChangePassword(adminID, "wrongold", "newpassword123")
	if err != ErrInvalidCredentials {
		t.Errorf("Expected ErrInvalidCredentials, got: %v", err)
	}

	// Change password correctly
	err = m.ChangePassword(adminID, "oldpassword123", "newpassword123")
	if err != nil {
		t.Fatalf("Failed to change password: %v", err)
	}

	// Old session should be revoked
	_, err = m.ValidateSession(token)
	if err != ErrSessionExpired {
		t.Errorf("Old session should be revoked, got: %v", err)
	}

	// Can authenticate with new password
	_, err = m.Authenticate("testadmin", "newpassword123", "127.0.0.1", "test-agent", false)
	if err != nil {
		t.Errorf("Should be able to login with new password: %v", err)
	}

	// Cannot authenticate with old password
	_, err = m.Authenticate("testadmin", "oldpassword123", "127.0.0.1", "test-agent", false)
	if err != ErrInvalidCredentials {
		t.Errorf("Should not be able to login with old password")
	}
}

func TestRevokeAllSessions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("testadmin", "testpassword123")

	// Create multiple sessions
	token1, _ := m.Authenticate("testadmin", "testpassword123", "127.0.0.1", "agent1", false)
	token2, _ := m.Authenticate("testadmin", "testpassword123", "127.0.0.2", "agent2", false)
	token3, _ := m.Authenticate("testadmin", "testpassword123", "127.0.0.3", "agent3", false)

	// Get admin ID
	session, _ := m.ValidateSession(token1)
	adminID := session.AdminID

	// Revoke all sessions
	err = m.RevokeAllSessions(adminID)
	if err != nil {
		t.Fatalf("Failed to revoke all sessions: %v", err)
	}

	// All sessions should be invalid
	for _, token := range []string{token1, token2, token3} {
		_, err := m.ValidateSession(token)
		if err != ErrSessionExpired {
			t.Errorf("Session should be revoked: %v", err)
		}
	}
}

func TestGetAdmin(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("testadmin", "testpassword123")
	token, _ := m.Authenticate("testadmin", "testpassword123", "127.0.0.1", "test-agent", false)
	session, _ := m.ValidateSession(token)

	// Get admin
	admin, err := m.GetAdmin(session.AdminID)
	if err != nil {
		t.Fatalf("Failed to get admin: %v", err)
	}

	if admin.Username != "testadmin" {
		t.Errorf("Wrong username: %s", admin.Username)
	}
	if admin.LastLoginAt == nil {
		t.Error("Last login should be set")
	}
}

func TestListActiveSessions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("testadmin", "testpassword123")

	// Create sessions
	token1, _ := m.Authenticate("testadmin", "testpassword123", "127.0.0.1", "agent1", false)
	m.Authenticate("testadmin", "testpassword123", "127.0.0.2", "agent2", false)

	session, _ := m.ValidateSession(token1)

	// List sessions
	sessions, err := m.ListActiveSessions(session.AdminID)
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}

	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions, got: %d", len(sessions))
	}
}

func TestCleanupExpiredSessions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("testadmin", "testpassword123")
	m.Authenticate("testadmin", "testpassword123", "127.0.0.1", "test-agent", false)

	// Cleanup should not remove active sessions
	deleted, err := m.CleanupExpiredSessions()
	if err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Should not have deleted any sessions, deleted: %d", deleted)
	}
}

func TestHashToken(t *testing.T) {
	token := "test-session-token-12345"
	hash := HashToken(token)

	// Should be 16 hex chars (8 bytes)
	if len(hash) != 16 {
		t.Errorf("Hash should be 16 chars, got: %d", len(hash))
	}

	// Same token should produce same hash
	hash2 := HashToken(token)
	if hash != hash2 {
		t.Error("Same token should produce same hash")
	}

	// Different token should produce different hash
	hash3 := HashToken("different-token")
	if hash == hash3 {
		t.Error("Different tokens should produce different hashes")
	}
}

func TestSessionExpiry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-admin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer m.Close()

	m.CreateAdmin("testadmin", "testpassword123")

	// Create session without remember me (short expiry)
	_, _ = m.Authenticate("testadmin", "testpassword123", "127.0.0.1", "test-agent", false)

	// Create session with remember me (long expiry)
	token2, _ := m.Authenticate("testadmin", "testpassword123", "127.0.0.1", "test-agent", true)

	session, _ := m.ValidateSession(token2)

	// Long session should expire after short session
	expectedExpiry := time.Now().Add(SessionExpiry)
	if session.ExpiresAt.Before(expectedExpiry.Add(-time.Minute)) {
		t.Error("Long session should have longer expiry")
	}
}
