package modulert

import "testing"

// TestUIDescriptorURLEmptyByDefault documents the pre-H1 back-compat
// contract: a module that never has SetUIURL called on it (i.e. it has no
// app-manifest UI entry, or wasn't loaded as part of an app at all) reports
// an empty UIDescriptor.URL, exactly as before SetUIURL existed.
func TestUIDescriptorURLEmptyByDefault(t *testing.T) {
	m := &Module{manifest: &Manifest{
		PluginID: "demo-module",
		Name:     "Demo Module",
		Version:  "1.0.0",
	}}

	desc := m.UIDescriptor()
	if desc.URL != "" {
		t.Fatalf("UIDescriptor().URL = %q, want empty for a module with no assigned UI URL", desc.URL)
	}
	if desc.Title != "Demo Module" {
		t.Fatalf("UIDescriptor().Title = %q, want %q", desc.Title, "Demo Module")
	}
}

// TestSetUIURLPopulatesUIDescriptor verifies the write side of app-manifest
// UI resolution (H1 loop): once SetUIURL is called — as node/cmd startup
// wiring does after resolving an app manifest's UI entry via
// internal/appmanifest.AppManifest.Resolve — UIDescriptor().URL reflects
// it.
func TestSetUIURLPopulatesUIDescriptor(t *testing.T) {
	m := &Module{manifest: &Manifest{
		PluginID: "spaceaware-ui",
		Name:     "SpaceAware UI",
		Version:  "0.9.0",
	}}

	if got := m.UIDescriptor().URL; got != "" {
		t.Fatalf("UIDescriptor().URL before SetUIURL = %q, want empty", got)
	}

	m.SetUIURL("/apps/spaceaware-console/")

	desc := m.UIDescriptor()
	if got, want := desc.URL, "/apps/spaceaware-console/"; got != want {
		t.Fatalf("UIDescriptor().URL after SetUIURL = %q, want %q", got, want)
	}
	// Everything else about the descriptor is unchanged by SetUIURL.
	if desc.Title != "SpaceAware UI" {
		t.Fatalf("UIDescriptor().Title = %q, want %q", desc.Title, "SpaceAware UI")
	}

	// SetUIURL is a plain assignment: calling it again (e.g. the app
	// manifest is re-resolved after a reload) reflects the latest value.
	m.SetUIURL("/apps/spaceaware-console/v2/")
	if got, want := m.UIDescriptor().URL, "/apps/spaceaware-console/v2/"; got != want {
		t.Fatalf("UIDescriptor().URL after second SetUIURL = %q, want %q", got, want)
	}
}

// TestModuleImplementsUIURLSetter is a compile-time-adjacent smoke test
// that *Module satisfies plugins.UIURLSetter, the interface
// plugins.Manager.SetModuleUIURL type-asserts against.
func TestModuleImplementsUIURLSetter(t *testing.T) {
	var _ interface{ SetUIURL(string) } = &Module{manifest: &Manifest{}}
}
