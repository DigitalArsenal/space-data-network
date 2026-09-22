package ecies

import (
	"bytes"
	"crypto/rand"
	"testing"

	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/curve25519"
)

// SealField keyed as KMF.KEY_BYTES (field 4) must produce exactly the bytes
// Wrap writes there: the conformance-tested content-key path and the general
// field path are one derivation.
func TestSealFieldMatchesWrapForTheSameField(t *testing.T) {
	recipient, _ := secp256k1.GeneratePrivateKey()
	pub := recipient.PubKey().SerializeCompressed()
	eph := bytes.Repeat([]byte{7}, 32)
	contentKey := bytes.Repeat([]byte{0x42}, 32)
	opts := WrapOptions{KeyExchange: Secp256k1, Context: "parity", ephemeralPrivOverride: eph}

	_, kmfBytes, err := Wrap(pub, contentKey, opts)
	if err != nil {
		t.Fatal(err)
	}
	h, sealed, err := SealField(pub, contentKey, kmfKeyBytesFieldID, opts)
	if err != nil {
		t.Fatal(err)
	}
	if want := kmf.GetRootAsKMF(kmfBytes, 0).KeyBytesBytes(); !bytes.Equal(sealed, want) {
		t.Fatalf("SealField(field 4) = %x, Wrap KEY_BYTES = %x", sealed, want)
	}
	if h.Context != "parity" || len(h.EphemeralPub) != 33 || len(h.NonceStart) != 12 {
		t.Fatalf("header %+v", h)
	}
}

func TestSealFieldRoundTripAndRefusals(t *testing.T) {
	for _, kx := range []KeyExchange{X25519, Secp256k1} {
		var priv, pub []byte
		if kx == X25519 {
			priv = make([]byte, 32)
			rand.Read(priv)
			pub, _ = curve25519.X25519(priv, curve25519.Basepoint)
		} else {
			sk, _ := secp256k1.GeneratePrivateKey()
			priv, pub = sk.Serialize(), sk.PubKey().SerializeCompressed()
		}
		msg := []byte("admin command body")
		h, ct, err := SealField(pub, msg, 7, WrapOptions{KeyExchange: kx, Context: "RPC/1|request"})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(ct, msg) {
			t.Fatal("ciphertext contains the plaintext")
		}
		got, err := OpenField(priv, h, ct, 7, "RPC/1|request")
		if err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("%v: open = %q, %v", kx, got, err)
		}
		if _, err := OpenField(priv, h, ct, 7, "RPC/1|response"); err == nil {
			t.Fatalf("%v: opened under the wrong context", kx)
		}
		if got, _ := OpenField(priv, h, ct, 8, ""); bytes.Equal(got, msg) {
			t.Fatalf("%v: a different field id opened the same plaintext", kx)
		}
	}
	if _, _, err := SealField(make([]byte, 32), []byte("x"), 7, WrapOptions{KeyExchange: X25519}); err == nil {
		t.Fatal("sealed without a context")
	}
}
