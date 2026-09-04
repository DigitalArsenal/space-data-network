package api

// FlatBuffer node directory for the node-first dashboard.
//
//   GET /api/v1/nodes                    all held signed $EPM profiles
//   GET /api/v1/nodes/{peerId}/profile   one held signed $EPM profile

// Presence stays on the $NST status feed; this lane carries identity only.

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	standardsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
)

const (
	NodesPath          = "/api/v1/nodes"
	NodesSchemaName    = "EPM.fbs"
	SelfPeerHeaderName = "X-SDN-Self-Peer"
)

// NodeProfile is one candidate held profile. The handler admits it to the lane
// only when its EPM self-signature verifies and its advertised peer ID matches.
type NodeProfile struct {
	PeerID string
	Frame  []byte
}

// NodeProfileReader is the read-only part of epm.Service used for this lane.
type NodeProfileReader interface {
	GetNodeEPM() []byte
}

// NodesHandlerOptions wires the local profile and the peer/directory copies.
type NodesHandlerOptions struct {
	SelfPeerID string
	Self       NodeProfileReader
	Profiles   func() []NodeProfile
}

// NodesHandler serves the node identity frame lanes.
type NodesHandler struct {
	selfPeerID string
	self       NodeProfileReader
	profiles   func() []NodeProfile
}

// NewNodesHandler constructs the node identity lane.
func NewNodesHandler(options NodesHandlerOptions) *NodesHandler {
	return &NodesHandler{
		selfPeerID: strings.TrimSpace(options.SelfPeerID),
		self:       options.Self,
		profiles:   options.Profiles,
	}
}

// RegisterRoutes mounts the collection and profile routes.
func (h *NodesHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(NodesPath, h.handleList)
	mux.HandleFunc(NodesPath+"/", h.handleProfile)
}

func (h *NodesHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read node profiles.", 0)
		return
	}
	frames, _, ok := h.heldProfiles()
	if !ok {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "profile_unavailable", "This node's signed profile is unavailable.", 5*time.Second)
		return
	}
	WriteFrameStream(w, http.StatusOK, frames, map[string]string{
		StreamSchemaHeader: NodesSchemaName,
		SelfPeerHeaderName: h.selfPeerID,
	})
}

func (h *NodesHandler) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read a node profile.", 0)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.EscapedPath(), NodesPath+"/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "profile" {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "That node profile is not known here.", 0)
		return
	}
	peerID, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(peerID) == "" {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_peer_id", "The node id could not be decoded.", 0)
		return
	}
	_, profiles, selfOK := h.heldProfiles()
	if !selfOK {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "profile_unavailable", "This node's signed profile is unavailable.", 5*time.Second)
		return
	}
	frame, found := profiles[strings.TrimSpace(peerID)]
	if !found {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "That node profile is not known here.", 0)
		return
	}
	WriteFrameStream(w, http.StatusOK, [][]byte{frame}, map[string]string{
		StreamSchemaHeader: NodesSchemaName,
		SelfPeerHeaderName: h.selfPeerID,
	})
}

func (h *NodesHandler) heldProfiles() ([][]byte, map[string][]byte, bool) {
	byPeer := make(map[string][]byte)
	if h == nil || h.self == nil || h.selfPeerID == "" {
		return nil, byPeer, false
	}
	self := h.self.GetNodeEPM()
	if !verifiedNodeEPM(self, h.selfPeerID) {
		return nil, byPeer, false
	}
	byPeer[h.selfPeerID] = append([]byte(nil), self...)

	if h.profiles != nil {
		for _, profile := range h.profiles() {
			peerID := strings.TrimSpace(profile.PeerID)
			if peerID == "" || peerID == h.selfPeerID {
				continue
			}
			if _, exists := byPeer[peerID]; exists || !verifiedNodeEPM(profile.Frame, peerID) {
				continue
			}
			byPeer[peerID] = append([]byte(nil), profile.Frame...)
		}
	}

	peerIDs := make([]string, 0, len(byPeer)-1)
	for peerID := range byPeer {
		if peerID != h.selfPeerID {
			peerIDs = append(peerIDs, peerID)
		}
	}
	sort.Strings(peerIDs)
	frames := make([][]byte, 0, len(byPeer))
	frames = append(frames, byPeer[h.selfPeerID])
	for _, peerID := range peerIDs {
		frames = append(frames, byPeer[peerID])
	}
	return frames, byPeer, true
}

func verifiedNodeEPM(frame []byte, peerID string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	frames, err := SplitFrames(frame)
	if err != nil || len(frames) != 1 || FrameIdentifier(frames[0]) != standardsEPM.EPMIdentifier {
		return false
	}
	if err := epm.VerifyEPMSignature(frame); err != nil {
		return false
	}
	advertised, err := epm.PeerIDFromEPM(frame)
	return err == nil && strings.TrimSpace(advertised) == strings.TrimSpace(peerID)
}
