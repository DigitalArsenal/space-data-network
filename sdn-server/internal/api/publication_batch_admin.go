package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (h *DatasetPublicationHandler) handlePublicationBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireDatasetUpdatesLocalClient(w, r, "publication batch action") {
		return
	}
	manager, ok := h.service.(interface {
		ManagePublicationBatch(string, string, string, string, string) error
	})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "publication batch management unavailable")
		return
	}
	var req struct {
		Schema   string `json:"schema"`
		Provider string `json:"providerId"`
		Source   string `json:"sourceName"`
		Batch    string `json:"batchId"`
		Action   string `json:"action"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Schema) == "" || strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.Batch) == "" {
		writeError(w, http.StatusBadRequest, "exact schema, providerId, sourceName and batchId are required")
		return
	}
	if err := manager.ManagePublicationBatch(normalizeDatasetPublicationSchema(req.Schema), req.Provider, req.Source, req.Batch, req.Action); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}
func (s *ConcreteDatasetPublicationService) ManagePublicationBatch(schema, provider, source, batch, action string) error {
	return s.store.ManagePublicationBatch(schema, provider, source, batch, action)
}
