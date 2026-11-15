package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// adminSurfaceMux mounts /admin and "/" the way main.go does, over a stand-in
// for the older admin UI that records what reached it.
func adminSurfaceMux(requireAuth bool, authHandler *auth.Handler) (*http.ServeMux, *[]string) {
	var reached []string
	oldUI := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte("OLDER ADMIN UI"))
	})
	surface := makeAdminSurfaceHandler(oldUI, requireAuth, authHandler)
	mux := http.NewServeMux()
	mux.Handle("/admin", surface)
	mux.Handle("/admin/", surface)
	mux.Handle("/", makeRootHandler())
	return mux, &reached
}

func throughMux(mux http.Handler, method, target string, prepare ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, p := range prepare {
		p(req)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestAdminPagesReachTheDashboard: every page request under /admin, with or
// without a session and with or without require_auth, is sent to "/", and
// following it serves the same dashboard document, with the same headers, as
// "/" itself. The older admin UI is never reached.
func TestAdminPagesReachTheDashboard(t *testing.T) {
	if len(dashboardHTML) == 0 {
		t.Fatal("embedded dashboard.html is empty — run sdn-js/dashboard/build-dashboard.mjs")
	}
	admin, adminToken := newAdminSession(t, peers.Admin)
	withAdmin := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: adminToken}) }

	for _, setup := range []struct {
		name        string
		requireAuth bool
		authHandler *auth.Handler
		prepare     []func(*http.Request)
	}{
		{"auth off", false, nil, nil},
		{"auth on, admin session", true, admin, []func(*http.Request){withAdmin}},
		{"auth on, no session", true, admin, nil},
	} {
		mux, reached := adminSurfaceMux(setup.requireAuth, setup.authHandler)
		root := throughMux(mux, http.MethodGet, "/")
		if root.Code != http.StatusOK || !bytes.Equal(root.Body.Bytes(), servedUI.current().dashboard) {
			t.Fatalf("%s: GET / = %d, not the embedded dashboard", setup.name, root.Code)
		}
		for _, page := range []struct{ target, location string }{
			{"/admin", "/"},
			{"/admin/", "/"},
			{"/admin/index", "/"},
			{"/admin/peers", "/"},
			{"/admin/settings/identity", "/"},
			{"/admin/?tab=trust", "/?tab=trust"},
		} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := throughMux(mux, method, page.target, setup.prepare...)
				if rec.Code != http.StatusFound || rec.Header().Get("Location") != page.location {
					t.Fatalf("%s: %s %s = %d Location %q, want 302 to %q", setup.name, method, page.target, rec.Code, rec.Header().Get("Location"), page.location)
				}
				if method != http.MethodGet {
					continue
				}
				landed := throughMux(mux, method, page.location, setup.prepare...)
				if landed.Code != http.StatusOK || !bytes.Equal(landed.Body.Bytes(), root.Body.Bytes()) {
					t.Fatalf("%s: following %s did not land on the dashboard \"/\" serves (status %d)", setup.name, page.target, landed.Code)
				}
				for _, header := range []string{"Content-Security-Policy", "X-SDN-UI", "Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy", "Content-Type"} {
					if landed.Header().Get(header) != root.Header().Get(header) {
						t.Fatalf("%s: %s after %s = %q, want %q as at \"/\"", setup.name, header, page.target, landed.Header().Get(header), root.Header().Get(header))
					}
				}
			}
		}
		if len(*reached) != 0 {
			t.Fatalf("%s: a page request reached the older admin UI: %v", setup.name, *reached)
		}
	}
}

// TestAdminSurfaceKeepsWhatIsNotAPage: the admin UI's files, the legacy
// /admin/api/ routes and every non-GET/HEAD request reach the admin UI with
// the prefix stripped, under the same Admin gate as before; a non-page
// request at /admin still moves to /admin/.
func TestAdminSurfaceKeepsWhatIsNotAPage(t *testing.T) {
	kept := []struct{ method, target, stripped string }{
		{http.MethodGet, "/admin/assets/index-abc123.js", "/assets/index-abc123.js"},
		{http.MethodHead, "/admin/assets/index-abc123.css", "/assets/index-abc123.css"},
		{http.MethodGet, "/admin/favicon.ico", "/favicon.ico"},
		{http.MethodGet, "/admin/api/peers", "/api/peers"},
		{http.MethodGet, "/admin/api", "/api"},
		{http.MethodPost, "/admin/api/settings", "/api/settings"},
		{http.MethodDelete, "/admin/api/peers/12D3KooWExample", "/api/peers/12D3KooWExample"},
		{http.MethodPost, "/admin/", "/"},
	}

	t.Run("auth off", func(t *testing.T) {
		mux, reached := adminSurfaceMux(false, nil)
		for _, c := range kept {
			before := len(*reached)
			rec := throughMux(mux, c.method, c.target)
			if rec.Code != http.StatusOK || len(*reached) != before+1 || (*reached)[before] != c.method+" "+c.stripped {
				t.Fatalf("%s %s = %d, reached %v; want the admin UI at %s", c.method, c.target, rec.Code, *reached, c.stripped)
			}
		}
		rec := throughMux(mux, http.MethodPost, "/admin")
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/admin/" {
			t.Fatalf("POST /admin = %d Location %q, want 301 to /admin/", rec.Code, rec.Header().Get("Location"))
		}
	})

	t.Run("auth on, no auth handler", func(t *testing.T) {
		mux, reached := adminSurfaceMux(true, nil)
		for _, c := range kept {
			if rec := throughMux(mux, c.method, c.target); rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s %s = %d, want 503 as before", c.method, c.target, rec.Code)
			}
		}
		if len(*reached) != 0 {
			t.Fatalf("the admin UI was reached without authentication: %v", *reached)
		}
	})

	t.Run("auth on", func(t *testing.T) {
		admin, tokens := sessionsAt(t, peers.Admin, peers.Standard)
		adminToken, standardToken := tokens[0], tokens[1]
		mux, reached := adminSurfaceMux(true, admin)
		asJSON := func(r *http.Request) { r.Header.Set("Accept", "application/json") }
		session := func(token string) func(*http.Request) {
			return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token}) }
		}
		for _, c := range kept {
			if rec := throughMux(mux, c.method, c.target, asJSON); rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s without a session = %d, want 401", c.method, c.target, rec.Code)
			}
			if rec := throughMux(mux, c.method, c.target, asJSON, session(standardToken)); rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s with a Standard session = %d, want 403", c.method, c.target, rec.Code)
			}
		}
		if len(*reached) != 0 {
			t.Fatalf("the admin UI was reached without an Admin session: %v", *reached)
		}
		for _, c := range kept {
			before := len(*reached)
			rec := throughMux(mux, c.method, c.target, session(adminToken))
			if rec.Code != http.StatusOK || len(*reached) != before+1 || (*reached)[before] != c.method+" "+c.stripped {
				t.Fatalf("%s %s with an Admin session = %d, reached %v; want the admin UI at %s", c.method, c.target, rec.Code, *reached, c.stripped)
			}
		}
		// Without a session, a browser asking for a file is still sent to sign in.
		rec := throughMux(mux, http.MethodGet, "/admin/assets/index-abc123.js")
		if want := "/login?next=%2Fadmin%2Fassets%2Findex-abc123.js"; rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Fatalf("GET of an admin file without a session = %d Location %q, want 302 to %q", rec.Code, rec.Header().Get("Location"), want)
		}
	})
}

// sessionsAt returns a real session-store-backed *auth.Handler and one valid
// session token per trust level, all in the same store.
func sessionsAt(t *testing.T, trusts ...peers.TrustLevel) (*auth.Handler, []string) {
	t.Helper()
	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })
	sessions, err := auth.NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	tokens := make([]string, 0, len(trusts))
	for i, trust := range trusts {
		token, err := sessions.CreateSession(fmt.Sprintf("xpub-test-%d", i), trust, "127.0.0.1", "test-agent", time.Hour)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		tokens = append(tokens, token)
	}
	return auth.NewHandler(nil, sessions, time.Hour, "", ""), tokens
}
