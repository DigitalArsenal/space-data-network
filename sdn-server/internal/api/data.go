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

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// DataQueryHandler serves read-only, cache-friendly schema query APIs.
type DataQueryHandler struct {
	store    *storage.FlatSQLStore
	verifier *license.TokenVerifier
}

const rawFlatBufferStreamContentType = "application/vnd.sdn.flatbuffers.stream"

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
	mux.HandleFunc("/api/v1/data/scan", h.handleScan)
	mux.HandleFunc("/api/v1/data/stream", h.handleRawStream)
	mux.HandleFunc("/api/v1/data/query", h.handleRawQuery)
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
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	records, err := h.store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName:        schemaName,
		ProviderID:        firstNonEmptyDataString(req.ProviderID, req.ProviderId),
		SourceName:        req.SourceName,
		BatchID:           firstNonEmptyDataString(req.BatchID, req.BatchId),
		ProducerPeerID:    firstNonEmptyDataString(req.ProducerPeerID, req.ProducerPeerId),
		ProducerPublicKey: firstNonEmptyDataString(req.ProducerPublicKey, req.ProducerPublicKeyCamel),
		PeerID:            firstNonEmptyDataString(req.PeerID, req.PeerId),
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
	schemaName := strings.TrimSpace(firstNonEmptyDataString(req.Schema, req.SchemaName))
	if schemaName == "" {
		writeError(w, http.StatusBadRequest, "schema is required")
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := req.Offset
	if strings.TrimSpace(req.Cursor) != "" {
		parsedOffset, err := parseScanCursor(req.Cursor)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		offset = parsedOffset
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, "offset must be non-negative")
		return
	}

	filter := storage.RawRecordQuery{
		SchemaName:        schemaName,
		ProviderID:        firstNonEmptyDataString(req.ProviderID, req.ProviderId),
		SourceName:        req.SourceName,
		BatchID:           firstNonEmptyDataString(req.BatchID, req.BatchId),
		ProducerPeerID:    firstNonEmptyDataString(req.ProducerPeerID, req.ProducerPeerId),
		ProducerPublicKey: firstNonEmptyDataString(req.ProducerPublicKey, req.ProducerPublicKeyCamel),
		PeerID:            firstNonEmptyDataString(req.PeerID, req.PeerId),
		Limit:             limit,
		Offset:            offset,
	}
	totalCount, err := h.store.CountRawRecords(filter)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	records, err := h.store.QueryRawRecords(filter)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		results = append(results, rawRecordRow(schemaName, rec, false))
	}
	nextCursor := ""
	if int64(offset+len(records)) < totalCount {
		nextCursor = encodeScanCursor(offset + len(records))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema":      schemaName,
		"total_count": totalCount,
		"count":       len(results),
		"limit":       limit,
		"offset":      offset,
		"cursor":      encodeScanCursor(offset),
		"next_cursor": nextCursor,
		"scan_hash":   scanHash(schemaName, records),
		"results":     results,
	})
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	schemaName := strings.TrimSpace(firstNonEmptyDataString(req.Schema, req.SchemaName))
	if schemaName == "" {
		writeError(w, http.StatusBadRequest, "schema is required")
		return
	}
	if len(req.Records) == 0 {
		writeError(w, http.StatusBadRequest, "records are required")
		return
	}
	if len(req.Records) > 1000 {
		writeError(w, http.StatusBadRequest, "records limit is 1000")
		return
	}

	records := make([]*storage.Record, 0, len(req.Records))
	for _, ref := range req.Records {
		cid := strings.TrimSpace(firstNonEmptyDataString(ref.CID, ref.ID))
		if cid == "" {
			writeError(w, http.StatusBadRequest, "record cid is required")
			return
		}
		refSchema := strings.TrimSpace(firstNonEmptyDataString(ref.Schema, ref.SchemaName))
		if refSchema != "" && refSchema != schemaName {
			writeError(w, http.StatusBadRequest, "record schema does not match stream schema")
			return
		}
		found, err := h.store.QueryRawRecords(storage.RawRecordQuery{
			SchemaName:        schemaName,
			CID:               cid,
			ProviderID:        firstNonEmptyDataString(ref.ProviderID, ref.ProviderId),
			SourceName:        ref.SourceName,
			BatchID:           firstNonEmptyDataString(ref.BatchID, ref.BatchId),
			ProducerPeerID:    firstNonEmptyDataString(ref.ProducerPeerID, ref.ProducerPeerId),
			ProducerPublicKey: firstNonEmptyDataString(ref.ProducerPublicKey, ref.ProducerPublicKeyCamel),
			PeerID:            firstNonEmptyDataString(ref.PeerID, ref.PeerId),
			Limit:             1,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(found) != 1 {
			writeError(w, http.StatusNotFound, fmt.Sprintf("record %s not found for scan ref", cid))
			return
		}
		records = append(records, found[0])
	}

	computedHash := scanHash(schemaName, records)
	if expectedHash := strings.TrimSpace(req.ScanHash); expectedHash != "" && expectedHash != computedHash {
		writeError(w, http.StatusConflict, "scan hash does not match requested record refs")
		return
	}

	w.Header().Set("X-SDN-Scan-Hash", computedHash)
	writeFlatBufferPayloadStreamWithContentType(w, schemaName, recordsToPayloads(records), rawFlatBufferStreamContentType)
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
	Limit                  int    `json:"limit"`
	Offset                 int    `json:"offset"`
	IncludeData            bool   `json:"include_data"`
}

type rawDataStreamRequest struct {
	Schema     string                `json:"schema"`
	SchemaName string                `json:"schema_name"`
	ScanHash   string                `json:"scan_hash"`
	Records    []rawDataStreamRecord `json:"records"`
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

func rawRecordRow(schemaName string, rec *storage.Record, includeData bool) map[string]interface{} {
	row := map[string]interface{}{
		"schema_name":    schemaName,
		"cid":            rec.CID,
		"peer_id":        rec.PeerID,
		"timestamp":      rec.Timestamp.UTC().Format(time.RFC3339),
		"size_bytes":     len(rec.Data),
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
			len(rec.Data),
			rec.SourceTags.ProviderID,
			rec.SourceTags.SourceName,
			rec.SourceTags.BatchID,
			rec.SourceTags.ProducerPeerID,
			rec.SourceTags.ProducerPublicKey,
		)
	}
	return hex.EncodeToString(hash.Sum(nil))
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

func (h *DataQueryHandler) bulkRecords(schemaName, day string, limit int) ([]*storage.Record, error) {
	if strings.TrimSpace(day) != "" {
		return h.store.QueryByIndexedFields(schemaName, day, nil, "", limit)
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
	w.Header().Set("X-SDN-Stream-Format", "uint32be-length-prefixed")
	w.WriteHeader(http.StatusOK)

	var lenBuf [4]byte
	for _, payload := range payloads {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
		if _, err := w.Write(lenBuf[:]); err != nil {
			return
		}
		if _, err := w.Write(payload); err != nil {
			return
		}
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
