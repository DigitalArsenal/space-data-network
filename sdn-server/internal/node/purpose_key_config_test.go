package node

// Tests for the CONFIGURED PURPOSE KEY surface (graph task
// sdn-managed-key-registry-api): the owner's "UNLESS they specifically setup
// another key" exception, and the structural limits that make it safe —
// the update root is never configurable, a configured key may not collide with
// another purpose's key, and a configured-but-unreadable key fails CLOSED
// (no seed provisioned) rather than falling back to the derived default.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// TestUpdateRootIsNeverConfigurable is law 2 of the whole surface: the 0' fleet
// update root stays isolated. If this test starts failing, someone has made
// fleet code authority reachable through the configure API.
func TestUpdateRootIsNeverConfigurable(t *testing.T) {
	t.Parallel()

	ok, reason := PurposeKeyConfigurable(wasm.PurposeIdentitySigning)
	if ok {
		t.Fatal("the fleet update root reports as configurable — the standing owner ruling isolates it FOREVER")
	}
	if !strings.Contains(reason, "FLEET UPDATE ROOT") {
		t.Fatalf("the refusal must name what the key is, got %q", reason)
	}

	// And the API path refuses outright, before touching any store.
	n := &Node{}
	if _, err := n.ConfigurePurposeKey(wasm.PurposeIdentitySigning, strings.Repeat("11", 32), nil, ""); err == nil {
		t.Fatal("ConfigurePurposeKey accepted the update root")
	}
	if err := n.ClearPurposeKey(wasm.PurposeIdentitySigning); err == nil {
		t.Fatal("ClearPurposeKey accepted the update root")
	}
}

func TestEncryptionPurposeIsNotConfigurable(t *testing.T) {
	t.Parallel()
	if ok, _ := PurposeKeyConfigurable(wasm.PurposeEncryption); ok {
		t.Fatal("the encryption purpose reports as configurable — published EPM ciphertext would be orphaned")
	}
}

func TestConfigurablePurposesIsExactlyTheGrantKeyToday(t *testing.T) {
	t.Parallel()
	got := ConfigurablePurposeKeys()
	if len(got) != 1 || got[0] != wasm.PurposeLicensingGrant {
		t.Fatalf("ConfigurablePurposeKeys() = %v, want exactly [licensing-grant]; registering a new configurable purpose must be a deliberate contract change", got)
	}
}

func TestPurposeByLabelRoundTrip(t *testing.T) {
	t.Parallel()
	for _, p := range wasm.RegisteredPurposes() {
		got, ok := PurposeByLabel(p.Label())
		if !ok || got != p {
			t.Fatalf("PurposeByLabel(%q) = %v,%v, want %v", p.Label(), got, ok, p)
		}
	}
	if _, ok := PurposeByLabel("no-such-purpose"); ok {
		t.Fatal("PurposeByLabel admitted an unregistered label")
	}
}

func TestDecodePurposeKeySeed(t *testing.T) {
	t.Parallel()
	want := bytes.Repeat([]byte{0xab}, 32)
	for _, input := range []string{hex.EncodeToString(want), "0x" + hex.EncodeToString(want), "  " + hex.EncodeToString(want) + "  "} {
		got, err := decodePurposeKeySeed(input)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("decodePurposeKeySeed(%q) = %x, %v", input, got, err)
		}
	}
	for _, bad := range []string{"", "zz", "abcd", strings.Repeat("11", 31), strings.Repeat("11", 33)} {
		if _, err := decodePurposeKeySeed(bad); err == nil {
			t.Fatalf("decodePurposeKeySeed(%q) accepted a bad seed", bad)
		}
	}
}

func TestValidateBondAddresses(t *testing.T) {
	t.Parallel()

	got, err := validateBondAddresses(map[string]string{
		"bitcoin":  "bc1qexample",
		"ETHEREUM": " 0xabc ",
		"solana":   "",
	})
	if err != nil {
		t.Fatalf("validateBondAddresses: %v", err)
	}
	if got["bitcoin"] != "bc1qexample" || got["ethereum"] != "0xabc" {
		t.Fatalf("normalization wrong: %v", got)
	}
	if _, present := got["solana"]; present {
		t.Fatal("an empty address must be absent, not an empty row")
	}

	if _, err := validateBondAddresses(map[string]string{"dogecoin": "D6..."}); err == nil {
		t.Fatal("an unknown chain must be refused — the attestation module cannot read it")
	}
	if _, err := validateBondAddresses(map[string]string{"bitcoin": "bad\naddr"}); err == nil {
		t.Fatal("a non-printable address must be refused")
	}
	if got, err := validateBondAddresses(nil); err != nil || got != nil {
		t.Fatalf("nil in, nil out: %v %v", got, err)
	}
}

func TestParsePurposeKeyConfigFailsClosed(t *testing.T) {
	t.Parallel()

	good := `{"purpose":"licensing-grant","seed_hex":"` + strings.Repeat("22", 32) + `"}`
	cfg, err := parsePurposeKeyConfig(wasm.PurposeLicensingGrant, good)
	if err != nil || cfg == nil || cfg.SeedHex != strings.Repeat("22", 32) {
		t.Fatalf("valid record refused: %v %v", cfg, err)
	}

	for name, raw := range map[string]string{
		"not json":      "{",
		"wrong purpose": `{"purpose":"encryption","seed_hex":"` + strings.Repeat("22", 32) + `"}`,
		"bad seed":      `{"purpose":"licensing-grant","seed_hex":"abcd"}`,
	} {
		if _, err := parsePurposeKeyConfig(wasm.PurposeLicensingGrant, raw); err == nil {
			t.Fatalf("%s: parsePurposeKeyConfig accepted a corrupt record", name)
		}
	}
}

// purposeKeyTestNode builds the standard unit fixture: a derived identity whose
// grant child is properly separated from its update root, and a config with a
// unique storage path so the package-level cache cannot bleed across tests.
func purposeKeyTestNode(t *testing.T) (*Node, []byte, []byte) {
	t.Helper()
	signingSeed := make([]byte, ed25519.SeedSize)
	for i := range signingSeed {
		signingSeed[i] = byte(0x40 + i)
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
	return n, signingSeed, grantSeed
}

// TestConfigureRefusesUpdateRootCollision: supplying the update root's own seed
// as the "dedicated" grant key must be refused BEFORE anything persists — that
// is the exact collision the domain-separation ruling removed.
func TestConfigureRefusesUpdateRootCollision(t *testing.T) {
	t.Parallel()
	n, signingSeed, _ := purposeKeyTestNode(t)

	_, err := n.ConfigurePurposeKey(wasm.PurposeLicensingGrant, hex.EncodeToString(signingSeed), nil, "")
	if err == nil {
		t.Fatal("ConfigurePurposeKey accepted the fleet update root's seed as a grant key")
	}
	if !strings.Contains(err.Error(), "REFUSED") {
		t.Fatalf("the refusal must be explicit, got %q", err)
	}
}

func TestConfigureRefusesEncryptionScalarCollision(t *testing.T) {
	t.Parallel()
	n, _, _ := purposeKeyTestNode(t)

	scalar := hex.EncodeToString(bytes.Repeat([]byte{0x5c}, 32)) // == identity.EncryptionKey
	if _, err := n.ConfigurePurposeKey(wasm.PurposeLicensingGrant, scalar, nil, ""); err == nil {
		t.Fatal("ConfigurePurposeKey accepted the encryption scalar as an Ed25519 grant seed")
	}
}

func TestConfigureRefusesBadSeedAndBadChains(t *testing.T) {
	t.Parallel()
	n, _, _ := purposeKeyTestNode(t)

	if _, err := n.ConfigurePurposeKey(wasm.PurposeLicensingGrant, "abcd", nil, ""); err == nil {
		t.Fatal("a short seed was accepted")
	}
	seed := strings.Repeat("33", 32)
	if _, err := n.ConfigurePurposeKey(wasm.PurposeLicensingGrant, seed, map[string]string{"dogecoin": "x"}, ""); err == nil {
		t.Fatal("an unknown bond chain was accepted")
	}
}

// TestModuleRuntimeKeySlotsPrefersConfiguredExternalKey is the wiring the owner
// asked for: once an external key is configured, IT signs grants — the derived
// default steps aside. Injected through the cache (the read path) so the unit
// needs no live keystore.
func TestModuleRuntimeKeySlotsPrefersConfiguredExternalKey(t *testing.T) {
	t.Parallel()
	n, _, grantSeed := purposeKeyTestNode(t)

	extSeed := bytes.Repeat([]byte{0x77}, 32)
	purposeKeyCacheSet(n.config.Storage.Path, purposeKeyLaneID(wasm.PurposeLicensingGrant), &PurposeKeyConfig{
		Purpose: wasm.PurposeLicensingGrant.Label(),
		SeedHex: hex.EncodeToString(extSeed),
		BondAddresses: map[string]string{
			"ethereum": "0x00000000000000000000000000000000000000ff",
		},
	})

	got, _, err := n.moduleRuntimeKeySlots()
	if err != nil {
		t.Fatalf("moduleRuntimeKeySlots: %v", err)
	}
	if !bytes.Equal(got, extSeed) {
		t.Fatalf("grant slot = %x, want the configured external key %x", got, extSeed)
	}
	if bytes.Equal(got, grantSeed) {
		t.Fatal("the derived default was used despite a configured external key")
	}
	if scheme := n.grantSigningKeyScheme(got); scheme != grantKeySchemeExternal {
		t.Fatalf("scheme = %q, want the external label", scheme)
	}

	// The inventory must SAY it is external, report ITS public half, carry its
	// bond addresses, and mark it non-reproducible.
	var grantRow *ManagedKey
	for _, key := range n.ServerManagedKeys() {
		if key.Purpose == wasm.PurposeLicensingGrant.String() {
			row := key
			grantRow = &row
		}
	}
	if grantRow == nil {
		t.Fatal("licensing-grant row missing from the inventory")
	}
	if grantRow.Provenance != string(wasm.ProvenanceExternalConfigured) {
		t.Fatalf("provenance = %q, want external-configured", grantRow.Provenance)
	}
	if grantRow.Reproducible {
		t.Fatal("an external key reported as reproducible — an operator would skip the backup it needs")
	}
	wantPub, _ := ed25519PublicFromSeed(extSeed)
	if grantRow.PublicKey != hex.EncodeToString(wantPub) {
		t.Fatalf("inventory reports pubkey %s, want the external key's %x", grantRow.PublicKey, wantPub)
	}
	if grantRow.BondAddresses["ethereum"] == "" {
		t.Fatal("the external key's bond addresses were dropped from the inventory")
	}
	if !grantRow.Configurable {
		t.Fatal("the grant purpose must report configurable")
	}

	// And the per-key bond map carries BOTH the root's absence (no chain addrs
	// in this fixture) and the external key's declared address.
	bondMap := n.PurposeBondAddresses()
	if bondMap[wasm.PurposeLicensingGrant.String()]["ethereum"] == "" {
		t.Fatalf("PurposeBondAddresses missing the external key's address: %v", bondMap)
	}
}

// TestModuleRuntimeKeySlotsFailsClosedOnUnreadableConfiguredKey: a configured
// key that cannot be used yields NO grant seed — never the derived fallback —
// and the refusal is recorded where the licensing bootstrap reports it.
func TestModuleRuntimeKeySlotsFailsClosedOnUnreadableConfiguredKey(t *testing.T) {
	t.Parallel()
	n, _, _ := purposeKeyTestNode(t)

	purposeKeyCacheSet(n.config.Storage.Path, purposeKeyLaneID(wasm.PurposeLicensingGrant), &PurposeKeyConfig{
		Purpose: wasm.PurposeLicensingGrant.Label(),
		SeedHex: "corrupt-not-hex",
	})

	got, wrapping, err := n.moduleRuntimeKeySlots()
	if err != nil {
		t.Fatalf("moduleRuntimeKeySlots: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a seed was provisioned despite an unreadable configured key: %x", got)
	}
	if len(wrapping) != 32 {
		t.Fatal("the wrapping key was collateral damage of the grant refusal")
	}

	nodeCtx, err := n.buildModuleNodeContext()
	if err != nil {
		t.Fatalf("buildModuleNodeContext: %v", err)
	}
	if reason := nodeCtx.KeySlotRefusals[providerSigningSlotID]; !strings.Contains(reason, "configured external key") {
		t.Fatalf("the refusal must name the configured key as the cause, got %q", reason)
	}

	// The inventory says the same thing instead of advertising a derived key
	// the node is deliberately not signing with.
	for _, key := range n.ServerManagedKeys() {
		if key.Purpose != wasm.PurposeLicensingGrant.String() {
			continue
		}
		if key.InUse {
			t.Fatal("an unusable configured key reports in_use")
		}
		if !strings.Contains(key.Note, "REFUSED") {
			t.Fatalf("the inventory note must carry the refusal, got %q", key.Note)
		}
	}
}

// TestManagedKeysDefaultInventoryUnchanged: with nothing configured, the
// derived defaults report exactly as before — root provenance, reproducible,
// the root's bond addresses on the identity row only.
func TestManagedKeysDefaultInventoryUnchanged(t *testing.T) {
	t.Parallel()
	n, _, grantSeed := purposeKeyTestNode(t)

	keys := n.ServerManagedKeys()
	byPurpose := map[string]ManagedKey{}
	for _, key := range keys {
		byPurpose[key.Purpose] = key
	}

	root, ok := byPurpose[wasm.PurposeIdentitySigning.String()]
	if !ok || !root.IsUpdateRoot {
		t.Fatalf("identity-signing row missing or unmarked: %+v", root)
	}
	if root.Configurable {
		t.Fatal("the update root reports configurable in the inventory")
	}
	grant, ok := byPurpose[wasm.PurposeLicensingGrant.String()]
	if !ok {
		t.Fatal("licensing-grant row missing")
	}
	if grant.Provenance != string(wasm.ProvenanceDerivedFromNodeRoot) {
		t.Fatalf("grant provenance = %q, want derived-from-node-root", grant.Provenance)
	}
	if !grant.Configurable {
		t.Fatal("the grant purpose must report configurable")
	}
	if grant.Slot != providerSigningSlotID {
		t.Fatalf("grant slot = %q, want %q", grant.Slot, providerSigningSlotID)
	}
	wantPub, _ := ed25519PublicFromSeed(grantSeed)
	if grant.PublicKey != hex.EncodeToString(wantPub) {
		t.Fatalf("grant pubkey %s, want derived %x", grant.PublicKey, wantPub)
	}
}
