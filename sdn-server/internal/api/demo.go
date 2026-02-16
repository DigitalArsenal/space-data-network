package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
)

var demoLog = logging.Logger("demo-api")

// DemoHandler serves the encrypted WASM demo payload and metadata.
type DemoHandler struct {
	payloadPath string
	demoDir     string
	ipfsAPIURL  string

	once    sync.Once
	payload json.RawMessage
	loadErr error

	ipfsCID string // set after successful IPFS pin
}

// NewDemoHandler creates a handler that serves the demo payload from the given path.
// The demo page (index.html) is served from the same directory as the payload file.
// If ipfsAPIURL is non-empty, the payload will be pinned to IPFS on first load.
func NewDemoHandler(payloadPath, ipfsAPIURL string) *DemoHandler {
	return &DemoHandler{
		payloadPath: payloadPath,
		demoDir:     filepath.Dir(payloadPath),
		ipfsAPIURL:  ipfsAPIURL,
	}
}

// RegisterRoutes registers demo API routes.
func (h *DemoHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/demo/payload", h.handlePayload)
	mux.HandleFunc("/api/v1/demo/info", h.handleInfo)
	mux.HandleFunc("/demo", h.handleDemoPage)
	mux.HandleFunc("/demo/", h.handleDemoPage)
}

// PinToIPFS publishes the demo payload to IPFS via the Kubo HTTP API.
// Returns the CID string on success.
func (h *DemoHandler) PinToIPFS(ctx context.Context) (string, error) {
	if h.ipfsAPIURL == "" {
		return "", fmt.Errorf("IPFS API URL not configured")
	}

	payload, err := h.loadPayload()
	if err != nil {
		return "", fmt.Errorf("load payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", h.ipfsAPIURL+"/api/v0/add", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("IPFS add: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("IPFS add HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Hash string `json:"Hash"`
		Size string `json:"Size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode IPFS response: %w", err)
	}

	h.ipfsCID = result.Hash
	demoLog.Infof("Demo payload pinned to IPFS: %s", h.ipfsCID)
	return h.ipfsCID, nil
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

	if h.ipfsCID != "" {
		info["ipfsCID"] = h.ipfsCID
		info["ipfsGatewayURL"] = "https://ipfs.io/ipfs/" + h.ipfsCID
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	json.NewEncoder(w).Encode(info)
}
