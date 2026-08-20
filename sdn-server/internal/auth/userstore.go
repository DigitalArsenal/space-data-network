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

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
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

	// PRR projection fields (owner directive 2026-07-27: operator sign-ins are
	// stored the same way peers are stored in the trust matrix). These mirror
	// internal/peers.TrustedPeer, which is itself a projection of the SDS PRR
	// record — see trust_matrix.go for the full ruling.
	Organization string `json:"organization,omitempty"`
	Groups       string `json:"groups,omitempty"` // comma-separated, as PRR GROUPS
	Notes        string `json:"notes,omitempty"`
	// SignInCount is PRR.CONNECTION_COUNT for an operator: a sign-in is an
	// operator's connection.
	SignInCount int64 `json:"connection_count"`
	// EPMData is PRR.EPM_DATA, VCardData is PRR.VCARD_DATA.
	EPMData   []byte `json:"epm_data,omitempty"`
	VCardData string `json:"vcard_data,omitempty"`
}

// UserStore manages xpub-to-trust-level mappings from config and database.
// Config values are always applied as authoritative overrides when a user exists
// in both places, so operator config changes are reflected immediately.
type UserStore struct {
	db          *sql.DB
	closer      func() error
	configUsers map[string]User
	// linkedEthereum maps a lowercase external EVM address to the config user
	// it is linked to (config.UserEntry.EthereumAddress). Recognition only —
	// nothing in the auth admission path reads this map.
	linkedEthereum map[string]User
	mu             sync.RWMutex
}

// NewUserStore creates a user store backed by a private SQLite database plus
// config-defined users.
func NewUserStore(dbPath string, configEntries []config.UserEntry) (*UserStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create user store directory: %w", err)
	}

	db, closer, err := flatsqldrv.OpenStandalone(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open user store database: %w", err)
	}

	s := &UserStore{
		db:             db,
		closer:         closer,
		configUsers:    make(map[string]User),
		linkedEthereum: make(map[string]User),
	}

	if err := s.initDB(); err != nil {
		closer()
		return nil, fmt.Errorf("failed to initialize user store: %w", err)
	}

	// Load config users into memory
	now := time.Now()
	for _, entry := range configEntries {
		xpub := strings.TrimSpace(entry.XPub)
		linkedAddr := strings.ToLower(strings.TrimSpace(entry.EthereumAddress))
		if xpub == "" && linkedAddr == "" {
			continue
		}
		trust, err := peers.ParseTrustLevel(strings.TrimSpace(strings.ToLower(entry.TrustLevel)))
		if err != nil {
			log.Warnf("Skipping config user %q: invalid trust level %q", entry.Name, entry.TrustLevel)
			continue
		}

		// A linked-only entry (external EVM address, no xpub) enrolls nothing
		// into the sign-in lane — it exists purely so a connected wallet can
		// be RECOGNIZED by name and trust label.
		if xpub == "" {
			if !isEVMAddress(linkedAddr) {
				log.Warnf("Config user %q: ethereum_address %q is not a 0x + 40-hex address; ignored", entry.Name, entry.EthereumAddress)
				continue
			}
			s.linkedEthereum[linkedAddr] = User{
				Name:       entry.Name,
				TrustLevel: trust,
				Source:     "config",
				CreatedAt:  now,
			}
			log.Infof("Config entry %q: external EVM address linked for recognition (%s…%s)", entry.Name, linkedAddr[:6], linkedAddr[len(linkedAddr)-4:])
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

		user := User{
			XPub:             xpub,
			Name:             entry.Name,
			TrustLevel:       trust,
			SigningPubKeyHex: signingHex,
			Source:           "config",
			CreatedAt:        now,
		}
		s.configUsers[entry.XPub] = user

		if linkedAddr != "" {
			if !isEVMAddress(linkedAddr) {
				log.Warnf("Config user %q: ethereum_address %q is not a 0x + 40-hex address; ignored", entry.Name, entry.EthereumAddress)
			} else {
				s.linkedEthereum[linkedAddr] = user
				log.Infof("Config user %q: external EVM address linked for recognition (%s…%s)", entry.Name, linkedAddr[:6], linkedAddr[len(linkedAddr)-4:])
			}
		}
	}

	log.Infof("User store initialized: %d config users, database at %s", len(s.configUsers), dbPath)
	return s, nil
}

// isEVMAddress reports whether address is a lowercase 0x + 40-hex string.
func isEVMAddress(address string) bool {
	if len(address) != 42 || address[0] != '0' || address[1] != 'x' {
		return false
	}
	for _, c := range address[2:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// LinkedExternalAccount reports the enrolled account an external EVM address
// is linked to, if any (config.UserEntry.EthereumAddress). Recognition only.
func (s *UserStore) LinkedExternalAccount(address string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.linkedEthereum[strings.ToLower(strings.TrimSpace(address))]
	return user, ok
}

func (s *UserStore) initDB() error {
	// The operator table is the PRR projection (schema/PRR/main.fbs) in this
	// store's OWN SQLite file — Themis rules the shape, the owner rules the
	// location. Column names are the snake_case projection internal/peers
	// already uses for the same record; PRR's IDL capitalization applies to
	// serialized records, not to local columns.
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			xpub TEXT PRIMARY KEY,
			peer_id TEXT DEFAULT '',
			name TEXT DEFAULT '',
			organization TEXT DEFAULT '',
			trust_level INTEGER NOT NULL DEFAULT 2,
			groups TEXT DEFAULT '',
			notes TEXT DEFAULT '',
			signing_pubkey_hex TEXT DEFAULT '',
			epm_data BLOB,
			vcard_data TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			last_login_at INTEGER,
			connection_count INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return err
	}
	// Columns added when operator entries became trust-matrix entries. Existing
	// auth.db files predate them, and this store opens real files in place, so
	// additive ALTERs are required — unlike the standards store, whose engine
	// databases are always created fresh. A duplicate-column error means the
	// migration already ran.
	for _, stmt := range []string{
		`ALTER TABLE users ADD COLUMN peer_id TEXT DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN organization TEXT DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN groups TEXT DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN notes TEXT DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN epm_data BLOB`,
		`ALTER TABLE users ADD COLUMN vcard_data TEXT DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN connection_count INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, aerr := s.db.Exec(stmt); aerr != nil &&
			!strings.Contains(strings.ToLower(aerr.Error()), "duplicate column") {
			log.Debugf("user store migration %q: %v", stmt, aerr)
		}
	}
	// No ALTER TABLE migration needed: engine databases are always created
	// with the current schema (legacy sqlite files are imported by the
	// dedicated migration CLI, not opened here).
	return err
}

// DB exposes the underlying engine-backed database so co-located auth state
// (e.g. the session store) can share the same private engine.
func (s *UserStore) DB() *sql.DB {
	return s.db
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
	var org, groups, notes, vcard sql.NullString
	var epmData []byte
	var signInCount sql.NullInt64
	err := s.db.QueryRow(
		`SELECT xpub, name, organization, trust_level, groups, notes,
		        signing_pubkey_hex, epm_data, vcard_data, created_at,
		        last_login_at, connection_count
		 FROM users WHERE xpub = ?`,
		xpub,
	).Scan(&u.XPub, &u.Name, &org, &u.TrustLevel, &groups, &notes,
		&u.SigningPubKeyHex, &epmData, &vcard, &createdAt, &lastLogin, &signInCount)

	if err == nil {
		u.Source = "database"
		u.Organization = org.String
		u.Groups = groups.String
		u.Notes = notes.String
		u.VCardData = vcard.String
		u.EPMData = epmData
		u.SignInCount = signInCount.Int64
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

// GetUserBySigningPubKey resolves a user by a bound Ed25519 signing public key.
// Config overrides are applied so the returned user matches the authoritative
// runtime view used by auth/session handling.
func (s *UserStore) GetUserBySigningPubKey(signingPubKeyHex string) (*User, error) {
	normalized, err := normalizeEd25519PubKeyHex(signingPubKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid signing_pubkey_hex: %w", err)
	}
	if normalized == "" {
		return nil, nil
	}

	users, err := s.ListUsers()
	if err != nil {
		return nil, err
	}

	for i := range users {
		if strings.EqualFold(strings.TrimSpace(users[i].SigningPubKeyHex), normalized) {
			u := users[i]
			return &u, nil
		}
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
	rows, err := s.db.Query(`SELECT xpub, name, organization, trust_level, groups, notes,
	                                signing_pubkey_hex, epm_data, vcard_data, created_at,
	                                last_login_at, connection_count
	                         FROM users ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var u User
		var createdAt int64
		var lastLogin sql.NullInt64
		var org, groups, notes, vcard sql.NullString
		var epmData []byte
		var signInCount sql.NullInt64
		if err := rows.Scan(&u.XPub, &u.Name, &org, &u.TrustLevel, &groups, &notes,
			&u.SigningPubKeyHex, &epmData, &vcard, &createdAt, &lastLogin, &signInCount); err != nil {
			continue
		}
		u.Source = "database"
		u.Organization = org.String
		u.Groups = groups.String
		u.Notes = notes.String
		u.VCardData = vcard.String
		u.EPMData = epmData
		u.SignInCount = signInCount.Int64
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

	// PRR.PEER_ID for the new operator, derived from the account xpub's
	// secp256k1 key (Themis binding). Empty when the identifier is not a
	// parseable xpub — a fabricated peer ID would collide with a real
	// identity's key space.
	peerID, _ := OperatorPeerID(xpub)
	_, err = s.db.Exec(
		`INSERT INTO users (xpub, peer_id, name, trust_level, signing_pubkey_hex, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		xpub, peerID, name, int(trust), signingHex, time.Now().Unix(),
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
	u.Source = "config"
	if strings.TrimSpace(cu.Name) != "" {
		u.Name = cu.Name
	}
	u.TrustLevel = cu.TrustLevel
	if strings.TrimSpace(cu.SigningPubKeyHex) != "" {
		u.SigningPubKeyHex = cu.SigningPubKeyHex
	}
}

// RecordRootSignIn upserts the trust-matrix entry for the node's OWN root
// account and counts the sign-in (PRR CONNECTION_COUNT / LAST_CONNECTED).
//
// The row is a RECORD, never the authorization: completeRootSignIn admits the
// node's owner on the strength of the derived-key match alone, so a failure
// here is logged and ignored rather than propagated. Losing an audit row must
// never lock the owner out of their own node.
//
// The trust level is forced to the caller's value on every sign-in — this row
// describes the node's own account, and it must not drift if someone edits it.
func (s *UserStore) RecordRootSignIn(xpub, name string, trust peers.TrustLevel, signingPubKeyHex string) error {
	trimmed := strings.TrimSpace(xpub)
	if trimmed == "" {
		return fmt.Errorf("root sign-in requires an xpub")
	}
	normalized, err := normalizeEd25519PubKeyHex(signingPubKeyHex)
	if err != nil {
		return fmt.Errorf("invalid signing_pubkey_hex: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	peerID, _ := OperatorPeerID(trimmed)
	now := time.Now().Unix()
	_, err = s.db.Exec(`
		INSERT INTO users (xpub, peer_id, name, trust_level, signing_pubkey_hex,
		                   created_at, last_login_at, connection_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(xpub) DO UPDATE SET
			peer_id = excluded.peer_id,
			name = excluded.name,
			trust_level = excluded.trust_level,
			signing_pubkey_hex = excluded.signing_pubkey_hex,
			last_login_at = excluded.last_login_at,
			connection_count = users.connection_count + 1
	`, trimmed, peerID, strings.TrimSpace(name), int(trust), normalized, now, now)
	if err != nil {
		return fmt.Errorf("record root sign-in: %w", err)
	}
	return nil
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

// ProfileUpdate carries the fields an operator may change ON THEIR OWN ROW.
//
// Every field is a pointer so that "absent" and "cleared" stay distinguishable:
// a PATCH that omits Organization must not blank an organization the operator
// set last week, while a PATCH that sends "" must. Nothing outside this struct
// is writable by its owner — trust level, signing key, xpub, sign-in count and
// created_at are facts the NODE asserts about an operator, not claims the
// operator makes about themselves, and they stay on the Admin surface
// (nst-node-admin-contract §7).
type ProfileUpdate struct {
	Name         *string
	Organization *string
	Notes        *string
	VCardData    *string
}

// UpdateProfile writes the self-describing fields of ONE operator row.
//
// # Why this exists at all
//
// PUT /api/auth/users/<xpub> has never written `name` — it calls UpdateTrust
// and UpdateSigningPubKey and drops the rest of the body on the floor — so both
// the account modal's rename and the ACCOUNTS page's rename have been silent
// no-ops: the request returned 200 and the row never changed. This is the write
// those surfaces always claimed to make.
//
// # Why config rows refuse instead of writing
//
// applyConfigOverrides re-imposes the config file's Name over whatever the
// database holds on every read, so a write here would appear to succeed and
// then vanish on the next GET. Refusing is the honest outcome, and it matches
// UpdateTrust's existing refusal for the same rows.
func (s *UserStore) UpdateProfile(xpub string, update ProfileUpdate) error {
	trimmed := strings.TrimSpace(xpub)
	if trimmed == "" {
		return fmt.Errorf("profile update requires an xpub")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.configUsers[trimmed]; ok {
		return fmt.Errorf("this entry comes from the node config file and cannot be edited through the API")
	}

	assignments := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if update.Name != nil {
		assignments = append(assignments, "name = ?")
		args = append(args, strings.TrimSpace(*update.Name))
	}
	if update.Organization != nil {
		assignments = append(assignments, "organization = ?")
		args = append(args, strings.TrimSpace(*update.Organization))
	}
	if update.Notes != nil {
		assignments = append(assignments, "notes = ?")
		args = append(args, strings.TrimSpace(*update.Notes))
	}
	if update.VCardData != nil {
		assignments = append(assignments, "vcard_data = ?")
		args = append(args, strings.TrimSpace(*update.VCardData))
	}
	if len(assignments) == 0 {
		return fmt.Errorf("no editable profile fields were supplied")
	}

	args = append(args, trimmed)
	result, err := s.db.Exec(
		"UPDATE users SET "+strings.Join(assignments, ", ")+" WHERE xpub = ?",
		args...,
	)
	if err != nil {
		return fmt.Errorf("failed to update profile: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
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

	if _, ok := s.configUsers[xpub]; ok {
		return fmt.Errorf("config-managed users cannot have trust changed through the API")
	}

	result, err := s.db.Exec("UPDATE users SET trust_level = ? WHERE xpub = ?", int(trust), xpub)
	if err != nil {
		return fmt.Errorf("failed to update trust: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("user not found")
	}

	return nil
}

// RecordLogin updates the last login timestamp for a user.
func (s *UserStore) RecordLogin(xpub string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()

	// Try to update existing database user. connection_count is PRR's
	// CONNECTION_COUNT for an operator — a sign-in IS an operator's
	// connection — so it advances on every recorded login, exactly as the peer
	// registry counts peer connections.
	result, err := s.db.Exec(
		"UPDATE users SET last_login_at = ?, connection_count = connection_count + 1 WHERE xpub = ?",
		now, xpub)
	if err != nil {
		return err
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		// Config user — create a database entry to track login
		if cu, ok := s.configUsers[xpub]; ok {
			peerID, _ := OperatorPeerID(xpub)
			_, err := s.db.Exec(
				`INSERT INTO users (xpub, peer_id, name, trust_level, signing_pubkey_hex,
				                    created_at, last_login_at, connection_count)
				 VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
				xpub, peerID, cu.Name, int(cu.TrustLevel), cu.SigningPubKeyHex, now, now,
			)
			return err
		}
	}

	return nil
}

// Close releases the store's private engine database.
func (s *UserStore) Close() error {
	return s.closer()
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
