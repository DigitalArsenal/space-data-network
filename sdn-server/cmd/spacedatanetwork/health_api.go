package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/metrics"
)

// healthDeps is what the operator-facing health surface needs from the daemon.
// Every probe is optional: a nil probe reports that component as absent.
type healthDeps struct {
	// engineReady answers whether the FlatSQL engine is linked and answering.
	// It may block while the store rebuilds a poisoned engine, so the
	// readiness handler bounds it with readyProbeTimeout instead of waiting
	// (reads never wait on the data layer — owner 2026-09-02).
	engineReady func() bool
	// dataReady reports whether mounted data services have completed warmup.
	// Nil means this node does not require a separate data-serving check.
	dataReady func() bool
	// peerCount reports connected libp2p peers, or -1 when the host is down.
	peerCount func() int
	// requireAuth and authHandler gate /metrics exactly like every other
	// operator surface: the numbers describe the HOST, not public data.
	requireAuth bool
	authHandler *auth.Handler
	// probeTimeout bounds engineReady; zero means readyProbeTimeout.
	probeTimeout time.Duration
}

const readyProbeTimeout = 2 * time.Second

// mountHealthRoutes serves the load-balancer and monitoring surface (OPS-08):
//
//	GET /health, /api/v1/health   200 "ok"          the process serves requests
//	GET /ready,  /api/v1/ready    200 "ready"       store linked, engine answering, host up
//	                              503 "not ready: …" with the first failing component
//	GET /metrics                  Prometheus text; operator session required
//
// /health and /ready are anonymous by design (a probe cannot sign in) and
// disclose nothing beyond a status word.
func mountHealthRoutes(mux *http.ServeMux, deps healthDeps) {
	probe := &boundedReadinessProbe{}
	health := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = fmt.Fprintln(w, "ok")
	}
	ready := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if reason := probe.check(r.Context(), deps); reason != "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "not ready: %s\n", reason)
			return
		}
		_, _ = fmt.Fprintln(w, "ready")
	}
	mux.HandleFunc("/health", health)
	mux.HandleFunc("/api/v1/health", health)
	mux.HandleFunc("/ready", ready)
	mux.HandleFunc("/api/v1/ready", ready)
	promHandler := metrics.Handler()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		gateAdminOnlyHandler(w, r, promHandler, deps.authHandler, deps.requireAuth)
	})
}

// One outstanding probe is shared by all requests. A stuck store cannot create
// a new blocked goroutine every time a load balancer polls readiness.
type readinessAttempt struct {
	done   chan struct{}
	reason string
}

type boundedReadinessProbe struct {
	mu      sync.Mutex
	pending *readinessAttempt
}

func (p *boundedReadinessProbe) check(ctx context.Context, deps healthDeps) string {
	if ctx.Err() != nil {
		return "probe cancelled"
	}
	p.mu.Lock()
	attempt := p.pending
	if attempt == nil {
		attempt = &readinessAttempt{done: make(chan struct{})}
		p.pending = attempt
		go func() {
			attempt.reason = notReadyReason(deps)
			p.mu.Lock()
			close(attempt.done)
			p.pending = nil
			p.mu.Unlock()
		}()
	}
	p.mu.Unlock()
	timeout := deps.probeTimeout
	if timeout <= 0 {
		timeout = readyProbeTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-attempt.done:
		return attempt.reason
	case <-ctx.Done():
		return "probe cancelled"
	case <-timer.C:
		return "readiness probe busy"
	}
}

// notReadyReason names the first unavailable component. The shared caller
// bounds every callback, including a blocked peer-count read.
func notReadyReason(deps healthDeps) string {
	if deps.engineReady == nil {
		return "store not linked"
	}
	if deps.dataReady != nil && !deps.dataReady() {
		return "data services warming"
	}
	if !deps.engineReady() {
		return "engine not answering"
	}
	if deps.peerCount == nil || deps.peerCount() < 0 {
		return "libp2p host down"
	}
	return ""
}
