package main

import (
	"net/http"
	"testing"
)

// The $APPS feed is an ANONYMOUS read. A board that has to authenticate to
// show what a public node is retrieving is not a public node's board, so
// /api/apps must sit in the auth wall's public allowlist for GET/HEAD — and
// nowhere near it for anything that mutates.
func TestAppsFeedIsAnonymousReadOnly(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if !isPublicAPIRequest(method, "/api/apps") {
			t.Fatalf("%s /api/apps is not in the anonymous read allowlist", method)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if isPublicAPIRequest(method, "/api/apps") {
			t.Fatalf("%s /api/apps must not be anonymous — the feed is read-only", method)
		}
	}

	// The allowlist is exact-match, so no neighbouring path is opened by it.
	for _, path := range []string{"/api/apps/install", "/api/apps/", "/api/appsecret"} {
		if isPublicAPIRequest(http.MethodGet, path) {
			t.Fatalf("GET %s must not be anonymous; only the exact /api/apps feed is", path)
		}
	}
}
