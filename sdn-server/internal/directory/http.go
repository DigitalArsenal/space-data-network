package directory

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// HTTPHandler serves the public directory search API.
type HTTPHandler struct {
	svc *Service
	mux *http.ServeMux
}

// NewHTTPHandler creates a directory HTTP handler.
func NewHTTPHandler(svc *Service) *HTTPHandler {
	h := &HTTPHandler{
		svc: svc,
		mux: http.NewServeMux(),
	}
	h.mux.HandleFunc("/api/directory/nodes", h.handleNodes)
	h.mux.HandleFunc("/api/directory/users", h.handleUsers)
	return h
}

// NewAdminHTTPHandler creates the authenticated directory mutation handler.
func NewAdminHTTPHandler(svc *Service) *HTTPHandler {
	h := &HTTPHandler{
		svc: svc,
		mux: http.NewServeMux(),
	}
	h.mux.HandleFunc("/api/v1/admin/directory/import", h.handleImport)
	return h
}

// ServeHTTP implements http.Handler.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "86400")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.mux.ServeHTTP(w, r)
}

func (h *HTTPHandler) handleNodes(w http.ResponseWriter, r *http.Request) {
	h.handleSearch(w, r, KindNode, func(search string, limit int) ([]storage.DirectoryRecord, error) {
		return h.svc.SearchNodes(search, limit)
	})
}

func (h *HTTPHandler) handleUsers(w http.ResponseWriter, r *http.Request) {
	h.handleSearch(w, r, KindUser, func(search string, limit int) ([]storage.DirectoryRecord, error) {
		return h.svc.SearchUsers(search, limit)
	})
}

func (h *HTTPHandler) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.svc == nil {
		http.Error(w, "directory service not available", http.StatusServiceUnavailable)
		return
	}

	var req ImportRecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid directory import payload", http.StatusBadRequest)
		return
	}

	result, err := h.svc.ImportRecord(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(directoryImportResponse{
		Imported: result.Imported,
		Nodes:    toAPIRecords(result.Nodes),
		Users:    toAPIRecords(result.Users),
	})
}

func (h *HTTPHandler) handleSearch(w http.ResponseWriter, r *http.Request, kind string, searchFn func(string, int) ([]storage.DirectoryRecord, error)) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.svc == nil {
		http.Error(w, "directory service not available", http.StatusServiceUnavailable)
		return
	}

	search := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, err := parseDirectoryLimit(r.URL.Query().Get("limit"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	records, err := searchFn(search, limit)
	if err != nil {
		http.Error(w, "directory search unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(directorySearchResponse{
		Kind:    kind,
		Count:   len(records),
		Results: toAPIRecords(records),
	})
}

func parseDirectoryLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return -1, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if limit < 0 {
		return 0, nil
	}
	return limit, nil
}

type directorySearchResponse struct {
	Kind    string             `json:"kind"`
	Count   int                `json:"count"`
	Results []directoryAPIItem `json:"results"`
}

type directoryImportResponse struct {
	Imported int                `json:"imported"`
	Nodes    []directoryAPIItem `json:"nodes"`
	Users    []directoryAPIItem `json:"users"`
}

type directoryAPIItem struct {
	Kind           string `json:"kind"`
	PeerID         string `json:"peer_id"`
	DN             string `json:"dn,omitempty"`
	LegalName      string `json:"legal_name,omitempty"`
	BitcoinAddress string `json:"bitcoin_address,omitempty"`
	EPMCID         string `json:"epm_cid,omitempty"`
	Source         string `json:"source,omitempty"`
	EPMJSON        string `json:"epm_json,omitempty"`
	UpdatedAt      int64  `json:"updated_at,omitempty"`
}

func toAPIRecords(records []storage.DirectoryRecord) []directoryAPIItem {
	if len(records) == 0 {
		return nil
	}
	out := make([]directoryAPIItem, 0, len(records))
	for _, record := range records {
		out = append(out, directoryAPIItem{
			Kind:           record.Kind,
			PeerID:         record.PeerID,
			DN:             record.DN,
			LegalName:      record.LegalName,
			BitcoinAddress: record.BitcoinAddress,
			EPMCID:         record.EPMCID,
			Source:         record.Source,
			EPMJSON:        record.EPMJSON,
			UpdatedAt:      record.UpdatedAt,
		})
	}
	return out
}
