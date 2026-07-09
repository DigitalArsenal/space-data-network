package modulert

// Loop B1-followup — defensive hardening, FAIL CLOSED.
//
// GAP: capability_policy.go's default-deny gate (loop B1) was enforced by
// ProvisionBridge ONLY when the caller supplied a *Module. The ONLY
// production caller of ProvisionBridge is flowrt/httpmount.go's
// loadFlowInstance (the flow-bundle mount path), and it ALWAYS passes
// mod == nil — flow bundles are raw WASM artifacts driven through the
// hostcall bridge without ever becoming a *Module. That meant the policy
// gate was unconditionally skipped in the only place it actually ran,
// letting a flow bundle obtain sensitive capabilities (wallet_sign, http,
// storage_write, ...) with no operator approval record at all.
//
// FIX: ProvisionBridge now takes a ProvisionIdentity the caller supplies
// when mod == nil, and always runs checkCapabilityPolicy — never a skipped
// branch. flowrt/httpmount.go (loadFlowInstance) and flowrt/cronmount.go
// (LoadFlowService) compute the flow bundle's content hash from its
// portable, pre-AOT WASM bytes and pass it through.
//
// These tests exercise ProvisionBridge directly the same way the
// flow-bundle mount path calls it (mod == nil), proving the gate now
// applies uniformly. They complement capability_policy_test.go's coverage
// of checkCapabilityPolicy itself (the module path's existing behavior).

import (
	"strings"
	"testing"
)

// TestProvisionBridgeFlowBundleDeniedWithoutApproval proves (a): a flow
// bundle (mod == nil) requesting a sensitive capability with no policy entry
// is DENIED at provision — both against a nil policy store and against an
// empty, freshly created one.
func TestProvisionBridgeFlowBundleDeniedWithoutApproval(t *testing.T) {
	t.Parallel()

	reg := NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_write", func(mod *Module, bridge *HostBridge) CapHandler {
		t.Fatal("factory must not be invoked when the capability policy denies the load")
		return nil
	})

	wasmBytes := []byte("fake flow bundle wasm bytes — no approval recorded")
	contentHash := ContentHashHex(wasmBytes)

	t.Run("nil policy store", func(t *testing.T) {
		bridge := NewHostBridge(nil, nil)
		err := ProvisionBridge(bridge, reg, []string{"storage_write"}, nil, ProvisionIdentity{
			ContentHash: contentHash,
			PluginID:    "example-flow",
			Policy:      nil,
		})
		if err == nil {
			t.Fatal("expected flow bundle requesting storage_write with a nil policy store to be DENIED, got nil error")
		}
		if !strings.Contains(err.Error(), "storage_write") {
			t.Fatalf("expected denial error to name storage_write, got: %v", err)
		}
	})

	t.Run("empty freshly created store", func(t *testing.T) {
		policy, err := NewCapabilityPolicyStore("") // in-memory, empty
		if err != nil {
			t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
		}
		bridge := NewHostBridge(nil, nil)
		err = ProvisionBridge(bridge, reg, []string{"storage_write"}, nil, ProvisionIdentity{
			ContentHash: contentHash,
			PluginID:    "example-flow",
			Policy:      policy,
		})
		if err == nil {
			t.Fatal("expected flow bundle requesting storage_write against an empty policy store to be DENIED, got nil error")
		}
		if !strings.Contains(err.Error(), "storage_write") {
			t.Fatalf("expected denial error to name storage_write, got: %v", err)
		}
	})

	t.Run("no identity supplied at all (blank ContentHash)", func(t *testing.T) {
		// A caller that forgot to plumb an identity through must still fail
		// closed, not silently skip the gate — a blank ContentHash can never
		// match a recorded approval (IsApproved's blank-identity default).
		policy, err := NewCapabilityPolicyStore("")
		if err != nil {
			t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
		}
		if _, err := policy.Approve(CapabilityApproval{ModuleHash: contentHash, Capability: "storage_write"}); err != nil {
			t.Fatalf("Approve failed: %v", err)
		}
		bridge := NewHostBridge(nil, nil)
		err = ProvisionBridge(bridge, reg, []string{"storage_write"}, nil, ProvisionIdentity{Policy: policy})
		if err == nil {
			t.Fatal("expected a blank-identity flow bundle to be DENIED even though an approval exists for a real content hash")
		}
	})
}

// TestProvisionBridgeFlowBundleAllowedWithApproval proves (b): once the
// operator records an approval keyed by the flow bundle's own content hash,
// the same mod == nil provisioning call succeeds and actually wires the
// capability (bridge.granted is set and the factory runs with the flow
// bundle's nil *Module, per BridgeCapFactory's documented nil-tolerant
// contract).
func TestProvisionBridgeFlowBundleAllowedWithApproval(t *testing.T) {
	t.Parallel()

	reg := NewCapabilityRegistry()
	called := false
	reg.RegisterBridgeAware("storage_write", func(mod *Module, bridge *HostBridge) CapHandler {
		called = true
		if mod != nil {
			t.Fatal("expected the flow bundle's nil *Module to be passed through to the factory unchanged")
		}
		return func(operation string, payload []byte) ([]byte, error) { return nil, nil }
	})

	wasmBytes := []byte("fake flow bundle wasm bytes — approved this time")
	contentHash := ContentHashHex(wasmBytes)

	policy, err := NewCapabilityPolicyStore("") // in-memory
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	if _, err := policy.Approve(CapabilityApproval{
		ModuleHash: contentHash,
		Capability: "storage_write",
		PluginID:   "example-flow",
	}); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	bridge := NewHostBridge(nil, nil)
	err = ProvisionBridge(bridge, reg, []string{"storage_write"}, nil, ProvisionIdentity{
		ContentHash: contentHash,
		PluginID:    "example-flow",
		Policy:      policy,
	})
	if err != nil {
		t.Fatalf("expected approved flow bundle capability to provision, got: %v", err)
	}
	if !called {
		t.Fatal("expected the storage_write factory to be invoked once the capability was granted")
	}
	if !bridge.granted["storage_write"] {
		t.Fatal("expected storage_write to be recorded in bridge.granted")
	}

	// A DIFFERENT flow bundle's bytes (different content hash) must still be
	// denied even though the capability name and plugin id are identical —
	// approvals are per-artifact-bytes, not per-declared-plugin-id (a
	// spoofable manifest field), matching the module path's existing
	// TestCheckCapabilityPolicyApprovalAllows guarantee.
	otherHash := ContentHashHex([]byte("a completely different flow bundle"))
	err = ProvisionBridge(NewHostBridge(nil, nil), reg, []string{"storage_write"}, nil, ProvisionIdentity{
		ContentHash: otherHash,
		PluginID:    "example-flow",
		Policy:      policy,
	})
	if err == nil {
		t.Fatal("expected an unapproved flow bundle content hash to be denied even with an identical plugin id/capability approved for a different hash")
	}
}

// TestProvisionBridgeFlowBundleBenignDefaultAllow proves (c): the benign
// default-allow tier still works for a flow bundle (mod == nil) with NO
// policy store configured at all — the loop B1-followup fix must not turn
// benign capabilities sensitive as a side effect of closing the mod==nil
// gap.
func TestProvisionBridgeFlowBundleBenignDefaultAllow(t *testing.T) {
	t.Parallel()

	reg := NewCapabilityRegistry()
	reg.RegisterBridgeAware("p2p_read", func(mod *Module, bridge *HostBridge) CapHandler {
		return func(operation string, payload []byte) ([]byte, error) { return nil, nil }
	})

	wasmBytes := []byte("fake flow bundle wasm bytes — benign capability only")
	contentHash := ContentHashHex(wasmBytes)

	bridge := NewHostBridge(nil, nil)
	err := ProvisionBridge(bridge, reg, []string{"p2p_read"}, nil, ProvisionIdentity{
		ContentHash: contentHash,
		PluginID:    "example-flow",
		Policy:      nil, // no policy store at all
	})
	if err != nil {
		t.Fatalf("expected benign capability p2p_read to default-allow for a flow bundle with no policy store, got: %v", err)
	}
	if !bridge.granted["p2p_read"] {
		t.Fatal("expected p2p_read to be recorded in bridge.granted")
	}
}

// TestProvisionBridgeModulePathIgnoresPassedIdentity is a regression guard
// for the mod != nil branch: it must keep deriving identity from the
// *Module itself (ContentHash/Manifest/NodeContext) and IGNORE whatever
// ProvisionIdentity a caller passes alongside it, so the flow-bundle fix
// cannot be used to smuggle an approval past — or weaken — a real module's
// actual content-hash identity. mod is a package-internal literal here
// (never instantiated against real WASM) purely to exercise ProvisionBridge's
// identity-selection branch; module.go's own instantiateWASM path (the real
// module load path) applies the equivalent check inline and is unchanged by
// this loop.
func TestProvisionBridgeModulePathIgnoresPassedIdentity(t *testing.T) {
	t.Parallel()

	reg := NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_write", func(mod *Module, bridge *HostBridge) CapHandler {
		return func(operation string, payload []byte) ([]byte, error) { return nil, nil }
	})

	const realModuleHash = "real-module-content-hash"
	policy, err := NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	if _, err := policy.Approve(CapabilityApproval{ModuleHash: realModuleHash, Capability: "storage_write"}); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	mod := &Module{
		contentHash: realModuleHash,
		manifest:    &Manifest{PluginID: "real-module"},
		nodeCtx:     &NodeContext{CapabilityPolicy: policy},
	}

	// A bogus/mismatched identity struct passed alongside a real *Module
	// must be ignored — the *Module's own approved hash still governs, and
	// the load succeeds.
	bridge := NewHostBridge(nil, nil)
	if err := ProvisionBridge(bridge, reg, []string{"storage_write"}, mod, ProvisionIdentity{
		ContentHash: "attacker-supplied-hash-that-is-not-approved",
		PluginID:    "attacker-plugin",
		Policy:      nil,
	}); err != nil {
		t.Fatalf("expected the real *Module's own approved content hash to govern regardless of the passed identity, got: %v", err)
	}

	// Conversely, a module whose ACTUAL hash has no approval is denied even
	// if the passed-in identity struct points at an approved hash — the
	// identity parameter can never override mod's own identity.
	unapprovedMod := &Module{
		contentHash: "unapproved-module-hash",
		manifest:    &Manifest{PluginID: "real-module"},
		nodeCtx:     &NodeContext{CapabilityPolicy: policy},
	}
	err = ProvisionBridge(NewHostBridge(nil, nil), reg, []string{"storage_write"}, unapprovedMod, ProvisionIdentity{
		ContentHash: realModuleHash, // approved hash, but NOT this module's own hash
		PluginID:    "real-module",
		Policy:      policy,
	})
	if err == nil {
		t.Fatal("expected the module's own (unapproved) content hash to govern — the passed identity must not smuggle an approval past it")
	}
}
