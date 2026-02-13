package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const sessionTokenLength = 32

// Session represents an active authenticated session.
type Session struct {
	Token      string           `json:"-"`
	XPub       string           `json:"xpub"`
	TrustLevel peers.TrustLevel `json:"trust_level"`
	CreatedAt  time.Time        `json:"created_at"`
	ExpiresAt  time.Time        `json:"expires_at"`
	IPAddress  string           `json:"ip_address,omitempty"`
	UserAgent  string           `json:"user_agent,omitempty"`
}

// SessionStore manages authentication sessions in SQLite.
type SessionStore struct {
	db *sql.DB
}

// NewSessionStore creates a session store using the provided database connection.
func NewSessionStore(db *sql.DB) (*SessionStore, error) {
	ss := &SessionStore{db: db}
	if err := ss.initDB(); err != nil {
		return nil, fmt.Errorf("failed to initialize session store: %w", err)
	}
	return ss, nil
}

func (ss *SessionStore) initDB() error {
	_, err := ss.db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			xpub TEXT NOT NULL,
			trust_level INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			ip_address TEXT,
			user_agent TEXT,
			revoked INTEGER DEFAULT 0
		)
	`)
	if err != nil {
		return err
	}
	_, err = ss.db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_xpub ON sessions(xpub)`)
	return err
}

// CreateSession generates a new session token and stores it.
func (ss *SessionStore) CreateSession(xpub string, trust peers.TrustLevel, ip, ua string, ttl time.Duration) (string, error) {
	tokenBytes := make([]byte, sessionTokenLength)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate session token: %w", err)
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes)

	now := time.Now()
	expiresAt := now.Add(ttl)

	_, err := ss.db.Exec(
		"INSERT INTO sessions (token, xpub, trust_level, created_at, expires_at, ip_address, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)",
		token, xpub, int(trust), now.Unix(), expiresAt.Unix(), ip, ua,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	return token, nil
}

// ValidateSession checks if a session token is valid and not expired.
func (ss *SessionStore) ValidateSession(token string) (*Session, error) {
	var s Session
	var trust int
	var createdAt, expiresAt int64
	var revoked int

	err := ss.db.QueryRow(
		"SELECT token, xpub, trust_level, created_at, expires_at, ip_address, user_agent, revoked FROM sessions WHERE token = ?",
		token,
	).Scan(&s.Token, &s.XPub, &trust, &createdAt, &expiresAt, &s.IPAddress, &s.UserAgent, &revoked)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found")
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	if revoked != 0 {
		return nil, fmt.Errorf("session revoked")
	}

	s.TrustLevel = peers.TrustLevel(trust)
	s.CreatedAt = time.Unix(createdAt, 0)
	s.ExpiresAt = time.Unix(expiresAt, 0)

	if time.Now().After(s.ExpiresAt) {
		return nil, fmt.Errorf("session expired")
	}

	return &s, nil
}

// RevokeSession invalidates a session token.
func (ss *SessionStore) RevokeSession(token string) error {
	_, err := ss.db.Exec("UPDATE sessions SET revoked = 1 WHERE token = ?", token)
	return err
}

// RevokeAllForUser invalidates all sessions for an xpub.
func (ss *SessionStore) RevokeAllForUser(xpub string) error {
	_, err := ss.db.Exec("UPDATE sessions SET revoked = 1 WHERE xpub = ?", xpub)
	return err
}

// Cleanup removes expired and revoked sessions.
func (ss *SessionStore) Cleanup() (int64, error) {
	result, err := ss.db.Exec(
		"DELETE FROM sessions WHERE revoked = 1 OR expires_at < ?",
		time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
