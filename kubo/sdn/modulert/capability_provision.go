package modulert

import (
	"fmt"
	"strings"
)

// ProvisionIdentity carries the content-hash identity used for the operator
// capability-policy gate (checkCapabilityPolicy) when the caller has no
// *Module to derive it from — e.g. a compiled flow bundle (see
// flowrt/httpmount.go loadFlowInstance), which is a raw WASM artifact driven
// through the hostcall bridge without ever becoming a *Module.
//
// Loop B1-followup: before this existed, ProvisionBridge's policy gate was
// enforced ONLY when mod != nil. In practice mod is nil at ProvisionBridge's
// one production call site (the flow-bundle mount path) — the gate was
// therefore always skipped there, letting a flow bundle obtain sensitive
// capabilities (wallet_sign, http, storage_write, ...) with no operator
// approval record. ProvisionIdentity lets a mod==nil caller supply the exact
// same three inputs checkCapabilityPolicy needs, so the gate applies
// uniformly regardless of whether the artifact is a *Module or a flow bundle.
type ProvisionIdentity struct {
	// ContentHash is the lowercase hex SHA-256 digest (ContentHashHex) of the
	// artifact's raw, canonical WASM bytes — the SAME bytes an operator
	// hashes to record a CapabilityApproval. For flow bundles this MUST be
	// computed from the portable artifact bytes (post publication-trailer
	// strip, pre-AOT-compile): an AOT-compiled artifact's bytes are
	// platform/runtime-version-specific, so hashing them would make a
	// recorded approval silently stop matching on a different host or
	// libwasmedge version. A blank ContentHash denies every sensitive
	// capability — checkCapabilityPolicy/IsApproved never match a blank
	// identity — so an identity-less caller fails closed rather than
	// bypassing the gate.
	ContentHash string
	// PluginID is display/audit-only, exactly as CapabilityApproval.PluginID
	// — never consulted for the approve/deny decision.
	PluginID string
	// Policy is the operator's persisted capability allowlist. A nil Policy
	// is equivalent to an empty one (checkCapabilityPolicy): default deny.
	Policy *CapabilityPolicyStore
}

// Lookup returns the registered factory for a capability.
func (r *CapabilityRegistry) Lookup(capability string) (BridgeCapFactory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[capability]
	return factory, ok
}

// ProvisionBridge restricts the bridge's granted set to exactly the given
// capability list and registers a handler from the registry for each one.
// Unlike the tolerant module path (which logs and skips capabilities the node
// cannot serve), this REJECTS: it returns an error naming every capability the
// registry has no factory for, so callers can refuse to load an artifact whose
// declared capability set the host cannot satisfy.
//
// mod may be nil for artifacts that are not driven through the Module
// invocation surface (e.g. compiled flow bundles); factories must tolerate a
// nil module. When mod is nil, identity supplies the content-hash identity
// for the capability-policy gate below (see ProvisionIdentity); when mod is
// non-nil, identity is ignored in favor of the Module's own identity — the
// two are never merged, so a caller cannot smuggle an approval past a real
// module's actual content hash.
func ProvisionBridge(bridge *HostBridge, reg *CapabilityRegistry, capabilities []string, mod *Module, identity ProvisionIdentity) error {
	if bridge == nil {
		return fmt.Errorf("provision bridge: bridge is nil")
	}

	// Operator capability policy gate (loop B1 — defensive hardening, FAIL
	// CLOSED; extended to mod==nil callers in loop B1-followup): the same
	// check module.go instantiateWASM applies to the generic module load
	// path now ALSO runs for flow bundles — there is no branch that skips
	// this gate. A flow bundle requesting a sensitive capability with no
	// recorded approval for its content hash is denied exactly like a
	// module would be.
	contentHash := identity.ContentHash
	pluginID := identity.PluginID
	policy := identity.Policy
	if mod != nil {
		contentHash = mod.ContentHash()
		pluginID = ""
		if manifest := mod.Manifest(); manifest != nil {
			pluginID = manifest.PluginID
		}
		policy = nil
		if nodeCtx := mod.NodeContext(); nodeCtx != nil {
			policy = nodeCtx.CapabilityPolicy
		}
	}
	if err := checkCapabilityPolicy(policy, contentHash, pluginID, capabilities); err != nil {
		return err
	}

	granted := make(map[string]bool, len(capabilities))
	for _, c := range capabilities {
		granted[c] = true
	}
	bridge.granted = granted

	var missing []string
	for _, capability := range capabilities {
		var factory BridgeCapFactory
		ok := false
		if reg != nil {
			factory, ok = reg.Lookup(capability)
		}
		if !ok {
			missing = append(missing, capability)
			continue
		}
		bridge.RegisterCapHandler(capPrefixFromName(capability), factory(mod, bridge))
	}
	if len(missing) > 0 {
		return fmt.Errorf("host cannot satisfy required capabilities: %s", strings.Join(missing, ", "))
	}
	return nil
}
