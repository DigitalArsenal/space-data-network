package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/adminui"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

type surfaceRequest struct{ method, target string }

// olderAdminUI is one of the two admin UIs main.go can mount at /admin, built
// and wired the way main.go does it.
type olderAdminUI struct {
	name     string
	handler  http.Handler
	resource func(urlPath string) bool
	// marker is text that only this UI's HTML carries.
	marker string
	// toDashboard lists the paths, besides those every UI shares, whose GET
	// or HEAD must go to "/".
	toDashboard []string
	// resources are the requests that still reach this UI.
	resources []surfaceRequest
}

// olderAdminUIs returns the hosted admin build, on a temporary build holding
// an index.html, another HTML page and two other files, and the legacy inline
// admin UI over an empty peer registry.
func olderAdminUIs(t *testing.T) []olderAdminUI {
	t.Helper()
	build := t.TempDir()
	for name, body := range map[string]string{
		"index.html":     "<!doctype html><html><head></head><body>OLDER HOSTED ADMIN UI</body></html>",
		"other.html":     "<!doctype html><html><body>OLDER HOSTED ADMIN UI, another page</body></html>",
		"favicon.ico":    "icon",
		"assets/main.js": "console.log('older hosted admin ui')",
	} {
		full := filepath.Join(build, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	host, err := adminui.NewHost(build)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}

	registry := peers.NewRegistry(false, nil)
	t.Cleanup(registry.StopStatsWriter)
	legacy, err := peers.NewAdminUI(registry, peers.NewTrustedConnectionGater(registry))
	if err != nil {
		t.Fatalf("NewAdminUI: %v", err)
	}

	return []olderAdminUI{
		{
			name:        "hosted admin build",
			handler:     host,
			resource:    hostedAdminResource(host),
			marker:      "OLDER HOSTED ADMIN UI",
			toDashboard: []string{"/admin/api/peers", "/admin/api/x", "/admin/index.html", "/admin/other.html", "/admin/assets/missing.js"},
			resources: []surfaceRequest{
				{http.MethodGet, "/admin/assets/main.js"},
				{http.MethodHead, "/admin/assets/main.js"},
				{http.MethodGet, "/admin/favicon.ico"},
				{http.MethodPost, "/admin/assets/main.js"},
			},
		},
		{
			name:        "legacy inline admin UI",
			handler:     legacy,
			resource:    legacy.IsAPIPath,
			marker:      "SDN Admin - Trusted Peer Registry",
			toDashboard: []string{"/admin/admin", "/admin/admin/"},
			resources: []surfaceRequest{
				{http.MethodGet, "/admin/api/peers"},
				{http.MethodGet, "/admin/api/settings"},
				{http.MethodGet, "/admin/api/x"},
				{http.MethodPost, "/admin/api/settings"},
				{http.MethodOptions, "/admin/api/peers"},
			},
		},
	}
}

// adminSurfaceMux mounts /admin over ui and "/" the way main.go does.
func adminSurfaceMux(ui olderAdminUI, requireAuth bool, authHandler *auth.Handler) *http.ServeMux {
	surface := makeAdminSurfaceHandler(ui.handler, ui.resource, requireAuth, authHandler)
	mux := http.NewServeMux()
	mux.Handle("/admin", surface)
	mux.Handle("/admin/", surface)
	mux.Handle("/", makeRootHandler())
	return mux
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

func sessionCookie(token string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token}) }
}

// TestAdminPagesReachTheDashboard: under either admin UI, with or without
// require_auth and a session, a GET or HEAD under /admin that is not one of
// the UI's files or API routes is sent to "/", and following it serves the
// same dashboard document, with the same headers, as "/" itself. That covers
// /admin/api, /admin/api/x and /admin/admin/x.html.
func TestAdminPagesReachTheDashboard(t *testing.T) {
	if len(dashboardHTML) == 0 {
		t.Fatal("embedded dashboard.html is empty — run sdn-js/dashboard/build-dashboard.mjs")
	}
	admin, adminToken := newAdminSession(t, peers.Admin)
	shared := []string{"/admin", "/admin/", "/admin/index", "/admin/peers", "/admin/settings/identity", "/admin/?tab=trust", "/admin/api", "/admin/admin/x.html"}

	for _, ui := range olderAdminUIs(t) {
		for _, setup := range []struct {
			name        string
			requireAuth bool
			authHandler *auth.Handler
			prepare     []func(*http.Request)
		}{
			{"auth off", false, nil, nil},
			{"auth on, admin session", true, admin, []func(*http.Request){sessionCookie(adminToken)}},
			{"auth on, no session", true, admin, nil},
		} {
			name := ui.name + ", " + setup.name
			mux := adminSurfaceMux(ui, setup.requireAuth, setup.authHandler)
			root := throughMux(mux, http.MethodGet, "/")
			if root.Code != http.StatusOK || !bytes.Equal(root.Body.Bytes(), servedUI.current().dashboard) {
				t.Fatalf("%s: GET / = %d, not the embedded dashboard", name, root.Code)
			}
			for _, target := range append(append([]string{}, shared...), ui.toDashboard...) {
				location := "/"
				if i := strings.IndexByte(target, '?'); i >= 0 {
					location += target[i:]
				}
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					rec := throughMux(mux, method, target, setup.prepare...)
					if rec.Code != http.StatusFound || rec.Header().Get("Location") != location || strings.Contains(rec.Body.String(), ui.marker) {
						t.Fatalf("%s: %s %s = %d Location %q, want 302 to %q", name, method, target, rec.Code, rec.Header().Get("Location"), location)
					}
					if method != http.MethodGet {
						continue
					}
					landed := throughMux(mux, method, location, setup.prepare...)
					if landed.Code != http.StatusOK || !bytes.Equal(landed.Body.Bytes(), root.Body.Bytes()) {
						t.Fatalf("%s: following %s did not land on the dashboard \"/\" serves (status %d)", name, target, landed.Code)
					}
					for _, header := range []string{"Content-Security-Policy", "X-SDN-UI", "Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy", "Content-Type"} {
						if landed.Header().Get(header) != root.Header().Get(header) {
							t.Fatalf("%s: %s after %s = %q, want %q as at \"/\"", name, header, target, landed.Header().Get(header), root.Header().Get(header))
						}
					}
				}
			}
		}
	}
}

// TestAdminSurfaceReachesOnlyTheAdminUIResources: the hosted build's files
// (other than HTML) and the legacy UI's /api/ routes reach the admin UI with
// the prefix stripped, under the same Admin gate as before, and answer as the
// UI answers them directly. Any other request that is not a GET or HEAD is
// not found. No response carries the older UI's HTML.
func TestAdminSurfaceReachesOnlyTheAdminUIResources(t *testing.T) {
	notFound := []surfaceRequest{
		{http.MethodPost, "/admin"},
		{http.MethodPost, "/admin/"},
		{http.MethodPost, "/admin/admin/"},
		{http.MethodPost, "/admin/admin/x.html"},
		{http.MethodPut, "/admin/peers"},
		{http.MethodDelete, "/admin/settings/identity"},
	}
	admin, tokens := sessionsAt(t, peers.Admin, peers.Standard)
	adminToken, standardToken := tokens[0], tokens[1]
	asJSON := func(r *http.Request) { r.Header.Set("Accept", "application/json") }

	for _, ui := range olderAdminUIs(t) {
		direct := func(c surfaceRequest) *httptest.ResponseRecorder {
			return throughMux(ui.handler, c.method, strings.TrimPrefix(c.target, "/admin"))
		}
		check := func(name string, c surfaceRequest, rec *httptest.ResponseRecorder, want int) {
			t.Helper()
			if rec.Code != want {
				t.Fatalf("%s, %s: %s %s = %d, want %d", ui.name, name, c.method, c.target, rec.Code, want)
			}
			if strings.Contains(rec.Body.String(), ui.marker) {
				t.Fatalf("%s, %s: %s %s served the older admin UI's HTML", ui.name, name, c.method, c.target)
			}
		}
		reaches := func(name string, c surfaceRequest, rec *httptest.ResponseRecorder) {
			t.Helper()
			want := direct(c)
			check(name, c, rec, want.Code)
			if !bytes.Equal(rec.Body.Bytes(), want.Body.Bytes()) {
				t.Fatalf("%s, %s: %s %s answered %q, want the admin UI's own answer %q", ui.name, name, c.method, c.target, rec.Body.String(), want.Body.String())
			}
		}

		mux := adminSurfaceMux(ui, false, nil)
		for _, c := range ui.resources {
			reaches("auth off", c, throughMux(mux, c.method, c.target))
		}
		for _, c := range notFound {
			check("auth off", c, throughMux(mux, c.method, c.target), http.StatusNotFound)
		}

		mux = adminSurfaceMux(ui, true, nil)
		for _, c := range ui.resources {
			check("auth on, no auth handler", c, throughMux(mux, c.method, c.target), http.StatusServiceUnavailable)
		}

		mux = adminSurfaceMux(ui, true, admin)
		for _, c := range ui.resources {
			check("auth on, no session", c, throughMux(mux, c.method, c.target, asJSON), http.StatusUnauthorized)
			check("auth on, Standard session", c, throughMux(mux, c.method, c.target, asJSON, sessionCookie(standardToken)), http.StatusForbidden)
			reaches("auth on, Admin session", c, throughMux(mux, c.method, c.target, sessionCookie(adminToken)))
		}
		for _, c := range notFound {
			check("auth on, Admin session", c, throughMux(mux, c.method, c.target, sessionCookie(adminToken)), http.StatusNotFound)
		}
		// Without a session, a browser asking for one of the UI's resources is
		// still sent to sign in.
		c := ui.resources[0]
		want := "/login?next=" + strings.ReplaceAll(c.target, "/", "%2F")
		if rec := throughMux(mux, c.method, c.target); rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Fatalf("%s: %s %s without a session = %d Location %q, want 302 to %q", ui.name, c.method, c.target, rec.Code, rec.Header().Get("Location"), want)
		}
	}
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
