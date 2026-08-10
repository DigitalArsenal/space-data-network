package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/pmm"
)

// TestBuildPublicationDescriptorFramePLUGINTYPEIsDeclaredNotHardcoded pins the
// defect that PLUGIN_TYPE used to be the compile-time literal 3: every listing
// this node published announced "Analysis" no matter what the module was.
//
// The three cases below are the whole contract. A declared symbol survives to
// the wire; an unrecognized symbol and an absent one BOTH resolve to
// Unspecified — never to the flatbuffers default, because pluginCategory's zero
// value is Sensor and a defaulted blank is a confident wrong answer rather than
// a missing one.
func TestBuildPublicationDescriptorFramePLUGINTYPEIsDeclaredNotHardcoded(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		declared string
		want     string
	}{
		{name: "declared symbol survives", declared: "Propagator", want: "Propagator"},
		{name: "a second, different symbol", declared: "Comms", want: "Comms"},
		{name: "blank is Unspecified, never Sensor", declared: "", want: "Unspecified"},
		{name: "unknown symbol is Unspecified", declared: "NotACategory", want: "Unspecified"},
		{name: "surrounding space is trimmed", declared: "  Sensor  ", want: "Sensor"},
	} {
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
			if got := plg.GetRootAsPLG(frame, 0).PLUGIN_TYPE().String(); got != tc.want {
				t.Errorf("PLUGIN_TYPE = %q, want %q (declared %q)", got, tc.want, tc.declared)
			}
		})
	}
}

// TestModuleDeliveryListingsReportDeclaredCategories walks the whole lane the
// storefront reads — deployed $PMM catalog -> registry join -> $PLG frame -> the
// bytes /api/module-delivery/listings serves — and DECODES the result rather
// than trusting it.
//
// The census at the end is the point: before this fix a count over that lane
// returned one bucket holding every module.
func TestModuleDeliveryListingsReportDeclaredCategories(t *testing.T) {
	t.Parallel()

	catalogDir := t.TempDir()
	catalogPath := filepath.Join(catalogDir, "modules-catalog.json")
	if err := os.WriteFile(catalogPath, []byte(`{
      "provider_name": "test",
      "entries": [
        {"MODULE_ID": "com.example.sgp4",       "PLUGIN_ID": "com.example.sgp4",       "PLUGIN_TYPE": "Propagator"},
        {"MODULE_ID": "com.example.sdp4",       "PLUGIN_ID": "com.example.sdp4",       "PLUGIN_TYPE": "Propagator"},
        {"MODULE_ID": "com.example.conjunction","PLUGIN_ID": "com.example.conjunction","PLUGIN_TYPE": "Analysis"},
        {"MODULE_ID": "com.example.downlink",   "PLUGIN_ID": "com.example.downlink",   "PLUGIN_TYPE": "Comms"},
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
	// A blank declaration is dropped at the source, not carried as an empty
	// string that a later reader might mistake for a value.
	if _, present := types["com.example.blank"]; present {
		t.Error("blank PLUGIN_TYPE must be dropped by CatalogPluginTypes, not carried")
	}

	reg, err := license.LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("LoadPluginRegistry() error = %v", err)
	}
	reg.SetPluginTypes(types)

	// com.example.unlisted is deliberately absent from the catalog: a module the
	// registry serves but the catalog never categorized.
	for _, id := range []string{
		"com.example.sgp4", "com.example.sdp4", "com.example.conjunction",
		"com.example.downlink", "com.example.timebase", "com.example.blank",
		"com.example.unlisted",
	} {
		if _, err := reg.AddPlugin(id, "1.0.0", []byte("\x00asm\x01\x00\x00\x00"), "", ""); err != nil {
			t.Fatalf("AddPlugin(%s) error = %v", id, err)
		}
	}

	listings, err := BuildModuleDeliveryListings(reg)
	if err != nil {
		t.Fatalf("BuildModuleDeliveryListings() error = %v", err)
	}
	if len(listings) != 7 {
		t.Fatalf("listings = %d, want 7", len(listings))
	}

	census := map[string]int{}
	for _, listing := range listings {
		census[plg.GetRootAsPLG(listing.Payload, 0).PLUGIN_TYPE().String()]++
	}

	want := map[string]int{
		"Propagator":  2,
		"Analysis":    1,
		"Comms":       1,
		"Foundation":  1,
		"Unspecified": 2, // the blank declaration and the uncatalogued module
	}
	if len(census) != len(want) {
		t.Fatalf("census has %d categories %v, want %d %v", len(census), census, len(want), want)
	}
	for category, count := range want {
		if census[category] != count {
			t.Errorf("category %s = %d, want %d (full census: %v)", category, census[category], count, census)
		}
	}
	if census["Sensor"] != 0 {
		t.Errorf("Sensor = %d, want 0 — an unset PLUGIN_TYPE leaked the enum's zero value", census["Sensor"])
	}
}

// TestPluginRegistryDoesNotPersistJoinedPluginType guards the one-truth rule:
// the category is joined in from the $PMM catalog for READERS, and must never
// be written back into the registry's own catalog.json, where a stored second
// copy could later disagree with the catalog it was copied from.
func TestPluginRegistryDoesNotPersistJoinedPluginType(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	reg, err := license.LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("LoadPluginRegistry() error = %v", err)
	}
	reg.SetPluginTypes(map[string]string{"com.example.mod": "Propagator"})
	if _, err := reg.AddPlugin("com.example.mod", "1.0.0", []byte("\x00asm\x01\x00\x00\x00"), "", ""); err != nil {
		t.Fatalf("AddPlugin() error = %v", err)
	}

	asset, ok := reg.Get("com.example.mod")
	if !ok {
		t.Fatal("Get() missing the asset just added")
	}
	if asset.PluginType != "Propagator" {
		t.Errorf("Get().PluginType = %q, want Propagator", asset.PluginType)
	}

	raw, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatalf("read catalog.json: %v", err)
	}
	for _, needle := range []string{"PLUGIN_TYPE", "PluginType", "Propagator"} {
		if strings.Contains(string(raw), needle) {
			t.Errorf("catalog.json contains %q — the joined category must not be persisted:\n%s", needle, raw)
		}
	}
}
