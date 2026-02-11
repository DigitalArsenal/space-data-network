// Package api provides HTTP API endpoints for the SDN server.
package api

import (
	"crypto/sha256"
	"encoding/base64"
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

	records, err := h.store.QueryByIndexedFields("MPE.fbs", day, nil, entityID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, day)
	if handleConditionalCache(w, r, "MPE.fbs", day, entityID, records) {
		return
	}

	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{
			"cid":       rec.CID,
			"peer_id":   rec.PeerID,
			"timestamp": rec.Timestamp.UTC().Format(time.RFC3339),
		}

		if mpe, err := decodeMPE(rec.Data); err == nil {
			row["entity_id"] = string(mpe.ENTITY_ID())
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

	records, err := h.store.QueryByIndexedFields("CAT.fbs", "", &noradID, "", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	setCachePolicy(w, "")
	if handleConditionalCache(w, r, "CAT.fbs", "", fmt.Sprintf("%d", noradID), records) {
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
	w.Header().Set("Vary", "Accept-Encoding")
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
