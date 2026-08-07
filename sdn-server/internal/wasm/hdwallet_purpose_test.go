package wasm

// The derive-child-for-purpose contract.
//
// OWNER RULING 2026-08-07: derive-from-node-root is the DEFAULT for every
// server-side purpose key; external keys are the opt-in exception; provenance must
// be queryable; the fleet update root stays isolated.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"
)

// TestPurposeRegistryIsClosedAndCollisionFree — a purpose index reused by two
// lanes silently makes two duties share a key, which is the whole defect class.
func TestPurposeRegistryIsClosedAndCollisionFree(t *testing.T) {
	t.Parallel()

	purposes := RegisteredPurposes()
	if len(purposes) < 3 {
		t.Fatalf("registry has %d purposes, expected at least identity/encryption/grant", len(purposes))
	}

	seenIndex := map[KeyPurpose]bool{}
	seenLabel := map[string]bool{}
	for i, purpose := range purposes {
		if seenIndex[purpose] {
			t.Fatalf("purpose index %d appears twice in the registry", uint32(purpose))
		}
		seenIndex[purpose] = true

		label := purpose.Label()
		if label == "" {
			t.Fatalf("registered purpose %d has no label", uint32(purpose))
		}
		if seenLabel[label] {
			t.Fatalf("two purposes share the label %q — labels are identifiers, including inside the legacy KDF domain", label)
		}
		seenLabel[label] = true

		if purpose.Description() == "" {
			t.Fatalf("purpose %s has no operator-facing description; the key-management UI would have to invent one", purpose)
		}
		// Stable ascending order, so a UI diffing two responses sees real changes.
		if i > 0 && purposes[i-1] >= purpose {
			t.Fatalf("RegisteredPurposes is not ascending at index %d", i)
		}
	}

	// The three that exist today are pinned: they are published contract, and the
	// grant key's index in particular determines the verifier key every deployed
	// client holds.
	for purpose, want := range map[KeyPurpose]string{
		PurposeIdentitySigning: "identity-signing",
		PurposeEncryption:      "encryption",
		PurposeLicensingGrant:  "licensing-grant",
	} {
		if got := purpose.Label(); got != want {
			t.Fatalf("purpose %d label = %q, want %q", uint32(purpose), got, want)
		}
	}
	if PurposeIdentitySigning != 0 || PurposeEncryption != 1 || PurposeLicensingGrant != 2 {
		t.Fatal("purpose indices moved; every deployed client's pinned grant verifier key depends on these")
	}
}

// TestPurposeKeyPathMatchesTheLegacyConstants — the general contract and the
// grant-specific constant must agree, or the grant lane and anything using the
// generic helper would derive different keys for the same purpose.
func TestPurposeKeyPathMatchesTheLegacyConstants(t *testing.T) {
	t.Parallel()

	for _, account := range []uint32{0, 1, 7, 2147483647} {
		if got, want := PurposeKeyPath(PurposeIdentitySigning, account), fmt.Sprintf(SigningKeyPath, account); got != want {
			t.Fatalf("identity path for account %d: %q != %q", account, got, want)
		}
		if got, want := PurposeKeyPath(PurposeEncryption, account), fmt.Sprintf(EncryptionKeyPath, account); got != want {
			t.Fatalf("encryption path for account %d: %q != %q", account, got, want)
		}
		if got, want := PurposeKeyPath(PurposeLicensingGrant, account), fmt.Sprintf(LicensingGrantKeyPath, account); got != want {
			t.Fatalf("grant path for account %d: %q != %q", account, got, want)
		}
	}
}

// TestDeriveChildForPurposeRefusesUnregisteredPurposes is the fail-closed edge of
// the registry: a lane that picks an index locally gets an error naming what to do,
// not a working key that quietly collides with someone else's later choice.
func TestDeriveChildForPurposeRefusesUnregisteredPurposes(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed: %v", err)
	}

	for _, purpose := range []KeyPurpose{3, 4, 99, 1 << 20} {
		if purpose.Registered() {
			t.Fatalf("purpose %d is registered; update this test rather than the assertion", uint32(purpose))
		}
		if _, _, err := hw.DeriveChildForPurpose(ctx, seed, 0, purpose); err == nil {
			t.Fatalf("derived an UNREGISTERED purpose %d — that is how two duties end up sharing a key", uint32(purpose))
		}
	}
}

// TestDeriveChildForPurposeIsDeterministicAndSeparated — the two properties every
// consumer of this contract depends on: same identity gives the same key forever,
// and no two purposes ever give the same key.
func TestDeriveChildForPurposeIsDeterministicAndSeparated(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed: %v", err)
	}

	byPurpose := map[KeyPurpose][]byte{}
	for _, purpose := range RegisteredPurposes() {
		if purpose == PurposeEncryption {
			continue // X25519, not derived through this Ed25519 helper
		}
		priv, _, err := hw.DeriveChildForPurpose(ctx, seed, 0, purpose)
		if err != nil {
			t.Fatalf("derive %s: %v", purpose, err)
		}
		raw, err := priv.Raw()
		if err != nil {
			t.Fatalf("raw %s: %v", purpose, err)
		}
		byPurpose[purpose] = raw[:ed25519.SeedSize]

		// Determinism.
		again, _, err := hw.DeriveChildForPurpose(ctx, seed, 0, purpose)
		if err != nil {
			t.Fatalf("re-derive %s: %v", purpose, err)
		}
		againRaw, _ := again.Raw()
		if !bytes.Equal(raw, againRaw) {
			t.Fatalf("%s is not deterministic across derivations", purpose)
		}
	}

	// Separation: every pair differs.
	for a, ka := range byPurpose {
		for b, kb := range byPurpose {
			if a != b && bytes.Equal(ka, kb) {
				t.Fatalf("purposes %s and %s derived the SAME key", a, b)
			}
		}
	}
}

// TestLegacyPurposeSeedIsPurposeSeparated — nodes with no HD seed must get the same
// separation guarantee, or the contract only holds on some of the fleet.
func TestLegacyPurposeSeedIsPurposeSeparated(t *testing.T) {
	t.Parallel()

	parent := bytes.Repeat([]byte{0x5a}, ed25519.SeedSize)

	seen := map[string]KeyPurpose{}
	for _, purpose := range RegisteredPurposes() {
		seed := DeriveLegacyPurposeSeed(parent, purpose)
		if len(seed) != ed25519.SeedSize {
			t.Fatalf("legacy seed for %s is %d bytes", purpose, len(seed))
		}
		if bytes.Equal(seed, parent) {
			t.Fatalf("legacy seed for %s IS the parent identity key", purpose)
		}
		if again := DeriveLegacyPurposeSeed(parent, purpose); !bytes.Equal(seed, again) {
			t.Fatalf("legacy seed for %s is not deterministic", purpose)
		}
		if prior, ok := seen[string(seed)]; ok {
			t.Fatalf("purposes %s and %s derived the same legacy seed", prior, purpose)
		}
		seen[string(seed)] = purpose

		// The domain is versioned, so the derivation can never be silently changed
		// under a fleet that already published the matching public key.
		if domain := LegacyPurposeKDFDomain(purpose); domain == "" || domain[len(domain)-3:] != "-V1" {
			t.Fatalf("legacy KDF domain for %s is not versioned: %q", purpose, domain)
		}
	}

	// Unregistered purposes and short parents produce nothing, so a caller cannot
	// accidentally get a weak or colliding key.
	if got := DeriveLegacyPurposeSeed(parent, KeyPurpose(99)); got != nil {
		t.Fatalf("legacy derivation accepted an unregistered purpose: %x", got)
	}
	if got := DeriveLegacyPurposeSeed(parent[:8], PurposeLicensingGrant); got != nil {
		t.Fatalf("legacy derivation accepted a short parent: %x", got)
	}
}

// TestPurposeKeysReportProvenanceAndTheUpdateRoot — the queryable-provenance ask.
// A UI must be able to say "signing with your node root key" and must be able to
// warn before touching fleet code authority.
func TestPurposeKeysReportProvenanceAndTheUpdateRoot(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed: %v", err)
	}
	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity: %v", err)
	}

	keys := identity.PurposeKeys()
	if len(keys) != 3 {
		t.Fatalf("PurposeKeys returned %d entries, want identity/encryption/grant", len(keys))
	}

	updateRoots := 0
	for _, key := range keys {
		if key.Provenance != ProvenanceDerivedFromNodeRoot {
			t.Fatalf("%s provenance = %q, want derived-from-node-root (the owner's DEFAULT)", key.Purpose, key.Provenance)
		}
		if !key.Provenance.Reproducible() {
			t.Fatalf("%s is reported as not reproducible, but it is derived from the mnemonic", key.Purpose)
		}
		if len(key.PublicKey) == 0 {
			t.Fatalf("%s carries no public key", key.Purpose)
		}
		if key.Path == "" {
			t.Fatalf("%s carries no derivation path", key.Purpose)
		}
		if key.IsUpdateRoot {
			updateRoots++
			if key.Purpose != PurposeIdentitySigning {
				t.Fatalf("%s is flagged as the fleet update root; only the identity signing key may be", key.Purpose)
			}
		}
	}
	if updateRoots != 1 {
		t.Fatalf("%d keys claim to be the fleet update root; exactly one may", updateRoots)
	}

	// External provenance is the only non-reproducible one — that is the flag a UI
	// turns into "you must back this up yourself".
	if ProvenanceExternalConfigured.Reproducible() {
		t.Fatal("an externally configured key must not be reported as reproducible from the node mnemonic")
	}
}
