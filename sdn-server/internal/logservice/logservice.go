// Package logservice manages append-only, hash-chained publication logs (PLG)
// and their head announcements (PLH) for the Space Data Network.
//
// Each publisher maintains one log per SDS schema type. When data is published,
// a PLG entry is appended that chains to the previous entry via ENTRY_HASH.
// PLH head announcements are broadcast via GossipSub so subscribers can detect
// when a log has advanced and sync the delta at their convenience.
package logservice

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

var log = logging.Logger("logservice")

const (
	plgSchema = "PLOG.fbs"
	plhSchema = "PLHD.fbs"
)

// TopicPublisher is the interface for publishing to a PubSub topic.
type TopicPublisher interface {
	Publish(schema string, data []byte) error
}

// Service manages publication logs and head announcements.
type Service struct {
	store      *storage.FlatSQLStore
	signingKey crypto.PrivKey
	peerID     string
	mu         sync.Mutex // serializes log appends per (publisher, schema)
}

// NewService creates a new log service.
func NewService(store *storage.FlatSQLStore, signingKey crypto.PrivKey, peerID string) *Service {
	return &Service{
		store:      store,
		signingKey: signingKey,
		peerID:     peerID,
	}
}

// AppendEntry creates and stores a new PLG entry for a published data record.
// It chains to the previous entry, signs the entry hash, stores in FlatSQL,
// and populates the sdn_log_index. Returns the PLG CID and the new sequence number.
func (s *Service) AppendEntry(schemaType, recordCID string, entityIDs []string, epochDay string) (string, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get current head for this (publisher, schema) log
	prevSeq, prevEntryHash, err := s.store.GetLogHead(s.peerID, schemaType)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get log head: %w", err)
	}

	newSeq := prevSeq + 1
	now := uint64(time.Now().Unix())

	// Compute canonical entry hash
	entryHash := ComputeEntryHash(newSeq, schemaType, s.peerID, recordCID, prevEntryHash, now)

	// Sign entry hash
	var sigBytes []byte
	if s.signingKey != nil {
		entryHashBytes, _ := hex.DecodeString(entryHash)
		sigBytes, err = s.signingKey.Sign(entryHashBytes)
		if err != nil {
			return "", 0, fmt.Errorf("failed to sign entry hash: %w", err)
		}
	}

	// Build PLG FlatBuffer
	plgData := buildPLGFlatBuffer(newSeq, schemaType, s.peerID, recordCID, prevEntryHash, entryHash, now, sigBytes, entityIDs, epochDay)

	// Store in sds_plg via FlatSQL
	plgCID, err := s.store.Store(plgSchema, plgData, s.peerID, sigBytes)
	if err != nil {
		return "", 0, fmt.Errorf("failed to store PLG entry: %w", err)
	}

	// Populate sdn_log_index
	if err := s.store.UpsertLogIndex(s.peerID, schemaType, newSeq, entryHash, recordCID, plgCID, epochDay, int64(now)); err != nil {
		log.Warnf("Failed to upsert log index for PLG %s: %v", plgCID[:16]+"...", err)
	}

	log.Debugf("Appended PLG entry seq=%d for %s/%s (CID: %s)", newSeq, s.peerID[:8]+"...", schemaType, plgCID[:16]+"...")
	return plgCID, newSeq, nil
}

// BuildPLH creates a PLH FlatBuffer for the current log head of the given schema type.
func (s *Service) BuildPLH(schemaType, multiaddr string) ([]byte, error) {
	headSeq, headEntryHash, err := s.store.GetLogHead(s.peerID, schemaType)
	if err != nil {
		return nil, fmt.Errorf("failed to get log head: %w", err)
	}
	if headSeq == 0 {
		return nil, fmt.Errorf("no log entries for %s/%s", s.peerID, schemaType)
	}

	recordCount, err := s.store.LogRecordCount(s.peerID, schemaType)
	if err != nil {
		return nil, fmt.Errorf("failed to get record count: %w", err)
	}

	oldest, newest, err := s.store.LogEpochRange(s.peerID, schemaType)
	if err != nil {
		log.Warnf("Failed to get epoch range for PLH: %v", err)
	}

	now := uint64(time.Now().Unix())

	// Sign the PLH content
	plhHash := ComputePLHHash(schemaType, s.peerID, headSeq, headEntryHash, now)
	var sigBytes []byte
	if s.signingKey != nil {
		plhHashBytes, _ := hex.DecodeString(plhHash)
		sigBytes, err = s.signingKey.Sign(plhHashBytes)
		if err != nil {
			log.Warnf("Failed to sign PLH: %v", err)
		}
	}

	data := buildPLHFlatBuffer(schemaType, s.peerID, headSeq, headEntryHash, recordCount, multiaddr, now, sigBytes, oldest, newest)
	return data, nil
}

// PublishHead builds and broadcasts a PLH for the given schema type.
func (s *Service) PublishHead(publisher TopicPublisher, schemaType, multiaddr string) error {
	plhData, err := s.BuildPLH(schemaType, multiaddr)
	if err != nil {
		return err
	}

	// Store PLH locally
	if _, err := s.store.Store(plhSchema, plhData, s.peerID, nil); err != nil {
		log.Warnf("Failed to store PLH locally: %v", err)
	}

	// Broadcast via GossipSub
	if err := publisher.Publish(plhSchema, plhData); err != nil {
		return fmt.Errorf("failed to publish PLH: %w", err)
	}

	log.Infof("Published PLH for %s (schema: %s)", s.peerID[:8]+"...", schemaType)
	return nil
}

// HandleIncomingPLH processes a PLH received from a remote peer.
// It stores the PLH and returns the parsed head info for further processing.
func (s *Service) HandleIncomingPLH(plhData []byte, fromPeer string) (*PLHInfo, error) {
	info, err := ParsePLH(plhData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PLH: %w", err)
	}

	// Verify signature if we have the publisher's public key
	// TODO: look up publisher's signing public key from peer registry / EPM cache
	// For now, store and log.

	// Store the PLH
	if _, err := s.store.Store(plhSchema, plhData, fromPeer, nil); err != nil {
		log.Warnf("Failed to store incoming PLH from %s: %v", fromPeer, err)
	}

	log.Debugf("Received PLH from %s: schema=%s head_seq=%d", fromPeer, info.SchemaType, info.HeadSequence)
	return info, nil
}

// VerifyChain verifies the hash chain and signatures of a sequence of PLG entries.
// Entries must be in ascending sequence order.
func VerifyChain(entries []PLGInfo, publisherPubKey crypto.PubKey) error {
	for i, entry := range entries {
		// Verify entry hash computation
		computed := ComputeEntryHash(entry.Sequence, entry.SchemaType, entry.PublisherPeerID,
			entry.RecordCID, entry.PreviousEntryHash, entry.Timestamp)
		if computed != entry.EntryHash {
			return fmt.Errorf("entry seq=%d: entry hash mismatch (computed=%s, got=%s)",
				entry.Sequence, computed[:16]+"...", entry.EntryHash[:16]+"...")
		}

		// Verify chain link
		if i > 0 {
			if entry.PreviousEntryHash != entries[i-1].EntryHash {
				return fmt.Errorf("entry seq=%d: chain break (previous_entry_hash=%s, expected=%s)",
					entry.Sequence, entry.PreviousEntryHash[:16]+"...", entries[i-1].EntryHash[:16]+"...")
			}
		}

		// Verify signature if public key provided
		if publisherPubKey != nil && len(entry.Signature) > 0 {
			entryHashBytes, _ := hex.DecodeString(entry.EntryHash)
			ok, err := publisherPubKey.Verify(entryHashBytes, entry.Signature)
			if err != nil {
				return fmt.Errorf("entry seq=%d: signature verification error: %w", entry.Sequence, err)
			}
			if !ok {
				return fmt.Errorf("entry seq=%d: invalid signature", entry.Sequence)
			}
		}
	}
	return nil
}

// ComputeEntryHash computes the canonical SHA-256 hash for a PLG entry.
// Hash = SHA-256(SEQUENCE_LE64 || SCHEMA_TYPE_UTF8 || PUBLISHER_PEER_ID_UTF8 ||
//
//	RECORD_CID_UTF8 || PREVIOUS_ENTRY_HASH_UTF8 || TIMESTAMP_LE64)
func ComputeEntryHash(sequence uint64, schemaType, publisherPeerID, recordCID, previousEntryHash string, timestamp uint64) string {
	h := sha256.New()

	seqBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(seqBytes, sequence)
	h.Write(seqBytes)

	h.Write([]byte(schemaType))
	h.Write([]byte(publisherPeerID))
	h.Write([]byte(recordCID))
	h.Write([]byte(previousEntryHash))

	tsBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(tsBytes, timestamp)
	h.Write(tsBytes)

	return hex.EncodeToString(h.Sum(nil))
}

// ComputePLHHash computes the canonical SHA-256 hash for a PLH announcement.
// Hash = SHA-256(SCHEMA_TYPE_UTF8 || PUBLISHER_PEER_ID_UTF8 ||
//
//	HEAD_SEQUENCE_LE64 || HEAD_ENTRY_HASH_UTF8 || TIMESTAMP_LE64)
func ComputePLHHash(schemaType, publisherPeerID string, headSequence uint64, headEntryHash string, timestamp uint64) string {
	h := sha256.New()

	h.Write([]byte(schemaType))
	h.Write([]byte(publisherPeerID))

	seqBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(seqBytes, headSequence)
	h.Write(seqBytes)

	h.Write([]byte(headEntryHash))

	tsBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(tsBytes, timestamp)
	h.Write(tsBytes)

	return hex.EncodeToString(h.Sum(nil))
}

// PLGInfo holds parsed fields from a PLG FlatBuffer.
type PLGInfo struct {
	Sequence          uint64
	SchemaType        string
	PublisherPeerID   string
	RecordCID         string
	PreviousEntryHash string
	EntryHash         string
	Timestamp         uint64
	Signature         []byte
	SignatureType     string
	EntityIDs         []string
	EpochDay          string
}

// PLHInfo holds parsed fields from a PLH FlatBuffer.
type PLHInfo struct {
	SchemaType       string
	PublisherPeerID  string
	HeadSequence     uint64
	HeadEntryHash    string
	RecordCount      uint64
	MultiformatAddr  string
	Timestamp        uint64
	Signature        []byte
	SignatureType    string
	OldestEpochDay   string
	NewestEpochDay   string
}

// ParsePLH extracts PLH fields from raw FlatBuffer data.
// Uses manual FlatBuffer table access since we don't have generated Go bindings yet.
func ParsePLH(data []byte) (*PLHInfo, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("PLH data too short: %d bytes", len(data))
	}

	// For now, return a basic parsed result. Full FlatBuffer parsing
	// will use generated bindings from PLHD.fbs once flatc is run.
	// This placeholder allows the service to compile and the API to function.
	info := &PLHInfo{}

	// Try size-prefixed FlatBuffer (4-byte size prefix)
	// The actual parsing will be done via generated PLH Go bindings.
	// For bootstrap: store raw and parse when bindings are available.
	log.Debugf("ParsePLH: received %d bytes (full parsing requires generated bindings)", len(data))
	return info, nil
}

// buildPLGFlatBuffer constructs a PLG FlatBuffer message.
// Uses manual FlatBuffer builder since we don't have generated Go bindings yet.
func buildPLGFlatBuffer(sequence uint64, schemaType, publisherPeerID, recordCID, previousEntryHash, entryHash string, timestamp uint64, signature []byte, entityIDs []string, epochDay string) []byte {
	builder := flatbuffers.NewBuilder(512)

	// Create strings
	schemaTypeOff := builder.CreateString(schemaType)
	publisherOff := builder.CreateString(publisherPeerID)
	recordCIDOff := builder.CreateString(recordCID)
	prevHashOff := builder.CreateString(previousEntryHash)
	entryHashOff := builder.CreateString(entryHash)
	sigTypeOff := builder.CreateString("Ed25519")
	epochDayOff := builder.CreateString(epochDay)

	// Create signature byte vector
	var sigOff flatbuffers.UOffsetT
	if len(signature) > 0 {
		sigOff = builder.CreateByteVector(signature)
	}

	// Create entity IDs vector
	var entityIDsOff flatbuffers.UOffsetT
	if len(entityIDs) > 0 {
		offsets := make([]flatbuffers.UOffsetT, len(entityIDs))
		for i := len(entityIDs) - 1; i >= 0; i-- {
			offsets[i] = builder.CreateString(entityIDs[i])
		}
		builder.StartVector(4, len(entityIDs), 4)
		for i := len(offsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(offsets[i])
		}
		entityIDsOff = builder.EndVector(len(entityIDs))
	}

	// Build PLG table manually (field order matches schema VTable)
	// PLG has 11 fields: SEQUENCE(0), SCHEMA_TYPE(1), PUBLISHER_PEER_ID(2),
	// RECORD_CID(3), PREVIOUS_ENTRY_HASH(4), ENTRY_HASH(5), TIMESTAMP(6),
	// SIGNATURE(7), SIGNATURE_TYPE(8), ENTITY_IDS(9), EPOCH_DAY(10)
	builder.StartObject(11)
	builder.PrependUint64Slot(0, sequence, 0)           // SEQUENCE
	builder.PrependUOffsetTSlot(1, schemaTypeOff, 0)    // SCHEMA_TYPE
	builder.PrependUOffsetTSlot(2, publisherOff, 0)     // PUBLISHER_PEER_ID
	builder.PrependUOffsetTSlot(3, recordCIDOff, 0)     // RECORD_CID
	builder.PrependUOffsetTSlot(4, prevHashOff, 0)      // PREVIOUS_ENTRY_HASH
	builder.PrependUOffsetTSlot(5, entryHashOff, 0)     // ENTRY_HASH
	builder.PrependUint64Slot(6, timestamp, 0)          // TIMESTAMP
	if len(signature) > 0 {
		builder.PrependUOffsetTSlot(7, sigOff, 0)   // SIGNATURE
	}
	builder.PrependUOffsetTSlot(8, sigTypeOff, 0)       // SIGNATURE_TYPE
	if len(entityIDs) > 0 {
		builder.PrependUOffsetTSlot(9, entityIDsOff, 0) // ENTITY_IDS
	}
	builder.PrependUOffsetTSlot(10, epochDayOff, 0)     // EPOCH_DAY
	plg := builder.EndObject()

	// Finish with size prefix and file identifier
	builder.FinishSizePrefixed(plg)

	out := make([]byte, len(builder.FinishedBytes()))
	copy(out, builder.FinishedBytes())
	return out
}

// buildPLHFlatBuffer constructs a PLH FlatBuffer message.
func buildPLHFlatBuffer(schemaType, publisherPeerID string, headSequence uint64, headEntryHash string, recordCount uint64, multiaddr string, timestamp uint64, signature []byte, oldestEpochDay, newestEpochDay string) []byte {
	builder := flatbuffers.NewBuilder(512)

	schemaTypeOff := builder.CreateString(schemaType)
	publisherOff := builder.CreateString(publisherPeerID)
	headHashOff := builder.CreateString(headEntryHash)
	multiaddrOff := builder.CreateString(multiaddr)
	sigTypeOff := builder.CreateString("Ed25519")
	oldestOff := builder.CreateString(oldestEpochDay)
	newestOff := builder.CreateString(newestEpochDay)

	var sigOff flatbuffers.UOffsetT
	if len(signature) > 0 {
		sigOff = builder.CreateByteVector(signature)
	}

	// PLH has 11 fields: SCHEMA_TYPE(0), PUBLISHER_PEER_ID(1), HEAD_SEQUENCE(2),
	// HEAD_ENTRY_HASH(3), RECORD_COUNT(4), MULTIFORMAT_ADDRESS(5), TIMESTAMP(6),
	// SIGNATURE(7), SIGNATURE_TYPE(8), OLDEST_EPOCH_DAY(9), NEWEST_EPOCH_DAY(10)
	builder.StartObject(11)
	builder.PrependUOffsetTSlot(0, schemaTypeOff, 0)    // SCHEMA_TYPE
	builder.PrependUOffsetTSlot(1, publisherOff, 0)     // PUBLISHER_PEER_ID
	builder.PrependUint64Slot(2, headSequence, 0)       // HEAD_SEQUENCE
	builder.PrependUOffsetTSlot(3, headHashOff, 0)      // HEAD_ENTRY_HASH
	builder.PrependUint64Slot(4, recordCount, 0)        // RECORD_COUNT
	builder.PrependUOffsetTSlot(5, multiaddrOff, 0)     // MULTIFORMAT_ADDRESS
	builder.PrependUint64Slot(6, timestamp, 0)          // TIMESTAMP
	if len(signature) > 0 {
		builder.PrependUOffsetTSlot(7, sigOff, 0)   // SIGNATURE
	}
	builder.PrependUOffsetTSlot(8, sigTypeOff, 0)       // SIGNATURE_TYPE
	builder.PrependUOffsetTSlot(9, oldestOff, 0)        // OLDEST_EPOCH_DAY
	builder.PrependUOffsetTSlot(10, newestOff, 0)       // NEWEST_EPOCH_DAY
	plh := builder.EndObject()

	builder.FinishSizePrefixed(plh)

	out := make([]byte, len(builder.FinishedBytes()))
	copy(out, builder.FinishedBytes())
	return out
}
