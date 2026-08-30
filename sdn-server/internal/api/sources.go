package api

// GET /api/v1/data/sources — RESOLVED SOURCES (Watchfloor build step 6).
//
// The dashboard's provenance chips need one server answer that composes the
// joins the store already holds but no code composed: per-source summaries
// (which CARRY producer_peer_id, host-stamped and trustworthy) joined to the
// EPM directory (peer_id -> signed publisher profile -> legal name). The
// client's resolver renders exactly what this returns — the ONE honesty
// locus stays intact, this endpoint just moves the composition server-side
// where the producer identity actually exists.
//
// Honesty contract (Watchfloor spec §4):
//   - organization is null unless a SIGNED publisher profile is indexed for
//     the producing peer — a provider slug alone never resolves to a name.
//   - state is "signed" for a directory hit. "domain-verified" and the
//     higher rungs wait for a Go-side domain-proof verifier and the trust
//     surfacing task; this endpoint will carry them in the same shape.
//   - evidence is plain product language, ready for the chip hover.

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type resolvedOrganization struct {
	Name     string   `json:"name"`
	State    string   `json:"state"`
	Evidence []string `json:"evidence"`
	PeerID   string   `json:"peer_id,omitempty"`
	EPMCID   string   `json:"epm_cid,omitempty"`
}

type resolvedSourceRow struct {
	Schema         string                `json:"schema"`
	ProviderID     string                `json:"provider_id"`
	SourceName     string                `json:"source_name"`
	BatchID        string                `json:"batch_id,omitempty"`
	ProducerPeerID string                `json:"producer_peer_id,omitempty"`
	Count          int64                 `json:"count"`
	TotalBytes     int64                 `json:"total_bytes"`
	Organization   *resolvedOrganization `json:"organization"`
}

type resolvedSourcesResponse struct {
	AsOf    string              `json:"as_of"`
	Sources []resolvedSourceRow `json:"sources"`
}

// RegisterResolvedSourcesRoute mounts the resolved-sources lane beside the
// other data routes; the admin wall's standing policy applies unchanged (this
// endpoint invents no auth semantics — spec rule: nothing here gets ahead of
// the security roadmap).
func (h *DataQueryHandler) RegisterResolvedSourcesRoute(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/data/sources", h.handleResolvedSources)
}

// resolveProducerOrganization answers the org for one producing peer, or nil.
// A missing directory entry is a NIL organization, never a guess.
func resolveProducerOrganization(store *storage.FlatSQLStore, peerID string) *resolvedOrganization {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" || store == nil {
		return nil
	}
	records, err := store.QueryDirectory(storage.DirectoryQuery{PeerID: peerID, Limit: 1})
	if err != nil || len(records) == 0 {
		return nil
	}
	rec := records[0]
	name := strings.TrimSpace(rec.LegalName)
	if name == "" {
		name = strings.TrimSpace(rec.DN)
	}
	if name == "" {
		return nil
	}
	return &resolvedOrganization{
		Name:  name,
		State: "signed",
		Evidence: []string{
			"Records from this source are published under " + name + "'s registered key.",
			"Identity from the signed publisher profile this node verified and indexed.",
		},
		PeerID: peerID,
		EPMCID: rec.EPMCID,
	}
}

func (h *DataQueryHandler) handleResolvedSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if !h.ensureStore(w) {
		return
	}
	summary, err := h.store.DataSummary()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// One directory lookup per distinct producing peer, not per source row.
	orgByPeer := map[string]*resolvedOrganization{}
	rows := make([]resolvedSourceRow, 0, len(summary.Sources))
	for _, src := range summary.Sources {
		org, seen := orgByPeer[src.ProducerPeerID]
		if !seen {
			org = resolveProducerOrganization(h.store, src.ProducerPeerID)
			orgByPeer[src.ProducerPeerID] = org
		}
		rows = append(rows, resolvedSourceRow{
			Schema:         src.SchemaName,
			ProviderID:     src.ProviderID,
			SourceName:     src.SourceName,
			BatchID:        src.BatchID,
			ProducerPeerID: src.ProducerPeerID,
			Count:          src.Count,
			TotalBytes:     src.TotalBytes,
			Organization:   org,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Schema != rows[j].Schema {
			return rows[i].Schema < rows[j].Schema
		}
		return rows[i].Count > rows[j].Count
	})
	writeJSON(w, http.StatusOK, resolvedSourcesResponse{
		AsOf:    time.Now().UTC().Format(time.RFC3339),
		Sources: rows,
	})
}
