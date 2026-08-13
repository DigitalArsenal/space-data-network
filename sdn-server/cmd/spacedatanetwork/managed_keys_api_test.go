package main

// Tests for the managed-key registry surface (graph task
// sdn-managed-key-registry-api): the merged slots[] table the shipped
// key-management UI matches purposes against, and the per-key signing audit
// folded from the two append-only logs.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/node"
)

// TestBuildManagedSlotRowsMergesIdentityAndPurposeSlots pins the shape
// nodekeys.js derivationState reads: identity slots keep their §18 fields and
// gain source "root"; purpose keys with a runtime slot become rows keyed by
// slot id + purpose label; the identity root (no runtime slot) stays out of
// slots[] (it lives in managed_keys).
func TestBuildManagedSlotRowsMergesIdentityAndPurposeSlots(t *testing.T) {
	t.Parallel()

	identitySlots := []epm.KeySlot{
		{Slot: epm.KeySlotSigning, Path: "m/44'/60'/0'/0/0", NextPath: "m/44'/60'/0'/0/1", Rotatable: true, XPubDerivable: true, Source: epm.KeySlotSourceRoot},
		{Slot: epm.KeySlotEncryption, Path: "m/44'/60'/0'/1/0", NextPath: "m/44'/60'/0'/1/1", Rotatable: true, XPubDerivable: true, Source: epm.KeySlotSourceRoot},
	}
	managed := []node.ManagedKey{
		{Purpose: "identity-signing", Provenance: "derived-from-node-root", IsUpdateRoot: true}, // Slot "" -> not a slots[] row
		{Purpose: "licensing-grant", Slot: "provider-signing", Provenance: "derived-from-node-root", DerivationPath: "m/44'/0'/0'/2'/0'", Algorithm: "ed25519", PublicKey: "aa11", Configurable: true},
		{Purpose: "encryption", Slot: "provider-wrapping", Provenance: "derived-from-node-root", Algorithm: "x25519"},
	}

	rows := buildManagedSlotRows(identitySlots, managed)
	if len(rows) != 4 {
		t.Fatalf("expected 2 identity + 2 purpose rows, got %d", len(rows))
	}
	bySlot := map[string]managedSlotRow{}
	for _, row := range rows {
		bySlot[row.Slot] = row
	}
	if bySlot["signing"].Source != "root" || bySlot["encryption"].Source != "root" {
		t.Fatalf("identity slots must carry source root: %+v", rows)
	}
	grant := bySlot["provider-signing"]
	if grant.Purpose != "licensing-grant" || grant.Source != "root" || !grant.Configurable {
		t.Fatalf("grant row wrong: %+v", grant)
	}
	if grant.Rotatable {
		t.Fatal("a purpose slot must not offer GEN KEY path rotation")
	}
	if grant.PublicKey != "aa11" || grant.Path != "m/44'/0'/0'/2'/0'" {
		t.Fatalf("grant row dropped public facts: %+v", grant)
	}
	if _, present := bySlot[""]; present {
		t.Fatal("the identity root leaked into slots[] with an empty slot id")
	}
}

// TestSlotSourceFollowsProvenance pins the two-state fold the UI renders:
// external-configured -> "external", both derived provenances -> "root".
func TestSlotSourceFollowsProvenance(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"derived-from-node-root":            "root",
		"derived-from-node-root-legacy-kdf": "root",
		"external-configured":               "external",
		"EXTERNAL-CONFIGURED":               "external",
		"":                                  "root",
	}
	for provenance, want := range cases {
		if got := slotSourceFromProvenance(provenance); got != want {
			t.Fatalf("slotSourceFromProvenance(%q) = %q, want %q", provenance, got, want)
		}
	}

	managed := []node.ManagedKey{{Purpose: "licensing-grant", Slot: "provider-signing", Provenance: "external-configured"}}
	rows := buildManagedSlotRows(nil, managed)
	if len(rows) != 1 || rows[0].Source != "external" {
		t.Fatalf("an external-configured key must render source external: %+v", rows)
	}
}

// writeAuditFixture writes a JSONL audit file the reader consumes.
func writeAuditFixture(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestReadSigningAuditLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	path := writeAuditFixture(t, dir, "module-signing.audit.jsonl", []string{
		`{"ts":"2026-08-01T10:00:00Z","event":"signature_issued","content_hash":"c1","statement_domain":"SDN-MODULE-PUBLICATION-V1","signer_pubkey_hex":"AB12","requester":"fa12d0d1924b","submitted_bytes":10}`,
		`not json at all`,
		`{"ts":"2026-08-02T10:00:00Z","event":"signature_refused","reason":"no session","submitted_bytes":0}`,
	})

	events, status := readSigningAuditLog("module-signing", path)
	if status.Error != "" {
		t.Fatalf("unexpected error: %s", status.Error)
	}
	if status.Entries != 2 || status.ParseErrors != 1 {
		t.Fatalf("entries=%d parse_errors=%d, want 2/1", status.Entries, status.ParseErrors)
	}
	if len(events) != 2 {
		t.Fatalf("events=%d", len(events))
	}
	if events[0].SignerPubKey != "ab12" {
		t.Fatalf("signer pubkey must be normalized lowercase, got %q", events[0].SignerPubKey)
	}
	if events[0].Domain != "SDN-MODULE-PUBLICATION-V1" || events[0].Lane != "module-signing" {
		t.Fatalf("event lost its domain/lane: %+v", events[0])
	}

	// Absent file = empty log, not an error (created lazily on first signature).
	_, absent := readSigningAuditLog("update-signing", filepath.Join(dir, "does-not-exist.jsonl"))
	if absent.Error != "" || absent.Entries != 0 {
		t.Fatalf("an absent audit file must read as empty: %+v", absent)
	}

	// No resolvable path = a named error, distinguishable from empty.
	_, noPath := readSigningAuditLog("update-signing", "")
	if noPath.Error == "" {
		t.Fatal("an unresolvable audit path must be reported")
	}
}

// TestMergeKeySigningEventsAttributesAndSorts is the per-key view itself:
// events resolve to the managed key whose public half signed them, an unlisted
// signer says so loudly, refusals attribute to the root lane, newest first,
// limit respected.
func TestMergeKeySigningEventsAttributesAndSorts(t *testing.T) {
	t.Parallel()

	managed := []node.ManagedKey{
		{Purpose: "identity-signing", PublicKey: "aa01", IsUpdateRoot: true},
		{Purpose: "licensing-grant", PublicKey: "bb02", Slot: "provider-signing"},
	}
	moduleEvents := []keySigningEvent{
		{TS: "2026-08-01T10:00:00Z", Event: "signature_issued", Lane: "module-signing", SignerPubKey: "aa01", ContentHash: "c1"},
		{TS: "2026-08-03T10:00:00Z", Event: "signature_issued", Lane: "module-signing", SignerPubKey: "dead", ContentHash: "c3"},
	}
	updateEvents := []keySigningEvent{
		{TS: "2026-08-02T10:00:00Z", Event: "signature_issued", Lane: "update-signing", SignerPubKey: "bb02", ContentHash: "c2"},
		{TS: "2026-08-04T10:00:00Z", Event: "signature_refused", Lane: "update-signing", Reason: "not admin"},
	}

	events := mergeKeySigningEvents(moduleEvents, updateEvents, managed, 0)
	if len(events) != 4 {
		t.Fatalf("events=%d", len(events))
	}
	// Newest first.
	for i := 1; i < len(events); i++ {
		if events[i-1].TS < events[i].TS {
			t.Fatalf("events not sorted newest-first: %v then %v", events[i-1].TS, events[i].TS)
		}
	}
	byHash := map[string]keySigningEvent{}
	for _, e := range events {
		byHash[e.ContentHash] = e
	}
	if byHash["c1"].Purpose != "identity-signing" {
		t.Fatalf("c1 attribution: %+v", byHash["c1"])
	}
	if byHash["c2"].Purpose != "licensing-grant" || byHash["c2"].Slot != "provider-signing" {
		t.Fatalf("c2 attribution: %+v", byHash["c2"])
	}
	if byHash["c3"].Purpose != "unrecorded-key" {
		t.Fatalf("an unlisted signer must be reported loudly, got %+v", byHash["c3"])
	}
	if byHash[""].Purpose != "identity-signing" {
		t.Fatalf("a refusal (no signer chosen yet) attributes to the root lane, got %+v", byHash[""])
	}

	limited := mergeKeySigningEvents(moduleEvents, updateEvents, managed, 2)
	if len(limited) != 2 || limited[0].TS != "2026-08-04T10:00:00Z" {
		t.Fatalf("limit must keep the newest events: %+v", limited)
	}
}

// TestManagedKeysResponseShapeIsUIConsumable renders the GET body the way the
// shipped UI folds it and asserts the join keys survive serialization.
func TestManagedKeysResponseShapeIsUIConsumable(t *testing.T) {
	t.Parallel()

	rows := buildManagedSlotRows(
		[]epm.KeySlot{{Slot: epm.KeySlotSigning, Path: "p", XPubDerivable: true, Source: epm.KeySlotSourceRoot}},
		[]node.ManagedKey{{Purpose: "licensing-grant", Slot: "provider-signing", Provenance: "external-configured", Configurable: true}},
	)
	raw, err := json.Marshal(map[string]interface{}{"slots": rows})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Slots []map[string]interface{} `json:"slots"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var grant map[string]interface{}
	for _, s := range decoded.Slots {
		if s["slot"] == "provider-signing" {
			grant = s
		}
	}
	if grant == nil {
		t.Fatal("provider-signing row missing")
	}
	// nodekeys.js: str(found.provenance ?? found.source) -> 'external...'
	if grant["provenance"] != "external-configured" || grant["source"] != "external" {
		t.Fatalf("the UI's provenance fold would misread this row: %v", grant)
	}
	if grant["purpose"] != "licensing-grant" {
		t.Fatalf("purpose join key missing: %v", grant)
	}
}
