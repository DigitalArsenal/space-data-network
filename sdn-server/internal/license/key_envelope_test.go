package license

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func TestProviderContentKeyEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	providerPriv, providerPub := testX25519KeyPair(t)
	contentKey := bytes.Repeat([]byte{0x42}, 32)
	aad := testProviderContentKeyAAD()

	envelope, err := WrapProviderContentKey(contentKey, providerPub, aad)
	if err != nil {
		t.Fatalf("WrapProviderContentKey failed: %v", err)
	}

	unwrapped, err := UnwrapProviderContentKey(envelope, providerPriv, aad)
	if err != nil {
		t.Fatalf("UnwrapProviderContentKey failed: %v", err)
	}
	if !bytes.Equal(unwrapped, contentKey) {
		t.Fatalf("unwrapped key mismatch")
	}
}

func TestProviderContentKeyEnvelopeRejectsTamperedAAD(t *testing.T) {
	t.Parallel()

	providerPriv, providerPub := testX25519KeyPair(t)
	contentKey := bytes.Repeat([]byte{0x37}, 32)
	aad := testProviderContentKeyAAD()

	envelope, err := WrapProviderContentKey(contentKey, providerPub, aad)
	if err != nil {
		t.Fatalf("WrapProviderContentKey failed: %v", err)
	}

	tamperedAAD := aad
	tamperedAAD.BundleSHA256 = strings.Repeat("a", 64)
	if _, err := UnwrapProviderContentKey(envelope, providerPriv, tamperedAAD); err == nil {
		t.Fatal("expected tampered AAD unwrap to fail")
	}
}

func TestProviderContentKeyEnvelopeRejectsWrongProviderKey(t *testing.T) {
	t.Parallel()

	_, providerPub := testX25519KeyPair(t)
	wrongProviderPriv, _ := testX25519KeyPair(t)
	contentKey := bytes.Repeat([]byte{0x91}, 32)
	aad := testProviderContentKeyAAD()

	envelope, err := WrapProviderContentKey(contentKey, providerPub, aad)
	if err != nil {
		t.Fatalf("WrapProviderContentKey failed: %v", err)
	}

	if _, err := UnwrapProviderContentKey(envelope, wrongProviderPriv, aad); err == nil {
		t.Fatal("expected wrong provider private key unwrap to fail")
	}
}

func TestProviderContentKeyEnvelopeJSONDoesNotContainRawContentKey(t *testing.T) {
	t.Parallel()

	_, providerPub := testX25519KeyPair(t)
	contentKey := bytes.Repeat([]byte{0xab}, 32)
	aad := testProviderContentKeyAAD()

	envelope, err := WrapProviderContentKey(contentKey, providerPub, aad)
	if err != nil {
		t.Fatalf("WrapProviderContentKey failed: %v", err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	envelopeJSON := string(raw)
	if strings.Contains(envelopeJSON, hex.EncodeToString(contentKey)) {
		t.Fatalf("envelope leaked hex content key: %s", envelopeJSON)
	}
	if strings.Contains(envelopeJSON, base64.RawURLEncoding.EncodeToString(contentKey)) {
		t.Fatalf("envelope leaked base64url content key: %s", envelopeJSON)
	}
	if strings.Contains(envelopeJSON, base64.RawStdEncoding.EncodeToString(contentKey)) {
		t.Fatalf("envelope leaked base64 content key: %s", envelopeJSON)
	}
}

func testProviderContentKeyAAD() ProviderContentKeyAAD {
	return ProviderContentKeyAAD{
		ModuleID:           "com.spaceaware.test-protocol",
		Version:            "0.0.1",
		BundleSHA256:       strings.Repeat("f", 64),
		SignerPublicKeyHex: strings.Repeat("c", 64),
		ProviderPeerID:     "12D3KooWProviderPeerForEnvelopeTests",
	}
}

func testX25519KeyPair(t *testing.T) ([]byte, []byte) {
	t.Helper()

	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatalf("rand.Read provider private key: %v", err)
	}
	clampX25519PrivateKey(priv)
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive provider public key: %v", err)
	}
	return priv, pub
}
