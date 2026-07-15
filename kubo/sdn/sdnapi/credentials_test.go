package sdnapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/credstore"
)

// The plaintext canary: if this string ever appears in ANY API response body,
// the write-only contract is broken.
const credCanarySecret = "PLAINTEXT-CANARY-credential-secret-9f3c"
const credCanaryUser = "operator@example.com"

// newCredsServer wires the real credential handler over a real temp-dir keystore
// with a permissive authorizer, and returns the store + a test server.
func newCredsServer(t *testing.T) (*credstore.Store, *httptest.Server) {
	t.Helper()
	store, err := credstore.NewStore(t.TempDir(), "test-root-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewCredentialsHandler(CredentialsDeps{
		Store:      func() CredentialStore { return store },
		Authorized: func(*http.Request) bool { return true },
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return store, srv
}

func do(t *testing.T, srv *httptest.Server, method, path, body string) (*http.Response, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// Round-trip: PUT a credential, then GET the list and see it (masked).
func TestCredentialsRoundTrip(t *testing.T) {
	store, srv := newCredsServer(t)

	resp, body := do(t, srv, http.MethodPut, "/sdn/v1/admin/credentials/spacetrack",
		`{"username":"`+credCanaryUser+`","secret":"`+credCanarySecret+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", resp.StatusCode, body)
	}
	// The PUT response is the masked status only.
	if !strings.Contains(body, "o***@example.com") {
		t.Errorf("PUT response missing masked username: %s", body)
	}

	// The store actually holds the real secret (host side), proving the round trip.
	cred, err := store.Reveal("spacetrack")
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if cred.Secret.Reveal() != credCanarySecret || cred.Username != credCanaryUser {
		t.Fatal("credential did not round-trip into the store")
	}

	// GET list shows the configured, masked lane.
	resp, body = do(t, srv, http.MethodGet, "/sdn/v1/admin/credentials", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, `"id":"spacetrack"`) || !strings.Contains(body, `"configured":true`) {
		t.Errorf("GET list missing the configured lane: %s", body)
	}
}

// HARD REQUIREMENT: plaintext is NEVER returned by ANY route. Serialize every
// response and grep for the canary.
func TestCredentialsNeverReturnPlaintext(t *testing.T) {
	_, srv := newCredsServer(t)

	// PUT (the one route that INGESTS plaintext): its response must not echo it.
	_, putBody := do(t, srv, http.MethodPut, "/sdn/v1/admin/credentials/spacetrack",
		`{"username":"`+credCanaryUser+`","secret":"`+credCanarySecret+`"}`)
	if strings.Contains(putBody, credCanarySecret) {
		t.Fatalf("SECURITY: PUT response leaked the plaintext secret: %s", putBody)
	}

	// GET list.
	_, listBody := do(t, srv, http.MethodGet, "/sdn/v1/admin/credentials", "")
	if strings.Contains(listBody, credCanarySecret) {
		t.Fatalf("SECURITY: GET list leaked the plaintext secret: %s", listBody)
	}

	// DELETE.
	_, delBody := do(t, srv, http.MethodDelete, "/sdn/v1/admin/credentials/spacetrack", "")
	if strings.Contains(delBody, credCanarySecret) {
		t.Fatalf("SECURITY: DELETE response leaked the plaintext secret: %s", delBody)
	}

	// And a fresh GET after delete.
	_, listBody2 := do(t, srv, http.MethodGet, "/sdn/v1/admin/credentials", "")
	if strings.Contains(listBody2, credCanarySecret) {
		t.Fatalf("SECURITY: post-delete GET leaked the plaintext secret: %s", listBody2)
	}
}

// DELETE clears the lane.
func TestCredentialsDelete(t *testing.T) {
	store, srv := newCredsServer(t)
	if err := store.Put("spacetrack", credCanaryUser, credCanarySecret); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, _ := do(t, srv, http.MethodDelete, "/sdn/v1/admin/credentials/spacetrack", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d", resp.StatusCode)
	}
	if _, err := store.Reveal("spacetrack"); err == nil {
		t.Fatal("credential still present after DELETE")
	}
}

// FAIL CLOSED: a nil Authorized (auth misconfigured) refuses every route.
func TestCredentialsAuthMisconfiguredRefuses(t *testing.T) {
	store, err := credstore.NewStore(t.TempDir(), "test-root-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewCredentialsHandler(CredentialsDeps{
		Store:      func() CredentialStore { return store },
		Authorized: nil, // misconfigured
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/sdn/v1/admin/credentials", ""},
		{http.MethodPut, "/sdn/v1/admin/credentials/spacetrack", `{"username":"u","secret":"s"}`},
		{http.MethodDelete, "/sdn/v1/admin/credentials/spacetrack", ""},
	} {
		resp, _ := do(t, srv, tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("SECURITY: %s %s returned %d with auth unconfigured — must be 403 (fail closed)", tc.method, tc.path, resp.StatusCode)
		}
	}
	// And nothing was written.
	if _, err := store.Reveal("spacetrack"); err == nil {
		t.Fatal("SECURITY: a PUT succeeded despite auth being unconfigured")
	}
}

// FAIL CLOSED: an unauthorized request (Authorized returns false) is refused.
func TestCredentialsUnauthorizedRefused(t *testing.T) {
	store, err := credstore.NewStore(t.TempDir(), "test-root-key-password")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewCredentialsHandler(CredentialsDeps{
		Store:      func() CredentialStore { return store },
		Authorized: func(*http.Request) bool { return false }, // deny all
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, _ := do(t, srv, http.MethodPut, "/sdn/v1/admin/credentials/spacetrack",
		`{"username":"`+credCanaryUser+`","secret":"`+credCanarySecret+`"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthorized PUT status = %d, want 403", resp.StatusCode)
	}
	if _, err := store.Reveal("spacetrack"); err == nil {
		t.Fatal("SECURITY: an unauthorized PUT wrote a credential")
	}
}

// A nil store fails closed with 503, not a panic or a false success.
func TestCredentialsNilStore(t *testing.T) {
	h := NewCredentialsHandler(CredentialsDeps{
		Store:      func() CredentialStore { return nil },
		Authorized: func(*http.Request) bool { return true },
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, _ := do(t, srv, http.MethodGet, "/sdn/v1/admin/credentials", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("nil-store GET status = %d, want 503", resp.StatusCode)
	}
	resp, _ = do(t, srv, http.MethodPut, "/sdn/v1/admin/credentials/spacetrack", `{"username":"u","secret":"s"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("nil-store PUT status = %d, want 503", resp.StatusCode)
	}
}

// PUT validation: a missing username or secret is a 400 and writes nothing.
func TestCredentialsPutValidation(t *testing.T) {
	store, srv := newCredsServer(t)

	for _, body := range []string{
		`{"username":"","secret":"s"}`,
		`{"username":"u","secret":""}`,
		`{`,
	} {
		resp, _ := do(t, srv, http.MethodPut, "/sdn/v1/admin/credentials/spacetrack", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT %q status = %d, want 400", body, resp.StatusCode)
		}
	}
	if _, err := store.Reveal("spacetrack"); err == nil {
		t.Fatal("an invalid PUT wrote a credential")
	}
}
