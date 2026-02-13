package setup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/admin"
	"github.com/spacedatanetwork/sdn-server/internal/audit"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// setupTestDeps creates all the managers needed for an integration test.
func setupTestDeps(t *testing.T) (string, *Manager, *keys.Manager, *admin.Manager, *audit.Logger) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "sdn-setup-integration-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	setupMgr, err := NewManager(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create setup manager: %v", err)
	}

	keyMgr, err := keys.NewManager(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create key manager: %v", err)
	}

	adminMgr, err := admin.NewManager(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create admin manager: %v", err)
	}

	auditLog, err := audit.NewLogger(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create audit logger: %v", err)
	}

	return tmpDir, setupMgr, keyMgr, adminMgr, auditLog
}

func TestFullSetupFlow(t *testing.T) {
	tmpDir, setupMgr, keyMgr, adminMgr, auditLog := setupTestDeps(t)
	defer os.RemoveAll(tmpDir)
	defer adminMgr.Close()
	defer auditLog.Close()

	// 1. Verify setup is required
	if !setupMgr.IsSetupRequired() {
		t.Fatal("Setup should be required initially")
	}

	// 2. Start setup mode and get token
	token, err := setupMgr.StartSetupMode()
	if err != nil {
		t.Fatalf("Failed to start setup mode: %v", err)
	}

	// 3. Create handler and submit setup via API
	handler := NewHandler(setupMgr, keyMgr, adminMgr, auditLog)

	reqBody := SetupRequest{
		Token:      token,
		Username:   "admin",
		Password:   "SecurePassword123",
		ServerName: "Test SDN Server",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleSetupAPI(w, req)

	// 4. Verify response
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got: %d, body: %s", w.Code, w.Body.String())
	}

	var resp SetupResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if !resp.Success {
		t.Fatalf("Setup should succeed, error: %s", resp.Error)
	}
	if resp.SigningPublicKey == "" {
		t.Error("Signing public key should not be empty")
	}
	if resp.EncryptionPublicKey == "" {
		t.Error("Encryption public key should not be empty")
	}
	if resp.Fingerprint == "" {
		t.Error("Fingerprint should not be empty")
	}

	// 5. Verify setup is complete
	if !setupMgr.IsSetupComplete() {
		t.Error("Setup should be complete")
	}

	// 6. Verify admin account was created
	if !adminMgr.HasAdmin() {
		t.Error("Admin account should exist")
	}

	// 7. Verify admin can authenticate
	sessionToken, err := adminMgr.Authenticate("admin", "SecurePassword123", "127.0.0.1", "test", false)
	if err != nil {
		t.Fatalf("Admin authentication failed: %v", err)
	}
	if sessionToken == "" {
		t.Error("Session token should not be empty")
	}

	// 8. Verify identity keys exist
	if !keyMgr.HasIdentity() {
		t.Error("Identity keys should exist")
	}

	// 9. Verify audit log recorded the setup
	count, _ := auditLog.Count()
	if count == 0 {
		t.Error("Audit log should have entries after setup")
	}

	// 10. Verify audit chain integrity
	valid, err := auditLog.VerifyChain()
	if err != nil {
		t.Fatalf("Audit chain verification error: %v", err)
	}
	if !valid {
		t.Error("Audit chain should be valid")
	}
}

func TestSetupPageRedirectsAfterComplete(t *testing.T) {
	tmpDir, setupMgr, keyMgr, adminMgr, auditLog := setupTestDeps(t)
	defer os.RemoveAll(tmpDir)
	defer adminMgr.Close()
	defer auditLog.Close()

	// Complete setup
	token, _ := setupMgr.StartSetupMode()
	setupMgr.VerifyToken(token)
	setupMgr.CompleteSetup()

	handler := NewHandler(setupMgr, keyMgr, adminMgr, auditLog)

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	w := httptest.NewRecorder()

	handler.HandleSetupPage(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect (303), got: %d", w.Code)
	}
}

func TestSetupAPIRejectsInvalidToken(t *testing.T) {
	tmpDir, setupMgr, keyMgr, adminMgr, auditLog := setupTestDeps(t)
	defer os.RemoveAll(tmpDir)
	defer adminMgr.Close()
	defer auditLog.Close()

	setupMgr.StartSetupMode()
	handler := NewHandler(setupMgr, keyMgr, adminMgr, auditLog)

	reqBody := SetupRequest{
		Token:    "SETUP-0000-0000-0000-0000-0000-0000-0000",
		Username: "admin",
		Password: "SecurePassword123",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleSetupAPI(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 for invalid token, got: %d", w.Code)
	}
}

func TestSetupAPIRejectsShortPassword(t *testing.T) {
	tmpDir, setupMgr, keyMgr, adminMgr, auditLog := setupTestDeps(t)
	defer os.RemoveAll(tmpDir)
	defer adminMgr.Close()
	defer auditLog.Close()

	token, _ := setupMgr.StartSetupMode()
	handler := NewHandler(setupMgr, keyMgr, adminMgr, auditLog)

	reqBody := SetupRequest{
		Token:    token,
		Username: "admin",
		Password: "short",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleSetupAPI(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for short password, got: %d", w.Code)
	}
}

func TestSetupAPIRejectsShortUsername(t *testing.T) {
	tmpDir, setupMgr, keyMgr, adminMgr, auditLog := setupTestDeps(t)
	defer os.RemoveAll(tmpDir)
	defer adminMgr.Close()
	defer auditLog.Close()

	token, _ := setupMgr.StartSetupMode()
	handler := NewHandler(setupMgr, keyMgr, adminMgr, auditLog)

	reqBody := SetupRequest{
		Token:    token,
		Username: "ab",
		Password: "SecurePassword123",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleSetupAPI(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for short username, got: %d", w.Code)
	}
}

func TestSetupAPIRejectsAfterCompletion(t *testing.T) {
	tmpDir, setupMgr, keyMgr, adminMgr, auditLog := setupTestDeps(t)
	defer os.RemoveAll(tmpDir)
	defer adminMgr.Close()
	defer auditLog.Close()

	// Complete setup first
	token, _ := setupMgr.StartSetupMode()
	handler := NewHandler(setupMgr, keyMgr, adminMgr, auditLog)

	reqBody := SetupRequest{
		Token:      token,
		Username:   "admin",
		Password:   "SecurePassword123",
		ServerName: "Test",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.HandleSetupAPI(w, req)

	// Try again - should fail
	body2, _ := json.Marshal(SetupRequest{
		Token:    "SETUP-0000-0000-0000-0000-0000-0000-0000",
		Username: "admin2",
		Password: "SecurePassword123",
	})

	req2 := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	handler.HandleSetupAPI(w2, req2)

	if w2.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for duplicate setup, got: %d", w2.Code)
	}
}

func TestSetupAPIRejectsGetMethod(t *testing.T) {
	tmpDir, setupMgr, keyMgr, adminMgr, auditLog := setupTestDeps(t)
	defer os.RemoveAll(tmpDir)
	defer adminMgr.Close()
	defer auditLog.Close()

	handler := NewHandler(setupMgr, keyMgr, adminMgr, auditLog)

	req := httptest.NewRequest(http.MethodGet, "/api/setup", nil)
	w := httptest.NewRecorder()

	handler.HandleSetupAPI(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405, got: %d", w.Code)
	}
}
