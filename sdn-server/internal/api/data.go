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
	"strconv"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// DataQueryHandler serves read-only, cache-friendly schema query APIs.
type DataQueryHandler struct {
	store    *storage.FlatSQLStore
	verifier *license.TokenVerifier
}

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
	mux.HandleFunc("/api/v1/data/omm", h.handleOMM)
	mux.HandleFunc("/api/v1/data/mpe", h.handleMPE)
	mux.HandleFunc("/api/v1/data/cat", h.handleCAT)
	mux.HandleFunc("/api/v1/data/secure/omm", h.handleSecureOMM)
	mux.HandleFunc("/api/v1/data/query/", h.handleGenericQuery)
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

	records, err := h.store.QueryByIndexedFields("OMM.fbs", day, nil, entityID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, day)
	if handleConditionalCache(w, r, "OMM.fbs", day, entityID, records) {
		return
	}

	mpePayloads := make([][]byte, 0, len(records))
	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		omm, err := decodeOMM(rec.Data)
		if err != nil {
			continue
		}
		mpeData := buildMPEFromOMM(omm)
		mpePayloads = append(mpePayloads, mpeData)

		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}

		epochUnix, _ := parseEpochUnix(strings.TrimSpace(string(omm.EPOCH())))
		row["entity_id"] = strings.TrimSpace(string(omm.OBJECT_ID()))
		row["epoch_unix"] = epochUnix
		row["mean_motion"] = omm.MEAN_MOTION()
		row["eccentricity"] = omm.ECCENTRICITY()
		row["inclination"] = omm.INCLINATION()

		if includeData {
			row["data_base64"] = base64.StdEncoding.EncodeToString(mpeData)
		}

		results = append(results, row)
	}

	if format == dataFormatFlatBuffers {
		writeFlatBufferPayloadStream(w, "MPE.fbs", mpePayloads)
		return
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

// handleGenericQuery serves GET /api/v1/data/query/{schema}?day=&norad_cat_id=&entity_id=&limit=&offset=&format=
// This generalizes the per-schema handlers into a single parameterized endpoint.
func (h *DataQueryHandler) handleGenericQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureStore(w) {
		return
	}

	// Extract schema from URL path: /api/v1/data/query/{schema}
	schema := strings.TrimPrefix(r.URL.Path, "/api/v1/data/query/")
	schema = strings.TrimSuffix(schema, "/")
	if schema == "" {
		writeError(w, http.StatusBadRequest, "missing schema in URL path")
		return
	}

	q := r.URL.Query()
	day := strings.TrimSpace(q.Get("day"))
	entityID := strings.TrimSpace(q.Get("entity_id"))
	limit := parseLimit(r, 100, 1000)
	format := requestedDataFormat(r)
	includeData := parseBool(r, "include_data")

	var noradPtr *uint32
	if raw := strings.TrimSpace(q.Get("norad_cat_id")); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid norad_cat_id")
			return
		}
		id := uint32(v)
		noradPtr = &id
	}

	if day != "" {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			writeError(w, http.StatusBadRequest, "invalid day (expected YYYY-MM-DD)")
			return
		}
	}

	records, err := h.store.QueryByIndexedFields(schema, day, noradPtr, entityID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, day)

	if format == dataFormatFlatBuffers {
		writeFlatBufferStream(w, schema, records)
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}
		if includeData {
			row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
		}
		results = append(results, row)
	}

	queryInfo := map[string]interface{}{
		"limit": limit,
	}
	if day != "" {
		queryInfo["day"] = day
	}
	if noradPtr != nil {
		queryInfo["norad_cat_id"] = *noradPtr
	}
	if entityID != "" {
		queryInfo["entity_id"] = entityID
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema":  schema,
		"query":   queryInfo,
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

func (h *DataQueryHandler) ensureStore(w http.ResponseWriter) bool {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "local storage unavailable in edge mode")
		return false
	}
	return true
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

func writeFlatBufferStream(w http.ResponseWriter, schema string, records []*storage.Record) {
	payloads := make([][]byte, 0, len(records))
	for _, rec := range records {
		payloads = append(payloads, rec.Data)
	}
	writeFlatBufferPayloadStream(w, schema, payloads)
}

func writeFlatBufferPayloadStream(w http.ResponseWriter, schema string, payloads [][]byte) {
	w.Header().Set("Content-Type", "application/x-flatbuffers")
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

func buildMPEFromOMM(omm *OMM.OMM) []byte {
	builder := flatbuffers.NewBuilder(256)
	entityID := strings.TrimSpace(string(omm.OBJECT_ID()))
	if entityID == "" && omm.NORAD_CAT_ID() > 0 {
		entityID = fmt.Sprintf("NORAD-%d", omm.NORAD_CAT_ID())
	}
	entityIDOffset := builder.CreateString(entityID)

	epochUnix, _ := parseEpochUnix(strings.TrimSpace(string(omm.EPOCH())))

	MPE.MPEStart(builder)
	MPE.MPEAddENTITY_ID(builder, entityIDOffset)
	if epochUnix > 0 {
		MPE.MPEAddEPOCH(builder, float64(epochUnix))
	}
	if v := omm.MEAN_MOTION(); v != 0 {
		MPE.MPEAddMEAN_MOTION(builder, v)
	}
	if v := omm.ECCENTRICITY(); v != 0 {
		MPE.MPEAddECCENTRICITY(builder, v)
	}
	if v := omm.INCLINATION(); v != 0 {
		MPE.MPEAddINCLINATION(builder, v)
	}
	if v := omm.RA_OF_ASC_NODE(); v != 0 {
		MPE.MPEAddRA_OF_ASC_NODE(builder, v)
	}
	if v := omm.ARG_OF_PERICENTER(); v != 0 {
		MPE.MPEAddARG_OF_PERICENTER(builder, v)
	}
	if v := omm.MEAN_ANOMALY(); v != 0 {
		MPE.MPEAddMEAN_ANOMALY(builder, v)
	}
	if v := omm.BSTAR(); v != 0 {
		MPE.MPEAddBSTAR(builder, v)
	}

	mpe := MPE.MPEEnd(builder)
	MPE.FinishSizePrefixedMPEBuffer(builder, mpe)

	out := make([]byte, len(builder.FinishedBytes()))
	copy(out, builder.FinishedBytes())
	return out
}

func parseEpochUnix(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("empty epoch")
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Unix(), nil
		}
	}

	if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 {
		return int64(f), nil
	}

	return 0, fmt.Errorf("unsupported epoch format: %q", raw)
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
