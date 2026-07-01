package deliveryclient

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
)

// aesGCMIVSize is the 12-byte IV/nonce the module-delivery bundle format prepends
// to the AES-256-GCM ciphertext (matching sdn-js decryptEncryptedModuleBundle).
const aesGCMIVSize = 12

// contentKeySize is the AES-256 content-key length.
const contentKeySize = 32

// ErrEncryptedContentKeyRequiresClientDecrypt is returned when a grant's content
// key is delivered as an encrypted ENC envelope rather than a plaintext KMF: the
// unwrap must go through the client-decrypt WASM module (the SDK delegates the
// same way), which the caller supplies via ClientDecrypt.
var ErrEncryptedContentKeyRequiresClientDecrypt = errors.New(
	"deliveryclient: encrypted content-key envelope requires the client-decrypt WASM module")

// AESGCMBundleDecryptor decrypts a module bundle laid out as
// [12-byte IV][ciphertext||16-byte GCM tag] with a 32-byte AES-256 content key.
// AES-256-GCM is a standard primitive, so this interoperates by construction
// with the sdn-js WebCrypto path (aesGcmDecryptWithIv) for the same inputs.
type AESGCMBundleDecryptor struct {
	// AAD is optional associated data bound into the GCM tag; must match what
	// the producer sealed with.
	AAD []byte
}

// Decrypt implements BundleDecryptor.
func (d AESGCMBundleDecryptor) Decrypt(encryptedBundle, contentKey []byte) ([]byte, error) {
	if len(contentKey) != contentKeySize {
		return nil, fmt.Errorf("deliveryclient: content key must be %d bytes, got %d", contentKeySize, len(contentKey))
	}
	if len(encryptedBundle) <= aesGCMIVSize+16 {
		return nil, errors.New("deliveryclient: encrypted bundle too short for iv + tag")
	}
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: gcm: %w", err)
	}
	if gcm.NonceSize() != aesGCMIVSize {
		return nil, fmt.Errorf("deliveryclient: unexpected gcm nonce size %d", gcm.NonceSize())
	}
	iv := encryptedBundle[:aesGCMIVSize]
	ciphertext := encryptedBundle[aesGCMIVSize:]
	plaintext, err := gcm.Open(nil, iv, ciphertext, d.AAD)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: aes-gcm open: %w", err)
	}
	return plaintext, nil
}

// EncryptBundleAESGCM seals plaintext into the module-delivery bundle format
// ([12-byte iv][ciphertext||tag]) with a 32-byte content key. It is the inverse
// of AESGCMBundleDecryptor.Decrypt — used by tests and by producers (a
// data-source module protecting a bundle it publishes).
func EncryptBundleAESGCM(contentKey, iv, plaintext, aad []byte) ([]byte, error) {
	if len(contentKey) != contentKeySize {
		return nil, fmt.Errorf("deliveryclient: content key must be %d bytes, got %d", contentKeySize, len(contentKey))
	}
	if len(iv) != aesGCMIVSize {
		return nil, fmt.Errorf("deliveryclient: iv must be %d bytes, got %d", aesGCMIVSize, len(iv))
	}
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: gcm: %w", err)
	}
	ciphertext := gcm.Seal(nil, iv, plaintext, aad)
	out := make([]byte, 0, len(iv)+len(ciphertext))
	out = append(out, iv...)
	out = append(out, ciphertext...)
	return out, nil
}

// PlaintextKMFUnwrapper recovers a content key from a grant whose wrapped
// content-key payload is a plaintext $KMF FlatBuffer (the unencrypted delivery
// path). It mirrors the sdn-js decodeKmfKeyBytes branch of unwrapGrantContentKey.
type PlaintextKMFUnwrapper struct{}

// Unwrap implements ContentKeyUnwrapper for plaintext-KMF grants.
func (PlaintextKMFUnwrapper) Unwrap(g *Grant) ([]byte, error) {
	if g == nil || len(g.WrappedContentKeyPayload) == 0 {
		return nil, errors.New("deliveryclient: grant has no wrapped content-key payload")
	}
	if !kmf.KMFBufferHasIdentifier(g.WrappedContentKeyPayload) {
		return nil, ErrEncryptedContentKeyRequiresClientDecrypt
	}
	root := kmf.GetRootAsKMF(g.WrappedContentKeyPayload, 0)
	key := root.KeyBytesBytes()
	if len(key) == 0 {
		return nil, errors.New("deliveryclient: KMF key bytes missing")
	}
	return append([]byte(nil), key...), nil
}

// ClientDecrypt is the client-decrypt WASM module contract (mirrors the sdn-js
// clientDecrypt.decryptArtifact): given the full grant response bytes and the
// recipient's private key, it returns the recovered content key. The concrete
// implementation is a WASM adapter wired at the node level; this package
// consumes only the interface so the encrypted path is testable in isolation.
type ClientDecrypt interface {
	DecryptArtifact(grantResponseBytes, recipientPrivateKey []byte) (contentKey []byte, err error)
}

// ClientDecryptUnwrapper delegates the encrypted-envelope content-key unwrap to
// a ClientDecrypt (the WASM module), carrying the grant bytes and recipient key
// the module needs.
type ClientDecryptUnwrapper struct {
	Client              ClientDecrypt
	GrantResponseBytes  []byte
	RecipientPrivateKey []byte
}

// Unwrap implements ContentKeyUnwrapper by delegating to the client-decrypt WASM.
func (u ClientDecryptUnwrapper) Unwrap(_ *Grant) ([]byte, error) {
	if u.Client == nil {
		return nil, errors.New("deliveryclient: client-decrypt module is required for encrypted grants")
	}
	if len(u.GrantResponseBytes) == 0 {
		return nil, errors.New("deliveryclient: grant response bytes are required for client-decrypt")
	}
	key, err := u.Client.DecryptArtifact(u.GrantResponseBytes, u.RecipientPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("deliveryclient: client-decrypt: %w", err)
	}
	if len(key) == 0 {
		return nil, errors.New("deliveryclient: client-decrypt returned empty content key")
	}
	return key, nil
}

// ContentKeyIsPlaintextKMF reports whether the grant's content key is delivered
// as a plaintext KMF (decodable natively) rather than an encrypted envelope.
func (g *Grant) ContentKeyIsPlaintextKMF() bool {
	if g == nil || len(g.WrappedContentKeyPayload) == 0 {
		return false
	}
	if g.WrappedContentKeyRootType == "KMF" || g.WrappedContentKeyRootType == "" {
		return kmf.KMFBufferHasIdentifier(g.WrappedContentKeyPayload)
	}
	return false
}

// UnwrapContentKey selects the correct unwrap path for a grant: a plaintext KMF
// payload is decoded natively; an encrypted envelope is delegated to the
// client-decrypt WASM module (client must be non-nil in that case).
func UnwrapContentKey(grant *Grant, grantResponseBytes, recipientPrivateKey []byte, client ClientDecrypt) ([]byte, error) {
	if grant == nil {
		return nil, errors.New("deliveryclient: nil grant")
	}
	if grant.ContentKeyIsPlaintextKMF() {
		return PlaintextKMFUnwrapper{}.Unwrap(grant)
	}
	if client == nil {
		return nil, ErrEncryptedContentKeyRequiresClientDecrypt
	}
	return ClientDecryptUnwrapper{
		Client:              client,
		GrantResponseBytes:  grantResponseBytes,
		RecipientPrivateKey: recipientPrivateKey,
	}.Unwrap(grant)
}
