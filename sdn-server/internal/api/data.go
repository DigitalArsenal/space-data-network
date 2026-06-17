// Package api provides HTTP API endpoints for the SDN server.
package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/SPW"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// DataQueryHandler serves read-only, cache-friendly schema query APIs.
type DataQueryHandler struct {
	store    *storage.FlatSQLStore
	verifier *license.TokenVerifier
}

const (
	rawFlatBufferStreamContentType = datasync.RawFlatBufferStreamType
	rawDataDefaultLimit            = datasync.DefaultLimit
	rawDataMaxQueryLimit           = datasync.MaxQueryLimit
	rawDataMaxSyncChunkLimit       = datasync.MaxSyncChunkLimit
	rawDataStreamRequestMaxBytes   = datasync.StreamRequestMaxBytes
)

// NewDataQueryHandler creates a new data query handler.
func NewDataQueryHandler(store *storage.FlatSQLStore, verifier *license.TokenVerifier) *DataQueryHandler {
	return &DataQueryHandler{
		store:    store,
		verifier: verifier,
	}
}

// RegisterRoutes registers public data API routes.
func (h *DataQueryHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/data/health", h.handleHealth)
	mux.HandleFunc("/api/v1/data/summary", h.handleSummary)
	mux.HandleFunc("/api/v1/data/datastores", h.handleDatastores)
	mux.HandleFunc("/api/v1/data/scan", h.handleScan)
	mux.HandleFunc("/api/v1/data/stream", h.handleRawStream)
	mux.HandleFunc("/api/v1/data/query", h.handleRawQuery)
	mux.HandleFunc("/api/v1/data/epoch", h.handleEpochQuery)
	mux.HandleFunc("/api/v1/data/local-replica-stats", h.handleLocalReplicaStats)
	mux.HandleFunc("/api/v1/data/records/", h.handleRawRecord)
	mux.HandleFunc("/api/v1/data/omm", h.handleOMM)
	mux.HandleFunc("/api/v1/data/omm/bulk", h.handleOMMBulk)
	mux.HandleFunc("/api/v1/data/mpe", h.handleMPE)
	mux.HandleFunc("/api/v1/data/mpe/bulk", h.handleMPEBulk)
	mux.HandleFunc("/api/v1/data/cat", h.handleCAT)
	mux.HandleFunc("/api/v1/data/cat/bulk", h.handleCATBulk)
	mux.HandleFunc("/api/v1/data/spw/bulk", h.handleSPWBulk)
	mux.HandleFunc("/api/v1/data/secure/omm", h.handleSecureOMM)
}

func (h *DataQueryHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	payload := map[string]interface{}{
		"status":    "ok",
		"component": "spaceaware-data-api",
		"time":      time.Now().UTC().Format(time.RFC3339),
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *DataQueryHandler) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}
	summary, err := h.store.DataSummary()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_records": summary.TotalRecords,
		"total_bytes":   summary.TotalBytes,
		"schemas":       schemaSummaryRows(summary.Schemas),
		"sources":       sourceSummaryRows(summary.Sources),
	})
}

func (h *DataQueryHandler) handleDatastores(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}
	entries, err := h.store.ListDatastoreIdentities()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	results := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		results = append(results, map[string]interface{}{
			"key":        entry.Key,
			"identity":   entry.Identity,
			"updated_at": entry.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":   len(results),
		"results": results,
	})
}

func (h *DataQueryHandler) handleLocalReplicaStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":   len(stats),
		"results": localReplicaStatsRows(stats),
	})
}

func (h *DataQueryHandler) handleRawQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	var req rawDataQueryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	schemaName := strings.TrimSpace(firstNonEmptyDataString(req.Schema, req.SchemaName))
	if schemaName == "" {
		writeError(w, http.StatusBadRequest, "schema is required")
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = rawDataDefaultLimit
	}
	if limit > rawDataMaxQueryLimit {
		limit = rawDataMaxQueryLimit
	}

	queryStore, closeQueryStore, err := h.storeForDatastoreKey(req.DatastoreKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer closeQueryStore()

	records, err := queryStore.QueryRawRecords(storage.RawRecordQuery{
		SchemaName:        schemaName,
		ProviderID:        firstNonEmptyDataString(req.ProviderID, req.ProviderId),
		SourceName:        req.SourceName,
		BatchID:           firstNonEmptyDataString(req.BatchID, req.BatchId),
		ProducerPeerID:    firstNonEmptyDataString(req.ProducerPeerID, req.ProducerPeerId),
		ProducerPublicKey: firstNonEmptyDataString(req.ProducerPublicKey, req.ProducerPublicKeyCamel),
		PeerID:            firstNonEmptyDataString(req.PeerID, req.PeerId),
		SyncFilter:        req.SyncFilter,
		Limit:             limit,
		Offset:            req.Offset,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if requestedRawFlatBufferStream(r) {
		writeFlatBufferPayloadStreamWithContentType(w, schemaName, recordsToPayloads(records), rawFlatBufferStreamContentType)
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		results = append(results, rawRecordRow(schemaName, rec, req.IncludeData))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema":  schemaName,
		"count":   len(results),
		"results": results,
	})
}

func (h *DataQueryHandler) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	var req rawDataQueryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	queryStore, closeQueryStore, err := h.storeForDatastoreKey(req.DatastoreKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer closeQueryStore()

	response, _, err := datasync.Scan(queryStore, rawQueryToSyncRequest(req), rawDataMaxSyncChunkLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *DataQueryHandler) handleRawStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	var req rawDataStreamRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, rawDataStreamRequestMaxBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	schemaName := strings.TrimSpace(firstNonEmptyDataString(req.Schema, req.SchemaName))
	if schemaName == "" {
		writeError(w, http.StatusBadRequest, "schema is required")
		return
	}
	queryStore, closeQueryStore, err := h.storeForDatastoreKey(req.DatastoreKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer closeQueryStore()

	computedHash, records, err := datasync.ResolveStreamRecords(queryStore, rawStreamToSyncRequest(req))
	if err != nil {
		status := http.StatusNotFound
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "limit") || strings.Contains(err.Error(), "schema") {
			status = http.StatusBadRequest
		}
		if strings.Contains(err.Error(), "scan hash") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}

	w.Header().Set("X-SDN-Scan-Hash", computedHash)
	w.Header().Set("X-SDN-Chunk-Hash", firstNonEmptyDataString(req.ChunkHash, computedHash))
	w.Header().Set("X-SDN-Sync-Protocol", datasync.ProtocolID)
	w.Header().Set("X-SDN-Snapshot-ID", req.SnapshotID)
	w.Header().Set("X-SDN-Head", req.Head)
	w.Header().Set("X-SDN-Cursor", req.Cursor)
	w.Header().Set("X-SDN-Next-Cursor", req.NextCursor)
	if req.TotalCount > 0 {
		w.Header().Set("X-SDN-Total-Count", strconv.FormatInt(req.TotalCount, 10))
	}
	w.Header().Set("X-SDN-High-Water-Mark", req.HighWaterMark)
	writeFlatBufferRecordStreamWithContentType(w, schemaName, records, rawFlatBufferStreamContentType, queryStore)
}

func (h *DataQueryHandler) handleRawRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/data/records/")
	parts := strings.SplitN(suffix, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		writeError(w, http.StatusBadRequest, "expected /api/v1/data/records/{schema}/{cid}")
		return
	}
	schemaName, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schema")
		return
	}
	recordID, err := url.PathUnescape(parts[1])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid record id")
		return
	}

	record, err := h.store.GetRawRecord(schemaName, recordID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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

func (h *DataQueryHandler) handleEpochQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	query, err := epochRecordQueryFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if query.Profile == storage.EpochProfileCoverage {
		coverage, err := h.store.QueryEpochCoverage(query)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"schema":      query.SchemaName,
			"profile":     query.Profile,
			"query":       epochQueryResponse(query),
			"count":       len(coverage),
			"total_count": len(coverage),
			"results":     epochCoverageRows(coverage),
		})
		return
	}

	totalCount, err := h.store.CountEpochRecords(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	matches, err := h.store.QueryEpochRecords(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows := make([]map[string]interface{}, 0, len(matches))
	includeData := parseBool(r, "include_data")
	for _, match := range matches {
		rows = append(rows, epochMatchRow(query.SchemaName, match, includeData))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema":      query.SchemaName,
		"profile":     query.Profile,
		"query":       epochQueryResponse(query),
		"count":       len(rows),
		"total_count": totalCount,
		"results":     rows,
	})
}

func (h *DataQueryHandler) handleOMM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.writeOMMResponse(w, r, true)
}

func (h *DataQueryHandler) handleSecureOMM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireScope(w, r, "api:data:read:premium") {
		return
	}
	h.writeOMMResponse(w, r, false)
}

func (h *DataQueryHandler) handleOMMBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	day, err := optionalDay(r, "day")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	limit := parseLimit(r, 50000, 250000)
	includeData := parseBool(r, "include_data")
	format := requestedDataFormat(r)

	records, err := h.bulkRecords("OMM.fbs", day, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, day)
	if handleConditionalCache(w, r, "OMM.fbs", day, "bulk", records) {
		return
	}

	if format == dataFormatFlatBuffers {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", ommBulkFilename(day)))
		writeFlatBufferStream(w, "OMM.fbs", records)
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}
		addRecordFreshness(row, rec)
		if omm, err := decodeOMM(rec.Data); err == nil {
			row["norad_cat_id"] = omm.NORAD_CAT_ID()
			row["object_name"] = string(omm.OBJECT_NAME())
			row["object_id"] = string(omm.OBJECT_ID())
			row["epoch"] = string(omm.EPOCH())
			row["mean_motion"] = omm.MEAN_MOTION()
			row["eccentricity"] = omm.ECCENTRICITY()
			row["inclination"] = omm.INCLINATION()
		}
		if includeData {
			row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
		}
		results = append(results, row)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema": "OMM.fbs",
		"query": map[string]interface{}{
			"day":   day,
			"limit": limit,
		},
		"count":   len(results),
		"results": results,
	})
}

func (h *DataQueryHandler) handleMPE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	day, err := requiredDay(r, "day")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	entityID := strings.TrimSpace(r.URL.Query().Get("entity_id"))
	if entityID == "" {
		writeError(w, http.StatusBadRequest, "missing required query parameter: entity_id")
		return
	}

	limit := parseLimit(r, 100, 1000)
	includeData := parseBool(r, "include_data")
	format := requestedDataFormat(r)

	records, err := h.store.QueryByIndexedFields("MPE.fbs", day, nil, entityID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, day)
	if handleConditionalCache(w, r, "MPE.fbs", day, entityID, records) {
		return
	}

	if format == dataFormatFlatBuffers {
		writeFlatBufferStream(w, "MPE.fbs", records)
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}
		addRecordFreshness(row, rec)

		if mpe, err := decodeMPE(rec.Data); err == nil {
			row["entity_id"] = strings.TrimSpace(string(mpe.ENTITY_ID()))
			row["epoch_unix"] = int64(mpe.EPOCH())
			row["mean_motion"] = mpe.MEAN_MOTION()
			row["eccentricity"] = mpe.ECCENTRICITY()
			row["inclination"] = mpe.INCLINATION()
		}

		if includeData {
			row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
		}

		results = append(results, row)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema": "MPE.fbs",
		"query": map[string]interface{}{
			"day":       day,
			"entity_id": entityID,
			"limit":     limit,
		},
		"count":   len(results),
		"results": results,
	})
}

func (h *DataQueryHandler) handleMPEBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	day, err := optionalDay(r, "day")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	limit := parseLimit(r, 50000, 250000)
	includeData := parseBool(r, "include_data")
	format := requestedDataFormat(r)

	records, err := h.bulkRecords("MPE.fbs", day, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, day)
	if handleConditionalCache(w, r, "MPE.fbs", day, "bulk", records) {
		return
	}

	if format == dataFormatFlatBuffers {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", mpeBulkFilename(day)))
		writeFlatBufferStream(w, "MPE.fbs", records)
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}
		addRecordFreshness(row, rec)
		if mpe, err := decodeMPE(rec.Data); err == nil {
			row["entity_id"] = strings.TrimSpace(string(mpe.ENTITY_ID()))
			row["epoch_unix"] = int64(mpe.EPOCH())
			row["mean_motion"] = mpe.MEAN_MOTION()
			row["eccentricity"] = mpe.ECCENTRICITY()
			row["inclination"] = mpe.INCLINATION()
		}
		if includeData {
			row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
		}
		results = append(results, row)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema": "MPE.fbs",
		"query": map[string]interface{}{
			"day":   day,
			"limit": limit,
		},
		"count":   len(results),
		"results": results,
	})
}

func (h *DataQueryHandler) handleCAT(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	noradID, err := requiredUint32(r, "norad_cat_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	limit := parseLimit(r, 5, 100)
	includeData := parseBool(r, "include_data")
	format := requestedDataFormat(r)

	records, err := h.store.QueryByIndexedFields("CAT.fbs", "", &noradID, "", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, "")
	if handleConditionalCache(w, r, "CAT.fbs", "", fmt.Sprintf("%d", noradID), records) {
		return
	}
	if format == dataFormatFlatBuffers {
		writeFlatBufferStream(w, "CAT.fbs", records)
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}
		addRecordFreshness(row, rec)

		if cat, err := decodeCAT(rec.Data); err == nil {
			row["norad_cat_id"] = cat.NORAD_CAT_ID()
			row["object_name"] = string(cat.OBJECT_NAME())
			row["object_id"] = string(cat.OBJECT_ID())
			row["launch_date"] = string(cat.LAUNCH_DATE())
			row["apogee_km"] = cat.APOGEE()
			row["perigee_km"] = cat.PERIGEE()
		}

		if includeData {
			row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
		}

		results = append(results, row)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema": "CAT.fbs",
		"query": map[string]interface{}{
			"norad_cat_id": noradID,
			"limit":        limit,
		},
		"count":   len(results),
		"results": results,
	})
}

func (h *DataQueryHandler) handleCATBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	limit := parseLimit(r, 50000, 250000)
	includeData := parseBool(r, "include_data")
	format := requestedDataFormat(r)

	records, err := h.bulkRecords("CAT.fbs", "", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, "")
	if handleConditionalCache(w, r, "CAT.fbs", "", "bulk", records) {
		return
	}

	if format == dataFormatFlatBuffers {
		w.Header().Set("Content-Disposition", `attachment; filename="cat-bulk.fbsstream"`)
		writeFlatBufferStream(w, "CAT.fbs", records)
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}
		addRecordFreshness(row, rec)

		if cat, err := decodeCAT(rec.Data); err == nil {
			row["norad_cat_id"] = cat.NORAD_CAT_ID()
			row["object_name"] = string(cat.OBJECT_NAME())
			row["object_id"] = string(cat.OBJECT_ID())
			row["launch_date"] = string(cat.LAUNCH_DATE())
			row["apogee_km"] = cat.APOGEE()
			row["perigee_km"] = cat.PERIGEE()
		}

		if includeData {
			row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
		}

		results = append(results, row)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema": "CAT.fbs",
		"query": map[string]interface{}{
			"limit": limit,
		},
		"count":   len(results),
		"results": results,
	})
}

func (h *DataQueryHandler) handleSPWBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	limit := parseLimit(r, 50000, 250000)
	includeData := parseBool(r, "include_data")
	format := requestedDataFormat(r)

	records, err := h.bulkRecords("SPW.fbs", "", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, "")
	if handleConditionalCache(w, r, "SPW.fbs", "", "bulk", records) {
		return
	}

	if format == dataFormatFlatBuffers {
		w.Header().Set("Content-Disposition", `attachment; filename="spw-bulk.fbsstream"`)
		writeFlatBufferStream(w, "SPW.fbs", records)
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}
		addRecordFreshness(row, rec)

		if spw, err := decodeSPW(rec.Data); err == nil {
			row["date"] = string(spw.DATE())
			row["bsrn"] = spw.BSRN()
			row["nd"] = spw.ND()
			row["kp1"] = spw.KP1()
			row["ap1"] = spw.AP1()
			row["f107_obs"] = spw.F107Obs()
			row["f107_adj"] = spw.F107Adj()
			row["f107_data_type"] = spw.F107DataType().String()
		}

		if includeData {
			row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
		}

		results = append(results, row)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema": "SPW.fbs",
		"query": map[string]interface{}{
			"limit": limit,
		},
		"count":   len(results),
		"results": results,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
		},
	})
}

type rawDataQueryRequest struct {
	DatastoreKey           string `json:"datastore_key"`
	Schema                 string `json:"schema"`
	SchemaName             string `json:"schema_name"`
	ProviderID             string `json:"provider_id"`
	ProviderId             string `json:"providerId"`
	SourceName             string `json:"source_name"`
	BatchID                string `json:"batch_id"`
	BatchId                string `json:"batchId"`
	ProducerPeerID         string `json:"producer_peer_id"`
	ProducerPeerId         string `json:"producerPeerId"`
	ProducerPublicKey      string `json:"producer_public_key"`
	ProducerPublicKeyCamel string `json:"producerPublicKey"`
	PeerID                 string `json:"peer_id"`
	PeerId                 string `json:"peerId"`
	Cursor                 string `json:"cursor"`
	SnapshotID             string `json:"snapshot_id"`
	Head                   string `json:"head"`
	QueryProfile           string `json:"query_profile"`
	SyncFilter             string `json:"sync_filter"`
	Limit                  int    `json:"limit"`
	Offset                 int    `json:"offset"`
	IncludeData            bool   `json:"include_data"`
}

type rawDataStreamRequest struct {
	DatastoreKey  string                `json:"datastore_key"`
	Schema        string                `json:"schema"`
	SchemaName    string                `json:"schema_name"`
	ScanHash      string                `json:"scan_hash"`
	ChunkHash     string                `json:"chunk_hash"`
	SnapshotID    string                `json:"snapshot_id"`
	Head          string                `json:"head"`
	Cursor        string                `json:"cursor"`
	NextCursor    string                `json:"next_cursor"`
	TotalCount    int64                 `json:"total_count"`
	HighWaterMark string                `json:"high_water_mark"`
	QueryProfile  string                `json:"query_profile"`
	SyncFilter    string                `json:"sync_filter"`
	Records       []rawDataStreamRecord `json:"records"`
}

type rawDataStreamRecord struct {
	Schema                 string `json:"schema"`
	SchemaName             string `json:"schema_name"`
	CID                    string `json:"cid"`
	ID                     string `json:"id"`
	ProviderID             string `json:"provider_id"`
	ProviderId             string `json:"providerId"`
	SourceName             string `json:"source_name"`
	BatchID                string `json:"batch_id"`
	BatchId                string `json:"batchId"`
	ProducerPeerID         string `json:"producer_peer_id"`
	ProducerPeerId         string `json:"producerPeerId"`
	ProducerPublicKey      string `json:"producer_public_key"`
	ProducerPublicKeyCamel string `json:"producerPublicKey"`
	PeerID                 string `json:"peer_id"`
	PeerId                 string `json:"peerId"`
}

func rawQueryToSyncRequest(req rawDataQueryRequest) datasync.QueryRequest {
	return datasync.QueryRequest{
		Schema:                 req.Schema,
		SchemaName:             req.SchemaName,
		ProviderID:             req.ProviderID,
		ProviderId:             req.ProviderId,
		SourceName:             req.SourceName,
		BatchID:                req.BatchID,
		BatchId:                req.BatchId,
		ProducerPeerID:         req.ProducerPeerID,
		ProducerPeerId:         req.ProducerPeerId,
		ProducerPublicKey:      req.ProducerPublicKey,
		ProducerPublicKeyCamel: req.ProducerPublicKeyCamel,
		PeerID:                 req.PeerID,
		PeerId:                 req.PeerId,
		Cursor:                 req.Cursor,
		SnapshotID:             req.SnapshotID,
		Head:                   req.Head,
		QueryProfile:           req.QueryProfile,
		SyncFilter:             req.SyncFilter,
		Limit:                  req.Limit,
		Offset:                 req.Offset,
		IncludeData:            req.IncludeData,
	}
}

func rawStreamToSyncRequest(req rawDataStreamRequest) datasync.StreamRequest {
	records := make([]datasync.RecordRef, 0, len(req.Records))
	for _, ref := range req.Records {
		records = append(records, datasync.RecordRef{
			Schema:                 ref.Schema,
			SchemaName:             ref.SchemaName,
			CID:                    ref.CID,
			ID:                     ref.ID,
			ProviderID:             ref.ProviderID,
			ProviderId:             ref.ProviderId,
			SourceName:             ref.SourceName,
			BatchID:                ref.BatchID,
			BatchId:                ref.BatchId,
			ProducerPeerID:         ref.ProducerPeerID,
			ProducerPeerId:         ref.ProducerPeerId,
			ProducerPublicKey:      ref.ProducerPublicKey,
			ProducerPublicKeyCamel: ref.ProducerPublicKeyCamel,
			PeerID:                 ref.PeerID,
			PeerId:                 ref.PeerId,
		})
	}
	return datasync.StreamRequest{
		QueryRequest: datasync.QueryRequest{
			Schema:       req.Schema,
			SchemaName:   req.SchemaName,
			Cursor:       req.Cursor,
			SnapshotID:   req.SnapshotID,
			Head:         req.Head,
			QueryProfile: req.QueryProfile,
			SyncFilter:   req.SyncFilter,
		},
		ScanHash:      req.ScanHash,
		ChunkHash:     req.ChunkHash,
		NextCursor:    req.NextCursor,
		TotalCount:    req.TotalCount,
		HighWaterMark: req.HighWaterMark,
		Records:       records,
	}
}

func schemaSummaryRows(schemas []storage.DataSchemaSummary) []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(schemas))
	for _, schema := range schemas {
		rows = append(rows, map[string]interface{}{
			"schema_name": schema.SchemaName,
			"count":       schema.Count,
			"total_bytes": schema.TotalBytes,
		})
	}
	return rows
}

func sourceSummaryRows(sources []storage.DataSourceSummary) []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(sources))
	for _, source := range sources {
		rows = append(rows, map[string]interface{}{
			"schema_name":         source.SchemaName,
			"provider_id":         source.ProviderID,
			"source_name":         source.SourceName,
			"batch_id":            source.BatchID,
			"producer_peer_id":    source.ProducerPeerID,
			"producer_public_key": source.ProducerPublicKey,
			"count":               source.Count,
			"total_bytes":         source.TotalBytes,
		})
	}
	return rows
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

func epochRecordQueryFromRequest(r *http.Request) (storage.EpochRecordQuery, error) {
	values := r.URL.Query()
	schemaName := strings.TrimSpace(values.Get("schema"))
	if schemaName == "" {
		schemaName = strings.TrimSpace(values.Get("schema_name"))
	}
	if schemaName == "" {
		return storage.EpochRecordQuery{}, fmt.Errorf("missing required query parameter: schema")
	}
	profile := strings.TrimSpace(values.Get("profile"))
	if profile == "" {
		profile = storage.EpochProfileDay
	}
	query := storage.EpochRecordQuery{
		SchemaName: schemaName,
		Profile:    profile,
		Day:        strings.TrimSpace(values.Get("day")),
		ProviderID: strings.TrimSpace(values.Get("provider_id")),
		SourceName: strings.TrimSpace(values.Get("source_name")),
		BatchID:    strings.TrimSpace(values.Get("batch_id")),
		EntityID:   strings.TrimSpace(values.Get("entity_id")),
		Limit:      parseLimit(r, 1000, 250000),
	}
	if raw := strings.TrimSpace(values.Get("at")); raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return storage.EpochRecordQuery{}, fmt.Errorf("invalid at %q (expected RFC3339)", raw)
		}
		query.At = at
	}
	if raw := strings.TrimSpace(values.Get("from")); raw != "" {
		from, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return storage.EpochRecordQuery{}, fmt.Errorf("invalid from %q (expected RFC3339)", raw)
		}
		query.From = &from
	}
	if raw := strings.TrimSpace(values.Get("to")); raw != "" {
		to, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return storage.EpochRecordQuery{}, fmt.Errorf("invalid to %q (expected RFC3339)", raw)
		}
		query.To = &to
	}
	if raw := strings.TrimSpace(values.Get("norad_cat_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return storage.EpochRecordQuery{}, fmt.Errorf("invalid norad_cat_id")
		}
		value := uint32(parsed)
		query.NoradCatID = &value
	}
	if raw := strings.TrimSpace(values.Get("max_delta_seconds")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return storage.EpochRecordQuery{}, fmt.Errorf("invalid max_delta_seconds")
		}
		query.MaxDeltaSeconds = parsed
	}
	return query, nil
}

func epochQueryResponse(query storage.EpochRecordQuery) map[string]interface{} {
	response := map[string]interface{}{
		"schema":      query.SchemaName,
		"profile":     query.Profile,
		"provider_id": query.ProviderID,
		"source_name": query.SourceName,
		"batch_id":    query.BatchID,
		"limit":       query.Limit,
	}
	if query.Day != "" {
		response["day"] = query.Day
	}
	if !query.At.IsZero() {
		response["at"] = query.At.UTC().Format(time.RFC3339)
	}
	if query.From != nil {
		response["from"] = query.From.UTC().Format(time.RFC3339)
	}
	if query.To != nil {
		response["to"] = query.To.UTC().Format(time.RFC3339)
	}
	if query.NoradCatID != nil {
		response["norad_cat_id"] = *query.NoradCatID
	}
	if query.EntityID != "" {
		response["entity_id"] = query.EntityID
	}
	if query.MaxDeltaSeconds > 0 {
		response["max_delta_seconds"] = query.MaxDeltaSeconds
	}
	return response
}

func epochMatchRow(schemaName string, match storage.EpochRecordMatch, includeData bool) map[string]interface{} {
	row := rawRecordRow(schemaName, match.Record, includeData)
	row["entity_key"] = match.EntityKey
	row["match_quality"] = map[string]interface{}{
		"requested_epoch": match.RequestedEpoch.UTC().Format(time.RFC3339),
		"matched_epoch":   match.MatchedEpoch.UTC().Format(time.RFC3339),
		"delta_seconds":   match.DeltaSeconds,
		"match_type":      match.MatchType,
	}
	switch schemaName {
	case "OMM.fbs":
		if omm, err := decodeOMM(match.Record.Data); err == nil {
			row["norad_cat_id"] = omm.NORAD_CAT_ID()
			row["object_name"] = string(omm.OBJECT_NAME())
			row["object_id"] = string(omm.OBJECT_ID())
			row["epoch"] = string(omm.EPOCH())
			row["mean_motion"] = omm.MEAN_MOTION()
		}
	case "MPE.fbs":
		if mpe, err := decodeMPE(match.Record.Data); err == nil {
			row["entity_id"] = strings.TrimSpace(string(mpe.ENTITY_ID()))
			row["epoch_unix"] = int64(mpe.EPOCH())
			row["mean_motion"] = mpe.MEAN_MOTION()
		}
	}
	return row
}

func epochCoverageRows(coverage []storage.EpochCoverageBucket) []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(coverage))
	for _, bucket := range coverage {
		rows = append(rows, map[string]interface{}{
			"day":          bucket.Day,
			"count":        bucket.Count,
			"oldest_epoch": bucket.OldestEpoch.UTC().Format(time.RFC3339),
			"newest_epoch": bucket.NewestEpoch.UTC().Format(time.RFC3339),
		})
	}
	return rows
}

func rawRecordRow(schemaName string, rec *storage.Record, includeData bool) map[string]interface{} {
	row := map[string]interface{}{
		"schema_name":    schemaName,
		"cid":            rec.CID,
		"peer_id":        rec.PeerID,
		"timestamp":      rec.Timestamp.UTC().Format(time.RFC3339),
		"size_bytes":     recordSizeBytes(rec),
		"flatbuffer_uri": fmt.Sprintf("/api/v1/data/records/%s/%s", url.PathEscape(schemaName), url.PathEscape(rec.CID)),
	}
	if includeData {
		row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
	}
	if len(rec.Signature) > 0 {
		row["signature_base64"] = base64.StdEncoding.EncodeToString(rec.Signature)
	}
	if !rec.MaterializedAt.IsZero() {
		row["materialized_at"] = rec.MaterializedAt.UTC().Format(time.RFC3339)
	}
	if rec.SourceTags.ProviderID != "" {
		row["provider_id"] = rec.SourceTags.ProviderID
	}
	if rec.SourceTags.SourceName != "" {
		row["source_name"] = rec.SourceTags.SourceName
	}
	if rec.SourceTags.SourceURL != "" {
		row["source_url"] = rec.SourceTags.SourceURL
	}
	if rec.SourceTags.BatchID != "" {
		row["batch_id"] = rec.SourceTags.BatchID
	}
	if rec.SourceTags.ContentKeyID != "" {
		row["content_key_id"] = rec.SourceTags.ContentKeyID
	}
	if rec.SourceTags.ProducerPeerID != "" {
		row["producer_peer_id"] = rec.SourceTags.ProducerPeerID
	}
	if rec.SourceTags.ProducerPublicKey != "" {
		row["producer_public_key"] = rec.SourceTags.ProducerPublicKey
	}
	return row
}

func scanHash(schemaName string, records []*storage.Record) string {
	hash := sha256.New()
	for _, rec := range records {
		_, _ = fmt.Fprintf(
			hash,
			"%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\n",
			schemaName,
			rec.CID,
			rec.PeerID,
			recordSizeBytes(rec),
			rec.SourceTags.ProviderID,
			rec.SourceTags.SourceName,
			rec.SourceTags.BatchID,
			rec.SourceTags.ProducerPeerID,
			rec.SourceTags.ProducerPublicKey,
		)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func recordSizeBytes(rec *storage.Record) int64 {
	if len(rec.Data) > 0 {
		return int64(len(rec.Data))
	}
	if rec.RecordLength > 0 {
		return rec.RecordLength
	}
	return 0
}

func encodeScanCursor(offset int) string {
	if offset < 0 {
		offset = 0
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func parseScanCursor(cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return 0, fmt.Errorf("invalid cursor")
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return offset, nil
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
		writeError(w, http.StatusServiceUnavailable, "local storage unavailable in edge mode")
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

func (h *DataQueryHandler) bulkRecords(schemaName, day string, limit int) ([]*storage.Record, error) {
	if strings.TrimSpace(day) != "" {
		return h.store.QueryByIndexedFields(schemaName, day, nil, "", limit)
	}
	if schemaName == "OMM.fbs" {
		records, ok, err := h.store.QueryLatestSourceBatchRecords(schemaName, "celestrak-gp", limit)
		if err != nil {
			return nil, err
		}
		if ok {
			return records, nil
		}
	}
	return h.store.QueryRecentRecords(schemaName, limit)
}

func addRecordFreshness(row map[string]interface{}, rec *storage.Record) {
	if rec == nil {
		return
	}
	if !rec.MaterializedAt.IsZero() {
		row["materialized_at"] = rec.MaterializedAt.UTC().Format(time.RFC3339)
	}
	if rec.SourceTags.ProviderID != "" {
		row["source_provider_id"] = rec.SourceTags.ProviderID
	}
	if rec.SourceTags.SourceName != "" {
		row["source_name"] = rec.SourceTags.SourceName
	}
	if rec.SourceTags.BatchID != "" {
		row["source_batch_id"] = rec.SourceTags.BatchID
	}
	if rec.SourceTags.ContentKeyID != "" {
		row["source_content_key_id"] = rec.SourceTags.ContentKeyID
	}
	if rec.SourceTags.ProducerPeerID != "" {
		row["source_producer_peer_id"] = rec.SourceTags.ProducerPeerID
	}
	if rec.SourceTags.ProducerPublicKey != "" {
		row["source_producer_public_key"] = rec.SourceTags.ProducerPublicKey
	}
}

func requiredDay(r *http.Request, key string) (string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return "", fmt.Errorf("missing required query parameter: %s", key)
	}
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		return "", fmt.Errorf("invalid %s (expected YYYY-MM-DD)", key)
	}
	return raw, nil
}

func optionalDay(r *http.Request, key string) (string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		return "", fmt.Errorf("invalid %s (expected YYYY-MM-DD)", key)
	}
	return raw, nil
}

func requiredUint32(r *http.Request, key string) (uint32, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, fmt.Errorf("missing required query parameter: %s", key)
	}
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return uint32(v), nil
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

func parseBool(r *http.Request, key string) bool {
	raw := strings.TrimSpace(strings.ToLower(r.URL.Query().Get(key)))
	return raw == "1" || raw == "true" || raw == "yes"
}

func (h *DataQueryHandler) requireScope(w http.ResponseWriter, r *http.Request, scope string) bool {
	if h.verifier == nil {
		writeError(w, http.StatusServiceUnavailable, "license verifier unavailable")
		return false
	}
	expectedPeerID := strings.TrimSpace(r.Header.Get("X-SDN-Peer-ID"))
	claims, err := h.verifier.VerifyAuthorizationHeader(r.Header.Get("Authorization"), expectedPeerID, []string{scope})
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return false
	}
	w.Header().Set("X-SDN-Token-Subject", claims.Sub)
	w.Header().Set("X-SDN-Token-Plan", claims.Plan)
	return true
}

func (h *DataQueryHandler) writeOMMResponse(w http.ResponseWriter, r *http.Request, cacheable bool) {
	if !h.ensureStore(w) {
		return
	}

	day, err := requiredDay(r, "day")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	noradID, err := requiredUint32(r, "norad_cat_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	limit := parseLimit(r, 100, 1000)
	includeData := parseBool(r, "include_data")
	format := requestedDataFormat(r)

	records, err := h.store.QueryByIndexedFields("OMM.fbs", day, &noradID, "", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if cacheable {
		setCachePolicy(w, day)
		if handleConditionalCache(w, r, "OMM.fbs", day, fmt.Sprintf("%d", noradID), records) {
			return
		}
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	if format == dataFormatFlatBuffers {
		writeFlatBufferStream(w, "OMM.fbs", records)
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}

		if omm, err := decodeOMM(rec.Data); err == nil {
			row["norad_cat_id"] = omm.NORAD_CAT_ID()
			row["object_name"] = string(omm.OBJECT_NAME())
			row["object_id"] = string(omm.OBJECT_ID())
			row["epoch"] = string(omm.EPOCH())
			row["mean_motion"] = omm.MEAN_MOTION()
			row["eccentricity"] = omm.ECCENTRICITY()
			row["inclination"] = omm.INCLINATION()
		}

		if includeData {
			row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
		}

		results = append(results, row)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema": "OMM.fbs",
		"query": map[string]interface{}{
			"day":          day,
			"norad_cat_id": noradID,
			"limit":        limit,
		},
		"count":   len(results),
		"results": results,
	})
}

func setCachePolicy(w http.ResponseWriter, day string) {
	cacheControl := "public, max-age=30, s-maxage=120, stale-while-revalidate=300"
	if day != "" {
		queryDay, err := time.Parse("2006-01-02", day)
		if err == nil && queryDay.Before(time.Now().UTC().AddDate(0, 0, -1)) {
			cacheControl = "public, max-age=300, s-maxage=86400, stale-while-revalidate=86400"
		}
	}
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Vary", "Accept, Accept-Encoding")
}

func handleConditionalCache(w http.ResponseWriter, r *http.Request, schema, day, objectKey string, records []*storage.Record) bool {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(schema))
	_, _ = hasher.Write([]byte("|"))
	_, _ = hasher.Write([]byte(day))
	_, _ = hasher.Write([]byte("|"))
	_, _ = hasher.Write([]byte(objectKey))
	for _, rec := range records {
		_, _ = hasher.Write([]byte(rec.CID))
		_, _ = hasher.Write([]byte(rec.Timestamp.UTC().Format(time.RFC3339Nano)))
	}

	tag := `"` + hex.EncodeToString(hasher.Sum(nil)) + `"`
	w.Header().Set("ETag", tag)

	if inm := strings.TrimSpace(r.Header.Get("If-None-Match")); inm != "" && inm == tag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}

	if len(records) > 0 {
		latest := records[0].Timestamp.UTC()
		for _, rec := range records[1:] {
			if rec.Timestamp.After(latest) {
				latest = rec.Timestamp.UTC()
			}
		}
		w.Header().Set("Last-Modified", latest.Format(http.TimeFormat))
	}

	return false
}

type dataFormat int

const (
	dataFormatFlatBuffers dataFormat = iota
	dataFormatJSON
)

func requestedDataFormat(r *http.Request) dataFormat {
	queryValue := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch queryValue {
	case "json", "application/json":
		return dataFormatJSON
	case "flatbuffers", "flatbuffer", "binary", "fbs", "fb":
		return dataFormatFlatBuffers
	}

	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	if strings.Contains(accept, "application/json") {
		return dataFormatJSON
	}

	// FlatBuffers-first API contract.
	return dataFormatFlatBuffers
}

func requestedRawFlatBufferStream(r *http.Request) bool {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "raw-flatbuffer-stream" || format == "flatbuffer-stream" || format == "flatbuffers-stream" {
		return true
	}
	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	return strings.Contains(accept, rawFlatBufferStreamContentType) ||
		strings.Contains(accept, "application/vnd.sdn.raw-flatbuffer-stream")
}

func writeFlatBufferStream(w http.ResponseWriter, schema string, records []*storage.Record) {
	writeFlatBufferPayloadStream(w, schema, recordsToPayloads(records))
}

func recordsToPayloads(records []*storage.Record) [][]byte {
	payloads := make([][]byte, 0, len(records))
	for _, rec := range records {
		payloads = append(payloads, rec.Data)
	}
	return payloads
}

func writeFlatBufferPayloadStream(w http.ResponseWriter, schema string, payloads [][]byte) {
	writeFlatBufferPayloadStreamWithContentType(w, schema, payloads, "application/x-flatbuffers")
}

func writeFlatBufferPayloadStreamWithContentType(w http.ResponseWriter, schema string, payloads [][]byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-SDN-Schema", schema)
	w.Header().Set("X-SDN-Record-Count", strconv.Itoa(len(payloads)))
	w.Header().Set("X-SDN-Stream-Format", "flatsql-size-prefixed-le-u32")
	w.WriteHeader(http.StatusOK)

	var lenBuf [4]byte
	for _, payload := range payloads {
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(payload)))
		if _, err := w.Write(lenBuf[:]); err != nil {
			return
		}
		if _, err := w.Write(payload); err != nil {
			return
		}
	}
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

func mpeBulkFilename(day string) string {
	if strings.TrimSpace(day) == "" {
		return "mpe-bulk.fbsstream"
	}
	return fmt.Sprintf("mpe-%s.fbsstream", day)
}

func ommBulkFilename(day string) string {
	if strings.TrimSpace(day) == "" {
		return "omm-bulk.fbsstream"
	}
	return fmt.Sprintf("omm-%s.fbsstream", day)
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

func decodeMPE(data []byte) (*MPE.MPE, error) {
	switch {
	case MPE.SizePrefixedMPEBufferHasIdentifier(data):
		return MPE.GetSizePrefixedRootAsMPE(data, 0), nil
	case MPE.MPEBufferHasIdentifier(data):
		return MPE.GetRootAsMPE(data, 0), nil
	default:
		return nil, fmt.Errorf("invalid MPE buffer")
	}
}

func decodeSPW(data []byte) (*SPW.SPW, error) {
	switch {
	case SPW.SizePrefixedSPWBufferHasIdentifier(data):
		return SPW.GetSizePrefixedRootAsSPW(data, 0), nil
	case SPW.SPWBufferHasIdentifier(data):
		return SPW.GetRootAsSPW(data, 0), nil
	default:
		return nil, fmt.Errorf("invalid SPW buffer")
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
