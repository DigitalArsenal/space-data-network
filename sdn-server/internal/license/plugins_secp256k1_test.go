package license

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/curve25519"
)

func envTestAsset() *PluginAsset {
	return &PluginAsset{ID: "com.orbpro.x", Version: "1.0.0", RequiredScope: "orbpro.default", BundleSHA256: "abc123"}
}

func envTestClaims() *CapabilityClaims {
	return &CapabilityClaims{Sub: "sub", PeerID: "peer", JTI: "jti"}
}

func randKey32(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestPluginKeyEnvelopeSecp256k1RoundTrip(t *testing.T) {
	pluginKey := randKey32(t)

	recipientPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipientPub := recipientPriv.PubKey().SerializeCompressed()

	env, err := BuildPluginKeyEnvelopeSecp256k1(envTestAsset(), pluginKey, recipientPub, envTestClaims(), "issuer", time.Time{})
	if err != nil {
		t.Fatalf("BuildPluginKeyEnvelopeSecp256k1: %v", err)
	}
	if env.Algorithm != secp256k1KeyEnvelopeAlgorithm {
		t.Fatalf("Algorithm = %q, want %q", env.Algorithm, secp256k1KeyEnvelopeAlgorithm)
	}

	got, err := OpenPluginKeyEnvelope(env, recipientPriv.Serialize())
	if err != nil {
		t.Fatalf("OpenPluginKeyEnvelope (secp256k1): %v", err)
	}
	if !bytes.Equal(got, pluginKey) {
		t.Fatal("secp256k1 round-trip recovered a different key")
	}

	// A different recipient key must not open the envelope.
	other, _ := secp256k1.GeneratePrivateKey()
	if _, err := OpenPluginKeyEnvelope(env, other.Serialize()); err == nil {
		t.Error("expected failure opening secp256k1 envelope with the wrong key")
	}
}

func TestPluginKeyEnvelopeX25519RoundTripViaOpen(t *testing.T) {
	pluginKey := randKey32(t)

	recipientPriv := randKey32(t)
	recipientPub, err := curve25519.X25519(recipientPriv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive x25519 pub: %v", err)
	}

	env, err := BuildPluginKeyEnvelope(envTestAsset(), pluginKey, recipientPub, envTestClaims(), "issuer", time.Time{})
	if err != nil {
		t.Fatalf("BuildPluginKeyEnvelope (x25519): %v", err)
	}
	if env.Algorithm != defaultKeyEnvelopeAlgorithm {
		t.Fatalf("Algorithm = %q, want %q", env.Algorithm, defaultKeyEnvelopeAlgorithm)
	}

	got, err := OpenPluginKeyEnvelope(env, recipientPriv)
	if err != nil {
		t.Fatalf("OpenPluginKeyEnvelope (x25519): %v", err)
	}
	if !bytes.Equal(got, pluginKey) {
		t.Fatal("x25519 round-trip recovered a different key")
	}
}

func TestOpenPluginKeyEnvelopeUnsupportedAlgorithm(t *testing.T) {
	_, err := OpenPluginKeyEnvelope(&PluginKeyEnvelope{Algorithm: "bogus"}, randKey32(t))
	if err == nil {
		t.Error("expected error for unsupported algorithm")
	}
}
