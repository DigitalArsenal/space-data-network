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

// TestRootDashboard locks the root surface: "/" serves the built SDN Node
// Status $APP homepage (single self-contained file, wired to /ws/status +
// /fonts) under its build-generated appSurfaceCSP-grade CSP; unknown paths 404,
// non-GET/HEAD 405.
func TestRootDashboard(t *testing.T) {
	if len(dashboardHTML) == 0 {
		t.Fatal("embedded dashboard.html is empty — run sdn-js/dashboard/build-dashboard.mjs")
	}
	handler := makeRootHandler()

	for _, path := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s content-type = %q, want text/html", path, ct)
		}
		body := rec.Body.String()
		for _, needle := range []string{"Space Data Network", "/ws/status", "/fonts/"} {
			if !strings.Contains(body, needle) {
				t.Errorf("GET %s dashboard body missing %q", path, needle)
			}
		}
		got := rec.Header().Get("Content-Security-Policy")
		if got != dashboardCSP() {
			t.Errorf("dashboard CSP header = %q, want %q", got, dashboardCSP())
		}
		for _, want := range []string{"script-src 'self'", "'wasm-unsafe-eval'", "'sha256-", "connect-src 'self' wss:", "font-src 'self' data:"} {
			if !strings.Contains(got, want) {
				t.Errorf("dashboard CSP missing %q: %s", want, got)
			}
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/console", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /console status = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / status = %d, want 405", rec.Code)
	}
}

// TestDashboardFonts locks the self-hosted font surface: each /fonts/*.woff2
// path serves the embedded font (GET/HEAD), path traversal / unknown names 404,
// non-GET/HEAD 405.
func TestDashboardFonts(t *testing.T) {
	handler := makeFontsHandler()

	for _, name := range []string{
		"chakra-400.woff2", "chakra-500.woff2", "chakra-600.woff2", "chakra-700.woff2",
		"plex-400.woff2", "plex-500.woff2", "plex-600.woff2",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/"+name, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /fonts/%s status = %d, want 200", name, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "font/woff2" {
			t.Errorf("GET /fonts/%s content-type = %q, want font/woff2", name, ct)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("GET /fonts/%s served empty body", name)
		}
	}

	for _, bad := range []string{"/fonts/", "/fonts/../secret", "/fonts/app.js", "/fonts/nope.woff2"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", bad, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/fonts/plex-400.woff2", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /fonts/plex-400.woff2 status = %d, want 405", rec.Code)
	}
}

func TestWalletCallbackIsExactAndNoStore(t *testing.T) {
	if len(walletCallbackHTML) == 0 {
		t.Fatal("embedded wallet callback is empty")
	}

	// Exercise the same outer security middleware used by the admin server. The
	// callback-specific policy must override its generic referrer policy.
	handler := adminSecurityMiddleware(makeRootHandler(), "", func(string, string) bool { return false })

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
			}
		})
	}
}

func TestWalletCallbackRejectsOtherMethods(t *testing.T) {
	handler := adminSecurityMiddleware(makeRootHandler(), "", func(string, string) bool { return false })

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
	mux.Handle("/", makeRootHandler())

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

// TestEmbeddingAssets locks the /embedding/* semantic-search asset surface:
// flat namespace, extension allow-list, correct media types, fail-open 404s
// when the staged dir or a file is absent, and GET/HEAD only. This is the
// same staged-file contract as the geoip mmdb — a node without staged assets
// still serves the dashboard, which keeps substring search.
func TestEmbeddingAssets(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"model.onnx":                  "application/octet-stream",
		"vocab.txt":                   "text/plain; charset=utf-8",
		"ort-wasm-simd-threaded.wasm": "application/wasm",
		"ort-wasm-simd-threaded.mjs":  "text/javascript; charset=utf-8",
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("asset-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	handler := makeEmbeddingHandler(dir)

	for name, wantType := range files {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/embedding/"+name, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /embedding/%s status = %d, want 200", name, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != wantType {
			t.Errorf("GET /embedding/%s content-type = %q, want %q", name, ct, wantType)
		}
		if rec.Body.String() != "asset-bytes" {
			t.Errorf("GET /embedding/%s body = %q", name, rec.Body.String())
		}
	}

	// HEAD works for the dashboard's availability probe.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/embedding/model.onnx", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD /embedding/model.onnx status = %d, want 200", rec.Code)
	}

	for _, bad := range []string{
		"/embedding/",                  // no name
		"/embedding/missing.onnx",      // absent file
		"/embedding/model.html",        // extension not allow-listed
		"/embedding/sub/model.onnx",    // nested path
		"/embedding/..%2Fmodel.onnx",   // encoded traversal
		"/embedding/.hidden.txt",       // dotfile
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", bad, rec.Code)
		}
	}

	// Unconfigured/absent dir fail-opens to 404 (never 500).
	for _, h := range []http.Handler{makeEmbeddingHandler(""), makeEmbeddingHandler(filepath.Join(dir, "nope"))} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/embedding/model.onnx", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("fail-open status = %d, want 404", rec.Code)
		}
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/embedding/model.onnx", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

// TestIdentityDownloads locks the anonymous /identity/* surface the node
// modal uses: <peerId>.vcf serves the vCard (self via the EPM service, peers
// via the registry chain), <peerId>.epm serves the signed serialized EPM
// record, unknown ids/extensions 404, and only GET/HEAD are allowed.
func TestIdentityDownloads(t *testing.T) {
	handler := makeIdentityHandler(identitySource{
		SelfID:    "16UiuSelf",
		SelfVCard: func() (string, error) { return "BEGIN:VCARD\r\nFN:Self\r\nEND:VCARD\r\n", nil },
		SelfEPM:   func() []byte { return []byte("self-epm-bytes") },
		PeerVCard: func(id string) (string, bool) {
			if id == "16UiuPeerA" {
				return "BEGIN:VCARD\r\nFN:Peer A\r\nEND:VCARD\r\n", true
			}
			return "", false
		},
		PeerEPM: func(id string) ([]byte, bool) {
			if id == "16UiuPeerA" {
				return []byte("peer-epm-bytes"), true
			}
			return nil, false
		},
	})

	cases := []struct {
		path     string
		wantCode int
		wantType string
		wantBody string
	}{
		{"/identity/16UiuSelf.vcf", http.StatusOK, "text/vcard; charset=utf-8", "FN:Self"},
		{"/identity/16UiuSelf.epm", http.StatusOK, "application/x-flatbuffers", "self-epm-bytes"},
		{"/identity/16UiuPeerA.vcf", http.StatusOK, "text/vcard; charset=utf-8", "FN:Peer A"},
		{"/identity/16UiuPeerA.epm", http.StatusOK, "application/x-flatbuffers", "peer-epm-bytes"},
		{"/identity/16UiuUnknown.vcf", http.StatusNotFound, "", ""},
		{"/identity/16UiuUnknown.epm", http.StatusNotFound, "", ""},
		{"/identity/16UiuPeerA.exe", http.StatusNotFound, "", ""},
		{"/identity/16UiuPeerA", http.StatusNotFound, "", ""},
		{"/identity/", http.StatusNotFound, "", ""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.wantCode {
			t.Errorf("GET %s status = %d, want %d", tc.path, rec.Code, tc.wantCode)
			continue
		}
		if tc.wantType != "" && rec.Header().Get("Content-Type") != tc.wantType {
			t.Errorf("GET %s content-type = %q, want %q", tc.path, rec.Header().Get("Content-Type"), tc.wantType)
		}
		if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("GET %s body missing %q", tc.path, tc.wantBody)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/identity/16UiuSelf.vcf", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD status/body = %d/%d, want 200/empty", rec.Code, rec.Body.Len())
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/identity/16UiuSelf.vcf", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

// TestIdentityQRDownloads locks the /identity/<peerId>.qr.vcf variant: the
// compact scannable card (self via GetNodeQRVCard, peers via their EPM or
// the minimal peer-alias card), 404 when unknown.
func TestIdentityQRDownloads(t *testing.T) {
	handler := makeIdentityHandler(identitySource{
		SelfID:      "16UiuSelf",
		SelfQRVCard: func() (string, error) { return "BEGIN:VCARD\r\nFN:Self QR\r\nEND:VCARD\r\n", nil },
		PeerQRVCard: func(id string) (string, bool) {
			if id == "16UiuPeerA" {
				return "BEGIN:VCARD\r\nFN:Peer QR\r\nEMAIL;type=INTERNET;type=peer:16UiuPeerA@peer.spacedatanetwork.org\r\nEND:VCARD\r\n", true
			}
			return "", false
		},
	})

	for path, want := range map[string]string{
		"/identity/16UiuSelf.qr.vcf":  "FN:Self QR",
		"/identity/16UiuPeerA.qr.vcf": "@peer.spacedatanetwork.org",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s body missing %q", path, want)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/vcard; charset=utf-8" {
			t.Errorf("GET %s content-type = %q", path, ct)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/identity/16UiuUnknown.qr.vcf", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown qr.vcf status = %d, want 404", rec.Code)
	}
}
