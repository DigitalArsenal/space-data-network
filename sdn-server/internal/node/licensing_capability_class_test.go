package node

import (
	"os"
	"path/filepath"
	"testing"

	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
	"github.com/spacedatanetwork/sdn-server/internal/cct"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/pmm"
)

// TestModuleDeliveryListingsShelveByCapabilityClass is the wave-2 measurement.
//
// Wave 1 proved the $PLG lane stopped announcing one hardcoded family. This
// walks the SAME lane — deployed $PMM catalog -> registry join -> $PLG frame ->
// the bytes /api/module-delivery/listings serves — and censuses the field the
// store now shelves by, PRIMARY_CATEGORY, decoding the result rather than
// trusting it.
//
// The catalog below is deliberately the awkward one. It carries the two members
// the migration had to rule on rather than guess (Basilisk, a vendor-derived
// name; Storefront, node-internal plumbing), a many-to-one collision
// (Storefront and Licensing both land on COMMERCE_AND_LICENSING), a blank
// declaration and an uncatalogued module.
func TestModuleDeliveryListingsShelveByCapabilityClass(t *testing.T) {
	t.Parallel()

	catalogPath := filepath.Join(t.TempDir(), "modules-catalog.json")
	if err := os.WriteFile(catalogPath, []byte(`{
      "provider_name": "test",
      "entries": [
        {"MODULE_ID": "com.example.sgp4",       "PLUGIN_ID": "com.example.sgp4",       "PLUGIN_TYPE": "Propagator"},
        {"MODULE_ID": "com.example.interp",     "PLUGIN_ID": "com.example.interp",     "PLUGIN_TYPE": "Interpolator"},
        {"MODULE_ID": "com.example.dynamics",   "PLUGIN_ID": "com.example.dynamics",   "PLUGIN_TYPE": "Basilisk"},
        {"MODULE_ID": "com.example.downlink",   "PLUGIN_ID": "com.example.downlink",   "PLUGIN_TYPE": "Comms"},
        {"MODULE_ID": "com.example.store",      "PLUGIN_ID": "com.example.store",      "PLUGIN_TYPE": "Storefront"},
        {"MODULE_ID": "com.example.keys",       "PLUGIN_ID": "com.example.keys",       "PLUGIN_TYPE": "Licensing"},
        {"MODULE_ID": "com.example.timebase",   "PLUGIN_ID": "com.example.timebase",   "PLUGIN_TYPE": "Foundation"},
        {"MODULE_ID": "com.example.blank",      "PLUGIN_ID": "com.example.blank",      "PLUGIN_TYPE": ""}
      ]
    }`), 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}

	types, err := pmm.CatalogPluginTypes(catalogPath)
	if err != nil {
		t.Fatalf("pmm.CatalogPluginTypes() error = %v", err)
	}
	reg, err := license.LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("LoadPluginRegistry() error = %v", err)
	}
	reg.SetPluginTypes(types)

	// com.example.unlisted is served by the registry but absent from the
	// catalog: a module nobody categorized.
	ids := []string{
		"com.example.sgp4", "com.example.interp", "com.example.dynamics",
		"com.example.downlink", "com.example.store", "com.example.keys",
		"com.example.timebase", "com.example.blank", "com.example.unlisted",
	}
	for _, id := range ids {
		if _, err := reg.AddPlugin(id, "1.0.0", []byte("\x00asm\x01\x00\x00\x00"), "", ""); err != nil {
			t.Fatalf("AddPlugin(%s) error = %v", id, err)
		}
	}

	listings, err := BuildModuleDeliveryListings(reg)
	if err != nil {
		t.Fatalf("BuildModuleDeliveryListings() error = %v", err)
	}
	if len(listings) != len(ids) {
		t.Fatalf("listings = %d, want %d", len(listings), len(ids))
	}

	census := map[string]int{}
	for _, listing := range listings {
		record := plg.GetRootAsPLG(listing.Payload, 0)
		census[record.PRIMARY_CATEGORY().String()]++
	}

	want := map[string]int{
		// Propagator, Interpolator and the deprecated vendor member all shelve
		// as PROPAGATION. Three old families, one honest class.
		"PROPAGATION": 3,
		// Storefront and Licensing were both node-internal plumbing.
		"COMMERCE_AND_LICENSING": 2,
		"RF_AND_COMMUNICATIONS":  1,
		"FOUNDATION_AND_MATH":    1,
		// The blank declaration and the uncatalogued module.
		"UNSPECIFIED": 2,
	}
	if len(census) != len(want) {
		t.Fatalf("census has %d classes %v, want %d %v", len(census), census, len(want), want)
	}
	for class, count := range want {
		if census[class] != count {
			t.Errorf("class %s = %d, want %d (full census: %v)", class, census[class], count, census)
		}
	}

	// The structural property this taxonomy exists for. pluginCategory's
	// ordinal 0 is Sensor, so an unset field there reads as a real family;
	// $CCT's ordinal 0 is UNSPECIFIED. Nothing in this catalog is a sensor, so
	// SENSORS_AND_COVERAGE appearing at all would mean a defaulted field
	// leaked — the wave-1 defect, in the new vocabulary.
	if census["SENSORS_AND_COVERAGE"] != 0 {
		t.Errorf("SENSORS_AND_COVERAGE = %d, want 0", census["SENSORS_AND_COVERAGE"])
	}
}

// TestPLGCarriesBothCategoryFields holds the declared migration shape: the new
// field is the shelf, the old one keeps being written as the fallback, and
// neither is derived from the other.
func TestPLGCarriesBothCategoryFields(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		declared        string
		wantPluginType  string
		wantPrimaryCat  string
		wantCategoryLen int
	}{
		{"a clean family", "Propagator", "Propagator", "PROPAGATION", 0},
		{"the deprecated vendor member is preserved on the wire, shelved as propagation",
			"Basilisk", "Basilisk", "PROPAGATION", 0},
		{"blank stays legible as silence in BOTH vocabularies", "", "Unspecified", cct.Unspecified, 0},
		{"an unknown symbol never guesses", "NotACategory", "Unspecified", cct.Unspecified, 0},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			frame, err := buildPublicationDescriptorFrame(&license.PluginAsset{
				ID:         "com.example.module",
				Version:    "1.0.0",
				PluginType: tc.declared,
			})
			if err != nil {
				t.Fatalf("buildPublicationDescriptorFrame() error = %v", err)
			}
			record := plg.GetRootAsPLG(frame, 0)

			if got := record.PLUGIN_TYPE().String(); got != tc.wantPluginType {
				t.Errorf("PLUGIN_TYPE = %q, want %q", got, tc.wantPluginType)
			}
			if got := record.PRIMARY_CATEGORY().String(); got != tc.wantPrimaryCat {
				t.Errorf("PRIMARY_CATEGORY = %q, want %q", got, tc.wantPrimaryCat)
			}
			// CATEGORIES stays empty: a category derived from a single-valued
			// family is always exactly one, and $PLG defines an empty list with
			// a set PRIMARY_CATEGORY as membership in that one category.
			if got := record.CATEGORIESLength(); got != tc.wantCategoryLen {
				t.Errorf("CATEGORIES length = %d, want %d", got, tc.wantCategoryLen)
			}
		})
	}
}

// TestPLGCategoriesIncludePrimaryWhenPresent asserts the $CCT invariant rather
// than trusting it: "if nonempty it MUST include PRIMARY_CATEGORY". The lane
// writes an empty vector today, so this passes vacuously — which is the point.
// The day anyone populates CATEGORIES, this fails unless they carry the primary
// with it.
func TestPLGCategoriesIncludePrimaryWhenPresent(t *testing.T) {
	t.Parallel()

	for _, declared := range []string{"Propagator", "Comms", "Basilisk", ""} {
		frame, err := buildPublicationDescriptorFrame(&license.PluginAsset{
			ID:         "com.example.module",
			Version:    "1.0.0",
			PluginType: declared,
		})
		if err != nil {
			t.Fatalf("buildPublicationDescriptorFrame(%q) error = %v", declared, err)
		}
		record := plg.GetRootAsPLG(frame, 0)
		n := record.CATEGORIESLength()
		if n == 0 {
			continue
		}
		primary := record.PRIMARY_CATEGORY()
		found := false
		for i := 0; i < n; i++ {
			if record.CATEGORIES(i) == primary {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q: CATEGORIES is nonempty but omits PRIMARY_CATEGORY %s", declared, primary.String())
		}
	}
}
