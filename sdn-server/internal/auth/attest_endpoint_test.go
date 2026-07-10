package auth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// POST /api/auth/attest — the live wire for F2's VerifyAttestation.

func attestHandler(t *testing.T, store *UserStore) *Handler {
	t.Helper()
	return &Handler{
		userStore: store,
		rates:     make(map[string]rateEntry),
		clockSkew: 2 * time.Minute,
	}
}

func postAttest(t *testing.T, h *Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal attest body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/attest", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	h.handleAttest(rec, req)
	return rec
}

func signedAttestBody(t *testing.T, priv ed25519.PrivateKey, xpub, claim string) map[string]any {
	t.Helper()
	// Sign exactly what will be transmitted: issued_at travels as RFC3339
	// (second precision), so the signed preimage must use the truncated
	// value or verification of the parsed wire form fails.
	att, sig, err := SignAttestation(priv, Attestation{
		XPub:     xpub,
		Claim:    claim,
		IssuedAt: time.Now().UTC().Truncate(time.Second),
	})
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}
	return map[string]any{
		"xpub":          att.XPub,
		"claim":         att.Claim,
		"issued_at":     att.IssuedAt.Format(time.RFC3339),
		"nonce_hex":     hex.EncodeToString(att.Nonce),
		"signature_hex": hex.EncodeToString(sig),
	}
}

func TestHandleAttest_ValidSignatureReturnsUser(t *testing.T) {
	pub, priv := newTestAttestationKeypair(t)
	xpub := "xpub-attest-endpoint"
	store := newTestAttestationStore(t, xpub, "Browser Self", peers.Standard, pub)
	h := attestHandler(t, store)

	rec := postAttest(t, h, signedAttestBody(t, priv, xpub, "self"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["verified"] != true || resp["name"] != "Browser Self" || resp["trust_level"] != "standard" {
		t.Fatalf("unexpected response: %v", resp)
	}
}

func TestHandleAttest_FailuresAreOneOpaqueCode(t *testing.T) {
	pub, priv := newTestAttestationKeypair(t)
	_, otherPriv := newTestAttestationKeypair(t)
	xpub := "xpub-attest-opaque"
	store := newTestAttestationStore(t, xpub, "User", peers.Standard, pub)
	h := attestHandler(t, store)

	// Wrong key and unknown user must be INDISTINGUISHABLE on the wire
	// (both 401 attestation_failed) — no membership oracle.
	wrongKey := postAttest(t, h, signedAttestBody(t, otherPriv, xpub, "self"))
	unknownUser := postAttest(t, h, signedAttestBody(t, priv, "xpub-never-registered", "self"))

	for name, rec := range map[string]*httptest.ResponseRecorder{"wrong key": wrongKey, "unknown user": unknownUser} {
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401 (body %s)", name, rec.Code, rec.Body.String())
		}
		var resp errorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if resp.Code != "attestation_failed" {
			t.Fatalf("%s: code = %q, want attestation_failed", name, resp.Code)
		}
	}
	if wrongKey.Body.String() != unknownUser.Body.String() {
		t.Fatalf("failure bodies differ (oracle):\n%s\n%s", wrongKey.Body.String(), unknownUser.Body.String())
	}
}

func TestHandleAttest_StaleIssuedAtRejected(t *testing.T) {
	pub, priv := newTestAttestationKeypair(t)
	xpub := "xpub-attest-stale"
	store := newTestAttestationStore(t, xpub, "User", peers.Standard, pub)
	h := attestHandler(t, store)

	att, sig, err := SignAttestation(priv, Attestation{
		XPub:     xpub,
		Claim:    "self",
		IssuedAt: time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}
	rec := postAttest(t, h, map[string]any{
		"xpub":          att.XPub,
		"claim":         att.Claim,
		"issued_at":     att.IssuedAt.Format(time.RFC3339),
		"nonce_hex":     hex.EncodeToString(att.Nonce),
		"signature_hex": hex.EncodeToString(sig),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestHandleAttest_MalformedAndMethod(t *testing.T) {
	pub, _ := newTestAttestationKeypair(t)
	store := newTestAttestationStore(t, "xpub-attest-malformed", "User", peers.Standard, pub)
	h := attestHandler(t, store)

	// Missing fields → 400.
	rec := postAttest(t, h, map[string]any{"xpub": "x"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing fields: status = %d, want 400", rec.Code)
	}
	// GET → 405.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/attest", nil)
	getRec := httptest.NewRecorder()
	h.handleAttest(getRec, req)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status = %d, want 405", getRec.Code)
	}
}

func TestHandleAttest_RouteRegistered(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	if got := matchedPattern(t, mux, "/api/auth/attest"); got != "/api/auth/attest" {
		t.Fatalf("/api/auth/attest matched pattern %q", got)
	}
}
