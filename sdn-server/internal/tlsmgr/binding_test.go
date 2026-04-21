package tlsmgr

import (
	"bytes"
	"testing"
)

func TestEncodeAndVerifyBootstrapBinding_RoundTrips(t *testing.T) {
	input := BootstrapBindingInput{
		PeerID:                    "12D3KooWTestPeer",
		EncryptionPath:            "m/44'/0'/0'/1'/0'",
		EncryptionX25519PublicKey: bytes.Repeat([]byte{9}, 32),
		ProofEd25519Seed:          bytes.Repeat([]byte{7}, 32),
		TLSSPKISHA256:             bytes.Repeat([]byte{3}, 32),
	}

	ext, err := EncodeBootstrapBinding(input)
	if err != nil {
		t.Fatalf("EncodeBootstrapBinding() error = %v", err)
	}

	binding, err := VerifyBootstrapBinding(ext.Value, input.TLSSPKISHA256)
	if err != nil {
		t.Fatalf("VerifyBootstrapBinding() error = %v", err)
	}
	if binding.PeerID != input.PeerID {
		t.Fatalf("PeerID = %q, want %q", binding.PeerID, input.PeerID)
	}
	if binding.EncryptionPath != input.EncryptionPath {
		t.Fatalf("EncryptionPath = %q, want %q", binding.EncryptionPath, input.EncryptionPath)
	}
}

func TestVerifyBootstrapBinding_RejectsMismatchedSPKIHash(t *testing.T) {
	input := BootstrapBindingInput{
		PeerID:                    "12D3KooWTestPeer",
		EncryptionPath:            "m/44'/0'/0'/1'/0'",
		EncryptionX25519PublicKey: bytes.Repeat([]byte{9}, 32),
		ProofEd25519Seed:          bytes.Repeat([]byte{7}, 32),
		TLSSPKISHA256:             bytes.Repeat([]byte{3}, 32),
	}

	ext, err := EncodeBootstrapBinding(input)
	if err != nil {
		t.Fatalf("EncodeBootstrapBinding() error = %v", err)
	}

	_, err = VerifyBootstrapBinding(ext.Value, bytes.Repeat([]byte{4}, 32))
	if err == nil {
		t.Fatal("VerifyBootstrapBinding() error = nil, want spki hash mismatch")
	}
}
