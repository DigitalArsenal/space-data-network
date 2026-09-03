package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStandardSchemaTextServesTheEngineTableBlock: the route hands a browser
// engine the node's own DDL for one standard — plain text, cacheable, with
// the engine table and file identifier in headers — and refuses names that
// are not routed standards.
func TestStandardSchemaTextServesTheEngineTableBlock(t *testing.T) {
	h, _ := newTestCoreAPIHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutesWithFlowMounts(mux, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/standards/CNP.fbs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "table CNP {") || !strings.Contains(body, "ID:string;") {
		t.Fatalf("body is not the CNP engine block:\n%s", body)
	}
	if strings.Contains(body, "table OMM {") {
		t.Fatalf("body carries a neighbouring table:\n%s", body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("X-SDN-Engine-Table"); got != "CNP" {
		t.Fatalf("X-SDN-Engine-Table = %q", got)
	}
	if got := rec.Header().Get("X-SDN-File-Id"); got != "$CNP" {
		t.Fatalf("X-SDN-File-Id = %q", got)
	}

	// HEAD carries the same headers and no body.
	head := httptest.NewRecorder()
	mux.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/api/v1/standards/OMM.fbs", nil))
	if head.Code != http.StatusOK || head.Header().Get("X-SDN-Engine-Table") != "OMM" || head.Body.Len() != 0 {
		t.Fatalf("HEAD OMM: status=%d table=%q body=%d", head.Code, head.Header().Get("X-SDN-Engine-Table"), head.Body.Len())
	}

	// The bare code resolves too; the list route is untouched.
	bare := httptest.NewRecorder()
	mux.ServeHTTP(bare, httptest.NewRequest(http.MethodGet, "/api/v1/standards/omm", nil))
	if bare.Code != http.StatusOK || !strings.Contains(bare.Body.String(), "table OMM {") {
		t.Fatalf("GET /api/v1/standards/omm: status=%d body=%s", bare.Code, bare.Body.String())
	}
	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/standards", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"standards"`) {
		t.Fatalf("GET /api/v1/standards: status=%d body=%s", list.Code, list.Body.String())
	}

	for path, want := range map[string]int{
		"/api/v1/standards/ZZZZ.fbs":        http.StatusNotFound,
		"/api/v1/standards/":                http.StatusBadRequest,
		"/api/v1/standards/x.fbs":           http.StatusBadRequest,
		"/api/v1/standards/..%2Fetc.fbs":    http.StatusBadRequest,
		"/api/v1/standards/TOOLONGCODE.fbs": http.StatusBadRequest,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Fatalf("GET %s: status=%d, want %d (%s)", path, rec.Code, want, rec.Body.String())
		}
	}
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/v1/standards/CNP.fbs", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: status=%d, want 405", post.Code)
	}
}
