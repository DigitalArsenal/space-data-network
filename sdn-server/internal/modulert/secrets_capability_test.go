package modulert

import (
	"path/filepath"
	"testing"
	"time"
)

// The secrets:* family MUST be sensitive. If any of these names fell out of the
// sensitive set they would be treated as benign and DEFAULT-ALLOWED to every
// module — handing the operator's Space-Track password to unapproved code.
func TestSecretsCapabilitiesAreSensitive(t *testing.T) {
	for _, cap := range []string{
		"secrets:spacetrack",
		"secrets:edc_cpf",
		"secrets:myintelsat",
		// A lane nobody has added to sensitiveCapabilities yet: the prefix rule
		// must still gate it, so a future credential lane cannot ship
		// accidentally default-allowed.
		"secrets:some_future_provider",
	} {
		if !IsSensitiveCapability(cap) {
			t.Errorf("SECURITY: %q is not gated — it would be default-allowed to every module", cap)
		}
	}

	// Sanity: the benign names really are still benign (the prefix rule must not
	// have over-reached).
	for _, cap := range []string{"p2p_read", "clock", "crypto_hash"} {
		if IsSensitiveCapability(cap) {
			t.Errorf("%q became sensitive unexpectedly", cap)
		}
	}
}

// Every secrets:* capability must route to the single "secrets" hostcall prefix.
func TestSecretsCapabilitiesMapToSecretsPrefix(t *testing.T) {
	for _, cap := range []string{"secrets:spacetrack", "secrets:edc_cpf", "secrets:anything"} {
		if got := capPrefixFromName(cap); got != "secrets" {
			t.Errorf("capPrefixFromName(%q) = %q, want \"secrets\"", cap, got)
		}
	}
}

// FAIL CLOSED AT LOAD: a module declaring secrets:spacetrack is denied unless the
// operator approved that exact content hash for that exact lane.
func TestSecretsCapabilityDeniedWithoutOperatorApproval(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "capability_policy.json")
	policy, err := NewCapabilityPolicyStore(policyPath)
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore: %v", err)
	}

	const approvedHash = "aaaa000000000000000000000000000000000000000000000000000000000001"
	const rogueHash = "bbbb000000000000000000000000000000000000000000000000000000000002"

	// No approvals at all: denied.
	if err := checkCapabilityPolicy(policy, approvedHash, "spacetrack-module", []string{"secrets:spacetrack"}); err == nil {
		t.Fatal("SECURITY: secrets:spacetrack was granted with an empty policy")
	}

	// A nil policy store is the same as an empty one: denied.
	if err := checkCapabilityPolicy(nil, approvedHash, "spacetrack-module", []string{"secrets:spacetrack"}); err == nil {
		t.Fatal("SECURITY: secrets:spacetrack was granted with a nil policy store")
	}

	// Operator approves ONE hash for ONE lane.
	if _, err := policy.Approve(CapabilityApproval{
		ModuleHash: approvedHash,
		Capability: "secrets:spacetrack",
		ApprovedAt: time.Now(),
		ApprovedBy: "operator",
	}); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	// The approved module+lane now loads.
	if err := checkCapabilityPolicy(policy, approvedHash, "spacetrack-module", []string{"secrets:spacetrack"}); err != nil {
		t.Fatalf("approved module was denied: %v", err)
	}

	// A DIFFERENT module hash declaring the same capability is still denied —
	// approval is keyed by content hash, so recompiling or swapping the module
	// revokes it automatically.
	if err := checkCapabilityPolicy(policy, rogueHash, "spacetrack-module", []string{"secrets:spacetrack"}); err == nil {
		t.Fatal("SECURITY: an unapproved module hash was granted secrets:spacetrack")
	}

	// The approved module asking for a DIFFERENT lane is denied at load.
	if err := checkCapabilityPolicy(policy, approvedHash, "spacetrack-module", []string{"secrets:edc_cpf"}); err == nil {
		t.Fatal("SECURITY: an approval for secrets:spacetrack also granted secrets:edc_cpf")
	}

	// Revocation takes effect.
	if err := policy.Revoke(approvedHash, "secrets:spacetrack"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := checkCapabilityPolicy(policy, approvedHash, "spacetrack-module", []string{"secrets:spacetrack"}); err == nil {
		t.Fatal("SECURITY: a revoked capability was still granted")
	}
}
