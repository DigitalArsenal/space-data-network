package flowrt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

// TestPaletteEndpointNoBaker asserts GET /api/v1/flows/palette serves the host
// capability node types even when no baker (flowcc toolchain) is attached, and
// that the modules list is present but empty. This is the palette a node with no
// staged toolchain shows the editor.
func TestPaletteEndpointNoBaker(t *testing.T) {
	cfg := flowconfig.FlowsConfig{Enabled: true, StoragePath: t.TempDir(), MaxMemoryPages: 512}
	mgr, err := NewFlowManager(cfg, plugins.New(), HandlerMap{})
	if err != nil {
		t.Fatalf("NewFlowManager: %v", err)
	}
	mux := http.NewServeMux()
	RegisterAPI(mux, mgr)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/flows/palette")
	if err != nil {
		t.Fatalf("GET palette: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("palette status = %d, want 200", resp.StatusCode)
	}
	var pal Palette
	if err := json.NewDecoder(resp.Body).Decode(&pal); err != nil {
		t.Fatalf("decode palette: %v", err)
	}
	if len(pal.Capabilities) == 0 {
		t.Fatal("palette has no host capabilities; expected the built-in IPFS/FlatSQL nodes")
	}
	if pal.Modules == nil {
		t.Fatal("palette modules must be a (possibly empty) array, not null")
	}
	if len(pal.Modules) != 0 {
		t.Fatalf("expected zero staged modules with no baker, got %d", len(pal.Modules))
	}
	// Every capability method must carry port arrays the editor can wire.
	for _, c := range pal.Capabilities {
		if c.Kind != "capability" {
			t.Errorf("capability %q has kind %q, want \"capability\"", c.PluginID, c.Kind)
		}
		if len(c.Methods) == 0 {
			t.Errorf("capability %q exposes no methods", c.PluginID)
		}
		for _, m := range c.Methods {
			if m.InputPorts == nil || m.OutputPorts == nil {
				t.Errorf("capability %q method %q has nil port arrays", c.PluginID, m.MethodID)
			}
		}
	}
}
