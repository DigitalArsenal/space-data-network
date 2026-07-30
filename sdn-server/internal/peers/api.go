// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/vcard"
)

// APIHandler provides HTTP endpoints for peer management.
type APIHandler struct {
	registry *Registry
	gater    *TrustedConnectionGater
	mux      *http.ServeMux
}

// NewAPIHandler creates a new API handler.
func NewAPIHandler(registry *Registry, gater *TrustedConnectionGater) *APIHandler {
	h := &APIHandler{
		registry: registry,
		gater:    gater,
		mux:      http.NewServeMux(),
	}
	h.setupRoutes()
	return h
}

// ServeHTTP implements http.Handler.
func (h *APIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Admin API is served from the same origin; no wildcard CORS.
	// Only set CORS headers for OPTIONS pre-flight so same-origin
	// requests work normally without exposing the API cross-origin.
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.WriteHeader(http.StatusOK)
		return
	}

	h.mux.ServeHTTP(w, r)
}

func (h *APIHandler) setupRoutes() {
	// Peer management endpoints
	h.mux.HandleFunc("/api/peers", h.handlePeers)
	h.mux.HandleFunc("/api/peers/", h.handlePeerByID)

	// Pin management (owner ruling 2026-07-30). Registered as its own pattern
	// so ServeMux's longest-match sends /api/peers/pins here instead of to the
	// /api/peers/ subtree, which would try to decode "pins" as a peer id.
	h.mux.HandleFunc("/api/peers/pins", h.handlePins)
	h.mux.HandleFunc("/api/peers/pins/", h.handlePinByID)

	// Group management endpoints
	h.mux.HandleFunc("/api/groups", h.handleGroups)
	h.mux.HandleFunc("/api/groups/", h.handleGroupByName)

	// Blocklist management
	h.mux.HandleFunc("/api/blocklist", h.handleBlocklist)
	h.mux.HandleFunc("/api/blocklist/", h.handleBlocklistByID)

	// Settings
	h.mux.HandleFunc("/api/settings", h.handleSettings)

	// Import/Export
	h.mux.HandleFunc("/api/export", h.handleExport)
	h.mux.HandleFunc("/api/import", h.handleImport)

	// vCard import/export
	h.mux.HandleFunc("/api/peers/import/vcard", h.handleVCardImport)
	h.mux.HandleFunc("/api/peers/export/vcard/", h.handleVCardExport)
}

// handlePeers handles GET /api/peers and POST /api/peers
func (h *APIHandler) handlePeers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		h.listPeers(w, r)
	case "POST":
		h.addPeer(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePeerByID handles /api/peers/:id endpoints
func (h *APIHandler) handlePeerByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/peers/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Peer ID required", http.StatusBadRequest)
		return
	}

	peerIDStr := parts[0]
	peerID, err := peer.Decode(peerIDStr)
	if err != nil {
		http.Error(w, "Invalid peer ID", http.StatusBadRequest)
		return
	}

	// Check for sub-resources
	if len(parts) > 1 {
		switch parts[1] {
		case "trust":
			h.handlePeerTrust(w, r, peerID)
			return
		case "stats":
			h.handlePeerStats(w, r, peerID)
			return
		case "epm":
			// /api/peers/:id/epm, /api/peers/:id/epm/vcard, /api/peers/:id/epm/qr
			subFormat := ""
			if len(parts) > 2 {
				subFormat = parts[2]
			}
			h.handlePeerEPM(w, r, peerID, subFormat)
			return
		}
	}

	switch r.Method {
	case "GET":
		h.getPeer(w, r, peerID)
	case "PUT":
		h.updatePeer(w, r, peerID)
	case "DELETE":
		h.removePeer(w, r, peerID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// listPeers returns all peers.
func (h *APIHandler) listPeers(w http.ResponseWriter, r *http.Request) {
	peers := h.registry.ListPeers()

	// Apply filters
	trustLevelFilter := r.URL.Query().Get("trust_level")
	groupFilter := r.URL.Query().Get("group")
	orgFilter := r.URL.Query().Get("organization")

	filtered := make([]*TrustedPeer, 0)
	for _, p := range peers {
		if trustLevelFilter != "" {
			level, _ := ParseTrustLevel(trustLevelFilter)
			if p.TrustLevel != level {
				continue
			}
		}
		if groupFilter != "" {
			hasGroup := false
			for _, g := range p.Groups {
				if g == groupFilter {
					hasGroup = true
					break
				}
			}
			if !hasGroup {
				continue
			}
		}
		if orgFilter != "" && p.Organization != orgFilter {
			continue
		}
		filtered = append(filtered, p)
	}

	views := make([]PeerView, 0, len(filtered))
	for _, tp := range filtered {
		views = append(views, h.peerView(tp))
	}
	writeJSON(w, views)
}

// getPeer returns a single peer.
func (h *APIHandler) getPeer(w http.ResponseWriter, r *http.Request, peerID peer.ID) {
	tp, err := h.registry.GetPeer(peerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, h.peerView(tp))
}

// PeerView is the JSON view served for peer-info reads (GET /api/peers,
// GET /api/peers/:id): the persisted TrustedPeer record plus trust values
// computed LIVE from the web-of-trust graph (Phase C2). These computed
// fields are never persisted — they are recalculated on every response so
// Phase D (auto-subscribe/auto-pin) and any UI can key off "fully
// trusted" / "computed valid" without the registry itself carrying stale
// derived state.
type PeerView struct {
	TrustedPeer *TrustedPeer `json:"-"`

	// EffectiveTrustLevel is the direct assignment augmented by computed
	// web-of-trust validity (see Registry.EffectiveTrustLevel).
	EffectiveTrustLevel TrustLevel `json:"effective_trust_level"`
	// ComputedValid reports ROOTED PGP web-of-trust validity independent of
	// the direct assignment (see Registry.IsValid / ComputeValidityRooted).
	ComputedValid bool `json:"computed_valid"`
	// MarginalTrusterCount / FullTrusterCount are the tallies behind
	// ComputedValid, exposed for UI/debugging.
	MarginalTrusterCount int `json:"marginal_truster_count"`
	FullTrusterCount     int `json:"full_truster_count"`
	// FullyTrusted is EffectiveTrustLevel >= Full — the Phase D
	// auto-subscribe/auto-pin trigger.
	FullyTrusted bool `json:"fully_trusted"`
}

// MarshalJSON merges TrustedPeer's own custom JSON encoding with the
// computed-only fields above (a plain embed would let TrustedPeer's
// MarshalJSON shadow PeerView's extra fields, since Go promotes the
// embedded Marshaler).
func (v PeerView) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(v.TrustedPeer)
	if err != nil {
		return nil, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	extra, err := json.Marshal(struct {
		EffectiveTrustLevel  TrustLevel `json:"effective_trust_level"`
		ComputedValid        bool       `json:"computed_valid"`
		MarginalTrusterCount int        `json:"marginal_truster_count"`
		FullTrusterCount     int        `json:"full_truster_count"`
		FullyTrusted         bool       `json:"fully_trusted"`
	}{
		EffectiveTrustLevel:  v.EffectiveTrustLevel,
		ComputedValid:        v.ComputedValid,
		MarginalTrusterCount: v.MarginalTrusterCount,
		FullTrusterCount:     v.FullTrusterCount,
		FullyTrusted:         v.FullyTrusted,
	})
	if err != nil {
		return nil, err
	}
	var extraMap map[string]json.RawMessage
	if err := json.Unmarshal(extra, &extraMap); err != nil {
		return nil, err
	}
	for k, val := range extraMap {
		merged[k] = val
	}
	return json.Marshal(merged)
}

// peerView computes a PeerView for tp against the handler's registry.
func (h *APIHandler) peerView(tp *TrustedPeer) PeerView {
	valid, marginal, full := h.registry.ComputedValidity(tp.ID)
	return PeerView{
		TrustedPeer:          tp,
		EffectiveTrustLevel:  h.registry.EffectiveTrustLevel(tp.ID),
		ComputedValid:        valid,
		MarginalTrusterCount: marginal,
		FullTrusterCount:     full,
		FullyTrusted:         h.registry.IsFullyTrusted(tp.ID),
	}
}

// AddPeerRequest is the request body for adding a peer.
//
// Identity may be supplied three ways (see ResolvePeerIdentifier, identity.go):
// id, its alias peer_id, or public_key — the key material an operator is more
// likely to have been handed. Supplying both an id and a public_key is allowed
// only when they agree.
//
// VCard is the optional contact card for the peer. When present it is stored
// verbatim as TrustedPeer.VCardData, and the EPM record derivable from it
// (vcard.VCardToEPM — which honours an EPM already embedded in the card) is
// stored as TrustedPeer.EPMData. A card that yields no EPM is still stored as
// VCardData; the request is not failed for it.
type AddPeerRequest struct {
	ID           string            `json:"id"`
	PeerID       string            `json:"peer_id,omitempty"`
	PublicKey    string            `json:"public_key,omitempty"`
	Addrs        []string          `json:"addrs,omitempty"`
	TrustLevel   string            `json:"trust_level"`
	Name         string            `json:"name,omitempty"`
	Organization string            `json:"organization,omitempty"`
	Groups       []string          `json:"groups,omitempty"`
	Notes        string            `json:"notes,omitempty"`
	VCard        string            `json:"vcard,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// epmFromVCard returns the EPM record derivable from a contact card, or nil
// when the card carries nothing convertible. Conversion failure is not an
// error for the caller: the card itself is still worth storing, and the peer
// registry never invents contact data it could not read.
func epmFromVCard(vcardStr string) []byte {
	if strings.TrimSpace(vcardStr) == "" {
		return nil
	}
	epmBytes, err := vcard.VCardToEPM(vcardStr)
	if err != nil || len(epmBytes) == 0 {
		return nil
	}
	return epmBytes
}

// addPeer adds a new peer.
func (h *APIHandler) addPeer(w http.ResponseWriter, r *http.Request) {
	var req AddPeerRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	peerID, err := ResolvePeerIdentifier(req.ID, req.PeerID, req.PublicKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	trustLevel, err := ParseTrustLevel(req.TrustLevel)
	if err != nil {
		trustLevel = Standard
	}

	addrs := make([]multiaddr.Multiaddr, 0, len(req.Addrs))
	for _, addrStr := range req.Addrs {
		if addr, err := multiaddr.NewMultiaddr(addrStr); err == nil {
			addrs = append(addrs, addr)
		}
	}

	tp := &TrustedPeer{
		ID:           peerID,
		Addrs:        addrs,
		TrustLevel:   trustLevel,
		Name:         req.Name,
		Organization: req.Organization,
		Groups:       req.Groups,
		Notes:        req.Notes,
		AddedAt:      time.Now(),
		Metadata:     req.Metadata,
		VCardData:    strings.TrimSpace(req.VCard),
		EPMData:      epmFromVCard(req.VCard),
	}

	if err := h.registry.AddPeer(tp); err != nil {
		if err == ErrPeerAlreadyExists {
			http.Error(w, err.Error(), http.StatusConflict)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, tp)
}

// updatePeer updates an existing peer.
func (h *APIHandler) updatePeer(w http.ResponseWriter, r *http.Request, peerID peer.ID) {
	existing, err := h.registry.GetPeer(peerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	var req AddPeerRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// The URL owns the identity of the record being updated. Identifier
	// fields in the body may restate it but must never redirect the write to
	// a different peer: a public_key that derives some other peer ID is a
	// mistake worth surfacing, not a silent no-op.
	if strings.TrimSpace(req.ID) != "" || strings.TrimSpace(req.PeerID) != "" || strings.TrimSpace(req.PublicKey) != "" {
		bodyID, err := ResolvePeerIdentifier(req.ID, req.PeerID, req.PublicKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if bodyID != peerID {
			http.Error(w, "body identifies peer "+bodyID.String()+", which does not match the request path", http.StatusBadRequest)
			return
		}
	}

	// Update fields if provided
	if req.TrustLevel != "" {
		if level, err := ParseTrustLevel(req.TrustLevel); err == nil {
			existing.TrustLevel = level
		}
	}
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Organization != "" {
		existing.Organization = req.Organization
	}
	if req.Groups != nil {
		existing.Groups = req.Groups
	}
	if req.Notes != "" {
		existing.Notes = req.Notes
	}
	if req.Metadata != nil {
		existing.Metadata = req.Metadata
	}
	if card := strings.TrimSpace(req.VCard); card != "" {
		existing.VCardData = card
		if epmBytes := epmFromVCard(card); len(epmBytes) > 0 {
			existing.EPMData = epmBytes
		}
	}
	if len(req.Addrs) > 0 {
		addrs := make([]multiaddr.Multiaddr, 0, len(req.Addrs))
		for _, addrStr := range req.Addrs {
			if addr, err := multiaddr.NewMultiaddr(addrStr); err == nil {
				addrs = append(addrs, addr)
			}
		}
		existing.Addrs = addrs
	}

	if err := h.registry.UpdatePeer(existing); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, existing)
}

// removePeer removes a peer.
func (h *APIHandler) removePeer(w http.ResponseWriter, r *http.Request, peerID peer.ID) {
	if err := h.registry.RemovePeer(peerID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateTrustRequest is the request body for updating trust level.
type UpdateTrustRequest struct {
	TrustLevel string `json:"trust_level"`
}

// handlePeerTrust handles PUT /api/peers/:id/trust
func (h *APIHandler) handlePeerTrust(w http.ResponseWriter, r *http.Request, peerID peer.ID) {
	if r.Method != "PUT" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req UpdateTrustRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	level, err := ParseTrustLevel(req.TrustLevel)
	if err != nil {
		http.Error(w, "Invalid trust level: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.registry.SetTrustLevel(peerID, level); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	tp, _ := h.registry.GetPeer(peerID)
	writeJSON(w, tp)
}

// handlePeerStats handles GET /api/peers/:id/stats
func (h *APIHandler) handlePeerStats(w http.ResponseWriter, r *http.Request, peerID peer.ID) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tp, err := h.registry.GetPeer(peerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	stats := ConnectionStats{
		PeerID:           peerID,
		LastConnected:    tp.LastConnected,
		ConnectionCount:  tp.ConnectionCount,
		MessagesReceived: tp.MessagesReceived,
		MessagesSent:     tp.MessagesSent,
		BytesReceived:    tp.BytesReceived,
		BytesSent:        tp.BytesSent,
	}

	writeJSON(w, stats)
}

// handlePeerEPM handles GET /api/peers/:id/epm[/vcard|/qr]
func (h *APIHandler) handlePeerEPM(w http.ResponseWriter, r *http.Request, peerID peer.ID, format string) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tp, err := h.registry.GetPeer(peerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	switch format {
	case "vcard":
		vcardData := tp.VCardData
		if vcardData == "" {
			// Try to generate from EPM data
			if len(tp.EPMData) > 0 {
				if vc, err := vcard.EPMToVCard(tp.EPMData); err == nil {
					vcardData = vc
				}
			}
		}
		if vcardData == "" {
			// Fall back to generating from TrustedPeer fields
			vcardData = TrustedPeerToVCard(tp)
		}
		w.Header().Set("Content-Type", "text/vcard")
		w.Header().Set("Content-Disposition", "attachment; filename=peer-"+peerID.ShortString()+".vcf")
		w.Write([]byte(vcardData))

	case "qr":
		var qrData []byte
		if len(tp.EPMData) > 0 {
			qrData, err = vcard.EPMToQR(tp.EPMData, 256)
		} else {
			vcardData := TrustedPeerToVCard(tp)
			qrData, err = vcard.VCardToQR(vcardData, 256)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(qrData)

	default:
		// Return raw EPM FlatBuffer
		if len(tp.EPMData) == 0 {
			http.Error(w, "no EPM data for this peer", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-flatbuffers")
		w.Write(tp.EPMData)
	}
}

// handleGroups handles GET /api/groups and POST /api/groups
func (h *APIHandler) handleGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		groups := h.registry.ListGroups()
		writeJSON(w, groups)
	case "POST":
		var group PeerGroup
		if err := readJSON(r, &group); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := h.registry.AddGroup(&group); err != nil {
			if err == ErrGroupAlreadyExists {
				http.Error(w, err.Error(), http.StatusConflict)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, group)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGroupByName handles /api/groups/:name endpoints
func (h *APIHandler) handleGroupByName(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/groups/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Group name required", http.StatusBadRequest)
		return
	}

	groupName := parts[0]

	// Check for member management
	if len(parts) > 1 && parts[1] == "members" {
		h.handleGroupMembers(w, r, groupName, parts[2:])
		return
	}

	switch r.Method {
	case "GET":
		group, err := h.registry.GetGroup(groupName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, group)
	case "DELETE":
		if err := h.registry.RemoveGroup(groupName); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGroupMembers handles /api/groups/:name/members endpoints
func (h *APIHandler) handleGroupMembers(w http.ResponseWriter, r *http.Request, groupName string, parts []string) {
	if len(parts) == 0 {
		// GET /api/groups/:name/members - list members
		if r.Method == "GET" {
			peers := h.registry.ListPeersByGroup(groupName)
			writeJSON(w, peers)
			return
		}

		// POST /api/groups/:name/members - add member
		if r.Method == "POST" {
			var req struct {
				PeerID string `json:"peer_id"`
			}
			if err := readJSON(r, &req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			peerID, err := peer.Decode(req.PeerID)
			if err != nil {
				http.Error(w, "Invalid peer ID", http.StatusBadRequest)
				return
			}
			if err := h.registry.AddPeerToGroup(peerID, groupName); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return
		}

		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// DELETE /api/groups/:name/members/:peer_id - remove member
	if r.Method == "DELETE" {
		peerID, err := peer.Decode(parts[0])
		if err != nil {
			http.Error(w, "Invalid peer ID", http.StatusBadRequest)
			return
		}
		if err := h.registry.RemovePeerFromGroup(peerID, groupName); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// handleBlocklist handles blocklist endpoints
func (h *APIHandler) handleBlocklist(w http.ResponseWriter, r *http.Request) {
	if h.gater == nil {
		http.Error(w, "Blocklist not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case "GET":
		blocked := h.gater.ListBlocked()
		blockedStrs := make([]string, len(blocked))
		for i, id := range blocked {
			blockedStrs[i] = id.String()
		}
		writeJSON(w, blockedStrs)
	case "POST":
		var req struct {
			PeerID string `json:"peer_id"`
		}
		if err := readJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		peerID, err := peer.Decode(req.PeerID)
		if err != nil {
			http.Error(w, "Invalid peer ID", http.StatusBadRequest)
			return
		}
		h.gater.Block(peerID)
		w.WriteHeader(http.StatusCreated)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleBlocklistByID handles blocklist endpoints for specific peer
func (h *APIHandler) handleBlocklistByID(w http.ResponseWriter, r *http.Request) {
	if h.gater == nil {
		http.Error(w, "Blocklist not available", http.StatusServiceUnavailable)
		return
	}

	peerIDStr := strings.TrimPrefix(r.URL.Path, "/api/blocklist/")
	peerID, err := peer.Decode(peerIDStr)
	if err != nil {
		http.Error(w, "Invalid peer ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "GET":
		blocked := h.gater.IsBlocked(peerID)
		writeJSON(w, map[string]bool{"blocked": blocked})
	case "DELETE":
		h.gater.Unblock(peerID)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// SettingsResponse represents the settings response.
type SettingsResponse struct {
	StrictMode bool `json:"strict_mode"`
	PeerCount  int  `json:"peer_count"`
	GroupCount int  `json:"group_count"`
}

// handleSettings handles settings endpoints
func (h *APIHandler) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		settings := SettingsResponse{
			StrictMode: h.registry.IsStrictMode(),
			PeerCount:  h.registry.PeerCount(),
			GroupCount: h.registry.GroupCount(),
		}
		writeJSON(w, settings)
	case "PUT":
		var req struct {
			StrictMode *bool `json:"strict_mode,omitempty"`
		}
		if err := readJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.StrictMode != nil {
			h.registry.SetStrictMode(*req.StrictMode)
		}
		settings := SettingsResponse{
			StrictMode: h.registry.IsStrictMode(),
			PeerCount:  h.registry.PeerCount(),
			GroupCount: h.registry.GroupCount(),
		}
		writeJSON(w, settings)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleExport handles GET /api/export
func (h *APIHandler) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	data, err := h.registry.Export()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=peers.json")
	w.Write(data)
}

// handleImport handles POST /api/import
func (h *APIHandler) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	merge := r.URL.Query().Get("merge") == "true"

	if err := h.registry.Import(data, merge); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	settings := SettingsResponse{
		StrictMode: h.registry.IsStrictMode(),
		PeerCount:  h.registry.PeerCount(),
		GroupCount: h.registry.GroupCount(),
	}
	writeJSON(w, settings)
}

// handleVCardImport handles POST /api/peers/import/vcard
func (h *APIHandler) handleVCardImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	vcardStr := string(data)

	// Try multi-vCard parse first
	infos, err := ParseVCards(vcardStr)
	if err != nil {
		http.Error(w, "Invalid vCard data: "+err.Error(), http.StatusBadRequest)
		return
	}

	var imported []*TrustedPeer
	var errors []string

	// Contact data is only attributed when attribution is unambiguous. A
	// single-card payload IS that peer's card, so it is stored verbatim and
	// its derivable EPM record with it. A multi-card payload describes several
	// distinct peers; storing the whole payload (or the first card's EPM) on
	// each one would attach contact data to identities it does not belong to,
	// so those imports carry peer fields only.
	singleCard := len(infos) == 1
	var cardData string
	var cardEPM []byte
	if singleCard {
		cardData = strings.TrimSpace(vcardStr)
		cardEPM = epmFromVCard(vcardStr)
	}

	for _, info := range infos {
		trustLevel := Standard
		if tlStr, ok := info.Metadata["trust_level"]; ok {
			if tl, parseErr := ParseTrustLevel(tlStr); parseErr == nil {
				trustLevel = tl
			}
		}

		tp := &TrustedPeer{
			ID:           info.PeerID,
			Addrs:        info.Addrs,
			TrustLevel:   trustLevel,
			Name:         info.Name,
			Organization: info.Organization,
			Notes:        info.Notes,
			VCardData:    cardData,
			EPMData:      cardEPM,
		}

		if addErr := h.registry.AddPeer(tp); addErr != nil {
			errors = append(errors, info.PeerID.ShortString()+": "+addErr.Error())
		} else {
			imported = append(imported, tp)
		}
	}

	result := struct {
		Imported int      `json:"imported"`
		Errors   []string `json:"errors,omitempty"`
	}{
		Imported: len(imported),
		Errors:   errors,
	}

	if len(imported) > 0 {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusBadRequest)
	}
	writeJSON(w, result)
}

// handleVCardExport handles GET /api/peers/export/vcard/:id
func (h *APIHandler) handleVCardExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	peerIDStr := strings.TrimPrefix(r.URL.Path, "/api/peers/export/vcard/")
	if peerIDStr == "" {
		http.Error(w, "Peer ID required", http.StatusBadRequest)
		return
	}

	peerID, err := peer.Decode(peerIDStr)
	if err != nil {
		http.Error(w, "Invalid peer ID", http.StatusBadRequest)
		return
	}

	tp, err := h.registry.GetPeer(peerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	vcardData := TrustedPeerToVCard(tp)
	w.Header().Set("Content-Type", "text/vcard")
	w.Header().Set("Content-Disposition", "attachment; filename=peer-"+peerID.ShortString()+".vcf")
	w.Write([]byte(vcardData))
}

// Helper functions

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}
