package tor

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// KeyBundle holds the Tor hidden service keys for a cluster.
type KeyBundle struct {
	// SecretKey is the 64-byte Ed25519 private key for the hidden service.
	SecretKey ed25519.PrivateKey `json:"secret_key"`
	// PublicKey is the 32-byte Ed25519 public key.
	PublicKey ed25519.PublicKey `json:"public_key"`
	// OnionHost is the v3 .onion hostname (56 chars + ".onion").
	OnionHost string `json:"onion_host"`
	// CreatedBy is the PeerID of the node that originated the bundle.
	CreatedBy string `json:"created_by"`
	// ClusterID identifies the cluster this bundle belongs to.
	ClusterID string `json:"cluster_id"`
	// Version is monotonically increasing; nodes reject stale bundles.
	Version uint64 `json:"version"`
}

// Validate checks that the key bundle is internally consistent.
func (kb *KeyBundle) Validate() error {
	if len(kb.SecretKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid secret key size: %d", len(kb.SecretKey))
	}
	if len(kb.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key size: %d", len(kb.PublicKey))
	}

	// Verify the public key matches the private key.
	derivedPub := kb.SecretKey.Public().(ed25519.PublicKey)
	if !derivedPub.Equal(kb.PublicKey) {
		return errors.New("public key does not match secret key")
	}

	// Verify the onion host matches the public key.
	expectedHost, err := onionAddressFromPublicKey(kb.PublicKey)
	if err != nil {
		return fmt.Errorf("derive onion address: %w", err)
	}
	if expectedHost != kb.OnionHost {
		return fmt.Errorf("onion host mismatch: bundle has %q, derived %q", kb.OnionHost, expectedHost)
	}

	if kb.ClusterID == "" {
		return errors.New("cluster_id is required")
	}

	return nil
}

// DeriveClusterSeed produces a deterministic 32-byte seed from a shared
// cluster secret. All nodes with the same secret derive the same seed,
// and therefore the same Ed25519 keypair and .onion address.
func DeriveClusterSeed(clusterSecret []byte) [32]byte {
	return deriveHiddenServiceSeed(append([]byte("sdn-tor-cluster-v1\x00"), clusterSecret...))
}

// NewKeyBundleFromClusterSecret creates a KeyBundle deterministically
// from a shared cluster secret.
func NewKeyBundleFromClusterSecret(clusterSecret []byte, clusterID, createdBy string) (*KeyBundle, error) {
	seed := DeriveClusterSeed(clusterSecret)
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)

	host, err := onionAddressFromPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("derive onion address: %w", err)
	}

	return &KeyBundle{
		SecretKey: priv,
		PublicKey: pub,
		OnionHost: host,
		CreatedBy: createdBy,
		ClusterID: clusterID,
		Version:   1,
	}, nil
}

// deriveKeyshareKey derives a 32-byte symmetric key for encrypting key bundles.
func deriveKeyshareKey(clusterSecret []byte, clusterID string) ([]byte, error) {
	salt := sha256.Sum256([]byte("sdn-tor-keyshare-v1\x00" + clusterID))
	hkdfReader := hkdf.New(sha256.New, clusterSecret, salt[:], []byte("sdn-tor-keyshare"))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, fmt.Errorf("HKDF expand: %w", err)
	}
	return key, nil
}

// EncryptKeyBundle encrypts a KeyBundle using XChaCha20-Poly1305 with a key
// derived from the cluster secret. The result is safe to store on untrusted
// storage (IPFS, disk, etc.).
func EncryptKeyBundle(bundle *KeyBundle, clusterSecret []byte) ([]byte, error) {
	if err := bundle.Validate(); err != nil {
		return nil, fmt.Errorf("invalid bundle: %w", err)
	}

	key, err := deriveKeyshareKey(clusterSecret, bundle.ClusterID)
	if err != nil {
		return nil, err
	}

	plaintext, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("marshal bundle: %w", err)
	}

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create XChaCha20-Poly1305: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// Format: [4-byte version][24-byte nonce][ciphertext+tag]
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 1) // envelope version 1

	ciphertext := aead.Seal(nil, nonce, plaintext, header[:])

	out := make([]byte, 0, len(header)+len(nonce)+len(ciphertext))
	out = append(out, header[:]...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// DecryptKeyBundle decrypts an encrypted key bundle using the cluster secret.
func DecryptKeyBundle(encrypted []byte, clusterSecret []byte, clusterID string) (*KeyBundle, error) {
	if len(encrypted) < 4 {
		return nil, errors.New("encrypted bundle too short")
	}

	version := binary.BigEndian.Uint32(encrypted[:4])
	if version != 1 {
		return nil, fmt.Errorf("unsupported envelope version: %d", version)
	}

	key, err := deriveKeyshareKey(clusterSecret, clusterID)
	if err != nil {
		return nil, err
	}

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create XChaCha20-Poly1305: %w", err)
	}

	nonceSize := aead.NonceSize()
	if len(encrypted) < 4+nonceSize {
		return nil, errors.New("encrypted bundle too short for nonce")
	}

	nonce := encrypted[4 : 4+nonceSize]
	ciphertext := encrypted[4+nonceSize:]

	plaintext, err := aead.Open(nil, nonce, ciphertext, encrypted[:4])
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	var bundle KeyBundle
	if err := json.Unmarshal(plaintext, &bundle); err != nil {
		return nil, fmt.Errorf("unmarshal bundle: %w", err)
	}

	if err := bundle.Validate(); err != nil {
		return nil, fmt.Errorf("validate decrypted bundle: %w", err)
	}

	if bundle.ClusterID != clusterID {
		return nil, fmt.Errorf("cluster ID mismatch: bundle has %q, expected %q", bundle.ClusterID, clusterID)
	}

	return &bundle, nil
}

// SaveKeyBundleToDir writes the Tor hidden service key files to a directory
// so that Tor can load them. Also caches the encrypted bundle for re-distribution.
func SaveKeyBundleToDir(dir string, bundle *KeyBundle) error {
	if err := bundle.Validate(); err != nil {
		return fmt.Errorf("invalid bundle: %w", err)
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}

	if err := writeHiddenServiceKeyFiles(dir, bundle.SecretKey, bundle.PublicKey); err != nil {
		return err
	}

	// Also write the hostname file for convenience.
	return os.WriteFile(filepath.Join(dir, "hostname"), []byte(bundle.OnionHost+"\n"), 0600)
}

// LoadKeyBundleFromDir reads cached key files from a directory and
// reconstructs a KeyBundle.
func LoadKeyBundleFromDir(dir, clusterID, createdBy string) (*KeyBundle, error) {
	secretPath := filepath.Join(dir, "hs_ed25519_secret_key")
	secretData, err := os.ReadFile(secretPath)
	if err != nil {
		return nil, fmt.Errorf("read secret key: %w", err)
	}

	// Tor key files have a 32-byte header.
	if len(secretData) < 32+ed25519.PrivateKeySize {
		return nil, fmt.Errorf("secret key file too short: %d bytes", len(secretData))
	}
	priv := ed25519.PrivateKey(secretData[32:])
	pub := priv.Public().(ed25519.PublicKey)

	host, err := onionAddressFromPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("derive onion address: %w", err)
	}

	return &KeyBundle{
		SecretKey: priv,
		PublicKey: pub,
		OnionHost: host,
		CreatedBy: createdBy,
		ClusterID: clusterID,
		Version:   0, // unknown version from disk
	}, nil
}
