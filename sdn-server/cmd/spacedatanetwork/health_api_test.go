package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
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
		rec := get(mux, path)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok","alerts":{"error":0,"warning":0}}` {
			t.Fatalf("%s: status %d body %q", path, rec.Code, rec.Body.String())
		}
	}
	// The alert list describes this node and its peers by name: operator
	// session only, exactly like /metrics.
	if rec := get(mux, "/api/v1/status/alerts"); rec.Code == http.StatusOK {
		t.Fatalf("/api/v1/status/alerts served anonymously on a require_auth node (status %d)", rec.Code)
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
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "probe busy") {
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

// The 31-hour publication outage of 2026-09-13..15 was invisible because
// nothing an operator polls ever said anything was wrong. /health says so now,
// and its HTTP status still does not change: a degraded node is still serving.
func TestHealthReportsDegradedAndAlertsListsDetail(t *testing.T) {
	registry := ops.NewRegistry()
	registry.Raise(ops.KindPublicationRejected, "12*ab12cd", ops.SeverityError,
		`SIGNATURE_TYPE "Ed25519" does not match the provider's Secp256k1 key`)
	// A neutral subject on purpose: the Go host is application-blind, and this
	// assertion is about whether a subject LEAKS, never about which app it names.
	registry.Raise(ops.KindLaneFailing, "example-source-ingest", ops.SeverityWarning, "parser returned HTTP 400")

	mux := http.NewServeMux()
	mountHealthRoutes(mux, healthDeps{
		engineReady: func() bool { return true },
		peerCount:   func() int { return 3 },
		alerts:      registry,
	})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	rec := get("/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want 200 even while degraded", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"status":"degraded","alerts":{"error":1,"warning":1}}` {
		t.Fatalf("/health body = %s", body)
	}
	if strings.Contains(rec.Body.String(), "example-source-ingest") {
		t.Fatal("anonymous /health leaked an alert subject")
	}

	// require_auth is off here, so the operator route answers in full.
	rec = get("/api/v1/status/alerts")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/status/alerts status = %d", rec.Code)
	}
	for _, want := range []string{"publication_rejected", "12*ab12cd", "example-source-ingest", "Secp256k1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("/api/v1/status/alerts missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestReadinessReportsDataWarmup(t *testing.T) {
	var ready atomic.Bool
	mux := http.NewServeMux()
	mountHealthRoutes(mux, healthDeps{
		engineReady: func() bool { return true },
		dataReady:   ready.Load,
		peerCount:   func() int { return 0 },
	})
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
		return w
	}
	if w := get(); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "data services warming") {
		t.Fatalf("warmup: %d %s", w.Code, w.Body.String())
	}
	ready.Store(true)
	if w := get(); w.Code != http.StatusOK {
		t.Fatalf("ready status = %d", w.Code)
	}
}

func TestReadinessSharesBlockedProbeAndHonorsRequestCancellation(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	mux := http.NewServeMux()
	mountHealthRoutes(mux, healthDeps{
		engineReady:  func() bool { calls.Add(1); <-release; return true },
		peerCount:    func() int { return 0 },
		probeTimeout: 10 * time.Millisecond,
	})
	var requests sync.WaitGroup
	for i := 0; i < 24; i++ {
		requests.Add(1)
		go func() {
			defer requests.Done()
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("blocked probe status = %d", w.Code)
			}
		}()
	}
	requests.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("blocked engine probe calls = %d, want one shared call", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil).WithContext(ctx))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "probe cancelled") {
		t.Fatalf("cancelled probe: %d %s", w.Code, w.Body.String())
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("cancelled request started another probe: %d", n)
	}
}
