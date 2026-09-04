package main

import (
	"fmt"
	"net/http"
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
		if reason := notReadyReason(deps); reason != "" {
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

// notReadyReason names the first component that is not ready, or "" when
// every probe passes. The engine probe is bounded: a store that cannot answer
// within the timeout is reported busy rather than waited for.
func notReadyReason(deps healthDeps) string {
	if deps.engineReady == nil {
		return "store not linked"
	}
	timeout := deps.probeTimeout
	if timeout <= 0 {
		timeout = readyProbeTimeout
	}
	answer := make(chan bool, 1)
	go func() { answer <- deps.engineReady() }()
	select {
	case ok := <-answer:
		if !ok {
			return "engine not answering"
		}
	case <-time.After(timeout):
		return "store busy"
	}
	if deps.peerCount == nil || deps.peerCount() < 0 {
		return "libp2p host down"
	}
	return ""
}
