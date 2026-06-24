package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// SearchHandler serves shared provider/data search routes for CLI, Desktop,
// and bundled UI consumers.
type SearchHandler struct {
	store *storage.FlatSQLStore
}

type searchRequest struct {
	Query             string `json:"query"`
	Schema            string `json:"schema"`
	SchemaName        string `json:"schema_name"`
	SchemaNameAlt     string `json:"schemaName"`
	ProviderID        string `json:"provider_id"`
	ProviderIDAlt     string `json:"providerId"`
	ProviderPeerID    string `json:"provider_peer_id"`
	ProviderPeerIDAlt string `json:"providerPeerId"`
	SourceName        string `json:"source_name"`
	SourceNameAlt     string `json:"sourceName"`
	BatchID           string `json:"batch_id"`
	BatchIDAlt        string `json:"batchId"`
	QueryProfile      string `json:"query_profile"`
	QueryProfileAlt   string `json:"queryProfile"`
	Mode              string `json:"mode"`
	Limit             int    `json:"limit"`
}

// NewSearchHandler creates a shared search API handler.
func NewSearchHandler(store *storage.FlatSQLStore) *SearchHandler {
	return &SearchHandler{store: store}
}

// RegisterRoutes registers shared search API routes.
func (h *SearchHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/search/providers", h.handleProviders)
	mux.HandleFunc("/api/v1/search/data", h.handleData)
}

func (h *SearchHandler) handleProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}
	req, ok := decodeSearchRequest(w, r)
	if !ok {
		return
	}

	stats, err := h.store.LocalReplicaStats(req.replicaStatsQuery())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	directoryRecords, err := h.store.QueryDirectory(storage.DirectoryQuery{
		Kind:   "node",
		Search: strings.TrimSpace(firstNonEmptyDataString(req.Query, req.ProviderID, req.ProviderIDAlt, req.ProviderPeerID, req.ProviderPeerIDAlt)),
		Limit:  req.limitOrDefault(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	rows := providerSearchRows(directoryRecords, stats, req)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":   len(rows),
		"results": rows,
	})
}

func (h *SearchHandler) handleData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}
	req, ok := decodeSearchRequest(w, r)
	if !ok {
		return
	}
	stats, err := h.store.LocalReplicaStats(req.replicaStatsQuery())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows := localReplicaStatsRows(filterSearchStatsByText(stats, req.Query))
	sortSearchAPIRows(rows, "schema_name", "provider_id", "source_name", "batch_id")
	rows = limitSearchAPIRows(rows, req.limitOrDefault())
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":   len(rows),
		"results": rows,
	})
}

func (h *SearchHandler) ensureStore(w http.ResponseWriter) bool {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "storage is unavailable")
		return false
	}
	return true
}

func decodeSearchRequest(w http.ResponseWriter, r *http.Request) (searchRequest, bool) {
	var req searchRequest
	if r.Body != nil {
		defer r.Body.Close()
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON search request")
		return req, false
	}
	return req, true
}

func (r searchRequest) replicaStatsQuery() storage.LocalReplicaStatsQuery {
	return storage.LocalReplicaStatsQuery{
		SchemaName:     normalizeSearchAPISchema(firstNonEmptyDataString(r.Schema, r.SchemaName, r.SchemaNameAlt)),
		ProviderPeerID: firstNonEmptyDataString(r.ProviderPeerID, r.ProviderPeerIDAlt),
		ProviderID:     firstNonEmptyDataString(r.ProviderID, r.ProviderIDAlt),
		SourceName:     firstNonEmptyDataString(r.SourceName, r.SourceNameAlt),
		BatchID:        firstNonEmptyDataString(r.BatchID, r.BatchIDAlt),
		QueryProfile:   firstNonEmptyDataString(r.QueryProfile, r.QueryProfileAlt),
	}
}

func (r searchRequest) limitOrDefault() int {
	if r.Limit <= 0 {
		return 100
	}
	if r.Limit > 1000 {
		return 1000
	}
	return r.Limit
}

func normalizeSearchAPISchema(value string) string {
	schema := strings.TrimSpace(value)
	if schema == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(schema), ".fbs") {
		schema = schema[:len(schema)-len(".fbs")]
	}
	return strings.ToUpper(schema) + ".fbs"
}

func providerSearchRows(records []storage.DirectoryRecord, stats []storage.LocalReplicaStats, req searchRequest) []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(records)+len(stats))
	seenStats := map[string]bool{}
	for _, record := range records {
		matchingStats := searchStatsForDirectory(record, stats)
		if len(matchingStats) == 0 {
			row := providerDirectorySearchRow(record)
			if searchAPIRowContains(row, req.Query) {
				rows = append(rows, row)
			}
			continue
		}
		for _, stat := range matchingStats {
			row := providerDirectorySearchRow(record)
			addSearchAPIReplicaStats(row, stat)
			if searchAPIRowContains(row, req.Query) {
				rows = append(rows, row)
			}
			seenStats[searchAPIStatKey(stat)] = true
		}
	}
	for _, stat := range stats {
		if seenStats[searchAPIStatKey(stat)] {
			continue
		}
		row := map[string]interface{}{}
		addSearchAPIReplicaStats(row, stat)
		if searchAPIRowContains(row, req.Query) {
			rows = append(rows, row)
		}
	}
	sortSearchAPIRows(rows, "dn", "provider_id", "source_name", "schema_name")
	return limitSearchAPIRows(rows, req.limitOrDefault())
}

func providerDirectorySearchRow(record storage.DirectoryRecord) map[string]interface{} {
	return map[string]interface{}{
		"peer_id":         record.PeerID,
		"dn":              record.DN,
		"legal_name":      record.LegalName,
		"bitcoin_address": record.BitcoinAddress,
		"epm_cid":         record.EPMCID,
		"source":          record.Source,
		"updated_at":      formatSearchAPIUnix(record.UpdatedAt),
	}
}

func searchStatsForDirectory(record storage.DirectoryRecord, stats []storage.LocalReplicaStats) []storage.LocalReplicaStats {
	matches := make([]storage.LocalReplicaStats, 0, len(stats))
	for _, stat := range stats {
		if strings.TrimSpace(stat.ProviderPeerID) != "" && stat.ProviderPeerID == record.PeerID {
			matches = append(matches, stat)
		}
	}
	return matches
}

func filterSearchStatsByText(stats []storage.LocalReplicaStats, query string) []storage.LocalReplicaStats {
	if strings.TrimSpace(query) == "" {
		return stats
	}
	filtered := make([]storage.LocalReplicaStats, 0, len(stats))
	for _, stat := range stats {
		row := map[string]interface{}{}
		addSearchAPIReplicaStats(row, stat)
		if searchAPIRowContains(row, query) {
			filtered = append(filtered, stat)
		}
	}
	return filtered
}

func addSearchAPIReplicaStats(row map[string]interface{}, stat storage.LocalReplicaStats) {
	row["schema_name"] = stat.SchemaName
	row["provider_peer_id"] = stat.ProviderPeerID
	row["provider_public_key"] = stat.ProviderPublicKey
	row["provider_id"] = stat.ProviderID
	row["source_name"] = stat.SourceName
	row["batch_id"] = stat.BatchID
	row["query_profile"] = stat.QueryProfile
	row["local_rows"] = stat.LocalRows
	row["pinned_rows"] = stat.PinnedRows
	row["cached_bytes"] = stat.CachedBytes
	row["pinned_bytes"] = stat.PinnedBytes
	row["snapshot_id"] = stat.SnapshotID
	row["head"] = stat.Head
	row["high_water_mark"] = stat.HighWaterMark
	if !stat.LastSyncedAt.IsZero() {
		row["last_synced_at"] = stat.LastSyncedAt.UTC().Format(time.RFC3339)
	} else {
		row["last_synced_at"] = ""
	}
}

func searchAPIRowContains(row map[string]interface{}, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, value := range row {
		if strings.Contains(strings.ToLower(searchAPIValueString(value)), query) {
			return true
		}
	}
	return false
}

func sortSearchAPIRows(rows []map[string]interface{}, keys ...string) {
	sort.Slice(rows, func(i, j int) bool {
		return searchAPISortKey(rows[i], keys) < searchAPISortKey(rows[j], keys)
	})
}

func searchAPISortKey(row map[string]interface{}, keys []string) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strings.ToLower(searchAPIValueString(row[key])))
	}
	return strings.Join(parts, "\x00")
}

func limitSearchAPIRows(rows []map[string]interface{}, limit int) []map[string]interface{} {
	if limit <= 0 || limit >= len(rows) {
		return rows
	}
	return rows[:limit]
}

func searchAPIStatKey(stat storage.LocalReplicaStats) string {
	return strings.Join([]string{
		stat.SchemaName,
		stat.ProviderPeerID,
		stat.ProviderPublicKey,
		stat.ProviderID,
		stat.SourceName,
		stat.BatchID,
		stat.QueryProfile,
	}, "\x00")
}

func searchAPIValueString(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(raw)
	}
}

func formatSearchAPIUnix(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}
