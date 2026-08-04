package credstore

import (
	"errors"
	"strings"
	"testing"
)

// The generic-lane contract (owner 2026-08-04): the keystore is a lane store,
// not a Space-Track store. An operator may define a lane for ANY service, and
// that lane must behave exactly like a well-known one.

const (
	laneUser   = "ops@acme.example"
	laneSecret = "ACME-PLAINTEXT-CANARY-7c19"
)

// An operator-defined lane round-trips through the same envelope as a
// well-known one: sealed on disk, revealed only host-side, reported by Status.
func TestArbitraryLaneRoundTrip(t *testing.T) {
	st, _ := newTestStore(t)

	const lane = "acme-weather"
	if err := st.Put(lane, laneUser, laneSecret); err != nil {
		t.Fatalf("Put(%q): %v", lane, err)
	}

	got, err := st.Reveal(lane)
	if err != nil {
		t.Fatalf("Reveal(%q): %v", lane, err)
	}
	if got.Username != laneUser {
		t.Errorf("username = %q, want %q", got.Username, laneUser)
	}
	if got.Secret.Reveal() != laneSecret {
		t.Error("the operator-defined lane did not round-trip its secret")
	}

	// Status is the API-safe shape and carries no secret for this lane either.
	status, err := st.Status(lane)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Configured {
		t.Error("want configured")
	}
	if status.VerifiedAt != nil {
		t.Error("a freshly stored lane must be unverified — there is no verifier for it")
	}
	if status.UsernameMasked == laneUser {
		t.Error("the username must be masked in Status")
	}

	// Replacing and clearing work identically.
	if err := st.Put(lane, laneUser, laneSecret+"-rotated"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := st.Clear(lane); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := st.Reveal(lane); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("after Clear, Reveal err = %v, want ErrNotConfigured", err)
	}
}

// The lane-id rule as implemented: ^[a-z0-9_-]{2,64}$, reserved prefixes out.
func TestValidateLaneID(t *testing.T) {
	good := []string{
		"spacetrack", "edc_cpf", "myintelsat", // the well-known ids must pass
		"acme", "acme-weather", "acme_weather", "a1", "x9",
		strings.Repeat("a", MaxLaneIDLen),
	}
	for _, id := range good {
		if err := ValidateLaneID(id); err != nil {
			t.Errorf("ValidateLaneID(%q) = %v, want nil", id, err)
		}
	}

	bad := map[string]string{
		"empty":              "",
		"blank":              "   ",
		"one char":           "a",
		"too long":           strings.Repeat("a", MaxLaneIDLen+1),
		"uppercase":          "SpaceTrack",
		"space":              "acme weather",
		"dot":                "acme.weather",
		"slash":              "acme/weather",
		"path traversal":     "../etc",
		"colon":              "acme:lane",
		"unicode":            "acmé",
		"reserved sdn_":      "sdn_internal",
		"reserved sdn_ only": "sdn_",
	}
	for name, id := range bad {
		err := ValidateLaneID(id)
		if err == nil {
			t.Errorf("%s: ValidateLaneID(%q) = nil, want rejection", name, id)
			continue
		}
		if !errors.Is(err, ErrInvalidLaneID) {
			t.Errorf("%s: error does not wrap ErrInvalidLaneID: %v", name, err)
		}
	}

	// The message states the RULE and must not echo the submitted id back: the
	// id arrives from a URL path and the API reflects this text to the caller.
	// (Checked with distinctive ids, so an incidental substring of the rule
	// text — "a", "sdn_" — cannot masquerade as an echo.)
	for _, id := range []string{"AcmeWeatherCanary", "acme.weather.canary", "sdn_reserved_canary"} {
		err := ValidateLaneID(id)
		if err == nil {
			t.Fatalf("ValidateLaneID(%q) = nil, want rejection", id)
		}
		if strings.Contains(err.Error(), id) {
			t.Errorf("validation error echoed the submitted id: %v", err)
		}
	}
}

// The store enforces the rule too — no caller anywhere can mint a lane whose id
// would not round-trip through a capability name and an operator approval.
func TestPutRejectsInvalidLaneID(t *testing.T) {
	st, _ := newTestStore(t)

	for _, id := range []string{"", "a", "SpaceTrack", "acme/weather", "sdn_reserved", strings.Repeat("z", MaxLaneIDLen+1)} {
		err := st.Put(id, laneUser, laneSecret)
		if err == nil {
			t.Errorf("Put(%q) succeeded; the store must enforce the lane-id rule", id)
			continue
		}
		if !errors.Is(err, ErrInvalidLaneID) {
			t.Errorf("Put(%q) error does not wrap ErrInvalidLaneID: %v", id, err)
		}
	}
}

// Lanes() is the enumeration every surface uses: well-known ∪ stored, sorted.
func TestLanesUnionWellKnownAndStored(t *testing.T) {
	st, _ := newTestStore(t)

	// Nothing stored: the well-known lanes are still enumerated, so an operator
	// can see the slots that exist to be filled.
	lanes, err := st.Lanes()
	if err != nil {
		t.Fatalf("Lanes: %v", err)
	}
	if len(lanes) != len(AllIDs()) {
		t.Fatalf("empty store lanes = %v, want the well-known set %v", lanes, AllIDs())
	}

	if err := st.Put("acme-weather", laneUser, laneSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := st.Put(IDSpaceTrack, testUser, testSecret); err != nil {
		t.Fatalf("Put spacetrack: %v", err)
	}

	lanes, err = st.Lanes()
	if err != nil {
		t.Fatalf("Lanes: %v", err)
	}
	want := []string{"acme-weather", IDEDCCPF, IDMyIntelsat, IDSpaceTrack} // sorted
	if len(lanes) != len(want) {
		t.Fatalf("lanes = %v, want %v", lanes, want)
	}
	for i, id := range want {
		if lanes[i] != id {
			t.Fatalf("lanes = %v, want %v (sorted union)", lanes, want)
		}
	}
	// A stored well-known lane must not be duplicated by the union.
	seen := map[string]int{}
	for _, id := range lanes {
		seen[id]++
	}
	if seen[IDSpaceTrack] != 1 {
		t.Errorf("spacetrack appears %d times; the union must deduplicate", seen[IDSpaceTrack])
	}
}

// ListLanes reports a status per lane — including well-known lanes that hold
// nothing — and never carries secret material.
func TestListLanesIncludesUnconfiguredWellKnownAndOperatorLanes(t *testing.T) {
	st, _ := newTestStore(t)
	if err := st.Put("acme-weather", laneUser, laneSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}

	list, err := st.ListLanes()
	if err != nil {
		t.Fatalf("ListLanes: %v", err)
	}
	byID := map[string]Status{}
	for _, s := range list {
		byID[s.ID] = s
	}

	acme, ok := byID["acme-weather"]
	if !ok {
		t.Fatal("the operator-defined lane is missing from ListLanes")
	}
	if !acme.Configured {
		t.Error("the operator-defined lane must report configured")
	}
	if acme.VerifiedAt != nil {
		t.Error("a lane with no verifier must report VerifiedAt nil — stored, not verified")
	}

	for _, id := range AllIDs() {
		s, ok := byID[id]
		if !ok {
			t.Errorf("well-known lane %q missing from ListLanes", id)
			continue
		}
		if s.Configured {
			t.Errorf("well-known lane %q reports configured with nothing stored", id)
		}
	}

	// Contrast List(), which reports only lanes that actually hold a credential.
	configured, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(configured) != 1 || configured[0].ID != "acme-weather" {
		t.Errorf("List() = %v, want only the one configured lane", configured)
	}
}

// IsWellKnown separates the lanes the node ships a verifier for from the rest.
func TestIsWellKnown(t *testing.T) {
	for _, id := range AllIDs() {
		if !IsWellKnown(id) {
			t.Errorf("IsWellKnown(%q) = false", id)
		}
	}
	for _, id := range []string{"acme-weather", "", "spacetrack2", "SPACETRACK"} {
		if IsWellKnown(id) {
			t.Errorf("IsWellKnown(%q) = true, want false", id)
		}
	}
}
