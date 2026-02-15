package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func TestSDNXPubRoundTrip(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	chainCode := make([]byte, 32)
	if _, err := rand.Read(chainCode); err != nil {
		t.Fatal(err)
	}

	xpub, err := NewSDNXPub(pub, chainCode, 5, [4]byte{0xDE, 0xAD, 0xBE, 0xEF}, 0x80000000)
	if err != nil {
		t.Fatal(err)
	}

	encoded := SerializeSDNXPub(xpub)
	t.Logf("SDN xpub: %s", encoded)
	t.Logf("Length: %d chars", len(encoded))

	parsed, err := ParseSDNXPub(encoded)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if parsed.Depth != 5 {
		t.Errorf("depth: got %d, want 5", parsed.Depth)
	}
	if parsed.ChildIndex != 0x80000000 {
		t.Errorf("child index: got %d, want 0x80000000", parsed.ChildIndex)
	}
	if parsed.Fingerprint != [4]byte{0xDE, 0xAD, 0xBE, 0xEF} {
		t.Errorf("fingerprint mismatch")
	}
	for i := 0; i < 32; i++ {
		if parsed.PubKey[i] != pub[i] {
			t.Fatalf("pubkey mismatch at byte %d", i)
		}
		if parsed.ChainCode[i] != chainCode[i] {
			t.Fatalf("chain code mismatch at byte %d", i)
		}
	}
}

func TestExtractEd25519PubKeyFromXPub(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	chainCode := make([]byte, 32)
	rand.Read(chainCode)

	xpub, _ := NewSDNXPub(pub, chainCode, 5, [4]byte{}, 0x80000000)
	encoded := SerializeSDNXPub(xpub)

	extracted, err := ExtractEd25519PubKeyFromXPub(encoded)
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	if len(extracted) != ed25519.PublicKeySize {
		t.Fatalf("extracted key length: got %d, want %d", len(extracted), ed25519.PublicKeySize)
	}
	if hex.EncodeToString(extracted) != hex.EncodeToString(pub) {
		t.Error("extracted key does not match original")
	}
}

func TestParseSDNXPubRejectsInvalid(t *testing.T) {
	// Standard BIP-32 xpub should fail (wrong version bytes)
	_, err := ParseSDNXPub("xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8")
	if err == nil {
		t.Error("expected error parsing BIP-32 xpub, got nil")
	}

	// Empty string
	_, err = ParseSDNXPub("")
	if err == nil {
		t.Error("expected error parsing empty string")
	}

	// Garbage
	_, err = ParseSDNXPub("notavalidxpub!!!")
	if err == nil {
		t.Error("expected error parsing garbage")
	}
}
