package tlsmgr

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagedTLSRedirectsHTTPRootToHTTPS(t *testing.T) {
	redirect := NewRedirectHandler("https://sdn.example")
	req := httptest.NewRequest(http.MethodGet, "http://sdn.example/", nil)
	rec := httptest.NewRecorder()

	redirect.ServeHTTP(rec, req)

	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPermanentRedirect)
	}
	if got := rec.Header().Get("Location"); got != "https://sdn.example/" {
		t.Fatalf("Location = %q, want %q", got, "https://sdn.example/")
	}
}

func TestManagedTLSRedirectLeavesChallengePathUnredirected(t *testing.T) {
	redirect := NewRedirectHandler("https://sdn.example")
	req := httptest.NewRequest(http.MethodGet, "http://sdn.example/.well-known/acme-challenge/token", nil)
	rec := httptest.NewRecorder()

	redirect.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want empty", got)
	}
}
