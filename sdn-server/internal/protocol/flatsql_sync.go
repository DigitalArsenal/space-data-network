package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const FlatSQLSyncProtocolID = datasync.ProtocolID

// FlatSQLSyncHandler serves chunked, resumable FlatSQL raw-record streams over
// any libp2p stream transport, including WebSocket and WebRTC.
type FlatSQLSyncHandler struct {
	store *storage.FlatSQLStore
}

type flatSQLSyncRequest struct {
	Op                     string               `json:"op"`
	Schema                 string               `json:"schema"`
	SchemaName             string               `json:"schema_name"`
	ProviderID             string               `json:"provider_id"`
	ProviderId             string               `json:"providerId"`
	SourceName             string               `json:"source_name"`
	BatchID                string               `json:"batch_id"`
	BatchId                string               `json:"batchId"`
	ProducerPeerID         string               `json:"producer_peer_id"`
	ProducerPeerId         string               `json:"producerPeerId"`
	ProducerPublicKey      string               `json:"producer_public_key"`
	ProducerPublicKeyCamel string               `json:"producerPublicKey"`
	PeerID                 string               `json:"peer_id"`
	PeerId                 string               `json:"peerId"`
	Cursor                 string               `json:"cursor"`
	SnapshotID             string               `json:"snapshot_id"`
	Head                   string               `json:"head"`
	QueryProfile           string               `json:"query_profile"`
	Limit                  int                  `json:"limit"`
	Offset                 int                  `json:"offset"`
	ScanHash               string               `json:"scan_hash"`
	ChunkHash              string               `json:"chunk_hash"`
	NextCursor             string               `json:"next_cursor"`
	TotalCount             int64                `json:"total_count"`
	HighWaterMark          string               `json:"high_water_mark"`
	Records                []datasync.RecordRef `json:"records"`
	LocalRows              int64                `json:"local_rows"`
	CachedBytes            int64                `json:"cached_bytes"`
	PinnedBytes            int64                `json:"pinned_bytes"`
	VerifiedChunks         []string             `json:"verified_chunks"`
}

// NewFlatSQLSyncHandler creates a FlatSQL sync stream handler.
func NewFlatSQLSyncHandler(store *storage.FlatSQLStore) *FlatSQLSyncHandler {
	return &FlatSQLSyncHandler{store: store}
}

// HandleStream handles one FlatSQL sync request. The record data plane is a
// native FlatSQL little-endian size-prefixed FlatBuffer stream after the
// response header for read_chunk.
func (h *FlatSQLSyncHandler) HandleStream(s network.Stream) {
	defer s.Close()

	var req flatSQLSyncRequest
	if err := readFlatSQLSyncJSONFrame(s, datasync.StreamRequestMaxBytes, &req); err != nil {
		_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		return
	}

	switch strings.TrimSpace(req.Op) {
	case "", "read_chunk":
		if err := h.handleReadChunk(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "scan", "open_snapshot":
		if err := h.handleScan(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "open_manifest":
		if err := h.handleOpenManifest(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "ack_progress":
		if err := h.handleAckProgress(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	default:
		_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(fmt.Errorf("unsupported sync op %q", req.Op)))
	}
}

func (h *FlatSQLSyncHandler) handleReadChunk(writer io.Writer, req flatSQLSyncRequest) error {
	if h.store == nil {
		return fmt.Errorf("FlatSQL store is unavailable")
	}
	if len(req.Records) > 0 {
		streamReq := req.streamRequest()
		chunkHash, records, err := datasync.ResolveStreamRecords(h.store, streamReq)
		if err != nil {
			return err
		}
		response := map[string]interface{}{
			"schema":          datasync.NormalizeSchema(streamReq.QueryRequest),
			"count":           len(records),
			"scan_hash":       chunkHash,
			"chunk_hash":      chunkHash,
			"snapshot_id":     req.SnapshotID,
			"head":            req.Head,
			"cursor":          req.Cursor,
			"next_cursor":     req.NextCursor,
			"total_count":     req.TotalCount,
			"high_water_mark": req.HighWaterMark,
			"query_profile":   datasync.NormalizeQueryProfile(req.QueryProfile),
			"sync_protocol":   FlatSQLSyncProtocolID,
			"max_chunk_size":  datasync.MaxSyncChunkLimit,
			"transports":      datasync.SupportedTransports,
		}
		if err := writeFlatSQLSyncJSONFrame(writer, response); err != nil {
			return err
		}
		return h.store.WriteRawRecordFrames(writer, records)
	}

	response, records, err := datasync.Scan(h.store, req.queryRequest(), datasync.MaxSyncChunkLimit)
	if err != nil {
		return err
	}
	if err := writeFlatSQLSyncJSONFrame(writer, response); err != nil {
		return err
	}
	return h.store.WriteRawRecordFrames(writer, records)
}

func (h *FlatSQLSyncHandler) handleScan(writer io.Writer, req flatSQLSyncRequest) error {
	if h.store == nil {
		return fmt.Errorf("FlatSQL store is unavailable")
	}
	response, _, err := datasync.Scan(h.store, req.queryRequest(), datasync.MaxSyncChunkLimit)
	if err != nil {
		return err
	}
	return writeFlatSQLSyncJSONFrame(writer, response)
}

func (h *FlatSQLSyncHandler) handleOpenManifest(writer io.Writer, req flatSQLSyncRequest) error {
	if h.store == nil {
		return fmt.Errorf("FlatSQL store is unavailable")
	}
	response, err := datasync.OpenManifest(h.store, req.queryRequest(), datasync.MaxSyncChunkLimit)
	if err != nil {
		return err
	}
	return writeFlatSQLSyncJSONFrame(writer, response)
}

func (h *FlatSQLSyncHandler) handleAckProgress(writer io.Writer, req flatSQLSyncRequest) error {
	return writeFlatSQLSyncJSONFrame(writer, map[string]interface{}{
		"status":          "acknowledged",
		"sync_protocol":   FlatSQLSyncProtocolID,
		"schema":          strings.TrimSpace(firstNonEmptyProtocolString(req.Schema, req.SchemaName)),
		"snapshot_id":     req.SnapshotID,
		"head":            req.Head,
		"next_cursor":     req.NextCursor,
		"chunk_hash":      req.ChunkHash,
		"local_rows":      req.LocalRows,
		"cached_bytes":    req.CachedBytes,
		"pinned_bytes":    req.PinnedBytes,
		"verified_chunks": append([]string(nil), req.VerifiedChunks...),
	})
}

func (req flatSQLSyncRequest) queryRequest() datasync.QueryRequest {
	return datasync.QueryRequest{
		Op:                     req.Op,
		Schema:                 req.Schema,
		SchemaName:             req.SchemaName,
		ProviderID:             req.ProviderID,
		ProviderId:             req.ProviderId,
		SourceName:             req.SourceName,
		BatchID:                req.BatchID,
		BatchId:                req.BatchId,
		ProducerPeerID:         req.ProducerPeerID,
		ProducerPeerId:         req.ProducerPeerId,
		ProducerPublicKey:      req.ProducerPublicKey,
		ProducerPublicKeyCamel: req.ProducerPublicKeyCamel,
		PeerID:                 req.PeerID,
		PeerId:                 req.PeerId,
		Cursor:                 req.Cursor,
		SnapshotID:             req.SnapshotID,
		Head:                   req.Head,
		HighWaterMark:          req.HighWaterMark,
		QueryProfile:           req.QueryProfile,
		TotalCount:             req.TotalCount,
		Limit:                  req.Limit,
		Offset:                 req.Offset,
	}
}

func (req flatSQLSyncRequest) streamRequest() datasync.StreamRequest {
	return datasync.StreamRequest{
		QueryRequest:  req.queryRequest(),
		ScanHash:      req.ScanHash,
		ChunkHash:     req.ChunkHash,
		NextCursor:    req.NextCursor,
		TotalCount:    req.TotalCount,
		HighWaterMark: req.HighWaterMark,
		Records:       append([]datasync.RecordRef(nil), req.Records...),
	}
}

func readFlatSQLSyncJSONFrame(reader io.Reader, maxBytes int, target interface{}) error {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return fmt.Errorf("read sync frame header: %w", err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return fmt.Errorf("empty sync frame")
	}
	if maxBytes > 0 && int64(length) > int64(maxBytes) {
		return fmt.Errorf("sync frame exceeds %d bytes", maxBytes)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return fmt.Errorf("read sync frame payload: %w", err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode sync frame: %w", err)
	}
	return nil
}

func writeFlatSQLSyncJSONFrame(writer io.Writer, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > int(^uint32(0)) {
		return fmt.Errorf("sync frame exceeds uint32 length")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func flatSQLSyncErrorResponse(err error) map[string]interface{} {
	return map[string]interface{}{
		"status":        "error",
		"sync_protocol": FlatSQLSyncProtocolID,
		"error": map[string]interface{}{
			"message": err.Error(),
		},
	}
}

func firstNonEmptyProtocolString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
