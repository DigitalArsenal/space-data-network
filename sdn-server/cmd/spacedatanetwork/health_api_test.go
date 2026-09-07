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
