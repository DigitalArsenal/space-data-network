package api

// admin_store.go — operator store-maintenance controls.
//
// ROUTE (admin write):
//
//	POST /api/v1/admin/store/hydrate[?force=true]
//	    Replays the compact record catalog into the SQL control tables and
//	    rebuilds the derived source summaries, then returns the resulting
//	    counts. This re-syncs /api/v1/stats sources[], /api/v1/data/index, and
//	    batch-clear bookkeeping WITHOUT a daemon restart — for the case where
//	    pre-restart runs are missing from the board because the post-boot
//	    background hydration has not finished yet (or a prior attempt errored).
//	    Returns {records_replayed, sources, total_records, duration_ms, forced}.
//
//	    force=true re-runs the metadata replay even when the catalog is already
//	    hydrated for this process (safe: every apply is an idempotent upsert).
//	    Without force, an already-hydrated catalog replays nothing
//	    (records_replayed=0) but the source summaries are still rebuilt from
//	    durable state, which is enough to re-sync a board that drifted.
//
// AUTH: the /api/v1/admin/ prefix places this behind the SAME top-level
// admin-auth wall as every other admin API (main.go isAdminOnlyAPIPath → Admin
// trust when RequireAuth is on; unauthenticated on auth-disabled dev nodes),
// exactly like the runs_control.go admin writes. No bypasses.

import (
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// StoreAdminHandler serves operator store-maintenance controls.
type StoreAdminHandler struct {
	store *storage.FlatSQLStore
}

// NewStoreAdminHandler creates the handler.
func NewStoreAdminHandler(store *storage.FlatSQLStore) *StoreAdminHandler {
	return &StoreAdminHandler{store: store}
}

// RegisterRoutes mounts the admin store-maintenance routes on the admin mux.
func (h *StoreAdminHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/store/hydrate", h.handleHydrate)
}

func (h *StoreAdminHandler) handleHydrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	force := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("force")), "true")

	start := time.Now()
	replayed, err := h.store.ReplayRecordCatalog(force, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "replay record catalog: "+err.Error())
		return
	}
	if err := h.store.RebuildSourceSummaries(); err != nil {
		writeError(w, http.StatusInternalServerError, "rebuild source summaries: "+err.Error())
		return
	}
	duration := time.Since(start)

	sources, total := 0, int64(0)
	if summary, sErr := h.store.DataSummary(); sErr == nil && summary != nil {
		sources = len(summary.Sources)
		total = summary.TotalRecords
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"records_replayed": replayed,
		"sources":          sources,
		"total_records":    total,
		"duration_ms":      duration.Milliseconds(),
		"forced":           force,
	})
}
