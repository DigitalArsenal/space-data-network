package caps

import (
	"bytes"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"golang.org/x/crypto/ed25519"
)

func keyslotPublicTestContext() *modulert.NodeContext {
	return &modulert.NodeContext{
		KeySlots: map[string][]byte{
			"provider-signing":  bytes.Repeat([]byte{0x11}, ed25519.SeedSize),
			"provider-wrapping": bytes.Repeat([]byte{0x22}, 32),
		},
		KeySlotAlgorithms: map[string]string{
			"provider-signing":  modulert.KeySlotAlgorithmEd25519,
			"provider-wrapping": modulert.KeySlotAlgorithmX25519,
		},
	}
}

func TestKeyslotEd25519PublicKeyMatchesTheKeyKeyslotSignUses(t *testing.T) {
	t.Parallel()

	nodeCtx := keyslotPublicTestContext()
	publicKey, err := KeyslotEd25519PublicKey(nodeCtx, "provider-signing")
	if err != nil {
		t.Fatalf("KeyslotEd25519PublicKey() failed: %v", err)
	}

	// The point of the function: the returned key must verify what the signing
	// oracle produces. Anything else is a self-consistent lie that passes every
	// host check and fails every client verify.
	message := []byte("licensing grant bytes with a zeroed provider signature")
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(nodeCtx.KeySlots["provider-signing"]), message)
	if !ed25519.Verify(publicKey, message, signature) {
		t.Fatal("returned public key does not verify a signature from the same slot")
	}
	if bytes.Equal(publicKey, make([]byte, ed25519.PublicKeySize)) {
		t.Fatal("returned an all-zero public key")
	}
}

func TestKeyslotEd25519PublicKeyFailsClosed(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func() (*modulert.NodeContext, string){
		"no context": func() (*modulert.NodeContext, string) {
			return nil, "provider-signing"
		},
		"unknown slot": func() (*modulert.NodeContext, string) {
			return keyslotPublicTestContext(), "no-such-slot"
		},
		"slot declared for another algorithm": func() (*modulert.NodeContext, string) {
			// A 32-byte slot is bit-compatible with an Ed25519 seed and an
			// X25519 scalar, so without the declared-algorithm gate the
			// wrapping key could be milked for a signing public key.
			return keyslotPublicTestContext(), "provider-wrapping"
		},
		"undeclared algorithm": func() (*modulert.NodeContext, string) {
			nodeCtx := keyslotPublicTestContext()
			delete(nodeCtx.KeySlotAlgorithms, "provider-signing")
			return nodeCtx, "provider-signing"
		},
		"wrong seed length": func() (*modulert.NodeContext, string) {
			nodeCtx := keyslotPublicTestContext()
			nodeCtx.KeySlots["provider-signing"] = bytes.Repeat([]byte{0x11}, 16)
			return nodeCtx, "provider-signing"
		},
	} {
		t.Run(name, func(t *testing.T) {
			nodeCtx, slotID := build()
			if _, err := KeyslotEd25519PublicKey(nodeCtx, slotID); err == nil {
				t.Fatal("KeyslotEd25519PublicKey() succeeded; want refusal")
			}
		})
	}
}
