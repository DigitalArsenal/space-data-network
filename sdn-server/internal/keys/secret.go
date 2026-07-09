// Generic at-rest secret encryption, sharing the exact Argon2id +
// XChaCha20-Poly1305 construction and tuned parameters used for mnemonic
// encryption (see mnemonic.go). Callers that need to persist other sensitive
// byte material (e.g. a libp2p node identity private key) should use this
// instead of inventing a new crypto construction.
//
// File format:  salt (32 bytes) || nonce (24 bytes) || ciphertext
// Permissions:  caller-defined (0600 recommended)

package keys

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// EncryptSecret encrypts arbitrary secret bytes for at-rest storage using the
// same Argon2id-derived XChaCha20-Poly1305 envelope as EncryptMnemonic.
// Returns salt || nonce || ciphertext.
func EncryptSecret(secret []byte, password string) ([]byte, error) {
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

	ciphertext := aead.Seal(nil, nonce, secret, nil)

	out := make([]byte, 0, mnemonicSaltSize+chacha20poly1305.NonceSizeX+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// DecryptSecret decrypts data produced by EncryptSecret. It fails closed: any
// corruption, truncation, or wrong password returns an error rather than
// partial or zeroed output.
func DecryptSecret(data []byte, password string) ([]byte, error) {
	minLen := mnemonicSaltSize + chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead
	if len(data) < minLen {
		return nil, fmt.Errorf("encrypted secret too short (%d bytes, need at least %d)", len(data), minLen)
	}

	salt := data[:mnemonicSaltSize]
	nonce := data[mnemonicSaltSize : mnemonicSaltSize+chacha20poly1305.NonceSizeX]
	ciphertext := data[mnemonicSaltSize+chacha20poly1305.NonceSizeX:]

	key := argon2.IDKey([]byte(password), salt, mnemonicArgon2Time, mnemonicArgon2Memory, mnemonicArgon2Threads, mnemonicArgon2KeyLen)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret (wrong password or corrupted data): %w", err)
	}

	return plaintext, nil
}
