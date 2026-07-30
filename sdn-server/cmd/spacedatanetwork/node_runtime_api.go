package main

// node_runtime_api.go — GET /api/node/runtime: the node's own runtime facts as
// JSON, for the dashboard's NODE HEALTH / SERVICE / NETWORK THROUGHPUT widgets
// (graph task sdn-dashboard-restore-template-widgets, IRIS ruling 2026-07-30
// §2 wave 1 + constraint (d): "your admin-gated JSON endpoint is the right
// call").
//
// WHY THIS EXISTS AT ALL. Every fact the widgets need already existed in this
// process — uptime, store totals, disk headroom, service state/mode, and the
// libp2p bandwidth totals plus a ~5s-cadence history ring — but ONLY behind the
// `node_status_read` WASM capability (internal/modulert/caps/nodestatus.go).
// There was no HTTP surface for any of it, so a browser could not read the
// node's own bandwidth at all. This is the read surface, and nothing more: it
// assembles nothing, decides nothing, and adds no state. It calls
// node.RuntimeStatusSnapshot(), which calls the capability's own assembler.
//
// WHAT IT IS NOT. It is not a control surface. There is no RESTART, STOP or
// CHECK here and no daemon-lifecycle verb anywhere in this file: remote process
// control over HTTP is a NEW HOST CAPABILITY and a trust-model expansion, which
// is STOPPED FOR THE OWNER (Hermes charter; the auth half is Seal Council
// territory). The dashboard does not render those buttons either — a greyed
// button advertises a capability the node does not have.
//
// AUTHORITY. Admin-only, two independent locks, exactly like /api/node/epm's
// write:
//
//  1. the top-level auth wall (serveAdminMuxRequest -> isAdminOnlyAPIPath,
//     which carries the "/api/node/runtime" prefix), and
//  2. the method gate below — GET/HEAD only, so this path can never grow a
//     mutating verb by accident.
//
// Fail-closed follows from (1): the path is absent from isPublicReadAPIPath and
// from isAnyTierAuthenticatedAPIPath, so an anonymous caller gets 401 and a
// signed-in non-Admin operator gets 403 before this handler is reached. The
// dashboard therefore only calls it once /api/auth/me has already succeeded at
// Admin tier (§16.5's rule: a 403 in the console on every public page load is
// exactly the noise that trains operators to ignore real errors).
//
// DISCLOSURE. Storage path, disk capacity and bandwidth totals describe the
// HOST, not public data, which is why this is Admin and not on the anonymous
// surface beside /api/v1/stats.

import (
	"encoding/json"
	"net/http"

	"github.com/spacedatanetwork/sdn-server/internal/node"
)

// handleNodeRuntime serves the node's own runtime snapshot.
//
//	GET /api/node/runtime -> the node_status_read.status result object
//
// The response body is the RESULT object verbatim — not wrapped in the
// capability's {"ok":true,"result":…} envelope, which is a hostcall convention
// and means nothing over HTTP where the status code already carries success.
func handleNodeRuntime(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if n == nil {
			http.Error(w, "node not available", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Uptime, rates and the history ring change every few seconds; a cached
		// copy of this is a lie about the present.
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(n.RuntimeStatusSnapshot())
	}
}
