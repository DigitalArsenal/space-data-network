package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// ConjunctionHandler serves requester-facing conjunction workflow routes.
type ConjunctionHandler struct {
	store *storage.FlatSQLStore
}

type conjunctionScreenRequest struct {
	PrimarySchema      string `json:"primary_schema"`
	PrimarySchemaAlt   string `json:"primarySchema"`
	SecondarySchema    string `json:"secondary_schema"`
	SecondarySchemaAlt string `json:"secondarySchema"`
	Encrypted          bool   `json:"encrypted"`
	GrantID            string `json:"grant_id"`
	GrantIDAlt         string `json:"grantId"`
	ChannelID          string `json:"channel_id"`
	ChannelIDAlt       string `json:"channelId"`
	AssessorPeerID     string `json:"assessor_peer_id"`
	AssessorPeerIDAlt  string `json:"assessorPeerId"`
	IncludeProvenance  bool   `json:"include_provenance"`
	Limit              int    `json:"limit"`
}

// NewConjunctionHandler creates a conjunction workflow handler.
func NewConjunctionHandler(store *storage.FlatSQLStore) *ConjunctionHandler {
	return &ConjunctionHandler{store: store}
}

// RegisterRoutes registers conjunction workflow routes.
func (h *ConjunctionHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/conjunction/screen", h.handleScreen)
}

func (h *ConjunctionHandler) handleScreen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req conjunctionScreenRequest
	if r.Body != nil {
		defer r.Body.Close()
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON conjunction request")
		return
	}

	primarySchema := normalizeConjunctionSchema(firstNonEmptyDataString(req.PrimarySchema, req.PrimarySchemaAlt), "MPE.fbs")
	secondarySchema := normalizeConjunctionSchema(firstNonEmptyDataString(req.SecondarySchema, req.SecondarySchemaAlt), "OMM.fbs")
	grantID := strings.TrimSpace(firstNonEmptyDataString(req.GrantID, req.GrantIDAlt))
	channelID := strings.TrimSpace(firstNonEmptyDataString(req.ChannelID, req.ChannelIDAlt))
	assessorPeerID := strings.TrimSpace(firstNonEmptyDataString(req.AssessorPeerID, req.AssessorPeerIDAlt))
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}

	payload := map[string]interface{}{
		"workflow":         "encrypted-conjunction-assessment",
		"mode":             conjunctionMode(primarySchema, req.Encrypted),
		"status":           "pending-module-execution",
		"primary_schema":   primarySchema,
		"secondary_schema": secondarySchema,
		"encrypted":        req.Encrypted,
		"grant_id":         grantID,
		"channel_id":       channelID,
		"assessor_peer_id": assessorPeerID,
		"limit":            limit,
		"count":            0,
		"events":           []interface{}{},
		"sources":          h.sourceSummaries(primarySchema, secondarySchema, req),
		"provenance": map[string]interface{}{
			"run_at":             time.Now().UTC().Format(time.RFC3339),
			"source_schemas":     []string{primarySchema, secondarySchema},
			"encrypted":          req.Encrypted,
			"grant_id":           grantID,
			"channel_id":         channelID,
			"assessor_peer_id":   assessorPeerID,
			"result_delivery":    "local-private",
			"module_status":      "pending-module-execution",
			"include_provenance": req.IncludeProvenance,
		},
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *ConjunctionHandler) sourceSummaries(primarySchema, secondarySchema string, req conjunctionScreenRequest) []map[string]interface{} {
	sources := []map[string]interface{}{
		h.sourceSummary("primary", primarySchema, req),
		h.sourceSummary("secondary", secondarySchema, req),
	}
	return sources
}

func (h *ConjunctionHandler) sourceSummary(role, schemaName string, req conjunctionScreenRequest) map[string]interface{} {
	row := map[string]interface{}{
		"role":      role,
		"schema":    schemaName,
		"encrypted": req.Encrypted && role == "primary",
	}
	if h.store == nil {
		row["available"] = false
		row["count"] = 0
		return row
	}
	count, err := h.store.CountRawRecords(storage.RawRecordQuery{SchemaName: schemaName, Limit: req.Limit})
	if err != nil {
		row["available"] = false
		row["count"] = 0
		row["error"] = err.Error()
		return row
	}
	row["available"] = true
	row["count"] = count
	return row
}

func normalizeConjunctionSchema(value, fallback string) string {
	schema := strings.TrimSpace(value)
	if schema == "" {
		schema = fallback
	}
	if strings.HasSuffix(strings.ToLower(schema), ".fbs") {
		schema = schema[:len(schema)-len(".fbs")]
	}
	schema = strings.ToUpper(schema)
	if schema == "" {
		return fallback
	}
	return schema + ".fbs"
}

func conjunctionMode(primarySchema string, encrypted bool) string {
	if encrypted && primarySchema == "MPE.fbs" {
		return "private-maneuver-ephemeris"
	}
	if encrypted {
		return "private-ephemeris"
	}
	return "local-screening"
}
