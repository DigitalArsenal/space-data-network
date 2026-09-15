package modulert

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

func TestIsSensitiveCapabilityTiering(t *testing.T) {
	t.Parallel()

	sensitive := []string{
		"wallet_sign", "http", "process_exec", "storage_write",
		"storage_ingest", "protocol_dial", "host_control",
		"storage_query", "storage_adapter", "ipfs", "pubsub",
	}
	for _, cap := range sensitive {
		if !IsSensitiveCapability(cap) {
			t.Errorf("IsSensitiveCapability(%q) = false, want true", cap)
		}
	}

	benign := []string{
		"p2p_read", "crypto_hash", "crypto_sign", "crypto_verify",
		"crypto_encrypt", "crypto_decrypt", "crypto_key_agreement", "crypto_kdf",
		"clock", "random", "logging", "timers", "protocol_handle",
	}
	for _, cap := range benign {
		if IsSensitiveCapability(cap) {
			t.Errorf("IsSensitiveCapability(%q) = true, want false", cap)
		}
	}
}

// TestCheckCapabilityPolicyDefaultDenyFreshNode covers the loop B1
// acceptance criterion: a fresh node (nil policy AND an empty, freshly
// created store) denies sensitive capabilities by default.
func TestCheckCapabilityPolicyDefaultDenyFreshNode(t *testing.T) {
	t.Parallel()

	t.Run("nil policy", func(t *testing.T) {
		err := checkCapabilityPolicy(nil, "deadbeef", "example-module", []string{"wallet_sign"})
		if err == nil {
			t.Fatal("expected denial with nil policy store")
		}
		if !strings.Contains(err.Error(), "wallet_sign") {
			t.Fatalf("expected error to name wallet_sign, got: %v", err)
		}
	})

	t.Run("empty freshly created store", func(t *testing.T) {
		dir := t.TempDir()
		policy, err := NewCapabilityPolicyStore(filepath.Join(dir, "does-not-exist.json"))
		if err != nil {
			t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
		}
		err = checkCapabilityPolicy(policy, "deadbeef", "example-module", []string{"wallet_sign"})
		if err == nil {
			t.Fatal("expected denial with an empty policy store")
		}
		if !strings.Contains(err.Error(), "wallet_sign") {
			t.Fatalf("expected error to name wallet_sign, got: %v", err)
		}
	})
}

// TestCheckCapabilityPolicyApprovalAllows covers: an approved (module,
// capability) pair passes the gate.
func TestCheckCapabilityPolicyApprovalAllows(t *testing.T) {
	t.Parallel()

	policy, err := NewCapabilityPolicyStore("") // in-memory
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	const hash = "deadbeef"
	if _, err := policy.Approve(CapabilityApproval{
		ModuleHash: hash,
		Capability: "wallet_sign",
		PluginID:   "example-module",
	}); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	if err := checkCapabilityPolicy(policy, hash, "example-module", []string{"wallet_sign"}); err != nil {
		t.Fatalf("expected approved capability to pass, got: %v", err)
	}

	// A DIFFERENT module hash requesting the same capability must still be
	// denied — approvals are keyed per-artifact, not per-capability-name.
	if err := checkCapabilityPolicy(policy, "some-other-hash", "example-module", []string{"wallet_sign"}); err == nil {
		t.Fatal("expected denial for an unapproved module hash even with the same plugin id/capability")
	}
}

// TestCheckCapabilityPolicyBenignDefaultAllow covers: a benign capability
// works with no policy entry at all (nil policy, no approval recorded).
func TestCheckCapabilityPolicyBenignDefaultAllow(t *testing.T) {
	t.Parallel()

	if err := checkCapabilityPolicy(nil, "deadbeef", "example-module", []string{"p2p_read"}); err != nil {
		t.Fatalf("expected benign capability p2p_read to default-allow, got: %v", err)
	}

	policy, err := NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	if err := checkCapabilityPolicy(policy, "deadbeef", "example-module", []string{"crypto_hash", "p2p_read"}); err != nil {
		t.Fatalf("expected benign capabilities to default-allow against an empty store, got: %v", err)
	}
}

// TestCapabilityPolicyStorePersistsAcrossReload covers: an approval
// recorded through one store instance is visible after a fresh
// NewCapabilityPolicyStore/Reload against the same file — i.e. it survives
// a restart.
func TestCapabilityPolicyStorePersistsAcrossReload(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "capability_policy.json")

	first, err := NewCapabilityPolicyStore(path)
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	const hash = "cafebabe"
	if _, err := first.Approve(CapabilityApproval{
		ModuleHash: hash,
		Capability: "http",
		PluginID:   "example-module",
		ApprovedBy: "operator",
	}); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	// Fresh store instance over the same path — simulates a node restart.
	second, err := NewCapabilityPolicyStore(path)
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore (reload) failed: %v", err)
	}
	if !second.IsApproved(hash, "http") {
		t.Fatal("expected approval to survive a fresh store load from the same path")
	}

	// Reload() on the original instance also picks up disk state (covers
	// out-of-band edits / explicit operator reload).
	third, err := NewCapabilityPolicyStore(path)
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	if _, err := second.Approve(CapabilityApproval{ModuleHash: hash, Capability: "storage_write"}); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	if third.IsApproved(hash, "storage_write") {
		t.Fatal("third store instance should not see the new approval before Reload()")
	}
	if err := third.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if !third.IsApproved(hash, "storage_write") {
		t.Fatal("expected Reload() to pick up the approval written by another store instance")
	}
}

// TestCapabilityPolicyStoreRevoke covers: revoking an approval denies the
// capability again.
func TestCapabilityPolicyStoreRevoke(t *testing.T) {
	t.Parallel()

	policy, err := NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	const hash = "abc123"
	if _, err := policy.Approve(CapabilityApproval{ModuleHash: hash, Capability: "wallet_sign"}); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	if !policy.IsApproved(hash, "wallet_sign") {
		t.Fatal("expected approval to be recorded")
	}
	if err := policy.Revoke(hash, "wallet_sign"); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	if policy.IsApproved(hash, "wallet_sign") {
		t.Fatal("expected revoked capability to be denied")
	}
	// Revoking an entry that was never approved is a no-op, not an error.
	if err := policy.Revoke(hash, "wallet_sign"); err != nil {
		t.Fatalf("Revoke of an already-revoked entry should be a no-op, got: %v", err)
	}
}

func TestCapabilityPolicyStoreListAndForModule(t *testing.T) {
	t.Parallel()

	policy, err := NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	if _, err := policy.Approve(CapabilityApproval{ModuleHash: "hash-a", Capability: "wallet_sign"}); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	if _, err := policy.Approve(CapabilityApproval{ModuleHash: "hash-a", Capability: "http"}); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	if _, err := policy.Approve(CapabilityApproval{ModuleHash: "hash-b", Capability: "ipfs"}); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	all := policy.List()
	if len(all) != 3 {
		t.Fatalf("List() returned %d entries, want 3", len(all))
	}

	forA := policy.ForModule("hash-a")
	if len(forA) != 2 {
		t.Fatalf("ForModule(hash-a) returned %d entries, want 2", len(forA))
	}
}

// A denied capability is an operator-visible fact, not just an error string
// handed back to whoever tried to mount the artifact — a flow refused at boot
// otherwise leaves one log line and then nothing.
func TestCapabilityDenialRaisesAndApprovalClearsTheAlert(t *testing.T) {
	registry := ops.NewRegistry()
	SetAlerts(registry)
	t.Cleanup(func() { SetAlerts(nil) })

	const pluginID = "celestrak-satcat-ingest"
	hash := "beefcafe"

	if err := checkCapabilityPolicy(nil, hash, pluginID, []string{"wallet_sign"}); err == nil {
		t.Fatal("expected a denial with no policy")
	}
	active := registry.Active()
	if len(active) != 1 {
		t.Fatalf("Active() = %+v, want one alert", active)
	}
	if active[0].Kind != ops.KindFlowCapabilityDenied || active[0].Subject != pluginID {
		t.Fatalf("alert = %s/%s, want %s/%s", active[0].Kind, active[0].Subject, ops.KindFlowCapabilityDenied, pluginID)
	}
	if active[0].Severity != ops.SeverityError {
		t.Errorf("Severity = %q, want %q: the artifact did not load", active[0].Severity, ops.SeverityError)
	}

	policy, err := NewCapabilityPolicyStore("") // in-memory
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore: %v", err)
	}
	if _, err := policy.Approve(CapabilityApproval{
		ModuleHash: hash,
		PluginID:   pluginID,
		Capability: "wallet_sign",
		ApprovedBy: "operator",
	}); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := checkCapabilityPolicy(policy, hash, pluginID, []string{"wallet_sign"}); err != nil {
		t.Fatalf("approved capability still denied: %v", err)
	}
	if got := registry.Active(); len(got) != 0 {
		t.Fatalf("Active() = %+v, want empty once the plugin provisions", got)
	}
}
