package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthRoutesReportLivenessReadinessAndGateMetrics(t *testing.T) {
	get := func(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	mux := http.NewServeMux()
	mountHealthRoutes(mux, healthDeps{
		engineReady: func() bool { return true },
		peerCount:   func() int { return 3 },
		requireAuth: true,
	})
	for _, path := range []string{"/health", "/api/v1/health"} {
		if rec := get(mux, path); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
			t.Fatalf("%s: status %d body %q", path, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{"/ready", "/api/v1/ready"} {
		if rec := get(mux, path); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ready" {
			t.Fatalf("%s: status %d body %q", path, rec.Code, rec.Body.String())
		}
	}
	// Metrics describe the host: no operator session, no numbers.
	if rec := get(mux, "/metrics"); rec.Code == http.StatusOK {
		t.Fatalf("/metrics served anonymously on a require_auth node (status %d)", rec.Code)
	}

	// A store that cannot answer within the probe budget is reported busy,
	// never waited for.
	busy := http.NewServeMux()
	started := time.Now()
	mountHealthRoutes(busy, healthDeps{
		engineReady:  func() bool { time.Sleep(2 * time.Second); return true },
		peerCount:    func() int { return 1 },
		probeTimeout: 50 * time.Millisecond,
	})
	rec := get(busy, "/ready")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "store busy") {
		t.Fatalf("busy store: status %d body %q", rec.Code, rec.Body.String())
	}
	if time.Since(started) > time.Second {
		t.Fatalf("readiness waited on the busy store for %s", time.Since(started))
	}

	// No store at all, and a libp2p host that is down, each name themselves.
	none := http.NewServeMux()
	mountHealthRoutes(none, healthDeps{})
	if rec := get(none, "/ready"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "store not linked") {
		t.Fatalf("no store: status %d body %q", rec.Code, rec.Body.String())
	}
	down := http.NewServeMux()
	mountHealthRoutes(down, healthDeps{engineReady: func() bool { return true }, peerCount: func() int { return -1 }})
	if rec := get(down, "/ready"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "libp2p host down") {
		t.Fatalf("host down: status %d body %q", rec.Code, rec.Body.String())
	}
	// Liveness stays green while readiness is red: the process is serving.
	if rec := get(none, "/health"); rec.Code != http.StatusOK {
		t.Fatalf("/health on an unready node: status %d", rec.Code)
	}
}
