package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
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

// host-02's real EPM record (2026-09-15): the top-level signing_pubkey_hex is
// the secp256k1 signing key, and keys[] holds that key, TWO different Ed25519
// signing keys (the publication signer and the licensing-grant verifier) and
// an encryption key. A verifier that resolves one key can only get this
// wrong; it needs them all.
func TestPublicKeysFromDirectoryJSONReturnsEveryAdvertisedSigningKey(t *testing.T) {
	_, secpSigning, _ := crypto.GenerateSecp256k1Key(rand.Reader)
	_, edSigning, _ := crypto.GenerateEd25519Key(rand.Reader)
	_, edGrant, _ := crypto.GenerateEd25519Key(rand.Reader)
	_, secpEncrypt, _ := crypto.GenerateSecp256k1Key(rand.Reader)
	rawHex := func(k crypto.PubKey) string { raw, _ := k.Raw(); return hex.EncodeToString(raw) }
	epmJSON := `{
		"peer_id": "16Uiu2HAmProvider",
		"signing_pubkey_hex": "` + rawHex(secpSigning) + `",
		"keys": [
			{"address_type": "secp256k1", "key_type": "signing", "public_key": "` + rawHex(secpSigning) + `"},
			{"address_type": "ed25519", "key_type": "signing", "public_key": "` + rawHex(edSigning) + `"},
			{"address_type": "secp256k1", "key_type": "encryption", "public_key": "` + rawHex(secpEncrypt) + `"},
			{"address_type": "ed25519", "key_type": "signing", "public_key": "` + rawHex(edGrant) + `"}
		]
	}`
	keys, err := publicKeysFromDirectoryJSON(epmJSON)
	if err != nil {
		t.Fatalf("publicKeysFromDirectoryJSON: %v", err)
	}
	has := func(want crypto.PubKey) bool {
		for _, k := range keys {
			if k.Equals(want) {
				return true
			}
		}
		return false
	}
	if len(keys) != 3 || !has(secpSigning) || !has(edSigning) || !has(edGrant) || has(secpEncrypt) {
		t.Fatalf("got %d keys (secp signing %v, ed signing %v, ed grant %v, encryption %v), want the three signing keys once each",
			len(keys), has(secpSigning), has(edSigning), has(edGrant), has(secpEncrypt))
	}
}

// The publication picks its own key: an Ed25519-labelled PNM signed with the
// EPM's Ed25519 signing key verifies against exactly that key, even when the
// provider is also known by a secp256k1 peer identity and another Ed25519
// key. With only the peer identity known, the failure names the type
// mismatch and reads as "wrong signer", so the catch-up tries the next
// candidate instead of marking the publication permanently failed.
func TestVerifyDatasetPublicationPNMPicksTheKeyThatSigned(t *testing.T) {
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := crypto.UnmarshalEd25519PublicKey(edPub)
	if err != nil {
		t.Fatal(err)
	}
	_, peerIdentity, _ := crypto.GenerateSecp256k1Key(rand.Reader)
	_, otherEd, _ := crypto.GenerateEd25519Key(rand.Reader)
	pnmBytes, err := storage.BuildDatasetPublicationPNM(&storage.DatasetPublicationManifest{
		CID:    "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		FileID: "DPM:OMM.fbs",
	}, storage.DatasetPublicationPNMOptions{SigningKey: edPriv})
	if err != nil {
		t.Fatalf("BuildDatasetPublicationPNM: %v", err)
	}

	key, proof, err := verifyDatasetPublicationPNM(pnmBytes, []crypto.PubKey{peerIdentity, otherEd, signer})
	if err != nil {
		t.Fatalf("verify with every known key: %v", err)
	}
	if !key.Equals(signer) || proof.SignatureType != sds.SignatureTypeEd25519 {
		t.Fatalf("verified with %v (%s), want the Ed25519 signing key", key, proof.SignatureType)
	}

	_, _, err = verifyDatasetPublicationPNM(pnmBytes, []crypto.PubKey{peerIdentity})
	if err == nil || !strings.Contains(err.Error(), "does not match the provider's") || !isDatasetPublicationSignerMismatch(err) {
		t.Fatalf("peer identity alone must fail as a type mismatch that reads as a wrong signer, got %v", err)
	}
	_, _, err = verifyDatasetPublicationPNM(pnmBytes, []crypto.PubKey{peerIdentity, otherEd})
	if err == nil || !isDatasetPublicationSignerMismatch(err) || isPermanentDatasetPublicationMaterializationError(err) {
		t.Fatalf("a wrong Ed25519 key must fail as a retryable wrong signer, got %v", err)
	}
}
