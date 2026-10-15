package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

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

	// SignedRequest marks a session that was NOT read from the session store:
	// it was minted for one request by signed-request admission
	// (signed_request.go) and dies with that request.
	//
	// It is a stamp, not a privilege — it grants nothing and is never
	// persisted (json:"-", and this shape is only ever built in memory). It
	// exists so the auth wall can decorate the ADMITTED response with CORS for
	// exactly the mode where that is safe, without having to re-derive the
	// safety argument from the absence of a cookie. HERMES ruling 2026-08-09.
	SignedRequest bool `json:"-"`
}

// SessionStore manages authentication sessions in SQLite.
type SessionStore struct {
	db *sql.DB
	// absoluteLifetime bounds a session from its ORIGINAL sign-in, however
	// often it is rotated (security audit 2026-10-15, SEC-029: a sliding
	// refresh used to extend a session forever). 0 disables the bound.
	absoluteLifetime time.Duration
}

// DefaultSessionAbsoluteLifetime is how long a session may live from its
// sign-in, across every rotation, unless the configured TTL is longer.
const DefaultSessionAbsoluteLifetime = 7 * 24 * time.Hour

// SetAbsoluteLifetime sets the bound measured from the original sign-in.
func (ss *SessionStore) SetAbsoluteLifetime(d time.Duration) {
	if d < 0 {
		d = 0
	}
	ss.absoluteLifetime = d
}

// NewSessionStore creates a session store using the provided database connection.
func NewSessionStore(db *sql.DB) (*SessionStore, error) {
	ss := &SessionStore{db: db, absoluteLifetime: DefaultSessionAbsoluteLifetime}
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
		hashToken(token), xpub, int(trust), now.Unix(), expiresAt.Unix(), ip, ua,
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

	tokenHash := hashToken(token)
	err := ss.db.QueryRow(
		"SELECT token, xpub, trust_level, created_at, expires_at, ip_address, user_agent, revoked FROM sessions WHERE token = ?",
		tokenHash,
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
	if ss.absoluteLifetime > 0 && time.Since(s.CreatedAt) > ss.absoluteLifetime {
		return nil, fmt.Errorf("session expired (sign-in is older than %s)", ss.absoluteLifetime)
	}

	return &s, nil
}

// RotateSession revokes the current session token and replaces it with a new
// opaque token for the same identity with a fresh expiry.
//
// trust is the caller's CURRENT trust level, re-resolved from the user store
// (Handler.reconcileSession); the replacement never carries more than the
// stored session did or more than trust. Copying the stored trust kept a
// removed or demoted admin at Admin for as long as they stayed active
// (SEC-029). The replacement keeps the ORIGINAL sign-in time, so the
// absolute lifetime still counts from the wallet ceremony.
func (ss *SessionStore) RotateSession(token string, ttl time.Duration, trust peers.TrustLevel) (string, *Session, error) {
	current, err := ss.ValidateSession(token)
	if err != nil {
		return "", nil, err
	}
	if trust > current.TrustLevel {
		trust = current.TrustLevel
	}

	newTokenBytes := make([]byte, sessionTokenLength)
	if _, err := rand.Read(newTokenBytes); err != nil {
		return "", nil, fmt.Errorf("failed to generate replacement session token: %w", err)
	}
	newToken := base64.URLEncoding.EncodeToString(newTokenBytes)

	now := time.Now()
	refreshed := &Session{
		Token:      hashToken(newToken),
		XPub:       current.XPub,
		TrustLevel: trust,
		CreatedAt:  current.CreatedAt,
		ExpiresAt:  now.Add(ttl),
		IPAddress:  current.IPAddress,
		UserAgent:  current.UserAgent,
	}

	tx, err := ss.db.Begin()
	if err != nil {
		return "", nil, fmt.Errorf("failed to begin session rotation transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		"INSERT INTO sessions (token, xpub, trust_level, created_at, expires_at, ip_address, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)",
		refreshed.Token,
		refreshed.XPub,
		int(refreshed.TrustLevel),
		refreshed.CreatedAt.Unix(),
		refreshed.ExpiresAt.Unix(),
		refreshed.IPAddress,
		refreshed.UserAgent,
	); err != nil {
		return "", nil, fmt.Errorf("failed to insert replacement session: %w", err)
	}

	if _, err := tx.Exec("UPDATE sessions SET revoked = 1 WHERE token = ?", hashToken(token)); err != nil {
		return "", nil, fmt.Errorf("failed to revoke prior session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("failed to commit session rotation: %w", err)
	}

	return newToken, refreshed, nil
}

// RevokeSession invalidates a session token.
func (ss *SessionStore) RevokeSession(token string) error {
	_, err := ss.db.Exec("UPDATE sessions SET revoked = 1 WHERE token = ?", hashToken(token))
	return err
}

// RevokeAllForUser invalidates all sessions for an xpub.
func (ss *SessionStore) RevokeAllForUser(xpub string) error {
	_, err := ss.db.Exec("UPDATE sessions SET revoked = 1 WHERE xpub = ?", xpub)
	return err
}

// StartCleanup runs Cleanup in a background goroutine every hour.
// The goroutine stops when the provided stop channel is closed.
func (ss *SessionStore) StartCleanup(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ss.Cleanup()
			case <-stop:
				return
			}
		}
	}()
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
