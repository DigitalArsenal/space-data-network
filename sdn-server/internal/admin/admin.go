// Package admin provides admin authentication and session management for SDN servers.
package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	logging "github.com/ipfs/go-log/v2"
	"golang.org/x/crypto/argon2"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

var log = logging.Logger("sdn-admin")

// Errors
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrSessionExpired     = errors.New("session expired")
	ErrSessionNotFound    = errors.New("session not found")
	ErrAdminExists        = errors.New("admin account already exists")
	ErrAdminNotFound      = errors.New("admin account not found")
	ErrTOTPRequired       = errors.New("TOTP verification required")
	ErrTOTPInvalid        = errors.New("invalid TOTP code")
	ErrWeakPassword       = errors.New("password must be at least 12 characters with uppercase, lowercase, and digit")
	ErrInvalidXPub        = errors.New("invalid xpub")
	ErrXPubInUse          = errors.New("xpub is already bound to another admin account")
)

const (
	// Session settings
	SessionTokenLength = 32
	SessionExpiry      = 24 * time.Hour
	ShortSessionExpiry = 1 * time.Hour // When "remember me" is not checked

	// Argon2 password hashing parameters
	argon2Time    = 3
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32
	saltLength    = 32

	// Database file
	AdminDBFile = "admin.db"
)

// Admin represents an admin account.
//
// Admin is an AUTHENTICATION METHOD (username/password + optional TOTP), not
// a second identity space. Task F1 (single hd-wallet identity, xpub-keyed,
// everywhere) makes internal/auth.UserStore — keyed by hd-wallet xpub — the
// one identity AUTHORITY. XPub is the bridge: once set, it binds this admin
// account to the xpub-keyed User record that carries the actual trust level.
// See BindXPub and GetAdminByXPub.
type Admin struct {
	ID           int64
	Username     string
	PasswordHash string
	PasswordSalt string
	TOTPSecret   string // Base32 encoded TOTP secret
	TOTPEnabled  bool
	WebAuthnCred []byte // WebAuthn credential
	XPub         string // bridge to the xpub-keyed identity in internal/auth.UserStore; empty until bound
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

// Session represents an active admin session.
type Session struct {
	Token     string
	AdminID   int64
	CreatedAt time.Time
	ExpiresAt time.Time
	IPAddress string
	UserAgent string
	Revoked   bool
}

// Manager handles admin authentication and sessions.
type Manager struct {
	db     *sql.DB
	closer func() error
	dbPath string
	mu     sync.RWMutex
}

// NewManager creates a new admin manager backed by a private SQLite database.
func NewManager(basePath string) (*Manager, error) {
	if err := os.MkdirAll(basePath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create admin directory: %w", err)
	}

	dbPath := filepath.Join(basePath, "admin.db")
	db, closer, err := flatsqldrv.OpenStandalone(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open admin database: %w", err)
	}

	m := &Manager{
		db:     db,
		closer: closer,
		dbPath: dbPath,
	}

	if err := m.initDB(); err != nil {
		closer()
		return nil, fmt.Errorf("failed to initialize admin database: %w", err)
	}

	return m, nil
}

// initDB creates the necessary tables.
func (m *Manager) initDB() error {
	// Create admins table
	_, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS admins (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			password_salt TEXT NOT NULL,
			totp_secret TEXT,
			totp_enabled INTEGER DEFAULT 0,
			webauthn_cred BLOB,
			xpub TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			last_login_at INTEGER
		)
	`)
	if err != nil {
		return err
	}

	// Migration: add the xpub identity-bridge column for admin databases
	// created before Task F1 (single hd-wallet identity, xpub-keyed,
	// everywhere). Error ignored: it only fails once the column already
	// exists, which is the steady state after the first run of this code.
	_, _ = m.db.Exec(`ALTER TABLE admins ADD COLUMN xpub TEXT DEFAULT ''`)

	// Create sessions table
	_, err = m.db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			admin_id INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			ip_address TEXT,
			user_agent TEXT,
			revoked INTEGER DEFAULT 0,
			FOREIGN KEY (admin_id) REFERENCES admins(id)
		)
	`)
	if err != nil {
		return err
	}

	// Create index on sessions
	_, err = m.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sessions_admin ON sessions(admin_id)
	`)

	return err
}

// HasAdmin returns true if an admin account exists.
func (m *Manager) HasAdmin() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var count int
	err := m.db.QueryRow("SELECT COUNT(*) FROM admins").Scan(&count)
	return err == nil && count > 0
}

func validatePassword(password string) error {
	if len(password) < 12 {
		return ErrWeakPassword
	}
	var hasUpper, hasLower, hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		return ErrWeakPassword
	}
	return nil
}

// CreateAdmin creates a new admin account.
func (m *Manager) CreateAdmin(username, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if admin already exists
	var exists int
	m.db.QueryRow("SELECT COUNT(*) FROM admins WHERE username = ?", username).Scan(&exists)
	if exists > 0 {
		return ErrAdminExists
	}

	// Generate salt
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("failed to generate salt: %w", err)
	}

	// Hash password with Argon2
	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	now := time.Now().Unix()
	_, err := m.db.Exec(`
		INSERT INTO admins (username, password_hash, password_salt, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, username, hex.EncodeToString(hash), hex.EncodeToString(salt), now, now)

	if err != nil {
		return fmt.Errorf("failed to create admin: %w", err)
	}

	log.Infof("Created admin account: %s", username)
	return nil
}

// Authenticate verifies credentials and returns a session token.
func (m *Manager) Authenticate(username, password, ipAddress, userAgent string, rememberMe bool) (string, error) {
	// Phase 1: Read admin record (read lock only).
	m.mu.RLock()
	var admin Admin
	var createdAt, updatedAt int64
	var lastLoginAt sql.NullInt64

	err := m.db.QueryRow(`
		SELECT id, username, password_hash, password_salt, totp_enabled, created_at, updated_at, last_login_at
		FROM admins WHERE username = ?
	`, username).Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.PasswordSalt,
		&admin.TOTPEnabled, &createdAt, &updatedAt, &lastLoginAt)
	m.mu.RUnlock()

	if err == sql.ErrNoRows {
		// Burn constant time to prevent user-enumeration via timing.
		argon2.IDKey([]byte(password), make([]byte, saltLength), argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
		return "", ErrInvalidCredentials
	} else if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	// Phase 2: Verify password (no lock — expensive Argon2 runs unlocked).
	salt, _ := hex.DecodeString(admin.PasswordSalt)
	expectedHash, _ := hex.DecodeString(admin.PasswordHash)
	providedHash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	if subtle.ConstantTimeCompare(expectedHash, providedHash) != 1 {
		return "", ErrInvalidCredentials
	}

	// Check if TOTP is required
	if admin.TOTPEnabled {
		return "", ErrTOTPRequired
	}

	// Phase 3: Create session (write lock).
	m.mu.Lock()
	token, err := m.createSession(admin.ID, ipAddress, userAgent, rememberMe)
	if err == nil {
		m.db.Exec("UPDATE admins SET last_login_at = ? WHERE id = ?", time.Now().Unix(), admin.ID)
	}
	m.mu.Unlock()

	if err != nil {
		return "", err
	}

	log.Infof("Admin authenticated: %s from %s", username, ipAddress)
	return token, nil
}

// AuthenticateWithTOTP verifies credentials including TOTP.
func (m *Manager) AuthenticateWithTOTP(username, password, totpCode, ipAddress, userAgent string, rememberMe bool) (string, error) {
	// Phase 1: Read admin record (read lock only).
	m.mu.RLock()
	var admin Admin
	var createdAt, updatedAt int64

	err := m.db.QueryRow(`
		SELECT id, username, password_hash, password_salt, totp_secret, totp_enabled, created_at, updated_at
		FROM admins WHERE username = ?
	`, username).Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.PasswordSalt,
		&admin.TOTPSecret, &admin.TOTPEnabled, &createdAt, &updatedAt)
	m.mu.RUnlock()

	if err == sql.ErrNoRows {
		// Burn constant time to prevent user-enumeration via timing.
		argon2.IDKey([]byte(password), make([]byte, saltLength), argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
		return "", ErrInvalidCredentials
	} else if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	// Phase 2: Verify password (no lock — expensive Argon2 runs unlocked).
	salt, _ := hex.DecodeString(admin.PasswordSalt)
	expectedHash, _ := hex.DecodeString(admin.PasswordHash)
	providedHash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	if subtle.ConstantTimeCompare(expectedHash, providedHash) != 1 {
		return "", ErrInvalidCredentials
	}

	// Verify TOTP if enabled
	if admin.TOTPEnabled {
		if IsTOTPCodeUsed(admin.ID, totpCode) {
			return "", ErrTOTPInvalid
		}
		if !verifyTOTP(admin.TOTPSecret, totpCode) {
			return "", ErrTOTPInvalid
		}
		MarkTOTPCodeUsed(admin.ID, totpCode)
	}

	// Phase 3: Create session (write lock).
	m.mu.Lock()
	token, err := m.createSession(admin.ID, ipAddress, userAgent, rememberMe)
	if err == nil {
		m.db.Exec("UPDATE admins SET last_login_at = ? WHERE id = ?", time.Now().Unix(), admin.ID)
	}
	m.mu.Unlock()

	if err != nil {
		return "", err
	}

	log.Infof("Admin authenticated with TOTP: %s from %s", username, ipAddress)
	return token, nil
}

// createSession creates a new session for an admin.
// Caller must hold m.mu write lock.
func (m *Manager) createSession(adminID int64, ipAddress, userAgent string, rememberMe bool) (string, error) {
	// Generate session token
	tokenBytes := make([]byte, SessionTokenLength)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate session token: %w", err)
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes)

	// Determine expiry
	expiry := ShortSessionExpiry
	if rememberMe {
		expiry = SessionExpiry
	}

	now := time.Now()
	expiresAt := now.Add(expiry)

	// Store SHA-256 hash of token to prevent exposure if DB is compromised.
	tokenHash := hashSessionTokenFull(token)
	_, err := m.db.Exec(`
		INSERT INTO sessions (token, admin_id, created_at, expires_at, ip_address, user_agent)
		VALUES (?, ?, ?, ?, ?, ?)
	`, tokenHash, adminID, now.Unix(), expiresAt.Unix(), ipAddress, userAgent)

	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	return token, nil
}

// ValidateSession checks if a session token is valid.
func (m *Manager) ValidateSession(token string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var session Session
	var createdAt, expiresAt int64

	tokenHash := hashSessionTokenFull(token)
	err := m.db.QueryRow(`
		SELECT token, admin_id, created_at, expires_at, ip_address, user_agent, revoked
		FROM sessions WHERE token = ?
	`, tokenHash).Scan(&session.Token, &session.AdminID, &createdAt, &expiresAt,
		&session.IPAddress, &session.UserAgent, &session.Revoked)

	if err == sql.ErrNoRows {
		return nil, ErrSessionNotFound
	} else if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	session.CreatedAt = time.Unix(createdAt, 0)
	session.ExpiresAt = time.Unix(expiresAt, 0)

	if session.Revoked {
		return nil, ErrSessionExpired
	}

	if time.Now().After(session.ExpiresAt) {
		return nil, ErrSessionExpired
	}

	return &session, nil
}

// RevokeSession invalidates a session.
func (m *Manager) RevokeSession(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.db.Exec("UPDATE sessions SET revoked = 1 WHERE token = ?", hashSessionTokenFull(token))
	return err
}

// RevokeAllSessions invalidates all sessions for an admin.
func (m *Manager) RevokeAllSessions(adminID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.db.Exec("UPDATE sessions SET revoked = 1 WHERE admin_id = ?", adminID)
	if err != nil {
		return err
	}

	log.Infof("Revoked all sessions for admin ID %d", adminID)
	return nil
}

// ChangePassword updates the admin password and revokes all sessions.
func (m *Manager) ChangePassword(adminID int64, oldPassword, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Get current password hash
	var currentHash, currentSalt string
	err := m.db.QueryRow("SELECT password_hash, password_salt FROM admins WHERE id = ?", adminID).
		Scan(&currentHash, &currentSalt)
	if err != nil {
		return ErrAdminNotFound
	}

	// Verify old password
	salt, _ := hex.DecodeString(currentSalt)
	expectedHash, _ := hex.DecodeString(currentHash)
	providedHash := argon2.IDKey([]byte(oldPassword), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	if subtle.ConstantTimeCompare(expectedHash, providedHash) != 1 {
		return ErrInvalidCredentials
	}

	// Generate new salt and hash
	newSalt := make([]byte, saltLength)
	if _, err := rand.Read(newSalt); err != nil {
		return fmt.Errorf("failed to generate salt: %w", err)
	}

	newHash := argon2.IDKey([]byte(newPassword), newSalt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	// Update password
	now := time.Now().Unix()
	_, err = m.db.Exec(`
		UPDATE admins SET password_hash = ?, password_salt = ?, updated_at = ?
		WHERE id = ?
	`, hex.EncodeToString(newHash), hex.EncodeToString(newSalt), now, adminID)

	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	// Revoke all sessions
	m.db.Exec("UPDATE sessions SET revoked = 1 WHERE admin_id = ?", adminID)

	log.Infof("Password changed for admin ID %d, all sessions revoked", adminID)
	return nil
}

// EnableTOTP enables TOTP 2FA for an admin.
func (m *Manager) EnableTOTP(adminID int64, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.db.Exec(`
		UPDATE admins SET totp_secret = ?, totp_enabled = 1, updated_at = ?
		WHERE id = ?
	`, secret, time.Now().Unix(), adminID)

	if err != nil {
		return fmt.Errorf("failed to enable TOTP: %w", err)
	}

	log.Infof("TOTP enabled for admin ID %d", adminID)
	return nil
}

// DisableTOTP disables TOTP 2FA for an admin.
func (m *Manager) DisableTOTP(adminID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.db.Exec(`
		UPDATE admins SET totp_secret = NULL, totp_enabled = 0, updated_at = ?
		WHERE id = ?
	`, time.Now().Unix(), adminID)

	if err != nil {
		return fmt.Errorf("failed to disable TOTP: %w", err)
	}

	log.Infof("TOTP disabled for admin ID %d", adminID)
	return nil
}

// GenerateTOTPSecretForUser generates a TOTP secret and provisioning URI for a user.
func GenerateTOTPSecretForUser(username string) (secret, uri string, err error) {
	return GenerateTOTPSetup(username)
}

// verifyTOTP verifies a TOTP code using RFC 6238 time-based one-time password.
func verifyTOTP(secret, code string) bool {
	return ValidateTOTP(secret, code)
}

// CleanupExpiredSessions removes expired sessions from the database.
func (m *Manager) CleanupExpiredSessions() (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result, err := m.db.Exec("DELETE FROM sessions WHERE expires_at < ? OR revoked = 1", time.Now().Unix())
	if err != nil {
		return 0, err
	}

	affected, _ := result.RowsAffected()
	if affected > 0 {
		log.Infof("Cleaned up %d expired sessions", affected)
	}
	return affected, nil
}

// Close releases the manager's private engine database.
func (m *Manager) Close() error {
	return m.closer()
}

// GetAdmin retrieves an admin by ID.
func (m *Manager) GetAdmin(adminID int64) (*Admin, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var admin Admin
	var createdAt, updatedAt int64
	var lastLoginAt sql.NullInt64
	var xpub sql.NullString

	err := m.db.QueryRow(`
		SELECT id, username, totp_enabled, xpub, created_at, updated_at, last_login_at
		FROM admins WHERE id = ?
	`, adminID).Scan(&admin.ID, &admin.Username, &admin.TOTPEnabled, &xpub,
		&createdAt, &updatedAt, &lastLoginAt)

	if err == sql.ErrNoRows {
		return nil, ErrAdminNotFound
	} else if err != nil {
		return nil, err
	}

	if xpub.Valid {
		admin.XPub = xpub.String
	}
	admin.CreatedAt = time.Unix(createdAt, 0)
	admin.UpdatedAt = time.Unix(updatedAt, 0)
	if lastLoginAt.Valid {
		t := time.Unix(lastLoginAt.Int64, 0)
		admin.LastLoginAt = &t
	}

	return &admin, nil
}

// BindXPub binds an admin account to an hd-wallet xpub, making the admin
// resolvable to the xpub-keyed identity in internal/auth.UserStore — the
// single identity authority (Task F1). This does not replace password/TOTP
// authentication; it is the minimal schema addition that lets a caller, once
// an admin has authenticated with their password/TOTP, resolve (or create)
// the corresponding auth.UserStore User by xpub and treat the two as one
// identity. Typical call sequence (wired by the HTTP layer, outside this
// package):
//
//	token, err := adminMgr.Authenticate(username, password, ip, ua, remember)
//	// ... on TOTP-required, AuthenticateWithTOTP instead ...
//	session, _ := adminMgr.ValidateSession(token)
//	adm, _ := adminMgr.GetAdmin(session.AdminID)
//	if adm.XPub == "" {
//	    // first bridge: bind the admin's wallet xpub once, then ensure a
//	    // corresponding Admin-trust User exists in the UserStore.
//	    adminMgr.BindXPub(adm.ID, walletXPub)
//	    userStore.AddUser(walletXPub, adm.Username, peers.Admin, "")
//	}
//	user, _ := userStore.GetUser(adm.XPub) // same identity, resolved by xpub
//
// An xpub may only be bound to one admin account at a time.
func (m *Manager) BindXPub(adminID int64, xpub string) error {
	xpub = strings.TrimSpace(xpub)
	if !auth.IsValidXPub(xpub) {
		return ErrInvalidXPub
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var existingID int64
	err := m.db.QueryRow(`SELECT id FROM admins WHERE xpub = ? AND xpub != ''`, xpub).Scan(&existingID)
	if err == nil && existingID != adminID {
		return ErrXPubInUse
	} else if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("database error: %w", err)
	}

	result, err := m.db.Exec(`UPDATE admins SET xpub = ?, updated_at = ? WHERE id = ?`,
		xpub, time.Now().Unix(), adminID)
	if err != nil {
		return fmt.Errorf("failed to bind xpub: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrAdminNotFound
	}

	log.Infof("Bound admin ID %d to xpub identity", adminID)
	return nil
}

// GetAdminByXPub resolves an admin account by its bound xpub. It returns
// (nil, nil) if no admin is bound to that xpub — mirroring the not-found
// convention used by auth.UserStore.GetUser so callers can treat "no admin
// row" and "no user row" identically when reconciling the two stores.
func (m *Manager) GetAdminByXPub(xpub string) (*Admin, error) {
	xpub = strings.TrimSpace(xpub)
	if xpub == "" {
		return nil, nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var admin Admin
	var createdAt, updatedAt int64
	var lastLoginAt sql.NullInt64

	err := m.db.QueryRow(`
		SELECT id, username, totp_enabled, xpub, created_at, updated_at, last_login_at
		FROM admins WHERE xpub = ? AND xpub != ''
	`, xpub).Scan(&admin.ID, &admin.Username, &admin.TOTPEnabled, &admin.XPub,
		&createdAt, &updatedAt, &lastLoginAt)

	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	admin.CreatedAt = time.Unix(createdAt, 0)
	admin.UpdatedAt = time.Unix(updatedAt, 0)
	if lastLoginAt.Valid {
		t := time.Unix(lastLoginAt.Int64, 0)
		admin.LastLoginAt = &t
	}

	return &admin, nil
}

// ListActiveSessions returns all active sessions for an admin.
func (m *Manager) ListActiveSessions(adminID int64) ([]Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rows, err := m.db.Query(`
		SELECT token, admin_id, created_at, expires_at, ip_address, user_agent, revoked
		FROM sessions
		WHERE admin_id = ? AND revoked = 0 AND expires_at > ?
		ORDER BY created_at DESC
	`, adminID, time.Now().Unix())

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var s Session
		var createdAt, expiresAt int64
		if err := rows.Scan(&s.Token, &s.AdminID, &createdAt, &expiresAt,
			&s.IPAddress, &s.UserAgent, &s.Revoked); err != nil {
			continue
		}
		s.CreatedAt = time.Unix(createdAt, 0)
		s.ExpiresAt = time.Unix(expiresAt, 0)
		sessions = append(sessions, s)
	}

	return sessions, nil
}

// hashSessionTokenFull returns the full SHA-256 hex digest of a session token
// for storage in the database (prevents exposure if DB is compromised).
func hashSessionTokenFull(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// HashToken creates a truncated SHA-256 hash of a session token for logging.
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:8])
}
