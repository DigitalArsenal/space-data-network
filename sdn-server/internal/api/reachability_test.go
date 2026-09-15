package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The point of the endpoint is that an operator behind a NAT can see the
// verdict the node already computed. Before this the snapshot existed only in
// memory: node.Reachability() had no caller anywhere in the tree.
func TestReachabilityHandlerServesTheSnapshot(t *testing.T) {
	want := map[string]any{
		"verdict":   "relayed",
		"autonat":   "private",
		"reachable": []any{},
	}
	rec := httptest.NewRecorder()
	ReachabilityHandler(func() any { return want }).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ReachabilityStatusPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rec.Body)
	}
	if got["verdict"] != "relayed" || got["autonat"] != "private" {
		t.Fatalf("snapshot did not round-trip: %v", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("reachability must not be cached; it changes with the network")
	}
}

// Boot is a real state, not an error: the host comes up after the HTTP surface.
func TestReachabilityHandlerBeforeTheHostIsUp(t *testing.T) {
	rec := httptest.NewRecorder()
	ReachabilityHandler(nil).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ReachabilityStatusPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET with no provider = %d, want 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["verdict"] != "unknown" {
		t.Fatalf("verdict = %v, want unknown", got["verdict"])
	}
}

func TestReachabilityHandlerRejectsWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	ReachabilityHandler(func() any { return map[string]any{} }).
		ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ReachabilityStatusPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}
