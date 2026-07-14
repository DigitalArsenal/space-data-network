package main

import (
	"net/http"
	"testing"
)

// TestRunControlPathsAuthClassification pins the auth-wall classification of
// the run-control surface: the WRITES live under /api/v1/admin/runs/* and are
// therefore admin-only behind the top-level auth wall; the two runner/board
// READS are anonymous GETs; and no write method ever passes the public check.
func TestRunControlPathsAuthClassification(t *testing.T) {
	// Anonymous reads (the board + external-runner contract surfaces).
	for _, path := range []string{"/api/v1/runs/flags", "/api/v1/runs/providers"} {
		if !isPublicReadAPIPath(path) {
			t.Errorf("%s must be an anonymous read", path)
		}
		if isPublicAPIRequest(http.MethodPost, path) {
			t.Errorf("POST %s must NOT be public", path)
		}
		if isAdminOnlyAPIPath(path) {
			t.Errorf("%s must not be admin-only (it is the anonymous read)", path)
		}
	}

	// Admin writes: never public, always admin-trust behind the wall.
	for _, path := range []string{
		"/api/v1/admin/runs/clear",
		"/api/v1/admin/runs/stop",
		"/api/v1/admin/runs/providers",
	} {
		if isPublicReadAPIPath(path) {
			t.Errorf("%s must not be an anonymous read", path)
		}
		if isPublicAPIRequest(http.MethodPost, path) {
			t.Errorf("POST %s must not be public", path)
		}
		if !isAdminOnlyAPIPath(path) {
			t.Errorf("%s must classify as admin-only", path)
		}
	}
}
