package ecies

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/curve25519"
)

func x25519Keypair(t *testing.T) (priv, pub []byte) {
	t.Helper()
	priv = make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func secp256k1Keypair(t *testing.T) (priv, pub []byte) {
	t.Helper()
	sk, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return sk.Serialize(), sk.PubKey().SerializeCompressed()
}

func TestWrapUnwrapRoundTrip(t *testing.T) {
	contentKey := make([]byte, 32)
	for i := range contentKey {
		contentKey[i] = byte(i * 7)
	}

	for _, tc := range []struct {
		name string
		kx   KeyExchange
		keys func(*testing.T) ([]byte, []byte)
	}{
		{"x25519", X25519, x25519Keypair},
		{"secp256k1", Secp256k1, secp256k1Keypair},
	} {
		t.Run(tc.name, func(t *testing.T) {
			priv, pub := tc.keys(t)
			encBytes, kmfBytes, err := Wrap(pub, contentKey, WrapOptions{KeyExchange: tc.kx})
			if err != nil {
				t.Fatalf("Wrap: %v", err)
			}
			// The wrapped KEY_BYTES must NOT equal the plaintext content key.
			if bytes.Contains(kmfBytes, contentKey) {
				t.Fatal("wrapped KMF leaks the plaintext content key")
			}
			got, err := Unwrap(priv, encBytes, kmfBytes, "")
			if err != nil {
				t.Fatalf("Unwrap: %v", err)
			}
			if !bytes.Equal(got, contentKey) {
				t.Fatalf("round-trip mismatch: got %x want %x", got, contentKey)
			}

			// Wrong recipient key must not recover the content key.
			otherPriv, _ := tc.keys(t)
			if bad, err := Unwrap(otherPriv, encBytes, kmfBytes, ""); err == nil && bytes.Equal(bad, contentKey) {
				t.Fatal("unwrap with the wrong key recovered the content key")
			}
		})
	}
}

func TestContextDomainSeparation(t *testing.T) {
	contentKey := make([]byte, 32)
	priv, pub := x25519Keypair(t)
	encBytes, kmfBytes, err := Wrap(pub, contentKey, WrapOptions{KeyExchange: X25519, Context: "context-A"})
	if err != nil {
		t.Fatal(err)
	}
	// Unwrapping under a different context must not recover the key (the master
	// key is HKDF'd with the context as info).
	got, err := Unwrap(priv, encBytes, kmfBytes, "context-B")
	if err == nil && bytes.Equal(got, contentKey) {
		t.Fatal("context-B recovered a key wrapped under context-A")
	}
	// Same context recovers it.
	ok, err := Unwrap(priv, encBytes, kmfBytes, "context-A")
	if err != nil || !bytes.Equal(ok, contentKey) {
		t.Fatalf("same-context unwrap failed: %v", err)
	}
}

func TestSecp256k1SharedSecretIsRawX(t *testing.T) {
	// Pin the cross-runtime invariant: the secp256k1 shared secret is the raw
	// X coordinate (RFC 5903), which is what CryptoPP ECDH<ECP>.Agree and
	// WebCrypto deriveBits produce — so Go/C++/JS agree.
	aPriv, aPub := secp256k1Keypair(t)
	bPriv, bPub := secp256k1Keypair(t)
	ab, err := ecdh(Secp256k1, aPriv, bPub)
	if err != nil {
		t.Fatal(err)
	}
	ba, err := ecdh(Secp256k1, bPriv, aPub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ab, ba) {
		t.Fatal("ECDH not symmetric")
	}
	if len(ab) != 32 {
		t.Fatalf("shared secret len = %d, want 32 (raw X)", len(ab))
	}
	// Independently: raw X via decred equals our ecdh output.
	sk := secp256k1.PrivKeyFromBytes(aPriv)
	pk, _ := secp256k1.ParsePubKey(bPub)
	rawX := secp256k1.GenerateSharedSecret(sk, pk)
	padded := make([]byte, 32)
	copy(padded[32-len(rawX):], rawX)
	if !bytes.Equal(ab, padded) {
		t.Fatal("ecdh() does not equal decred raw-X shared secret")
	}
}

// conformanceVector is the cross-runtime test-vector shape other runtimes
// (C++/WASM, JS SDK) validate against: unwrap encBytes+kmfBytes with
// recipientPriv → contentKey.
type conformanceVector struct {
	KeyExchange   string `json:"keyExchange"`
	Context       string `json:"context"`
	RecipientPriv string `json:"recipientPrivHex"`
	RecipientPub  string `json:"recipientPubHex"`
	EphemeralPriv string `json:"ephemeralPrivHex"`
	ContentKey    string `json:"contentKeyHex"`
	ENCBytes      string `json:"encHex"`
	KMFBytes      string `json:"kmfHex"`
}

// TestEmitConformanceVectors writes deterministic vectors (fixed ephemeral +
// nonce) for the other runtimes when SDN_ECIES_WRITE_VECTORS is set, and
// always self-validates that they round-trip.
func TestEmitConformanceVectors(t *testing.T) {
	zeroRand := zeroReader{}
	fixedContentKey, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")

	vectors := []conformanceVector{}
	// X25519 with a fixed ephemeral private key.
	{
		recipPriv, _ := hex.DecodeString("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
		recipPub, err := curve25519.X25519(recipPriv, curve25519.Basepoint)
		if err != nil {
			t.Fatal(err)
		}
		ephPriv, _ := hex.DecodeString("5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb")
		encB, kmfB, err := Wrap(recipPub, fixedContentKey, WrapOptions{
			KeyExchange: X25519, Context: DefaultGrantContext,
			rand: zeroRand, ephemeralPrivOverride: ephPriv,
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := Unwrap(recipPriv, encB, kmfB, DefaultGrantContext)
		if err != nil || !bytes.Equal(got, fixedContentKey) {
			t.Fatalf("x25519 vector self-check failed: %v", err)
		}
		vectors = append(vectors, conformanceVector{
			KeyExchange: "X25519", Context: DefaultGrantContext,
			RecipientPriv: hex.EncodeToString(recipPriv), RecipientPub: hex.EncodeToString(recipPub),
			EphemeralPriv: hex.EncodeToString(ephPriv), ContentKey: hex.EncodeToString(fixedContentKey),
			ENCBytes: hex.EncodeToString(encB), KMFBytes: hex.EncodeToString(kmfB),
		})
	}
	// secp256k1 with a fixed ephemeral private key.
	{
		recipSk := secp256k1.PrivKeyFromBytes(mustHex("c9afa9d845ba75166b5c215767b1d6934e50c3db36e89b127b8a622b120f6721"))
		recipPriv := recipSk.Serialize()
		recipPub := recipSk.PubKey().SerializeCompressed()
		ephPriv := mustHex("cca9fbc7e8d8b1c2f4a3e6d5b8a7c9e0f1d2c3b4a5968778695a4b3c2d1e0f01")
		encB, kmfB, err := Wrap(recipPub, fixedContentKey, WrapOptions{
			KeyExchange: Secp256k1, Context: DefaultGrantContext,
			rand: zeroRand, ephemeralPrivOverride: ephPriv,
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := Unwrap(recipPriv, encB, kmfB, DefaultGrantContext)
		if err != nil || !bytes.Equal(got, fixedContentKey) {
			t.Fatalf("secp256k1 vector self-check failed: %v", err)
		}
		vectors = append(vectors, conformanceVector{
			KeyExchange: "Secp256k1", Context: DefaultGrantContext,
			RecipientPriv: hex.EncodeToString(recipPriv), RecipientPub: hex.EncodeToString(recipPub),
			EphemeralPriv: hex.EncodeToString(ephPriv), ContentKey: hex.EncodeToString(fixedContentKey),
			ENCBytes: hex.EncodeToString(encB), KMFBytes: hex.EncodeToString(kmfB),
		})
	}

	if os.Getenv("SDN_ECIES_WRITE_VECTORS") == "1" {
		dir := filepath.Join("testdata")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		out, _ := json.MarshalIndent(vectors, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "conformance_vectors.json"), append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d conformance vectors to %s", len(vectors), dir)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
