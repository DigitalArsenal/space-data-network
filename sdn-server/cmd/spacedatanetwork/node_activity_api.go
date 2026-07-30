package main

// node_activity_api.go — GET /api/node/activity: the node's own bounded activity
// ring as JSON, for the dashboard's ACTIVITY LOG widget (graph task
// sdn-dashboard-wave2-edit-layout, IRIS §2/§3 wave 2).
//
// WHY THIS EXISTS AT ALL, and why it is one connector rather than a feature.
// The events already exist in this process: peer connects/disconnects, PNM
// publications, schema record stores and channel-grant issuance are appended to
// ONE shared 256-entry ring by taps that were already there
// (internal/node/epm_exchange_notifee.go, internal/node/node.go,
// internal/api/channels.go). The ring was readable ONLY behind the
// `node_activity_read` WASM capability, so a browser could not read this node's
// own activity at all. This adds the read surface and NOTHING else: it assembles
// nothing, decides nothing, stores nothing, and taps nothing new. It calls
// caps.NodeActivitySnapshot — the capability's OWN assembler — so the page can
// never render a shape no module sees.
//
// WHAT IT IS NOT. It is not a log API and not a subscription. There is no filter
// vocabulary, no since-cursor and no server-side search: the ring is 256 entries
// of host-own state, the widget shows five, and a query language over it would be
// a feature the node does not need in order to answer "what has this node been
// doing".
//
// AUTHORITY. Admin-only, two independent locks, exactly as
// node_runtime_api.go's read surface:
//
//  1. the top-level auth wall (serveAdminMuxRequest -> isAdminOnlyAPIPath,
//     which carries the "/api/node/activity" prefix), and
//  2. the method gate below — GET/HEAD only.
//
// Fail-closed follows from (1): the path is absent from isPublicReadAPIPath and
// isAnyTierAuthenticatedAPIPath, so anonymous gets 401 and a signed-in non-Admin
// operator gets 403 before this handler runs.
//
// DISCLOSURE, and why Admin rather than anonymous. Every row names a PEER this
// host talked to and when. Those peer ids are already public on the network, but
// the pattern — who this node connects to, in what order, how often — is a fact
// about the HOST, not published data. Same classification as
// /api/node/runtime's bandwidth totals.

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/node"
)

// nodeActivityDefaultLimit mirrors the hostcall's own default (caps/
// nodeactivity.go). The dashboard asks for five; a caller that asks for nothing
// gets the same fifty a module would.
const nodeActivityDefaultLimit = 50

// handleNodeActivity serves the node's own activity ring.
//
//	GET /api/node/activity?limit=N -> the node_activity_read.activity result
//
// `limit` is clamped by the assembler (<=0 -> 50, >capacity -> capacity). A
// non-numeric limit is treated as absent rather than as an error: this is a
// read-only convenience parameter, and the same tolerance the hostcall applies to
// a malformed payload.
//
// The response body is the RESULT object verbatim — not the capability's
// {"ok":true,"result":…} envelope, which is a hostcall convention and means
// nothing over HTTP where the status code already carries success.
func handleNodeActivity(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if n == nil {
			http.Error(w, "node not available", http.StatusServiceUnavailable)
			return
		}

		limit := nodeActivityDefaultLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				limit = parsed
			}
		}

		w.Header().Set("Content-Type", "application/json")
		// The newest entry is the point of the widget; a cached copy of "recent
		// activity" is a lie about the present.
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		// The ring accessor is nil-safe (node.ActivityRing may be nil on a
		// struct-literal Node) and so is the assembler: an unwired ring answers
		// with an empty, honest {"count":0,"events":[]}.
		_ = json.NewEncoder(w).Encode(
			caps.NodeActivitySnapshot(caps.NodeActivityMaterials{Ring: n.ActivityRing()}, limit),
		)
	}
}
