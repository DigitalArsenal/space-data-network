package flowrt

// Shared fixture helper (loop B1-followup companion): capability_provision.go
// ProvisionBridge now ALWAYS runs the operator capability-policy gate for
// flow bundles (checkCapabilityPolicy), even in these package's own
// integration-test fixtures that mount a REAL compiled flow bundle. A
// fixture requesting a sensitive capability (http, storage_query,
// storage_ingest, ...) with no recorded operator approval for its content
// hash is denied fail-closed — exactly like production. These fixtures must
// record a test-scoped approval before loading, mirroring what an operator
// does via modulert.CapabilityPolicyAPI / the modulert package's own
// capability_provision_test.go coverage of ProvisionBridge.
//
// The approval MUST be keyed by the bundle's REAL content hash — computed
// the SAME way production does (modulert.ContentHashHex over the portable,
// publication-trailer-stripped wasm bytes; see readFlowArtifactForTest in
// engine_link_test.go, which LoadMountedFlow/LoadFlowService mirror) —
// rather than a hard-coded digest, so these fixtures keep working unchanged
// when the fixture flow bundles under space-data-network-modules are
// rebuilt.

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// newTestCapabilityPolicy returns a fresh, empty, in-memory (no disk
// persistence — path "") capability policy store for one test.
func newTestCapabilityPolicy(t *testing.T) *modulert.CapabilityPolicyStore {
	t.Helper()
	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore: %v", err)
	}
	return policy
}

// approveFlowCapabilities records an operator approval on policy for every
// name in capabilities, keyed by the ACTUAL content hash of the compiled
// flow bundle at dist (a bundle directory or a direct .wasm path — the same
// shapes resolveFlowArtifact accepts). Safe to call more than once for the
// same (dist, capability) pair (Approve is idempotent).
func approveFlowCapabilities(t *testing.T, policy *modulert.CapabilityPolicyStore, dist string, capabilities ...string) {
	t.Helper()
	wasmBytes, err := readFlowArtifactForTest(dist)
	if err != nil {
		t.Fatalf("read flow artifact %s: %v", dist, err)
	}
	hash := modulert.ContentHashHex(wasmBytes)
	for _, capability := range capabilities {
		if _, err := policy.Approve(modulert.CapabilityApproval{
			ModuleHash: hash,
			Capability: capability,
			PluginID:   "flowrt-test-fixture",
			Note:       "test-scoped approval recorded by a flowrt integration test fixture",
		}); err != nil {
			t.Fatalf("Approve(%s, %s): %v", dist, capability, err)
		}
	}
}

// approvedCapabilityPolicy is a convenience wrapper for the common case of
// one flow bundle mounted once: a fresh in-memory policy store with
// capabilities pre-approved for dist's content hash.
func approvedCapabilityPolicy(t *testing.T, dist string, capabilities ...string) *modulert.CapabilityPolicyStore {
	t.Helper()
	policy := newTestCapabilityPolicy(t)
	approveFlowCapabilities(t, policy, dist, capabilities...)
	return policy
}
