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
// and X25519 encryption key pairs.
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

	// Save keys to disk
	if err := m.saveKey(SigningPrivateKeyFile, signingPriv); err != nil {
		return nil, fmt.Errorf("failed to save signing private key: %w", err)
	}
	if err := m.saveKey(SigningPublicKeyFile, signingPub); err != nil {
		return nil, fmt.Errorf("failed to save signing public key: %w", err)
	}
	if err := m.saveKey(EncryptionPrivateKeyFile, encryptionPriv[:]); err != nil {
		return nil, fmt.Errorf("failed to save encryption private key: %w", err)
	}
	if err := m.saveKey(EncryptionPublicKeyFile, encryptionPub[:]); err != nil {
		return nil, fmt.Errorf("failed to save encryption public key: %w", err)
	}

	m.identity = &Identity{
		SigningKey: &KeyPair{
			PublicKey:  signingPub,
			PrivateKey: signingPriv,
			KeyType:    "Ed25519",
		},
		EncryptionKey: &KeyPair{
			PublicKey:  encryptionPub[:],
			PrivateKey: encryptionPriv[:],
			KeyType:    "X25519",
		},
	}

	log.Info("Generated new server identity")
	log.Infof("Signing public key: %s", hex.EncodeToString(signingPub))
	log.Infof("Encryption public key: %s", hex.EncodeToString(encryptionPub[:]))

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
