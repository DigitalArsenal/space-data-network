package sdnapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	plugin "github.com/ipfs/kubo/plugin"

	"github.com/ipfs/kubo/sdn/sdnui"
)

func TestInitDefaults(t *testing.T) {
	p := &sdnAPIPlugin{}
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init(nil): %v", err)
	}
	if !p.enabled {
		t.Error("SDN API must be enabled by default")
	}
	if p.addr != DefaultAddr {
		t.Errorf("addr = %q, want default %q", p.addr, DefaultAddr)
	}
	if !isLoopbackAddr(p.addr) {
		t.Errorf("default addr %q must be loopback-bound", p.addr)
	}
}

func TestInitConfigOverride(t *testing.T) {
	env := &plugin.Environment{Config: map[string]interface{}{
		"Enabled": false,
		"Addr":    "127.0.0.1:6060",
	}}
	p := &sdnAPIPlugin{}
	if err := p.Init(env); err != nil {
		t.Fatalf("Init(env): %v", err)
	}
	if p.enabled {
		t.Error("Enabled=false must disable the plugin")
	}
	if p.addr != "127.0.0.1:6060" {
		t.Errorf("addr = %q, want 127.0.0.1:6060", p.addr)
	}
}

func TestLoopbackClassification(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:5020":    true,
		"[::1]:5020":        true,
		"localhost:5020":    true,
		"0.0.0.0:5020":      false,
		"[::]:5020":         false,
		"192.168.1.10:5020": false,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestImplementsPluginDaemonInternal(t *testing.T) {
	var _ plugin.PluginDaemonInternal = (*sdnAPIPlugin)(nil)
}

// The single loopback listener serves the console at "/" and the JSON API
// under /sdn/v1/ from one handler. The API subtree must win over the console
// catch-all, and the console must serve the app shell at the root.
func TestRootHandlerRoutesUIAndAPI(t *testing.T) {
	apiMarker := "SDN-API-MARKER"
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(apiMarker + " " + r.URL.Path))
	})
	credsMarker := "SDN-CREDS-MARKER"
	creds := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(credsMarker + " " + r.URL.Path))
	})
	runsMarker := "SDN-RUNS-MARKER"
	runs := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(runsMarker + " " + r.URL.Path))
	})
	nodeEPMMarker := "SDN-NODEEPM-MARKER"
	nodeEPM := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nodeEPMMarker + " " + r.URL.Path))
	})
	root := newRootHandler(api, sdnui.Handler(), creds, runs, nodeEPM)

	// API subtree reaches the API handler with the full path intact.
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/node", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), apiMarker) {
		t.Errorf("GET /sdn/v1/node -> code=%d body=%q, want API handler", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/sdn/v1/node") {
		t.Errorf("API handler received a rewritten path: %q", rec.Body.String())
	}

	// The exact node EPM export routes win over the /sdn/v1/ subtree and reach
	// the export handler, while the bare /sdn/v1/node route stays on the API.
	for _, path := range []string{"/sdn/v1/node/epm", "/sdn/v1/node/vcard", "/sdn/v1/node/qr"} {
		rec = httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if !strings.Contains(rec.Body.String(), nodeEPMMarker) {
			t.Errorf("GET %s -> body=%q, want node EPM handler", path, rec.Body.String())
		}
	}

	// The credential admin prefix wins over the /sdn/v1/ subtree and reaches the
	// guarded credential handler, not the read-only API.
	rec = httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/sdn/v1/admin/credentials/spacetrack", nil))
	if !strings.Contains(rec.Body.String(), credsMarker) {
		t.Errorf("PUT /sdn/v1/admin/credentials/spacetrack -> body=%q, want credentials handler", rec.Body.String())
	}

	// The runs prefix wins over the /sdn/v1/ subtree and reaches the runs handler.
	rec = httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/runs", nil))
	if !strings.Contains(rec.Body.String(), runsMarker) {
		t.Errorf("GET /sdn/v1/runs -> body=%q, want runs handler", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/runs/abc/objects", nil))
	if !strings.Contains(rec.Body.String(), runsMarker) {
		t.Errorf("GET /sdn/v1/runs/abc/objects -> body=%q, want runs handler", rec.Body.String())
	}

	// Root serves the console app shell.
	rec = httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / code = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / content-type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "app-shell") {
		t.Errorf("GET / did not serve the console app shell")
	}

	// Console assets are served, not routed to the API.
	rec = httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), apiMarker) {
		t.Errorf("GET /app.js -> code=%d, want console asset (not API)", rec.Code)
	}
}
