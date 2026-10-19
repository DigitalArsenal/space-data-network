package api

// The dashboard uses the node's existing peer connection when a provider has
// no browser transport. This bounded connector forwards one read-only
// flatsql-sync 2.0.0 request and relays the peer's reply bytes verbatim; the
// host does not project or store records.
//
//	POST /api/v1/data/remote/{peerID}/{op}   op: read-chunk | list-published | read-shard
//	  request:  application/vnd.sdn.flatbuffers.stream, one $QRP{KIND=Request} frame (<= 64 KiB)
//	  response: application/vnd.sdn.flatbuffers.stream, the peer's reply as sent
//	  errors:   one $QRP{KIND=Error} frame
import (
	"context"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const (
	remoteRecordsMaxResponse = 8 << 20
	remoteRequestMaxBytes    = 64 << 10
)

var remoteRecordSlots = make(chan struct{}, 8)

func (h *CoreAPIHandler) registerRemoteRecordRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/data/remote/{peerID}/{op}", h.withReadRL(h.handleRemoteRecords))
}

// remoteRequestBounded reports whether one request of op stays inside the
// relay's bounds: small record pages, bounded publication listings and
// bounded shard ranges only.
func remoteRequestBounded(op string, body []byte) bool {
	frames, err := SplitFrames(body)
	if err != nil || len(frames) != 1 {
		return false
	}
	q, err := ParseQRP(frames[0])
	if err != nil || int8(q.KIND()) != QRPKindRequest {
		return false
	}
	switch op {
	case syncwire.OpReadChunk:
		return q.LIMIT() >= 1 && q.LIMIT() <= 1000
	case syncwire.OpListPublished:
		offset := 0
		if cursor := string(q.CURSOR()); cursor != "" {
			if offset, err = strconv.Atoi(cursor); err != nil {
				return false
			}
		}
		return q.LIMIT() >= 1 && q.LIMIT() <= 1000 && offset >= 0 && offset <= 16000
	case syncwire.OpReadShard:
		offset := int64(0)
		if cursor := string(q.CURSOR()); cursor != "" {
			if offset, err = strconv.ParseInt(cursor, 10, 64); err != nil {
				return false
			}
		}
		cid := len(q.CID())
		return cid >= 10 && cid <= 128 && offset >= 0 && q.MAX_BYTES() >= 1 && q.MAX_BYTES() <= 4<<20
	}
	return false
}

func (h *CoreAPIHandler) handleRemoteRecords(w http.ResponseWriter, r *http.Request) {
	target, err := peer.Decode(r.PathValue("peerID"))
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "invalid_peer_id", "Invalid node ID", 0)
		return
	}
	if h.h2pHost == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "peer_connections_unavailable", "Peer connections are unavailable", 0)
		return
	}
	// Addresses come only from this node's peerstore, never from browser input.
	if target == h.h2pHost.ID() || (len(h.h2pHost.Peerstore().Addrs(target)) == 0 && len(h.h2pHost.Network().ConnsToPeer(target)) == 0) {
		WriteErrorFrame(w, http.StatusNotFound, "peer_unknown", "Remote node is not known to this node", 0)
		return
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != StreamContentType {
		WriteErrorFrame(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "the relay takes one $QRP request frame as "+StreamContentType, 0)
		return
	}
	op := r.PathValue("op")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, remoteRequestMaxBytes))
	if err != nil || !remoteRequestBounded(op, body) {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "Only bounded record pages, publication listings and published shard reads are allowed", 0)
		return
	}
	select {
	case remoteRecordSlots <- struct{}{}:
		defer func() { <-remoteRecordSlots }()
	default:
		WriteErrorFrame(w, http.StatusTooManyRequests, "busy", "Remote page capacity is busy", time.Second)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	stream, err := h.h2pHost.NewStream(ctx, target, libp2pprotocol.ID(syncwire.ProtocolID(op)))
	if err != nil {
		log.Warnf("Remote records channel to %s: %v", target, err)
		WriteErrorFrame(w, http.StatusBadGateway, "peer_unreachable", "Could not connect to the remote node", 0)
		return
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	_ = stream.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err = stream.Write(body); err != nil {
		WriteErrorFrame(w, http.StatusBadGateway, "peer_request_failed", "Remote request failed", 0)
		return
	}
	_ = stream.CloseWrite()
	response, err := io.ReadAll(io.LimitReader(stream, remoteRecordsMaxResponse+1))
	if err != nil || len(response) > remoteRecordsMaxResponse {
		_ = stream.Reset()
		WriteErrorFrame(w, http.StatusBadGateway, "peer_reply_failed", "Remote page failed or exceeded the byte limit", 0)
		return
	}
	w.Header().Set("Content-Type", StreamContentType)
	w.Header().Set(StreamFormatHeader, StreamFormat)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-SDN-Remote-Peer", target.String())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
}
