package channelkeys

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestMessageRoundTrip(t *testing.T) {
	ch, err := New("chat-room-msg")
	if err != nil {
		t.Fatal(err)
	}
	alice := x25519Party(t, "alice")
	_ = ch.AddMember(alice.member())
	key := ch.ContentKey()

	_, senderPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("hello channel — привет 🚀")

	env, err := EncryptMessage(key, senderPriv, ch.Context(), ch.Epoch(), plaintext, EncryptOptions{TimestampMs: 1_750_000_000_000})
	if err != nil {
		t.Fatalf("EncryptMessage: %v", err)
	}
	if bytes.Contains(env, plaintext) {
		t.Fatal("envelope leaks plaintext")
	}

	msg, err := DecryptMessage(key, env, ch.Context())
	if err != nil {
		t.Fatalf("DecryptMessage: %v", err)
	}
	if !bytes.Equal(msg.Plaintext, plaintext) {
		t.Fatal("plaintext mismatch")
	}
	if msg.Epoch != ch.Epoch() {
		t.Fatalf("epoch = %d want %d", msg.Epoch, ch.Epoch())
	}
	if msg.TimestampMs != 1_750_000_000_000 {
		t.Fatalf("timestamp = %d", msg.TimestampMs)
	}
	wantPub := senderPriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(msg.SenderPublicKey, wantPub) {
		t.Fatal("sender public key mismatch")
	}

	// Non-member (wrong key) cannot decrypt.
	wrongKey := make([]byte, 32)
	if _, err := DecryptMessage(wrongKey, env, ch.Context()); err == nil {
		t.Fatal("wrong content key decrypted the message")
	}

	// Tampered ciphertext fails (GCM tag).
	tampered := append([]byte(nil), env...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := DecryptMessage(key, tampered, ch.Context()); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}

	// Tampered header fails (signature + AAD).
	tamperedHdr := append([]byte(nil), env...)
	tamperedHdr[8] ^= 0x01
	if _, err := DecryptMessage(key, tamperedHdr, ch.Context()); err == nil {
		t.Fatal("tampered header accepted")
	}

	// Context mismatch rejected.
	if _, err := DecryptMessage(key, env, "some-other-context"); err == nil {
		t.Fatal("context mismatch accepted")
	}
}

func TestMessageSignatureBindsSender(t *testing.T) {
	ch, err := New("chat-room-sig")
	if err != nil {
		t.Fatal(err)
	}
	key := ch.ContentKey()
	_, senderPriv, _ := ed25519.GenerateKey(nil)
	env, err := EncryptMessage(key, senderPriv, ch.Context(), 1, []byte("m"), EncryptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Flip a signature bit → verification must fail even though GCM would pass.
	encLen := int(uint32(env[0]) | uint32(env[1])<<8 | uint32(env[2])<<16 | uint32(env[3])<<24)
	bad := append([]byte(nil), env...)
	bad[4+encLen] ^= 0x01 // first signature byte
	if _, err := DecryptMessage(key, bad, ch.Context()); err == nil {
		t.Fatal("forged signature accepted")
	}
}

// messageVector is the cross-runtime chat-message vector: sdn-js must decrypt
// envelopeHex to plaintext AND reproduce envelopeHex byte-for-byte from the
// same deterministic inputs.
type messageVector struct {
	ContentKey  string `json:"contentKeyHex"`
	SenderSeed  string `json:"senderSeedHex"` // ed25519 seed (32 bytes)
	SenderPub   string `json:"senderPubHex"`
	Context     string `json:"context"`
	Epoch       uint64 `json:"epoch"`
	Nonce       string `json:"nonceHex"`
	TimestampMs uint64 `json:"timestampMs"`
	Plaintext   string `json:"plaintextHex"`
	Envelope    string `json:"envelopeHex"`
	ChatTopic   string `json:"chatTopic"`
}

func TestEmitMessageVector(t *testing.T) {
	contentKey := mustHex("202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f")
	seed := mustHex("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	senderPriv := ed25519.NewKeyFromSeed(seed)
	nonce := mustHex("000102030405060708090a0b")
	const ctx = "space-data-network/channel/chat-room-vector/v1"
	const epoch = uint64(3)
	const ts = uint64(1751400000000)
	plaintext := []byte("cross-runtime channel chat vector")

	env, err := EncryptMessage(contentKey, senderPriv, ctx, epoch, plaintext, EncryptOptions{Nonce: nonce, TimestampMs: ts})
	if err != nil {
		t.Fatal(err)
	}
	// Self-check.
	msg, err := DecryptMessage(contentKey, env, ctx)
	if err != nil || !bytes.Equal(msg.Plaintext, plaintext) || msg.Epoch != epoch {
		t.Fatalf("vector self-check failed: %v", err)
	}

	v := messageVector{
		ContentKey:  hex.EncodeToString(contentKey),
		SenderSeed:  hex.EncodeToString(seed),
		SenderPub:   hex.EncodeToString(senderPriv.Public().(ed25519.PublicKey)),
		Context:     ctx,
		Epoch:       epoch,
		Nonce:       hex.EncodeToString(nonce),
		TimestampMs: ts,
		Plaintext:   hex.EncodeToString(plaintext),
		Envelope:    hex.EncodeToString(env),
		ChatTopic:   ChatTopic("chat-room-vector"),
	}
	if os.Getenv("SDN_ECIES_WRITE_VECTORS") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		out, _ := json.MarshalIndent([]messageVector{v}, "", "  ")
		if err := os.WriteFile(filepath.Join("testdata", "message_vectors.json"), append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote message vector")
	}
}
