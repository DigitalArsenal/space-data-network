package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// catalogCacheTTL bounds how stale the served catalog may be. It matches the
// response's Cache-Control max-age so every client sees the same freshness.
const catalogCacheTTL = 30 * time.Second

// CatalogHandler serves the node's schema catalog endpoint.
type CatalogHandler struct {
	store  *storage.FlatSQLStore
	peerID peer.ID
	cfg    *config.Config

	// The underlying query counts and scans every schema under the store's
	// read lock; on a 140,586-record publisher it exceeded 12 s per request
	// while ingestion held the lock (node-transfer tests, 2026-09-10).
	// Concurrent callers wait for one computation instead of stampeding.
	mu       sync.Mutex
	cachedAt time.Time
	cached   []storage.SchemaDateRange
	cacheTTL time.Duration
}

// NewCatalogHandler creates a new catalog handler.
func NewCatalogHandler(store *storage.FlatSQLStore, peerID peer.ID, cfg *config.Config) *CatalogHandler {
	return &CatalogHandler{
		store:    store,
		peerID:   peerID,
		cfg:      cfg,
		cacheTTL: catalogCacheTTL,
	}
}

// SetCacheTTL overrides the catalog cache lifetime (zero disables caching).
func (h *CatalogHandler) SetCacheTTL(ttl time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cacheTTL = ttl
	h.cached = nil
}

// RegisterRoutes registers the catalog API route.
func (h *CatalogHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/catalog", h.handleCatalog)
}

// schemaRanges returns the per-schema ranges, served from the short-lived
// cache when it is fresh. The second result reports whether the cache
// answered; the third is the age of the served data.
func (h *CatalogHandler) schemaRanges() ([]storage.SchemaDateRange, bool, time.Duration, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cached != nil && h.cacheTTL > 0 && time.Since(h.cachedAt) < h.cacheTTL {
		return h.cached, true, time.Since(h.cachedAt), nil
	}
	ranges, err := h.store.SchemaDateRanges()
	if err != nil {
		return nil, false, 0, err
	}
	h.cached = ranges
	h.cachedAt = time.Now()
	return ranges, false, 0, nil
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

	ranges, cached, age, err := h.schemaRanges()
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
			// Search readiness per schema: "ready", "building", "failed" or
			// "cold". A warm UI is not evidence that a cold node can serve a
			// search; clients gate their search affordance on this field.
			"index_state": h.store.FullTextIndexState(sr.Schema),
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
		"peer_id":             h.peerID.String(),
		"schemas":             schemas,
		"capabilities":        capabilities,
		"rate_limits":         rateLimits,
		"catalog_cached":      cached,
		"catalog_age_seconds": int64(age / time.Second),
	})
}
