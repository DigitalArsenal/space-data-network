package epm

// The operator-facing key slots behind the SIGNING PATH / ENCRYPTION PATH
// fields and their GEN KEY buttons (§18 of
// graph/tasks/nst-node-admin-contract.md).
//
// This is the read/derive half: it reports the paths currently in effect and
// computes what GEN KEY would rotate them to. Both are pure functions of state
// the node already holds — no private key material is read, produced, or
// returned, and the node's seed never leaves the process.

import (
	"fmt"
	"strings"
)

// KeySlotID names an operator-rotatable slot.
type KeySlotID string

const (
	// KeySlotSigning is the secp256k1 EPM signing key. XPUB-DERIVABLE: a
	// verifier reconstructs it from the published xpub plus this path, which is
	// why rotating it costs nothing and publishes no key material.
	KeySlotSigning KeySlotID = "signing"
	// KeySlotEncryption is the secp256k1 encryption key. Also xpub-derivable.
	KeySlotEncryption KeySlotID = "encryption"
)

// KeySlot is one row of the identity widget: which path is in effect, and what
// GEN KEY would move it to.
type KeySlot struct {
	Slot KeySlotID `json:"slot"`
	// Path is the derivation path currently in effect.
	Path string `json:"path"`
	// NextPath is what GEN KEY would rotate Path to — the same path with its
	// final index incremented. Returned so the operator SEES the proposed value
	// before committing it, rather than pressing a button that silently mutates
	// published identity.
	NextPath string `json:"next_path"`
	// Rotatable is false when the current path cannot be advanced (already at
	// the maximum index, or malformed). NextPath is empty when false.
	Rotatable bool `json:"rotatable"`
	// Reason explains a false Rotatable to the operator.
	Reason string `json:"reason,omitempty"`
	// XPubDerivable records whether this key's path is one an escrow/recovery
	// holder can reconstruct from the operator's private xpub. Under §21 the xpub
	// is no longer published, so this is a console-only operator-facing fact, not
	// a verifier-facing one. Both slots here are derivable; the node's ed25519
	// record-signing key is NOT, which is why it is not offered as a rotatable
	// slot (see the note on KeySlots below).
	XPubDerivable bool `json:"xpub_derivable"`
}

// KeySlots reports the operator-rotatable key slots for this node.
//
// # Why the ed25519 record-signing key is NOT listed
//
// The EPM's ed25519 signing key is what actually produces the record
// self-signature, and it is NOT xpub-derivable (SLIP-10 ed25519 has no public
// derivation — §17.8). Rotating it therefore:
//
//   - changes the key that verifies every future record, while old records stay
//     verifiable only against the key they each published;
//   - requires deriving a NEW ed25519 PRIVATE key at the new path, i.e. real
//     signing-identity rotation rather than a path relabel;
//   - is the key the directory's Ed25519 lookup and PNM/dataset publication
//     signatures depend on.
//
// That is a signing-identity change, which is Seal Council territory (joint
// Hermes/Hephaestus) and not something a console button should do unannounced.
// The two secp256k1 slots below are safe to rotate precisely because they carry
// no private-key consequence: the key is a pure function of xpub + path, so
// "rotating" is choosing a different derivable child.
func (s *Service) KeySlots() ([]KeySlot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.identity == nil {
		return nil, fmt.Errorf("node identity is not available")
	}
	// The paths IN EFFECT — the operator's edits when set, defaults otherwise.
	// Reading the constants here instead would make the widget disagree with
	// the published record the moment anyone edited a path.
	signPath, encPath := EffectiveKeyPaths(s.profile, s.identity.Account)

	slots := []KeySlot{
		{Slot: KeySlotSigning, Path: signPath, XPubDerivable: true},
		{Slot: KeySlotEncryption, Path: encPath, XPubDerivable: true},
	}
	for i := range slots {
		next, err := NextKeyPath(slots[i].Path, SlotXPubDerivable)
		if err != nil {
			slots[i].Rotatable = false
			slots[i].Reason = err.Error()
			continue
		}
		slots[i].Rotatable = true
		slots[i].NextPath = next
	}
	return slots, nil
}

// ProposeNextKeyPath is the GEN KEY computation for one slot: validate the
// current path for that slot and return the next one.
//
// It DERIVES NOTHING SECRET and returns no key material — only a path. The
// corresponding public key is reconstructible by anyone from the published xpub
// plus that path, which is the entire point of the paradigm, and the private
// key never exists outside the node's seed.
//
// The proposal is not applied here. Saving it is a separate, explicit operator
// action through the EPM edit flow, so GEN KEY can never silently re-publish the
// node's identity.
func (s *Service) ProposeNextKeyPath(slot KeySlotID) (current string, next string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.identity == nil {
		return "", "", fmt.Errorf("node identity is not available")
	}
	signPath, encPath := EffectiveKeyPaths(s.profile, s.identity.Account)

	switch KeySlotID(strings.TrimSpace(string(slot))) {
	case KeySlotSigning:
		current = signPath
	case KeySlotEncryption:
		current = encPath
	default:
		return "", "", fmt.Errorf("unknown key slot %q: expected %q or %q", slot, KeySlotSigning, KeySlotEncryption)
	}

	next, err = NextKeyPath(current, SlotXPubDerivable)
	if err != nil {
		return current, "", err
	}
	return current, next, nil
}
