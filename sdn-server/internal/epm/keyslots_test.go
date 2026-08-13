package epm

// The §18 key-slot surface now carries derivation provenance (`source`) for the
// key-management UI (graph task sdn-managed-key-registry-api). Both identity
// slots are xpub-derived by construction, so their source is FIXED at "root" —
// this test pins that: an identity slot that ever reported "external" would be
// claiming a provenance the xpub paradigm cannot produce.

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

func TestKeySlotsCarryRootSource(t *testing.T) {
	t.Parallel()

	s := &Service{identity: &wasm.DerivedIdentity{Account: 0}}
	slots, err := s.KeySlots()
	if err != nil {
		t.Fatalf("KeySlots: %v", err)
	}
	if len(slots) != 2 {
		t.Fatalf("expected the two §18 slots, got %d", len(slots))
	}
	for _, slot := range slots {
		if slot.Source != KeySlotSourceRoot {
			t.Fatalf("slot %s reports source %q, want %q — an xpub-derived slot cannot be external", slot.Slot, slot.Source, KeySlotSourceRoot)
		}
		if !slot.XPubDerivable {
			t.Fatalf("slot %s must be xpub-derivable", slot.Slot)
		}
	}
}

func TestKeySlotsWithoutIdentityRefuse(t *testing.T) {
	t.Parallel()
	s := &Service{}
	if _, err := s.KeySlots(); err == nil {
		t.Fatal("KeySlots without an identity must refuse, not invent slots")
	}
}
