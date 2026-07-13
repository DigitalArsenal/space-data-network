package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResolveUIModeDefaultsToConjunction locks in the SHIPPED default: with
// SDN_UI_MODE unset or unrecognized, the daemon serves the conjunction-only
// app. Only an explicit spaceaware/full opt-in flips to the full app.
func TestResolveUIModeDefaultsToConjunction(t *testing.T) {
	cases := []struct {
		env  string
		want uiMode
	}{
		{"", uiModeConjunction},
		{"conjunction", uiModeConjunction},
		{"Conjunction", uiModeConjunction},
		{"nonsense", uiModeConjunction},
		{"spaceaware", uiModeSpaceAware},
		{"SpaceAware", uiModeSpaceAware},
		{"  full  ", uiModeSpaceAware},
		{"dev", uiModeSpaceAware},
		{"spaceaware-full", uiModeSpaceAware},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("SDN_UI_MODE", tc.env)
			if got := resolveUIMode(); got != tc.want {
				t.Fatalf("resolveUIMode() with SDN_UI_MODE=%q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestConjunctionAppEmbeddedArtifact asserts the conjunction artifact is
// embedded, self-contained (no external references — packaging hard rule), and
// has the config-injection point plus the standalone root element.
func TestConjunctionAppEmbeddedArtifact(t *testing.T) {
	if len(conjunctionAppHTML) == 0 {
		t.Fatal("embedded conjunction_app.html is empty")
	}
	html := string(conjunctionAppHTML)
	if !strings.Contains(html, "</head>") {
		t.Fatal("embedded conjunction artifact missing </head> (config injection point)")
	}
	if !strings.Contains(html, "conj-root") {
		t.Error("embedded conjunction artifact missing standalone root element #conj-root")
	}
	for _, forbidden := range []string{"<script src=", `<link rel="stylesheet"`, "fonts.googleapis.com", "fonts.gstatic.com", "cdn.jsdelivr.net", "unpkg.com"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("embedded conjunction artifact contains forbidden external reference %q", forbidden)
		}
	}
	// C1 packaging win: no hd-wallet wasm ships with the conjunction UI.
	for _, forbidden := range []string{"application/wasm", "AGFzbQ"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("embedded conjunction artifact unexpectedly contains %q (wasm must not ship with the conjunction UI)", forbidden)
		}
	}
}

// TestServeConjunctionUI verifies the serving handler: config-injected bytes,
// cross-origin-isolation headers, no-store caching, and method restriction.
func TestServeConjunctionUI(t *testing.T) {
	rec := httptest.NewRecorder()
	serveConjunctionUI(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Errorf("COOP = %q, want same-origin", got)
	}
	if got := res.Header.Get("Cross-Origin-Embedder-Policy"); got != "require-corp" {
		t.Errorf("COEP = %q, want require-corp", got)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := res.Header.Get("Content-Security-Policy"); got != conjunctionCSP {
		t.Errorf("CSP = %q, want %q", got, conjunctionCSP)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "window.__SDN_CONFIG__") {
		t.Error("served body missing injected window.__SDN_CONFIG__")
	}
	if !strings.Contains(body, "conj-root") {
		t.Error("served body missing conjunction root element")
	}

	// HEAD returns headers without a body.
	rec = httptest.NewRecorder()
	serveConjunctionUI(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Result().StatusCode != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", rec.Result().StatusCode)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD wrote a body of %d bytes, want 0", rec.Body.Len())
	}

	// Non-GET/HEAD is rejected.
	rec = httptest.NewRecorder()
	serveConjunctionUI(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Result().StatusCode)
	}
}

// TestConjunctionCSP asserts the Content-Security-Policy served with the
// conjunction UI (C3) is present and encodes the self-hosting posture: the
// exfiltration-blocking connect-src 'self', locked default/object/base-uri,
// clickjacking/form protections, and the inline-allowing script/style +
// data:-font directives the single-file build requires. A regression that
// loosens connect-src to allow a third-party host, or drops the header, fails
// here.
func TestConjunctionCSP(t *testing.T) {
	rec := httptest.NewRecorder()
	serveConjunctionUI(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Result().Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("conjunction UI served without a Content-Security-Policy header")
	}
	for _, directive := range []string{
		"default-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"form-action 'none'",
		"script-src 'self' 'unsafe-inline'",
		"style-src 'self' 'unsafe-inline'",
		"font-src 'self' data:",
		"img-src 'self' data:",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP missing directive %q; got %q", directive, csp)
		}
	}
	// connect-src must NOT be widened to any external host (exfiltration guard).
	if strings.Contains(csp, "connect-src 'self' http") || strings.Contains(csp, "connect-src *") {
		t.Errorf("CSP connect-src is widened beyond 'self': %q", csp)
	}
	// No wasm ships with the conjunction UI (C1), so no eval escape hatch.
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP unexpectedly allows unsafe-eval: %q", csp)
	}
}

// frontendSentinel is a stand-in disk frontend handler that records whether it
// was reached and echoes a sentinel body, so the surface-handler tests can tell
// "served by the embedded UI" from "fell through to the disk frontend".
func frontendSentinel(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("disk-frontend"))
	})
}

// TestFrontendSurfaceHandlerConjunctionMode is the C2 serving-shape contract:
// the conjunction app is served at "/", every descoped SpaceAware screen 404s,
// and non-UI paths fall through to the disk frontend handler. Exercised through
// makeUISurfaceHandler (the mode dispatcher wired at "/" in main.go).
func TestFrontendSurfaceHandlerConjunctionMode(t *testing.T) {
	var fell bool
	handler := makeUISurfaceHandler(frontendSentinel(&fell), nil, true, uiModeConjunction)

	// Primary route serves the conjunction bytes (not the disk frontend).
	for _, p := range []string{"/", "/index.html"} {
		fell = false
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", p, rec.Code)
		}
		if fell {
			t.Fatalf("%s: fell through to disk frontend, want conjunction app", p)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "conj-root") || !strings.Contains(body, "window.__SDN_CONFIG__") {
			t.Fatalf("%s: body is not the injected conjunction app", p)
		}
		if got := rec.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
			t.Errorf("%s: COOP = %q, want same-origin", p, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != conjunctionCSP {
			t.Errorf("%s: CSP = %q, want %q", p, got, conjunctionCSP)
		}
	}

	// Deep link via ?group= is preserved (path is still "/").
	fell = false
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?group=iss-env", nil))
	if rec.Code != http.StatusOK || fell || !strings.Contains(rec.Body.String(), "conj-root") {
		t.Fatalf("/?group=iss-env did not serve the conjunction app (code=%d fell=%v)", rec.Code, fell)
	}

	// Every descoped SpaceAware screen must 404 — code stays, serving stops.
	descoped := []string{
		"/login",
		"/console",
		"/console/node",
		"/console/peers",
		"/console/groups",
		"/console/data",
		"/console/channels",
		"/console/conjunction",
		"/orbital",
		"/gantt",
		"/bmc2",
		"/bmc2/f1",
		"/bmc2/f4",
	}
	for _, p := range descoped {
		fell = false
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("descoped %s: status = %d, want 404", p, rec.Code)
		}
		if fell {
			t.Errorf("descoped %s: fell through to disk frontend, want 404", p)
		}
	}

	// Non-UI paths (assets, unknown) fall through to the disk frontend handler.
	for _, p := range []string{"/assets/app.js", "/some-legacy-page"} {
		fell = false
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if !fell {
			t.Errorf("%s: did not fall through to disk frontend", p)
		}
		if body := rec.Body.String(); body != "disk-frontend" {
			t.Errorf("%s: body = %q, want disk-frontend passthrough", p, body)
		}
	}
}

// TestFrontendSurfaceHandlerSpaceAwareMode locks in that the dev/full mode is
// unchanged: SpaceAware routes serve the full app, and "/" falls through. The
// dispatcher must delegate to the unmodified makeFrontendSurfaceHandler.
func TestFrontendSurfaceHandlerSpaceAwareMode(t *testing.T) {
	var fell bool
	handler := makeUISurfaceHandler(frontendSentinel(&fell), nil, true, uiModeSpaceAware)

	// A SpaceAware route serves the full app artifact.
	fell = false
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/console/node", nil))
	if rec.Code != http.StatusOK || fell {
		t.Fatalf("/console/node: code=%d fell=%v, want 200 served by SpaceAware app", rec.Code, fell)
	}
	if !strings.Contains(rec.Body.String(), "sa-root") {
		t.Error("/console/node: body is not the SpaceAware app (missing sa-root)")
	}

	// "/" is not a SpaceAware route → disk frontend passthrough.
	fell = false
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !fell || rec.Body.String() != "disk-frontend" {
		t.Fatalf("/: expected disk-frontend passthrough in spaceaware mode (fell=%v body=%q)", fell, rec.Body.String())
	}
}

// TestConjunctionDataSourcesStayAnonymous re-verifies (unit level) that the
// conjunction app's data sources remain anonymous-reachable and the admin API
// wall stays gated — the auth semantics C2 must not touch.
func TestConjunctionDataSourcesStayAnonymous(t *testing.T) {
	anonymous := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/peers"},
		{http.MethodGet, "/api/v1/channels"},
		{http.MethodGet, "/api/v1/stats"},
		{http.MethodGet, "/api/v1/data/health"},
	}
	for _, tc := range anonymous {
		if !isPublicAPIRequest(tc.method, tc.path) {
			t.Errorf("conjunction data source %s %s is not anonymous-reachable", tc.method, tc.path)
		}
	}

	// Admin/operator surfaces stay walled.
	gated := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/data/summary"},
		{http.MethodPost, "/api/v1/data/query"},
		{http.MethodGet, "/api/auth/me"},
	}
	for _, tc := range gated {
		if isPublicAPIRequest(tc.method, tc.path) {
			t.Errorf("%s %s must stay gated, but is anonymous-reachable", tc.method, tc.path)
		}
	}
}
