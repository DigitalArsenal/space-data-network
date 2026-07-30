package peers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// THE PIN INTERFACE the owner asked for, 2026-07-30: "The Peers table should
// not ever show peers that have never been seen, UNLESS they have been added
// manually and 'pinned' (we need an interface for that)."
//
//	GET    /api/peers/pins          list pins
//	POST   /api/peers/pins          pin a peer (bare peer id or full multiaddr)
//	DELETE /api/peers/pins/{peerID} unpin an operator pin
//
// Trust gate: these live on the admin mux behind the same auth wall as the rest
// of /api/peers — anonymous callers never reach the handler.
//
// JSON keys are lowercase: API-synthesized fields, not SDS record fields
// (standing capitalization rule). Errors carry {"code","message"} so the UI can
// tell "already pinned" from "this pin is owned by the config file" and say
// which, rather than failing silently.

// pinRequest is the POST /api/peers/pins body. PeerID accepts either a bare
// peer id or a full multiaddr ending in /p2p/<id>, because an operator adding a
// peer by hand has whichever one they were given.
type pinRequest struct {
	PeerID string   `json:"peer_id"`
	Addrs  []string `json:"addrs,omitempty"`
	Name   string   `json:"name,omitempty"`
	Note   string   `json:"note,omitempty"`
}

func writePinError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

func writePinJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// parsePinTarget accepts a bare peer id or a multiaddr and returns the peer id
// plus any addresses carried alongside it.
func parsePinTarget(value string, extra []string) (peer.ID, []string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil, errors.New("peer_id is required")
	}

	addrs := make([]string, 0, len(extra)+1)
	appendAddr := func(a string) {
		a = strings.TrimSpace(a)
		if a == "" {
			return
		}
		for _, existing := range addrs {
			if existing == a {
				return
			}
		}
		addrs = append(addrs, a)
	}

	var id peer.ID
	if strings.HasPrefix(value, "/") {
		info, err := ParsePeerMultiaddr(value)
		if err != nil {
			return "", nil, err
		}
		id = info.ID
		for _, a := range info.Addrs {
			appendAddr(a.String())
		}
	} else {
		decoded, err := peer.Decode(value)
		if err != nil {
			return "", nil, err
		}
		id = decoded
	}

	for _, a := range extra {
		trimmed := strings.TrimSpace(a)
		if trimmed == "" {
			continue
		}
		if _, err := multiaddr.NewMultiaddr(trimmed); err != nil {
			return "", nil, err
		}
		appendAddr(trimmed)
	}
	return id, addrs, nil
}

// handlePins serves GET and POST on /api/peers/pins.
func (h *APIHandler) handlePins(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writePinJSON(w, http.StatusOK, map[string]any{"pins": h.registry.Pins().List()})
	case http.MethodPost:
		h.createPin(w, r)
	default:
		writePinError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

// handlePinByID serves DELETE on /api/peers/pins/{peerID}.
func (h *APIHandler) handlePinByID(w http.ResponseWriter, r *http.Request) {
	idStr := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/peers/pins/"), "/")
	if idStr == "" {
		h.handlePins(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		writePinError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use DELETE")
		return
	}
	if _, err := peer.Decode(idStr); err != nil {
		writePinError(w, http.StatusBadRequest, "invalid_peer", "not a valid peer id: "+idStr)
		return
	}

	err := h.registry.Pins().Unpin(idStr)
	switch {
	case errors.Is(err, ErrPinNotFound):
		writePinError(w, http.StatusNotFound, "not_pinned", "this peer is not pinned")
	case errors.Is(err, ErrConfigPin):
		// The row is locked because a REAL file owns it — name that file
		// rather than leaving the operator to guess.
		message := "this pin is declared in the node's config file under peers.trusted_peers — remove it there"
		if pin, ok := h.registry.Pins().Get(idStr); ok && strings.TrimSpace(pin.Note) != "" {
			message = "this pin is declared in " + pin.Note + " — remove it there"
		}
		writePinError(w, http.StatusConflict, "config_pin", message)
	case err != nil:
		writePinError(w, http.StatusInternalServerError, "pin_store_error", err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *APIHandler) createPin(w http.ResponseWriter, r *http.Request) {
	var req pinRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writePinError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	id, addrs, err := parsePinTarget(req.PeerID, req.Addrs)
	if err != nil {
		writePinError(w, http.StatusBadRequest, "invalid_peer", err.Error())
		return
	}

	if existing, ok := h.registry.Pins().Get(id.String()); ok && existing.Source == PinSourceConfig {
		writePinError(w, http.StatusConflict, "config_pin",
			"this peer is already pinned by the node's config file under peers.trusted_peers")
		return
	}

	pin, err := h.registry.Pins().Pin(Pin{
		PeerID: id.String(),
		Addrs:  addrs,
		Name:   strings.TrimSpace(req.Name),
		Note:   strings.TrimSpace(req.Note),
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrPinLimit):
			writePinError(w, http.StatusConflict, "pin_limit", err.Error())
		default:
			writePinError(w, http.StatusInternalServerError, "pin_store_error", err.Error())
		}
		return
	}

	// A pin is also a statement that we want to know this peer, so give it a
	// registry entry if it has none. Trust is NOT granted here: pinning decides
	// what an operator SEES, never what a peer may DO. Raising trust stays an
	// explicit act through the trust endpoint.
	if _, getErr := h.registry.GetPeer(id); getErr == ErrPeerNotFound {
		tp := &TrustedPeer{ID: id, TrustLevel: Standard, Name: pin.Name, AddrsStrings: addrs}
		for _, a := range addrs {
			if parsed, parseErr := multiaddr.NewMultiaddr(a); parseErr == nil {
				tp.Addrs = append(tp.Addrs, parsed)
			}
		}
		if addErr := h.registry.AddPeer(tp); addErr != nil && addErr != ErrPeerAlreadyExists {
			log.Warnf("pin: registry entry for %s could not be created: %v", id.ShortString(), addErr)
		}
	}

	writePinJSON(w, http.StatusCreated, map[string]any{"pin": pin})
}
