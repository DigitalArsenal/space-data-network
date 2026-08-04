// Package secrets implements a small named-credential store for operator-supplied
// third-party credentials (Space-Track today; EDC/DGFI CPF and MyIntelsat next).
//
// # WHY THIS IS NOT AN SDS RECORD
//
// SDS records replicate to peers. A credential placed in the record store is a
// credential published to the world. These credentials therefore live in a
// separate, node-local keystore file that is never announced, never gossiped,
// and never handed to the datasync layer:
//
//	<storage.path>/secrets/credentials.enc     mode 0600
//	<storage.path>/secrets/                    mode 0700
//
// # CRYPTOGRAPHY — composed, not invented
//
// This package introduces NO new cryptographic construction. The whole keystore
// is sealed with internal/keys.EncryptSecret, the exact envelope the node
// already uses for its own libp2p identity key at rest (see
// internal/node/node.go writeEncryptedNodeKey / "migrated node identity key ...
// to encrypted-at-rest"):
//
//	KDF   Argon2id (t=3, m=64 MiB, p=4, 32-byte key), random 32-byte salt per write
//	AEAD  XChaCha20-Poly1305, random 24-byte nonce per write
//	File  magic || salt(32) || nonce(24) || ciphertext
//
// # ROOT KEY — deterministic, machine-bound, no external secret
//
// The DEFAULT root key is derived deterministically from THREE inputs the node
// can reproduce on every boot with NO env var, NO prompt, and NO external secret
// (owner directive: unattended operation). Same machine + same node identity +
// same hostname => same root => decrypts the same credentials.enc:
//
//  1. machine state   keys.DeriveDefaultPassword() — Argon2id over the stable
//     (machine, user)-bound fingerprint; the SAME mechanism the
//     node already relies on to unlock its own identity key at
//     rest, so it adds no new fragility. It can REFUSE (unknown
//     user, unreadable source); that refusal fails closed here.
//
//  2. node key pair   the UNLOCKED libp2p identity private key's raw bytes
//     (node.IdentityKeyMaterial()). Never re-read from node.key
//     here, never with a hardcoded password — the in-memory key
//     the node already unlocked at boot is passed in.
//
//  3. machine name    the hostname (os.Hostname()).
//
//     rootKey = HKDF-SHA256(ikm = len||machineState || len||nodePrivKey || len||hostname,
//     info = "sdn/secrets/credstore/root/v2")
//
// The resolved root is then domain-separated again with HKDF-SHA256 (info
// "sdn/secrets/credstore/v1") into the actual keystore passphrase and sealed
// with the Argon2id + XChaCha20-Poly1305 envelope above (per-write random salt
// and nonce). Only the root IKM changed vs the first cut; the envelope is
// unchanged.
//
// SECURITY POSTURE: binding to (2) and (3) defeats FILE EXFILTRATION —
// credentials.enc copied to another host, or opened under a different node
// identity, will not decrypt. It does NOT defend against code running as the
// same user on the same host: a deterministic, no-external-secret key is
// inherently re-derivable there. That is the accepted cost of unattended boot.
//
// OVERRIDE: SDN_KEY_PASSWORD, if set, replaces the derivation and is used
// verbatim. Once used it must remain set on every subsequent boot, or the
// keystore becomes undecryptable.
//
// FAIL CLOSED: if the node identity key is unavailable at init, ResolveRoot
// returns an error and the credential routes refuse to serve — a weaker key is
// never substituted.
//
// # WRITE-ONLY FROM THE OUTSIDE
//
// Reveal() is the ONLY accessor that returns plaintext, it is host-side (daemon)
// only, and it is never reachable from an HTTP handler. The Secret type below
// actively resists exfiltration: it redacts itself under encoding/json, fmt %v /
// %s / %#v, and String(), so an accidental log line or API struct embed emits
// "[REDACTED]" instead of the credential.
package credstore

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// credFileMagic prefixes the keystore file. The NUL keeps it from colliding with
// any plausible plaintext/JSON first bytes, so a legacy or corrupt file is
// detected rather than misparsed.
var credFileMagic = []byte("sdncred1\x00")

const (
	// credDirName / credFileName are relative to the node's storage path.
	credDirName  = "secrets"
	credFileName = "credentials.enc"

	// DirMode / FileMode are asserted by tests. The keystore is operator-only.
	DirMode  os.FileMode = 0700
	FileMode os.FileMode = 0600

	// hkdfInfo domain-separates the credential keystore key from every other
	// use of the node's root key password.
	hkdfInfo = "sdn/secrets/credstore/v1"
)

// Well-known credential IDs. The store is generic (id -> credential); these are
// simply the lanes the node ships knowing about, i.e. the ones that have a
// built-in verifier and documented semantics.
const (
	IDSpaceTrack = "spacetrack"
	IDEDCCPF     = "edc_cpf"
	IDMyIntelsat = "myintelsat"
)

// AllIDs lists the WELL-KNOWN credential lanes — the ones the node ships
// knowing about. It is NOT the set of lanes that exist on a running node: an
// operator may define arbitrary lanes for any service (see ValidateLaneID), and
// the live set is well-known ∪ stored, reported by Store.Lanes.
//
// Each lane gets its own capability (caps.CapabilityForID) so that approving a
// module for one lane grants exactly that lane. Operator-defined lanes are
// covered by the same rule: modulert.IsSensitiveCapability gates the whole
// "secrets:" prefix, so a lane nobody enumerated fails closed (denied at load
// without an explicit per-hash approval) rather than default-allowing.
func AllIDs() []string {
	return []string{IDSpaceTrack, IDEDCCPF, IDMyIntelsat}
}

// IsWellKnown reports whether id is one of the lanes the node ships knowing
// about (AllIDs). Everything else is an operator-defined lane: stored the same
// way, gated the same way, but with no built-in verifier.
func IsWellKnown(id string) bool {
	switch strings.TrimSpace(id) {
	case IDSpaceTrack, IDEDCCPF, IDMyIntelsat:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------
// Lane ids — operator-defined lanes (owner 2026-08-04)
// ---------------------------------------------------------------------

// Lane-id shape. A lane id is not merely a map key: it becomes a capability
// name ("secrets:<id>") that an operator reads, reasons about, and approves per
// module hash in capability_policy.json. So it is constrained to a form that is
// unambiguous on sight and stable across surfaces — one canonical spelling, no
// case folding, no path/JSON/shell metacharacters, no whitespace.
const (
	// MinLaneIDLen rejects one-character lanes: they are too easy to typo into
	// a DIFFERENT lane, and a typo here means approving a module for the wrong
	// credential.
	MinLaneIDLen = 2
	// MaxLaneIDLen bounds what ends up in a capability string and in the
	// policy file.
	MaxLaneIDLen = 64
)

// reservedLaneIDPrefixes may never be claimed by an operator-defined lane.
//
// "sdn_" is reserved so the project can later mint node-semantic lanes (lanes
// the daemon itself interprets) without colliding with something an operator
// already created — a collision that would silently re-point an existing
// operator approval at different semantics.
var reservedLaneIDPrefixes = []string{"sdn_"}

// ErrInvalidLaneID is the sentinel wrapped by every ValidateLaneID failure, so
// callers can distinguish "this id is not allowed" from a keystore error.
var ErrInvalidLaneID = errors.New("invalid credential lane id")

// ValidateLaneID enforces the lane-id rule for lanes being CREATED:
//
//	^[a-z0-9_-]{2,64}$   and not starting with a reserved prefix
//
// Lowercase only — deliberately not case-folded. Two spellings of one lane
// ("SpaceTrack" and "spacetrack") would be two capability names, and an
// operator approving one while a module declares the other is a silent denial
// at best and a mis-scoped approval at worst.
//
// The well-known ids (AllIDs) satisfy this rule and are accepted as they are
// today; the reserved-prefix check applies to every id including theirs.
//
// The returned errors describe the RULE and never echo the offending id — the
// id arrives from an HTTP path and is reflected back to the caller.
func ValidateLaneID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("%w: a lane id is required", ErrInvalidLaneID)
	}
	if len(id) < MinLaneIDLen || len(id) > MaxLaneIDLen {
		return fmt.Errorf("%w: must be %d-%d characters", ErrInvalidLaneID, MinLaneIDLen, MaxLaneIDLen)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			return fmt.Errorf("%w: only lowercase letters, digits, '_' and '-' are allowed", ErrInvalidLaneID)
		}
	}
	for _, prefix := range reservedLaneIDPrefixes {
		if strings.HasPrefix(id, prefix) {
			return fmt.Errorf("%w: the %q prefix is reserved", ErrInvalidLaneID, prefix)
		}
	}
	return nil
}

// ErrNotConfigured is returned by Reveal when no credential is stored under id.
var ErrNotConfigured = errors.New("credential not configured")

// Secret is a credential's plaintext secret. It is a string underneath, but it
// refuses to render itself: json.Marshal, fmt's %v/%s/%q/%#v, and String() all
// emit "[REDACTED]". Plaintext leaves only via the explicit, greppable Reveal().
//
// This is defense in depth for two of the hard requirements — "plaintext is
// never serialized into an API response" and "never log the credential" — so
// that a future careless `log.Printf("%+v", cred)` or a struct embedded in a
// response body cannot leak the password even by accident.
type Secret string

const redacted = "[REDACTED]"

// MarshalJSON redacts. A Secret can never be serialized into an API response.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// String redacts.
func (Secret) String() string { return redacted }

// GoString redacts (%#v).
func (Secret) GoString() string { return redacted }

// Format redacts under every fmt verb, including %s, %v, %q and %x.
func (Secret) Format(f fmt.State, verb rune) { _, _ = io.WriteString(f, redacted) }

// Reveal returns the plaintext. This is the ONLY way to obtain it, and it must
// never be called from an HTTP handler.
func (s Secret) Reveal() string { return string(s) }

// Credential is one named credential.
type Credential struct {
	Username   string     `json:"username"`
	Secret     Secret     `json:"secret"`
	UpdatedAt  time.Time  `json:"updated_at"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
}

// Status is the ONLY shape that may cross the API boundary. It carries no
// secret: configured-or-not, a masked username, and timestamps.
type Status struct {
	ID             string     `json:"id"`
	Configured     bool       `json:"configured"`
	UsernameMasked string     `json:"username_masked,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
}

// Store is the encrypted-at-rest named-credential keystore.
//
// It deliberately holds NO decrypted credential in memory: every read decrypts
// the file on demand and zeroizes the plaintext buffer before returning. The
// only long-lived secret in the process is the derived keystore passphrase.
type Store struct {
	mu         sync.Mutex
	path       string
	passphrase []byte
	// fallbacks are passphrases derived from OLDER machine-derivation
	// generations, tried only when the current one fails to open an existing
	// keystore. On success the keystore is re-sealed under passphrase, so the
	// fallbacks are used at most once per upgrade. See keys.CandidatePasswords.
	fallbacks [][]byte
}

// rootHKDFInfo domain-separates the three-input root derivation. Bumping the
// "/v2" suffix is a breaking change: it re-derives the root and orphans any
// existing credentials.enc.
const rootHKDFInfo = "sdn/secrets/credstore/root/v2"

// ResolveRoot resolves the credential-keystore root key.
//
// Override: if SDN_KEY_PASSWORD is set it is returned verbatim (and MUST stay
// set on every subsequent boot, or the keystore becomes undecryptable).
//
// Default: the deterministic three-input derivation binding the keystore to this
// machine + this node identity + this hostname (see the package doc). It FAILS
// CLOSED — never a weaker key — if the node identity key or the hostname is
// unavailable.
//
// nodePrivKey MUST be the UNLOCKED identity private key's raw bytes
// (node.IdentityKeyMaterial()); ResolveRoot zeroizes the slice before returning,
// so callers must pass a fresh copy (IdentityKeyMaterial already returns one).
func ResolveRoot(nodePrivKey []byte) (string, error) {
	defer zero(nodePrivKey)
	if env := os.Getenv("SDN_KEY_PASSWORD"); env != "" {
		return env, nil
	}
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "", errors.New("credstore: hostname unavailable; refusing to derive root key")
	}
	machineState, err := keys.DeriveDefaultPassword()
	if err != nil {
		// Fail closed (never a weaker key): the machine derivation refused —
		// e.g. unknown user, or a source that exists but cannot be read.
		return "", fmt.Errorf("credstore: machine derivation refused; refusing to derive root key: %w", err)
	}
	return DeriveRootKey(machineState, nodePrivKey, hostname)
}

// DeriveRootKey combines the three machine-binding inputs into a 32-byte root
// key via HKDF-SHA256. Every input is REQUIRED; a missing input fails closed
// rather than yielding a weaker key.
//
// Exposed (not merely used by ResolveRoot) so tests can vary each input
// independently and assert the machine-binding — changing the machine state,
// the node key, or the hostname must change the root.
func DeriveRootKey(machineState string, nodePrivKey []byte, hostname string) (string, error) {
	if machineState == "" {
		return "", errors.New("credstore: machine fingerprint unavailable; refusing to derive root key")
	}
	if len(nodePrivKey) == 0 {
		return "", errors.New("credstore: node identity key unavailable; refusing to derive a weaker root key")
	}
	if strings.TrimSpace(hostname) == "" {
		return "", errors.New("credstore: hostname unavailable; refusing to derive root key")
	}

	// Length-prefixed concatenation: an unambiguous encoding so no shift between
	// inputs (a hostname suffix vs a key prefix, say) can collide with a
	// different triple.
	var ikm []byte
	ikm = appendField(ikm, []byte(machineState))
	ikm = appendField(ikm, nodePrivKey)
	ikm = appendField(ikm, []byte(hostname))
	defer zero(ikm)

	r := hkdf.New(sha256.New, ikm, nil, []byte(rootHKDFInfo))
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		return "", fmt.Errorf("credstore: derive root key: %w", err)
	}
	return string(out), nil
}

// appendField appends an 8-byte big-endian length prefix followed by b.
func appendField(dst, b []byte) []byte {
	var l [8]byte
	binary.BigEndian.PutUint64(l[:], uint64(len(b)))
	dst = append(dst, l[:]...)
	return append(dst, b...)
}

// OpenStore resolves the machine-bound root key and opens the keystore in one
// call. Fails closed (no weaker-key fallback) if the node identity key is
// unavailable — the caller must then refuse to serve the credential routes.
//
// nodePrivKey is the unlocked identity private key's raw bytes; it is zeroized
// during resolution.
func OpenStore(storageDir string, nodePrivKey []byte) (*Store, error) {
	// Keep a copy: ResolveRoot zeroizes what it is given, and the older
	// generations need the same input.
	keyCopy := append([]byte(nil), nodePrivKey...)
	root, err := ResolveRoot(nodePrivKey)
	if err != nil {
		zero(keyCopy)
		return nil, err
	}
	store, err := NewStore(storageDir, root)
	if err != nil {
		zero(keyCopy)
		return nil, err
	}
	// An explicit operator passphrase does not depend on machine state, so
	// there is nothing to migrate from.
	if os.Getenv("SDN_KEY_PASSWORD") == "" {
		store.attachLegacyRoots(keyCopy)
	}
	zero(keyCopy)
	return store, nil
}

// attachLegacyRoots derives the keystore passphrases for OLDER machine
// derivation generations, so credentials sealed before the derivation upgrade
// still open (and are then re-sealed under the current generation).
//
// Without this, changing keys.DeriveDefaultPassword silently orphans every
// node's stored credentials.
func (s *Store) attachLegacyRoots(nodePrivKey []byte) {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return
	}
	for _, cand := range keys.CandidatePasswords() {
		if cand.Scheme == keys.SchemeV4 {
			continue // already the primary
		}
		root, derr := DeriveRootKey(cand.Password, append([]byte(nil), nodePrivKey...), hostname)
		if derr != nil {
			continue
		}
		pass, perr := derivePassphrase(root)
		if perr != nil {
			continue
		}
		s.fallbacks = append(s.fallbacks, pass)
	}
}

// derivePassphrase domain-separates the root password for credential-keystore use.
func derivePassphrase(rootPassword string) ([]byte, error) {
	r := hkdf.New(sha256.New, []byte(rootPassword), nil, []byte(hkdfInfo))
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("derive credential keystore key: %w", err)
	}
	return out, nil
}

// NewStore opens (does not create) the credential keystore under storageDir.
// The secrets directory is created 0700 if absent; the file is written 0600 on
// first Put.
func NewStore(storageDir, rootPassword string) (*Store, error) {
	if strings.TrimSpace(storageDir) == "" {
		return nil, errors.New("secrets: storage dir required")
	}
	if rootPassword == "" {
		// Fail closed: without a root key we would otherwise be tempted to
		// write plaintext or use a constant. Neither is acceptable.
		return nil, errors.New("secrets: empty root key password; refusing to operate")
	}
	dir := filepath.Join(storageDir, credDirName)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return nil, fmt.Errorf("create secrets dir: %w", err)
	}
	// MkdirAll honors umask, and an upgrade may inherit a looser mode from an
	// earlier release; force the mode we advertise.
	if err := os.Chmod(dir, DirMode); err != nil {
		return nil, fmt.Errorf("secure secrets dir: %w", err)
	}
	pass, err := derivePassphrase(rootPassword)
	if err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, credFileName), passphrase: pass}, nil
}

// Path is the keystore file path (tests assert its mode).
func (s *Store) Path() string { return s.path }

// zero overwrites b. Meaningful for the []byte buffers we control (decrypted
// JSON, marshaled JSON). Go strings are immutable and cannot be wiped; see the
// package README note.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// load decrypts the keystore. A missing file is an empty store, not an error.
// The caller must not retain the returned map beyond its immediate use.
func (s *Store) load() (map[string]Credential, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Credential{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credential store: %w", err)
	}
	defer zero(raw)

	if !hasPrefix(raw, credFileMagic) {
		// Fail closed. We never guess at an unrecognized keystore.
		return nil, errors.New("credential store: unrecognized file format")
	}
	body := raw[len(credFileMagic):]
	plain, err := keys.DecryptSecret(body, string(s.passphrase))
	migrated := false
	if err != nil {
		// Try older machine-derivation generations before failing. This is the
		// upgrade path for a node whose credentials were sealed under the
		// previous derivation; without it the upgrade loses the credentials.
		for _, fb := range s.fallbacks {
			if alt, aerr := keys.DecryptSecret(body, string(fb)); aerr == nil {
				plain, err, migrated = alt, nil, true
				break
			}
		}
	}
	if err != nil {
		// Deliberately does not echo any file bytes.
		return nil, fmt.Errorf("credential store: decrypt failed (wrong node key or corrupted file)")
	}
	defer zero(plain)

	creds := map[string]Credential{}
	if err := json.Unmarshal(plain, &creds); err != nil {
		return nil, fmt.Errorf("credential store: malformed contents")
	}
	if migrated {
		// Re-seal under the current derivation so the fallback is needed only
		// once. A write failure is not fatal — the credentials were recovered
		// and the fallback will simply run again next boot.
		if serr := s.save(creds); serr != nil {
			return creds, nil
		}
	}
	return creds, nil
}

// save encrypts and atomically replaces the keystore with mode 0600.
func (s *Store) save(creds map[string]Credential) error {
	// Credential.Secret redacts under encoding/json (that is the whole point of
	// the type), so persistence uses an explicit on-disk DTO carrying the real
	// plaintext. This is the ONLY place a secret is serialized, and its output
	// goes straight into the AEAD.
	type diskCred struct {
		Username   string     `json:"username"`
		Secret     string     `json:"secret"`
		UpdatedAt  time.Time  `json:"updated_at"`
		VerifiedAt *time.Time `json:"verified_at,omitempty"`
	}
	disk := make(map[string]diskCred, len(creds))
	for id, c := range creds {
		disk[id] = diskCred{
			Username:   c.Username,
			Secret:     c.Secret.Reveal(),
			UpdatedAt:  c.UpdatedAt,
			VerifiedAt: c.VerifiedAt,
		}
	}
	plain, err := json.Marshal(disk)
	if err != nil {
		return errors.New("credential store: encode failed")
	}
	defer zero(plain)

	enc, err := keys.EncryptSecret(plain, string(s.passphrase))
	if err != nil {
		return fmt.Errorf("credential store: encrypt failed: %w", err)
	}
	out := make([]byte, 0, len(credFileMagic)+len(enc))
	out = append(out, credFileMagic...)
	out = append(out, enc...)

	// Atomic replace so a crash mid-write cannot truncate the keystore.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, FileMode); err != nil {
		return fmt.Errorf("write credential store: %w", err)
	}
	if err := os.Chmod(tmp, FileMode); err != nil { // defeat umask
		_ = os.Remove(tmp)
		return fmt.Errorf("secure credential store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace credential store: %w", err)
	}
	return nil
}

// Put stores (or replaces) the credential under id. Storing always resets
// VerifiedAt: a new secret has not been verified until it is probed.
//
// id may name ANY service — the well-known lanes have no privileged status
// here — but it must satisfy ValidateLaneID. Validating at the STORE (not only
// at the admin API) means no caller anywhere can mint a lane whose id would not
// round-trip through a capability name and an operator approval.
func (s *Store) Put(id, username, secret string) error {
	id = strings.TrimSpace(id)
	if err := ValidateLaneID(id); err != nil {
		return err
	}
	if strings.TrimSpace(username) == "" {
		return errors.New("username required")
	}
	if secret == "" {
		return errors.New("secret required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return err
	}
	creds[id] = Credential{
		Username:  strings.TrimSpace(username),
		Secret:    Secret(secret),
		UpdatedAt: time.Now().UTC(),
	}
	return s.save(creds)
}

// MarkVerified records a successful credential probe.
func (s *Store) MarkVerified(id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return err
	}
	c, ok := creds[id]
	if !ok {
		return ErrNotConfigured
	}
	at = at.UTC()
	c.VerifiedAt = &at
	creds[id] = c
	return s.save(creds)
}

// Clear removes the credential under id. Clearing an absent id is not an error.
func (s *Store) Clear(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := creds[id]; !ok {
		return nil
	}
	delete(creds, id)
	if len(creds) == 0 {
		// No credentials left: remove the file rather than leaving an
		// encrypted empty map behind.
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove credential store: %w", err)
		}
		return nil
	}
	return s.save(creds)
}

// Reveal returns the plaintext credential for id. HOST-SIDE ONLY — never call
// this from an HTTP handler. Callers: the capability-gated module host call and
// the credential-verification probe.
func (s *Store) Reveal(id string) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return Credential{}, err
	}
	c, ok := creds[id]
	if !ok {
		return Credential{}, ErrNotConfigured
	}
	return c, nil
}

// Status reports configured-or-not for id. Safe to serialize to an API response.
func (s *Store) Status(id string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return Status{}, err
	}
	c, ok := creds[id]
	if !ok {
		return Status{ID: id, Configured: false}, nil
	}
	updated := c.UpdatedAt
	return Status{
		ID:             id,
		Configured:     true,
		UsernameMasked: MaskUsername(c.Username),
		UpdatedAt:      &updated,
		VerifiedAt:     c.VerifiedAt,
	}, nil
}

// List reports status for every configured credential, sorted by id.
func (s *Store) List() ([]Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(creds))
	for id, c := range creds {
		updated := c.UpdatedAt
		out = append(out, Status{
			ID:             id,
			Configured:     true,
			UsernameMasked: MaskUsername(c.Username),
			UpdatedAt:      &updated,
			VerifiedAt:     c.VerifiedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Lanes reports every credential lane this node knows about: the well-known set
// (AllIDs) UNION every id actually present in the keystore, sorted.
//
// This is the enumeration every surface should use. AllIDs alone would hide the
// operator's own lanes; the stored ids alone would hide a well-known lane that
// simply has not been filled in yet (and the operator needs to see that it
// exists in order to fill it in).
//
// The lane ids themselves are NOT secret — they are service names, and they are
// already visible to the operator in capability_policy.json. No credential
// material is read here beyond the map keys.
func (s *Store) Lanes() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(creds)+3)
	out := make([]string, 0, len(creds)+3)
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range AllIDs() {
		add(id)
	}
	for id := range creds {
		add(id)
	}
	sort.Strings(out)
	return out, nil
}

// ListLanes reports the status of every lane Lanes() reports — including
// well-known lanes with nothing stored yet, which come back Configured=false.
//
// Contrast List(), which reports only lanes that HAVE a credential. Both are
// secret-free (Status carries none); ListLanes is the one an operator surface
// wants, because "this lane exists and is empty" is exactly the state that
// needs acting on.
//
// A lane that was stored but never probed keeps VerifiedAt nil. That is the
// honest answer — "saved, not verified" — and it is what an operator-defined
// lane with no verifier will report forever. Nothing here fabricates a
// verification that did not happen.
func (s *Store) ListLanes() ([]Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(creds)+3)
	seen := make(map[string]bool, len(creds)+3)
	for _, id := range AllIDs() {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for id := range creds {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	out := make([]Status, 0, len(ids))
	for _, id := range ids {
		c, ok := creds[id]
		if !ok {
			out = append(out, Status{ID: id, Configured: false})
			continue
		}
		updated := c.UpdatedAt
		out = append(out, Status{
			ID:             id,
			Configured:     true,
			UsernameMasked: MaskUsername(c.Username),
			UpdatedAt:      &updated,
			VerifiedAt:     c.VerifiedAt,
		})
	}
	return out, nil
}

// MaskUsername renders a username for display without disclosing it:
// "operator@example.com" -> "o***@example.com", "someuser" -> "s***".
//
// The local part is reduced to its first rune; the domain is retained because it
// is not secret and lets an operator confirm WHICH account is configured.
func MaskUsername(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	local, domain, hasAt := strings.Cut(u, "@")
	r := []rune(local)
	masked := string(r[0]) + "***"
	if hasAt && domain != "" {
		return masked + "@" + domain
	}
	return masked
}

func hasPrefix(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := range prefix {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}
