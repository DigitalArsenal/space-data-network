package sdnapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	plugin "github.com/ipfs/kubo/plugin"

	sdnapihttp "github.com/ipfs/kubo/sdn/sdnapi"
	"github.com/ipfs/kubo/sdn/sdnflows"
)

func TestFlowInstallerImplementsArtifactRuntimeNodeReader(t *testing.T) {
	var _ sdnapihttp.ArtifactRuntimeNodeReader = (*sdnflows.Installer)(nil)
}

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
	nodeEPMMarker := "SDN-NODEEPM-MARKER"
	nodeEPM := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nodeEPMMarker + " " + r.URL.Path))
	})
	flowsMarker := "SDN-FLOWS-MARKER"
	flows := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(flowsMarker + " " + r.URL.Path))
	})
	root := newRootHandler(api, creds, nodeEPM, flows)

	// The flow-platform subtree wins over the "/" console catch-all and reaches
	// the flow handler with the full path intact (the editor $APP posts here).
	for _, path := range []string{"/api/v1/flows/bake", "/api/v1/flows/palette"} {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if !strings.Contains(rec.Body.String(), flowsMarker) || !strings.Contains(rec.Body.String(), path) {
			t.Errorf("GET %s -> body=%q, want flows handler with intact path", path, rec.Body.String())
		}
	}

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

	// Unrecognized paths remain on the generic API subtree; the root router has
	// no application-specific read or control handlers.
	for _, path := range []string{
		"/sdn/v1/jobs",
		"/sdn/v1/modules/example-module/run/start",
	} {
		rec = httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if !strings.Contains(rec.Body.String(), apiMarker) {
			t.Errorf("GET %s -> body=%q, want generic API handler", path, rec.Body.String())
		}
	}

	// The operator console was retired: "/" and former console asset paths are
	// no longer served by this plugin — they 404 from the mux (the node's
	// user-facing homepage is served by the sdn-server daemon, not here).
	for _, path := range []string{"/", "/app.js", "/styles.css", "/index.html"} {
		rec = httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s code = %d, want 404 (console retired)", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), apiMarker) {
			t.Errorf("GET %s leaked to the API handler", path)
		}
	}
}
