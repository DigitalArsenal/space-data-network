package sdnui_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/sdnui"
)

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The console root returns the app shell: 200, text/html, containing the design
// rail markup and all six screen names (including the new Modules screen), and
// it references the /sdn/v1 API.
func TestServesAppShell(t *testing.T) {
	rec := get(t, sdnui.Handler(), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	// The design shell: the collapsible left icon rail + the brand.
	for _, needle := range []string{"app-shell", "sdn-rail", "nav-i", "Space Data Network"} {
		if !strings.Contains(body, needle) {
			t.Errorf("app shell HTML missing %q", needle)
		}
	}
	// Every rail screen label, uppercase per the design (Chakra Petch labels).
	for _, screen := range []string{"NODE", "PEERS", "DATA", "CHANNELS", "APPS", "MODULES"} {
		if !strings.Contains(body, ">"+screen+"<") {
			t.Errorf("app shell HTML missing rail screen name %q", screen)
		}
	}
	if !strings.Contains(body, "/sdn/v1") {
		t.Errorf("app shell HTML does not reference the /sdn/v1 API")
	}
}

func TestServesAssets(t *testing.T) {
	cases := map[string]string{
		"/styles.css":             "text/css",
		"/app.js":                 "text/javascript",
		"/fonts/chakra-600.woff2": "font/woff2",
		"/fonts/plex-400.woff2":   "font/woff2",
	}
	for path, wantCT := range cases {
		rec := get(t, sdnui.Handler(), http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, wantCT) {
			t.Errorf("GET %s content-type = %q, want %s", path, ct, wantCT)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("GET %s served empty body", path)
		}
	}
}

// The JS actually wires the screens to the live endpoints, including the new
// Modules screen and its mutating config PUT.
func TestJSWiresLiveEndpoints(t *testing.T) {
	rec := get(t, sdnui.Handler(), http.MethodGet, "/app.js")
	js := rec.Body.String()
	for _, endpoint := range []string{"/node", "/peers", "/data/sources", "/data?source=", "/channels", "/apps", "/modules", "/config", "API_BASE = '/sdn/v1'"} {
		if !strings.Contains(js, endpoint) {
			t.Errorf("app.js does not reference %q", endpoint)
		}
	}
	// The Modules settings drawer saves a schedule via an HTTP PUT.
	if !strings.Contains(js, "'PUT'") {
		t.Errorf("app.js does not issue a PUT for the module config save")
	}
}

// Self-containment gate: NO embedded asset may reference an external origin.
// A same-origin console must fetch only relative /sdn/v1/* paths.
func TestNoExternalOrigins(t *testing.T) {
	extURL := regexp.MustCompile(`https?://`)
	err := fs.WalkDir(sdnui.Assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(sdnui.Assets, p)
		if err != nil {
			return err
		}
		if loc := extURL.FindIndex(b); loc != nil {
			start := loc[0] - 20
			if start < 0 {
				start = 0
			}
			end := loc[1] + 40
			if end > len(b) {
				end = len(b)
			}
			t.Errorf("embedded asset %s contains an external-origin URL near: %q", p, string(b[start:end]))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk assets: %v", err)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	for _, path := range []string{"/nope", "/assets/index.html", "/../etc", "/index.html"} {
		rec := get(t, sdnui.Handler(), http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, rec.Code)
		}
	}
}

func TestNonGetIs404(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := get(t, sdnui.Handler(), method, "/")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s / status = %d, want 404", method, rec.Code)
		}
	}
}
