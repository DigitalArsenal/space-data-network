package license

// SEC-030 regressions, from the audit repro: a signed module publish or
// entitlement request is applied once, on the node it names, while fresh; a
// publish never silently downgrades a module.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const sec030NodeA = "16Uiu2HAmProviderNodeA"
const sec030NodeB = "16Uiu2HAmProviderNodeB"

func sec030Publish(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, audience, version, nonce string, issued time.Time, rollback bool) []byte {
	t.Helper()
	req := ModulePublishRequest{
		Type: ModulePublishRequestType, Audience: audience, IssuedAtMs: issued.UnixMilli(), Nonce: nonce, Rollback: rollback,
		Modules: []ModulePublishEntry{{
			ID: "paid-mod", Version: version, EncryptedBundle: []byte(version),
			KeyMaterial:  []byte("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"),
			AllowedXpubs: []string{"xpub-base"},
		}},
	}
	if err := SignModulePublishRequest(&req, "xpub-admin", pub, priv); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func sec030Entitle(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, audience string, entitle, revoke []string, nonce string, issued time.Time) []byte {
	t.Helper()
	req := ModulePublishRequest{
		Type: ModuleEntitlementRequestType, Audience: audience, IssuedAtMs: issued.UnixMilli(), Nonce: nonce,
		Entitlement: &ModuleEntitlementChange{ModuleIDs: []string{"paid-mod"}, Entitle: entitle, Revoke: revoke},
	}
	if err := SignModulePublishRequest(&req, "xpub-admin", pub, priv); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestSEC030SignedRequestsCannotBeReplayed(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	auth := testModulePublishAuthorizer("xpub-admin", pub, true)
	dirA := t.TempDir()
	regA, err := LoadPluginRegistry(dirA)
	if err != nil {
		t.Fatal(err)
	}
	regB, err := LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.UnixMilli(1777392000000)
	guardA := ModulePublishGuard{Audience: sec030NodeA, Now: func() time.Time { return clock }}
	guardB := ModulePublishGuard{Audience: sec030NodeB, Now: func() time.Time { return clock }}

	apply := func(reg *PluginRegistry, guard ModulePublishGuard, wire []byte) ModulePublishResponse {
		return ApplyModulePublishJSON(bytes.NewReader(wire), reg, auth, guard)
	}
	allowed := func(reg *PluginRegistry) string {
		asset, ok := reg.Get("paid-mod")
		if !ok {
			return ""
		}
		return asset.Version + " " + strings.Join(asset.AllowedXpubs, ",")
	}
	mustOK := func(label string, resp ModulePublishResponse) {
		t.Helper()
		if !resp.OK {
			t.Fatalf("%s: %s", label, resp.Error)
		}
	}
	mustRefuse := func(label string, resp ModulePublishResponse, want string) {
		t.Helper()
		if resp.OK || !strings.Contains(resp.Error, want) {
			t.Fatalf("%s: ok=%v error=%q, want refusal containing %q", label, resp.OK, resp.Error, want)
		}
	}

	pubV1 := sec030Publish(t, pub, priv, sec030NodeA, "1.0.0", "n-v1", clock, false)
	pubV2 := sec030Publish(t, pub, priv, sec030NodeA, "2.0.0", "n-v2", clock.Add(time.Second), false)
	entX := sec030Entitle(t, pub, priv, sec030NodeA, []string{"xpub-X"}, nil, "n-entX", clock.Add(2*time.Second))
	revX := sec030Entitle(t, pub, priv, sec030NodeA, nil, []string{"xpub-X"}, "n-revX", clock.Add(3*time.Second))

	mustOK("publish v1", apply(regA, guardA, pubV1))
	mustOK("publish v2", apply(regA, guardA, pubV2))
	mustRefuse("replayed publish v1 (version rollback)", apply(regA, guardA, pubV1), "already applied")
	if got := allowed(regA); !strings.HasPrefix(got, "2.0.0 ") {
		t.Fatalf("after the replay the module is %q, want 2.0.0", got)
	}
	mustOK("entitle X", apply(regA, guardA, entX))
	mustOK("revoke X", apply(regA, guardA, revX))
	mustRefuse("replayed entitle X (revocation rollback)", apply(regA, guardA, entX), "already applied")
	if got := allowed(regA); strings.Contains(got, "xpub-X") {
		t.Fatalf("replay re-entitled the revoked account: %q", got)
	}

	// The same captured requests on another node: wrong audience.
	mustRefuse("publish v1 replayed on node B", apply(regB, guardB, pubV1), "not this node")
	mustRefuse("entitle X replayed on node B", apply(regB, guardB, entX), "not this node")
	if _, ok := regB.Get("paid-mod"); ok {
		t.Fatal("a request for node A changed node B")
	}

	// The nonce ledger survives a restart.
	regA2, err := LoadPluginRegistry(dirA)
	if err != nil {
		t.Fatal(err)
	}
	mustRefuse("replay after restart", apply(regA2, guardA, entX), "already applied")
}

func TestSEC030StaleAndFutureRequestsAreRefused(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	auth := testModulePublishAuthorizer("xpub-admin", pub, true)
	reg, err := LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.UnixMilli(1777392000000)
	guard := ModulePublishGuard{Audience: sec030NodeA, Now: func() time.Time { return clock }}

	// The audit repro signed with issued_at_ms=1 (1970).
	ancient := sec030Publish(t, pub, priv, sec030NodeA, "1.0.0", "n-1970", time.UnixMilli(1), false)
	if resp := ApplyModulePublishJSON(bytes.NewReader(ancient), reg, auth, guard); resp.OK || !strings.Contains(resp.Error, "outside") {
		t.Fatalf("1970 request: %+v", resp)
	}
	stale := sec030Publish(t, pub, priv, sec030NodeA, "1.0.0", "n-stale", clock.Add(-6*time.Minute), false)
	if resp := ApplyModulePublishJSON(bytes.NewReader(stale), reg, auth, guard); resp.OK {
		t.Fatal("a request issued six minutes ago was applied")
	}
	future := sec030Publish(t, pub, priv, sec030NodeA, "1.0.0", "n-future", clock.Add(6*time.Minute), false)
	if resp := ApplyModulePublishJSON(bytes.NewReader(future), reg, auth, guard); resp.OK {
		t.Fatal("a request issued six minutes ahead was applied")
	}
	// A node with no configured audience refuses everything.
	fresh := sec030Publish(t, pub, priv, sec030NodeA, "1.0.0", "n-fresh", clock, false)
	if resp := ApplyModulePublishJSON(bytes.NewReader(fresh), reg, auth, ModulePublishGuard{Now: guard.Now}); resp.OK {
		t.Fatal("a node without an audience applied a request")
	}
	// A request without an audience cannot even be signed.
	req := ModulePublishRequest{Type: ModulePublishRequestType, IssuedAtMs: clock.UnixMilli(), Nonce: "n-none",
		Modules: []ModulePublishEntry{{ID: "paid-mod", Version: "1.0.0", EncryptedBundle: []byte{1}, KeyMaterial: []byte("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")}}}
	if err := SignModulePublishRequest(&req, "xpub-admin", pub, priv); err == nil {
		t.Fatal("a request with no audience was signed")
	}
}

func TestSEC030DowngradeNeedsSignedRollback(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	auth := testModulePublishAuthorizer("xpub-admin", pub, true)
	reg, err := LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.UnixMilli(1777392000000)
	guard := ModulePublishGuard{Audience: sec030NodeA, Now: func() time.Time { return clock }}
	apply := func(wire []byte) ModulePublishResponse {
		return ApplyModulePublishJSON(bytes.NewReader(wire), reg, auth, guard)
	}
	if resp := apply(sec030Publish(t, pub, priv, sec030NodeA, "2.0.0", "n-a", clock, false)); !resp.OK {
		t.Fatal(resp.Error)
	}
	if resp := apply(sec030Publish(t, pub, priv, sec030NodeA, "1.9.9", "n-b", clock, false)); resp.OK || !strings.Contains(resp.Error, "rollback") {
		t.Fatalf("a fresh downgrade without rollback: %+v", resp)
	}
	if resp := apply(sec030Publish(t, pub, priv, sec030NodeA, "2.0.0", "n-c", clock, false)); !resp.OK {
		t.Fatalf("republishing the same version: %s", resp.Error)
	}
	if resp := apply(sec030Publish(t, pub, priv, sec030NodeA, "1.9.9", "n-d", clock, true)); !resp.OK {
		t.Fatalf("an explicit signed rollback: %s", resp.Error)
	}
	if asset, _ := reg.Get("paid-mod"); asset.Version != "1.9.9" {
		t.Fatalf("version = %q after the signed rollback", asset.Version)
	}
}

func TestCompareModuleVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b  string
		order int
		ok    bool
	}{
		{"1.0.0", "1.0.0", 0, true},
		{"1.10.0", "1.9.0", 1, true},
		{"2026.04.28", "2026.04.3", 1, true},
		{"v1.2.3", "1.2.3", 0, true},
		{"1.0.0-beta", "1.0.0", -1, true},
		{"1.0", "1.0.0", 0, true},
		{"1.0.0-vulnerable", "2.0.0-fixed", -1, true},
		{"nightly", "1.0.0", 0, false},
		{"nightly", "nightly", 0, true},
	} {
		order, ok := compareModuleVersions(tc.a, tc.b)
		if order != tc.order || ok != tc.ok {
			t.Fatalf("compareModuleVersions(%q, %q) = %d, %v; want %d, %v", tc.a, tc.b, order, ok, tc.order, tc.ok)
		}
	}
}
