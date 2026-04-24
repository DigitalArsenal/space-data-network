package adminui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewHostServesIndexAndAssetsUnderAdmin(t *testing.T) {
	t.Parallel()

	buildDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(buildDir, "index.html"), []byte("<!doctype html><html><body>admin-ui</body></html>"), 0o644); err != nil {
		t.Fatalf("write index.html failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(buildDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "assets", "main.js"), []byte("console.log('admin')"), 0o644); err != nil {
		t.Fatalf("write asset failed: %v", err)
	}

	handler, err := NewHost(buildDir)
	if err != nil {
		t.Fatalf("NewHost failed: %v", err)
	}

	t.Run("serves index at mount root", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/admin/", nil)

		http.StripPrefix("/admin", handler).ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, "window.__SDN_CONFIG__") {
			t.Fatalf("body = %q, want injected SDN runtime config", body)
		}
		if !strings.Contains(body, "serverBaseUrl:window.location.origin") {
			t.Fatalf("body = %q, want same-origin server runtime config", body)
		}
		if !strings.Contains(body, "<body>admin-ui</body>") {
			t.Fatalf("body = %q, want index.html contents preserved", body)
		}
	})

	t.Run("serves static assets", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/admin/assets/main.js", nil)

		http.StripPrefix("/admin", handler).ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		if got := recorder.Body.String(); got != "console.log('admin')" {
			t.Fatalf("body = %q, want asset contents", got)
		}
	})
}
