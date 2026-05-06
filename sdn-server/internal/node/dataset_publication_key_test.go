package node

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
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

	got, err := ed25519PublicKeyFromDirectoryJSON(epmJSON)
	if err != nil {
		t.Fatalf("ed25519PublicKeyFromDirectoryJSON failed: %v", err)
	}
	if string(got) != string(publicKey) {
		t.Fatalf("public key mismatch: got %x want %x", got, publicKey)
	}
}

func TestEd25519PublicKeyFromDirectoryJSONUsesTopLevelSigningPubkeyHex(t *testing.T) {
	publicKey := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	for i := range publicKey {
		publicKey[i] = byte(0x80 + i)
	}
	epmJSON := `{"signing_pubkey_hex":"` + hex.EncodeToString(publicKey) + `"}`

	got, err := ed25519PublicKeyFromDirectoryJSON(epmJSON)
	if err != nil {
		t.Fatalf("ed25519PublicKeyFromDirectoryJSON failed: %v", err)
	}
	if string(got) != string(publicKey) {
		t.Fatalf("public key mismatch: got %x want %x", got, publicKey)
	}
}
