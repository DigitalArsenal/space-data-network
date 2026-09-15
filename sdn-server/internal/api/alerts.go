package api

import (
	"net/http"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

// The operational-alert surfaces (OPS ALERT lane). Two routes, deliberately
// split by what they are allowed to disclose:
//
//	GET /health                  anonymous, always HTTP 200 — a status word and
//	                             a count per severity, nothing else
//	GET /api/v1/status/alerts    operator session (the /metrics posture) — the
//	                             full alert list, subjects and error text
//
// The split exists because the subject of an alert is an app id, a producer
// peer id, or a plugin id: real operational detail about this node and its
// peers, which a load-balancer probe has no business reading. A count is not.
const AlertsStatusPath = "/api/v1/status/alerts"

// healthBody is the anonymous /health payload. The HTTP status is unchanged
// (always 200 while the process is serving — that is what a liveness probe
// asks); "degraded" is how the body says something is wrong without making a
// load balancer take the node out of rotation for a failing CelesTrak lane.
type healthBody struct {
	Status string     `json:"status"`
	Alerts ops.Counts `json:"alerts"`
}

// HealthHandler serves the anonymous liveness surface with the alert summary
// folded in. A nil registry reports a plain healthy node.
func HealthHandler(alerts *ops.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		snapshot := alerts.Snapshot()
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, http.StatusOK, healthBody{Status: snapshot.Status, Alerts: snapshot.Counts})
	}
}

// AlertsHandler serves the full active-alert list for an operator. Mount it
// behind the same gate as /metrics.
func AlertsHandler(alerts *ops.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		active := alerts.Active()
		if active == nil {
			active = []ops.Alert{}
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"alerts": active})
	}
}
