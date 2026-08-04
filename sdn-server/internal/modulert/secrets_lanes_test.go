package modulert

import (
	"path/filepath"
	"testing"
	"time"
)

// Operator-defined credential lanes (owner 2026-08-04). Lane ids are not known
// at build time, so the guarantees below cannot rest on any enumeration:
//
//  1. every secrets:<lane> is sensitive BY PREFIX, so an arbitrary lane is
//     denied at load without an explicit per-content-hash approval;
//  2. an approval for one lane conveys nothing about another;
//  3. the registry resolves an arbitrary lane to the secrets handler through
//     the family registration, so a lane created after boot works without a
//     daemon restart — and no OTHER capability family is widened by it.

const (
	opLaneCapA = "secrets:acme-weather"
	opLaneCapB = "secrets:zephyr_billing"
)

// FAIL CLOSED AT LOAD for a lane nobody enumerated.
func TestOperatorDefinedLaneRequiresPerHashApproval(t *testing.T) {
	policy, err := NewCapabilityPolicyStore(filepath.Join(t.TempDir(), "capability_policy.json"))
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore: %v", err)
	}

	const approvedHash = "cccc000000000000000000000000000000000000000000000000000000000003"
	const rogueHash = "dddd000000000000000000000000000000000000000000000000000000000004"

	if !IsSensitiveCapability(opLaneCapA) {
		t.Fatalf("SECURITY: %q is not gated — an operator-defined lane would be default-allowed", opLaneCapA)
	}

	// Empty policy: denied.
	if err := checkCapabilityPolicy(policy, approvedHash, "acme-module", []string{opLaneCapA}); err == nil {
		t.Fatalf("SECURITY: %s was granted with an empty policy", opLaneCapA)
	}

	// Operator approves ONE hash for ONE operator-defined lane.
	if _, err := policy.Approve(CapabilityApproval{
		ModuleHash: approvedHash,
		Capability: opLaneCapA,
		ApprovedAt: time.Now(),
		ApprovedBy: "operator",
	}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := checkCapabilityPolicy(policy, approvedHash, "acme-module", []string{opLaneCapA}); err != nil {
		t.Fatalf("the approved module+lane was denied: %v", err)
	}

	// A different hash declaring the same lane: denied.
	if err := checkCapabilityPolicy(policy, rogueHash, "acme-module", []string{opLaneCapA}); err == nil {
		t.Fatalf("SECURITY: an unapproved module hash was granted %s", opLaneCapA)
	}
	// The approved hash asking for a DIFFERENT operator-defined lane: denied.
	if err := checkCapabilityPolicy(policy, approvedHash, "acme-module", []string{opLaneCapB}); err == nil {
		t.Fatalf("SECURITY: an approval for %s also granted %s", opLaneCapA, opLaneCapB)
	}
	// ...and a well-known lane is no more reachable than any other.
	if err := checkCapabilityPolicy(policy, approvedHash, "acme-module", []string{"secrets:spacetrack"}); err == nil {
		t.Fatalf("SECURITY: an approval for %s also granted secrets:spacetrack", opLaneCapA)
	}
}

// The registry resolves ANY secrets:<lane> through the family registration —
// including lanes that did not exist when the daemon booted.
func TestRegisterFamilyResolvesArbitrarySecretsLanes(t *testing.T) {
	reg := NewCapabilityRegistry()
	served := ""
	reg.RegisterFamily(SecretsCapabilityPrefix, func(_ *Module, _ *HostBridge) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			served = op
			return []byte(`{"ok":true}`), nil
		}
	})

	for _, cap := range []string{
		"secrets:spacetrack",
		"secrets:acme-weather",
		"secrets:a_lane_invented_after_boot",
	} {
		factory, ok := reg.Lookup(cap)
		if !ok {
			t.Fatalf("Lookup(%q) = not found; an operator-defined lane would need a daemon restart", cap)
		}
		if _, err := factory(nil, nil)("secrets.get", nil); err != nil {
			t.Fatalf("%s handler: %v", cap, err)
		}
		if served != "secrets.get" {
			t.Fatalf("%s did not route to the secrets handler", cap)
		}
		served = ""
	}

	// The family must not over-reach: names outside the prefix stay unresolved,
	// so registering it cannot silently satisfy some other capability.
	for _, cap := range []string{"http", "wallet_sign", "storage_write", "secrets", "secretsfoo", "xsecrets:acme"} {
		if _, ok := reg.Lookup(cap); ok {
			t.Errorf("SECURITY: the secrets family resolved unrelated capability %q", cap)
		}
	}
}

// Exact registrations win over a family, and the longest family prefix wins —
// so a family can never shadow a more specific registration.
func TestExactRegistrationBeatsFamily(t *testing.T) {
	reg := NewCapabilityRegistry()
	mark := func(id string) BridgeCapFactory {
		return func(_ *Module, _ *HostBridge) CapHandler {
			return func(string, []byte) ([]byte, error) { return []byte(id), nil }
		}
	}
	reg.RegisterFamily("secrets:", mark("family"))
	reg.RegisterFamily("secrets:vault-", mark("longer-family"))
	reg.RegisterBridgeAware("secrets:spacetrack", mark("exact"))

	for cap, want := range map[string]string{
		"secrets:spacetrack":   "exact",
		"secrets:acme":         "family",
		"secrets:vault-acme":   "longer-family",
		"secrets:vaultishacme": "family",
	} {
		factory, ok := reg.Lookup(cap)
		if !ok {
			t.Fatalf("Lookup(%q) = not found", cap)
		}
		got, _ := factory(nil, nil)("", nil)
		if string(got) != want {
			t.Errorf("Lookup(%q) resolved to %q, want %q", cap, got, want)
		}
	}

	// A blank prefix must be refused rather than installed as a catch-all that
	// would satisfy every capability name on the node.
	reg.RegisterFamily("   ", mark("catch-all"))
	if _, ok := reg.Lookup("http"); ok {
		t.Fatal("SECURITY: a blank family prefix installed a catch-all factory")
	}
}
