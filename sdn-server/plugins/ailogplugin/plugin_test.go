package ailogplugin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegisterRoutesMountsDashboardUnderAPIPrefix locks in gap B10.1's fix:
// the dashboard must be reachable at DashboardPath (which lives under /api/,
// so the top-level auth wall in cmd/spacedatanetwork/main.go applies
// RequireAuth to it) and the old hardcoded-UUID-under-/diag/ path must no
// longer serve anything.
func TestRegisterRoutesMountsDashboardUnderAPIPrefix(t *testing.T) {
	if !strings.HasPrefix(DashboardPath, "/api/") {
		t.Fatalf("DashboardPath = %q, want a path under /api/ so the auth wall's isAPIOrPlugin check covers it", DashboardPath)
	}

	p := New()
	p.LogEntry(QueryEntry{IP: "203.0.113.5", Provider: "test-provider", Model: "test-model", Query: "SELECT 1"})

	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, DashboardPath, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d", DashboardPath, rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "203.0.113.5") {
		t.Fatalf("dashboard body did not contain logged entry IP: %s", rec.Body.String())
	}
}

// TestRegisterRoutesNoLongerServesLegacyUUIDPath locks in that the previous
// "/diag/<hardcoded-uuid>" route (a static shared secret, not
// authentication) no longer serves the dashboard at all — the mux has no
// handler for it, so it 404s regardless of auth wall behavior.
func TestRegisterRoutesNoLongerServesLegacyUUIDPath(t *testing.T) {
	p := New()
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/diag/eaf6c52a-40f1-462d-aa22-b22f015b74f2", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("legacy UUID diag path status = %d, want %d (not registered)", rec.Code, http.StatusNotFound)
	}
}

// TestUIDescriptorURLMatchesDashboardPath ensures the plugin's own UI link
// emission stays in sync with the actual registered route.
func TestUIDescriptorURLMatchesDashboardPath(t *testing.T) {
	p := New()
	if got := p.UIDescriptor().URL; got != DashboardPath {
		t.Fatalf("UIDescriptor().URL = %q, want %q", got, DashboardPath)
	}
}

// TestHandleDashboardRejectsNonGET locks in the existing method restriction
// survives the route move.
func TestHandleDashboardRejectsNonGET(t *testing.T) {
	p := New()
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, DashboardPath, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %s = %d, want %d", DashboardPath, rec.Code, http.StatusMethodNotAllowed)
	}
}
