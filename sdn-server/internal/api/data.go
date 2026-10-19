// Package api provides HTTP API endpoints for the SDN server.
package api

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// DataQueryHandler serves read-only, cache-friendly schema query APIs.
type DataQueryHandler struct {
	store *storage.FlatSQLStore
	// indexCache bounds the anonymous /api/v1/data/index read so it can never
	// queue behind the ingest writer. Keyed by the full query. See
	// boundedread.go.
	indexCache *boundedReader
	// publisher is the node's own identity and its held publisher profiles;
	// see SetPublisherIdentity (sources.go).
	publisher publisherIdentity
	// namedOnly are the lanes the unfiltered bulk lists leave out
	// (data_bulk_scope.go).
	namedOnly []config.LaneSelector
}

// NewDataQueryHandler creates a new data query handler with a RAM-only index
// cache: every cached page is lost on restart.
func NewDataQueryHandler(store *storage.FlatSQLStore) *DataQueryHandler {
	return NewDataQueryHandlerWithUICache(store, "")
}

// indexCacheFileName is the index cache's backing file inside the UI cache dir.
const indexCacheFileName = "data-index.json"

// NewDataQueryHandlerWithUICache creates a data query handler whose index cache
// is backed by uiCacheDir, so the pages this node served before a restart are
// served again from the FIRST request of the next boot instead of STORE_BUSY
// for the hour the store spends hydrating. An empty uiCacheDir is RAM-only.
func NewDataQueryHandlerWithUICache(store *storage.FlatSQLStore, uiCacheDir string) *DataQueryHandler {
	path := ""
	if uiCacheDir != "" {
		path = filepath.Join(uiCacheDir, indexCacheFileName)
	}
	return &DataQueryHandler{
		store:      store,
		indexCache: newBoundedReaderPersisted(boundedReaderDefaultKeys, path, decodeRecordIndexPageValue),
	}
}

// RegisterRoutes registers the NATIVE data API routes: health/summary
// introspection, the streaming CSV/JSONL bulk-export adapters, and the datasync
// record surface (scan/query/records — the sdn-js remote backend). The scan
// is FlatBuffer-only: $QRP request and page frames around verbatim records
// (data_scan.go).
//
// The record-serving "manual http" endpoints that used to live here
// (omm, omm/bulk, mpe, mpe/bulk, cat, cat/bulk, spw/bulk, secure/omm) were
// RETIRED in loop C.4: /api/v1/data/* record retrieval is served by the
// data-retrieval FLOW module mounted through the config mount table
// (config flows.mounts, default mount path "/api/v1/data/"). Param parsing,
// profile resolution, and FlatBuffer selection remain shared with that lane.
// The exact OMM/CAT bulk adapters below add schema-generic presentation
// encodings without buffering a full response. Exact-match native routes take
// precedence over the flow's subtree mount on http.ServeMux, so the two
// coexist on the same mux.
func (h *DataQueryHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/data/omm/bulk", h.handleOMMBulkExport)
	mux.HandleFunc("/api/v1/data/cat/bulk", h.handleCATBulkExport)
	mux.HandleFunc("/api/v1/data/health", h.handleHealth)
	mux.HandleFunc("/api/v1/data/index", h.handleRecordIndex)
	mux.HandleFunc("/api/v1/data/summary", h.handleSummary)
	// Resolved sources: summaries joined to signed publisher profiles — the
	// provenance chips' server lane (sources.go).
	h.RegisterResolvedSourcesRoute(mux)
	mux.HandleFunc("/api/v1/data/datastores", h.handleDatastores)
	mux.HandleFunc("/api/v1/data/scan", h.handleScan)
	mux.HandleFunc("/api/v1/data/query", h.handleDataQuery)
	mux.HandleFunc("/api/v1/data/local-replica-stats", h.handleLocalReplicaStats)
	mux.HandleFunc("/api/v1/data/records/", h.handleRawRecord)
}

func (h *DataQueryHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "health takes GET", 0)
		return
	}
	writeTablePage(w, qrpTable{
		Status:  QRPStatusOk,
		Columns: []qrpCol{{Name: "component", Type: colString}, {Name: "status", Type: colString}},
		Rows:    [][]qrpVal{{textVal("spaceaware-data-api"), textVal("ok")}},
		AsOfMs:  time.Now().UnixMilli(),
	})
}

// recordIndexMaxLimit caps a single index page at the query page's cap: an
// index row is a CID, a NORAD and an epoch, no record bytes.
const recordIndexMaxLimit = dataQueryMaxLimit

// handleRecordIndex serves an ANONYMOUS per-record index page for an explicitly
// selected SDS schema:
//
//	GET /api/v1/data/index?schema=OMM.fbs&source_name=&provider_id=&batch_id=&norad=&page=&limit=&cursor=
//	-> one $QRP{KIND=Page} frame: TOTAL_COUNT, PAGE, LIMIT, CURSOR, NEXT_CURSOR,
//	   STALE, AS_OF_MS, COLUMNS [cid string, norad int64, epoch_ms int64],
//	   ROWS (Null = absent)
//
// Rows are in store sequence order (the scan's and /data/query's), paged
// exactly as /data/query pages a raw lane: a page without cursor pins a
// snapshot at the sequence head (TOTAL_COUNT counts it) and serves its
// [(page-1)*limit, page*limit); NEXT_CURSOR (empty on the snapshot's last
// page) continues that snapshot after the page's last row, page unused.
//
// It reads sdn_record_index (joined to sdn_record_source_tags for the source
// lane filter) — no FlatBuffer payloads are hydrated. norad is a NORAD substring
// (digits only), matched via CAST(norad_cat_id AS TEXT) LIKE. Read-only.
func (h *DataQueryHandler) handleRecordIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "index takes GET", 0)
		return
	}
	if h.store == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "storage_unavailable", "local storage unavailable in edge mode", 0)
		return
	}

	values := r.URL.Query()
	schema := normalizeRecordIndexSchema(firstNonEmptyDataString(values.Get("schema"), values.Get("schema_name"), values.Get("schemaName")))
	if schema == "" {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "schema is required", 0)
		return
	}

	page := parsePositiveIntParam(values.Get("page"), 1)
	limit := parsePositiveIntParam(values.Get("limit"), 50)
	if limit > recordIndexMaxLimit {
		limit = recordIndexMaxLimit
	}

	query := storage.RecordIndexSeqQuery{RecordIndexPageQuery: storage.RecordIndexPageQuery{
		SchemaName: schema,
		ProviderID: firstNonEmptyDataString(values.Get("provider_id"), values.Get("providerId")),
		SourceName: firstNonEmptyDataString(values.Get("source_name"), values.Get("sourceName")),
		BatchID:    firstNonEmptyDataString(values.Get("batch_id"), values.Get("batchId")),
		NoradLike:  digitsOnly(values.Get("norad")),
		Limit:      limit,
		Offset:     (page - 1) * limit,
	}}
	laneHash := recordIndexLaneHash(query.RecordIndexPageQuery)
	if raw := strings.TrimSpace(values.Get("cursor")); raw != "" {
		cursor, ok, err := datasync.ParseRawRecordCursor(raw)
		if err != nil || !ok || cursor.MaxRowID <= 0 || cursor.QueryHash != laneHash {
			WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "cursor is not a NEXT_CURSOR of this index query; restart from the first page", 0)
			return
		}
		query.AfterSeq, query.ThroughSeq, query.Offset = cursor.AfterRowID, cursor.MaxRowID, 0
	}

	// BOUNDED. This is an anonymous read of the single-writer record store: a
	// running ingest holds the write lock for minutes and every reader queues
	// behind it. Measured on host-01 mid-ingest 2026-07-28, this endpoint did
	// not answer within 30 s. It now answers from the last page it served for
	// this exact query, and refreshes off the hot path. See boundedread.go.
	// A partitioned store (formats 2 and 4) answers the page from readers
	// that wait on no writer: read directly (T6 scope 6).
	indexCache := h.indexCache
	if h.store.PartitionedRecords() {
		indexCache = nil
	}
	res := indexCache.read(recordIndexCacheKey(query), storeReadBudget, storeReadMinRefresh, func() (interface{}, error) {
		page, err := h.store.RecordIndexSeqPage(query)
		if err != nil {
			return nil, err
		}
		return &page, nil
	})

	if !res.OK {
		// Never read successfully: the store has been busy since boot for this
		// query. Say so. Returning an empty page would be a lie indistinguishable
		// from "this schema holds no records", and a UI would render "none".
		writeStatusFrame(w, http.StatusServiceUnavailable, QRPStatusBusy, "STORE_BUSY",
			"record store is busy; no cached page for this query yet", time.Second)
		return
	}

	pageData, _ := res.Value.(*recordIndexPage)
	if pageData == nil {
		pageData = &recordIndexPage{}
	}
	encodeCursor := func(after int64) string {
		return datasync.EncodeRawRecordCursor(after, pageData.Through, "index-"+strconv.FormatInt(pageData.Through, 10), laneHash)
	}
	cursor, nextCursor := encodeCursor(query.AfterSeq), ""
	if n := len(pageData.Rows); n > 0 {
		cursor = encodeCursor(pageData.Rows[0].Seq - 1)
		if last := pageData.Rows[n-1].Seq; n >= limit && last < pageData.Through {
			nextCursor = encodeCursor(last)
		}
	}
	rows := make([][]QRPCell, 0, len(pageData.Rows))
	for _, rr := range pageData.Rows {
		row := []QRPCell{{IsText: true, Text: rr.CID}, {}, {}}
		if rr.NoradCatID != nil {
			row[1] = QRPCell{IsInt: true, Int: int64(*rr.NoradCatID)}
		}
		if rr.EpochUnix != nil {
			row[2] = QRPCell{IsInt: true, Int: *rr.EpochUnix * 1000}
		}
		rows = append(rows, row)
	}
	fields := QRPFields{
		Kind:          QRPKindPage,
		Status:        QRPStatusOk,
		SchemaName:    schema,
		Page:          uint32(page),
		Limit:         uint32(limit),
		TotalCount:    uint64(pageData.Total),
		Cursor:        cursor,
		NextCursor:    nextCursor,
		Stale:         !res.Fresh,
		GeneratedAtMs: time.Now().UnixMilli(),
		Columns:       []QRPColumnField{{Name: "cid", Type: "string"}, {Name: "norad", Type: "int64"}, {Name: "epoch_ms", Type: "int64"}},
		Rows:          rows,
	}
	if !res.AsOf.IsZero() {
		fields.AsOfMs = res.AsOf.UnixMilli()
	}
	WriteFrameStream(w, http.StatusOK, [][]byte{BuildQRP(fields)}, map[string]string{StreamSchemaHeader: schema})
}

// recordIndexCacheKey identifies one index query. Every filter participates —
// two different filters are two different answers, and sharing a cache slot
// between them would serve one query's rows under another's parameters.
func recordIndexCacheKey(q storage.RecordIndexSeqQuery) string {
	return strings.Join([]string{
		q.SchemaName, q.ProviderID, q.SourceName, q.BatchID, q.NoradLike,
		strconv.Itoa(q.Limit), strconv.Itoa(q.Offset),
		strconv.FormatInt(q.AfterSeq, 10), strconv.FormatInt(q.ThroughSeq, 10),
	}, "\x00")
}

// recordIndexPage is one cached answer of the index surface (the bounded
// read's value, restored by boundedpersist.go): the rows, the total and the
// snapshot head they were read against, kept together so a stale answer is
// internally consistent.
type recordIndexPage = storage.RecordIndexSeqPage

// recordIndexLaneHash binds an index cursor to its query's filters, so a
// cursor of one lane (or of /data/query) never pages another.
func recordIndexLaneHash(q storage.RecordIndexPageQuery) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"index", q.SchemaName, q.ProviderID, q.SourceName, q.BatchID, q.NoradLike,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// normalizeRecordIndexSchema maps a caller schema alias to the canonical stored
// schema_name (e.g. "OMM" -> "OMM.fbs"). An empty value remains empty so the
// caller can reject implicit application-specific defaults.
func normalizeRecordIndexSchema(schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return ""
	}
	if !strings.Contains(schema, ".") {
		return schema + ".fbs"
	}
	return schema
}

// parsePositiveIntParam parses a positive integer query param, falling back to
// def for empty/invalid/non-positive input.
func parsePositiveIntParam(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def
	}
	return n
}

// digitsOnly strips every non-digit rune — NORAD IDs are numeric, and this keeps
// LIKE wildcards (% _) out of the substring filter.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// handleSummary answers one page: a row per (schema, source lane) with the
// lane's records and bytes, then a schema-total row per schema (source
// columns empty), then the store total (schema empty).
func (h *DataQueryHandler) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "summary takes GET", 0)
		return
	}
	if !h.ensureStore(w) {
		return
	}
	summary, err := h.store.DataSummary()
	if err != nil {
		writeDataReadError(w, err)
		return
	}
	cols := []qrpCol{
		{Name: "schema_name", Type: colString}, {Name: "provider_id", Type: colString}, {Name: "source_name", Type: colString},
		{Name: "batch_id", Type: colString}, {Name: "producer_peer_id", Type: colString}, {Name: "producer_public_key", Type: colString},
		{Name: "count", Type: colInt64}, {Name: "total_bytes", Type: colInt64, Unit: "B"},
	}
	rows := make([][]qrpVal, 0, len(summary.Sources)+len(summary.Schemas)+1)
	for _, src := range summary.Sources {
		rows = append(rows, []qrpVal{textVal(src.SchemaName), textVal(src.ProviderID), textVal(src.SourceName),
			textVal(src.BatchID), textVal(src.ProducerPeerID), textVal(src.ProducerPublicKey), intVal(src.Count), intVal(src.TotalBytes)})
	}
	for _, sch := range summary.Schemas {
		rows = append(rows, []qrpVal{textVal(sch.SchemaName), textVal(""), textVal(""), textVal(""), textVal(""), textVal(""),
			intVal(sch.Count), intVal(sch.TotalBytes)})
	}
	rows = append(rows, []qrpVal{textVal(""), textVal(""), textVal(""), textVal(""), textVal(""), textVal(""),
		intVal(summary.TotalRecords), intVal(summary.TotalBytes)})
	writeTablePage(w, qrpTable{Status: QRPStatusOk, Columns: cols, Rows: rows, TotalCount: uint64(summary.TotalRecords), AsOfMs: time.Now().UnixMilli()})
}

func (h *DataQueryHandler) handleDatastores(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "datastores takes GET", 0)
		return
	}
	if !h.ensureStore(w) {
		return
	}
	entries, err := h.store.ListDatastoreIdentities()
	if err != nil {
		writeDataReadError(w, err)
		return
	}
	names := []string{"key", "identity_schema_name", "identity_source_peer_id", "identity_source_public_key", "identity_provider_id",
		"identity_source_name", "identity_batch_head", "identity_query_profile", "identity_canonical_params", "identity_snapshot_id",
		"identity_high_water_mark", "identity_artifact_hash"}
	cols := make([]qrpCol, 0, len(names)+1)
	for _, name := range names {
		cols = append(cols, qrpCol{Name: name, Type: colString})
	}
	cols = append(cols, qrpCol{Name: "updated_at", Type: colInt64, Unit: "s"})
	rows := make([][]qrpVal, 0, len(entries))
	for _, e := range entries {
		id := e.Identity
		params := make([]string, 0, len(id.CanonicalParams))
		for k, v := range id.CanonicalParams {
			params = append(params, k+"="+v)
		}
		sort.Strings(params)
		rows = append(rows, []qrpVal{textVal(e.Key), textVal(id.SchemaName), textVal(id.SourcePeerID), textVal(id.SourcePublicKey),
			textVal(id.ProviderID), textVal(id.SourceName), textVal(id.BatchHead), textVal(id.QueryProfile),
			textVal(strings.Join(params, ", ")), textVal(id.SnapshotID), textVal(id.HighWaterMark), textVal(id.ArtifactHash),
			intVal(e.UpdatedAt)})
	}
	writeTablePage(w, qrpTable{Status: QRPStatusOk, Columns: cols, Rows: rows, TotalCount: uint64(len(rows)), AsOfMs: time.Now().UnixMilli()})
}

func (h *DataQueryHandler) handleLocalReplicaStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "local-replica-stats takes GET", 0)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	values := r.URL.Query()
	stats, err := h.store.LocalReplicaStats(storage.LocalReplicaStatsQuery{
		SchemaName:        firstNonEmptyDataString(values.Get("schema"), values.Get("schema_name"), values.Get("schemaName")),
		ProviderPeerID:    firstNonEmptyDataString(values.Get("provider_peer_id"), values.Get("providerPeerId"), values.Get("producer_peer_id"), values.Get("producerPeerId")),
		ProviderPublicKey: firstNonEmptyDataString(values.Get("provider_public_key"), values.Get("providerPublicKey"), values.Get("producer_public_key"), values.Get("producerPublicKey")),
		ProviderID:        firstNonEmptyDataString(values.Get("provider_id"), values.Get("providerId")),
		SourceName:        firstNonEmptyDataString(values.Get("source_name"), values.Get("sourceName")),
		BatchID:           firstNonEmptyDataString(values.Get("batch_id"), values.Get("batchId")),
		QueryProfile:      firstNonEmptyDataString(values.Get("query_profile"), values.Get("queryProfile")),
	})
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", err.Error(), 0)
		return
	}
	cols := []qrpCol{
		{Name: "schema_name", Type: colString}, {Name: "provider_peer_id", Type: colString}, {Name: "provider_public_key", Type: colString},
		{Name: "provider_id", Type: colString}, {Name: "source_name", Type: colString}, {Name: "batch_id", Type: colString},
		{Name: "query_profile", Type: colString}, {Name: "local_rows", Type: colInt64}, {Name: "pinned_rows", Type: colInt64},
		{Name: "cached_bytes", Type: colInt64, Unit: "B"}, {Name: "pinned_bytes", Type: colInt64, Unit: "B"},
		{Name: "snapshot_id", Type: colString}, {Name: "head", Type: colString}, {Name: "high_water_mark", Type: colString},
		{Name: "last_synced_at", Type: colInt64, Unit: "ms"},
	}
	rows := make([][]qrpVal, 0, len(stats))
	for _, st := range stats {
		rows = append(rows, []qrpVal{textVal(st.SchemaName), textVal(st.ProviderPeerID), textVal(st.ProviderPublicKey),
			textVal(st.ProviderID), textVal(st.SourceName), textVal(st.BatchID), textVal(st.QueryProfile),
			intVal(st.LocalRows), intVal(st.PinnedRows), intVal(st.CachedBytes), intVal(st.PinnedBytes),
			textVal(st.SnapshotID), textVal(st.Head), textVal(st.HighWaterMark), timeMsOrNull(st.LastSyncedAt)})
	}
	writeTablePage(w, qrpTable{Status: QRPStatusOk, Columns: cols, Rows: rows, TotalCount: uint64(len(rows)), AsOfMs: time.Now().UnixMilli()})
}

func (h *DataQueryHandler) handleRawRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "records takes GET or HEAD", 0)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/data/records/")
	parts := strings.SplitN(suffix, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "expected /api/v1/data/records/{schema}/{cid}", 0)
		return
	}
	schemaName, err := url.PathUnescape(parts[0])
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "invalid schema", 0)
		return
	}
	recordID, err := url.PathUnescape(parts[1])
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "invalid record id", 0)
		return
	}

	record, err := h.store.GetRawRecord(schemaName, recordID)
	if err != nil {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", err.Error(), 0)
		return
	}
	w.Header().Set("Content-Type", "application/x-flatbuffers")
	w.Header().Set("X-SDN-Schema", schemaName)
	w.Header().Set("X-SDN-Record-ID", record.CID)
	w.Header().Set("X-SDN-Peer-ID", record.PeerID)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(record.Data)
	}
}

func localReplicaStatsRows(stats []storage.LocalReplicaStats) []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(stats))
	for _, stat := range stats {
		lastSyncedAt := ""
		if !stat.LastSyncedAt.IsZero() {
			lastSyncedAt = stat.LastSyncedAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, map[string]interface{}{
			"schema_name":         stat.SchemaName,
			"provider_peer_id":    stat.ProviderPeerID,
			"provider_public_key": stat.ProviderPublicKey,
			"provider_id":         stat.ProviderID,
			"source_name":         stat.SourceName,
			"batch_id":            stat.BatchID,
			"query_profile":       stat.QueryProfile,
			"local_rows":          stat.LocalRows,
			"pinned_rows":         stat.PinnedRows,
			"cached_bytes":        stat.CachedBytes,
			"pinned_bytes":        stat.PinnedBytes,
			"snapshot_id":         stat.SnapshotID,
			"head":                stat.Head,
			"high_water_mark":     stat.HighWaterMark,
			"last_synced_at":      lastSyncedAt,
		})
	}
	return rows
}

func firstNonEmptyDataString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (h *DataQueryHandler) ensureStore(w http.ResponseWriter) bool {
	if h.store == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "storage_unavailable", "local storage unavailable in edge mode", 0)
		return false
	}
	return true
}

func (h *DataQueryHandler) storeForDatastoreKey(key string) (*storage.FlatSQLStore, func(), error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return h.store, func() {}, nil
	}
	store, err := h.store.OpenRegisteredDatastore(key)
	if err != nil {
		return nil, nil, err
	}
	return store, func() { _ = store.Close() }, nil
}

func parseLimit(r *http.Request, defaultValue, maxValue int) int {
	limit := defaultValue
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return limit
	}
	if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
		limit = parsed
	}
	if limit > maxValue {
		limit = maxValue
	}
	return limit
}

type bulkExportFormat string

const (
	bulkFormatFlatBuffer bulkExportFormat = "flatbuffer"
	bulkFormatJSON       bulkExportFormat = "json"
	bulkFormatCSV        bulkExportFormat = "csv"
	bulkFormatJSONL      bulkExportFormat = "jsonl"
)

func requestedBulkExportFormat(r *http.Request) (bulkExportFormat, error) {
	// Accept is deliberately ignored: the public bulk-lane contract selects
	// presentation encodings only through the format query parameter.
	switch value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))); value {
	case "", "flatbuffer", "flatbuffers", "binary", "fbs", "fb":
		return bulkFormatFlatBuffer, nil
	case "json", "application/json":
		return bulkFormatJSON, nil
	case "csv", "text/csv":
		return bulkFormatCSV, nil
	case "jsonl", "ndjson", "application/x-ndjson":
		return bulkFormatJSONL, nil
	default:
		return "", fmt.Errorf("unsupported format %q; use flatbuffer, json, csv, or jsonl", value)
	}
}

func (h *DataQueryHandler) handleOMMBulkExport(w http.ResponseWriter, r *http.Request) {
	h.handleBulkExport(w, r, "OMM.fbs", "omm")
}

func (h *DataQueryHandler) handleCATBulkExport(w http.ResponseWriter, r *http.Request) {
	h.handleBulkExport(w, r, "CAT.fbs", "cat")
}

func (h *DataQueryHandler) handleBulkExport(w http.ResponseWriter, r *http.Request, schemaName, standard string) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "bulk export takes GET", 0)
		return
	}
	format, err := requestedBulkExportFormat(r)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", err.Error()+".", 0)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	records, err := h.bulkExportRecords(r, schemaName)
	if err != nil {
		writeDataReadError(w, err)
		return
	}
	w.Header().Set("X-SDN-Schema", schemaName)
	w.Header().Set("X-SDN-Record-Count", strconv.Itoa(len(records)))
	w.Header().Set("ETag", bulkExportETag(schemaName, records))
	if match := strings.TrimSpace(r.Header.Get("If-None-Match")); match != "" && match == w.Header().Get("ETag") {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if format == bulkFormatFlatBuffer {
		writeFlatBufferRecordStreamWithContentType(w, schemaName, records, ContentTypeFlatBufferStream, h.store)
		return
	}

	binding, err := bulkBindingForSchema(schemaName)
	if err != nil {
		WriteErrorFrame(w, http.StatusInternalServerError, "internal", err.Error(), 0)
		return
	}

	switch format {
	case bulkFormatJSON:
		w.Header().Set("Content-Type", ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		_ = writeBulkJSONArray(w, binding, records)
	case bulkFormatCSV:
		w.Header().Set("Content-Type", ContentTypeCSV)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", bulkExportFilename(standard, bulkExportAsOf(r, records), "csv")))
		w.WriteHeader(http.StatusOK)
		_ = writeBulkCSV(w, binding, records)
	case bulkFormatJSONL:
		w.Header().Set("Content-Type", ContentTypeJSONL)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", bulkExportFilename(standard, bulkExportAsOf(r, records), "jsonl")))
		w.WriteHeader(http.StatusOK)
		_ = writeBulkJSONLines(w, binding, records)
	}
}

func (h *DataQueryHandler) bulkExportRecords(r *http.Request, schemaName string) ([]*storage.Record, error) {
	values := r.URL.Query()
	limit := parseLimit(r, 50000, 250000)
	providerID := firstNonEmptyDataString(values.Get("provider_id"), values.Get("providerId"))
	sourceName := firstNonEmptyDataString(values.Get("source"), values.Get("source_name"), values.Get("sourceName"))
	batchID := firstNonEmptyDataString(values.Get("batch_id"), values.Get("batchId"))

	if schemaName == "OMM.fbs" {
		profile := firstNonEmptyDataString(values.Get("profile"), values.Get("mode"))
		profile = strings.TrimPrefix(profile, "epoch.")
		if profile == "" {
			profile = "nearest"
		}
		query := storage.EpochRecordQuery{
			SchemaName: schemaName,
			Profile:    "epoch." + profile,
			ProviderID: providerID,
			SourceName: sourceName,
			BatchID:    batchID,
			Limit:      limit,
		}
		if day := strings.TrimSpace(values.Get("day")); day != "" {
			if _, err := time.Parse("2006-01-02", day); err != nil {
				return nil, fmt.Errorf("invalid day (expected YYYY-MM-DD)")
			}
			query.Profile = storage.EpochProfileDay
			query.Day = day
		} else if fromRaw, toRaw := strings.TrimSpace(values.Get("from")), strings.TrimSpace(values.Get("to")); fromRaw != "" || toRaw != "" {
			from, err := time.Parse(time.RFC3339, fromRaw)
			if err != nil {
				return nil, fmt.Errorf("invalid from (expected RFC3339)")
			}
			to, err := time.Parse(time.RFC3339, toRaw)
			if err != nil {
				return nil, fmt.Errorf("invalid to (expected RFC3339)")
			}
			query.Profile = storage.EpochProfileWindow
			query.From = &from
			query.To = &to
		} else {
			at, err := parseBulkExportEpoch(firstNonEmptyDataString(values.Get("epoch"), values.Get("at")))
			if err != nil {
				return nil, err
			}
			query.At = at
		}
		var matches []storage.EpochRecordMatch
		var err error
		if providerID == "" && sourceName == "" && batchID == "" {
			lanes, hidden, laneErr := h.unfilteredBulkLanes(schemaName)
			if laneErr == nil && hidden {
				matches, err = h.queryEpochLanes(query, lanes)
			} else {
				matches, err = h.store.QueryEpochRecords(query)
			}
		} else {
			matches, err = h.store.QueryEpochRecords(query)
		}
		if err != nil {
			return nil, err
		}
		records := make([]*storage.Record, 0, len(matches))
		for _, match := range matches {
			records = append(records, match.Record)
		}
		return records, nil
	}

	filter := storage.IndexedRecordQuery{
		SchemaName:          schemaName,
		ProviderID:          providerID,
		SourceName:          sourceName,
		BatchID:             batchID,
		Day:                 strings.TrimSpace(values.Get("day")),
		Limit:               limit,
		AllowLargeResultSet: true,
	}
	if filter.Day != "" {
		if _, err := time.Parse("2006-01-02", filter.Day); err != nil {
			return nil, fmt.Errorf("invalid day (expected YYYY-MM-DD)")
		}
	}
	if fromRaw := strings.TrimSpace(values.Get("from")); fromRaw != "" {
		from, err := time.Parse(time.RFC3339, fromRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid from (expected RFC3339)")
		}
		filter.From = &from
	}
	if toRaw := strings.TrimSpace(values.Get("to")); toRaw != "" {
		to, err := time.Parse(time.RFC3339, toRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid to (expected RFC3339)")
		}
		filter.To = &to
	}
	return h.store.QueryIndexedRecords(filter)
}

func parseBulkExportEpoch(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC(), nil
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		whole, fraction := mathModf(seconds)
		return time.Unix(int64(whole), int64(fraction*float64(time.Second))).UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid epoch (expected RFC3339 or unix seconds)")
	}
	return parsed.UTC(), nil
}

// mathModf is the small, dependency-free subset of math.Modf needed for unix
// timestamps with fractional seconds.
func mathModf(value float64) (float64, float64) {
	whole := float64(int64(value))
	return whole, value - whole
}

func bulkExportETag(schemaName string, records []*storage.Record) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, schemaName)
	for _, record := range records {
		_, _ = io.WriteString(hash, "\x00"+record.CID)
	}
	return `"` + hex.EncodeToString(hash.Sum(nil)) + `"`
}

func bulkExportAsOf(r *http.Request, records []*storage.Record) string {
	values := r.URL.Query()
	if raw := firstNonEmptyDataString(values.Get("as-of"), values.Get("as_of"), values.Get("day")); raw != "" {
		return sanitizeBulkFilenamePart(raw)
	}
	if raw := firstNonEmptyDataString(values.Get("epoch"), values.Get("at")); raw != "" {
		if parsed, err := parseBulkExportEpoch(raw); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	latest := time.Time{}
	for _, record := range records {
		candidate := record.MaterializedAt
		if candidate.IsZero() {
			candidate = record.Timestamp
		}
		if candidate.After(latest) {
			latest = candidate
		}
	}
	if latest.IsZero() {
		latest = time.Now().UTC()
	}
	return latest.UTC().Format("2006-01-02")
}

func sanitizeBulkFilenamePart(value string) string {
	var out strings.Builder
	for _, char := range strings.TrimSpace(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			out.WriteRune(char)
		}
	}
	if out.Len() == 0 {
		return "latest"
	}
	return out.String()
}

func bulkExportFilename(standard, asOf, extension string) string {
	return sanitizeBulkFilenamePart(strings.ToLower(standard)) + "-" + sanitizeBulkFilenamePart(asOf) + "." + extension
}

type bulkBindingFieldKind uint8

const (
	bulkBindingScalar bulkBindingFieldKind = iota
	bulkBindingTable
	bulkBindingVector
	bulkBindingTableVector
	bulkBindingUnion
)

type bulkBindingField struct {
	name         string
	getter       reflect.Method
	lengthGetter reflect.Method
	kind         bulkBindingFieldKind
	childType    reflect.Type
	line         int
}

type bulkBinding struct {
	schemaName string
	rootType   reflect.Type
	fields     []bulkBindingField
	decode     func([]byte) (interface{}, error)
}

var (
	bulkBindings    sync.Map // schema name -> *bulkBinding
	bindingFieldsBy sync.Map // reflect.Type -> []bulkBindingField
)

func bulkBindingForSchema(schemaName string) (*bulkBinding, error) {
	if cached, ok := bulkBindings.Load(schemaName); ok {
		return cached.(*bulkBinding), nil
	}
	binding := &bulkBinding{schemaName: schemaName}
	switch schemaName {
	case "OMM.fbs":
		binding.rootType = reflect.TypeOf((*OMM.OMM)(nil))
		binding.decode = func(data []byte) (interface{}, error) { return decodeOMM(data) }
	case "CAT.fbs":
		binding.rootType = reflect.TypeOf((*CAT.CAT)(nil))
		binding.decode = func(data []byte) (interface{}, error) { return decodeCAT(data) }
	default:
		return nil, fmt.Errorf("no generated Go binding registered for %s", schemaName)
	}
	fields, err := generatedBindingFields(binding.rootType)
	if err != nil {
		return nil, err
	}
	binding.fields = fields
	actual, _ := bulkBindings.LoadOrStore(schemaName, binding)
	return actual.(*bulkBinding), nil
}

// generatedBindingFields discovers the IDL accessors from the generated Go
// binding and orders them by their compiled source-line metadata. The Go
// reflection API exposes methods alphabetically; the source line is what
// preserves the FlatBuffers generator's schema declaration order without a
// second, hand-maintained list.
func generatedBindingFields(rootType reflect.Type) ([]bulkBindingField, error) {
	if cached, ok := bindingFieldsBy.Load(rootType); ok {
		return cached.([]bulkBindingField), nil
	}
	if rootType.Kind() != reflect.Ptr || rootType.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("generated binding root must be a pointer to struct, got %s", rootType)
	}
	fields := make([]bulkBindingField, 0, rootType.NumMethod())
	for index := 0; index < rootType.NumMethod(); index++ {
		method := rootType.Method(index)
		if !isGeneratedIDLAccessor(method.Name) {
			continue
		}
		field := bulkBindingField{name: method.Name, getter: method}
		if fn := runtime.FuncForPC(method.Func.Pointer()); fn != nil {
			_, field.line = fn.FileLine(method.Func.Pointer())
		}
		lengthGetter, hasLength := rootType.MethodByName(method.Name + "Length")
		switch method.Type.NumIn() {
		case 1:
			if method.Type.NumOut() != 1 {
				continue
			}
			field.kind = bulkBindingScalar
		case 2:
			if method.Type.NumOut() != 1 {
				continue
			}
			argument := method.Type.In(1)
			result := method.Type.Out(0)
			switch {
			case hasLength && argument.Kind() == reflect.Int:
				field.kind = bulkBindingVector
				field.lengthGetter = lengthGetter
			case argument.Kind() == reflect.Ptr && result.Kind() == reflect.Ptr:
				field.kind = bulkBindingTable
				field.childType = argument
			case argument.Kind() == reflect.Ptr && result.Kind() == reflect.Bool:
				field.kind = bulkBindingUnion
				field.childType = argument
			default:
				continue
			}
		case 3:
			if !hasLength || method.Type.In(1).Kind() != reflect.Ptr || method.Type.In(2).Kind() != reflect.Int ||
				method.Type.NumOut() != 1 || method.Type.Out(0).Kind() != reflect.Bool {
				continue
			}
			field.kind = bulkBindingTableVector
			field.childType = method.Type.In(1)
			field.lengthGetter = lengthGetter
		default:
			continue
		}
		fields = append(fields, field)
	}
	sort.SliceStable(fields, func(i, j int) bool {
		if fields[i].line == fields[j].line {
			return fields[i].name < fields[j].name
		}
		return fields[i].line < fields[j].line
	})
	if len(fields) == 0 {
		return nil, fmt.Errorf("generated binding %s exposes no IDL accessors", rootType)
	}
	bindingFieldsBy.Store(rootType, fields)
	return fields, nil
}

func isGeneratedIDLAccessor(name string) bool {
	if strings.HasPrefix(name, "Mutate") || strings.HasSuffix(name, "Length") || strings.HasSuffix(name, "Bytes") {
		return false
	}
	if name == strings.ToUpper(name) {
		return name != ""
	}
	return strings.HasSuffix(name, "_type") && strings.TrimSuffix(name, "_type") == strings.ToUpper(strings.TrimSuffix(name, "_type"))
}

func bindingObject(root interface{}, fields []bulkBindingField) (map[string]interface{}, error) {
	value := reflect.ValueOf(root)
	if !value.IsValid() || (value.Kind() == reflect.Ptr && value.IsNil()) {
		return nil, nil
	}
	object := make(map[string]interface{}, len(fields))
	for _, field := range fields {
		fieldValue, err := bindingFieldValue(value, field)
		if err != nil {
			return nil, err
		}
		object[field.name] = fieldValue
	}
	return object, nil
}

func bindingFieldValue(root reflect.Value, field bulkBindingField) (interface{}, error) {
	switch field.kind {
	case bulkBindingScalar:
		result := field.getter.Func.Call([]reflect.Value{root})[0]
		return exportedBindingScalar(result), nil
	case bulkBindingTable:
		child := reflect.New(field.childType.Elem())
		result := field.getter.Func.Call([]reflect.Value{root, child})[0]
		if result.IsNil() {
			return nil, nil
		}
		fields, err := generatedBindingFields(field.childType)
		if err != nil {
			return nil, err
		}
		return bindingObject(result.Interface(), fields)
	case bulkBindingVector:
		length := int(field.lengthGetter.Func.Call([]reflect.Value{root})[0].Int())
		values := make([]interface{}, 0, length)
		for index := 0; index < length; index++ {
			result := field.getter.Func.Call([]reflect.Value{root, reflect.ValueOf(index)})[0]
			values = append(values, exportedBindingScalar(result))
		}
		return values, nil
	case bulkBindingTableVector:
		length := int(field.lengthGetter.Func.Call([]reflect.Value{root})[0].Int())
		values := make([]interface{}, 0, length)
		childFields, err := generatedBindingFields(field.childType)
		if err != nil {
			return nil, err
		}
		for index := 0; index < length; index++ {
			child := reflect.New(field.childType.Elem())
			ok := field.getter.Func.Call([]reflect.Value{root, child, reflect.ValueOf(index)})[0].Bool()
			if !ok {
				values = append(values, nil)
				continue
			}
			object, err := bindingObject(child.Interface(), childFields)
			if err != nil {
				return nil, err
			}
			values = append(values, object)
		}
		return values, nil
	case bulkBindingUnion:
		// The generated union discriminator is exported as its adjacent *_type
		// field. A union table has no schema-independent concrete Go type, so its
		// value remains null here instead of being guessed from raw table bytes.
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported generated binding field %s", field.name)
	}
}

func exportedBindingScalar(value reflect.Value) interface{} {
	if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
		return string(value.Bytes())
	}
	if value.CanInterface() {
		if stringer, ok := value.Interface().(fmt.Stringer); ok {
			return stringer.String()
		}
		return value.Interface()
	}
	switch value.Kind() {
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	case reflect.String:
		return value.String()
	default:
		return nil
	}
}

func bindingCSVHeader(fields []bulkBindingField) ([]string, error) {
	return appendBindingCSVHeader(nil, "", fields)
}

func appendBindingCSVHeader(header []string, prefix string, fields []bulkBindingField) ([]string, error) {
	for _, field := range fields {
		name := prefix + field.name
		if field.kind != bulkBindingTable {
			header = append(header, name)
			continue
		}
		children, err := generatedBindingFields(field.childType)
		if err != nil {
			return nil, err
		}
		header, err = appendBindingCSVHeader(header, name+".", children)
		if err != nil {
			return nil, err
		}
	}
	return header, nil
}

func bindingCSVValues(fields []bulkBindingField, object map[string]interface{}) ([]string, error) {
	values := make([]string, 0, len(fields))
	return appendBindingCSVValues(values, fields, object)
}

func appendBindingCSVValues(values []string, fields []bulkBindingField, object map[string]interface{}) ([]string, error) {
	for _, field := range fields {
		value := object[field.name]
		if field.kind == bulkBindingTable {
			children, err := generatedBindingFields(field.childType)
			if err != nil {
				return nil, err
			}
			if child, ok := value.(map[string]interface{}); ok {
				values, err = appendBindingCSVValues(values, children, child)
				if err != nil {
					return nil, err
				}
				continue
			}
			childHeader, err := bindingCSVHeader(children)
			if err != nil {
				return nil, err
			}
			for range childHeader {
				values = append(values, "")
			}
			continue
		}
		if value == nil {
			values = append(values, "")
			continue
		}
		if field.kind == bulkBindingVector || field.kind == bulkBindingTableVector {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			values = append(values, string(encoded))
			continue
		}
		values = append(values, fmt.Sprint(value))
	}
	return values, nil
}

func writeBulkCSV(writer io.Writer, binding *bulkBinding, records []*storage.Record) error {
	header, err := bindingCSVHeader(binding.fields)
	if err != nil {
		return err
	}
	csvWriter := csv.NewWriter(writer)
	csvWriter.UseCRLF = true
	if err := csvWriter.Write(header); err != nil {
		return err
	}
	for _, record := range records {
		root, err := binding.decode(record.Data)
		if err != nil {
			return err
		}
		object, err := bindingObject(root, binding.fields)
		if err != nil {
			return err
		}
		row, err := bindingCSVValues(binding.fields, object)
		if err != nil {
			return err
		}
		if err := csvWriter.Write(row); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func writeBulkJSONArray(writer io.Writer, binding *bulkBinding, records []*storage.Record) error {
	if _, err := io.WriteString(writer, "["); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for index, record := range records {
		if index > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		root, err := binding.decode(record.Data)
		if err != nil {
			return err
		}
		object, err := bindingObject(root, binding.fields)
		if err != nil {
			return err
		}
		if err := encoder.Encode(object); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "]")
	return err
}

func writeBulkJSONLines(writer io.Writer, binding *bulkBinding, records []*storage.Record) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, record := range records {
		root, err := binding.decode(record.Data)
		if err != nil {
			return err
		}
		object, err := bindingObject(root, binding.fields)
		if err != nil {
			return err
		}
		if err := encoder.Encode(object); err != nil {
			return err
		}
	}
	return nil
}

func writeFlatBufferRecordStreamWithContentType(w http.ResponseWriter, schema string, records []*storage.Record, contentType string, store *storage.FlatSQLStore) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-SDN-Schema", schema)
	w.Header().Set("X-SDN-Record-Count", strconv.Itoa(len(records)))
	w.Header().Set("X-SDN-Stream-Format", "flatsql-size-prefixed-le-u32")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if err := store.WriteRawRecordFrames(w, records); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func decodeOMM(data []byte) (*OMM.OMM, error) {
	switch {
	case OMM.SizePrefixedOMMBufferHasIdentifier(data):
		return OMM.GetSizePrefixedRootAsOMM(data, 0), nil
	case OMM.OMMBufferHasIdentifier(data):
		return OMM.GetRootAsOMM(data, 0), nil
	default:
		return nil, fmt.Errorf("invalid OMM buffer")
	}
}

func decodeCAT(data []byte) (*CAT.CAT, error) {
	switch {
	case CAT.SizePrefixedCATBufferHasIdentifier(data):
		return CAT.GetSizePrefixedRootAsCAT(data, 0), nil
	case CAT.CATBufferHasIdentifier(data):
		return CAT.GetRootAsCAT(data, 0), nil
	default:
		return nil, fmt.Errorf("invalid CAT buffer")
	}
}
