package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestEd25519PublicKeyFromDirectoryJSONUsesSigningKeyEntry(t *testing.T) {
	publicKey := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	for i := range publicKey {
		publicKey[i] = byte(i + 1)
	}
	epmJSON := `{
		"peer_id": "16Uiu2HAmProvider",
		"keys": [
			{"key_type": "signing", "address_type": "secp256k1", "public_key": "02aabb"},
			{"key_type": "signing", "address_type": "ed25519", "public_key": "` + hex.EncodeToString(publicKey) + `"}
		]
	}`

	got, err := publicKeyFromDirectoryJSON(epmJSON)
	if err != nil {
		t.Fatalf("publicKeyFromDirectoryJSON failed: %v", err)
	}
	raw, rawErr := got.Raw()
	if rawErr != nil {
		t.Fatalf("raw public key: %v", rawErr)
	}
	if string(raw) != string(publicKey) {
		t.Fatalf("public key mismatch: got %x want %x", raw, publicKey)
	}
}

func TestEd25519PublicKeyFromDirectoryJSONUsesTopLevelSigningPubkeyHex(t *testing.T) {
	publicKey := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	for i := range publicKey {
		publicKey[i] = byte(0x80 + i)
	}
	epmJSON := `{"signing_pubkey_hex":"` + hex.EncodeToString(publicKey) + `"}`

	got, err := publicKeyFromDirectoryJSON(epmJSON)
	if err != nil {
		t.Fatalf("publicKeyFromDirectoryJSON failed: %v", err)
	}
	raw, rawErr := got.Raw()
	if rawErr != nil {
		t.Fatalf("raw public key: %v", rawErr)
	}
	if string(raw) != string(publicKey) {
		t.Fatalf("public key mismatch: got %x want %x", raw, publicKey)
	}
}

// An HD node publishes with the Ed25519 signing key its EPM carries, while
// its peer identity is secp256k1. The verifier must take the key the
// publication DECLARES — not the peer identity first — or every publication
// between HD nodes is rejected as a signature-type mismatch (host-01,
// 2026-09-13..15: 2,122 rejections, one shard materialized).
func TestSelectDatasetPublicationKeyTakesTheDeclaredAlgorithm(t *testing.T) {
	secpPriv, secpPub, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = secpPriv
	_, edPub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Peer identity first, as the resolver lists them.
	candidates := []crypto.PubKey{secpPub, edPub}

	key, ok := selectDatasetPublicationKey(sds.SignatureTypeEd25519, candidates...)
	if !ok || key != edPub {
		t.Fatalf("an Ed25519 publication must verify against the EPM signing key, got %v (ok=%v)", key, ok)
	}
	key, ok = selectDatasetPublicationKey(sds.SignatureTypeSecp256k1, candidates...)
	if !ok || key != secpPub {
		t.Fatalf("a Secp256k1 publication must verify against the peer identity, got %v (ok=%v)", key, ok)
	}
	key, ok = selectDatasetPublicationKey("", candidates...)
	if !ok || key != secpPub {
		t.Fatalf("an undeclared type takes the first key, got %v (ok=%v)", key, ok)
	}
	if _, ok := selectDatasetPublicationKey(sds.SignatureTypeEd25519, secpPub); ok {
		t.Fatal("a peer identity of the wrong algorithm must not be selected for an Ed25519 publication")
	}

	// The exact production failure: verifying an Ed25519-labelled envelope
	// with the secp256k1 peer identity is a type mismatch, not a bad
	// signature, and the retry logic must treat it as "wrong signer".
	err = sds.VerifySDSSignature(sds.SignatureTypeEd25519, secpPub, []byte("payload"), make([]byte, 64))
	if err == nil || !isDatasetPublicationSignerMismatch(err) {
		t.Fatalf("type mismatch must read as a signer mismatch, got %v", err)
	}
}
