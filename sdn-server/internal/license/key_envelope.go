package license

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
)

const (
	providerContentKeyEnvelopeAlgorithm = "X25519-HKDF-SHA256-AES-256-GCM"
	providerContentKeyEnvelopeContext   = "space-data-network/plugin-module/content-key/v1"
)

type ProviderContentKeyAAD struct {
	ModuleID           string `json:"module_id"`
	Version            string `json:"version"`
	BundleSHA256       string `json:"bundle_sha256"`
	SignerPublicKeyHex string `json:"signer_public_key_hex"`
	ProviderPeerID     string `json:"provider_peer_id"`
}

type ProviderContentKeyEnvelope struct {
	Version               int    `json:"version"`
	Algorithm             string `json:"alg"`
	Context               string `json:"context"`
	ProviderX25519PubKey  string `json:"provider_x25519_pubkey"`
	EphemeralX25519PubKey string `json:"ephemeral_x25519_pubkey"`
	Nonce                 string `json:"nonce"`
	AssociatedData        string `json:"aad"`
	Ciphertext            string `json:"ciphertext"`
}

func WrapProviderContentKey(contentKey, providerPublicKey []byte, aad ProviderContentKeyAAD) (*ProviderContentKeyEnvelope, error) {
	if len(contentKey) != 32 {
		return nil, fmt.Errorf("content key must be 32 bytes, got %d", len(contentKey))
	}
	if len(providerPublicKey) != 32 {
		return nil, fmt.Errorf("provider x25519 public key must be 32 bytes, got %d", len(providerPublicKey))
	}

	aadBytes, err := MarshalProviderContentKeyAAD(aad)
	if err != nil {
		return nil, err
	}

	ephemeralPrivateKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, ephemeralPrivateKey); err != nil {
		return nil, fmt.Errorf("generate ephemeral x25519 private key: %w", err)
	}
	defer ZeroBytes(ephemeralPrivateKey)
	clampX25519PrivateKey(ephemeralPrivateKey)

	ephemeralPublicKey, err := curve25519.X25519(ephemeralPrivateKey, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive ephemeral x25519 public key: %w", err)
	}
	sharedSecret, err := curve25519.X25519(ephemeralPrivateKey, providerPublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive provider shared secret: %w", err)
	}
	defer ZeroBytes(sharedSecret)

	wrapKey, err := deriveHKDFSHA256(sharedSecret, nil, []byte(providerContentKeyEnvelopeContext), 32)
	if err != nil {
		return nil, fmt.Errorf("derive provider content key wrap key: %w", err)
	}
	defer ZeroBytes(wrapKey)

	block, err := aes.NewCipher(wrapKey)
	if err != nil {
		return nil, fmt.Errorf("create content key wrap cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create content key wrap AEAD: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate content key wrap nonce: %w", err)
	}

	return &ProviderContentKeyEnvelope{
		Version:               1,
		Algorithm:             providerContentKeyEnvelopeAlgorithm,
		Context:               providerContentKeyEnvelopeContext,
		ProviderX25519PubKey:  base64.RawURLEncoding.EncodeToString(providerPublicKey),
		EphemeralX25519PubKey: base64.RawURLEncoding.EncodeToString(ephemeralPublicKey),
		Nonce:                 base64.RawURLEncoding.EncodeToString(nonce),
		AssociatedData:        base64.RawURLEncoding.EncodeToString(aadBytes),
		Ciphertext:            base64.RawURLEncoding.EncodeToString(gcm.Seal(nil, nonce, contentKey, aadBytes)),
	}, nil
}

func UnwrapProviderContentKey(envelope *ProviderContentKeyEnvelope, providerPrivateKey []byte, aad ProviderContentKeyAAD) ([]byte, error) {
	if envelope == nil {
		return nil, errors.New("provider content key envelope is required")
	}
	if len(providerPrivateKey) != 32 {
		return nil, fmt.Errorf("provider x25519 private key must be 32 bytes, got %d", len(providerPrivateKey))
	}
	if err := validateProviderContentKeyEnvelope(envelope); err != nil {
		return nil, err
	}

	expectedAAD, err := MarshalProviderContentKeyAAD(aad)
	if err != nil {
		return nil, err
	}
	envelopeAAD, err := decodeBase64URLEncodedField("aad", envelope.AssociatedData, 0)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(envelopeAAD, expectedAAD) {
		return nil, errors.New("provider content key envelope AAD does not match expected metadata")
	}

	providerPublicKey, err := decodeBase64URLEncodedField("provider_x25519_pubkey", envelope.ProviderX25519PubKey, 32)
	if err != nil {
		return nil, err
	}
	expectedProviderPublicKey, err := curve25519.X25519(providerPrivateKey, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive provider x25519 public key: %w", err)
	}
	if !bytes.Equal(providerPublicKey, expectedProviderPublicKey) {
		return nil, errors.New("provider content key envelope does not target provider private key")
	}

	ephemeralPublicKey, err := decodeBase64URLEncodedField("ephemeral_x25519_pubkey", envelope.EphemeralX25519PubKey, 32)
	if err != nil {
		return nil, err
	}
	nonce, err := decodeBase64URLEncodedField("nonce", envelope.Nonce, 12)
	if err != nil {
		return nil, err
	}
	ciphertext, err := decodeBase64URLEncodedField("ciphertext", envelope.Ciphertext, 0)
	if err != nil {
		return nil, err
	}

	sharedSecret, err := curve25519.X25519(providerPrivateKey, ephemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive provider shared secret: %w", err)
	}
	defer ZeroBytes(sharedSecret)

	wrapKey, err := deriveHKDFSHA256(sharedSecret, nil, []byte(providerContentKeyEnvelopeContext), 32)
	if err != nil {
		return nil, fmt.Errorf("derive provider content key wrap key: %w", err)
	}
	defer ZeroBytes(wrapKey)

	block, err := aes.NewCipher(wrapKey)
	if err != nil {
		return nil, fmt.Errorf("create content key unwrap cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create content key unwrap AEAD: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, expectedAAD)
	if err != nil {
		return nil, fmt.Errorf("unwrap provider content key: %w", err)
	}
	if len(plaintext) != 32 {
		ZeroBytes(plaintext)
		return nil, fmt.Errorf("unwrapped content key must be 32 bytes, got %d", len(plaintext))
	}
	return plaintext, nil
}

func MarshalProviderContentKeyAAD(aad ProviderContentKeyAAD) ([]byte, error) {
	if aad.ModuleID == "" {
		return nil, errors.New("module_id is required")
	}
	if aad.Version == "" {
		return nil, errors.New("version is required")
	}
	if aad.BundleSHA256 == "" {
		return nil, errors.New("bundle_sha256 is required")
	}
	if aad.SignerPublicKeyHex == "" {
		return nil, errors.New("signer_public_key_hex is required")
	}
	if aad.ProviderPeerID == "" {
		return nil, errors.New("provider_peer_id is required")
	}
	raw, err := json.Marshal(aad)
	if err != nil {
		return nil, fmt.Errorf("marshal provider content key aad: %w", err)
	}
	return raw, nil
}

func ParseProviderContentKeyEnvelopeJSON(raw []byte) (*ProviderContentKeyEnvelope, error) {
	var envelope ProviderContentKeyEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode provider content key envelope: %w", err)
	}
	if err := validateProviderContentKeyEnvelope(&envelope); err != nil {
		return nil, err
	}
	return &envelope, nil
}

func validateProviderContentKeyEnvelope(envelope *ProviderContentKeyEnvelope) error {
	if envelope == nil {
		return errors.New("provider content key envelope is required")
	}
	if envelope.Version != 1 {
		return fmt.Errorf("unsupported provider content key envelope version %d", envelope.Version)
	}
	if envelope.Algorithm != providerContentKeyEnvelopeAlgorithm {
		return fmt.Errorf("unsupported provider content key envelope algorithm %q", envelope.Algorithm)
	}
	if envelope.Context != providerContentKeyEnvelopeContext {
		return fmt.Errorf("unsupported provider content key envelope context %q", envelope.Context)
	}
	if _, err := decodeBase64URLEncodedField("provider_x25519_pubkey", envelope.ProviderX25519PubKey, 32); err != nil {
		return err
	}
	if _, err := decodeBase64URLEncodedField("ephemeral_x25519_pubkey", envelope.EphemeralX25519PubKey, 32); err != nil {
		return err
	}
	if _, err := decodeBase64URLEncodedField("nonce", envelope.Nonce, 12); err != nil {
		return err
	}
	if _, err := decodeBase64URLEncodedField("aad", envelope.AssociatedData, 0); err != nil {
		return err
	}
	ciphertext, err := decodeBase64URLEncodedField("ciphertext", envelope.Ciphertext, 0)
	if err != nil {
		return err
	}
	if len(ciphertext) == 0 {
		return errors.New("ciphertext is required")
	}
	return nil
}

func decodeBase64URLEncodedField(name, value string, expectedLen int) ([]byte, error) {
	if value == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	if expectedLen > 0 && len(decoded) != expectedLen {
		return nil, fmt.Errorf("%s must decode to %d bytes, got %d", name, expectedLen, len(decoded))
	}
	return decoded, nil
}
