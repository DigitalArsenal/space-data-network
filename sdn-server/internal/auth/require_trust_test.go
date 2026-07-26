package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// newRequireTrustHandler builds a Handler backed by a real session store, plus
// a factory for session cookies at any trust level. The user store is not
// needed: RequireTrust reads the session only.
func newRequireTrustHandler(t *testing.T) (*Handler, func(peers.TrustLevel) *http.Cookie) {
	t.Helper()

	db, closer, err := flatsqldrv.OpenStandalone(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })

	sessions, err := NewSessionStore(db)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	handler := NewHandler(nil, sessions, time.Hour, "", "")
	return handler, func(trust peers.TrustLevel) *http.Cookie {
		t.Helper()
		token, err := sessions.CreateSession("xpub-require-trust", trust, "127.0.0.1", "test-agent", time.Hour)
		if err != nil {
			t.Fatalf("CreateSession(%s): %v", trust, err)
		}
		return &http.Cookie{Name: "sdn_wallet_session", Value: token}
	}
}

func requireTrustErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Code
}

// TestRequireTrustRejectsAnonymousAndUnderTrustedSessions locks the two failure
// shapes: no session is 401 unauthorized, a session below the minimum is 403
// forbidden — the SAME codes RequireAuth writes for API paths, so a client
// cannot tell a method-granular gate from a route-level one.
func TestRequireTrustRejectsAnonymousAndUnderTrustedSessions(t *testing.T) {
	t.Parallel()

	handler, cookieFor := newRequireTrustHandler(t)

	t.Run("no cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/api/node/epm", nil)
		rec := httptest.NewRecorder()
		if handler.RequireTrust(rec, req, peers.Admin) {
			t.Fatal("RequireTrust admitted an anonymous request")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if code := requireTrustErrorCode(t, rec); code != "unauthorized" {
			t.Fatalf("error code = %q, want %q", code, "unauthorized")
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/api/node/epm", nil)
		req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: "not-a-real-token"})
		rec := httptest.NewRecorder()
		if handler.RequireTrust(rec, req, peers.Admin) {
			t.Fatal("RequireTrust admitted a forged session token")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	for _, trust := range []peers.TrustLevel{peers.Unknown, peers.Marginal, peers.Standard, peers.Trusted} {
		trust := trust
		t.Run("below admin/"+trust.String(), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/node/epm", nil)
			req.AddCookie(cookieFor(trust))
			rec := httptest.NewRecorder()
			if handler.RequireTrust(rec, req, peers.Admin) {
				t.Fatalf("RequireTrust admitted a %s session at the admin gate", trust)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
			}
			if code := requireTrustErrorCode(t, rec); code != "forbidden" {
				t.Fatalf("error code = %q, want %q", code, "forbidden")
			}
		})
	}
}

// TestRequireTrustAdmitsAdminAndAboveWithoutTouchingTheResponse locks that an
// admitted request is untouched: no status, no body, and — critically — no
// Set-Cookie. RequireTrust composes UNDERNEATH the top-level auth wall, which
// may already have rotated the session; a second rotation here would emit a
// conflicting cookie.
func TestRequireTrustAdmitsAdminAndAboveWithoutTouchingTheResponse(t *testing.T) {
	t.Parallel()

	handler, cookieFor := newRequireTrustHandler(t)

	for _, trust := range []peers.TrustLevel{peers.Admin, peers.Ultimate} {
		trust := trust
		t.Run(trust.String(), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/node/epm", nil)
			req.AddCookie(cookieFor(trust))
			rec := httptest.NewRecorder()
			if !handler.RequireTrust(rec, req, peers.Admin) {
				t.Fatalf("RequireTrust rejected a %s session at the admin gate: %s", trust, rec.Body.String())
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("RequireTrust wrote a body on success: %q", rec.Body.String())
			}
			if got := rec.Result().Header.Get("Set-Cookie"); got != "" {
				t.Fatalf("RequireTrust rotated the session cookie on success: %q", got)
			}
		})
	}
}

// TestRequireTrustNilHandlerFailsClosed locks the fail-closed posture: a route
// whose auth handler was never constructed must admit nobody, not panic and not
// fall open.
func TestRequireTrustNilHandlerFailsClosed(t *testing.T) {
	t.Parallel()

	var handler *Handler
	req := httptest.NewRequest(http.MethodPut, "/api/node/epm", nil)
	rec := httptest.NewRecorder()
	if handler.RequireTrust(rec, req, peers.Admin) {
		t.Fatal("nil handler admitted a request")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
