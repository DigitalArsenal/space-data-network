package api

// POST /api/v1/query — the sandboxed read-only SELECT surface the API docs
// declared as Phase G.5, implemented for the dashboard's data explorer.
// Admin-gated: raw SQL over the store is an operator tool; the anonymous
// read surface stays the routed bulk/index endpoints.

import (
	"encoding/json"
	"net/http"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type sandboxQueryRequest struct {
	SQL     string `json:"sql"`
	MaxRows int    `json:"max_rows,omitempty"`
}

type sandboxQueryResponse struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
}

// RegisterSandboxQueryRoute mounts POST /api/v1/query behind the admin trust
// gate.
func (h *CoreAPIHandler) RegisterSandboxQueryRoute(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/query", h.requireAuth(peers.Admin, h.handleSandboxQuery))
}

func (h *CoreAPIHandler) handleSandboxQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "no store")
		return
	}
	var req sandboxQueryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	result, err := h.store.SandboxedSelect(r.Context(), req.SQL, storage.SandboxSelectCaps{MaxRows: req.MaxRows})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sandboxQueryResponse{
		Columns:   result.Columns,
		Rows:      result.Rows,
		Truncated: result.Truncated,
	})
}
