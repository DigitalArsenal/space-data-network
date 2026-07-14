package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/credstore"
)

const (
	credTestUser   = "operator@example.com"
	credTestSecret = "PLAINTEXT-CANARY-do-not-leak-9f3a"
)

func newCredStore(t *testing.T) *credstore.Store {
	t.Helper()
	st, err := credstore.NewStore(t.TempDir(), "test-root-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

// authOn builds a handler with authentication enabled. The auth.Handler has no
// sessions, so every request is unauthenticated.
func authOn(t *testing.T, store *credstore.Store) *CredentialsHandler {
	t.Helper()
	ah := auth.NewHandler(nil, nil, time.Hour, "", "")
	return NewCredentialsHandler(store, ah, true, nil)
}

type stubVerifier struct {
	err    error
	calls  int
	gotUsr string
	gotSec string
}

func (s *stubVerifier) Verify(_ context.Context, username, secret string) error {
	s.calls++
	s.gotUsr, s.gotSec = username, secret
	return s.err
}

// HARD REQUIREMENT: an unauthenticated request to a credential route is rejected.
func TestCredentialRoutesRejectUnauthenticated(t *testing.T) {
	store := newCredStore(t)
	if err := store.Put(credstore.IDSpaceTrack, credTestUser, credTestSecret); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := authOn(t, store)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/admin/credentials", ""},
		{http.MethodGet, "/api/v1/admin/credentials/spacetrack", ""},
		{http.MethodPut, "/api/v1/admin/credentials/spacetrack", `{"username":"x@y.z","secret":"s"}`},
		{http.MethodDelete, "/api/v1/admin/credentials/spacetrack", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", tc.method, tc.path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), credTestSecret) {
			t.Fatalf("SECURITY: %s %s leaked the secret to an unauthenticated caller", tc.method, tc.path)
		}
	}

	// The rejected DELETE must not have cleared anything.
	if st, err := store.Status(credstore.IDSpaceTrack); err != nil || !st.Configured {
		t.Error("an unauthenticated DELETE mutated the store")
	}
}

// HARD REQUIREMENT: with authentication disabled the routes must REFUSE, not
// fall open. On this deployment nginx forwards every /api/** path to the daemon,
// so a fall-open here is an internet-exposed credential-entry endpoint.
func TestCredentialRoutesFailClosedWhenAuthDisabled(t *testing.T) {
	store := newCredStore(t)
	ah := auth.NewHandler(nil, nil, time.Hour, "", "")

	for name, h := range map[string]*CredentialsHandler{
		"require_auth=false":    NewCredentialsHandler(store, ah, false, nil),
		"nil auth handler":      NewCredentialsHandler(store, nil, true, nil),
		"auth off and no store": NewCredentialsHandler(nil, nil, false, nil),
	} {
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)

		for _, tc := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/admin/credentials", ""},
			{http.MethodGet, "/api/v1/admin/credentials/spacetrack", ""},
			{http.MethodPut, "/api/v1/admin/credentials/spacetrack", `{"username":"a@b.c","secret":"injected"}`},
			{http.MethodDelete, "/api/v1/admin/credentials/spacetrack", ""},
		} {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("%s: %s %s: status = %d, want 503 (fail closed)", name, tc.method, tc.path, rec.Code)
			}
		}
	}

	// Nothing may have been written through the refusing routes.
	st, err := store.Status(credstore.IDSpaceTrack)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Configured {
		t.Fatal("SECURITY: a credential was written while authentication was disabled")
	}
}

// HARD REQUIREMENT: the plaintext must never be serialized into an API response.
// Every route is exercised against a store holding a known secret, and every
// response body is grepped for it.
func TestPlaintextNeverAppearsInAnyAPIResponse(t *testing.T) {
	store := newCredStore(t)
	verifier := &stubVerifier{}
	// Auth is bypassed here deliberately: we call the inner handlers directly so
	// the RESPONSE BODIES themselves are under test, not the gate.
	h := NewCredentialsHandler(store, nil, true, map[string]Verifier{
		credstore.IDSpaceTrack: verifier,
	})

	type call struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
		path    string
		body    string
	}
	calls := []call{
		{"PUT store", h.handleByID, http.MethodPut, "/api/v1/admin/credentials/spacetrack",
			`{"username":"` + credTestUser + `","secret":"` + credTestSecret + `"}`},
		{"PUT verify", h.handleByID, http.MethodPut, "/api/v1/admin/credentials/spacetrack",
			`{"username":"` + credTestUser + `","secret":"` + credTestSecret + `","verify":true}`},
		{"GET one", h.handleByID, http.MethodGet, "/api/v1/admin/credentials/spacetrack", ""},
		{"GET all", h.handleCollection, http.MethodGet, "/api/v1/admin/credentials", ""},
		{"DELETE", h.handleByID, http.MethodDelete, "/api/v1/admin/credentials/spacetrack", ""},
	}

	for _, c := range calls {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		c.handler(rec, req)

		body := rec.Body.String()
		if strings.Contains(body, credTestSecret) {
			t.Fatalf("SECURITY: %s response leaked the plaintext secret: %s", c.name, body)
		}
		// The unmasked username is not secret material, but it is not the
		// contract either: only the masked form may appear.
		if strings.Contains(body, credTestUser) {
			t.Fatalf("SECURITY: %s response leaked the unmasked username: %s", c.name, body)
		}
	}

	// Sanity: the secret really was stored, so the greps above were meaningful.
	if verifier.calls != 1 {
		t.Errorf("verifier called %d times, want 1", verifier.calls)
	}
	if verifier.gotSec != credTestSecret {
		t.Error("the verifier did not receive the submitted secret — the test greps proved nothing")
	}
}

// The write path is genuinely write-only: PUT stores, GET reports status only.
func TestPutThenStatusIsWriteOnly(t *testing.T) {
	store := newCredStore(t)
	h := NewCredentialsHandler(store, nil, true, nil)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/credentials/spacetrack",
		strings.NewReader(`{"username":"`+credTestUser+`","secret":"`+credTestSecret+`"}`))
	rec := httptest.NewRecorder()
	h.handleByID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", rec.Code, rec.Body.String())
	}

	var wrote credentialWriteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &wrote); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !wrote.Status.Configured {
		t.Error("want configured after PUT")
	}
	if wrote.Verification != "unverified" {
		t.Errorf("verification = %q, want unverified (no verifier registered)", wrote.Verification)
	}
	if wrote.Status.UsernameMasked != "o***@example.com" {
		t.Errorf("username_masked = %q", wrote.Status.UsernameMasked)
	}

	// The credential is really there — reachable ONLY host-side.
	cred, err := store.Reveal(credstore.IDSpaceTrack)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if cred.Secret.Reveal() != credTestSecret {
		t.Error("the stored secret does not match what was PUT")
	}

	// GET reports status, never the secret.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/credentials/spacetrack", nil)
	rec = httptest.NewRecorder()
	h.handleByID(rec, req)

	var got credstore.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Configured {
		t.Error("want configured")
	}
	// Decode into a loose map and assert no field carries a secret-ish value.
	var loose map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &loose); err != nil {
		t.Fatalf("decode loose: %v", err)
	}
	for k, v := range loose {
		if s, ok := v.(string); ok && s == credTestSecret {
			t.Fatalf("SECURITY: response field %q carried the plaintext", k)
		}
	}
	if _, present := loose["secret"]; present {
		t.Fatal("SECURITY: the status response has a 'secret' field")
	}
	if _, present := loose["password"]; present {
		t.Fatal("SECURITY: the status response has a 'password' field")
	}
}

// A failed probe must still store the credential, reported as unverified/failed,
// and must not echo the credential in the error.
func TestVerificationFailureStoresUnverified(t *testing.T) {
	store := newCredStore(t)
	verifier := &stubVerifier{err: errors.New("Space-Track rejected the credential")}
	h := NewCredentialsHandler(store, nil, true, map[string]Verifier{credstore.IDSpaceTrack: verifier})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/credentials/spacetrack",
		strings.NewReader(`{"username":"`+credTestUser+`","secret":"`+credTestSecret+`","verify":true}`))
	rec := httptest.NewRecorder()
	h.handleByID(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp credentialWriteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Verification != "failed" {
		t.Errorf("verification = %q, want failed", resp.Verification)
	}
	if !resp.Status.Configured {
		t.Error("a failed probe must still store the credential (saved but unverified)")
	}
	if resp.Status.VerifiedAt != nil {
		t.Error("a failed probe must not mark the credential verified")
	}
	if strings.Contains(rec.Body.String(), credTestSecret) {
		t.Fatal("SECURITY: the verification error leaked the secret")
	}
}

func TestPutValidationRejectsEmptyFields(t *testing.T) {
	h := NewCredentialsHandler(newCredStore(t), nil, true, nil)

	for name, body := range map[string]string{
		"no username": `{"secret":"s"}`,
		"no secret":   `{"username":"a@b.c"}`,
		"empty body":  `{}`,
		"bad json":    `{`,
	} {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/credentials/spacetrack", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.handleByID(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}

// A malformed-JSON error must never quote the request body back to the caller —
// the body contains a password.
func TestDecodeErrorDoesNotEchoBody(t *testing.T) {
	h := NewCredentialsHandler(newCredStore(t), nil, true, nil)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/credentials/spacetrack",
		strings.NewReader(`{"username":"u","secret":"`+credTestSecret+`"`)) // truncated JSON
	rec := httptest.NewRecorder()
	h.handleByID(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), credTestSecret) {
		t.Fatal("SECURITY: the JSON decode error echoed the request body, leaking the secret")
	}
}

// STRUCTURAL GUARD: no handler in credentials.go may call Reveal(). This is the
// invariant that keeps the API write-only; a future edit that adds a
// "show credential" route trips this test.
func TestNoHandlerRevealsPlaintext(t *testing.T) {
	src, err := os.ReadFile("credentials.go")
	if err != nil {
		t.Fatalf("read credentials.go: %v", err)
	}
	if strings.Contains(string(src), ".Reveal(") {
		t.Fatal("SECURITY: credentials.go calls Reveal() — the API must never obtain plaintext")
	}
	if strings.Contains(string(src), "store.Reveal") {
		t.Fatal("SECURITY: credentials.go reads the plaintext credential")
	}
}
