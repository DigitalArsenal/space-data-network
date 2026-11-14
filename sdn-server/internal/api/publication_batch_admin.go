package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/publicationstate"
	"github.com/spacedatanetwork/sdn-server/internal/publicationstate/pst"
)

func (h *DatasetPublicationHandler) handlePublicationBatch(w http.ResponseWriter, r *http.Request) {
	explicitJSON := r.URL.Query().Get("format") == "json"
	fail := func(status int, message string) {
		if explicitJSON {
			writeError(w, status, message)
			return
		}
		WriteErrorFrame(w, status, "publication_batch", message, 0)
	}
	if r.Method != http.MethodPost {
		fail(http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if err := requireLoopbackClient(r); err != nil {
		fail(http.StatusForbidden, "publication batch action is only available to local daemon clients: "+err.Error())
		return
	}
	manager, ok := h.service.(interface {
		ManagePublicationBatch(string, string, string, string, string) error
	})
	if !ok {
		fail(http.StatusServiceUnavailable, "publication batch management unavailable")
		return
	}
	var req struct {
		Schema   string `json:"schema"`
		Provider string `json:"providerId"`
		Source   string `json:"sourceName"`
		Batch    string `json:"batchId"`
		Action   string `json:"action"`
	}
	if explicitJSON {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			fail(http.StatusBadRequest, err.Error())
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			fail(http.StatusBadRequest, "expected one batch action")
			return
		}
	} else {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
		if err != nil {
			fail(http.StatusBadRequest, err.Error())
			return
		}
		body, err := publicationstate.Decode(raw, pst.BodyBatchAction)
		if err != nil {
			fail(http.StatusBadRequest, err.Error())
			return
		}
		action := body.Value.(*pst.BatchActionT)
		req.Schema, req.Provider, req.Source, req.Batch, req.Action = action.Schema, action.Provider, action.Source, action.Batch, action.Action
	}
	if strings.TrimSpace(req.Schema) == "" || strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.Batch) == "" {
		fail(http.StatusBadRequest, "exact schema, providerId, sourceName and batchId are required")
		return
	}
	if err := manager.ManagePublicationBatch(normalizeDatasetPublicationSchema(req.Schema), req.Provider, req.Source, req.Batch, req.Action); err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	if explicitJSON {
		writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
		return
	}
	WriteFrameStream(w, http.StatusOK, [][]byte{BuildQRP(QRPFields{Kind: QRPKindPage, Status: QRPStatusOk})}, nil)
}
func (s *ConcreteDatasetPublicationService) ManagePublicationBatch(schema, provider, source, batch, action string) error {
	return s.store.ManagePublicationBatch(schema, provider, source, batch, action)
}
