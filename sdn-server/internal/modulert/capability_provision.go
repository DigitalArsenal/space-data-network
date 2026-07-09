package modulert

import (
	"fmt"
	"strings"
)

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
// nil module.
func ProvisionBridge(bridge *HostBridge, reg *CapabilityRegistry, capabilities []string, mod *Module) error {
	if bridge == nil {
		return fmt.Errorf("provision bridge: bridge is nil")
	}

	// Operator capability policy gate (loop B1 — defensive hardening, FAIL
	// CLOSED): the same check module.go instantiateWASM applies to the
	// generic module load path. Only enforced when the caller supplies a
	// *Module — its content hash is the policy lookup key. Flow-bundle
	// callers (mod == nil, e.g. flowrt/httpmount.go) have no stable
	// content-hash identity plumbed through this call today and are
	// unaffected by this gate; extending policy enforcement to flow
	// bundles is a follow-up (see loop B1 report).
	if mod != nil {
		var policy *CapabilityPolicyStore
		if nodeCtx := mod.NodeContext(); nodeCtx != nil {
			policy = nodeCtx.CapabilityPolicy
		}
		pluginID := ""
		if manifest := mod.Manifest(); manifest != nil {
			pluginID = manifest.PluginID
		}
		if err := checkCapabilityPolicy(policy, mod.ContentHash(), pluginID, capabilities); err != nil {
			return err
		}
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
