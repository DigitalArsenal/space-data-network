package api

import (
	"net/http"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// CatalogHandler serves the node's schema catalog endpoint.
type CatalogHandler struct {
	store  *storage.FlatSQLStore
	peerID peer.ID
	cfg    *config.Config
}

// NewCatalogHandler creates a new catalog handler.
func NewCatalogHandler(store *storage.FlatSQLStore, peerID peer.ID, cfg *config.Config) *CatalogHandler {
	return &CatalogHandler{
		store:  store,
		peerID: peerID,
		cfg:    cfg,
	}
}

// RegisterRoutes registers the catalog API route.
func (h *CatalogHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/catalog", h.handleCatalog)
}

func (h *CatalogHandler) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "local storage unavailable in edge mode")
		return
	}

	ranges, err := h.store.SchemaDateRanges()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query catalog: "+err.Error())
		return
	}

	schemas := make([]map[string]interface{}, 0, len(ranges))
	for _, sr := range ranges {
		entry := map[string]interface{}{
			"name":         sr.Schema,
			"record_count": sr.RecordCount,
			"total_bytes":  sr.TotalBytes,
		}
		if sr.OldestEpoch != nil {
			entry["oldest_epoch"] = sr.OldestEpoch.Format(time.RFC3339)
		}
		if sr.NewestEpoch != nil {
			entry["newest_epoch"] = sr.NewestEpoch.Format(time.RFC3339)
		}
		schemas = append(schemas, entry)
	}

	capabilities := []string{"data_query"}
	if h.cfg.Publishing.Enabled {
		capabilities = append(capabilities, "data_publish")
	}
	capabilities = append(capabilities, "pubsub")

	rateLimits := map[string]interface{}{
		"query_per_minute":   h.cfg.Network.MaxMessagesPerMinute,
		"publish_per_minute": 10,
		"max_record_bytes":   h.cfg.Publishing.MaxRecordBytes,
	}

	w.Header().Set("Cache-Control", "public, max-age=30, s-maxage=120, stale-while-revalidate=300")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"peer_id":      h.peerID.String(),
		"schemas":      schemas,
		"capabilities": capabilities,
		"rate_limits":  rateLimits,
	})
}
