package api

// The dashboard uses the node's existing peer connection when a provider has
// no browser transport. This bounded connector forwards a read-only protocol
// frame and opaque response bytes; the host does not project or store records.
import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

const remoteRecordsMaxResponse = 8 << 20

var remoteRecordSlots = make(chan struct{}, 8)

func (h *CoreAPIHandler) handleRemoteRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	target, err := peer.Decode(r.PathValue("peerID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid node ID")
		return
	}
	if h.h2pHost == nil {
		writeError(w, http.StatusServiceUnavailable, "Peer connections are unavailable")
		return
	}
	// Addresses come only from this node's peerstore, never from browser input.
	if target == h.h2pHost.ID() || (len(h.h2pHost.Peerstore().Addrs(target)) == 0 && len(h.h2pHost.Network().ConnsToPeer(target)) == 0) {
		writeError(w, http.StatusNotFound, "Remote node is not known to this node")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || len(body) < 4 || int(binary.BigEndian.Uint32(body[:4])) != len(body)-4 {
		writeError(w, http.StatusBadRequest, "Expected one bounded protocol request")
		return
	}
	var request struct {
		Op    string `json:"op"`
		Limit int    `json:"limit"`
	}
	if json.Unmarshal(body[4:], &request) != nil || request.Op != "read_chunk" || request.Limit < 1 || request.Limit > 1000 {
		writeError(w, http.StatusBadRequest, "Only record pages of 1 to 1000 rows are allowed")
		return
	}
	select {
	case remoteRecordSlots <- struct{}{}:
		defer func() { <-remoteRecordSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "Remote page capacity is busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	stream, err := h.h2pHost.NewStream(ctx, target, protocol.FlatSQLSyncProtocolID)
	if err != nil {
		log.Warnf("Remote records channel to %s: %v", target, err)
		writeError(w, http.StatusBadGateway, "Could not connect to the remote node")
		return
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	_ = stream.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err = stream.Write(body); err != nil {
		writeError(w, http.StatusBadGateway, "Remote request failed")
		return
	}
	_ = stream.CloseWrite()
	response, err := io.ReadAll(io.LimitReader(stream, remoteRecordsMaxResponse+1))
	if err != nil || len(response) > remoteRecordsMaxResponse {
		_ = stream.Reset()
		writeError(w, http.StatusBadGateway, "Remote page failed or exceeded the byte limit")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-SDN-Remote-Peer", target.String())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
}
