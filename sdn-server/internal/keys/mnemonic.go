// Mnemonic encryption for at-rest storage.
//
// The mnemonic is encrypted with XChaCha20-Poly1305 using a key derived
// from a password via Argon2id.  The password can be explicitly set via
// config/env, or derived deterministically from machine attributes
// (hostname + GOARCH + GOOS) so the server can start unattended.
//
// File format:  salt (32 bytes) || nonce (24 bytes) || ciphertext
// Permissions:  0600

package keys

import (
	"crypto/rand"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	mnemonicSaltSize = 32
	// Argon2id parameters — tuned for server-side key derivation.
	mnemonicArgon2Time    = 3
	mnemonicArgon2Memory  = 64 * 1024 // 64 MiB
	mnemonicArgon2Threads = 4
	mnemonicArgon2KeyLen  = chacha20poly1305.KeySize // 32
)

// EncryptMnemonic encrypts a mnemonic phrase for at-rest storage.
// Returns salt || nonce || ciphertext.
func EncryptMnemonic(mnemonic string, password string) ([]byte, error) {
	salt := make([]byte, mnemonicSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, mnemonicArgon2Time, mnemonicArgon2Memory, mnemonicArgon2Threads, mnemonicArgon2KeyLen)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := aead.Seal(nil, nonce, []byte(mnemonic), nil)

	// salt || nonce || ciphertext
	out := make([]byte, 0, mnemonicSaltSize+chacha20poly1305.NonceSizeX+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// DecryptMnemonic decrypts an encrypted mnemonic produced by EncryptMnemonic.
func DecryptMnemonic(data []byte, password string) (string, error) {
	minLen := mnemonicSaltSize + chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead + 1
	if len(data) < minLen {
		return "", fmt.Errorf("encrypted mnemonic too short (%d bytes, need at least %d)", len(data), minLen)
	}

	salt := data[:mnemonicSaltSize]
	nonce := data[mnemonicSaltSize : mnemonicSaltSize+chacha20poly1305.NonceSizeX]
	ciphertext := data[mnemonicSaltSize+chacha20poly1305.NonceSizeX:]

	key := argon2.IDKey([]byte(password), salt, mnemonicArgon2Time, mnemonicArgon2Memory, mnemonicArgon2Threads, mnemonicArgon2KeyLen)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt mnemonic (wrong password?): %w", err)
	}

	return string(plaintext), nil
}

// IsMnemonicEncrypted checks whether raw file bytes look like an encrypted
// mnemonic (binary) rather than a plaintext BIP-39 phrase (ASCII words).
func IsMnemonicEncrypted(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	// A plaintext BIP-39 mnemonic is entirely printable ASCII (letters + spaces).
	// Encrypted data will contain non-ASCII bytes in the salt/nonce prefix.
	for _, b := range data[:min(16, len(data))] {
		if b < 0x20 || b > 0x7e {
			return true
		}
	}
	return false
}

// machineKeySalt is the fixed Argon2id salt for the hardware-derived password.
// It is a domain-separation constant, not a secret; the per-file random salt in
// EncryptMnemonic provides the actual cryptographic salting.
var machineKeySalt = []byte("sdn-mnemonic-machine-key/v2")

// DeriveDefaultPassword derives a deterministic password from STABLE hardware
// attributes (machine-id, total RAM, CPU model, CPU count, arch, OS) using
// Argon2id, so the server can start unattended without a stored password.
//
// The hostname is deliberately excluded — see hardwareFingerprint. Renaming the
// host must not change this key (that would lock the node out of its own
// encrypted mnemonic); hostname changes are surfaced separately via
// CheckAndUpdateHostnameCanary.
//
// This is NOT a substitute for a strong explicit password (SDN_KEY_PASSWORD);
// it merely prevents trivial offline reads of the mnemonic file if the disk is
// copied off the machine.
func DeriveDefaultPassword() string {
	derived := argon2.IDKey([]byte(hardwareFingerprint()), machineKeySalt, 1, 64*1024, 4, 32)
	return string(derived)
}

// DeriveLegacyPassword reproduces the pre-v2 hostname-based derivation. It is
// retained only so nodes whose mnemonic was encrypted under the old scheme can
// be transparently migrated to the hardware-derived key on first start after
// upgrade (decrypt with legacy → re-encrypt with DeriveDefaultPassword).
func DeriveLegacyPassword() string {
	hostname, _ := os.Hostname()
	homeDir, _ := os.UserHomeDir()
	input := fmt.Sprintf("%s:%s:%s", hostname, runtime.GOARCH, runtime.GOOS)
	salt := []byte(homeDir)
	derived := argon2.IDKey([]byte(input), salt, 1, 64*1024, 4, 32)
	return string(derived)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
