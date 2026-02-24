// Package auth provides HD wallet authentication and session management for SDN servers.
package auth

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	_ "github.com/mattn/go-sqlite3" // SQLite driver

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

var log = logging.Logger("sdn-auth")

// User represents an authenticated user mapped by xpub.
type User struct {
	XPub             string           `json:"xpub"`
	Name             string           `json:"name,omitempty"`
	TrustLevel       peers.TrustLevel `json:"trust_level"`
	SigningPubKeyHex string           `json:"signing_pubkey_hex,omitempty"`
	Source           string           `json:"source"` // "config" or "database"
	CreatedAt        time.Time        `json:"created_at"`
	LastLogin        *time.Time       `json:"last_login,omitempty"`
}

// UserStore manages xpub-to-trust-level mappings from config and database.
// Config values are always applied as authoritative overrides when a user exists
// in both places, so operator config changes are reflected immediately.
type UserStore struct {
	db          *sql.DB
	configUsers map[string]User
	mu          sync.RWMutex
}

// NewUserStore creates a user store backed by SQLite and config-defined users.
func NewUserStore(dbPath string, configEntries []config.UserEntry) (*UserStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create user store directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open user store database: %w", err)
	}

	s := &UserStore{
		db:          db,
		configUsers: make(map[string]User),
	}

	if err := s.initDB(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize user store: %w", err)
	}

	// Load config users into memory
	now := time.Now()
	for _, entry := range configEntries {
		xpub := strings.TrimSpace(entry.XPub)
		if xpub == "" {
			continue
		}
		trust, err := peers.ParseTrustLevel(strings.TrimSpace(strings.ToLower(entry.TrustLevel)))
		if err != nil {
			log.Warnf("Skipping config user %q: invalid trust level %q", xpub, entry.TrustLevel)
			continue
		}

		signingHex := ""
		if explicit := strings.TrimSpace(entry.SigningPubKeyHex); explicit != "" {
			// Explicit signing_pubkey_hex provided — use it.
			normalized, err := normalizeEd25519PubKeyHex(explicit)
			if err != nil {
				log.Warnf("Config user %q: invalid signing_pubkey_hex: %v", entry.Name, err)
			} else {
				signingHex = normalized
			}
		}

		// No signing key yet — it will be bound on first wallet login (TOFU).
		if signingHex == "" {
			log.Infof("Config user %q: signing key will be bound on first login (TOFU)", entry.Name)
		}

		s.configUsers[entry.XPub] = User{
			XPub:             xpub,
			Name:             entry.Name,
			TrustLevel:       trust,
			SigningPubKeyHex: signingHex,
			Source:           "config",
			CreatedAt:        now,
		}
	}

	log.Infof("User store initialized: %d config users, database at %s", len(s.configUsers), dbPath)
	return s, nil
}

func (s *UserStore) initDB() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			xpub TEXT PRIMARY KEY,
			name TEXT DEFAULT '',
			trust_level INTEGER NOT NULL DEFAULT 2,
			signing_pubkey_hex TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			last_login_at INTEGER
		)
	`)
	if err != nil {
		return err
	}

	// Migrate older databases (pre signing_pubkey_hex).
	_, err = s.db.Exec(`ALTER TABLE users ADD COLUMN signing_pubkey_hex TEXT DEFAULT ''`)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return err
	}
	return nil
}

// GetUser retrieves a user by xpub, applying config-defined trust and key values
// as overrides when the user also exists in the database.
func (s *UserStore) GetUser(xpub string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check database first
	var u User
	var createdAt int64
	var lastLogin sql.NullInt64
	err := s.db.QueryRow(
		"SELECT xpub, name, trust_level, signing_pubkey_hex, created_at, last_login_at FROM users WHERE xpub = ?",
		xpub,
	).Scan(&u.XPub, &u.Name, &u.TrustLevel, &u.SigningPubKeyHex, &createdAt, &lastLogin)

	if err == nil {
		u.Source = "database"
		u.CreatedAt = time.Unix(createdAt, 0)
		if lastLogin.Valid {
			t := time.Unix(lastLogin.Int64, 0)
			u.LastLogin = &t
		}
		// Keep runtime state in DB, but apply configured values as the source of
		// truth for trust and signing key checks.
		s.applyConfigOverrides(&u)
		return &u, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("database error: %w", err)
	}

	// Fall back to config users
	if cu, ok := s.configUsers[xpub]; ok {
		return &cu, nil
	}

	return nil, nil
}

// ListUsers returns all users from both config and database.
func (s *UserStore) ListUsers() ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]bool)
	var users []User

	// Database users first (higher precedence)
	rows, err := s.db.Query("SELECT xpub, name, trust_level, signing_pubkey_hex, created_at, last_login_at FROM users ORDER BY created_at")
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var u User
		var createdAt int64
		var lastLogin sql.NullInt64
		if err := rows.Scan(&u.XPub, &u.Name, &u.TrustLevel, &u.SigningPubKeyHex, &createdAt, &lastLogin); err != nil {
			continue
		}
		u.Source = "database"
		u.CreatedAt = time.Unix(createdAt, 0)
		if lastLogin.Valid {
			t := time.Unix(lastLogin.Int64, 0)
			u.LastLogin = &t
		}
		s.applyConfigOverrides(&u)
		users = append(users, u)
		seen[u.XPub] = true
	}

	// Config users (only those not overridden by database)
	for _, cu := range s.configUsers {
		if !seen[cu.XPub] {
			users = append(users, cu)
		}
	}

	return users, nil
}

// HasAdmin returns true if at least one user with Admin trust level exists.
func (s *UserStore) HasAdmin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// An admin is "configured" if there is at least one user with Admin trust,
	// even if the signing key is not yet bound. The signing key will be bound
	// on first successful wallet login (TOFU — Trust On First Use).
	for _, u := range s.configUsers {
		if u.TrustLevel >= peers.Admin {
			return true
		}
	}

	var count int
	_ = s.db.QueryRow(
		"SELECT COUNT(*) FROM users WHERE trust_level >= ?",
		int(peers.Admin),
	).Scan(&count)
	return count > 0
}

// UserCount returns the total number of configured users.
func (s *UserStore) UserCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var dbCount int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&dbCount)
	return dbCount + len(s.configUsers)
}

// AddUser adds a user to the database.
func (s *UserStore) AddUser(xpub, name string, trust peers.TrustLevel, signingPubKeyHex string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	signingHex, err := normalizeEd25519PubKeyHex(signingPubKeyHex)
	if err != nil {
		return fmt.Errorf("invalid signing_pubkey_hex: %w", err)
	}

	// Signing key is optional at creation — it gets bound on first login (TOFU).
	// If provided, it's already validated above.

	_, err = s.db.Exec(
		"INSERT INTO users (xpub, name, trust_level, signing_pubkey_hex, created_at) VALUES (?, ?, ?, ?, ?)",
		xpub, name, int(trust), signingHex, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to add user: %w", err)
	}

	log.Infof("Added user %q (trust=%s) to database", name, trust)
	return nil
}

func (s *UserStore) applyConfigOverrides(u *User) {
	if u == nil {
		return
	}

	cu, ok := s.configUsers[u.XPub]
	if !ok {
		return
	}

	// Config wins on trusted metadata to avoid stale DB rows blocking updated config.
	u.Source = "database"
	if strings.TrimSpace(cu.Name) != "" {
		u.Name = cu.Name
	}
	u.TrustLevel = cu.TrustLevel
	if strings.TrimSpace(cu.SigningPubKeyHex) != "" {
		u.SigningPubKeyHex = cu.SigningPubKeyHex
	}
}

// UpdateSigningPubKey sets/overrides the signing public key for a user.
// For config users, this creates a database row to override the config value.
func (s *UserStore) UpdateSigningPubKey(xpub, signingPubKeyHex string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	signingHex, err := normalizeEd25519PubKeyHex(signingPubKeyHex)
	if err != nil {
		return fmt.Errorf("invalid signing_pubkey_hex: %w", err)
	}
	if signingHex == "" {
		return fmt.Errorf("signing_pubkey_hex is required")
	}

	result, err := s.db.Exec("UPDATE users SET signing_pubkey_hex = ? WHERE xpub = ?", signingHex, xpub)
	if err != nil {
		return fmt.Errorf("failed to update signing key: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		// If it's a config user, promote to database to override
		if cu, ok := s.configUsers[xpub]; ok {
			_, err := s.db.Exec(
				"INSERT INTO users (xpub, name, trust_level, signing_pubkey_hex, created_at) VALUES (?, ?, ?, ?, ?)",
				xpub, cu.Name, int(cu.TrustLevel), signingHex, time.Now().Unix(),
			)
			if err != nil {
				return fmt.Errorf("failed to override config user signing key: %w", err)
			}
			return nil
		}
		return fmt.Errorf("user not found")
	}

	return nil
}

// RemoveUser removes a user from the database. Config users cannot be removed.
func (s *UserStore) RemoveUser(xpub string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec("DELETE FROM users WHERE xpub = ?", xpub)
	if err != nil {
		return fmt.Errorf("failed to remove user: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("user not found in database (config users cannot be removed)")
	}

	if len(xpub) >= 12 {
		log.Infof("Removed user with xpub %s...%s from database", xpub[:8], xpub[len(xpub)-4:])
	} else {
		log.Infof("Removed user from database")
	}
	return nil
}

// UpdateTrust updates the trust level for a database user.
func (s *UserStore) UpdateTrust(xpub string, trust peers.TrustLevel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec("UPDATE users SET trust_level = ? WHERE xpub = ?", int(trust), xpub)
	if err != nil {
		return fmt.Errorf("failed to update trust: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		// If it's a config user, promote to database to override
		if _, ok := s.configUsers[xpub]; ok {
			cu := s.configUsers[xpub]
			_, err := s.db.Exec(
				"INSERT INTO users (xpub, name, trust_level, signing_pubkey_hex, created_at) VALUES (?, ?, ?, ?, ?)",
				xpub, cu.Name, int(trust), cu.SigningPubKeyHex, time.Now().Unix(),
			)
			if err != nil {
				return fmt.Errorf("failed to override config user trust: %w", err)
			}
			return nil
		}
		return fmt.Errorf("user not found")
	}

	return nil
}

// RecordLogin updates the last login timestamp for a user.
func (s *UserStore) RecordLogin(xpub string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()

	// Try to update existing database user
	result, err := s.db.Exec("UPDATE users SET last_login_at = ? WHERE xpub = ?", now, xpub)
	if err != nil {
		return err
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		// Config user — create a database entry to track login
		if cu, ok := s.configUsers[xpub]; ok {
			_, err := s.db.Exec(
				"INSERT INTO users (xpub, name, trust_level, signing_pubkey_hex, created_at, last_login_at) VALUES (?, ?, ?, ?, ?, ?)",
				xpub, cu.Name, int(cu.TrustLevel), cu.SigningPubKeyHex, now, now,
			)
			return err
		}
	}

	return nil
}

// Close closes the database connection.
func (s *UserStore) Close() error {
	return s.db.Close()
}

func normalizeEd25519PubKeyHex(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	if strings.HasPrefix(s, "0x") {
		s = s[2:]
	}
	if s == "" {
		return "", nil
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return "", err
	}
	if len(raw) != ed25519.PublicKeySize {
		return "", fmt.Errorf("expected 32-byte Ed25519 public key hex, got %d bytes", len(raw))
	}
	return s, nil
}
