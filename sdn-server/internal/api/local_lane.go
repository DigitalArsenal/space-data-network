package api

// local_lane.go — the read half of the loopback-only local publish lane.
//
// The lane itself (publish.go, PublishHandler.RegisterLocalLaneRoutes) is a
// SEPARATE, loopback-bound listener that carries the publish routes with no HTTP
// authentication, for a data pipeline running ON the node's host. See
// RegisterLocalLaneRoutes for why it is a second socket rather than an IP-based
// exemption on the public listener: nginx reverse-proxies the public listener with
// a catch-all location, so public traffic already reaches the daemon from
// 127.0.0.1, and trusting the client IP there would expose writes to the internet.

import "net/http"

// RegisterLocalLaneReadRoutes mounts the ONLY read route an on-host pipeline needs
// on the private, loopback-bound publish-lane mux: GET /api/v1/stats.
//
// The constellation pipeline polls /api/v1/stats for its progress heartbeat and,
// more importantly, for its end-of-run completeness gate — it fails the run unless
// the node's sources[] row for the batch accounts for every record it acked. A
// write-only lane would therefore fail every run, forcing the pipeline to reach
// back to the TLS public listener for reads and juggle two base URLs.
//
// This grants nothing new: /api/v1/stats is ALREADY an anonymous read on the
// public listener (isPublicReadAPIPath), so serving it on a loopback-only socket
// adds no surface. Writes remain the publish routes; nothing else is mounted here.
func (h *CoreAPIHandler) RegisterLocalLaneReadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/stats", h.withRL(h.handleStats))
}
