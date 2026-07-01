package node

import (
	"testing"

	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
	"github.com/spacedatanetwork/sdn-server/internal/license"
)

// TestBuildPublicationDescriptorFrameEmitsDependencies asserts that an asset's
// declared dependencies round-trip onto the PLG publication wire as a
// PLG.DEPENDENCIES vector, in order, with optional version bounds preserved.
func TestBuildPublicationDescriptorFrameEmitsDependencies(t *testing.T) {
	t.Parallel()

	asset := &license.PluginAsset{
		ID:      "com.orbpro.starlink-source",
		Version: "1.0.0",
		Dependencies: []license.PluginDependencyRef{
			{PluginID: "com.orbpro.starlink-parser", MinVersion: "1.0.0", MaxVersion: "2.0.0"},
			{PluginID: "com.orbpro.starlink-validator", MinVersion: "0.9.0"},
		},
	}

	frame, err := buildPublicationDescriptorFrame(asset)
	if err != nil {
		t.Fatalf("buildPublicationDescriptorFrame() error = %v", err)
	}

	manifest := plg.GetRootAsPLG(frame, 0)
	if got := manifest.DEPENDENCIESLength(); got != 2 {
		t.Fatalf("DEPENDENCIESLength() = %d, want 2", got)
	}

	var dep plg.PluginDependency
	if !manifest.DEPENDENCIES(&dep, 0) {
		t.Fatal("DEPENDENCIES(0) returned false")
	}
	if got := string(dep.PLUGIN_ID()); got != "com.orbpro.starlink-parser" {
		t.Errorf("dep[0].PLUGIN_ID = %q, want com.orbpro.starlink-parser", got)
	}
	if got := string(dep.MIN_VERSION()); got != "1.0.0" {
		t.Errorf("dep[0].MIN_VERSION = %q, want 1.0.0", got)
	}
	if got := string(dep.MAX_VERSION()); got != "2.0.0" {
		t.Errorf("dep[0].MAX_VERSION = %q, want 2.0.0", got)
	}

	if !manifest.DEPENDENCIES(&dep, 1) {
		t.Fatal("DEPENDENCIES(1) returned false")
	}
	if got := string(dep.PLUGIN_ID()); got != "com.orbpro.starlink-validator" {
		t.Errorf("dep[1].PLUGIN_ID = %q, want com.orbpro.starlink-validator", got)
	}
	if got := string(dep.MIN_VERSION()); got != "0.9.0" {
		t.Errorf("dep[1].MIN_VERSION = %q, want 0.9.0", got)
	}
	if got := dep.MAX_VERSION(); got != nil {
		t.Errorf("dep[1].MAX_VERSION = %q, want empty/nil (unset)", string(got))
	}
}

// TestBuildPublicationDescriptorFrameNoDependencies ensures the DEPENDENCIES
// vector is simply absent (length 0) when an asset declares none.
func TestBuildPublicationDescriptorFrameNoDependencies(t *testing.T) {
	t.Parallel()

	frame, err := buildPublicationDescriptorFrame(&license.PluginAsset{ID: "x", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("buildPublicationDescriptorFrame() error = %v", err)
	}
	if got := plg.GetRootAsPLG(frame, 0).DEPENDENCIESLength(); got != 0 {
		t.Fatalf("DEPENDENCIESLength() = %d, want 0", got)
	}
}
