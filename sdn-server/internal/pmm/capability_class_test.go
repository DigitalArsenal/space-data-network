package pmm

import (
	"encoding/json"
	"strings"
	"testing"

	sdspmm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PMM"
	"github.com/spacedatanetwork/sdn-server/internal/cct"
)

// TestManifestEntriesCarryCapabilityClass censuses the $PMM lane the way the
// $PLG lane is censused: decode the encoded record and count the shelf.
//
// $PMM mirrors PRIMARY_CATEGORY from $PLG so an anonymous client can section
// the whole catalogue at boot without fetching one $PLG per module. If these
// two lanes ever disagree, that boot-time sectioning is a lie, so both are
// measured from the same declared PLUGIN_TYPE.
func TestManifestEntriesCarryCapabilityClass(t *testing.T) {
	m := testManifest()
	m.Modules = append(m.Modules,
		Entry{
			ModuleID: "com.orbpro.dynamics", Version: "2.0.0",
			ContentHash: strings.Repeat("c", 64),
			TrustTier:   "OPTIONAL", AccessPolicy: "ENTITLED",
			EntryState: "ACTIVE", PluginType: "Basilisk",
		},
		Entry{
			ModuleID: "com.orbpro.store", Version: "1.0.0",
			ContentHash: strings.Repeat("d", 64),
			TrustTier:   "OPTIONAL", AccessPolicy: "ENTITLED",
			EntryState: "ACTIVE", PluginType: "Storefront",
		},
		Entry{
			ModuleID: "com.orbpro.unstated", Version: "1.0.0",
			ContentHash: strings.Repeat("e", 64),
			TrustTier:   "OPTIONAL", AccessPolicy: "ENTITLED",
			EntryState: "ACTIVE", PluginType: "Unspecified",
		},
	)

	raw, err := MarshalBinary(m)
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}
	// MarshalBinary emits the size-prefixed form.
	record := sdspmm.GetSizePrefixedRootAsPMM(raw, 0)

	census := map[string]int{}
	byModule := map[string]string{}
	for i := 0; i < record.MODULESLength(); i++ {
		var entry sdspmm.PMMModuleEntry
		if !record.MODULES(&entry, i) {
			t.Fatalf("MODULES(%d) missing", i)
		}
		class := entry.PRIMARY_CATEGORY().String()
		census[class]++
		byModule[string(entry.MODULE_ID())] = class

		// PLUGIN_TYPE keeps being written: $PLG declares it the fallback for
		// consumers pinned before the taxonomy. A record that dropped it would
		// go dark for them.
		if entry.PLUGIN_TYPE().String() == "" {
			t.Errorf("%s lost PLUGIN_TYPE — the declared fallback must survive the migration", entry.MODULE_ID())
		}

		// $PMM restates the $CCT invariant. Asserted, not trusted.
		if n := entry.CATEGORIESLength(); n > 0 {
			found := false
			for j := 0; j < n; j++ {
				if entry.CATEGORIES(j) == entry.PRIMARY_CATEGORY() {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: CATEGORIES is nonempty but omits PRIMARY_CATEGORY", entry.MODULE_ID())
			}
		}
	}

	want := map[string]string{
		"com.orbpro.sgp4":     "PROPAGATION",
		"com.orbpro.rf-fspl":  "RF_AND_COMMUNICATIONS",
		"com.orbpro.dynamics": "PROPAGATION",
		"com.orbpro.store":    "COMMERCE_AND_LICENSING",
		"com.orbpro.unstated": cct.Unspecified,
	}
	for moduleID, class := range want {
		if byModule[moduleID] != class {
			t.Errorf("%s shelved as %q, want %q", moduleID, byModule[moduleID], class)
		}
	}
	if census["SENSORS_AND_COVERAGE"] != 0 {
		t.Errorf("SENSORS_AND_COVERAGE = %d, want 0 — nothing here is a sensor; a defaulted field leaked", census["SENSORS_AND_COVERAGE"])
	}
}

// TestCanonicalStatementExcludesPresentationFields is the signature seam.
//
// $PMM states normatively that PRIMARY_CATEGORY is NOT covered by
// PMM.SIGNATURE under SDN-MODULE-MANIFEST-V1 — it is an unverified provider
// claim sitting alongside NAME, DESCRIPTION and ICON_URL, resting on a signed
// content hash. Extending the statement to cover presentation fields is a V2
// change that has to move every verifier in lockstep.
//
// So: changing a module's family must not move one byte of the statement. If
// this fails, some future edit quietly re-signed the fleet.
func TestCanonicalStatementExcludesPresentationFields(t *testing.T) {
	base := CanonicalStatement(testManifest())

	reclassified := testManifest()
	for i := range reclassified.Modules {
		reclassified.Modules[i].PluginType = "Storefront"
	}
	if got := CanonicalStatement(reclassified); got != base {
		t.Errorf("re-classifying every module moved the canonical statement.\n got: %q\nwant: %q", got, base)
	}

	// The presentation fields the schema groups PRIMARY_CATEGORY with behave
	// the same way; pinning them together states WHY the category is exempt.
	decorated := testManifest()
	for i := range decorated.Modules {
		decorated.Modules[i].Name = "Renamed"
		decorated.Modules[i].Description = "Rewritten"
		decorated.Modules[i].IconURL = "https://example.invalid/icon.png"
	}
	if got := CanonicalStatement(decorated); got != base {
		t.Errorf("renaming/redescribing modules moved the canonical statement.\n got: %q\nwant: %q", got, base)
	}
}

// TestEntryJSONProjectsCategoryAndNeverAcceptsIt pins the derived-only rule.
// PRIMARY_CATEGORY is emitted so a client sees the same shelf the FlatBuffer
// carries, and ignored on input so a catalog file cannot declare a category
// that disagrees with the PLUGIN_TYPE it was supposed to be derived from.
func TestEntryJSONProjectsCategoryAndNeverAcceptsIt(t *testing.T) {
	raw, err := json.Marshal(Entry{ModuleID: "com.example.mod", PluginType: "Basilisk"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var projected map[string]any
	if err := json.Unmarshal(raw, &projected); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if projected["PRIMARY_CATEGORY"] != "PROPAGATION" {
		t.Errorf("PRIMARY_CATEGORY = %v, want PROPAGATION", projected["PRIMARY_CATEGORY"])
	}
	if projected["PLUGIN_TYPE"] != "Basilisk" {
		t.Errorf("PLUGIN_TYPE = %v, want Basilisk — the fallback must survive", projected["PLUGIN_TYPE"])
	}

	// A catalog file trying to declare its own category is ignored: the field
	// is projected from PLUGIN_TYPE, and there is no path back in.
	var decoded Entry
	if err := json.Unmarshal([]byte(`{"MODULE_ID":"com.example.mod","PLUGIN_TYPE":"Comms","PRIMARY_CATEGORY":"PROPAGATION"}`), &decoded); err != nil {
		t.Fatalf("Unmarshal(declared category) error = %v", err)
	}
	if decoded.PluginType != "Comms" {
		t.Fatalf("PluginType = %q, want Comms", decoded.PluginType)
	}
	round, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-Marshal() error = %v", err)
	}
	if !strings.Contains(string(round), `"PRIMARY_CATEGORY":"RF_AND_COMMUNICATIONS"`) {
		t.Errorf("a declared PRIMARY_CATEGORY survived instead of being re-derived from PLUGIN_TYPE: %s", round)
	}
}
