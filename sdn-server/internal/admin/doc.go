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
//   - admins: id, username, password_hash, password_salt, totp_secret, xpub, etc.
//   - sessions: token, admin_id, created_at, expires_at, ip_address, etc.
//
// # Relationship to the hd-wallet identity authority (Task F1)
//
// This package is an AUTHENTICATION METHOD (username/password, optionally
// plus TOTP) — it is not a second identity space. The single identity
// authority is internal/auth.UserStore, which keys users by hd-wallet xpub.
// Admin.XPub is the bridge column/field: once an admin account is bound to
// an xpub (Manager.BindXPub), it resolves to the same User row that
// UserStore.GetUser(xpub) returns, so "authenticated via password+TOTP" and
// "authenticated via wallet challenge-response" land in one trust record
// instead of two divergent ones. See Manager.BindXPub for the intended call
// sequence. Note: this package's current callers (internal/setup,
// internal/server) are not part of the running sdn-server binary — the live
// server (cmd/spacedatanetwork) authenticates exclusively through the
// hd-wallet challenge-response flow in internal/auth. Wiring BindXPub into a
// live HTTP login flow is a follow-up for whoever re-activates (or repurposes)
// this package's callers.
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
