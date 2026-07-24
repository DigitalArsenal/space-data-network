package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
