package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// ACLHandler provides admin REST endpoints for managing peer trust levels.
type ACLHandler struct {
	registry    *peers.Registry
	authHandler *auth.Handler
}

// NewACLHandler creates a new peer ACL handler.
func NewACLHandler(registry *peers.Registry, authHandler *auth.Handler) *ACLHandler {
	return &ACLHandler{
		registry:    registry,
		authHandler: authHandler,
	}
}

// RegisterRoutes registers admin ACL API routes.
func (h *ACLHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/peers", h.authHandler.RequireAuth(peers.Admin, h.handlePeers))
	mux.HandleFunc("/api/v1/admin/peers/", h.authHandler.RequireAuth(peers.Admin, h.handlePeerByID))
}

func (h *ACLHandler) handlePeers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		allPeers := h.registry.ListPeers()
		writeJSON(w, http.StatusOK, allPeers)

	case http.MethodPost:
		var req addPeerRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.PeerID == "" {
			writeError(w, http.StatusBadRequest, "peer_id is required")
			return
		}

		trustLevel := peers.Standard
		if req.TrustLevel != "" {
			parsed, err := peers.ParseTrustLevel(req.TrustLevel)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid trust_level: "+err.Error())
				return
			}
			trustLevel = parsed
		}

		pid, err := peer.Decode(req.PeerID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid peer_id: "+err.Error())
			return
		}

		tp := &peers.TrustedPeer{
			ID:         pid,
			TrustLevel: trustLevel,
			Name:       req.Name,
		}
		if err := h.registry.AddPeer(tp); err != nil {
			writeError(w, http.StatusConflict, "failed to add peer: "+err.Error())
			return
		}

		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"peer_id":     req.PeerID,
			"trust_level": trustLevel.String(),
			"added":       true,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *ACLHandler) handlePeerByID(w http.ResponseWriter, r *http.Request) {
	peerIDStr := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/peers/")
	peerIDStr = strings.TrimSuffix(peerIDStr, "/")

	// Handle trust sub-resource: /api/v1/admin/peers/{id}/trust
	if strings.HasSuffix(peerIDStr, "/trust") {
		peerIDStr = strings.TrimSuffix(peerIDStr, "/trust")
		h.handlePeerTrust(w, r, peerIDStr)
		return
	}

	if peerIDStr == "" {
		writeError(w, http.StatusBadRequest, "peer ID required in path")
		return
	}

	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid peer ID: "+err.Error())
		return
	}

	switch r.Method {
	case http.MethodGet:
		tp, err := h.registry.GetPeer(pid)
		if err != nil {
			writeError(w, http.StatusNotFound, "peer not found: "+peerIDStr)
			return
		}
		writeJSON(w, http.StatusOK, tp)

	case http.MethodDelete:
		if err := h.registry.RemovePeer(pid); err != nil {
			writeError(w, http.StatusNotFound, "peer not found: "+peerIDStr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"peer_id": peerIDStr,
			"removed": true,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *ACLHandler) handlePeerTrust(w http.ResponseWriter, r *http.Request, peerIDStr string) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TrustLevel string `json:"trust_level"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	trustLevel, err := peers.ParseTrustLevel(req.TrustLevel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid trust_level: "+err.Error())
		return
	}

	pid, err2 := peer.Decode(peerIDStr)
	if err2 != nil {
		writeError(w, http.StatusBadRequest, "invalid peer ID: "+err2.Error())
		return
	}

	if err := h.registry.SetTrustLevel(pid, trustLevel); err != nil {
		writeError(w, http.StatusNotFound, "peer not found: "+peerIDStr)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"peer_id":     peerIDStr,
		"trust_level": trustLevel.String(),
		"updated":     true,
	})
}

type addPeerRequest struct {
	PeerID     string `json:"peer_id"`
	TrustLevel string `json:"trust_level"`
	Name       string `json:"name"`
}
