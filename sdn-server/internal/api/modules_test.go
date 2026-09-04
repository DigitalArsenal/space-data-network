package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	sdspmm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PMM"

	"github.com/spacedatanetwork/sdn-server/plugins"
)

func TestModulesLaneDecodesRuntimeSnapshotAsPMM(t *testing.T) {
	h := NewModulesHandler(func() plugins.RuntimeSnapshot {
		return plugins.RuntimeSnapshot{
			GeneratedAt: "2026-09-04T12:00:00Z",
			Modules: []plugins.RuntimeModuleEntry{
				{
					ID: "zeta", Version: "2.0.0", Status: "error", StatusMessage: "startup failed",
					Manifest: &plugins.RuntimeModuleManifest{Name: "Zeta", PluginID: "zeta", PluginFamily: "Analysis"},
				},
				{
					ID: "alpha", Version: "1.2.3", Status: "running",
					Manifest: &plugins.RuntimeModuleManifest{Name: "Alpha", PluginID: "alpha", PluginFamily: "DataSource"},
				},
			},
		}
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ModulesPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET modules = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(StreamSchemaHeader); got != ModulesSchemaName {
		t.Fatalf("%s = %q, want %q", StreamSchemaHeader, got, ModulesSchemaName)
	}
	frames, err := SplitFrames(rec.Body.Bytes())
	if err != nil || len(frames) != 1 {
		t.Fatalf("frames = %d, err=%v", len(frames), err)
	}
	if !sdspmm.SizePrefixedPMMBufferHasIdentifier(frames[0]) {
		t.Fatal("module lane frame is not size-prefixed $PMM")
	}
	manifest := sdspmm.GetSizePrefixedRootAsPMM(frames[0], 0)
	if manifest.MODULESLength() != 2 {
		t.Fatalf("MODULES length = %d", manifest.MODULESLength())
	}
	var alpha, zeta sdspmm.PMMModuleEntry
	if !manifest.MODULES(&alpha, 0) || !manifest.MODULES(&zeta, 1) {
		t.Fatal("PMM entries could not be decoded")
	}
	if got := string(alpha.MODULE_ID()); got != "alpha" {
		t.Fatalf("first module = %q, want stable alpha-first order", got)
	}
	if got := string(alpha.NAME()); got != "Alpha" {
		t.Fatalf("alpha NAME = %q", got)
	}
	if got := string(alpha.VERSION()); got != "1.2.3" {
		t.Fatalf("alpha VERSION = %q", got)
	}
	if got := alpha.PLUGIN_TYPE().String(); got != "DataSource" {
		t.Fatalf("alpha kind = %q", got)
	}
	if got := string(alpha.DESCRIPTION()); got != "runtime-state=running" {
		t.Fatalf("alpha runtime state = %q", got)
	}
	if !alpha.DEFAULT_ENABLED() {
		t.Fatal("running module was not marked enabled")
	}
	if got := string(zeta.DESCRIPTION()); got != "runtime-state=error; startup failed" {
		t.Fatalf("zeta runtime state = %q", got)
	}
}
