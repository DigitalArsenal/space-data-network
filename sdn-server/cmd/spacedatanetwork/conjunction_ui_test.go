package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
)

// TestResolveUIModeDefaultsToConjunction locks in the SHIPPED default: with
// SDN_UI_MODE unset or unrecognized, the daemon runs the isolated-wallet
// conjunction auth surface. Only an explicit spaceaware/full opt-in flips to
// the legacy development surfaces.
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

func rootAppSurfaceForTest(t *testing.T) http.Handler {
	t.Helper()
	handler, err := makeEmbeddedAppSurfaceHandler("spaceaware")
	if err != nil {
		t.Fatalf("makeEmbeddedAppSurfaceHandler() error = %v", err)
	}
	return handler
}

func TestWalletCallbackIsExactAndNoStore(t *testing.T) {
	if len(walletCallbackHTML) == 0 {
		t.Fatal("embedded wallet callback is empty")
	}

	// Exercise the same outer security middleware used by the admin server. The
	// callback-specific policy must override its generic referrer policy.
	handler := adminSecurityMiddleware(rootAppSurfaceForTest(t), "", func(string, string) bool { return false })

	for _, path := range []string{"/wallet/callback", "/wallet/callback/", "/wallet-callback.html"} {
		t.Run(path, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("%s %s: status = %d, want 200", method, path, rec.Code)
				}
				assertWalletCallbackHeaders(t, rec.Header())
				if method == http.MethodHead {
					if rec.Body.Len() != 0 {
						t.Fatalf("HEAD body length = %d, want 0", rec.Body.Len())
					}
					return
				}
				if !bytes.Equal(rec.Body.Bytes(), walletCallbackHTML) {
					t.Fatal("GET body differs from exact embedded wallet callback bytes")
				}
				if strings.Contains(rec.Body.String(), "window.__SDN_CONFIG__") {
					t.Fatal("wallet callback received application-shell bytes")
				}
			}
		})
	}
}

func TestWalletCallbackRejectsOtherMethods(t *testing.T) {
	handler := adminSecurityMiddleware(rootAppSurfaceForTest(t), "", func(string, string) bool { return false })

	for _, path := range []string{"/wallet/callback", "/wallet/callback/", "/wallet-callback.html"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, path, nil)
			// Even a stale authenticated cookie and hostile Origin must reach the
			// callback's method guard, not be turned into a generic CSRF response.
			req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: "stale"})
			req.Header.Set("Origin", "https://hostile.example")
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status = %d, want 405", method, path, rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("%s %s: Allow = %q, want GET, HEAD", method, path, got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s %s: Cache-Control = %q, want no-store", method, path, got)
			}
		}
	}
}

func assertWalletCallbackHeaders(t *testing.T, header http.Header) {
	t.Helper()
	wants := map[string]string{
		"Cache-Control":           "no-store",
		"Content-Type":            "text/html; charset=utf-8",
		"Content-Security-Policy": walletCallbackCSP,
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
	}
	for name, want := range wants {
		if got := header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestProductionRoutesExcludeLegacyWalletUIAndConfiguration(t *testing.T) {
	walletRoot := t.TempDir()
	legacyBytes := []byte("LEGACY_GENERIC_WALLET_SECRET_RUNTIME")
	if err := os.WriteFile(filepath.Join(walletRoot, "legacy-wallet.js"), legacyBytes, 0o644); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}

	userStore, err := auth.NewUserStore(filepath.Join(t.TempDir(), "auth.db"), nil)
	if err != nil {
		t.Fatalf("create user store: %v", err)
	}
	t.Cleanup(func() { _ = userStore.Close() })

	mux := http.NewServeMux()
	productionWalletPath := legacyWalletUIPathForMode(uiModeConjunction, walletRoot)
	if productionWalletPath != "" {
		t.Fatalf("conjunction wallet UI path = %q, want empty", productionWalletPath)
	}
	authHandler := auth.NewHandler(userStore, nil, time.Hour, productionWalletPath, "/config.yaml")
	authHandler.SetExternalLoginUI(false)
	authHandler.RegisterRoutes(mux)
	if serveRoot, mounted := registerLegacyWalletStaticFiles(mux, uiModeConjunction, walletRoot); mounted || serveRoot != "" {
		t.Fatalf("conjunction legacy static mount = (%q, %v), want disabled", serveRoot, mounted)
	}
	mux.Handle("/", rootAppSurfaceForTest(t))

	for _, requestPath := range []string{"/login", "/login/legacy", "/wallet-ui/legacy-wallet.js"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", requestPath, rec.Code)
		}
		if bytes.Contains(rec.Body.Bytes(), legacyBytes) || strings.Contains(rec.Body.String(), "hd-wallet") {
			t.Errorf("GET %s exposed legacy wallet bytes", requestPath)
		}
	}

	status := httptest.NewRecorder()
	mux.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/status status = %d, want 200", status.Code)
	}
	for _, want := range []string{
		`"wallet_ui_configured":false`,
		`"wallet_js_file":""`,
		`"wallet_css_file":""`,
	} {
		if !strings.Contains(status.Body.String(), want) {
			t.Errorf("auth status missing %s: %s", want, status.Body.String())
		}
	}
}

func TestDataSourcesStayAnonymous(t *testing.T) {
	anonymous := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/peers"},
		{http.MethodGet, "/api/v1/channels"},
		{http.MethodGet, "/api/v1/stats"},
		{http.MethodGet, "/api/v1/data/health"},
	}
	for _, tc := range anonymous {
		if !isPublicAPIRequest(tc.method, tc.path) {
			t.Errorf("data source %s %s is not anonymous-reachable", tc.method, tc.path)
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

// TestEmbeddedRootAPP serves the decoded SDS $APP entry page at the homepage.
// The host must not retain a second raw HTML embed or a UI-specific route.
func TestEmbeddedRootAPP(t *testing.T) {
	if !strings.Contains(appSurfaceCSP, "'wasm-unsafe-eval'") {
		t.Fatalf("embedded app CSP must permit WebAssembly compilation: %q", appSurfaceCSP)
	}
	if !strings.Contains(appSurfaceCSP, "worker-src 'self' blob:") {
		t.Fatalf("embedded app CSP must permit in-memory workers: %q", appSurfaceCSP)
	}

	handler, err := makeEmbeddedAppSurfaceHandler("spaceaware")
	if err != nil {
		t.Fatalf("makeEmbeddedAppSurfaceHandler() error = %v", err)
	}

	want := injectFrontendConfig(decodedEmbeddedAppPage(t, "spaceaware"))
	for _, path := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, rec.Code)
		}
		assertAppSurfaceHeaders(t, rec.Header())
		if !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("GET %s did not serve injectFrontendConfig(decode($APP))", path)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / status = %d, want 405", rec.Code)
	}
}

// TestEmbeddedRootAPPIsSingleFile verifies the record's entry page is an
// entirely inline HTML document suitable for runtime extraction on each host.
func TestEmbeddedRootAPPIsSingleFile(t *testing.T) {
	html := string(decodedEmbeddedAppPage(t, "spaceaware"))
	for _, want := range []string{"<title>SpaceAware · Space Data Network</title>", "<div id=\"sa-root\"></div>", "</head>"} {
		if !strings.Contains(html, want) {
			t.Errorf("embedded SDN UI page missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"<script src=", `<link rel="stylesheet"`, "fonts.googleapis.com", "fonts.gstatic.com", "cdn.jsdelivr.net", "unpkg.com",
		"coi-serviceworker.js", "local-flatsql.worker-", "flatsql-qV6XKjJS.wasm",
	} {
		if strings.Contains(html, forbidden) {
			t.Errorf("embedded SDN UI page contains forbidden external reference %q", forbidden)
		}
	}
}
