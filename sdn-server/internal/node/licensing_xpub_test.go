package node

import (
	"testing"

	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
	"github.com/spacedatanetwork/sdn-server/internal/license"
)

// The per-module xpub allowlist must round-trip into the PLG descriptor that the
// licensing/core wasm gate reads (descriptor.ALLOWED_XPUBS); otherwise the PKI gate
// can never enforce and stays open. This pins the plumbing from PluginAsset through
// the FlatBuffer the provider hands to server_publish_module.
func TestBuildPublicationDescriptorFrameCarriesAllowedXpubs(t *testing.T) {
	asset := &license.PluginAsset{
		ID:           "orbpro:test-module",
		Version:      "1.0.0",
		AllowedXpubs: []string{"xpubDEVKEY", "xpubSECOND"},
	}
	frame, err := buildPublicationDescriptorFrame(asset)
	if err != nil {
		t.Fatalf("build descriptor: %v", err)
	}
	root := plg.GetRootAsPLG(frame, 0)
	if got := root.ALLOWED_XPUBSLength(); got != 2 {
		t.Fatalf("ALLOWED_XPUBS length = %d, want 2", got)
	}
	seen := map[string]bool{}
	for i := 0; i < root.ALLOWED_XPUBSLength(); i++ {
		seen[string(root.ALLOWED_XPUBS(i))] = true
	}
	if !seen["xpubDEVKEY"] || !seen["xpubSECOND"] {
		t.Fatalf("ALLOWED_XPUBS = %v, want xpubDEVKEY + xpubSECOND", seen)
	}
}

// No allowlist => empty ALLOWED_XPUBS => the gate is open (legacy / unrestricted).
func TestBuildPublicationDescriptorFrameOmitsEmptyAllowedXpubs(t *testing.T) {
	asset := &license.PluginAsset{ID: "orbpro:open", Version: "1.0.0"}
	frame, err := buildPublicationDescriptorFrame(asset)
	if err != nil {
		t.Fatalf("build descriptor: %v", err)
	}
	root := plg.GetRootAsPLG(frame, 0)
	if got := root.ALLOWED_XPUBSLength(); got != 0 {
		t.Fatalf("ALLOWED_XPUBS length = %d, want 0 (open)", got)
	}
}
