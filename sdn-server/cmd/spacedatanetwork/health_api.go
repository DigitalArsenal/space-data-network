package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/metrics"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
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
	// alerts is the node's operational alert registry. /health reports its
	// severity COUNTS (a probe gets a number, never a subject) and
	// /api/v1/status/alerts serves the full list to an operator session.
	// Nil is tolerated and reports a node with nothing wrong.
	alerts *ops.Registry
}

const readyProbeTimeout = 2 * time.Second

// mountHealthRoutes serves the load-balancer and monitoring surface (OPS-08):
//
//	GET /health, /api/v1/health   200 {"status":"ok|degraded","alerts":{…}}
//	GET /ready,  /api/v1/ready    200 "ready"       store linked, engine answering, host up
//	                              503 "not ready: …" with the first failing component
//	GET /api/v1/status/alerts     the active alerts in full; operator session required
//	GET /metrics                  Prometheus text; operator session required
//
// /health and /ready are anonymous by design (a probe cannot sign in) and
// disclose nothing beyond a status word and a count per alert severity. The
// HTTP status of /health is unchanged by degradation: a failing CelesTrak lane
// is not a reason for a load balancer to pull the node out of rotation, it is
// a reason for an operator to look.
func mountHealthRoutes(mux *http.ServeMux, deps healthDeps) {
	probe := &boundedReadinessProbe{}
	health := api.HealthHandler(deps.alerts)
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
	alertsHandler := api.AlertsHandler(deps.alerts)
	mux.HandleFunc(api.AlertsStatusPath, func(w http.ResponseWriter, r *http.Request) {
		gateAdminOnlyHandler(w, r, alertsHandler, deps.authHandler, deps.requireAuth)
	})
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
