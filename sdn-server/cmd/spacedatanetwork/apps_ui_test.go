package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodedEmbeddedAppPage(t *testing.T, slug string) []byte {
	t.Helper()
	manifest, err := embeddedAppManifest(slug)
	if err != nil {
		t.Fatalf("embeddedAppManifest(%q): %v", slug, err)
	}
	resolution, err := manifest.Resolve()
	if err != nil {
		t.Fatalf("resolve %q: %v", slug, err)
	}
	if resolution.EntryPage == nil {
		t.Fatalf("%q has no entry page", slug)
	}
	page, err := resolution.EntryPage.DecodedContent()
	if err != nil {
		t.Fatalf("decode %q entry page: %v", slug, err)
	}
	return page
}

// TestResolveServedAppsRegistry proves the embedded registry is decoded from
// SDS $APP records and includes the SDN UI homepage application.
func TestResolveServedAppsRegistry(t *testing.T) {
	apps, err := resolveServedApps()
	if err != nil {
		t.Fatalf("resolveServedApps() error = %v", err)
	}
	if len(apps) < 1 {
		t.Fatalf("resolveServedApps() returned %d apps, want at least the SDN UI", len(apps))
	}

	bySlug := map[string]resolvedApp{}
	for _, a := range apps {
		bySlug[a.slug] = a
	}

	sdnUI, ok := bySlug["spaceaware"]
	if !ok {
		t.Fatal("registry missing the SDN UI app")
	}
	if sdnUI.id != "io.spaceaware.sdn-ui" {
		t.Errorf("SDN UI id = %q, want io.spaceaware.sdn-ui", sdnUI.id)
	}
	if sdnUI.mediaType != "text/html" {
		t.Errorf("SDN UI mediaType = %q, want text/html", sdnUI.mediaType)
	}
	// The served page must byte-equal decode($APP), not a second HTML embed.
	if !bytes.Equal(sdnUI.page, decodedEmbeddedAppPage(t, "spaceaware")) {
		t.Errorf("SDN UI resolved page (%d bytes) does not byte-equal decoded embedded $APP", len(sdnUI.page))
	}
}

// TestAppsLauncher exercises GET /apps/: 200, the conjunction-grade header set,
// text/html, self-contained (zero external references), and a listing that names
// and links the remaining embedded app. Served with no auth headers — this is
// a public page.
func TestAppsLauncher(t *testing.T) {
	handler, err := makeAppsHandler()
	if err != nil {
		t.Fatalf("makeAppsHandler() error = %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps/", nil))
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/ status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	assertAppSurfaceHeaders(t, res.Header)

	body := rec.Body.String()
	for _, want := range []string{
		"Space Data Network",
		`href="/apps/spaceaware/"`,
		"io.spaceaware.sdn-ui",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("launcher body missing %q", want)
		}
	}

	// Self-contained: the generated launcher must make no external request and
	// pull no external asset (packaging hard rule; CSP posture).
	for _, forbidden := range []string{"http://", "https://", "//fonts.", "<script src=", `<link rel="stylesheet"`, "cdn.jsdelivr.net", "unpkg.com"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("launcher body contains forbidden external reference %q", forbidden)
		}
	}

	// HEAD returns headers without a body.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/apps/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD /apps/ status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD /apps/ wrote %d body bytes, want 0", rec.Body.Len())
	}
}

// TestAppsPerAppPages exercises GET /apps/<slug>/: 200, the
// conjunction-grade header set, and body == injectFrontendConfig(decode(record))
// — the served page is the app read through the record with the same
// __SDN_CONFIG__ injection serveConjunctionUI performs. Both the trailing-slash
// and no-trailing-slash forms serve.
func TestAppsPerAppPages(t *testing.T) {
	handler, err := makeAppsHandler()
	if err != nil {
		t.Fatalf("makeAppsHandler() error = %v", err)
	}

	cases := []struct {
		slug string
		want []byte // decode(record) for this app
	}{
		{"spaceaware", decodedEmbeddedAppPage(t, "spaceaware")},
	}
	for _, tc := range cases {
		for _, path := range []string{"/apps/" + tc.slug + "/", "/apps/" + tc.slug} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			res := rec.Result()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", path, res.StatusCode)
			}
			assertAppSurfaceHeaders(t, res.Header)
			if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("%s Content-Type = %q, want text/html", path, ct)
			}

			expected := injectFrontendConfig(tc.want)
			if !bytes.Equal(rec.Body.Bytes(), expected) {
				t.Errorf("%s body (%d bytes) != injectFrontendConfig(decode(record)) (%d bytes)", path, rec.Body.Len(), len(expected))
			}
			if !strings.Contains(rec.Body.String(), "window.__SDN_CONFIG__") {
				t.Errorf("%s body missing injected window.__SDN_CONFIG__", path)
			}
		}
	}
}

// TestAppsRegistryIndexJSON exercises GET /apps/index.json: the machine-readable
// serving registry a served app's nav uses to enumerate sibling apps. It must
// list every registered app with its serving path and mark exactly one root.
func TestAppsRegistryIndexJSON(t *testing.T) {
	handler, err := makeAppsHandler()
	if err != nil {
		t.Fatalf("makeAppsHandler() error = %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps/index.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /apps/index.json status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	var payload struct {
		Apps []struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
			Path string `json:"path"`
			Root bool   `json:"root"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("parse registry JSON: %v", err)
	}
	// App-blind contract: the registry mirrors the embedded serving registry
	// exactly — same size, every entry canonical, exactly one root.
	specs, err := embeddedAppSpecs()
	if err != nil {
		t.Fatalf("embeddedAppSpecs() error = %v", err)
	}
	if len(payload.Apps) != len(specs) {
		t.Errorf("registry lists %d apps, want %d (the embedded registry)", len(payload.Apps), len(specs))
	}
	roots := 0
	for _, app := range payload.Apps {
		if app.Root {
			roots++
		}
		if app.Slug == "" || app.Name == "" {
			t.Errorf("registry entry %+v missing slug or name", app)
		}
		if app.Path != "/apps/"+app.Slug+"/" {
			t.Errorf("app %q path = %q, want /apps/%s/", app.Slug, app.Path, app.Slug)
		}
	}
	if roots != 1 {
		t.Errorf("registry marks %d root apps, want exactly 1", roots)
	}
}

// TestAppsUnknown404 proves an unknown app slug and any deeper path under a known
// app both 404 (pages are self-contained inline; there are no sub-assets).
func TestAppsUnknown404(t *testing.T) {
	handler, err := makeAppsHandler()
	if err != nil {
		t.Fatalf("makeAppsHandler() error = %v", err)
	}
	for _, path := range []string{
		"/apps/nope/",
		"/apps/nope",
		"/apps/spaceaware/sub",
		"/apps/unknown/assets/x.js",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, rec.Code)
		}
	}
}

// TestAppsMethodNotAllowed proves non-GET/HEAD methods are rejected.
func TestAppsMethodNotAllowed(t *testing.T) {
	handler, err := makeAppsHandler()
	if err != nil {
		t.Fatalf("makeAppsHandler() error = %v", err)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, "/apps/", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /apps/ status = %d, want 405", method, rec.Code)
		}
	}
}

// TestAppsSurfaceSharesTheEmbeddedRootRecord proves / and /apps/spaceaware/ serve
// the same decoded embedded $APP page through the same generic resolver.
func TestAppsSurfaceSharesTheEmbeddedRootRecord(t *testing.T) {
	appsHandler, err := makeAppsHandler()
	if err != nil {
		t.Fatalf("makeAppsHandler() error = %v", err)
	}
	surface, err := makeEmbeddedAppSurfaceHandler("spaceaware")
	if err != nil {
		t.Fatalf("makeEmbeddedAppSurfaceHandler() error = %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/apps/", appsHandler)
	mux.Handle("/", surface)

	// The SDN UI APP owns "/".
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Space Data Network") {
		t.Fatalf("GET /: code=%d, SDN UI app not served", rec.Code)
	}

	// /apps/ launcher + SDN UI page served through the same mux.
	for _, tc := range []struct {
		path    string
		wantSub string
	}{
		{"/apps/", "Space Data Network"},
		{"/apps/spaceaware/", "Space Data Network Dashboard"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.wantSub) {
			t.Errorf("GET %s body missing %q", tc.path, tc.wantSub)
		}
	}

	// "/apps" (no trailing slash) → ServeMux subtree redirect to "/apps/"
	// (the exact 3xx code is a stdlib detail; the contract is "redirects to the
	// canonical trailing-slash launcher path").
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps", nil))
	if rec.Code < 300 || rec.Code >= 400 {
		t.Errorf("GET /apps status = %d, want a 3xx redirect to /apps/", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/apps/" {
		t.Errorf("GET /apps Location = %q, want /apps/", loc)
	}

	// Unknown app under /apps/ still 404s through the mux.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps/does-not-exist/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /apps/does-not-exist/ status = %d, want 404", rec.Code)
	}

	// Root is a single-page APP; unrecognized path routes do not become a
	// separate static frontend surface.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/not-an-app-route", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /not-an-app-route status = %d, want 404", rec.Code)
	}
}

// assertAppSurfaceHeaders checks the generic APP header set.
func assertAppSurfaceHeaders(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Errorf("COOP = %q, want same-origin", got)
	}
	if got := h.Get("Cross-Origin-Embedder-Policy"); got != "require-corp" {
		t.Errorf("COEP = %q, want require-corp", got)
	}
	if got := h.Get("Content-Security-Policy"); got != appSurfaceCSP {
		t.Errorf("CSP = %q, want %q", got, appSurfaceCSP)
	}
	if strings.Contains(h.Get("Content-Security-Policy"), "wallet.spacedatanetwork.org") {
		t.Errorf("supplemental app CSP unexpectedly permits the wallet origin: %q", h.Get("Content-Security-Policy"))
	}
	if got := h.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
