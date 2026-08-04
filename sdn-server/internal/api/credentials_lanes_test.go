package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/credstore"
)

// Operator-defined credential lanes (owner 2026-08-04). The admin credentials
// API accepts an id for ANY service; the well-known three are only the lanes
// this node can PROBE.

const (
	laneTestUser   = "ops@acme.example"
	laneTestSecret = "LANE-PLAINTEXT-CANARY-2f8d"
)

// putLane drives a PUT through the inner handler (auth is exercised elsewhere).
func putLane(t *testing.T, h *CredentialsHandler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/credentials/"+id, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleByID(rec, req)
	return rec
}

// An operator can create a lane for a service the node has never heard of, and
// it behaves exactly like a well-known one — minus verification, which is
// reported honestly.
func TestArbitraryLaneStoresAndReportsUnverified(t *testing.T) {
	store := newCredStore(t)
	h := NewCredentialsHandler(store, nil, true, map[string]Verifier{
		credstore.IDSpaceTrack: &stubVerifier{},
	})

	const lane = "acme-weather"
	rec := putLane(t, h, lane, `{"username":"`+laneTestUser+`","secret":"`+laneTestSecret+`","verify":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT %s = %d: %s", lane, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), laneTestSecret) {
		t.Fatal("SECURITY: the PUT response leaked the secret")
	}

	var wrote credentialWriteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &wrote); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !wrote.Status.Configured {
		t.Error("the operator-defined lane was not stored")
	}
	// HONEST STATE: no verifier exists, so it is stored and unverified — never
	// reported as verified, and never refused.
	if wrote.Verification != "unverified" {
		t.Errorf("verification = %q, want unverified (no verifier for this lane)", wrote.Verification)
	}
	if wrote.Status.HasVerifier {
		t.Error("has_verifier must be false for a lane with no registered verifier")
	}
	if wrote.Status.WellKnown {
		t.Error("an operator-defined lane must not report well_known")
	}
	if wrote.Status.VerifiedAt != nil {
		t.Error("verified_at must stay unset for an unverifiable lane")
	}

	// The credential really is retrievable host-side — otherwise everything
	// above is vacuous.
	cred, err := store.Reveal(lane)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if cred.Secret.Reveal() != laneTestSecret {
		t.Error("the stored secret does not match what was PUT")
	}
}

// Lane-id validation at the API boundary: lowercase [a-z0-9_-]{2,64}, reserved
// prefixes refused. Rejections are 400 (a caller-fixable rule), not 500.
func TestLaneIDValidation(t *testing.T) {
	store := newCredStore(t)
	h := NewCredentialsHandler(store, nil, true, nil)

	good := []string{"spacetrack", "acme", "acme-weather", "acme_weather", "a1", strings.Repeat("a", credstore.MaxLaneIDLen)}
	for _, id := range good {
		rec := putLane(t, h, id, `{"username":"`+laneTestUser+`","secret":"`+laneTestSecret+`"}`)
		if rec.Code != http.StatusOK {
			t.Errorf("PUT %q = %d, want 200: %s", id, rec.Code, rec.Body.String())
		}
	}

	bad := map[string]string{
		"one char":      "a",
		"too long":      strings.Repeat("a", credstore.MaxLaneIDLen+1),
		"uppercase":     "SpaceTrack",
		"dot":           "acme.weather",
		"colon":         "acme:weather",
		"reserved sdn_": "sdn_internal",
		"percent":       "acme%2Fweather",
	}
	for name, id := range bad {
		rec := putLane(t, h, id, `{"username":"`+laneTestUser+`","secret":"`+laneTestSecret+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: PUT %q = %d, want 400", name, id, rec.Code)
		}
		if strings.Contains(rec.Body.String(), laneTestSecret) {
			t.Fatalf("SECURITY: %s rejection echoed the secret", name)
		}
		// Nothing may have been stored under a rejected id.
		if st, err := store.Status(id); err == nil && st.Configured {
			t.Errorf("%s: a rejected lane id was stored anyway", name)
		}
	}

	// GET and DELETE reject the same ids, so an unstorable id never looks like
	// a lane that merely happens to be empty.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/v1/admin/credentials/sdn_internal", nil)
		rec := httptest.NewRecorder()
		h.handleByID(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s on a reserved id = %d, want 400", method, rec.Code)
		}
	}
}

// The listing is well-known ∪ stored, and every row states whether the node can
// verify that lane. No row carries secret material.
func TestListingEnumeratesWellKnownAndOperatorLanes(t *testing.T) {
	store := newCredStore(t)
	h := NewCredentialsHandler(store, nil, true, map[string]Verifier{
		credstore.IDSpaceTrack: &stubVerifier{},
	})

	if rec := putLane(t, h, "acme-weather", `{"username":"`+laneTestUser+`","secret":"`+laneTestSecret+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed lane: %d %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/credentials", nil)
	rec := httptest.NewRecorder()
	h.handleCollection(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET collection = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, laneTestSecret) {
		t.Fatal("SECURITY: the listing leaked a secret")
	}
	if strings.Contains(body, laneTestUser) {
		t.Fatal("SECURITY: the listing leaked an unmasked username")
	}

	var payload struct {
		Credentials []laneStatus `json:"credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]laneStatus{}
	for _, row := range payload.Credentials {
		byID[row.ID] = row
	}

	// Every well-known lane appears even with nothing stored.
	for _, id := range credstore.AllIDs() {
		row, ok := byID[id]
		if !ok {
			t.Errorf("well-known lane %q missing from the listing", id)
			continue
		}
		if !row.WellKnown {
			t.Errorf("%q must report well_known", id)
		}
		if row.Configured {
			t.Errorf("%q reports configured with nothing stored", id)
		}
	}
	// The lane with a registered verifier says so; the ones without do not.
	if !byID[credstore.IDSpaceTrack].HasVerifier {
		t.Error("spacetrack must report has_verifier (a verifier is registered)")
	}
	if byID[credstore.IDEDCCPF].HasVerifier {
		t.Error("edc_cpf must report has_verifier=false on a node with no EDC verifier")
	}

	// The operator's own lane is enumerated alongside them.
	acme, ok := byID["acme-weather"]
	if !ok {
		t.Fatal("the operator-defined lane is missing from the listing")
	}
	if acme.WellKnown || acme.HasVerifier {
		t.Error("an operator-defined lane is neither well-known nor verifiable")
	}
	if !acme.Configured {
		t.Error("the operator-defined lane must report configured")
	}
	if acme.UsernameMasked != "o***@acme.example" {
		t.Errorf("username_masked = %q", acme.UsernameMasked)
	}
	if acme.VerifiedAt != nil {
		t.Error("verified_at must stay unset — the node never verified this lane")
	}

	// STRUCTURAL: no row may carry a secret-ish field, whatever the lane.
	var loose struct {
		Credentials []map[string]any `json:"credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &loose); err != nil {
		t.Fatalf("decode loose: %v", err)
	}
	for _, row := range loose.Credentials {
		for _, forbidden := range []string{"secret", "password", "username"} {
			if _, present := row[forbidden]; present {
				t.Fatalf("SECURITY: listing row %v has a %q field", row["id"], forbidden)
			}
		}
	}
}
