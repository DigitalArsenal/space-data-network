package api

import "net/http"

// ReachabilityStatusPath serves the node's own answer to "can anyone reach me?"
//
// The node has always COMPUTED this — internal/node/reachability.go tracks
// AutoNAT, the reachable/unreachable address sets, the NAT watchdog's port
// mappings and the IPFS sidecar's view, and reduces them to one verdict
// (direct / relayed / unconfirmed / unreachable). Nothing served it. An
// operator running a node behind a home router had no way to find out whether
// it was reachable, and neither did anyone helping them: the answer existed in
// memory and never left the process.
//
// It sits behind the operator gate with /metrics and the alert list, for the
// same reason those do — the payload is this host's addresses and NAT state,
// which is real operational detail. The anonymous /health stays a status word.
const ReachabilityStatusPath = "/api/v1/status/reachability"

// ReachabilityHandler serves the snapshot the node computes.
//
// snapshot returns node.ReachabilitySnapshot, typed as any so this package does
// not import internal/node; it marshals as itself. A nil provider means the
// host is not up yet, which is a real state during boot, not an error.
func ReachabilityHandler(snapshot func() any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			return
		}
		if snapshot == nil {
			writeJSON(w, http.StatusOK, map[string]any{"verdict": "unknown", "autonat": "unknown"})
			return
		}
		writeJSON(w, http.StatusOK, snapshot())
	}
}
