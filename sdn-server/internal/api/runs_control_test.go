package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newRunsControlTestMux(t *testing.T, toggle ModuleScheduleToggler) (*http.ServeMux, *RunsControlHandler, string) {
	t.Helper()
	store := newDataAPITestStore(t)
	dir, err := os.MkdirTemp("", "runs-control-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	statePath := filepath.Join(dir, "runs-control.json")
	h := NewRunsControlHandlerWithStatePath(store, statePath, toggle)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	// The clear test needs records too.
	NewDataQueryHandler(store).RegisterRoutes(mux)
	return mux, h, statePath
}

func doJSON(t *testing.T, mux *http.ServeMux, method, target, body string) (int, map[string]interface{}) {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestRunsControlStopFlagLifecycle proves the cooperative stop-flag contract:
// admin POST sets a persisted flag, the anonymous flags read reflects it, and
// clear=true removes it.
func TestRunsControlStopFlagLifecycle(t *testing.T) {
	mux, _, statePath := newRunsControlTestMux(t, nil)

	// Unknown batch: honest stop=false.
	code, body := doJSON(t, mux, http.MethodGet, "/api/v1/runs/flags?batch_id=batch-x", "")
	if code != http.StatusOK || body["stop"] != false {
		t.Fatalf("initial flags read = %d %v", code, body)
	}

	// Set the flag (run_token alias must work too).
	code, body = doJSON(t, mux, http.MethodPost, "/api/v1/admin/runs/stop?run_token=batch-x&schema=OMM.fbs&source_name=spacex-starlink", "")
	if code != http.StatusOK || body["stop"] != true {
		t.Fatalf("stop set = %d %v", code, body)
	}
	if body["requested_at"] == nil {
		t.Fatalf("stop response missing requested_at: %v", body)
	}

	code, body = doJSON(t, mux, http.MethodGet, "/api/v1/runs/flags?batch_id=batch-x", "")
	if code != http.StatusOK || body["stop"] != true {
		t.Fatalf("flags read after stop = %d %v", code, body)
	}

	// Persisted to disk (survives a handler reload).
	data, err := os.ReadFile(statePath)
	if err != nil || !strings.Contains(string(data), "batch-x") {
		t.Fatalf("state file missing flag: err=%v content=%s", err, data)
	}

	// Clear the flag.
	code, body = doJSON(t, mux, http.MethodPost, "/api/v1/admin/runs/stop?batch_id=batch-x&clear=true", "")
	if code != http.StatusOK || body["stop"] != false {
		t.Fatalf("stop clear = %d %v", code, body)
	}
}

// TestRunsControlProvidersRoundTripAndModuleToggle proves the provider
// selection persists across handler restarts, the anonymous read serves it,
// and the module toggler is invoked with honest per-provider results.
func TestRunsControlProvidersRoundTripAndModuleToggle(t *testing.T) {
	toggled := map[string]bool{}
	toggle := func(ctx context.Context, key string, enabled bool) (int, error) {
		toggled[key] = enabled
		if key == "glonass" {
			return 2, nil // pretend a module-scheduled lane with 2 cron methods
		}
		return 0, nil // external-runner lane
	}
	mux, _, statePath := newRunsControlTestMux(t, toggle)

	// Default: empty selection, default enabled.
	code, body := doJSON(t, mux, http.MethodGet, "/api/v1/runs/providers", "")
	if code != http.StatusOK || body["default"] != "enabled" {
		t.Fatalf("default providers read = %d %v", code, body)
	}

	// Update the selection.
	code, body = doJSON(t, mux, http.MethodPost, "/api/v1/admin/runs/providers",
		`{"providers":{"spacex-starlink":true,"kuiper":false,"glonass":false}}`)
	if code != http.StatusOK {
		t.Fatalf("providers POST = %d %v", code, body)
	}
	ms, _ := body["module_schedules"].(map[string]interface{})
	if ms == nil {
		t.Fatalf("providers POST missing module_schedules: %v", body)
	}
	if got := ms["glonass"]; got != "updated 2 schedule(s)" {
		t.Fatalf("glonass module result = %v", got)
	}
	if got := ms["kuiper"]; got != "no module schedule (external runner lane)" {
		t.Fatalf("kuiper module result = %v", got)
	}
	if toggled["kuiper"] != false || toggled["glonass"] != false || toggled["spacex-starlink"] != true {
		t.Fatalf("toggler saw wrong states: %v", toggled)
	}

	// Anonymous read reflects it.
	code, body = doJSON(t, mux, http.MethodGet, "/api/v1/runs/providers", "")
	if code != http.StatusOK {
		t.Fatalf("providers read = %d", code)
	}
	providers, _ := body["providers"].(map[string]interface{})
	if providers["kuiper"] != false || providers["spacex-starlink"] != true {
		t.Fatalf("providers read state wrong: %v", providers)
	}

	// Survives a restart: a NEW handler on the same state file serves it.
	store := newDataAPITestStore(t)
	h2 := NewRunsControlHandlerWithStatePath(store, statePath, nil)
	mux2 := http.NewServeMux()
	h2.RegisterRoutes(mux2)
	code, body = doJSON(t, mux2, http.MethodGet, "/api/v1/runs/providers", "")
	providers, _ = body["providers"].(map[string]interface{})
	if code != http.StatusOK || providers["kuiper"] != false {
		t.Fatalf("providers not persisted across restart: %d %v", code, providers)
	}
}

// TestRunsControlClearBatchEndToEnd proves the admin clear route deletes one
// batch (orphans only — shared records survive) and the stats/index surfaces
// reflect it; a pending stop flag for the cleared batch is dropped.
func TestRunsControlClearBatchEndToEnd(t *testing.T) {
	store := newDataAPITestStore(t)
	dir, err := os.MkdirTemp("", "runs-clear-e2e-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	h := NewRunsControlHandlerWithStatePath(store, filepath.Join(dir, "runs-control.json"), nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	NewDataQueryHandler(store).RegisterRoutes(mux)

	// batch-stale: 2 records. batch-live: 1 record (distinct payloads).
	storeDataAPITestOMMWithBatch(t, store, 40901, "STALE-A", "2026-05-05", "batch-stale")
	storeDataAPITestOMMWithBatch(t, store, 40902, "STALE-B", "2026-05-06", "batch-stale")
	storeDataAPITestOMMWithBatch(t, store, 40903, "LIVE-A", "2026-05-07", "batch-live")

	// Arm a stop flag for the batch we are about to clear.
	code, _ := doJSON(t, mux, http.MethodPost, "/api/v1/admin/runs/stop?batch_id=batch-stale", "")
	if code != http.StatusOK {
		t.Fatalf("arm stop flag = %d", code)
	}

	code, body := doJSON(t, mux, http.MethodPost,
		"/api/v1/admin/runs/clear?schema=OMM.fbs&provider_id=space-data-network-02&source_name=celestrak-gp&batch_id=batch-stale", "")
	if code != http.StatusOK {
		t.Fatalf("clear = %d %v", code, body)
	}
	if body["tags_deleted"] != float64(2) || body["records_deleted"] != float64(2) {
		t.Fatalf("clear counts wrong: %v", body)
	}

	// Index now only serves the live batch's record.
	code, body = doJSON(t, mux, http.MethodGet, "/api/v1/data/index?schema=OMM.fbs", "")
	if code != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("index after clear = %d %v", code, body)
	}

	// The cleared batch's stop flag is dropped (moot).
	code, body = doJSON(t, mux, http.MethodGet, "/api/v1/runs/flags?batch_id=batch-stale", "")
	if code != http.StatusOK || body["stop"] != false {
		t.Fatalf("stop flag survived clear: %d %v", code, body)
	}

	// Missing params rejected.
	code, _ = doJSON(t, mux, http.MethodPost, "/api/v1/admin/runs/clear?schema=OMM.fbs", "")
	if code != http.StatusBadRequest {
		t.Fatalf("clear without scope = %d, want 400", code)
	}
}
