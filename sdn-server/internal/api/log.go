package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// LogQueryHandler serves read-only log query APIs for PLG/PLH data.
type LogQueryHandler struct {
	store *storage.FlatSQLStore
}

// NewLogQueryHandler creates a new log query handler.
func NewLogQueryHandler(store *storage.FlatSQLStore) *LogQueryHandler {
	return &LogQueryHandler{store: store}
}

// RegisterRoutes registers log API routes.
func (h *LogQueryHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/log/", h.handleLog)
}

func (h *LogQueryHandler) handleLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse path: /api/v1/log/{schema}/{action}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/log/")
	path = strings.TrimSuffix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		writeError(w, http.StatusBadRequest, "expected /api/v1/log/{schema}/{head|entries}")
		return
	}

	schemaType := parts[0]
	action := parts[1]

	switch action {
	case "head":
		h.handleLogHead(w, r, schemaType)
	case "entries":
		h.handleLogEntries(w, r, schemaType)
	case "heads":
		h.handleLogHeads(w, r, schemaType)
	default:
		writeError(w, http.StatusBadRequest, "unknown log action: "+action+" (expected head, entries, or heads)")
	}
}

// handleLogHead returns the latest PLH info for a specific publisher+schema.
// GET /api/v1/log/{schema}/head?publisher={peerID}
func (h *LogQueryHandler) handleLogHead(w http.ResponseWriter, r *http.Request, schemaType string) {
	publisher := r.URL.Query().Get("publisher")
	if publisher == "" {
		writeError(w, http.StatusBadRequest, "missing publisher query parameter")
		return
	}

	sequence, entryHash, err := h.store.GetLogHead(publisher, schemaType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get log head: "+err.Error())
		return
	}

	if sequence == 0 {
		writeError(w, http.StatusNotFound, "no log entries found for publisher/schema")
		return
	}

	recordCount, _ := h.store.LogRecordCount(publisher, schemaType)
	oldest, newest, _ := h.store.LogEpochRange(publisher, schemaType)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema_type":       schemaType,
		"publisher_peer_id": publisher,
		"head_sequence":     sequence,
		"head_entry_hash":   entryHash,
		"record_count":      recordCount,
		"oldest_epoch_day":  oldest,
		"newest_epoch_day":  newest,
	})
}

// handleLogEntries returns PLG entries for a publisher+schema since a given sequence.
// GET /api/v1/log/{schema}/entries?publisher={peerID}&since={seq}&limit={n}
func (h *LogQueryHandler) handleLogEntries(w http.ResponseWriter, r *http.Request, schemaType string) {
	publisher := r.URL.Query().Get("publisher")
	if publisher == "" {
		writeError(w, http.StatusBadRequest, "missing publisher query parameter")
		return
	}

	sinceSequence := uint64(0)
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		parsed, err := strconv.ParseUint(sinceStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter: "+err.Error())
			return
		}
		sinceSequence = parsed
	}

	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		parsed, err := strconv.Atoi(limitStr)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		if parsed > 1000 {
			parsed = 1000
		}
		limit = parsed
	}

	entries, err := h.store.QueryLogEntries(publisher, schemaType, sinceSequence, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query log entries: "+err.Error())
		return
	}

	results := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		results = append(results, map[string]interface{}{
			"data_base64": base64.StdEncoding.EncodeToString(entry),
			"bytes":       len(entry),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema_type":       schemaType,
		"publisher_peer_id": publisher,
		"since_sequence":    sinceSequence,
		"count":             len(results),
		"entries":           results,
	})
}

// handleLogHeads returns the latest log head for all publishers of a given schema type.
// GET /api/v1/log/{schema}/heads
func (h *LogQueryHandler) handleLogHeads(w http.ResponseWriter, r *http.Request, schemaType string) {
	heads, err := h.store.QueryLogHeads(schemaType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query log heads: "+err.Error())
		return
	}

	results := make([]map[string]interface{}, 0, len(heads))
	for _, head := range heads {
		results = append(results, map[string]interface{}{
			"publisher_peer_id": head.PublisherPeerID,
			"schema_type":       head.SchemaType,
			"head_sequence":     head.Sequence,
			"head_entry_hash":   head.EntryHash,
			"timestamp":         head.Timestamp,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema_type": schemaType,
		"count":       len(results),
		"heads":       results,
	})
}
