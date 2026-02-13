// Package auth provides HD wallet authentication and session management for SDN servers.
package auth

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	_ "github.com/mattn/go-sqlite3"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

var log = logging.Logger("sdn-auth")

// User represents an authenticated user mapped by xpub.
type User struct {
	XPub       string          `json:"xpub"`
	Name       string          `json:"name,omitempty"`
	TrustLevel peers.TrustLevel `json:"trust_level"`
	Source     string          `json:"source"` // "config" or "database"
	CreatedAt  time.Time       `json:"created_at"`
	LastLogin  *time.Time      `json:"last_login,omitempty"`
}

// UserStore manages xpub-to-trust-level mappings from config and database.
// Database entries take precedence over config entries for the same xpub.
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
		trust, err := peers.ParseTrustLevel(entry.TrustLevel)
		if err != nil {
			log.Warnf("Skipping config user %q: invalid trust level %q", entry.Name, entry.TrustLevel)
			continue
		}
		s.configUsers[entry.XPub] = User{
			XPub:       entry.XPub,
			Name:       entry.Name,
			TrustLevel: trust,
			Source:     "config",
			CreatedAt:  now,
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
			created_at INTEGER NOT NULL,
			last_login_at INTEGER
		)
	`)
	return err
}

// GetUser retrieves a user by xpub. Database entries take precedence over config.
func (s *UserStore) GetUser(xpub string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check database first
	var u User
	var createdAt int64
	var lastLogin sql.NullInt64
	err := s.db.QueryRow(
		"SELECT xpub, name, trust_level, created_at, last_login_at FROM users WHERE xpub = ?",
		xpub,
	).Scan(&u.XPub, &u.Name, &u.TrustLevel, &createdAt, &lastLogin)

	if err == nil {
		u.Source = "database"
		u.CreatedAt = time.Unix(createdAt, 0)
		if lastLogin.Valid {
			t := time.Unix(lastLogin.Int64, 0)
			u.LastLogin = &t
		}
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

// ListUsers returns all users from both config and database, with database taking precedence.
func (s *UserStore) ListUsers() ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]bool)
	var users []User

	// Database users first (higher precedence)
	rows, err := s.db.Query("SELECT xpub, name, trust_level, created_at, last_login_at FROM users ORDER BY created_at")
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var u User
		var createdAt int64
		var lastLogin sql.NullInt64
		if err := rows.Scan(&u.XPub, &u.Name, &u.TrustLevel, &createdAt, &lastLogin); err != nil {
			continue
		}
		u.Source = "database"
		u.CreatedAt = time.Unix(createdAt, 0)
		if lastLogin.Valid {
			t := time.Unix(lastLogin.Int64, 0)
			u.LastLogin = &t
		}
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

// HasAdmin returns true if at least one user with Admin trust exists.
func (s *UserStore) HasAdmin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, u := range s.configUsers {
		if u.TrustLevel >= peers.Admin {
			return true
		}
	}

	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM users WHERE trust_level >= ?", int(peers.Admin)).Scan(&count)
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
func (s *UserStore) AddUser(xpub, name string, trust peers.TrustLevel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		"INSERT INTO users (xpub, name, trust_level, created_at) VALUES (?, ?, ?, ?)",
		xpub, name, int(trust), time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to add user: %w", err)
	}

	log.Infof("Added user %q (trust=%s) to database", name, trust)
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

	log.Infof("Removed user with xpub %s...%s from database", xpub[:8], xpub[len(xpub)-4:])
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
				"INSERT INTO users (xpub, name, trust_level, created_at) VALUES (?, ?, ?, ?)",
				xpub, cu.Name, int(trust), time.Now().Unix(),
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
				"INSERT INTO users (xpub, name, trust_level, created_at, last_login_at) VALUES (?, ?, ?, ?, ?)",
				xpub, cu.Name, int(cu.TrustLevel), now, now,
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
