package node

// Key-domain separation between the LICENSING GRANT signer and the FLEET UPDATE /
// PUBLISHER root.
//
// OWNER RULING 2026-08-07, verbatim: "derive a grant-signing child from the node
// identity, keep the update root isolated"
// (graph/tasks/sdn-grant-verifier-key-domain-separation.md).
//
// The defect these tests exist to make impossible: one ed25519 key
// (fleet-trust-roots.json key_id d4a971a7e534) simultaneously signed
// SDN-UPDATE-MANIFEST-V1 — whatever it signs, every box in the fleet installs and
// runs — and every licensing grant issued to every anonymous browser. Nothing in
// either suite asserted the two were distinct, which is why it survived until a
// trust-root key_id was decoded by hand.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	lcf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCF"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// licensingConfigProviderSigningPublicKey pulls the advertised grant verifier key
// out of a licensing config frame — the field the 02f012b1 fix started populating
// and the only channel by which a client learns which key to verify grants with.
func licensingConfigProviderSigningPublicKey(t *testing.T, frame []byte) []byte {
	t.Helper()
	cfg := lcf.GetRootAsLCF(frame, 0)
	signingKey := cfg.PROVIDER_SIGNING_KEY(nil)
	if signingKey == nil {
		t.Fatal("PROVIDER_SIGNING_KEY is absent from the licensing config frame")
	}
	pub := signingKey.PUBLIC_KEYBytes()
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("PROVIDER_SIGNING_KEY.PUBLIC_KEY must be %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	return pub
}

// TestLicensingGrantKeyPathIsItsOwnHardenedBranch pins the derivation path itself.
// The path is a published contract: it is recorded in the node identity contract,
// it determines the verifier key every deployed client has pinned via KRF, and a
// silent change to it invalidates every grant in flight. It is also the thing that
// makes the ruling safe — fully hardened means SLIP-0010 Ed25519 admits it and
// publishing the child pubkey discloses nothing about parent or siblings.
func TestLicensingGrantKeyPathIsItsOwnHardenedBranch(t *testing.T) {
	t.Parallel()

	if got, want := wasm.LicensingGrantKeyPath, "m/44'/0'/%d'/2'/0'"; got != want {
		t.Fatalf("LicensingGrantKeyPath = %q, want %q — this path is a published contract, not an implementation detail", got, want)
	}
	if wasm.LicensingGrantKeyPath == wasm.SigningKeyPath {
		t.Fatal("the grant signing path is the identity signing path — that is the collision this task removed")
	}
	if wasm.LicensingGrantKeyPath == wasm.EncryptionKeyPath {
		t.Fatal("the grant signing path collides with the encryption path")
	}
}

// TestLegacyGrantSigningSeedIsDeterministicAndSeparated is the derivation
// determinism property for the LEGACY (non-HD) identity path: same identity in,
// same child out, every time, and never the parent.
func TestLegacyGrantSigningSeedIsDeterministicAndSeparated(t *testing.T) {
	t.Parallel()

	parent := make([]byte, ed25519.SeedSize)
	for i := range parent {
		parent[i] = byte(i * 3)
	}

	first := legacyGrantSigningSeed(parent)
	if len(first) != ed25519.SeedSize {
		t.Fatalf("legacyGrantSigningSeed returned %d bytes, want %d", len(first), ed25519.SeedSize)
	}

	// Determinism: the node must derive the same grant key across restarts, or
	// every restart silently invalidates the verifier key clients hold.
	for i := 0; i < 8; i++ {
		if again := legacyGrantSigningSeed(parent); !bytes.Equal(first, again) {
			t.Fatalf("legacyGrantSigningSeed is not deterministic: run %d gave %x, want %x", i, again, first)
		}
	}

	// Separation: the child is never the parent.
	if bytes.Equal(first, parent) {
		t.Fatal("the derived grant seed IS the parent identity seed")
	}

	// A different identity must give a different child, or the "child" carries no
	// identity at all.
	other := make([]byte, ed25519.SeedSize)
	copy(other, parent)
	other[0] ^= 0xFF
	if bytes.Equal(legacyGrantSigningSeed(other), first) {
		t.Fatal("two different identities derived the same grant signing seed")
	}

	// Short input is refused rather than padded into a weak key.
	if got := legacyGrantSigningSeed(parent[:16]); got != nil {
		t.Fatalf("legacyGrantSigningSeed accepted a %d-byte parent, got %x", 16, got)
	}
}

// TestGrantSigningKeyDomainConflictDetectsCollisions is the guard's own truth
// table. It must be silent when the keys are separated and loud when they are not
// — a guard that reports a conflict on the healthy case would be turned off.
func TestGrantSigningKeyDomainConflictDetectsCollisions(t *testing.T) {
	t.Parallel()

	updateRoot := make([]byte, ed25519.SeedSize)
	for i := range updateRoot {
		updateRoot[i] = byte(0x40 + i)
	}
	grant := legacyGrantSigningSeed(updateRoot)

	if reason := grantSigningKeyDomainConflict(grant, updateRoot); reason != "" {
		t.Fatalf("properly separated keys reported a conflict: %s", reason)
	}

	// The collision that shipped: the same seed in both lanes.
	reason := grantSigningKeyDomainConflict(updateRoot, updateRoot)
	if reason == "" {
		t.Fatal("identical seeds in the grant lane and the update lane were NOT reported as a conflict")
	}
	if !bytes.Contains([]byte(reason), []byte("BYTE-IDENTICAL")) {
		t.Fatalf("collision reason must name what collided, got %q", reason)
	}

	// The update lane hands the guard a 64-byte libp2p raw key (seed||pub); the
	// guard must compare on the seed half, not fall through as "no update root".
	full := ed25519.NewKeyFromSeed(updateRoot)
	if reason := grantSigningKeyDomainConflict(updateRoot, full); reason == "" {
		t.Fatal("a 64-byte update-root private key hiding the same seed was not detected as a collision")
	}
	if reason := grantSigningKeyDomainConflict(grant, full); reason != "" {
		t.Fatalf("separated keys reported a conflict against a 64-byte update root: %s", reason)
	}

	// No update root at all (no HD identity, so no update signing endpoint is
	// registered) is not a conflict — there is no second authority to collide with.
	if reason := grantSigningKeyDomainConflict(grant, nil); reason != "" {
		t.Fatalf("absent update root reported a conflict: %s", reason)
	}
}

// collidingIdentity builds a DerivedIdentity whose grant key IS its signing key —
// i.e. the exact shape the fleet shipped before the ruling. Constructed directly
// rather than derived, because the whole point is to produce a state the real
// derivation can no longer produce.
func collidingIdentity(t *testing.T) *wasm.DerivedIdentity {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(0x90 + i)
	}
	priv, pub, err := crypto.GenerateEd25519Key(bytes.NewReader(seed))
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	return &wasm.DerivedIdentity{
		SigningPrivKey: priv,
		SigningPubKey:  pub,
		// THE COLLISION: same key in both lanes.
		GrantSigningPrivKey: priv,
		GrantSigningPubKey:  pub,
		GrantSigningKeyPath: "m/44'/0'/0'/0'/0'",
		EncryptionKey:       bytes.Repeat([]byte{0x5c}, 32),
	}
}

// TestBuildModuleNodeContextRefusesCollidingGrantKey is the STRUCTURAL guard end
// to end, through the real code path: when the two lanes would share a key the
// slot is NOT provisioned, the refusal is recorded with a reason, and the
// licensing runtime then refuses to configure. Fail closed — a node in this state
// serves no grants at all, and says why.
func TestBuildModuleNodeContextRefusesCollidingGrantKey(t *testing.T) {
	t.Parallel()

	n := &Node{
		identity: collidingIdentity(t),
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(t.TempDir(), "data")},
		},
	}

	nodeCtx, err := n.buildModuleNodeContext()
	if err != nil {
		t.Fatalf("buildModuleNodeContext() failed: %v", err)
	}
	nodeCtx.PeerID = "16Uiu2HAmTestPeerIDForDomainSeparation" // no libp2p host in this unit

	// The colliding slot must not exist. Absence is the enforcement; nothing can
	// sign with a slot that was never provisioned.
	if got, ok := nodeCtx.KeySlots[providerSigningSlotID]; ok {
		t.Fatalf("provider signing slot was provisioned with a colliding key: %x", got)
	}
	if nodeCtx.KeySlotAlgorithms[providerSigningSlotID] != "" {
		t.Fatal("a refused slot must not carry an algorithm declaration")
	}
	// The wrapping slot is unaffected — the refusal is scoped to the lane at fault.
	if len(nodeCtx.KeySlots[providerWrappingSlotID]) != 32 {
		t.Fatal("the wrapping slot was collateral damage of the grant-key refusal")
	}

	reason := nodeCtx.KeySlotRefusals[providerSigningSlotID]
	if reason == "" {
		t.Fatal("the refusal carries no reason — an operator cannot tell it from a missing key")
	}

	// And the fail-closed end: the licensing runtime cannot be configured, so no
	// grant can be issued at all.
	_, err = buildLicensingRuntimeConfigFrame(nodeCtx)
	if err == nil {
		t.Fatal("licensing runtime configured despite a refused provider signing slot — grants would still be issued")
	}
	if !strings.Contains(err.Error(), "REFUSED") {
		t.Fatalf("refusal error must be distinguishable from a missing slot, got %q", err)
	}
	if !strings.Contains(err.Error(), licensingGrantKeyPathLabel) {
		t.Fatalf("refusal must tell the operator where the grant key belongs, got %q", err)
	}

	// GrantSigningKey must refuse too, or the storefront lane would keep signing
	// grants with the update root through a different door.
	if got := n.GrantSigningKey(); got != nil {
		t.Fatalf("GrantSigningKey() handed out a colliding key: %x", got)
	}
}

// TestBuildModuleNodeContextProvisionsSeparatedGrantKey is the healthy case, and
// the reason the guard is safe to enable: a properly derived child provisions
// normally and produces a working licensing config frame.
func TestBuildModuleNodeContextProvisionsSeparatedGrantKey(t *testing.T) {
	t.Parallel()

	signingSeed := make([]byte, ed25519.SeedSize)
	for i := range signingSeed {
		signingSeed[i] = byte(0x20 + i)
	}
	grantSeed := legacyGrantSigningSeed(signingSeed)

	signingPriv, signingPub, err := crypto.GenerateEd25519Key(bytes.NewReader(signingSeed))
	if err != nil {
		t.Fatalf("GenerateEd25519Key(signing): %v", err)
	}
	grantPriv, grantPub, err := crypto.GenerateEd25519Key(bytes.NewReader(grantSeed))
	if err != nil {
		t.Fatalf("GenerateEd25519Key(grant): %v", err)
	}

	n := &Node{
		identity: &wasm.DerivedIdentity{
			SigningPrivKey:      signingPriv,
			SigningPubKey:       signingPub,
			GrantSigningPrivKey: grantPriv,
			GrantSigningPubKey:  grantPub,
			GrantSigningKeyPath: "m/44'/0'/0'/2'/0'",
			EncryptionKey:       bytes.Repeat([]byte{0x5c}, 32),
		},
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(t.TempDir(), "data")},
		},
	}

	nodeCtx, err := n.buildModuleNodeContext()
	if err != nil {
		t.Fatalf("buildModuleNodeContext() failed: %v", err)
	}
	nodeCtx.PeerID = "16Uiu2HAmTestPeerIDForDomainSeparation" // no libp2p host in this unit
	if len(nodeCtx.KeySlotRefusals) != 0 {
		t.Fatalf("a properly separated grant key was refused: %v", nodeCtx.KeySlotRefusals)
	}
	slot := nodeCtx.KeySlots[providerSigningSlotID]
	if !bytes.Equal(slot, grantSeed) {
		t.Fatalf("provider signing slot = %x, want the grant child %x", slot, grantSeed)
	}
	if bytes.Equal(slot, signingSeed) {
		t.Fatal("provider signing slot holds the fleet update/publisher root")
	}
	if got := n.GrantSigningKey(); !bytes.Equal(got, grantSeed) {
		t.Fatalf("GrantSigningKey() = %x, want %x — the licensing and storefront lanes must advertise ONE grant verifier key", got, grantSeed)
	}

	frame, err := buildLicensingRuntimeConfigFrame(nodeCtx)
	if err != nil {
		t.Fatalf("buildLicensingRuntimeConfigFrame: %v", err)
	}
	advertised := licensingConfigProviderSigningPublicKey(t, frame)
	if want := ed25519.NewKeyFromSeed(grantSeed).Public().(ed25519.PublicKey); !bytes.Equal(advertised, want) {
		t.Fatalf("advertised verifier key %x != grant child public key %x", advertised, want)
	}
	if updateRootPub := ed25519.NewKeyFromSeed(signingSeed).Public().(ed25519.PublicKey); bytes.Equal(advertised, updateRootPub) {
		t.Fatal("the advertised grant verifier key is the fleet update root")
	}
}

// TestGrantRoundTripVerifiesAgainstKRFAdvertisedPubkey is the property the browser
// actually depends on: a grant signed by whatever is in the provider signing slot
// verifies against the pubkey the host advertised in KRF.PUBLIC_KEY — and that
// pubkey is the CHILD's, not the update root's.
func TestGrantRoundTripVerifiesAgainstKRFAdvertisedPubkey(t *testing.T) {
	t.Parallel()

	updateRoot := make([]byte, ed25519.SeedSize)
	for i := range updateRoot {
		updateRoot[i] = byte(0x11 * (i%15 + 1))
	}
	grantSeed := legacyGrantSigningSeed(updateRoot)

	frame, err := buildLicensingRuntimeConfigFrame(&modulert.NodeContext{
		PeerID: "16Uiu2HAmTestPeerIDForDomainSeparation",
		KeySlots: map[string][]byte{
			providerSigningSlotID:  grantSeed,
			providerWrappingSlotID: bytes.Repeat([]byte{0x5c}, 32),
		},
	})
	if err != nil {
		t.Fatalf("buildLicensingRuntimeConfigFrame: %v", err)
	}

	advertised := licensingConfigProviderSigningPublicKey(t, frame)

	// The advertised verifier key is the CHILD's.
	wantChild := ed25519.NewKeyFromSeed(grantSeed).Public().(ed25519.PublicKey)
	if !bytes.Equal(advertised, wantChild) {
		t.Fatalf("KRF.PUBLIC_KEY = %x, want the grant child's public key %x", advertised, wantChild)
	}

	// And it is NOT the update root's. This is the whole point: the descriptor may
	// now be advertised without widening the cluster's code-authority key.
	updateRootPub := ed25519.NewKeyFromSeed(updateRoot).Public().(ed25519.PublicKey)
	if bytes.Equal(advertised, updateRootPub) {
		t.Fatal("KRF.PUBLIC_KEY advertises the FLEET UPDATE ROOT public key")
	}

	// Round trip, exactly as a browser does it.
	grant := []byte("$LGR grant body signed by the provider signing slot")
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(grantSeed), grant)
	if !ed25519.Verify(ed25519.PublicKey(advertised), grant, sig) {
		t.Fatal("a grant signed by the provider signing slot does not verify against the advertised verifier key")
	}
	// A grant forged with the update root must NOT verify against the grant key.
	forged := ed25519.Sign(ed25519.NewKeyFromSeed(updateRoot), grant)
	if ed25519.Verify(ed25519.PublicKey(advertised), grant, forged) {
		t.Fatal("a signature made with the update root verified against the grant verifier key — the keys are not separated")
	}
}

// TestDirectoryEd25519SelectorIgnoresTheGrantKey is HEPHAESTUS's Q2b trap, closed.
//
// ed25519PublicKeyFromDirectoryJSON picks the key that verifies DATASET
// PUBLICATIONS. It used to take the FIRST ed25519 signing entry, which was safe
// only while a node advertised exactly one. The node now also advertises the
// licensing grant verifier key as an ed25519 Signing key — it must, or clients
// cannot verify grants — so "first" would be decided by vector order, and picking
// the grant key here would make peers reject this node's OMM/OCM fleet-wide.
func TestDirectoryEd25519SelectorIgnoresTheGrantKey(t *testing.T) {
	t.Parallel()

	publicationPub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	grantPub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x22}, ed25519.SeedSize)).Public().(ed25519.PublicKey)

	entry := func(pub ed25519.PublicKey, path string) string {
		return fmt.Sprintf(`{"key_type":"signing","address_type":"ed25519","public_key":%q,"key_address":%q}`, hex.EncodeToString(pub), path)
	}
	publication := entry(publicationPub, "m/44'/0'/0'/0'/0'")
	grant := entry(grantPub, "m/44'/0'/0'/2'/0'")

	// BOTH orders must select the publication key. Order-dependence here is the
	// whole defect.
	for name, keys := range map[string]string{
		"publication first": publication + "," + grant,
		"grant first":       grant + "," + publication,
	} {
		got, err := ed25519PublicKeyFromDirectoryJSON(`{"keys":[` + keys + `]}`)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !got.Equal(publicationPub) {
			t.Fatalf("%s: selected %x, want the publication key %x", name, got, publicationPub)
		}
	}

	// A record carrying ONLY a grant key has no publication key — say so, do not
	// hand back the grant key.
	if _, err := ed25519PublicKeyFromDirectoryJSON(`{"keys":[` + grant + `]}`); err == nil {
		t.Fatal("a record advertising only the grant key yielded a dataset publication key")
	}

	// Genuine ambiguity (two different non-grant signing keys) fails closed rather
	// than picking one. A wrong key here fails at every peer and is indistinguishable
	// from a corrupt signature.
	other := entry(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x33}, ed25519.SeedSize)).Public().(ed25519.PublicKey), "sdn/runtime-signing")
	if _, err := ed25519PublicKeyFromDirectoryJSON(`{"keys":[` + publication + "," + other + `]}`); err == nil {
		t.Fatal("two different candidate publication keys were resolved by guessing instead of refused")
	}

	// Back-compat: a pre-existing single-key record, and a record with an
	// unparseable path, both still resolve. The purpose filter is a DENY list
	// precisely so it can never discard the only candidate.
	legacy := entry(publicationPub, "")
	if got, err := ed25519PublicKeyFromDirectoryJSON(`{"keys":[` + legacy + `]}`); err != nil || !got.Equal(publicationPub) {
		t.Fatalf("a legacy single-key record no longer resolves: %x %v", got, err)
	}
}

// TestDirectoryKeyPathPurposeClassifier pins the classifier's edges. It must
// recognise the grant purpose exactly and treat everything it does not understand
// as a candidate.
func TestDirectoryKeyPathPurposeClassifier(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"m/44'/0'/0'/2'/0'", "m/44'/0'/7'/2'/0'", "m/44h/0h/0h/2h/0h"} {
		if !directoryKeyPathIsNonPublicationPurpose(path) {
			t.Fatalf("%q is the licensing grant path and was not recognised", path)
		}
	}
	for _, path := range []string{
		"m/44'/0'/0'/0'/0'", // identity signing / update root
		"m/44'/0'/0'/1'/0'", // encryption
		"m/44'/0'/0'/0/0",   // legacy auth
		"sdn/runtime-signing",
		"",
		"m/44'/0'/0'", // too shallow to classify
	} {
		if directoryKeyPathIsNonPublicationPurpose(path) {
			t.Fatalf("%q was wrongly excluded from the publication-key candidates", path)
		}
	}
}

// TestServerManagedKeysReportsProvenanceAndRefusals is the owner's second ask
// (2026-08-07): per-key provenance must be QUERYABLE, because the key-management
// UI has to annotate "signing with the node root key" states and attribute bond
// value to the right key.
func TestServerManagedKeysReportsProvenanceAndRefusals(t *testing.T) {
	t.Parallel()

	signingSeed := make([]byte, ed25519.SeedSize)
	for i := range signingSeed {
		signingSeed[i] = byte(0x20 + i)
	}
	grantSeed := legacyGrantSigningSeed(signingSeed)

	signingPriv, signingPub, err := crypto.GenerateEd25519Key(bytes.NewReader(signingSeed))
	if err != nil {
		t.Fatalf("GenerateEd25519Key(signing): %v", err)
	}
	grantPriv, grantPub, err := crypto.GenerateEd25519Key(bytes.NewReader(grantSeed))
	if err != nil {
		t.Fatalf("GenerateEd25519Key(grant): %v", err)
	}

	healthy := &Node{
		identity: &wasm.DerivedIdentity{
			SigningPrivKey:      signingPriv,
			SigningPubKey:       signingPub,
			SigningKeyPath:      "m/44'/0'/0'/0'/0'",
			EncryptionKey:       bytes.Repeat([]byte{0x5c}, 32),
			EncryptionPub:       bytes.Repeat([]byte{0x6d}, 32),
			EncryptionKeyPath:   "m/44'/0'/0'/1'/0'",
			GrantSigningPrivKey: grantPriv,
			GrantSigningPubKey:  grantPub,
			GrantSigningKeyPath: "m/44'/0'/0'/2'/0'",
		},
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(t.TempDir(), "data")},
		},
	}

	keys := healthy.ServerManagedKeys()
	if len(keys) != 3 {
		t.Fatalf("ServerManagedKeys returned %d entries, want identity/encryption/grant: %+v", len(keys), keys)
	}

	byPurpose := map[string]ManagedKey{}
	updateRoots := 0
	for _, key := range keys {
		byPurpose[key.Purpose] = key
		if key.Provenance != string(wasm.ProvenanceDerivedFromNodeRoot) {
			t.Fatalf("%s provenance = %q, want the derive-from-node-root DEFAULT", key.Purpose, key.Provenance)
		}
		if !key.Reproducible {
			t.Fatalf("%s is derived from the mnemonic but reported as not reproducible", key.Purpose)
		}
		if key.Description == "" {
			t.Fatalf("%s has no description; the UI would have to invent one", key.Purpose)
		}
		if key.IsUpdateRoot {
			updateRoots++
		}
	}
	if updateRoots != 1 {
		t.Fatalf("%d keys claim fleet code authority; exactly one may", updateRoots)
	}
	if !byPurpose["identity-signing"].IsUpdateRoot {
		t.Fatal("the identity signing key is not flagged as the fleet update root")
	}
	if byPurpose["licensing-grant"].IsUpdateRoot {
		t.Fatal("the grant key is flagged as the fleet update root")
	}
	if !byPurpose["licensing-grant"].InUse {
		t.Fatalf("a properly separated grant key is reported as not in use: %q", byPurpose["licensing-grant"].Note)
	}
	if byPurpose["licensing-grant"].PublicKey == byPurpose["identity-signing"].PublicKey {
		t.Fatal("the inventory reports the same public key for the grant lane and the update root")
	}

	// A REFUSED key is derivable but NOT signing. Reporting it as in-use would be
	// a lie an operator would act on.
	refused := &Node{
		identity: collidingIdentity(t),
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(t.TempDir(), "data")},
		},
	}
	for _, key := range refused.ServerManagedKeys() {
		if key.Purpose != "licensing-grant" {
			continue
		}
		if key.InUse {
			t.Fatal("a refused grant key is reported as in use")
		}
		if !strings.Contains(key.Note, "REFUSED") {
			t.Fatalf("a refused grant key does not say why: %q", key.Note)
		}
	}
}

// TestBondableAddressesAreIdentityOnly — the host names the addresses; summing
// their value is chain RPC and therefore WASM's job, never the host's
// (wasm-not-go-host-boundary).
func TestBondableAddressesAreIdentityOnly(t *testing.T) {
	t.Parallel()

	n := &Node{
		identity: &wasm.DerivedIdentity{
			Addresses: &wasm.CoinAddresses{
				Bitcoin:  &wasm.CoinAddress{Address: "bc1qexampleaddress0000000000000000000000000"},
				Ethereum: &wasm.CoinAddress{Address: "0x1234567890abcdef1234567890abcdef12345678"},
				Solana:   &wasm.CoinAddress{Address: "So1anaAddress1111111111111111111111111111111"},
			},
		},
	}
	addrs := n.BondableAddresses()
	for _, chain := range []string{"bitcoin", "ethereum", "solana"} {
		if strings.TrimSpace(addrs[chain]) == "" {
			t.Fatalf("no bondable %s address reported", chain)
		}
	}

	// No identity, no addresses — and no invented placeholders.
	if got := (&Node{}).BondableAddresses(); got != nil {
		t.Fatalf("a node with no identity reported bondable addresses: %v", got)
	}
}
