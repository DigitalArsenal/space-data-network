package audit

import (
	"os"
	"testing"
)

func TestTamperDetection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-tamper-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	// Log some entries
	l.Log(EventTypeAdminLogin, SeverityInfo, "Login 1", 1, "127.0.0.1", nil)
	l.Log(EventTypeConfigChange, SeverityInfo, "Config change", 1, "127.0.0.1",
		map[string]interface{}{"key": "test", "value": "new"})
	l.Log(EventTypePeerAdd, SeverityInfo, "Peer added", 1, "127.0.0.2", nil)

	// Verify chain is valid
	valid, err := l.VerifyChain()
	if err != nil || !valid {
		t.Fatalf("Chain should be valid: valid=%v err=%v", valid, err)
	}

	// Tamper with an entry (modify description directly in DB)
	_, err = l.db.Exec("UPDATE audit_log SET description = 'TAMPERED' WHERE id = 2")
	if err != nil {
		t.Fatalf("Failed to tamper: %v", err)
	}

	// Chain should now be invalid
	valid, err = l.VerifyChain()
	if valid {
		t.Error("Chain should be invalid after tampering")
	}
	if err != ErrLogTampered {
		t.Errorf("Expected ErrLogTampered, got: %v", err)
	}
}

func TestChainBreakDetection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-chain-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	l.Log(EventTypeServerStart, SeverityInfo, "Start", 0, "", nil)
	l.Log(EventTypeAdminLogin, SeverityInfo, "Login", 1, "127.0.0.1", nil)
	l.Log(EventTypeConfigChange, SeverityInfo, "Config", 1, "127.0.0.1", nil)

	// Tamper with chain link (modify previous_hash)
	_, err = l.db.Exec("UPDATE audit_log SET previous_hash = 'fakehash0000' WHERE id = 3")
	if err != nil {
		t.Fatalf("Failed to tamper chain: %v", err)
	}

	valid, err := l.VerifyChain()
	if valid {
		t.Error("Chain should detect broken link")
	}
	if err != ErrLogTampered {
		t.Errorf("Expected ErrLogTampered, got: %v", err)
	}
}

func TestAuditLogSecurityEvents(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-security-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Simulate complete server lifecycle
	l.LogSetupStart("127.0.0.1")
	l.LogKeyGenerate(0, "127.0.0.1", "abc123def456")
	l.LogSetupComplete(1, "127.0.0.1", "abc123def456")
	l.LogServerStart("abc123def456")
	l.LogAdminLogin(1, "admin", "127.0.0.1", true)
	l.LogAdminLogin(0, "hacker", "10.0.0.1", false)
	l.LogPasswordChange(1, "127.0.0.1")
	l.LogPeerTrustChange(1, "127.0.0.1", "QmPeer1", 0, 2)
	l.LogConfigChange(1, "127.0.0.1", "network.port", 4001, 4002)
	l.LogKeyBackup(1, "127.0.0.1")
	l.LogServerStop()

	// Verify full chain
	valid, err := l.VerifyChain()
	if err != nil || !valid {
		t.Fatalf("Chain should be valid after full lifecycle: %v", err)
	}

	// Query security-relevant events
	warnings, _ := l.Query(QueryOptions{Severity: SeverityWarning})
	if len(warnings) != 1 {
		t.Errorf("Expected 1 warning (failed login), got: %d", len(warnings))
	}

	// Query login events
	logins, _ := l.Query(QueryOptions{EventType: EventTypeAdminLogin})
	if len(logins) != 2 {
		t.Errorf("Expected 2 login events, got: %d", len(logins))
	}

	// Export should include all entries
	data, err := l.Export()
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	if len(data) < 100 {
		t.Error("Export data seems too small")
	}

	// Total count
	count, _ := l.Count()
	if count != 11 {
		t.Errorf("Expected 11 entries, got: %d", count)
	}
}

func TestAuditLogContinuity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-continuity-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// First server session
	l1, _ := NewLogger(tmpDir)
	l1.LogServerStart("fp1")
	l1.LogAdminLogin(1, "admin", "127.0.0.1", true)
	hash1 := l1.LastHash()
	l1.LogServerStop()
	l1.Close()

	// Second server session (restart)
	l2, _ := NewLogger(tmpDir)

	// Hash should be loaded from DB
	if l2.LastHash() != l1.LastHash() {
		t.Error("Last hash should persist across restarts")
	}

	// New entries should continue the chain
	l2.LogServerStart("fp1")
	if l2.LastHash() == hash1 {
		t.Error("Hash should change after new entry")
	}

	// Full chain should still verify
	valid, err := l2.VerifyChain()
	if err != nil || !valid {
		t.Fatalf("Chain should be valid across restarts: %v", err)
	}

	count, _ := l2.Count()
	if count != 4 { // start, login, stop, start
		t.Errorf("Expected 4 entries across sessions, got: %d", count)
	}
	l2.Close()
}
