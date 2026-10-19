package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	logging "github.com/ipfs/go-log/v2"
)

var demoLog = logging.Logger("demo-api")

// DemoHandler serves the encrypted WASM demo payload and metadata.
type DemoHandler struct {
	payloadPath string
	demoDir     string

	once    sync.Once
	payload json.RawMessage
	loadErr error
}

// NewDemoHandler creates a handler that serves the demo payload from the given path.
// The demo page (index.html) is served from the same directory as the payload file.
func NewDemoHandler(payloadPath string) *DemoHandler {
	return &DemoHandler{
		payloadPath: payloadPath,
		demoDir:     filepath.Dir(payloadPath),
	}
}

// RegisterRoutes registers demo API routes.
func (h *DemoHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/demo/payload", h.handlePayload)
	mux.HandleFunc("/api/v1/demo/info", h.handleInfo)
	mux.HandleFunc("/demo", h.handleDemoPage)
	mux.HandleFunc("/demo/", h.handleDemoPage)
}

func (h *DemoHandler) loadPayload() (json.RawMessage, error) {
	h.once.Do(func() {
		data, err := os.ReadFile(h.payloadPath)
		if err != nil {
			h.loadErr = err
			return
		}
		if !json.Valid(data) {
			h.loadErr = os.ErrInvalid
			return
		}
		h.payload = json.RawMessage(data)
	})
	return h.payload, h.loadErr
}

// handleDemoPage serves the demo HTML page.
func (h *DemoHandler) handleDemoPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.ServeFile(w, r, filepath.Join(h.demoDir, "index.html"))
}

// handlePayload serves the full encrypted demo payload (encrypted WASM + wrapped DEKs).
func (h *DemoHandler) handlePayload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	payload, err := h.loadPayload()
	if err != nil {
		http.Error(w, "demo payload not available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(payload)
}

// handleInfo serves demo metadata (plugin ID, version, server public key, IPFS CID).
func (h *DemoHandler) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	payload, err := h.loadPayload()
	if err != nil {
		http.Error(w, "demo payload not available", http.StatusServiceUnavailable)
		return
	}

	var full struct {
		Version         int      `json:"version"`
		PluginID        string   `json:"pluginId"`
		EpochPeriodMs   int64    `json:"epochPeriodMs"`
		CurrentEpoch    int      `json:"currentEpoch"`
		CreatedAt       string   `json:"createdAt"`
		ServerPubKeyHex string   `json:"serverPublicKeyHex"`
		Domains         []string `json:"domains"`
		WasmSize        int      `json:"wasmSize"`
	}
	if err := json.Unmarshal(payload, &full); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	info := map[string]interface{}{
		"version":            full.Version,
		"pluginId":           full.PluginID,
		"epochPeriodMs":      full.EpochPeriodMs,
		"currentEpoch":       full.CurrentEpoch,
		"createdAt":          full.CreatedAt,
		"serverPublicKeyHex": full.ServerPubKeyHex,
		"domains":            full.Domains,
		"wasmSize":           full.WasmSize,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	json.NewEncoder(w).Encode(info)
}
