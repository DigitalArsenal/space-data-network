package modulert

import (
	"os"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
)

// TestNewModuleDeniedForUnapprovedSensitiveCapability is the loop B1
// acceptance case against a real artifact: the unified licensing manifest
// declares wallet_sign (among other sensitive capabilities). With no
// recorded operator approval (fresh node, nil/empty policy), NewModule
// must refuse to load the module entirely — fail closed, no partial silent
// grant — and the error must name the unapproved capability.
func TestNewModuleDeniedForUnapprovedSensitiveCapability(t *testing.T) {
	t.Parallel()

	wasmPath := testsupport.SkipIfNoLicensingModuleWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	mod, err := NewModule(wasmBytes, nil, &NodeContext{})
	if err == nil {
		mod.Close()
		t.Fatal("expected NewModule to deny an unapproved sensitive capability, got success")
	}
	if !strings.Contains(err.Error(), "wallet_sign") {
		t.Fatalf("expected denial error to name wallet_sign, got: %v", err)
	}
	if mod != nil {
		t.Fatal("expected NewModule to return a nil module on denial")
	}
}

func TestNewModuleLoadsUnifiedLicensingArtifact(t *testing.T) {
	t.Parallel()

	wasmPath := testsupport.SkipIfNoLicensingModuleWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	// loop B1: the real licensing manifest declares sensitive capabilities
	// (ipfs, protocol_dial, wallet_sign) that now require an explicit
	// operator approval before NewModule will load it (default-deny).
	// Pre-approve them so this test keeps exercising manifest parsing;
	// approval enforcement itself is covered by capability_policy_test.go.
	policy, err := NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	moduleHash := ContentHashHex(wasmBytes)
	for _, capability := range []string{"ipfs", "protocol_dial", "wallet_sign"} {
		if _, err := policy.Approve(CapabilityApproval{
			ModuleHash: moduleHash,
			Capability: capability,
			PluginID:   "licensing",
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("Approve(%s) failed: %v", capability, err)
		}
	}

	mod, err := NewModule(wasmBytes, nil, &NodeContext{CapabilityPolicy: policy})
	if err != nil {
		t.Fatalf("NewModule(unified licensing artifact) failed: %v", err)
	}
	defer func() {
		if closeErr := mod.Close(); closeErr != nil {
			t.Fatalf("Close() failed: %v", closeErr)
		}
	}()

	manifest := mod.Manifest()
	if manifest == nil {
		t.Fatal("expected manifest to be available")
	}
	if manifest.PluginID != "licensing" {
		t.Fatalf("expected plugin id licensing, got %q", manifest.PluginID)
	}
	if len(manifest.Methods) == 0 {
		t.Fatal("expected unified licensing module to expose methods")
	}
	if len(manifest.Protocols) == 0 {
		t.Fatal("expected unified licensing module to expose protocols")
	}
}
