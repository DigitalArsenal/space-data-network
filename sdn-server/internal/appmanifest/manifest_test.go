package appmanifest

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// sampleManifest returns a representative app manifest referencing three
// member modules, one produced/one consumed data binding, a module source
// and an external-api source, and a UI entry served by the "ui" module —
// the shape H2 (the launcher) will need to consume.
func sampleManifest() *AppManifest {
	return &AppManifest{
		ID:          "spaceaware-console",
		Name:        "SpaceAware Console",
		Version:     "1.0.0",
		Description: "Conjunction and catalog console",
		Modules: []ModuleRef{
			{
				ID:          "propagator",
				PluginID:    "sgp4-propagator",
				ContentHash: "aa11bb22cc33dd44ee55ff66aa11bb22cc33dd44ee55ff66aa11bb22cc33dd4",
				Version:     "2.1.0",
				Role:        "primary",
				Description: "SGP4 orbit propagator",
			},
			{
				ID:       "catalog-source",
				PluginID: "celestrak-source",
				Version:  "1.4.0",
				Role:     "worker",
			},
			{
				ID:       "ui",
				PluginID: "spaceaware-ui",
				Version:  "0.9.0",
				Role:     "ui-host",
			},
		},
		Data: []DataRef{
			{
				ID:        "omm-out",
				SDSType:   "OMM",
				Direction: DataDirectionProduces,
				ModuleID:  "propagator",
			},
			{
				ID:        "epm-in",
				SDSType:   "EPM",
				Direction: DataDirectionConsumes,
				ModuleID:  "catalog-source",
			},
		},
		Sources: []SourceRef{
			{
				ID:   "celestrak-feed",
				Kind: SourceKindModule,
				Ref:  "catalog-source",
			},
			{
				ID:          "space-track",
				Kind:        SourceKindExternalAPI,
				Ref:         "https://www.space-track.org/basicspacedata",
				Description: "Fallback TLE source",
			},
		},
		UI: &UIEntry{
			ModuleID: "ui",
			URL:      "/apps/spaceaware-console/",
			Icon:     "🛰",
			Color:    "#1d4ed8",
		},
	}
}

func TestParseResolvesModulesDataSourcesAndUI(t *testing.T) {
	original := sampleManifest()
	data, err := original.MarshalCanonicalJSON()
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON() error = %v", err)
	}

	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	resolution, err := parsed.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if len(resolution.Modules) != 3 {
		t.Fatalf("resolved modules = %d, want 3", len(resolution.Modules))
	}
	if _, ok := resolution.ModuleByID["propagator"]; !ok {
		t.Fatalf("ModuleByID missing %q: %#v", "propagator", resolution.ModuleByID)
	}

	if len(resolution.Data) != 2 {
		t.Fatalf("resolved data = %d, want 2", len(resolution.Data))
	}
	omm := resolution.Data[0]
	if omm.SDSType != "OMM" || omm.Module == nil || omm.Module.ID != "propagator" {
		t.Fatalf("resolved data[0] = %#v, want OMM owned by propagator", omm)
	}

	if len(resolution.Sources) != 2 {
		t.Fatalf("resolved sources = %d, want 2", len(resolution.Sources))
	}
	moduleSource := resolution.Sources[0]
	if moduleSource.Module == nil || moduleSource.Module.ID != "catalog-source" {
		t.Fatalf("resolved sources[0] = %#v, want module source resolved to catalog-source", moduleSource)
	}
	externalSource := resolution.Sources[1]
	if externalSource.Module != nil {
		t.Fatalf("resolved sources[1] = %#v, want no resolved module for an external-api source", externalSource)
	}

	if resolution.UI == nil {
		t.Fatalf("resolution.UI = nil, want a resolved UI entry")
	}
	if resolution.UI.Module.ID != "ui" {
		t.Fatalf("resolution.UI.Module.ID = %q, want %q", resolution.UI.Module.ID, "ui")
	}
	if got, want := resolution.UI.Descriptor.URL, "/apps/spaceaware-console/"; got != want {
		t.Fatalf("resolution.UI.Descriptor.URL = %q, want %q", got, want)
	}
	// UIEntry left Title/Description blank: Resolve falls back to the app's
	// own Name/Description so the launcher always has something to show.
	if got, want := resolution.UI.Descriptor.Title, "SpaceAware Console"; got != want {
		t.Fatalf("resolution.UI.Descriptor.Title = %q, want app name fallback %q", got, want)
	}
	if got, want := resolution.UI.Descriptor.Description, original.Description; got != want {
		t.Fatalf("resolution.UI.Descriptor.Description = %q, want app description fallback %q", got, want)
	}
	if got, want := resolution.UI.Descriptor.Icon, "🛰"; got != want {
		t.Fatalf("resolution.UI.Descriptor.Icon = %q, want %q", got, want)
	}
}

func TestAppManifestJSONRoundTrip(t *testing.T) {
	original := sampleManifest()

	data, err := original.MarshalCanonicalJSON()
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON() error = %v", err)
	}

	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if !reflect.DeepEqual(original, parsed) {
		t.Fatalf("round-tripped manifest differs:\n got  = %#v\n want = %#v", parsed, original)
	}

	// Re-marshaling the parsed manifest must reproduce byte-identical JSON —
	// the "stable, documented serialization" requirement.
	data2, err := parsed.MarshalCanonicalJSON()
	if err != nil {
		t.Fatalf("second MarshalCanonicalJSON() error = %v", err)
	}
	if string(data) != string(data2) {
		t.Fatalf("canonical JSON is not stable across round-trips:\n first  = %s\n second = %s", data, data2)
	}
}

func TestAppManifestMBLRoundTrip(t *testing.T) {
	original := sampleManifest()

	buf, err := original.ToMBL()
	if err != nil {
		t.Fatalf("ToMBL() error = %v", err)
	}
	if len(buf) == 0 {
		t.Fatalf("ToMBL() returned an empty buffer")
	}

	parsed, err := FromMBL(buf)
	if err != nil {
		t.Fatalf("FromMBL() error = %v", err)
	}
	if !reflect.DeepEqual(original, parsed) {
		t.Fatalf("MBL round-tripped manifest differs:\n got  = %#v\n want = %#v", parsed, original)
	}
}

func TestFromMBLDetectsPayloadTampering(t *testing.T) {
	original := sampleManifest()
	buf, err := original.ToMBL()
	if err != nil {
		t.Fatalf("ToMBL() error = %v", err)
	}
	payload, err := original.MarshalCanonicalJSON()
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON() error = %v", err)
	}

	// Locate the embedded JSON payload verbatim inside the FlatBuffer and
	// flip one byte within it. This corrupts the payload the sha256 entry
	// covers without disturbing any FlatBuffer offset/vtable structure, so
	// the failure we're asserting on is specifically the sha256 mismatch
	// check, not an incidental structural parse error.
	idx := bytes.Index(buf, payload)
	if idx < 0 {
		t.Fatalf("could not locate JSON payload inside the MBL buffer")
	}
	tampered := append([]byte(nil), buf...)
	tampered[idx] ^= 0xFF

	_, err = FromMBL(tampered)
	if err == nil {
		t.Fatalf("FromMBL() on tampered buffer: want error, got nil")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("FromMBL() error = %q, want a sha256 mismatch error", err.Error())
	}
}

func TestValidateRejectsDanglingModuleReferences(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*AppManifest)
		wantErr string
	}{
		{
			name: "data moduleId dangling",
			mutate: func(m *AppManifest) {
				m.Data[0].ModuleID = "does-not-exist"
			},
			wantErr: "does not match any modules",
		},
		{
			name: "source module ref dangling",
			mutate: func(m *AppManifest) {
				m.Sources[0].Ref = "does-not-exist"
			},
			wantErr: "does not match any modules",
		},
		{
			name: "ui moduleId dangling",
			mutate: func(m *AppManifest) {
				m.UI.ModuleID = "does-not-exist"
			},
			wantErr: "does not match any modules",
		},
		{
			name: "ui missing url",
			mutate: func(m *AppManifest) {
				m.UI.URL = ""
			},
			wantErr: "ui.url is required",
		},
		{
			name: "duplicate module id",
			mutate: func(m *AppManifest) {
				m.Modules[1].ID = m.Modules[0].ID
			},
			wantErr: "duplicate module id",
		},
		{
			name: "no modules",
			mutate: func(m *AppManifest) {
				m.Modules = nil
			},
			wantErr: "at least one module is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := sampleManifest()
			tt.mutate(manifest)
			err := manifest.Validate()
			if err == nil {
				t.Fatalf("Validate() error = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestAppManifestWithoutUIResolvesNilUI(t *testing.T) {
	manifest := sampleManifest()
	manifest.UI = nil

	resolution, err := manifest.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolution.UI != nil {
		t.Fatalf("resolution.UI = %#v, want nil for a headless app manifest", resolution.UI)
	}
}

func TestModuleLookupByID(t *testing.T) {
	manifest := sampleManifest()

	module, ok := manifest.Module("catalog-source")
	if !ok {
		t.Fatalf("Module(%q) not found", "catalog-source")
	}
	if module.PluginID != "celestrak-source" {
		t.Fatalf("Module(%q).PluginID = %q, want %q", "catalog-source", module.PluginID, "celestrak-source")
	}

	if _, ok := manifest.Module("nope"); ok {
		t.Fatalf("Module(%q) found, want not found", "nope")
	}
}
