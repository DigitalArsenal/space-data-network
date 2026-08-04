package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/tlsmgr"
)

// THE DEFECT THESE TESTS CLOSE (sdn-rfb-public-read-allowlist).
//
// The anonymous read surface was a literal list containing exactly one
// per-schema route, "/api/v1/data/omm/bulk". A browser reading the live $RFB
// catalogue therefore got 401 — and, because the CORS headers are only applied
// to requests the classifier calls public, it got that 401 WITHOUT
// Access-Control-Allow-Origin, so the page saw an opaque network failure and
// could not even report the refusal. Every future standard would have repeated
// it.

func TestDataPlaneBulkSchemaParsesOnlyTheBulkVerb(t *testing.T) {
	tests := []struct {
		path string
		code string
		ok   bool
	}{
		{"/api/v1/data/omm/bulk", "omm", true},
		{"/api/v1/data/rfb/bulk", "rfb", true},
		{"/api/v1/data/RFB/bulk", "RFB", true},
		{"/api/v1/data/bulk", "", false},  // no schema segment
		{"/api/v1/data//bulk", "", false}, // empty schema segment
		{"/api/v1/data/rfb/bulk/extra", "", false},
		{"/api/v1/data/a/b/bulk", "", false},    // nested, never the flow's shape
		{"/api/v1/data/records/cid", "", false}, // record-by-CID keeps its own gate
		{"/api/v1/data/query", "", false},       // SQL route keeps its own gate
		{"/api/v1/data/summary", "", false},
		{"/api/v2/data/rfb/bulk", "", false}, // different API version
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			code, ok := dataPlaneBulkSchema(tc.path)
			if ok != tc.ok || code != tc.code {
				t.Fatalf("dataPlaneBulkSchema(%q) = (%q, %v), want (%q, %v)", tc.path, code, ok, tc.code, tc.ok)
			}
		})
	}
}

func TestPublicDataPlaneAdmitsPublishedStandardsAndRefusesTheRest(t *testing.T) {
	tests := []struct {
		method string
		path   string
		public bool
	}{
		// The live RF catalogue — the read that 401'd.
		{http.MethodGet, "/api/v1/data/rfb/bulk", true},
		{http.MethodHead, "/api/v1/data/rfb/bulk", true},
		{http.MethodOptions, "/api/v1/data/rfb/bulk", true},
		{http.MethodGet, "/api/v1/data/RFB/bulk", true},
		// Regression: the route that already worked must keep working.
		{http.MethodGet, "/api/v1/data/omm/bulk", true},
		{http.MethodGet, "/api/v1/data/cat/bulk", true},
		{http.MethodGet, "/api/v1/data/lks/bulk", true},
		// Closed by default: key material, grants, node-internal ledgers, and
		// anything unknown.
		{http.MethodGet, "/api/v1/data/kmf/bulk", false},
		{http.MethodGet, "/api/v1/data/acl/bulk", false},
		{http.MethodGet, "/api/v1/data/lgr/bulk", false},
		{http.MethodGet, "/api/v1/data/plog/bulk", false},
		{http.MethodGet, "/api/v1/data/nosuchstandard/bulk", false},
		// Reads only. A per-schema WRITE is never anonymous.
		{http.MethodPost, "/api/v1/data/rfb/bulk", false},
		{http.MethodDelete, "/api/v1/data/rfb/bulk", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if got := isPublicAPIRequest(tc.method, tc.path); got != tc.public {
				t.Fatalf("isPublicAPIRequest(%q, %q) = %v, want %v", tc.method, tc.path, got, tc.public)
			}
		})
	}
}

// TestPublicDataPlaneReadsCarryCORS proves the browser half: the classifier
// decision is what attaches Access-Control-Allow-Origin, so a $RFB read from a
// page must both pass the gate AND come back with CORS on the preflight and on
// the read itself.
func TestPublicDataPlaneReadsCarryCORS(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := adminSecurityMiddleware(inner, tlsmgr.ModeStatic, isPublicAPIRequest)

	for _, method := range []string{http.MethodOptions, http.MethodGet} {
		req := httptest.NewRequest(method, "/api/v1/data/rfb/bulk", nil)
		req.Header.Set("Origin", "https://spaceaware.io")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://spaceaware.io" {
			t.Fatalf("%s /api/v1/data/rfb/bulk: Access-Control-Allow-Origin = %q, want the request origin", method, got)
		}
		if rec.Header().Get("Vary") != "Origin" {
			t.Fatalf("%s /api/v1/data/rfb/bulk: missing Vary: Origin", method)
		}
	}

	// A gated schema must NOT be advertised as cross-origin readable.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data/kmf/bulk", nil)
	req.Header.Set("Origin", "https://spaceaware.io")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("gated schema answered with Access-Control-Allow-Origin %q", got)
	}
}
