package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/appmanifest"
)

// TestResolveServedAppsRegistry proves the data-driven registry resolves both
// apps (conjunction + supplemental-omm) with the expected slugs/ids and that
// each resolved page byte-equals decode(record) — i.e. the current serving
// artifact for that app. Adding App 3 is a data change in appServingSpecs();
// this test then just gets a third expected entry.
func TestResolveServedAppsRegistry(t *testing.T) {
	apps, err := resolveServedApps()
	if err != nil {
		t.Fatalf("resolveServedApps() error = %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("resolveServedApps() returned %d apps, want 2 (conjunction + supplemental-omm)", len(apps))
	}

	bySlug := map[string]resolvedApp{}
	for _, a := range apps {
		bySlug[a.slug] = a
	}

	conj, ok := bySlug["conjunction"]
	if !ok {
		t.Fatal("registry missing the conjunction app")
	}
	if conj.id != appmanifest.ConjunctionAppID {
		t.Errorf("conjunction id = %q, want %q", conj.id, appmanifest.ConjunctionAppID)
	}
	if conj.mediaType != "text/html" {
		t.Errorf("conjunction mediaType = %q, want text/html", conj.mediaType)
	}
	// The served page must byte-equal decode(record), which for conjunction is
	// the embedded serving artifact itself.
	if !bytes.Equal(conj.page, conjunctionAppHTML) {
		t.Errorf("conjunction resolved page (%d bytes) does not byte-equal the embedded artifact (%d bytes)", len(conj.page), len(conjunctionAppHTML))
	}

	supp, ok := bySlug["supplemental-omm"]
	if !ok {
		t.Fatal("registry missing the supplemental-omm app")
	}
	if supp.id != appmanifest.SupplementalOMMAppID {
		t.Errorf("supplemental-omm id = %q, want %q", supp.id, appmanifest.SupplementalOMMAppID)
	}
	// Consumed ONLY via the record/embed API — the served page must byte-equal
	// the current embedded board (read through the record), never a snapshot.
	if !bytes.Equal(supp.page, appmanifest.SupplementalOMMBoardHTML()) {
		t.Errorf("supplemental-omm resolved page (%d bytes) does not byte-equal the embedded board via the record/embed API (%d bytes)", len(supp.page), len(appmanifest.SupplementalOMMBoardHTML()))
	}
}

// TestAppsLauncher exercises GET /apps/: 200, the conjunction-grade header set,
// text/html, self-contained (zero external references), and a listing that names
// and links BOTH apps. Served with no auth headers — these are public pages.
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
		appmanifest.ConjunctionAppName,
		appmanifest.SupplementalOMMAppName,
		`href="/apps/conjunction/"`,
		`href="/apps/supplemental-omm/"`,
		appmanifest.ConjunctionAppID,
		appmanifest.SupplementalOMMAppID,
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

// TestAppsPerAppPages exercises GET /apps/<slug>/ for both apps: 200, the
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
		{"conjunction", conjunctionAppHTML},
		{"supplemental-omm", appmanifest.SupplementalOMMBoardHTML()},
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
		"/apps/conjunction/sub",
		"/apps/supplemental-omm/assets/x.js",
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

// TestAppsSurfaceCoexistsWithConjunction is the A2.10 cutover-contract surface
// block: a mux mirroring main.go's wiring (the /apps/ subtree registered ahead
// of the "/" conjunction surface). It proves the new /apps/* surface serves the
// launcher + both app pages, that "/apps" redirects to "/apps/", that the
// conjunction app still owns "/" — and, crucially, that adding /apps/* does NOT
// un-404 any descoped SpaceAware screen (the C2/C3 404 contract still holds).
func TestAppsSurfaceCoexistsWithConjunction(t *testing.T) {
	appsHandler, err := makeAppsHandler()
	if err != nil {
		t.Fatalf("makeAppsHandler() error = %v", err)
	}
	var fell bool
	surface := makeUISurfaceHandler(frontendSentinel(&fell), nil, true, uiModeConjunction)

	mux := http.NewServeMux()
	mux.Handle("/apps/", appsHandler)
	mux.Handle("/", surface)

	// The conjunction app still owns "/".
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "conj-root") {
		t.Fatalf("GET /: code=%d, conjunction app not served", rec.Code)
	}

	// /apps/ launcher + both app pages served through the same mux.
	for _, tc := range []struct {
		path    string
		wantSub string
	}{
		{"/apps/", appmanifest.SupplementalOMMAppName},
		{"/apps/conjunction/", "conj-root"},
		{"/apps/supplemental-omm/", "window.__SDN_CONFIG__"},
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

	// THE CONTRACT: every descoped SpaceAware screen STILL 404s — adding /apps/*
	// must not re-open any of them.
	descoped := []string{
		"/login", "/console", "/console/node", "/console/peers", "/console/groups",
		"/console/data", "/console/channels", "/console/conjunction",
		"/orbital", "/gantt", "/bmc2", "/bmc2/f1", "/bmc2/f4",
	}
	for _, p := range descoped {
		fell = false
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("descoped %s: status = %d, want 404 (still descoped after /apps/* added)", p, rec.Code)
		}
		if fell {
			t.Errorf("descoped %s: fell through to disk frontend, want 404", p)
		}
	}
}

// TestServeConjunctionUIInjectsAppsLink proves the served conjunction UI carries
// the /apps/ launcher affordance (A2.10 item 3), that it is a same-origin anchor
// (no external reference), and that the embedded artifact itself is untouched
// (the injection is serve-time only).
func TestServeConjunctionUIInjectsAppsLink(t *testing.T) {
	rec := httptest.NewRecorder()
	serveConjunctionUI(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `href="/apps/"`) {
		t.Error("served conjunction UI missing the /apps/ launcher link")
	}
	if !strings.Contains(body, ">APPS</a>") {
		t.Error("served conjunction UI missing the APPS launcher affordance label")
	}
	// Same-origin only: the injected link must not introduce an external host.
	if strings.Contains(appsLauncherLink, "http://") || strings.Contains(appsLauncherLink, "https://") {
		t.Errorf("apps launcher link is not same-origin: %q", appsLauncherLink)
	}
	// The embedded artifact is untouched by the serve-time injection.
	if bytes.Contains(conjunctionAppHTML, []byte(`href="/apps/"`)) {
		t.Error("embedded conjunction artifact unexpectedly contains the injected /apps/ link (injection must be serve-time only)")
	}
}

// assertAppSurfaceHeaders checks the conjunction-grade header set applied to
// every /apps/ response (COOP/COEP/CSP/no-store).
func assertAppSurfaceHeaders(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Errorf("COOP = %q, want same-origin", got)
	}
	if got := h.Get("Cross-Origin-Embedder-Policy"); got != "require-corp" {
		t.Errorf("COEP = %q, want require-corp", got)
	}
	if got := h.Get("Content-Security-Policy"); got != appsCSP {
		t.Errorf("CSP = %q, want %q", got, appsCSP)
	}
	if strings.Contains(h.Get("Content-Security-Policy"), "wallet.spacedatanetwork.org") {
		t.Errorf("supplemental app CSP unexpectedly permits the wallet origin: %q", h.Get("Content-Security-Policy"))
	}
	if got := h.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
