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
// The root key is the node's existing key material, resolved exactly as the node
// resolves the password for its identity key and mnemonic (RootPassword below):
// SDN_KEY_PASSWORD > config security.key_password > keys.DeriveDefaultPassword()
// (Argon2id over the stable hardware fingerprint; deliberately NOT the hostname,
// NOT the peer ID, NOT a constant).
//
// That root is then domain-separated with HKDF-SHA256 so the credential
// keystore's passphrase is not literally the node identity key's passphrase:
//
//	credstorePassphrase = HKDF-SHA256(ikm=rootPassword, info="sdn/secrets/credstore/v1")
//
// Compromise of one does not hand the attacker the other's passphrase directly.
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
// simply the lanes that exist today.
const (
	IDSpaceTrack = "spacetrack"
	IDEDCCPF     = "edc_cpf"
	IDMyIntelsat = "myintelsat"
)

// AllIDs lists every credential lane the node knows about. Each gets its own
// capability (caps.CapabilityForID) so that approving a module for one lane
// grants exactly that lane.
//
// Adding a lane here means adding it to modulert's sensitiveCapabilities too —
// though IsSensitiveCapability gates the whole "secrets:" prefix, so a missed
// entry fails closed rather than default-allowing.
func AllIDs() []string {
	return []string{IDSpaceTrack, IDEDCCPF, IDMyIntelsat}
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
}

// RootPassword resolves the node's root key password exactly as the node does
// for its identity key and mnemonic: SDN_KEY_PASSWORD, then config
// security.key_password, then the machine-derived default.
//
// Kept here (rather than reaching into internal/node) so the credential store
// can be constructed without a running Node.
func RootPassword(configuredKeyPassword string) string {
	if env := os.Getenv("SDN_KEY_PASSWORD"); env != "" {
		return env
	}
	if configuredKeyPassword != "" {
		return configuredKeyPassword
	}
	return keys.DeriveDefaultPassword()
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
	plain, err := keys.DecryptSecret(raw[len(credFileMagic):], string(s.passphrase))
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
