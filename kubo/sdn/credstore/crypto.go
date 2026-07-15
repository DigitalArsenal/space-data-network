// Generic at-rest secret encryption for the credential keystore.
//
// This is a self-contained port of the sdn-server internal/keys secret
// envelope (EncryptSecret/DecryptSecret): the exact Argon2id +
// XChaCha20-Poly1305 construction and tuned parameters the sdn-server node uses
// for its mnemonic and libp2p-identity-key at-rest encryption. It is
// re-implemented here (rather than imported from sdn-server) so the kubo module
// tree carries NO dependency on sdn-server internals — the same rule the caps
// JSON envelope (sdnservices/capsjson.go) already follows.
//
// This introduces NO new cryptographic construction; it composes standard
// primitives from golang.org/x/crypto.
//
//	File format:  salt (32 bytes) || nonce (24 bytes) || ciphertext
//	Permissions:  caller-defined (0600 for the keystore)

package credstore

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	secretSaltSize = 32
	// Argon2id parameters — identical to sdn-server's tuned server-side KDF
	// (internal/keys mnemonic.go). Changing any of these orphans an existing
	// credentials.enc, so they are pinned.
	secretArgon2Time    = 3
	secretArgon2Memory  = 64 * 1024 // 64 MiB
	secretArgon2Threads = 4
	secretArgon2KeyLen  = chacha20poly1305.KeySize // 32
)

// encryptSecret encrypts arbitrary secret bytes for at-rest storage using an
// Argon2id-derived XChaCha20-Poly1305 envelope. A fresh random salt and nonce
// are drawn per call, so the same plaintext never yields the same ciphertext
// twice. Returns salt || nonce || ciphertext.
func encryptSecret(secret []byte, password string) ([]byte, error) {
	salt := make([]byte, secretSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, secretArgon2Time, secretArgon2Memory, secretArgon2Threads, secretArgon2KeyLen)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := aead.Seal(nil, nonce, secret, nil)

	out := make([]byte, 0, secretSaltSize+chacha20poly1305.NonceSizeX+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// decryptSecret decrypts data produced by encryptSecret. It fails closed: any
// corruption, truncation, or wrong password returns an error rather than
// partial or zeroed output.
func decryptSecret(data []byte, password string) ([]byte, error) {
	minLen := secretSaltSize + chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead
	if len(data) < minLen {
		return nil, fmt.Errorf("encrypted secret too short (%d bytes, need at least %d)", len(data), minLen)
	}

	salt := data[:secretSaltSize]
	nonce := data[secretSaltSize : secretSaltSize+chacha20poly1305.NonceSizeX]
	ciphertext := data[secretSaltSize+chacha20poly1305.NonceSizeX:]

	key := argon2.IDKey([]byte(password), salt, secretArgon2Time, secretArgon2Memory, secretArgon2Threads, secretArgon2KeyLen)

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
