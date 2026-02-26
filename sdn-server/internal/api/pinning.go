package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/pubsub"
)

// PinningHandler provides admin REST endpoints for managing TipQueue pinning policy.
type PinningHandler struct {
	tipConfig   *pubsub.TipQueueConfig
	authHandler *auth.Handler
}

// NewPinningHandler creates a new pinning policy handler.
func NewPinningHandler(tipConfig *pubsub.TipQueueConfig, authHandler *auth.Handler) *PinningHandler {
	return &PinningHandler{
		tipConfig:   tipConfig,
		authHandler: authHandler,
	}
}

// RegisterRoutes registers admin pinning API routes.
func (h *PinningHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/pinning", h.authHandler.RequireAuth(peers.Admin, h.handlePinning))
	mux.HandleFunc("/api/v1/admin/pinning/schema/", h.authHandler.RequireAuth(peers.Admin, h.handleSchemaPolicy))
	mux.HandleFunc("/api/v1/admin/pinning/source/", h.authHandler.RequireAuth(peers.Admin, h.handleSourcePolicy))
}

func (h *PinningHandler) handlePinning(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.tipConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "pinning configuration unavailable")
		return
	}

	// Return current pinning configuration overview
	schemas := make(map[string]interface{})
	for _, name := range []string{"OMM.fbs", "CDM.fbs", "CAT.fbs", "OEM.fbs", "TDM.fbs", "MPE.fbs"} {
		if cfg, ok := h.tipConfig.GetSchemaDefault(name); ok {
			schemas[name] = cfg
		}
	}

	sources := h.tipConfig.ListTrustedSources()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schemas":         schemas,
		"trusted_sources": sources,
	})
}

func (h *PinningHandler) handleSchemaPolicy(w http.ResponseWriter, r *http.Request) {
	schema := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/pinning/schema/")
	schema = strings.TrimSuffix(schema, "/")
	if schema == "" {
		writeError(w, http.StatusBadRequest, "schema name required in path")
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg, ok := h.tipConfig.GetSchemaDefault(schema)
		if !ok {
			writeError(w, http.StatusNotFound, "no policy for schema: "+schema)
			return
		}
		writeJSON(w, http.StatusOK, cfg)

	case http.MethodPut:
		var cfg pubsub.SchemaConfig
		if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		h.tipConfig.SetSchemaDefault(schema, &cfg)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"schema":  schema,
			"updated": true,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *PinningHandler) handleSourcePolicy(w http.ResponseWriter, r *http.Request) {
	peerID := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/pinning/source/")
	peerID = strings.TrimSuffix(peerID, "/")
	if peerID == "" {
		writeError(w, http.StatusBadRequest, "peer ID required in path")
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg, ok := h.tipConfig.GetSourceConfig(peerID)
		if !ok {
			writeError(w, http.StatusNotFound, "no source policy for peer: "+peerID)
			return
		}
		writeJSON(w, http.StatusOK, cfg)

	case http.MethodPut:
		var cfg pubsub.SourceConfig
		if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		h.tipConfig.SetSourceOverride(peerID, &cfg)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"peer_id": peerID,
			"updated": true,
		})

	case http.MethodDelete:
		// Trust/untrust toggle
		if h.tipConfig.IsTrusted(peerID) {
			h.tipConfig.UntrustSource(peerID)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"peer_id": peerID,
			"removed": true,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
