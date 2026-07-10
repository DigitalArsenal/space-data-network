// Package keys provides cryptographic key management for SDN servers.
// It handles Ed25519 signing keys and X25519 encryption keys.
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	logging "github.com/ipfs/go-log/v2"
	"golang.org/x/crypto/curve25519"
)

var log = logging.Logger("sdn-keys")

// Errors
var (
	ErrKeyNotFound      = errors.New("key not found")
	ErrInvalidKey       = errors.New("invalid key data")
	ErrKeyAlreadyExists = errors.New("key already exists")
)

const (
	// Key file names
	SigningPrivateKeyFile    = "signing_private.key"
	SigningPublicKeyFile     = "signing_public.key"
	EncryptionPrivateKeyFile = "encryption_private.key"
	EncryptionPublicKeyFile  = "encryption_public.key"

	// Key sizes
	Ed25519PublicKeySize  = ed25519.PublicKeySize
	Ed25519PrivateKeySize = ed25519.PrivateKeySize
	Ed25519SeedSize       = ed25519.SeedSize
	X25519KeySize         = 32
)

// KeyPair represents a cryptographic key pair.
type KeyPair struct {
	PublicKey  []byte
	PrivateKey []byte
	KeyType    string // "Ed25519" or "X25519"
}

// Identity represents the server's cryptographic identity.
type Identity struct {
	SigningKey    *KeyPair
	EncryptionKey *KeyPair
}

// Manager handles key generation, storage, and retrieval.
type Manager struct {
	basePath string
	identity *Identity
}

// NewManager creates a new key manager.
func NewManager(basePath string) (*Manager, error) {
	keysPath := filepath.Join(basePath, "keys")
	if err := os.MkdirAll(keysPath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create keys directory: %w", err)
	}

	return &Manager{
		basePath: keysPath,
	}, nil
}

// HasIdentity returns true if an identity key exists.
func (m *Manager) HasIdentity() bool {
	signingPath := filepath.Join(m.basePath, SigningPrivateKeyFile)
	_, err := os.Stat(signingPath)
	return err == nil
}

// LoadIdentity loads the existing identity from disk.
func (m *Manager) LoadIdentity() (*Identity, error) {
	if !m.HasIdentity() {
		return nil, ErrKeyNotFound
	}

	// Load signing key
	signingPriv, err := m.loadKey(SigningPrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load signing private key: %w", err)
	}

	signingPub, err := m.loadKey(SigningPublicKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load signing public key: %w", err)
	}

	// Load encryption key
	encryptionPriv, err := m.loadKey(EncryptionPrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load encryption private key: %w", err)
	}

	encryptionPub, err := m.loadKey(EncryptionPublicKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load encryption public key: %w", err)
	}

	m.identity = &Identity{
		SigningKey: &KeyPair{
			PublicKey:  signingPub,
			PrivateKey: signingPriv,
			KeyType:    "Ed25519",
		},
		EncryptionKey: &KeyPair{
			PublicKey:  encryptionPub,
			PrivateKey: encryptionPriv,
			KeyType:    "X25519",
		},
	}

	log.Info("Loaded existing server identity")
	return m.identity, nil
}

// GenerateIdentity creates a new server identity with Ed25519 signing
// and X25519 encryption key pairs, generated from crypto/rand.
//
// # Legacy status (Task F1: single hd-wallet identity, xpub-keyed, everywhere)
//
// This manager predates the hd-wallet identity system and is NOT the
// identity authority. The authority is the hd-wallet xpub: user identity
// lives in internal/auth.UserStore (keyed by xpub), and node identity lives
// in internal/node.IdentityBundle (derived from the same mnemonic/xpub via
// the hd-wallet WASM module — see internal/node/identity_bundle.go). Do not
// call GenerateIdentity to mint a new "node identity" or "user identity";
// use the hd-wallet-derived path instead.
//
// GenerateIdentity's random, non-deterministic key remains legitimate for a
// narrow set of non-identity, backward-compatibility uses that predate the
// hd-wallet system and must keep working with already-signed/already-issued
// material:
//
//  1. Legacy dataset-publication signing key fallback
//     (cmd/spacedatanetwork's datasetPublicationSigningKey, used by the
//     legacy-sqlite import and legacy-publication-plan tools): when no live
//     node hd-wallet signing key is available, these tools fall back to a
//     stable per-installation signing key so previously-published data keeps
//     verifying against the same key. Changing this to an xpub-derived key
//     would NOT be backward compatible for data already signed under the
//     legacy key.
//  2. Module-runtime key-slot fallback (internal/node's
//     loadLegacyServerIdentity / moduleRuntimeKeySlots): LoadIdentity only
//     (never GenerateIdentity) supplies provider signing/wrapping key
//     material when a node has no hd-wallet identity yet. It never mints a
//     fresh identity at runtime — if no legacy key file exists, it is
//     skipped entirely.
//
// internal/setup/handlers.go also calls GenerateIdentity as part of a
// first-time setup flow that mints a random server identity alongside a
// bare username/password admin account (see internal/admin). As of Task F1,
// that setup flow (internal/setup + internal/server) is not reachable from
// the running sdn-server binary (cmd/spacedatanetwork) — the live server
// authenticates exclusively through the hd-wallet challenge-response flow in
// internal/auth. If internal/setup's flow is ever revived, it must not mint
// a divergent identity here; it should resolve the admin into the xpub
// identity space instead (see internal/admin.Manager.BindXPub for the bridge
// mechanism this task added).
//
// New callers that already hold hd-wallet-derived key material should use
// GenerateIdentityFromSeed instead, which binds this manager's on-disk
// identity to that material deterministically rather than minting an
// unrelated random one.
func (m *Manager) GenerateIdentity() (*Identity, error) {
	if m.HasIdentity() {
		return nil, ErrKeyAlreadyExists
	}

	// Generate Ed25519 signing keypair
	signingPub, signingPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate signing key: %w", err)
	}

	// Generate X25519 encryption keypair
	var encryptionPriv [X25519KeySize]byte
	if _, err := rand.Read(encryptionPriv[:]); err != nil {
		return nil, fmt.Errorf("failed to generate encryption key: %w", err)
	}
	// Clamp the private key per X25519 spec
	encryptionPriv[0] &= 248
	encryptionPriv[31] &= 127
	encryptionPriv[31] |= 64

	var encryptionPub [X25519KeySize]byte
	curve25519.ScalarBaseMult(&encryptionPub, &encryptionPriv)

	identity, err := m.persistIdentity(signingPub, signingPriv, encryptionPriv[:], encryptionPub[:])
	if err != nil {
		return nil, err
	}

	log.Info("Generated new server identity")
	log.Infof("Signing public key: %s", hex.EncodeToString(signingPub))
	log.Infof("Encryption public key: %s", hex.EncodeToString(encryptionPub[:]))

	return identity, nil
}

// GenerateIdentityFromSeed deterministically derives a server identity from
// a caller-supplied 32-byte seed instead of crypto/rand. It exists as the
// bind-to-xpub path referenced in GenerateIdentity's doc comment: a caller
// that already holds hd-wallet-derived key material (e.g. a signing seed
// derived from the mnemonic/xpub the way internal/node.IdentityBundle does)
// can use it to make this manager's on-disk identity a deterministic
// function of that material, instead of an unrelated random identity.
//
// The same seed always yields the same signing and encryption keypairs. The
// encryption key is derived from the seed with domain separation (SHA-256
// with a fixed context string) so it is not simply the signing key's bytes
// reused under a different algorithm.
func (m *Manager) GenerateIdentityFromSeed(seed [Ed25519SeedSize]byte) (*Identity, error) {
	if m.HasIdentity() {
		return nil, ErrKeyAlreadyExists
	}

	signingPriv := ed25519.NewKeyFromSeed(seed[:])
	signingPub, ok := signingPriv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("failed to derive signing public key from seed")
	}

	encSeed := sha256.Sum256(append(append([]byte(nil), seed[:]...), []byte("sdn-keys-x25519-v1")...))
	var encryptionPriv [X25519KeySize]byte
	copy(encryptionPriv[:], encSeed[:])
	// Clamp the private key per X25519 spec
	encryptionPriv[0] &= 248
	encryptionPriv[31] &= 127
	encryptionPriv[31] |= 64

	var encryptionPub [X25519KeySize]byte
	curve25519.ScalarBaseMult(&encryptionPub, &encryptionPriv)

	identity, err := m.persistIdentity(signingPub, signingPriv, encryptionPriv[:], encryptionPub[:])
	if err != nil {
		return nil, err
	}

	log.Info("Generated new server identity from seed (deterministic, xpub-bindable)")
	log.Infof("Signing public key: %s", hex.EncodeToString(signingPub))
	log.Infof("Encryption public key: %s", hex.EncodeToString(encryptionPub[:]))

	return identity, nil
}

// persistIdentity saves the given key material to disk and sets it as the
// manager's current identity. Shared by GenerateIdentity and
// GenerateIdentityFromSeed so both produce byte-for-byte identical on-disk
// layout and in-memory Identity shape.
func (m *Manager) persistIdentity(signingPub ed25519.PublicKey, signingPriv ed25519.PrivateKey, encryptionPriv, encryptionPub []byte) (*Identity, error) {
	if err := m.saveKey(SigningPrivateKeyFile, signingPriv); err != nil {
		return nil, fmt.Errorf("failed to save signing private key: %w", err)
	}
	if err := m.saveKey(SigningPublicKeyFile, signingPub); err != nil {
		return nil, fmt.Errorf("failed to save signing public key: %w", err)
	}
	if err := m.saveKey(EncryptionPrivateKeyFile, encryptionPriv); err != nil {
		return nil, fmt.Errorf("failed to save encryption private key: %w", err)
	}
	if err := m.saveKey(EncryptionPublicKeyFile, encryptionPub); err != nil {
		return nil, fmt.Errorf("failed to save encryption public key: %w", err)
	}

	m.identity = &Identity{
		SigningKey: &KeyPair{
			PublicKey:  signingPub,
			PrivateKey: signingPriv,
			KeyType:    "Ed25519",
		},
		EncryptionKey: &KeyPair{
			PublicKey:  encryptionPub,
			PrivateKey: encryptionPriv,
			KeyType:    "X25519",
		},
	}

	return m.identity, nil
}

// GetIdentity returns the current identity.
func (m *Manager) GetIdentity() *Identity {
	return m.identity
}

// Sign signs data using the Ed25519 signing key.
func (m *Manager) Sign(data []byte) ([]byte, error) {
	if m.identity == nil || m.identity.SigningKey == nil {
		return nil, ErrKeyNotFound
	}

	signature := ed25519.Sign(m.identity.SigningKey.PrivateKey, data)
	return signature, nil
}

// Verify verifies an Ed25519 signature.
func (m *Manager) Verify(publicKey, data, signature []byte) bool {
	if len(publicKey) != Ed25519PublicKeySize {
		return false
	}
	return ed25519.Verify(publicKey, data, signature)
}

// PublicKeyFingerprint returns a fingerprint of the signing public key.
func (m *Manager) PublicKeyFingerprint() string {
	if m.identity == nil || m.identity.SigningKey == nil {
		return ""
	}
	hash := sha256.Sum256(m.identity.SigningKey.PublicKey)
	return hex.EncodeToString(hash[:8])
}

// loadKey loads a key from disk.
func (m *Manager) loadKey(filename string) ([]byte, error) {
	path := filepath.Join(m.basePath, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// saveKey saves a key to disk with secure permissions.
func (m *Manager) saveKey(filename string, data []byte) error {
	path := filepath.Join(m.basePath, filename)
	return os.WriteFile(path, data, 0600)
}

// ExportPublicKeys returns the public keys as hex strings.
func (m *Manager) ExportPublicKeys() (signingKey, encryptionKey string) {
	if m.identity == nil {
		return "", ""
	}
	if m.identity.SigningKey != nil {
		signingKey = hex.EncodeToString(m.identity.SigningKey.PublicKey)
	}
	if m.identity.EncryptionKey != nil {
		encryptionKey = hex.EncodeToString(m.identity.EncryptionKey.PublicKey)
	}
	return
}

// DeleteIdentity securely deletes the identity keys.
// This is a destructive operation and should be used with caution.
func (m *Manager) DeleteIdentity() error {
	files := []string{
		SigningPrivateKeyFile,
		SigningPublicKeyFile,
		EncryptionPrivateKeyFile,
		EncryptionPublicKeyFile,
	}

	for _, f := range files {
		path := filepath.Join(m.basePath, f)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Warnf("Failed to delete key file %s: %v", f, err)
		}
	}

	m.identity = nil
	log.Warn("Server identity deleted")
	return nil
}
