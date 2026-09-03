package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/aiproviders"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/credstore"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const aiProviderRequestMaxBytes = 16 << 10

// AIProvidersHandler exposes secret-free provider connection state and
// write-only connection actions on the authenticated admin surface.
type AIProvidersHandler struct {
	service     *aiproviders.Service
	authHandler *auth.Handler
	requireAuth bool
}

// NewAIProvidersHandler builds the admin-only AI provider API.
func NewAIProvidersHandler(store *credstore.Store, authHandler *auth.Handler, requireAuth bool) *AIProvidersHandler {
	return &AIProvidersHandler{
		service:     aiproviders.NewService(store),
		authHandler: authHandler,
		requireAuth: requireAuth,
	}
}

// RegisterRoutes mounts the exact AI provider connection routes. The gate is
// inside every route so this credential surface fails closed even when the
// node-wide authentication wall is disabled or bypassed by gateway policy.
func (h *AIProvidersHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/ai/providers", h.gate(h.handleProviders))
	mux.HandleFunc("POST /api/v1/ai/providers/{id}/connect", h.gate(h.handleConnect))
	mux.HandleFunc("POST /api/v1/ai/providers/{id}/connect/device/start", h.gate(h.handleDeviceStart))
	mux.HandleFunc("POST /api/v1/ai/providers/{id}/connect/device/poll", h.gate(h.handleDevicePoll))
	mux.HandleFunc("POST /api/v1/ai/providers/{id}/test", h.gate(h.handleTest))
	mux.HandleFunc("POST /api/v1/ai/providers/{id}/disconnect", h.gate(h.handleDisconnect))
}

func (h *AIProvidersHandler) gate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.requireAuth || h.authHandler == nil {
			writeError(w, http.StatusServiceUnavailable,
				"AI provider connections are unavailable because node authentication is disabled; enable admin.require_auth")
			return
		}
		if h.service == nil {
			writeError(w, http.StatusServiceUnavailable, "AI provider connections are unavailable")
			return
		}
		h.authHandler.RequireAuth(peers.Admin, next)(w, r)
	}
}

func (h *AIProvidersHandler) handleProviders(w http.ResponseWriter, _ *http.Request) {
	providers, err := h.service.Providers()
	if err != nil {
		writeAIProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}

func (h *AIProvidersHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIKey string `json:"api_key"`
	}
	if err := decodeAIProviderRequest(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "API key is required")
		return
	}
	provider, err := h.service.ConnectAPIKey(r.PathValue("id"), body.APIKey)
	if err != nil {
		writeAIProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, provider)
}

func (h *AIProvidersHandler) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	start, err := h.service.StartDevice(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAIProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, start)
}

func (h *AIProvidersHandler) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FlowID string `json:"flow_id"`
	}
	if err := decodeAIProviderRequest(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.FlowID) == "" {
		writeError(w, http.StatusBadRequest, "device sign-in flow is required")
		return
	}
	result, err := h.service.PollDevice(r.Context(), r.PathValue("id"), body.FlowID)
	if err != nil {
		writeAIProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *AIProvidersHandler) handleTest(w http.ResponseWriter, r *http.Request) {
	testedAt, err := h.service.TestConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAIProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tested_at": testedAt})
}

func (h *AIProvidersHandler) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	provider, err := h.service.Disconnect(r.PathValue("id"))
	if err != nil {
		writeAIProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, provider)
}

func decodeAIProviderRequest(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, aiProviderRequestMaxBytes))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func writeAIProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, aiproviders.ErrUnknownProvider):
		writeError(w, http.StatusNotFound, "AI provider not found")
	case errors.Is(err, aiproviders.ErrUnsupportedAuth):
		writeError(w, http.StatusBadRequest, "this sign-in method is not available for the provider")
	case errors.Is(err, aiproviders.ErrNotConnected):
		writeError(w, http.StatusConflict, "AI provider is not connected")
	case errors.Is(err, aiproviders.ErrInvalidFlow):
		writeError(w, http.StatusNotFound, "device sign-in was not found or has expired")
	case errors.Is(err, aiproviders.ErrDeviceExpired), errors.Is(err, aiproviders.ErrDeviceDenied):
		writeError(w, http.StatusConflict, err.Error())
	default:
		// Upstream and keystore errors are intentionally collapsed. They may be
		// logged by callers, but no submitted secret or token is ever serialized.
		writeError(w, http.StatusBadGateway, "AI provider request failed")
	}
}
