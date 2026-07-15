// Package credstore implements a small named-credential store for operator-
// supplied third-party credentials (Space-Track today; EDC/DGFI CPF and
// MyIntelsat next) on the kubo-based SDN node. It is a direct port of the
// sdn-server internal/credstore keystore, re-homed onto the kubo node's own
// libp2p identity key and repo path, and made self-contained (no dependency on
// sdn-server internals — the crypto envelope and machine fingerprint are ported
// alongside in crypto.go / machine*.go).
//
// # WHY THIS IS NOT AN SDS RECORD
//
// SDS records replicate to peers. A credential placed in the record store is a
// credential published to the world. These credentials therefore live in a
// separate, node-local keystore file that is never announced, never gossiped,
// and never handed to the datasync/channel layer:
//
//	<Repo.Path>/sdn/secrets/credentials.enc     mode 0600
//	<Repo.Path>/sdn/secrets/                     mode 0700
//
// # CRYPTOGRAPHY — composed, not invented
//
// The whole keystore is sealed with the same Argon2id + XChaCha20-Poly1305
// envelope the sdn-server node already uses for its own identity key at rest
// (ported to crypto.go):
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
//  1. machine state   deriveDefaultPassword() — Argon2id over the stable
//     hardware fingerprint (machine.go).
//
//  2. node key pair   the UNLOCKED libp2p identity private key's raw bytes.
//     On the kubo node this is core.IpfsNode.PrivateKey.Raw()
//     (the equivalent of sdn-server's node.IdentityKeyMaterial()).
//     The in-memory key the node already unlocked at boot is
//     passed in; the keystore is never opened with a hardcoded
//     password.
//
//  3. machine name    the hostname (os.Hostname()).
//
//     rootKey = HKDF-SHA256(ikm = len||machineState || len||nodePrivKey || len||hostname,
//     info = "sdn/secrets/credstore/root/v2")
//
// The resolved root is then domain-separated again with HKDF-SHA256 (info
// "sdn/secrets/credstore/v1") into the actual keystore passphrase and sealed
// with the envelope above (per-write random salt and nonce).
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
// simply the lanes that exist today.
const (
	IDSpaceTrack = "spacetrack"
	IDEDCCPF     = "edc_cpf"
	IDMyIntelsat = "myintelsat"
)

// AllIDs lists every credential lane the node knows about. Each gets its own
// capability (CapabilityForID) so that approving a module for one lane grants
// exactly that lane. The secrets capability is registered under each of these
// in sdnservices.BuildServices.
//
// The whole "secrets:" prefix is gated by modulert.IsSensitiveCapability, so a
// lane missed here fails closed rather than default-allowing.
func AllIDs() []string {
	return []string{IDSpaceTrack, IDEDCCPF, IDMyIntelsat}
}

// ErrNotConfigured is returned by Reveal when no credential is stored under id.
var ErrNotConfigured = errors.New("credential not configured")

// Secret is a credential's plaintext secret. It is a string underneath, but it
// refuses to render itself: json.Marshal, fmt's %v/%s/%q/%#v, and String() all
// emit "[REDACTED]". Plaintext leaves only via the explicit, greppable Reveal().
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
// (core.IpfsNode.PrivateKey.Raw()); ResolveRoot zeroizes the slice before
// returning, so callers must pass a fresh copy (crypto.PrivKey.Raw already
// returns one).
func ResolveRoot(nodePrivKey []byte) (string, error) {
	defer zero(nodePrivKey)
	if env := os.Getenv("SDN_KEY_PASSWORD"); env != "" {
		return env, nil
	}
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "", errors.New("credstore: hostname unavailable; refusing to derive root key")
	}
	return DeriveRootKey(deriveDefaultPassword(), nodePrivKey, hostname)
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
	root, err := ResolveRoot(nodePrivKey)
	if err != nil {
		return nil, err
	}
	return NewStore(storageDir, root)
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
// JSON, marshaled JSON). Go strings are immutable and cannot be wiped.
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
	plain, err := decryptSecret(raw[len(credFileMagic):], string(s.passphrase))
	if err != nil {
		// Deliberately does not echo any file bytes.
		return nil, fmt.Errorf("credential store: decrypt failed (wrong node key or corrupted file)")
	}
	defer zero(plain)

	creds := map[string]Credential{}
	if err := json.Unmarshal(plain, &creds); err != nil {
		return nil, fmt.Errorf("credential store: malformed contents")
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

	enc, err := encryptSecret(plain, string(s.passphrase))
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
func (s *Store) Put(id, username, secret string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("credential id required")
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

// MaskUsername renders a username for display without disclosing it:
// "operator@example.com" -> "o***@example.com", "someuser" -> "s***".
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
