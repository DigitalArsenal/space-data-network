package license

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/curve25519"
)

// secp256k1KeyEnvelopeAlgorithm labels a plugin key envelope wrapped with
// secp256k1 ECIES (ephemeral secp256k1 ECDH + HKDF-SHA256 + AES-256-GCM). The
// Algorithm field discriminates it from the X25519 default at unwrap time.
const secp256k1KeyEnvelopeAlgorithm = "secp256k1+SHA256+AES-256-GCM"

// BuildPluginKeyEnvelopeSecp256k1 wraps plugin key material to a recipient's
// secp256k1 public key — the secp256k1 ECIES counterpart of BuildPluginKeyEnvelope
// (X25519). X25519 stays the operational default; this path is selected when the
// recipient advertises a secp256k1 encryption key (WS2b). The wrap key is
// derived identically (derivePluginWrapKey / HKDF-SHA256), so only the ECDH
// differs.
func BuildPluginKeyEnvelopeSecp256k1(asset *PluginAsset, pluginKey, clientSecp256k1Pub []byte, claims *CapabilityClaims, issuer string, now time.Time) (*PluginKeyEnvelope, error) {
	if asset == nil {
		return nil, errors.New("plugin asset is required")
	}
	if len(pluginKey) != 32 {
		return nil, fmt.Errorf("plugin key must be 32 bytes, got %d", len(pluginKey))
	}
	recipientPub, err := secp256k1.ParsePubKey(clientSecp256k1Pub)
	if err != nil {
		return nil, fmt.Errorf("parse recipient secp256k1 public key: %w", err)
	}
	if claims == nil {
		return nil, errors.New("capability claims are required")
	}
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		issuer = "spaceaware-license"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	exp := now.Unix() + defaultKeyEnvelopeLifetimeSec
	if claims.Exp > 0 && claims.Exp < exp {
		exp = claims.Exp
	}
	if exp <= now.Unix() {
		return nil, errors.New("capability token already expired")
	}

	ephemeralPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral secp256k1 key: %w", err)
	}
	ephemeralPub := ephemeralPriv.PubKey().SerializeCompressed()
	sharedSecret := secp256k1.GenerateSharedSecret(ephemeralPriv, recipientPub)
	defer zeroBytes(sharedSecret)

	aad := buildPluginEnvelopeAAD(asset, claims, issuer, exp)
	wrapKey := derivePluginWrapKey(sharedSecret, aad)
	block, err := aes.NewCipher(wrapKey[:])
	if err != nil {
		return nil, fmt.Errorf("create key-wrap cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create key-wrap AEAD: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate key-wrap nonce: %w", err)
	}

	payload := pluginKeyEnvelopePayload{
		Key:           base64.RawStdEncoding.EncodeToString(pluginKey),
		PluginID:      asset.ID,
		Version:       asset.Version,
		RequiredScope: asset.RequiredScope,
		BundleSHA256:  asset.BundleSHA256,
		Sub:           claims.Sub,
		PeerID:        claims.PeerID,
		JTI:           claims.JTI,
		Exp:           exp,
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope payload: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, []byte(aad))

	return &PluginKeyEnvelope{
		PluginID:           asset.ID,
		Version:            asset.Version,
		RequiredScope:      asset.RequiredScope,
		BundleSHA256:       asset.BundleSHA256,
		Algorithm:          secp256k1KeyEnvelopeAlgorithm,
		ServerX25519PubKey: base64.RawStdEncoding.EncodeToString(ephemeralPub), // ephemeral secp256k1 pub for this algorithm
		Nonce:              base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext:         base64.RawStdEncoding.EncodeToString(ciphertext),
		AssociatedData:     aad,
		Issuer:             issuer,
		Subject:            claims.Sub,
		PeerID:             claims.PeerID,
		CapabilityTokenJTI: claims.JTI,
		ExpiresAt:          exp,
	}, nil
}

// OpenPluginKeyEnvelope unwraps a plugin key envelope with the recipient's
// private key, dispatching on the envelope Algorithm (X25519 default or
// secp256k1), and returns the 32-byte plugin content key. Production clients
// unwrap inside the client-decrypt WASM module; this Go path backs tests and a
// future native client, and proves the wrap round-trips on both curves.
func OpenPluginKeyEnvelope(envelope *PluginKeyEnvelope, recipientPrivateKey []byte) ([]byte, error) {
	if envelope == nil {
		return nil, errors.New("envelope is required")
	}
	ephemeralPub, err := base64.RawStdEncoding.DecodeString(envelope.ServerX25519PubKey)
	if err != nil {
		return nil, fmt.Errorf("decode ephemeral public key: %w", err)
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}

	var sharedSecret []byte
	switch envelope.Algorithm {
	case secp256k1KeyEnvelopeAlgorithm:
		if len(recipientPrivateKey) != 32 {
			return nil, fmt.Errorf("secp256k1 private key must be 32 bytes, got %d", len(recipientPrivateKey))
		}
		recipientPriv := secp256k1.PrivKeyFromBytes(recipientPrivateKey)
		pub, perr := secp256k1.ParsePubKey(ephemeralPub)
		if perr != nil {
			return nil, fmt.Errorf("parse ephemeral secp256k1 pub: %w", perr)
		}
		sharedSecret = secp256k1.GenerateSharedSecret(recipientPriv, pub)
	case defaultKeyEnvelopeAlgorithm, "":
		if len(recipientPrivateKey) != 32 {
			return nil, fmt.Errorf("x25519 private key must be 32 bytes, got %d", len(recipientPrivateKey))
		}
		ss, xerr := curve25519.X25519(recipientPrivateKey, ephemeralPub)
		if xerr != nil {
			return nil, fmt.Errorf("x25519 shared secret: %w", xerr)
		}
		sharedSecret = ss
	default:
		return nil, fmt.Errorf("unsupported key envelope algorithm %q", envelope.Algorithm)
	}
	defer zeroBytes(sharedSecret)

	wrapKey := derivePluginWrapKey(sharedSecret, envelope.AssociatedData)
	block, err := aes.NewCipher(wrapKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(envelope.AssociatedData))
	if err != nil {
		return nil, fmt.Errorf("open key envelope: %w", err)
	}

	var payload pluginKeyEnvelopePayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal envelope payload: %w", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(payload.Key)
	if err != nil {
		return nil, fmt.Errorf("decode plugin key: %w", err)
	}
	return key, nil
}
