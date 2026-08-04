package caps

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/credstore"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// Operator-defined lanes (owner 2026-08-04) must be gated EXACTLY like the
// well-known ones: same per-lane capability, same isolation, no shortcut for a
// lane the node happens not to have shipped a verifier for.

const (
	laneAID     = "acme-weather"
	laneASecret = "ACME-WEATHER-CANARY-3d70"
	laneBID     = "zephyr_billing"
	laneBSecret = "ZEPHYR-BILLING-CANARY-9a44"
)

// newTwoLaneHandler seeds two operator-defined lanes (plus a well-known one)
// and returns a handler whose bridge is granted exactly the given capabilities.
func newTwoLaneHandler(t *testing.T, grants ...string) modulert.CapHandler {
	t.Helper()
	store, err := credstore.NewStore(t.TempDir(), "test-root-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, seed := range []struct{ id, user, secret string }{
		{laneAID, "ops@acme.example", laneASecret},
		{laneBID, "ops@zephyr.example", laneBSecret},
		{credstore.IDSpaceTrack, stUser, stSecret},
	} {
		if err := store.Put(seed.id, seed.user, seed.secret); err != nil {
			t.Fatalf("Put(%q): %v", seed.id, err)
		}
	}
	return NewSecretsCapFactory(store)(nil, modulert.NewHostBridge(nil, grants))
}

func laneResult(t *testing.T, h modulert.CapHandler, op, id string) (map[string]any, string) {
	t.Helper()
	raw, err := h(op, []byte(`{"id":"`+id+`"}`))
	if err != nil {
		t.Fatalf("%s(%q) returned a Go error: %v", op, id, err)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode %s(%q): %v", op, id, err)
	}
	return resp, string(raw)
}

// HARD REQUIREMENT: a module approved for one OPERATOR-DEFINED lane cannot read
// another one — nor a well-known lane. This is the spacetrack/edc_cpf isolation
// test, re-run on lanes the node has never heard of.
func TestOperatorLaneGrantIsIsolated(t *testing.T) {
	h := newTwoLaneHandler(t, CapabilityForID(laneAID))

	// Its own lane: allowed, and it really does get the credential (otherwise
	// the denials below prove nothing).
	resp, raw := laneResult(t, h, "secrets.get", laneAID)
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("the approved operator-defined lane was denied: %v", resp)
	}
	result, _ := resp["result"].(map[string]any)
	if result["secret"] != laneASecret {
		t.Fatalf("the approved module did not receive lane A's secret: %s", raw)
	}

	// Every other lane — operator-defined or well-known — is denied, and the
	// response bytes never carry the other credential.
	for _, other := range []struct{ id, canary string }{
		{laneBID, laneBSecret},
		{credstore.IDSpaceTrack, stSecret},
	} {
		for _, op := range []string{"secrets.get", "secrets.status"} {
			resp, raw := laneResult(t, h, op, other.id)
			if ok, _ := resp["ok"].(bool); ok {
				t.Fatalf("SECURITY: a %s grant also unlocked %s via %s", CapabilityForID(laneAID), other.id, op)
			}
			if strings.Contains(raw, other.canary) {
				t.Fatalf("SECURITY: %s leaked the %s credential", op, other.id)
			}
		}
	}
}

// The reverse direction: a well-known grant conveys nothing about an
// operator-defined lane. Well-known lanes have no privileged position.
func TestWellKnownGrantDoesNotUnlockOperatorLane(t *testing.T) {
	h := newTwoLaneHandler(t, CapabilityForID(credstore.IDSpaceTrack))

	resp, raw := laneResult(t, h, "secrets.get", laneAID)
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatal("SECURITY: a secrets:spacetrack grant unlocked an operator-defined lane")
	}
	if strings.Contains(raw, laneASecret) {
		t.Fatal("SECURITY: the operator-defined lane's credential leaked to a spacetrack-only module")
	}
}

// A module with no secrets grant at all cannot touch an operator-defined lane,
// and cannot even probe whether it is configured.
func TestOperatorLaneDeniedWithoutGrant(t *testing.T) {
	h := newTwoLaneHandler(t, "http", "storage_query", "wallet_sign")

	for _, op := range []string{"secrets.get", "secrets.status"} {
		resp, raw := laneResult(t, h, op, laneAID)
		if ok, _ := resp["ok"].(bool); ok {
			t.Fatalf("SECURITY: %s on an operator-defined lane succeeded without a grant", op)
		}
		if strings.Contains(raw, laneASecret) {
			t.Fatalf("SECURITY: %s leaked an operator-defined lane's credential", op)
		}
	}
}

// CapabilityForID must produce the one canonical name for an operator-defined
// lane — the same string the operator records in capability_policy.json.
func TestCapabilityForOperatorLane(t *testing.T) {
	if got := CapabilityForID(laneAID); got != "secrets:"+laneAID {
		t.Errorf("CapabilityForID(%q) = %q", laneAID, got)
	}
	if got := CapabilityForID("  " + laneAID + "  "); got != "secrets:"+laneAID {
		t.Errorf("CapabilityForID must trim: got %q", got)
	}
}
