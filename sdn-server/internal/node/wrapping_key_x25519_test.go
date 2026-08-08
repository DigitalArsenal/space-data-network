package node

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// ⛔ P1: keyslot.unwrap was UNUSABLE AS SHIPPED — one scalar, three curves.
//
// The provider-wrapping slot is DECLARED KeySlotAlgorithmX25519
// (licensing_bootstrap.go) and OPENED with crypto/ecdh X25519
// (modulert/caps/keyslot.go). But the public half the node published was
// P256(sha256(scalar)).PublicKey — a different curve over a different scalar.
// No browser could seal a payload the node could open, and no module could open
// one, so the whole wrapping lane was dead. Nothing tested it: the slot's
// ALGORITHM was asserted, its PUBLISHED KEY never was.
//
// The property that matters is not "the code calls X25519" — it is that a party
// holding only nodeCtx.PublicKeyHex and the slot holder derive THE SAME shared
// secret. That is what these tests assert, end to end.

func newWrappingKeyTestNode(t *testing.T) *Node {
	t.Helper()
	basePath := t.TempDir()
	keyMgr, err := keys.NewManager(basePath)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}
	if _, err := keyMgr.GenerateIdentity(); err != nil {
		t.Fatalf("GenerateIdentity() failed: %v", err)
	}
	return &Node{
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(basePath, "data")},
		},
	}
}

// The acceptance property: seal to the published key, open with the slot.
func TestProviderWrappingPublicKeyCompletesAnX25519ECDHWithTheSlotScalar(t *testing.T) {
	t.Parallel()

	n := newWrappingKeyTestNode(t)
	nodeCtx, err := n.buildModuleNodeContext()
	if err != nil {
		t.Fatalf("buildModuleNodeContext() failed: %v", err)
	}

	publishedHex := nodeCtx.PublicKeyHex
	if publishedHex == "" {
		t.Fatal("node published NO wrapping public key: a browser has nothing to seal to")
	}
	published, err := hex.DecodeString(publishedHex)
	if err != nil {
		t.Fatalf("published wrapping public key is not hex: %v", err)
	}
	if len(published) != 32 {
		t.Fatalf("published wrapping public key is %d bytes, want 32 (X25519); a P-256 point is 65", len(published))
	}

	slotScalar := nodeCtx.KeySlots[providerWrappingSlotID]
	if len(slotScalar) != 32 {
		t.Fatalf("provider wrapping slot holds %d bytes, want a 32-byte X25519 scalar", len(slotScalar))
	}
	if nodeCtx.KeySlotAlgorithms[providerWrappingSlotID] != modulert.KeySlotAlgorithmX25519 {
		t.Fatalf("provider wrapping slot algorithm = %q, want %q",
			nodeCtx.KeySlotAlgorithms[providerWrappingSlotID], modulert.KeySlotAlgorithmX25519)
	}

	curve := ecdh.X25519()

	// THE BROWSER SIDE: an ephemeral key sealing to the published public half.
	// This is exactly what a client does before calling keyslot.unwrap.
	browserPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate browser ephemeral key: %v", err)
	}
	nodePub, err := curve.NewPublicKey(published)
	if err != nil {
		t.Fatalf("published key is not a valid X25519 public key: %v", err)
	}
	browserShared, err := browserPriv.ECDH(nodePub)
	if err != nil {
		t.Fatalf("browser ECDH against the published key failed: %v", err)
	}

	// THE NODE SIDE: the same computation caps/keyslot.go performs with the
	// slot scalar.
	slotPriv, err := curve.NewPrivateKey(slotScalar)
	if err != nil {
		t.Fatalf("slot scalar is not a valid X25519 private key: %v", err)
	}
	nodeShared, err := slotPriv.ECDH(browserPriv.PublicKey())
	if err != nil {
		t.Fatalf("node ECDH with the slot scalar failed: %v", err)
	}

	if !bytes.Equal(browserShared, nodeShared) {
		t.Fatalf("SEALED PAYLOADS CANNOT BE OPENED: browser shared secret %x != node shared secret %x — "+
			"the published public key does not belong to the slot scalar", browserShared, nodeShared)
	}

	// And the published key IS the slot scalar's own public half, not merely
	// something that happens to interoperate.
	if want := slotPriv.PublicKey().Bytes(); !bytes.Equal(published, want) {
		t.Fatalf("published wrapping key %x != the slot scalar's X25519 public half %x", published, want)
	}
}

// deriveX25519PublicKeyHex must be the SAME derivation the unwrap oracle uses,
// and must fail closed on a scalar it cannot use rather than returning
// something unusable.
func TestDeriveX25519PublicKeyHexMatchesTheUnwrapCurveAndFailsClosed(t *testing.T) {
	t.Parallel()

	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	got, err := deriveX25519PublicKeyHex(priv.Bytes())
	if err != nil {
		t.Fatalf("deriveX25519PublicKeyHex: %v", err)
	}
	if want := hex.EncodeToString(priv.PublicKey().Bytes()); got != want {
		t.Fatalf("derived %s, want %s", got, want)
	}

	for name, scalar := range map[string][]byte{
		"empty":     nil,
		"short":     make([]byte, 31),
		"oversized": make([]byte, 33),
	} {
		if _, err := deriveX25519PublicKeyHex(scalar); err == nil {
			t.Fatalf("deriveX25519PublicKeyHex accepted a %s scalar", name)
		}
	}
}
