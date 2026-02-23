// Package admin provides admin authentication and session management for SDN servers.
//
// # Overview
//
// The admin package implements secure authentication for server administrators.
// It provides password-based authentication with optional TOTP two-factor
// authentication, and secure session management.
//
// # Authentication
//
// Password Security:
//   - Passwords are hashed using Argon2id, the recommended password hashing algorithm
//   - Each password uses a unique 32-byte random salt
//   - Parameters: time=3, memory=64MB, threads=4, keyLen=32
//
// Two-Factor Authentication:
//   - Optional TOTP (Time-based One-Time Password) support
//   - Compatible with authenticator apps like Google Authenticator
//   - WebAuthn/Passkey support planned for future versions
//
// # Session Management
//
// Sessions are managed with the following security measures:
//   - Session tokens are 32-byte cryptographically random values
//   - Tokens are stored with associated IP address and user agent
//   - Session expiry: 24 hours (with "remember me") or 1 hour (without)
//   - All sessions are revoked on password change
//   - Individual session revocation supported
//
// # Database Schema
//
// The admin package uses SQLite with WAL mode for storage:
//   - admins: id, username, password_hash, password_salt, totp_secret, etc.
//   - sessions: token, admin_id, created_at, expires_at, ip_address, etc.
//
// # Usage
//
// Create admin manager:
//
//	mgr, _ := admin.NewManager("/path/to/data")
//
// Create admin account:
//
//	mgr.CreateAdmin("admin", "secure_password")
//
// Authenticate:
//
//	token, err := mgr.Authenticate("admin", "password", "127.0.0.1", "agent", true)
//
// Validate session:
//
//	session, err := mgr.ValidateSession(token)
//
// Change password (revokes all sessions):
//
//	mgr.ChangePassword(adminID, "old_password", "new_password")
package admin
