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
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/argon2"
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
type Admin struct {
	ID           int64
	Username     string
	PasswordHash string
	PasswordSalt string
	TOTPSecret   string // Base32 encoded TOTP secret
	TOTPEnabled  bool
	WebAuthnCred []byte // WebAuthn credential
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

// Session represents an active admin session.
type Session struct {
	Token       string
	AdminID     int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
	IPAddress   string
	UserAgent   string
	Revoked     bool
}

// Manager handles admin authentication and sessions.
type Manager struct {
	db     *sql.DB
	dbPath string
	mu     sync.RWMutex
}

// NewManager creates a new admin manager.
func NewManager(basePath string) (*Manager, error) {
	if err := os.MkdirAll(basePath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create admin directory: %w", err)
	}

	dbPath := filepath.Join(basePath, AdminDBFile)
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open admin database: %w", err)
	}

	m := &Manager{
		db:     db,
		dbPath: dbPath,
	}

	if err := m.initDB(); err != nil {
		db.Close()
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
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			last_login_at INTEGER
		)
	`)
	if err != nil {
		return err
	}

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

// CreateAdmin creates a new admin account.
func (m *Manager) CreateAdmin(username, password string) error {
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
	m.mu.Lock()
	defer m.mu.Unlock()

	// Look up admin
	var admin Admin
	var createdAt, updatedAt int64
	var lastLoginAt sql.NullInt64

	err := m.db.QueryRow(`
		SELECT id, username, password_hash, password_salt, totp_enabled, created_at, updated_at, last_login_at
		FROM admins WHERE username = ?
	`, username).Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.PasswordSalt,
		&admin.TOTPEnabled, &createdAt, &updatedAt, &lastLoginAt)

	if err == sql.ErrNoRows {
		return "", ErrInvalidCredentials
	} else if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	// Verify password
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

	// Create session
	token, err := m.createSession(admin.ID, ipAddress, userAgent, rememberMe)
	if err != nil {
		return "", err
	}

	// Update last login time
	m.db.Exec("UPDATE admins SET last_login_at = ? WHERE id = ?", time.Now().Unix(), admin.ID)

	log.Infof("Admin authenticated: %s from %s", username, ipAddress)
	return token, nil
}

// AuthenticateWithTOTP verifies credentials including TOTP.
func (m *Manager) AuthenticateWithTOTP(username, password, totpCode, ipAddress, userAgent string, rememberMe bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Look up admin
	var admin Admin
	var createdAt, updatedAt int64

	err := m.db.QueryRow(`
		SELECT id, username, password_hash, password_salt, totp_secret, totp_enabled, created_at, updated_at
		FROM admins WHERE username = ?
	`, username).Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.PasswordSalt,
		&admin.TOTPSecret, &admin.TOTPEnabled, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return "", ErrInvalidCredentials
	} else if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	// Verify password
	salt, _ := hex.DecodeString(admin.PasswordSalt)
	expectedHash, _ := hex.DecodeString(admin.PasswordHash)
	providedHash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	if subtle.ConstantTimeCompare(expectedHash, providedHash) != 1 {
		return "", ErrInvalidCredentials
	}

	// Verify TOTP if enabled
	if admin.TOTPEnabled {
		if !verifyTOTP(admin.TOTPSecret, totpCode) {
			return "", ErrTOTPInvalid
		}
	}

	// Create session
	token, err := m.createSession(admin.ID, ipAddress, userAgent, rememberMe)
	if err != nil {
		return "", err
	}

	// Update last login time
	m.db.Exec("UPDATE admins SET last_login_at = ? WHERE id = ?", time.Now().Unix(), admin.ID)

	log.Infof("Admin authenticated with TOTP: %s from %s", username, ipAddress)
	return token, nil
}

// createSession creates a new session for an admin.
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

	_, err := m.db.Exec(`
		INSERT INTO sessions (token, admin_id, created_at, expires_at, ip_address, user_agent)
		VALUES (?, ?, ?, ?, ?, ?)
	`, token, adminID, now.Unix(), expiresAt.Unix(), ipAddress, userAgent)

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

	err := m.db.QueryRow(`
		SELECT token, admin_id, created_at, expires_at, ip_address, user_agent, revoked
		FROM sessions WHERE token = ?
	`, token).Scan(&session.Token, &session.AdminID, &createdAt, &expiresAt,
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

	_, err := m.db.Exec("UPDATE sessions SET revoked = 1 WHERE token = ?", token)
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

	result, err := m.db.Exec("DELETE FROM sessions WHERE expires_at < ?", time.Now().Unix())
	if err != nil {
		return 0, err
	}

	affected, _ := result.RowsAffected()
	if affected > 0 {
		log.Infof("Cleaned up %d expired sessions", affected)
	}
	return affected, nil
}

// Close closes the database connection.
func (m *Manager) Close() error {
	return m.db.Close()
}

// GetAdmin retrieves an admin by ID.
func (m *Manager) GetAdmin(adminID int64) (*Admin, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var admin Admin
	var createdAt, updatedAt int64
	var lastLoginAt sql.NullInt64

	err := m.db.QueryRow(`
		SELECT id, username, totp_enabled, created_at, updated_at, last_login_at
		FROM admins WHERE id = ?
	`, adminID).Scan(&admin.ID, &admin.Username, &admin.TOTPEnabled,
		&createdAt, &updatedAt, &lastLoginAt)

	if err == sql.ErrNoRows {
		return nil, ErrAdminNotFound
	} else if err != nil {
		return nil, err
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

// HashToken creates a SHA-256 hash of a session token for logging.
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:8])
}
