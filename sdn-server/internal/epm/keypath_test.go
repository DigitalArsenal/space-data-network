package epm

// Locks the derivation-path rules behind the operator-editable SIGNING PATH /
// ENCRYPTION PATH fields and the GEN KEY button (§18 of
// graph/tasks/nst-node-admin-contract.md).
//
// The two slots require OPPOSITE hardening, and getting either backwards
// produces a record that looks fine and verifies nowhere — which is exactly the
// failure a hand-editable field invites.

import (
	"strings"
	"testing"
)

func TestParseKeyPathAcceptsCanonicalForms(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"m/44'/0'/0'/0/0":   "m/44'/0'/0'/0/0",
		"m/44'/0'/0'/0'/0'": "m/44'/0'/0'/0'/0'",
		"m/44h/0h/0h/0/0":   "m/44'/0'/0'/0/0", // h and H are accepted on input
		"m/44H/0H/0H/1/7":   "m/44'/0'/0'/1/7",
		"M/44'/0'/0'/0/0":   "m/44'/0'/0'/0/0",
		" m/44'/0'/0'/0/0 ": "m/44'/0'/0'/0/0",
	}
	for in, want := range cases {
		in, want := in, want
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			components, err := ParseKeyPath(in)
			if err != nil {
				t.Fatalf("ParseKeyPath(%q): %v", in, err)
			}
			if got := FormatKeyPath(components); got != want {
				t.Fatalf("round trip = %q, want %q", got, want)
			}
		})
	}
}

// TestParseKeyPathRefusesGarbage locks that an operator-supplied path which is
// not a path is refused outright. This text ends up signed into a published
// record, so lenient parsing would let a typo travel the network.
func TestParseKeyPathRefusesGarbage(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]string{
		"empty":            "",
		"whitespace":       "   ",
		"no m prefix":      "44'/0'/0'/0/0",
		"bare m":           "m",
		"trailing slash":   "m/44'/0'/0'/0/",
		"double slash":     "m/44'//0'/0/0",
		"negative":         "m/44'/-1/0'/0/0",
		"signed plus":      "m/44'/+1/0'/0/0",
		"non numeric":      "m/44'/abc/0'/0/0",
		"hex":              "m/44'/0x10/0'/0/0",
		"index too large":  "m/44'/0'/0'/0/2147483648",
		"way too deep":     "m/1/2/3/4/5/6/7/8/9",
		"lone apostrophe":  "m/44'/'/0'/0/0",
		"words":            "m/not/a/path",
		"path with spaces": "m/44' /0'/0'/0/0",
	} {
		name, in := name, in
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseKeyPath(in); err == nil {
				t.Fatalf("ParseKeyPath(%q) was accepted", in)
			}
		})
	}
}

// TestValidateKeyPathEnforcesOppositeHardening is the core rule. An
// xpub-derivable key must be NON-hardened below the account (BIP-32 public
// derivation cannot produce a hardened child); a SLIP-10 ed25519 key must be
// hardened everywhere (it has no public derivation at all).
func TestValidateKeyPathEnforcesOppositeHardening(t *testing.T) {
	t.Parallel()

	t.Run("xpub-derivable accepts non-hardened below the account", func(t *testing.T) {
		t.Parallel()
		for _, ok := range []string{
			"m/44'/0'/0'/0/0",
			"m/44'/0'/0'/1/0",
			"m/44'/0'/7'/0/12",
			"m/44'/60'/0'/0/0",
		} {
			if err := ValidateKeyPath(ok, SlotXPubDerivable); err != nil {
				t.Fatalf("ValidateKeyPath(%q, xpub-derivable): %v", ok, err)
			}
		}
	})

	t.Run("xpub-derivable REFUSES hardened below the account", func(t *testing.T) {
		t.Parallel()
		for _, bad := range []string{
			"m/44'/0'/0'/0'/0",  // hardened change level
			"m/44'/0'/0'/0/0'",  // hardened index
			"m/44'/0'/0'/0'/0'", // the SLIP-10 shape, in the wrong slot
		} {
			err := ValidateKeyPath(bad, SlotXPubDerivable)
			if err == nil {
				t.Fatalf("ValidateKeyPath(%q, xpub-derivable) was accepted; it cannot be derived from a published xpub", bad)
			}
			if !strings.Contains(err.Error(), "hardened") {
				t.Fatalf("error should explain the hardening problem, got: %v", err)
			}
		}
	})

	t.Run("xpub-derivable requires a hardened BIP-44 account prefix", func(t *testing.T) {
		t.Parallel()
		for _, bad := range []string{
			"m/44/0'/0'/0/0",
			"m/44'/0/0'/0/0",
			"m/44'/0'/0/0/0",
		} {
			if err := ValidateKeyPath(bad, SlotXPubDerivable); err == nil {
				t.Fatalf("ValidateKeyPath(%q) accepted a non-hardened account prefix", bad)
			}
		}
	})

	t.Run("xpub-derivable must descend below the account", func(t *testing.T) {
		t.Parallel()
		for _, bad := range []string{"m/44'", "m/44'/0'", "m/44'/0'/0'"} {
			if err := ValidateKeyPath(bad, SlotXPubDerivable); err == nil {
				t.Fatalf("ValidateKeyPath(%q) accepted a path with nothing to derive", bad)
			}
		}
	})

	t.Run("slip10 ed25519 accepts fully hardened", func(t *testing.T) {
		t.Parallel()
		for _, ok := range []string{
			"m/44'/0'/0'/0'/0'",
			"m/44'/0'/0'/1'/0'",
			"m/44'/0'/7'/2'/3'",
		} {
			if err := ValidateKeyPath(ok, SlotSLIP10Ed25519); err != nil {
				t.Fatalf("ValidateKeyPath(%q, slip10): %v", ok, err)
			}
		}
	})

	t.Run("slip10 ed25519 REFUSES any non-hardened component", func(t *testing.T) {
		t.Parallel()
		for _, bad := range []string{
			"m/44'/0'/0'/0/0",  // the xpub-derivable shape, in the wrong slot
			"m/44'/0'/0'/0'/0", // last component only
			"m/44/0'/0'/0'/0'",
		} {
			err := ValidateKeyPath(bad, SlotSLIP10Ed25519)
			if err == nil {
				t.Fatalf("ValidateKeyPath(%q, slip10) was accepted; SLIP-10 ed25519 has no public derivation", bad)
			}
			if !strings.Contains(err.Error(), "hardened") {
				t.Fatalf("error should explain the hardening problem, got: %v", err)
			}
		}
	})
}

// TestNextKeyPathRotatesOnlyTheFinalIndex is what GEN KEY does. It must move
// sideways within a slot and nothing else: changing the depth or the hardening
// would change which slot the path belongs to, and changing the ACCOUNT would
// move the node's whole identity.
func TestNextKeyPathRotatesOnlyTheFinalIndex(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		slot KeyPathSlot
		want string
	}{
		{"m/44'/0'/0'/0/0", SlotXPubDerivable, "m/44'/0'/0'/0/1"},
		{"m/44'/0'/0'/1/0", SlotXPubDerivable, "m/44'/0'/0'/1/1"},
		{"m/44'/0'/0'/0/41", SlotXPubDerivable, "m/44'/0'/0'/0/42"},
		{"m/44'/0'/0'/0'/0'", SlotSLIP10Ed25519, "m/44'/0'/0'/0'/1'"},
		{"m/44'/0'/7'/1'/9'", SlotSLIP10Ed25519, "m/44'/0'/7'/1'/10'"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := NextKeyPath(tc.in, tc.slot)
			if err != nil {
				t.Fatalf("NextKeyPath(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NextKeyPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// The account level must be untouched: rotating a key must never
			// move the node to a different account.
			inParts, _ := ParseKeyPath(tc.in)
			gotParts, _ := ParseKeyPath(got)
			for i := 0; i < 3; i++ {
				if inParts[i] != gotParts[i] {
					t.Fatalf("rotation changed account-level component %d: %v -> %v", i+1, inParts[i], gotParts[i])
				}
			}
			if len(inParts) != len(gotParts) {
				t.Fatalf("rotation changed the path depth")
			}
			if inParts[len(inParts)-1].Hardened != gotParts[len(gotParts)-1].Hardened {
				t.Fatalf("rotation changed the hardening of the final component")
			}
		})
	}
}

// TestNextKeyPathRefusesToRotateAnInvalidPath locks that GEN KEY cannot be used
// to launder a bad path into a saved one.
func TestNextKeyPathRefusesToRotateAnInvalidPath(t *testing.T) {
	t.Parallel()

	if _, err := NextKeyPath("m/44'/0'/0'/0'/0'", SlotXPubDerivable); err == nil {
		t.Fatal("rotated a hardened path in the xpub-derivable slot")
	}
	if _, err := NextKeyPath("m/44'/0'/0'/0/0", SlotSLIP10Ed25519); err == nil {
		t.Fatal("rotated a non-hardened path in the slip10 slot")
	}
	if _, err := NextKeyPath("not a path", SlotXPubDerivable); err == nil {
		t.Fatal("rotated garbage")
	}
	if _, err := NextKeyPath("m/44'/0'/0'/0/2147483647", SlotXPubDerivable); err == nil {
		t.Fatal("rotated past the maximum index instead of refusing")
	}
}

// TestKeySlotsOfferOnlyXPubDerivableSlots locks §18's scope: GEN KEY is offered
// for the two secp256k1 slots, whose rotation costs nothing because the key is
// a pure function of xpub + path — and NOT for the ed25519 record-signing key,
// whose rotation is a signing-identity change (Seal Council, §17.8).
func TestKeySlotsOfferOnlyXPubDerivableSlots(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity: %v", err)
	}
	svc := NewService(identity, nil, identity.PeerID, "xpub-test", t.TempDir())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	slots, err := svc.KeySlots()
	if err != nil {
		t.Fatalf("KeySlots: %v", err)
	}
	if len(slots) != 2 {
		t.Fatalf("got %d slots, want exactly 2 (signing, encryption): %+v", len(slots), slots)
	}

	seen := map[KeySlotID]KeySlot{}
	for _, s := range slots {
		seen[s.Slot] = s
	}
	for _, want := range []KeySlotID{KeySlotSigning, KeySlotEncryption} {
		slot, ok := seen[want]
		if !ok {
			t.Fatalf("missing slot %q", want)
		}
		if !slot.XPubDerivable {
			t.Fatalf("slot %q is not marked xpub-derivable; only derivable slots are rotatable here", want)
		}
		if !slot.Rotatable || slot.NextPath == "" {
			t.Fatalf("slot %q is not rotatable: %+v", want, slot)
		}
		// The proposal must be a legal path for the slot, and must differ.
		if err := ValidateKeyPath(slot.NextPath, SlotXPubDerivable); err != nil {
			t.Fatalf("slot %q proposed an invalid next path %q: %v", want, slot.NextPath, err)
		}
		if slot.NextPath == slot.Path {
			t.Fatalf("slot %q proposed the same path", want)
		}
	}
	// The ed25519 record-signing key must NOT be offered as a rotatable slot.
	for _, s := range slots {
		if strings.Contains(s.Path, "'/0'/0'") && !s.XPubDerivable {
			t.Fatalf("a non-derivable (SLIP-10) slot was offered for rotation: %+v", s)
		}
	}
}

// TestProposeNextKeyPathReturnsPathsNotKeys locks the custody property: GEN KEY
// returns a PATH. It never returns key material, and the seed never leaves the
// process — the public key is reconstructible by anyone from xpub + path.
func TestProposeNextKeyPathReturnsPathsNotKeys(t *testing.T) {
	t.Parallel()

	identity, err := testDerivedIdentity()
	if err != nil {
		t.Fatalf("testDerivedIdentity: %v", err)
	}
	svc := NewService(identity, nil, identity.PeerID, "xpub-test", t.TempDir())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	current, next, err := svc.ProposeNextKeyPath(KeySlotSigning)
	if err != nil {
		t.Fatalf("ProposeNextKeyPath: %v", err)
	}
	if !strings.HasPrefix(current, "m/") || !strings.HasPrefix(next, "m/") {
		t.Fatalf("expected derivation paths, got current=%q next=%q", current, next)
	}
	if current == next {
		t.Fatal("GEN KEY proposed no change")
	}
	// A path is short and structural; key material is long hex/base64. This is
	// a crude but effective guard against someone "helpfully" returning the key.
	for _, v := range []string{current, next} {
		if len(v) > 64 {
			t.Fatalf("value %q is too long to be a derivation path — key material must never be returned", v)
		}
	}

	if _, _, err := svc.ProposeNextKeyPath("ed25519-signing"); err == nil {
		t.Fatal("a non-derivable slot was accepted for rotation")
	}
	if _, _, err := svc.ProposeNextKeyPath("garbage"); err == nil {
		t.Fatal("an unknown slot was accepted")
	}
}
