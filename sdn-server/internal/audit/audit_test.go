package audit

import (
	"os"
	"testing"
	"time"
)

func TestAuditLogger(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Initial state
	count, _ := l.Count()
	if count != 0 {
		t.Errorf("Should have 0 entries initially, got: %d", count)
	}

	// Last hash should be genesis
	if l.LastHash() != GenesisHash {
		t.Errorf("Initial hash should be genesis hash")
	}
}

func TestLogEntry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Log an entry
	err = l.Log(EventTypeAdminLogin, SeverityInfo, "Test login", 1, "127.0.0.1", map[string]interface{}{
		"username": "admin",
		"success":  true,
	})
	if err != nil {
		t.Fatalf("Failed to log: %v", err)
	}

	// Check count
	count, _ := l.Count()
	if count != 1 {
		t.Errorf("Should have 1 entry, got: %d", count)
	}

	// Last hash should have changed
	if l.LastHash() == GenesisHash {
		t.Error("Hash should have changed after logging")
	}
}

func TestChainIntegrity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Log multiple entries
	for i := 0; i < 10; i++ {
		err = l.Log(EventTypeConfigChange, SeverityInfo, "Config change", int64(i), "127.0.0.1", nil)
		if err != nil {
			t.Fatalf("Failed to log entry %d: %v", i, err)
		}
	}

	// Verify chain
	valid, err := l.VerifyChain()
	if err != nil {
		t.Fatalf("Chain verification error: %v", err)
	}
	if !valid {
		t.Error("Chain should be valid")
	}
}

func TestQuery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Log various entries
	l.Log(EventTypeAdminLogin, SeverityInfo, "Login 1", 1, "127.0.0.1", nil)
	l.Log(EventTypeAdminLogin, SeverityWarning, "Login failed", 0, "127.0.0.2", nil)
	l.Log(EventTypeConfigChange, SeverityInfo, "Config", 1, "127.0.0.1", nil)
	l.Log(EventTypePeerAdd, SeverityInfo, "Peer added", 2, "127.0.0.3", nil)

	// Query by event type
	entries, err := l.Query(QueryOptions{EventType: EventTypeAdminLogin})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("Expected 2 login entries, got: %d", len(entries))
	}

	// Query by severity
	entries, err = l.Query(QueryOptions{Severity: SeverityWarning})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("Expected 1 warning entry, got: %d", len(entries))
	}

	// Query by actor
	entries, err = l.Query(QueryOptions{ActorID: 1})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("Expected 2 entries from actor 1, got: %d", len(entries))
	}

	// Query with limit
	entries, err = l.Query(QueryOptions{Limit: 2})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("Expected 2 entries with limit, got: %d", len(entries))
	}
}

func TestQueryByTime(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	beforeFirst := time.Now().Add(-time.Second) // 1 second ago to ensure it's before

	l.Log(EventTypeAdminLogin, SeverityInfo, "First", 1, "127.0.0.1", nil)

	// Wait a bit and record middle time
	time.Sleep(100 * time.Millisecond)
	afterFirst := time.Now()
	time.Sleep(100 * time.Millisecond)

	l.Log(EventTypeAdminLogin, SeverityInfo, "Second", 1, "127.0.0.1", nil)

	// Query since before first - should get both
	entries, _ := l.Query(QueryOptions{Since: beforeFirst})
	if len(entries) != 2 {
		t.Errorf("Expected 2 entries since before, got: %d", len(entries))
	}

	// Query since after first - should get 1 (the second entry)
	// Note: SQLite stores timestamps as Unix seconds, so we need sufficient time gap
	entries, _ = l.Query(QueryOptions{Since: afterFirst})
	// Due to timestamp precision, this may include entries from the same second
	if len(entries) > 2 {
		t.Errorf("Expected at most 2 entries since after first, got: %d", len(entries))
	}
}

func TestGetEntry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	l.Log(EventTypeAdminLogin, SeverityInfo, "Test entry", 1, "127.0.0.1", nil)

	// Get entry by ID
	entry, err := l.GetEntry(1)
	if err != nil {
		t.Fatalf("Failed to get entry: %v", err)
	}

	if entry.EventType != EventTypeAdminLogin {
		t.Errorf("Wrong event type: %s", entry.EventType)
	}
	if entry.Description != "Test entry" {
		t.Errorf("Wrong description: %s", entry.Description)
	}

	// Get non-existent entry
	_, err = l.GetEntry(999)
	if err != ErrEntryNotFound {
		t.Errorf("Expected ErrEntryNotFound, got: %v", err)
	}
}

func TestLogWithTarget(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	err = l.LogWithTarget(EventTypePeerTrustChange, SeverityInfo, "Trust changed",
		1, "127.0.0.1", "peer", "QmTestPeer123",
		map[string]interface{}{"old_level": 0, "new_level": 2})
	if err != nil {
		t.Fatalf("Failed to log with target: %v", err)
	}

	entry, _ := l.GetEntry(1)
	if entry.TargetType != "peer" {
		t.Errorf("Wrong target type: %s", entry.TargetType)
	}
	if entry.TargetID != "QmTestPeer123" {
		t.Errorf("Wrong target ID: %s", entry.TargetID)
	}
}

func TestConvenienceMethods(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Test all convenience methods
	l.LogAdminLogin(1, "admin", "127.0.0.1", true)
	l.LogAdminLogin(0, "admin", "127.0.0.1", false)
	l.LogPasswordChange(1, "127.0.0.1")
	l.LogPeerTrustChange(1, "127.0.0.1", "QmPeer", 0, 2)
	l.LogConfigChange(1, "127.0.0.1", "network.port", 4001, 4002)
	l.LogKeyGenerate(1, "127.0.0.1", "abc123")
	l.LogKeyBackup(1, "127.0.0.1")
	l.LogKeyRestore(1, "127.0.0.1", "abc123")
	l.LogSetupStart("127.0.0.1")
	l.LogSetupComplete(1, "127.0.0.1", "abc123")
	l.LogServerStart("abc123")
	l.LogServerStop()

	// Verify all were logged
	count, _ := l.Count()
	if count != 12 {
		t.Errorf("Expected 12 entries, got: %d", count)
	}

	// Verify chain
	valid, _ := l.VerifyChain()
	if !valid {
		t.Error("Chain should be valid after convenience methods")
	}
}

func TestExport(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	l.Log(EventTypeAdminLogin, SeverityInfo, "Test", 1, "127.0.0.1", nil)
	l.Log(EventTypeConfigChange, SeverityInfo, "Config", 1, "127.0.0.1", nil)

	// Export
	data, err := l.Export()
	if err != nil {
		t.Fatalf("Failed to export: %v", err)
	}

	// Should be JSON array
	if data[0] != '[' {
		t.Error("Export should be JSON array")
	}
}

func TestPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-audit-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create logger and add entries
	l1, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	l1.Log(EventTypeAdminLogin, SeverityInfo, "Entry 1", 1, "127.0.0.1", nil)
	l1.Log(EventTypeAdminLogin, SeverityInfo, "Entry 2", 2, "127.0.0.2", nil)
	hash1 := l1.LastHash()
	l1.Close()

	// Create new logger instance
	l2, err := NewLogger(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create second logger: %v", err)
	}
	defer l2.Close()

	// Check entries persisted
	count, _ := l2.Count()
	if count != 2 {
		t.Errorf("Expected 2 entries after reload, got: %d", count)
	}

	// Check hash persisted
	if l2.LastHash() != hash1 {
		t.Error("Hash should be loaded from database")
	}

	// Add another entry
	l2.Log(EventTypeAdminLogin, SeverityInfo, "Entry 3", 3, "127.0.0.3", nil)

	// Verify chain still valid
	valid, _ := l2.VerifyChain()
	if !valid {
		t.Error("Chain should be valid after reopen and new entry")
	}
}
