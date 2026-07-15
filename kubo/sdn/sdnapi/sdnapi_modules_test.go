package sdnapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/plugins"
	"github.com/ipfs/kubo/sdn/sdnapi"
	"github.com/ipfs/kubo/sdn/sdncron"
)

// cronMod is a minimal sdncron.CronModule used to register a real module into a
// real scheduler so the settings endpoints run against the production code path.
type cronMod struct{ id string }

func (c cronMod) ID() string { return c.id }

func (c cronMod) CronMethods() []plugins.CronMethodSpec {
	return []plugins.CronMethodSpec{{
		Method:          "poll",
		Description:     "poll for new data",
		DefaultInterval: "1h",
		Input:           "none",
		Output:          "json",
	}}
}

func (c cronMod) InvokeCron(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return []byte("{}"), nil
}

// newModulesTestHandler builds a handler backed by a real scheduler with one
// registered module, plus the config dir so tests can assert persistence.
func newModulesTestHandler(t *testing.T) (http.Handler, *sdncron.Scheduler, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := sdncron.NewConfigStore(dir)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	sched := sdncron.NewScheduler(store, nil)
	if err := sched.Register(sdncron.Registration{
		Module:  cronMod{id: "demo-mod"},
		Name:    "Demo Module",
		Version: "2.3.4",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	sched.Start(context.Background())
	t.Cleanup(sched.Stop)

	h := sdnapi.NewHandler(sdnapi.Deps{
		Modules: func() sdnapi.ModuleAdmin { return sched },
	})
	return h, sched, dir
}

// TestModulesList: GET /sdn/v1/modules lists the registered module with its
// timers (effective interval) and config.
func TestModulesList(t *testing.T) {
	h, _, _ := newModulesTestHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/modules", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /sdn/v1/modules status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var mods []sdncron.ModuleView
	if err := json.Unmarshal(rec.Body.Bytes(), &mods); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(mods) != 1 {
		t.Fatalf("modules = %d, want 1: %+v", len(mods), mods)
	}
	m := mods[0]
	if m.ID != "demo-mod" || m.Name != "Demo Module" || m.Version != "2.3.4" {
		t.Fatalf("module identity = %+v", m)
	}
	if !m.Running {
		t.Fatalf("expected running module, got %+v", m)
	}
	if len(m.Timers) != 1 || m.Timers[0].ID != "poll" || m.Timers[0].IntervalMs != 3_600_000 {
		t.Fatalf("timers = %+v, want [{poll 3600000}]", m.Timers)
	}
	if m.Config == nil {
		t.Fatalf("expected a non-nil config object")
	}
}

// TestModuleConfigPutUpdatesRescheduleAndPersists: PUT config updates the
// interval, persists it to the home-dir file, and the change is reflected in the
// effective timer interval (which is what the live reschedule uses).
func TestModuleConfigPutUpdatesRescheduleAndPersists(t *testing.T) {
	h, _, dir := newModulesTestHandler(t)

	body := strings.NewReader(`{"interval_ms": 1234, "custom_input": "hello"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/sdn/v1/modules/demo-mod/config", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var applied map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil {
		t.Fatalf("decode applied: %v", err)
	}
	if applied["interval_ms"] != float64(1234) {
		t.Fatalf("applied interval_ms = %v, want 1234", applied["interval_ms"])
	}
	if applied["custom_input"] != "hello" {
		t.Fatalf("applied config dropped the opaque module input: %+v", applied)
	}

	// Persisted to <dir>/demo-mod.json.
	path := dir + "/demo-mod.json"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected persisted config at %s: %v", path, err)
	}
	if !strings.Contains(string(data), "1234") || !strings.Contains(string(data), "custom_input") {
		t.Fatalf("persisted file missing expected keys: %s", string(data))
	}

	// The effective timer interval now reflects the override (reschedule input).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/modules", nil))
	var mods []sdncron.ModuleView
	_ = json.Unmarshal(rec.Body.Bytes(), &mods)
	if len(mods) != 1 || mods[0].Timers[0].IntervalMs != 1234 {
		t.Fatalf("effective timer interval not updated: %+v", mods)
	}
}

// TestModuleConfigGet returns a module's stored config.
func TestModuleConfigGet(t *testing.T) {
	h, _, _ := newModulesTestHandler(t)

	// Seed a config via PUT, then read it back with GET.
	put := httptest.NewRecorder()
	h.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/sdn/v1/modules/demo-mod/config",
		strings.NewReader(`{"interval_ms": 500}`)))
	if put.Code != http.StatusOK {
		t.Fatalf("seed PUT status = %d", put.Code)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/modules/demo-mod/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg["interval_ms"] != float64(500) {
		t.Fatalf("config interval_ms = %v, want 500", cfg["interval_ms"])
	}
}

// TestModuleConfigPutMalformed: a body that is not a JSON object is a 400.
func TestModuleConfigPutMalformed(t *testing.T) {
	h, _, _ := newModulesTestHandler(t)
	for _, body := range []string{`[1,2,3]`, `not json`, `42`} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/sdn/v1/modules/demo-mod/config",
			strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT malformed %q status = %d, want 400; body=%s", body, rec.Code, rec.Body.String())
		}
	}
}

// TestModuleConfigPutInvalidInterval: a well-formed object with a bad interval is
// a 400.
func TestModuleConfigPutInvalidInterval(t *testing.T) {
	h, _, _ := newModulesTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/sdn/v1/modules/demo-mod/config",
		strings.NewReader(`{"interval_ms": 0}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT interval_ms=0 status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestModuleConfigUnknownModule: GET/PUT for an unknown module id is a 404.
func TestModuleConfigUnknownModule(t *testing.T) {
	h, _, _ := newModulesTestHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/modules/nope/config", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown config status = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/sdn/v1/modules/nope/config",
		strings.NewReader(`{"interval_ms": 10}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT unknown config status = %d, want 404", rec.Code)
	}
}

// TestModulesEmptyWhenRuntimeDown: with no Modules dep the list is empty and the
// config endpoints report unavailable rather than crashing.
func TestModulesEmptyWhenRuntimeDown(t *testing.T) {
	h := sdnapi.NewHandler(sdnapi.Deps{}) // no Modules dep

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/modules", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("GET /sdn/v1/modules with no runtime = %d %q, want 200 []", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sdn/v1/modules/x/config", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET config with no runtime = %d, want 503", rec.Code)
	}
}
