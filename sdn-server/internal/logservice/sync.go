package logservice

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Sync protocol constants
const (
	// MaxSyncEntries is the maximum number of PLG entries returned in one sync response.
	MaxSyncEntries = 500
)

// SyncHandler handles log synchronization requests over libp2p streams.
type SyncHandler struct {
	store *storage.FlatSQLStore
}

// NewSyncHandler creates a new sync handler.
func NewSyncHandler(store *storage.FlatSQLStore) *SyncHandler {
	return &SyncHandler{store: store}
}

// HandleSyncLog processes a MsgSyncLog request from a remote peer.
// Wire format request:
//
//	schema_name_len(2 BE) + schema_name + publisher_peer_id_len(2 BE) +
//	publisher_peer_id + since_sequence(8 LE) + max_entries(4 LE)
//
// Wire format response:
//
//	resp_code(1) + entry_count(4 BE) + [plg_data_len(4 BE) + plg_data]...
func (h *SyncHandler) HandleSyncLog(s network.Stream) {
	// Read schema name
	schemaNameLen := make([]byte, 2)
	if _, err := io.ReadFull(s, schemaNameLen); err != nil {
		log.Warnf("SyncLog: failed to read schema name length: %v", err)
		s.Write([]byte{0x00}) // reject
		return
	}

	schemaLen := binary.BigEndian.Uint16(schemaNameLen)
	if schemaLen > 256 {
		log.Warnf("SyncLog: schema name too long: %d", schemaLen)
		s.Write([]byte{0x00})
		return
	}

	schemaName := make([]byte, schemaLen)
	if _, err := io.ReadFull(s, schemaName); err != nil {
		log.Warnf("SyncLog: failed to read schema name: %v", err)
		s.Write([]byte{0x00})
		return
	}

	if err := sds.ValidateSchemaName(string(schemaName)); err != nil {
		log.Warnf("SyncLog: invalid schema name: %v", err)
		s.Write([]byte{0x00})
		return
	}

	// Read publisher peer ID
	pubIDLen := make([]byte, 2)
	if _, err := io.ReadFull(s, pubIDLen); err != nil {
		log.Warnf("SyncLog: failed to read publisher ID length: %v", err)
		s.Write([]byte{0x00})
		return
	}

	publisherID := make([]byte, binary.BigEndian.Uint16(pubIDLen))
	if _, err := io.ReadFull(s, publisherID); err != nil {
		log.Warnf("SyncLog: failed to read publisher ID: %v", err)
		s.Write([]byte{0x00})
		return
	}

	// Read since_sequence (8 bytes LE)
	seqBuf := make([]byte, 8)
	if _, err := io.ReadFull(s, seqBuf); err != nil {
		log.Warnf("SyncLog: failed to read since_sequence: %v", err)
		s.Write([]byte{0x00})
		return
	}
	sinceSequence := binary.LittleEndian.Uint64(seqBuf)

	// Read max_entries (4 bytes LE)
	maxBuf := make([]byte, 4)
	if _, err := io.ReadFull(s, maxBuf); err != nil {
		log.Warnf("SyncLog: failed to read max_entries: %v", err)
		s.Write([]byte{0x00})
		return
	}
	maxEntries := int(binary.LittleEndian.Uint32(maxBuf))
	if maxEntries <= 0 || maxEntries > MaxSyncEntries {
		maxEntries = MaxSyncEntries
	}

	// Query PLG entries from the log index
	// Strip .fbs suffix for schema_type (log index stores schema type without suffix)
	schemaType := string(schemaName)

	entries, err := h.store.QueryLogEntries(string(publisherID), schemaType, sinceSequence, maxEntries)
	if err != nil {
		log.Warnf("SyncLog: failed to query log entries: %v", err)
		s.Write([]byte{0x00})
		return
	}

	// Write response
	s.Write([]byte{0x01}) // accept

	// Write entry count (4 bytes BE)
	countBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(countBuf, uint32(len(entries)))
	s.Write(countBuf)

	// Write each entry: length(4 BE) + data
	for _, entry := range entries {
		lenBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBuf, uint32(len(entry)))
		s.Write(lenBuf)
		s.Write(entry)
	}

	log.Debugf("SyncLog: sent %d entries for %s/%s (since_seq=%d)",
		len(entries), publisherID, schemaName, sinceSequence)
}

// RequestSyncLog sends a MsgSyncLog request to a remote peer and returns the PLG entries.
func RequestSyncLog(s network.Stream, schemaType, publisherPeerID string, sinceSequence uint64, maxEntries uint32) ([][]byte, error) {
	// Write message type
	s.Write([]byte{0x07}) // MsgSyncLog

	// Write schema name
	schemaBytes := []byte(schemaType)
	lenBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(lenBuf, uint16(len(schemaBytes)))
	s.Write(lenBuf)
	s.Write(schemaBytes)

	// Write publisher peer ID
	pubBytes := []byte(publisherPeerID)
	binary.BigEndian.PutUint16(lenBuf, uint16(len(pubBytes)))
	s.Write(lenBuf)
	s.Write(pubBytes)

	// Write since_sequence (8 bytes LE)
	seqBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(seqBuf, sinceSequence)
	s.Write(seqBuf)

	// Write max_entries (4 bytes LE)
	maxBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(maxBuf, maxEntries)
	s.Write(maxBuf)

	// Read response code
	respCode := make([]byte, 1)
	if _, err := io.ReadFull(s, respCode); err != nil {
		return nil, fmt.Errorf("failed to read sync response: %w", err)
	}
	if respCode[0] != 0x01 {
		return nil, fmt.Errorf("sync request rejected (code=0x%02x)", respCode[0])
	}

	// Read entry count
	countBuf := make([]byte, 4)
	if _, err := io.ReadFull(s, countBuf); err != nil {
		return nil, fmt.Errorf("failed to read entry count: %w", err)
	}
	entryCount := binary.BigEndian.Uint32(countBuf)

	// Read entries
	entries := make([][]byte, 0, entryCount)
	for i := uint32(0); i < entryCount; i++ {
		entryLenBuf := make([]byte, 4)
		if _, err := io.ReadFull(s, entryLenBuf); err != nil {
			return entries, fmt.Errorf("failed to read entry %d length: %w", i, err)
		}
		entryLen := binary.BigEndian.Uint32(entryLenBuf)
		if entryLen > 10*1024*1024 { // 10MB sanity limit
			return entries, fmt.Errorf("entry %d too large: %d bytes", i, entryLen)
		}

		entryData := make([]byte, entryLen)
		if _, err := io.ReadFull(s, entryData); err != nil {
			return entries, fmt.Errorf("failed to read entry %d data: %w", i, err)
		}
		entries = append(entries, entryData)
	}

	return entries, nil
}
