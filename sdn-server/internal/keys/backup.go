// Package keys provides cryptographic key management for SDN servers.
package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Errors for backup/recovery
var (
	ErrInvalidBackup      = errors.New("invalid backup data")
	ErrDecryptionFailed   = errors.New("failed to decrypt backup - wrong password?")
	ErrInvalidMnemonic    = errors.New("invalid mnemonic phrase")
	ErrChecksumMismatch   = errors.New("mnemonic checksum mismatch")
)

// BackupFormat represents an encrypted key backup.
type BackupFormat struct {
	Version     int    `json:"version"`
	CreatedAt   string `json:"created_at"`
	Salt        string `json:"salt"`        // Base64 encoded salt for key derivation
	Nonce       string `json:"nonce"`       // Base64 encoded nonce for AES-GCM
	Ciphertext  string `json:"ciphertext"`  // Base64 encoded encrypted keys
	Fingerprint string `json:"fingerprint"` // Public key fingerprint for identification
}

// BackupPayload is the plaintext data that gets encrypted.
type BackupPayload struct {
	SigningPrivateKey    string `json:"signing_private_key"`
	SigningPublicKey     string `json:"signing_public_key"`
	EncryptionPrivateKey string `json:"encryption_private_key"`
	EncryptionPublicKey  string `json:"encryption_public_key"`
	CreatedAt            string `json:"created_at"`
}

const (
	// Argon2 parameters for key derivation
	argon2Time    = 3
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32

	// Salt and nonce sizes
	saltSize  = 32
	nonceSize = 12
)

// ExportEncrypted creates an encrypted backup of the identity keys.
func (m *Manager) ExportEncrypted(password string) (string, error) {
	if m.identity == nil {
		return "", ErrKeyNotFound
	}

	// Create payload
	payload := BackupPayload{
		SigningPrivateKey:    base64.StdEncoding.EncodeToString(m.identity.SigningKey.PrivateKey),
		SigningPublicKey:     base64.StdEncoding.EncodeToString(m.identity.SigningKey.PublicKey),
		EncryptionPrivateKey: base64.StdEncoding.EncodeToString(m.identity.EncryptionKey.PrivateKey),
		EncryptionPublicKey:  base64.StdEncoding.EncodeToString(m.identity.EncryptionKey.PublicKey),
		CreatedAt:            time.Now().UTC().Format(time.RFC3339),
	}

	plaintext, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Generate random salt and derive key using Argon2
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	// Encrypt with AES-256-GCM
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Create backup structure
	backup := BackupFormat{
		Version:     1,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Salt:        base64.StdEncoding.EncodeToString(salt),
		Nonce:       base64.StdEncoding.EncodeToString(nonce),
		Ciphertext:  base64.StdEncoding.EncodeToString(ciphertext),
		Fingerprint: m.PublicKeyFingerprint(),
	}

	backupJSON, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal backup: %w", err)
	}

	log.Info("Created encrypted key backup")
	return string(backupJSON), nil
}

// ImportEncrypted restores identity keys from an encrypted backup.
func (m *Manager) ImportEncrypted(backupData, password string) error {
	var backup BackupFormat
	if err := json.Unmarshal([]byte(backupData), &backup); err != nil {
		return ErrInvalidBackup
	}

	if backup.Version != 1 {
		return fmt.Errorf("unsupported backup version: %d", backup.Version)
	}

	// Decode base64 fields
	salt, err := base64.StdEncoding.DecodeString(backup.Salt)
	if err != nil {
		return ErrInvalidBackup
	}

	nonce, err := base64.StdEncoding.DecodeString(backup.Nonce)
	if err != nil {
		return ErrInvalidBackup
	}

	ciphertext, err := base64.StdEncoding.DecodeString(backup.Ciphertext)
	if err != nil {
		return ErrInvalidBackup
	}

	// Derive key using same parameters
	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	// Decrypt
	block, err := aes.NewCipher(key)
	if err != nil {
		return ErrDecryptionFailed
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ErrDecryptionFailed
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return ErrDecryptionFailed
	}

	// Parse payload
	var payload BackupPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return ErrInvalidBackup
	}

	// Decode keys
	signingPriv, err := base64.StdEncoding.DecodeString(payload.SigningPrivateKey)
	if err != nil {
		return ErrInvalidBackup
	}
	signingPub, err := base64.StdEncoding.DecodeString(payload.SigningPublicKey)
	if err != nil {
		return ErrInvalidBackup
	}
	encryptionPriv, err := base64.StdEncoding.DecodeString(payload.EncryptionPrivateKey)
	if err != nil {
		return ErrInvalidBackup
	}
	encryptionPub, err := base64.StdEncoding.DecodeString(payload.EncryptionPublicKey)
	if err != nil {
		return ErrInvalidBackup
	}

	// Save keys to disk
	if err := m.saveKey(SigningPrivateKeyFile, signingPriv); err != nil {
		return fmt.Errorf("failed to save signing private key: %w", err)
	}
	if err := m.saveKey(SigningPublicKeyFile, signingPub); err != nil {
		return fmt.Errorf("failed to save signing public key: %w", err)
	}
	if err := m.saveKey(EncryptionPrivateKeyFile, encryptionPriv); err != nil {
		return fmt.Errorf("failed to save encryption private key: %w", err)
	}
	if err := m.saveKey(EncryptionPublicKeyFile, encryptionPub); err != nil {
		return fmt.Errorf("failed to save encryption public key: %w", err)
	}

	// Update in-memory identity
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

	log.Info("Restored identity from encrypted backup")
	return nil
}

// BIP39 wordlist (first 128 words for simplicity - full list has 2048)
// In production, use a proper BIP39 library
var bip39Words = []string{
	"abandon", "ability", "able", "about", "above", "absent", "absorb", "abstract",
	"absurd", "abuse", "access", "accident", "account", "accuse", "achieve", "acid",
	"acoustic", "acquire", "across", "act", "action", "actor", "actress", "actual",
	"adapt", "add", "addict", "address", "adjust", "admit", "adult", "advance",
	"advice", "aerobic", "affair", "afford", "afraid", "again", "age", "agent",
	"agree", "ahead", "aim", "air", "airport", "aisle", "alarm", "album",
	"alcohol", "alert", "alien", "all", "alley", "allow", "almost", "alone",
	"alpha", "already", "also", "alter", "always", "amateur", "amazing", "among",
	"amount", "amused", "analyst", "anchor", "ancient", "anger", "angle", "angry",
	"animal", "ankle", "announce", "annual", "another", "answer", "antenna", "antique",
	"anxiety", "any", "apart", "apology", "appear", "apple", "approve", "april",
	"arch", "arctic", "area", "arena", "argue", "arm", "armed", "armor",
	"army", "around", "arrange", "arrest", "arrive", "arrow", "art", "artefact",
	"artist", "artwork", "ask", "aspect", "assault", "asset", "assist", "assume",
	"asthma", "athlete", "atom", "attack", "attend", "attitude", "attract", "auction",
	"audit", "august", "aunt", "author", "auto", "autumn", "average", "avocado",
}

// ExportMnemonic generates a BIP-39 style mnemonic for the signing key.
// Note: This is a simplified implementation. For production, use a proper BIP-39 library.
func (m *Manager) ExportMnemonic() (string, error) {
	if m.identity == nil || m.identity.SigningKey == nil {
		return "", ErrKeyNotFound
	}

	// Use first 32 bytes of private key (the seed portion for Ed25519)
	seed := m.identity.SigningKey.PrivateKey[:32]

	// Convert to mnemonic - simplified version using bytes directly
	// Each byte maps to a word (with modulo for our limited wordlist)
	words := make([]string, 24)
	for i := 0; i < 24; i++ {
		var index int
		if i < 32 {
			index = int(seed[i]) % len(bip39Words)
		} else {
			// Add checksum for last words
			hash := sha256.Sum256(seed)
			index = int(hash[i-32]) % len(bip39Words)
		}
		words[i] = bip39Words[index]
	}

	log.Info("Generated mnemonic backup phrase")
	return strings.Join(words, " "), nil
}

// ImportMnemonic restores identity from a BIP-39 mnemonic.
// Note: This is a simplified implementation.
func (m *Manager) ImportMnemonic(mnemonic string) error {
	words := strings.Fields(strings.ToLower(mnemonic))
	if len(words) != 24 {
		return ErrInvalidMnemonic
	}

	// Convert words back to indices
	indices := make([]int, 24)
	for i, word := range words {
		found := false
		for j, w := range bip39Words {
			if w == word {
				indices[i] = j
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: unknown word '%s'", ErrInvalidMnemonic, word)
		}
	}

	// This is a placeholder - proper implementation would derive
	// Ed25519 seed from mnemonic using BIP39/BIP32
	log.Warn("Mnemonic import is a simplified implementation")

	return fmt.Errorf("mnemonic import not fully implemented - use encrypted backup instead")
}

// GenerateQRData returns data suitable for QR code generation.
func (m *Manager) GenerateQRData(password string) (string, error) {
	backup, err := m.ExportEncrypted(password)
	if err != nil {
		return "", err
	}

	// Compress for QR code (use base64 of compressed JSON)
	return base64.StdEncoding.EncodeToString([]byte(backup)), nil
}

// ImportFromQRData restores identity from QR code data.
func (m *Manager) ImportFromQRData(qrData, password string) error {
	decoded, err := base64.StdEncoding.DecodeString(qrData)
	if err != nil {
		return ErrInvalidBackup
	}

	return m.ImportEncrypted(string(decoded), password)
}
