package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/abac"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func newRefusalSessions(t *testing.T) *SessionStore {
	t.Helper()
	dir := t.TempDir()
	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })
	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	return sessions
}

// The defect this task closes: a cross-origin browser could not READ the wall's
// 401, so the page could not tell "sign in" from "the node is down".
func TestRequireAuthUnauthorizedIsReadableCrossOrigin(t *testing.T) {
	t.Parallel()

	h := NewHandler(nil, newRefusalSessions(t), time.Hour, "", "")
	req := httptest.NewRequest(http.MethodPut, credentialRoute, nil)
	req.Header.Set("Origin", galleryOriginAuth)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run for an unauthenticated request")
	})(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != galleryOriginAuth {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q — the browser discards a 401 it cannot read", got, galleryOriginAuth)
	}
	if got := strings.Join(rec.Header().Values("Vary"), ", "); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary = %q, want it to contain Origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"unauthorized"`) {
		t.Fatalf("body = %q, want the unauthorized code", body)
	}
}

func TestRequireTrustRefusalsAreReadableCrossOrigin(t *testing.T) {
	t.Parallel()

	sessions := newRefusalSessions(t)
	token, err := sessions.CreateSession("xpub-standard-user", peers.Standard, "127.0.0.1", "test-agent", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	h := NewHandler(nil, sessions, time.Hour, "", "")

	cases := []struct {
		name    string
		handler *Handler
		cookie  string
		want    int
	}{
		{name: "nil handler admits nobody", handler: nil, want: http.StatusUnauthorized},
		{name: "no session", handler: h, want: http.StatusUnauthorized},
		{name: "below trust", handler: h, cookie: token, want: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/node/epm", nil)
			req.Header.Set("Origin", galleryOriginAuth)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: tc.cookie})
			}
			rec := httptest.NewRecorder()

			if tc.handler.RequireTrust(rec, req, peers.Admin) {
				t.Fatal("RequireTrust admitted a caller it must refuse")
			}
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != galleryOriginAuth {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, galleryOriginAuth)
			}
		})
	}
}

type denyEverything struct{}

func (denyEverything) Evaluate(abac.Subject, abac.Action, abac.Resource) abac.Decision {
	return abac.Decision{Allowed: false, Reason: "test denial"}
}

func TestRequirePolicyRefusalsAreReadableCrossOrigin(t *testing.T) {
	t.Parallel()

	h := NewHandler(nil, newRefusalSessions(t), time.Hour, "", "")
	resource := func(*http.Request) abac.Resource { return abac.Resource{} }

	t.Run("policy denial", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/data/records/x", nil)
		req.Header.Set("Origin", galleryOriginAuth)
		req = req.WithContext(ContextWithSession(req.Context(), &Session{XPub: "xpub-user", TrustLevel: peers.Standard}))
		rec := httptest.NewRecorder()

		h.RequirePolicy(denyEverything{}, abac.Action("write"), resource)(func(http.ResponseWriter, *http.Request) {
			t.Fatal("handler must not run after a policy denial")
		})(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != galleryOriginAuth {
			t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, galleryOriginAuth)
		}
	})

	t.Run("defensive missing session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/data/records/x", nil)
		req.Header.Set("Origin", galleryOriginAuth)
		rec := httptest.NewRecorder()

		h.RequirePolicy(denyEverything{}, abac.Action("write"), resource)(func(http.ResponseWriter, *http.Request) {
			t.Fatal("handler must not run with no session in context")
		})(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != galleryOriginAuth {
			t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, galleryOriginAuth)
		}
	})
}

// curl, the loopback control lanes and every same-origin page request must see
// the response they have always seen.
func TestRefusalWithoutOriginIsUnchanged(t *testing.T) {
	t.Parallel()

	h := NewHandler(nil, newRefusalSessions(t), time.Hour, "", "")
	req := httptest.NewRequest(http.MethodPut, credentialRoute, nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {})(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	for _, header := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Vary"} {
		if got := rec.Header().Get(header); got != "" {
			t.Fatalf("%s = %q on a request with no Origin, want empty", header, got)
		}
	}
}

// The redirect half of the wall is for PAGE requests and is deliberately left
// alone: a cross-origin fetch never wants a login page.
func TestBrowserPageRedirectIsUnchanged(t *testing.T) {
	t.Parallel()

	h := NewHandler(nil, newRefusalSessions(t), time.Hour, "", "")
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.Header.Set("Origin", galleryOriginAuth)
	rec := httptest.NewRecorder()

	h.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {})(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
}

// "A refusal is readable" must be a property of the WALL, not of the lines that
// happened to be updated in one commit.
func TestEveryAuthWallRefusalGoesThroughWriteAuthRefusal(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("middleware.go")
	if err != nil {
		t.Fatalf("read middleware.go: %v", err)
	}
	for _, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(trimmed, "writeJSON(w, http.StatusUnauthorized") ||
			strings.Contains(trimmed, "writeJSON(w, http.StatusForbidden") {
			t.Fatalf("bare refusal in middleware.go — use writeAuthRefusal so a cross-origin browser can read it:\n  %s", trimmed)
		}
	}
}

// Making the refusal readable is not enough: a caller that AUTHENTICATES
// cross-origin must be able to read its own answer, or the lane is still
// unusable. Scoped to signed-request admission (HERMES 2026-08-09 §b).
func TestAdmittedSignedRequestResponseIsReadableCrossOrigin(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	id, challenge := f.mintChallenge(t)
	body := []byte(`{}`)

	rec := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"credentialConfigured":true}`))
	})(rec, newSignedRequest(http.MethodPut, credentialRoute, body, f.authorization(id, challenge, http.MethodPut, credentialRoute, body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != galleryOriginAuth {
		t.Fatalf("Access-Control-Allow-Origin = %q on the SUCCESS path, want %q — an unreadable 200 leaves the lane just as broken", got, galleryOriginAuth)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want empty — never, on any path", got)
	}
}

// A COOKIE-authenticated caller gains nothing from the decoration, so it does
// not get one. Narrower on purpose.
func TestAdmittedCookieSessionResponseIsNotDecorated(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	token, err := f.handler.sessions.CreateSession(f.xpub, peers.Standard, "127.0.0.1", "test-agent", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := newSignedRequest(http.MethodPut, credentialRoute, []byte(`{}`), "")
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q for a cookie-authenticated caller; the decoration is scoped to signed-request admission", got)
	}
}

// RequireTrust is the sole gate for some routes on a require_auth:false node,
// so it carries the decoration too.
func TestRequireTrustDecoratesAnAdmittedSignedRequest(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	id, challenge := f.mintChallenge(t)
	body := []byte(`{}`)

	req := newSignedRequest(http.MethodPut, "/api/node/epm", body, f.authorization(id, challenge, http.MethodPut, "/api/node/epm", body))
	rec := httptest.NewRecorder()

	if !f.handler.RequireTrust(rec, req, peers.Standard) {
		t.Fatalf("RequireTrust refused a valid signed request (status %d, body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != galleryOriginAuth {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, galleryOriginAuth)
	}
}
