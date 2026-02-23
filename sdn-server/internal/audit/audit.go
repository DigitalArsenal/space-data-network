// Package audit provides tamper-evident audit logging for SDN servers.
// It implements a hash-linked log chain where each entry contains the hash
// of the previous entry, making tampering detectable.
package audit

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	_ "github.com/mattn/go-sqlite3"
)

var log = logging.Logger("sdn-audit")

// Event types
const (
	EventTypeAdminLogin       = "admin.login"
	EventTypeAdminLogout      = "admin.logout"
	EventTypeAdminCreate      = "admin.create"
	EventTypePasswordChange   = "admin.password_change"
	EventTypeTOTPEnable       = "admin.totp_enable"
	EventTypeTOTPDisable      = "admin.totp_disable"
	EventTypeSessionRevoke    = "admin.session_revoke"
	EventTypePeerTrustChange  = "peer.trust_change"
	EventTypePeerAdd          = "peer.add"
	EventTypePeerRemove       = "peer.remove"
	EventTypeConfigChange     = "config.change"
	EventTypeKeyGenerate      = "key.generate"
	EventTypeKeyBackup        = "key.backup"
	EventTypeKeyRestore       = "key.restore"
	EventTypeSetupStart       = "setup.start"
	EventTypeSetupComplete    = "setup.complete"
	EventTypeServerStart      = "server.start"
	EventTypeServerStop       = "server.stop"
)

// Severity levels
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
	SeverityCritical = "critical"
)

// Errors
var (
	ErrLogTampered   = errors.New("audit log tampering detected")
	ErrLogCorrupted  = errors.New("audit log corrupted")
	ErrEntryNotFound = errors.New("log entry not found")
)

const (
	// Database file
	AuditDBFile = "audit.db"

	// Genesis block hash (used for first entry)
	GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"
)

// Entry represents a single audit log entry.
type Entry struct {
	ID           int64     `json:"id"`
	Timestamp    time.Time `json:"timestamp"`
	EventType    string    `json:"event_type"`
	Severity     string    `json:"severity"`
	ActorID      int64     `json:"actor_id,omitempty"`      // Admin ID who performed action
	ActorIP      string    `json:"actor_ip,omitempty"`      // IP address
	TargetType   string    `json:"target_type,omitempty"`   // Type of target (peer, config, etc.)
	TargetID     string    `json:"target_id,omitempty"`     // ID of target
	Description  string    `json:"description"`
	Details      string    `json:"details,omitempty"`       // JSON encoded details
	PreviousHash string    `json:"previous_hash"`           // Hash of previous entry
	EntryHash    string    `json:"entry_hash"`              // Hash of this entry
}

// Logger provides tamper-evident audit logging.
type Logger struct {
	db           *sql.DB
	dbPath       string
	lastHash     string
	lastID       int64
	mu           sync.Mutex
}

// NewLogger creates a new audit logger.
func NewLogger(basePath string) (*Logger, error) {
	if err := os.MkdirAll(basePath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create audit directory: %w", err)
	}

	dbPath := filepath.Join(basePath, AuditDBFile)
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open audit database: %w", err)
	}

	l := &Logger{
		db:       db,
		dbPath:   dbPath,
		lastHash: GenesisHash,
	}

	if err := l.initDB(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize audit database: %w", err)
	}

	// Load last hash
	if err := l.loadLastHash(); err != nil {
		log.Warnf("Failed to load last hash: %v", err)
	}

	return l, nil
}

// initDB creates the audit log table.
func (l *Logger) initDB() error {
	_, err := l.db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			severity TEXT NOT NULL,
			actor_id INTEGER,
			actor_ip TEXT,
			target_type TEXT,
			target_id TEXT,
			description TEXT NOT NULL,
			details TEXT,
			previous_hash TEXT NOT NULL,
			entry_hash TEXT NOT NULL UNIQUE
		)
	`)
	if err != nil {
		return err
	}

	// Create indexes
	_, err = l.db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_log(timestamp)`)
	if err != nil {
		return err
	}
	_, err = l.db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_event_type ON audit_log(event_type)`)
	if err != nil {
		return err
	}
	_, err = l.db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_log(actor_id)`)

	return err
}

// loadLastHash loads the hash of the most recent log entry.
func (l *Logger) loadLastHash() error {
	var hash string
	var id int64
	err := l.db.QueryRow(`
		SELECT id, entry_hash FROM audit_log ORDER BY id DESC LIMIT 1
	`).Scan(&id, &hash)

	if err == sql.ErrNoRows {
		l.lastHash = GenesisHash
		l.lastID = 0
		return nil
	} else if err != nil {
		return err
	}

	l.lastHash = hash
	l.lastID = id
	return nil
}

// Log creates a new audit log entry.
func (l *Logger) Log(eventType, severity, description string, actorID int64, actorIP string, details map[string]interface{}) error {
	return l.LogWithTarget(eventType, severity, description, actorID, actorIP, "", "", details)
}

// LogWithTarget creates a new audit log entry with target information.
func (l *Logger) LogWithTarget(eventType, severity, description string, actorID int64, actorIP, targetType, targetID string, details map[string]interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	timestamp := time.Now().UTC()

	// Serialize details
	var detailsJSON string
	if details != nil {
		data, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("failed to marshal details: %w", err)
		}
		detailsJSON = string(data)
	}

	// Create entry for hashing
	entry := Entry{
		Timestamp:    timestamp,
		EventType:    eventType,
		Severity:     severity,
		ActorID:      actorID,
		ActorIP:      actorIP,
		TargetType:   targetType,
		TargetID:     targetID,
		Description:  description,
		Details:      detailsJSON,
		PreviousHash: l.lastHash,
	}

	// Compute hash
	entryHash := computeEntryHash(entry)
	entry.EntryHash = entryHash

	// Insert into database
	result, err := l.db.Exec(`
		INSERT INTO audit_log (timestamp, event_type, severity, actor_id, actor_ip,
			target_type, target_id, description, details, previous_hash, entry_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, timestamp.Unix(), eventType, severity, actorID, actorIP,
		targetType, targetID, description, detailsJSON, l.lastHash, entryHash)

	if err != nil {
		return fmt.Errorf("failed to write audit log: %w", err)
	}

	// Update last hash
	id, _ := result.LastInsertId()
	l.lastID = id
	l.lastHash = entryHash

	log.Debugf("Audit: [%s] %s - %s", eventType, severity, description)
	return nil
}

// computeEntryHash computes the SHA-256 hash of an entry.
func computeEntryHash(e Entry) string {
	data := fmt.Sprintf("%d|%s|%s|%d|%s|%s|%s|%s|%s|%s",
		e.Timestamp.Unix(), e.EventType, e.Severity, e.ActorID, e.ActorIP,
		e.TargetType, e.TargetID, e.Description, e.Details, e.PreviousHash)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// VerifyChain verifies the integrity of the audit log chain.
func (l *Logger) VerifyChain() (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	rows, err := l.db.Query(`
		SELECT id, timestamp, event_type, severity, actor_id, actor_ip,
			target_type, target_id, description, details, previous_hash, entry_hash
		FROM audit_log ORDER BY id ASC
	`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	expectedPrevHash := GenesisHash
	var count int

	for rows.Next() {
		var entry Entry
		var timestamp int64
		var actorID sql.NullInt64
		var actorIP, targetType, targetID, details sql.NullString

		err := rows.Scan(&entry.ID, &timestamp, &entry.EventType, &entry.Severity,
			&actorID, &actorIP, &targetType, &targetID, &entry.Description,
			&details, &entry.PreviousHash, &entry.EntryHash)
		if err != nil {
			return false, fmt.Errorf("failed to scan entry: %w", err)
		}

		entry.Timestamp = time.Unix(timestamp, 0)
		if actorID.Valid {
			entry.ActorID = actorID.Int64
		}
		if actorIP.Valid {
			entry.ActorIP = actorIP.String
		}
		if targetType.Valid {
			entry.TargetType = targetType.String
		}
		if targetID.Valid {
			entry.TargetID = targetID.String
		}
		if details.Valid {
			entry.Details = details.String
		}

		// Verify previous hash matches expected
		if entry.PreviousHash != expectedPrevHash {
			log.Errorf("Chain break at entry %d: expected prev hash %s, got %s",
				entry.ID, expectedPrevHash, entry.PreviousHash)
			return false, ErrLogTampered
		}

		// Verify entry hash is correct
		computedHash := computeEntryHash(entry)
		if entry.EntryHash != computedHash {
			log.Errorf("Hash mismatch at entry %d: stored %s, computed %s",
				entry.ID, entry.EntryHash, computedHash)
			return false, ErrLogTampered
		}

		expectedPrevHash = entry.EntryHash
		count++
	}

	log.Infof("Audit chain verified: %d entries, integrity OK", count)
	return true, nil
}

// Query retrieves audit log entries matching criteria.
func (l *Logger) Query(opts QueryOptions) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	query := `
		SELECT id, timestamp, event_type, severity, actor_id, actor_ip,
			target_type, target_id, description, details, previous_hash, entry_hash
		FROM audit_log WHERE 1=1
	`
	var args []interface{}

	if opts.EventType != "" {
		query += " AND event_type = ?"
		args = append(args, opts.EventType)
	}
	if opts.Severity != "" {
		query += " AND severity = ?"
		args = append(args, opts.Severity)
	}
	if opts.ActorID > 0 {
		query += " AND actor_id = ?"
		args = append(args, opts.ActorID)
	}
	if !opts.Since.IsZero() {
		query += " AND timestamp >= ?"
		args = append(args, opts.Since.Unix())
	}
	if !opts.Until.IsZero() {
		query += " AND timestamp <= ?"
		args = append(args, opts.Until.Unix())
	}

	query += " ORDER BY id DESC"

	if opts.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", opts.Limit)
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", opts.Offset)
	}

	rows, err := l.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var entry Entry
		var timestamp int64
		var actorID sql.NullInt64
		var actorIP, targetType, targetID, details sql.NullString

		err := rows.Scan(&entry.ID, &timestamp, &entry.EventType, &entry.Severity,
			&actorID, &actorIP, &targetType, &targetID, &entry.Description,
			&details, &entry.PreviousHash, &entry.EntryHash)
		if err != nil {
			continue
		}

		entry.Timestamp = time.Unix(timestamp, 0)
		if actorID.Valid {
			entry.ActorID = actorID.Int64
		}
		if actorIP.Valid {
			entry.ActorIP = actorIP.String
		}
		if targetType.Valid {
			entry.TargetType = targetType.String
		}
		if targetID.Valid {
			entry.TargetID = targetID.String
		}
		if details.Valid {
			entry.Details = details.String
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// QueryOptions specifies filters for querying the audit log.
type QueryOptions struct {
	EventType string
	Severity  string
	ActorID   int64
	Since     time.Time
	Until     time.Time
	Limit     int
	Offset    int
}

// GetEntry retrieves a single audit log entry by ID.
func (l *Logger) GetEntry(id int64) (*Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var entry Entry
	var timestamp int64
	var actorID sql.NullInt64
	var actorIP, targetType, targetID, details sql.NullString

	err := l.db.QueryRow(`
		SELECT id, timestamp, event_type, severity, actor_id, actor_ip,
			target_type, target_id, description, details, previous_hash, entry_hash
		FROM audit_log WHERE id = ?
	`, id).Scan(&entry.ID, &timestamp, &entry.EventType, &entry.Severity,
		&actorID, &actorIP, &targetType, &targetID, &entry.Description,
		&details, &entry.PreviousHash, &entry.EntryHash)

	if err == sql.ErrNoRows {
		return nil, ErrEntryNotFound
	} else if err != nil {
		return nil, err
	}

	entry.Timestamp = time.Unix(timestamp, 0)
	if actorID.Valid {
		entry.ActorID = actorID.Int64
	}
	if actorIP.Valid {
		entry.ActorIP = actorIP.String
	}
	if targetType.Valid {
		entry.TargetType = targetType.String
	}
	if targetID.Valid {
		entry.TargetID = targetID.String
	}
	if details.Valid {
		entry.Details = details.String
	}

	return &entry, nil
}

// Count returns the total number of audit log entries.
func (l *Logger) Count() (int64, error) {
	var count int64
	err := l.db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	return count, err
}

// LastHash returns the hash of the most recent entry.
func (l *Logger) LastHash() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastHash
}

// Export exports the audit log to JSON format.
func (l *Logger) Export() ([]byte, error) {
	entries, err := l.Query(QueryOptions{Limit: 0}) // All entries
	if err != nil {
		return nil, err
	}

	return json.MarshalIndent(entries, "", "  ")
}

// Close closes the database connection.
func (l *Logger) Close() error {
	return l.db.Close()
}

// Convenience methods for common audit events

// LogAdminLogin logs an admin login event.
func (l *Logger) LogAdminLogin(adminID int64, username, ip string, success bool) error {
	severity := SeverityInfo
	description := fmt.Sprintf("Admin login successful: %s", username)
	if !success {
		severity = SeverityWarning
		description = fmt.Sprintf("Admin login failed: %s", username)
	}
	return l.Log(EventTypeAdminLogin, severity, description, adminID, ip, map[string]interface{}{
		"username": username,
		"success":  success,
	})
}

// LogPasswordChange logs a password change event.
func (l *Logger) LogPasswordChange(adminID int64, ip string) error {
	return l.Log(EventTypePasswordChange, SeverityInfo, "Password changed", adminID, ip, nil)
}

// LogPeerTrustChange logs a peer trust level change.
func (l *Logger) LogPeerTrustChange(adminID int64, ip, peerID string, oldLevel, newLevel int) error {
	return l.LogWithTarget(EventTypePeerTrustChange, SeverityInfo,
		fmt.Sprintf("Peer trust level changed: %d -> %d", oldLevel, newLevel),
		adminID, ip, "peer", peerID,
		map[string]interface{}{"old_level": oldLevel, "new_level": newLevel})
}

// LogConfigChange logs a configuration change.
func (l *Logger) LogConfigChange(adminID int64, ip, configKey string, oldValue, newValue interface{}) error {
	return l.LogWithTarget(EventTypeConfigChange, SeverityInfo,
		fmt.Sprintf("Configuration changed: %s", configKey),
		adminID, ip, "config", configKey,
		map[string]interface{}{"old_value": oldValue, "new_value": newValue})
}

// LogKeyGenerate logs key generation.
func (l *Logger) LogKeyGenerate(adminID int64, ip, keyFingerprint string) error {
	return l.Log(EventTypeKeyGenerate, SeverityInfo,
		fmt.Sprintf("Server identity keys generated: %s", keyFingerprint),
		adminID, ip, map[string]interface{}{"fingerprint": keyFingerprint})
}

// LogKeyBackup logs key backup creation.
func (l *Logger) LogKeyBackup(adminID int64, ip string) error {
	return l.Log(EventTypeKeyBackup, SeverityInfo, "Key backup created", adminID, ip, nil)
}

// LogKeyRestore logs key restoration.
func (l *Logger) LogKeyRestore(adminID int64, ip, keyFingerprint string) error {
	return l.Log(EventTypeKeyRestore, SeverityWarning,
		fmt.Sprintf("Keys restored from backup: %s", keyFingerprint),
		adminID, ip, map[string]interface{}{"fingerprint": keyFingerprint})
}

// LogSetupStart logs setup mode initiation.
func (l *Logger) LogSetupStart(ip string) error {
	return l.Log(EventTypeSetupStart, SeverityInfo, "First-time setup started", 0, ip, nil)
}

// LogSetupComplete logs setup completion.
func (l *Logger) LogSetupComplete(adminID int64, ip, serverFingerprint string) error {
	return l.Log(EventTypeSetupComplete, SeverityInfo,
		fmt.Sprintf("Setup completed, server identity: %s", serverFingerprint),
		adminID, ip, map[string]interface{}{"fingerprint": serverFingerprint})
}

// LogServerStart logs server startup.
func (l *Logger) LogServerStart(fingerprint string) error {
	return l.Log(EventTypeServerStart, SeverityInfo,
		fmt.Sprintf("Server started, identity: %s", fingerprint), 0, "",
		map[string]interface{}{"fingerprint": fingerprint})
}

// LogServerStop logs server shutdown.
func (l *Logger) LogServerStop() error {
	return l.Log(EventTypeServerStop, SeverityInfo, "Server stopped gracefully", 0, "", nil)
}
