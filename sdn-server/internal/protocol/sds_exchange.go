// Package protocol provides the SDS exchange protocol handlers.
package protocol

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Protocol timeouts
const (
	// DefaultHandlerTimeout is the default timeout for protocol handlers
	DefaultHandlerTimeout = 30 * time.Second
	// DefaultReadTimeout is the timeout for reading from streams
	DefaultReadTimeout = 10 * time.Second
	// DefaultValidationTimeout is the timeout for validation operations
	DefaultValidationTimeout = 5 * time.Second
	// DefaultQueryRecordLimit caps records returned per protocol query response.
	DefaultQueryRecordLimit = 100
	// DefaultQueryResponseMaxBytes caps total serialized payload bytes for protocol queries.
	DefaultQueryResponseMaxBytes = 2 * 1024 * 1024
)

var log = logging.Logger("sds-protocol")

// Protocol IDs
const (
	SDSProtocolID     = "/spacedatanetwork/sds-exchange/1.0.0"
	IDExchangeProtoID = "/space-data-network/id-exchange/1.0.0"
	ChatProtoID       = "/space-data-network/chat/1.0.0"
)

// Message types
const (
	MsgRequestData byte = 0x01
	MsgPushData    byte = 0x02
	MsgQuery       byte = 0x03
	MsgResponse    byte = 0x04
	MsgAck         byte = 0x05
	MsgNack        byte = 0x06
	MsgSyncLog     byte = 0x07 // Request PLG entries since a sequence number
	MsgSyncReply   byte = 0x08 // Response with PLG entries (length-prefixed stream)
)

// Response codes
const (
	RespAccept      byte = 0x01
	RespReject      byte = 0x00
	RespRateLimited byte = 0x02 // Rate limit exceeded
)

// MessageLimits defines size limits for protocol messages.
type MessageLimits struct {
	MaxMessageSize int // Maximum data payload size in bytes
	MaxSchemaName  int // Maximum schema name length
	MaxQuerySize   int // Maximum query string size
}

// DefaultMessageLimits returns sensible default limits.
func DefaultMessageLimits() MessageLimits {
	return MessageLimits{
		MaxMessageSize: 10 * 1024 * 1024, // 10MB
		MaxSchemaName:  256,
		MaxQuerySize:   4 * 1024, // 4KB
	}
}

// SyncLogHandler is an optional handler for MsgSyncLog requests.
type SyncLogHandler interface {
	HandleSyncLog(s network.Stream)
}

// PubSubPNMHandler receives validated PNM announcements after local storage.
type PubSubPNMHandler func(ctx context.Context, schema string, data []byte, from peer.ID) error

// SDSExchangeHandler handles the SDS exchange protocol.
type SDSExchangeHandler struct {
	store       *storage.FlatSQLStore
	validator   *sds.Validator
	limits      MessageLimits
	rateLimiter *PeerRateLimiter
	syncHandler SyncLogHandler
	pnmHandler  PubSubPNMHandler

	// droppedNoStore counts protocol operations skipped because this node has
	// no local store (config mode: edge). An edge node still subscribes to
	// every schema pubsub topic and still exposes the SDS exchange stream
	// handler, so record messages and exchange requests reach the store
	// boundary with store == nil; dereferencing it SIGSEGVs. The guards below
	// skip the store op and bump this counter instead of panicking.
	droppedNoStore atomic.Uint64
}

// DroppedNoStore returns the number of protocol operations skipped because
// this node has no local store (edge mode). Exposed for observability and
// tests; safe to call concurrently.
func (h *SDSExchangeHandler) DroppedNoStore() uint64 {
	return h.droppedNoStore.Load()
}

// ErrRateLimited is returned when a peer exceeds the rate limit.
var ErrRateLimited = errors.New("rate limit exceeded")

// NewSDSExchangeHandler creates a new SDS exchange handler.
func NewSDSExchangeHandler(store *storage.FlatSQLStore, validator *sds.Validator) *SDSExchangeHandler {
	return NewSDSExchangeHandlerWithOptions(store, validator, DefaultMessageLimits(), nil)
}

// NewSDSExchangeHandlerWithLimits creates a new SDS exchange handler with custom limits.
func NewSDSExchangeHandlerWithLimits(store *storage.FlatSQLStore, validator *sds.Validator, limits MessageLimits) *SDSExchangeHandler {
	return NewSDSExchangeHandlerWithOptions(store, validator, limits, nil)
}

// NewSDSExchangeHandlerWithOptions creates a new SDS exchange handler with all options.
// If rateLimiter is nil, rate limiting will be disabled.
func NewSDSExchangeHandlerWithOptions(store *storage.FlatSQLStore, validator *sds.Validator, limits MessageLimits, rateLimiter *PeerRateLimiter) *SDSExchangeHandler {
	// NOTE: SDS v1 uses transport-authenticated streams and no detached payload signatures.
	log.Infof("SDS message auth mode: transport-authenticated streams (no detached payload signatures)")

	if rateLimiter != nil {
		log.Infof("Rate limiting enabled: %.1f msg/s, %d msg/min, burst %d",
			rateLimiter.config.MaxMessagesPerSecond,
			rateLimiter.config.MaxMessagesPerMinute,
			rateLimiter.config.Burst)
	} else {
		log.Warnf("Rate limiting is DISABLED - server may be vulnerable to DoS attacks")
	}

	h := &SDSExchangeHandler{
		store:       store,
		validator:   validator,
		limits:      limits,
		rateLimiter: rateLimiter,
	}

	return h
}

// HandleStream handles an incoming SDS exchange stream.
func (h *SDSExchangeHandler) HandleStream(s network.Stream) {
	defer s.Close()

	// Get peer ID for rate limiting
	peerID := s.Conn().RemotePeer()

	// Check rate limit before processing
	if h.rateLimiter != nil && !h.rateLimiter.Allow(peerID) {
		log.Warnf("Rate limit exceeded for peer %s, rejecting stream", peerID.ShortString())
		s.Write([]byte{RespRateLimited})
		return
	}

	// Create context with timeout for the entire handler
	ctx, cancel := context.WithTimeout(context.Background(), DefaultHandlerTimeout)
	defer cancel()

	// Set stream deadline for read operations
	if err := s.SetReadDeadline(time.Now().Add(DefaultReadTimeout)); err != nil {
		log.Warnf("Failed to set read deadline: %v", err)
	}

	// Read message type
	msgType := make([]byte, 1)
	if _, err := io.ReadFull(s, msgType); err != nil {
		log.Warnf("Failed to read message type: %v", err)
		return
	}

	switch msgType[0] {
	case MsgRequestData:
		h.handleDataRequest(ctx, s)
	case MsgPushData:
		h.handleDataPush(ctx, s)
	case MsgQuery:
		h.handleQuery(ctx, s)
	case MsgSyncLog:
		if h.syncHandler != nil {
			h.syncHandler.HandleSyncLog(s)
		} else {
			log.Warnf("MsgSyncLog received but no sync handler registered")
			s.Write([]byte{RespReject})
		}
	default:
		log.Warnf("Unknown message type: 0x%02x", msgType[0])
		s.Write([]byte{RespReject})
	}
}

func (h *SDSExchangeHandler) handleDataRequest(ctx context.Context, s network.Stream) {
	// Read schema name length (2 bytes)
	schemaNameLen := make([]byte, 2)
	if _, err := io.ReadFull(s, schemaNameLen); err != nil {
		log.Warnf("Failed to read schema name length: %v", err)
		return
	}

	// Validate schema name length
	schemaLen := binary.BigEndian.Uint16(schemaNameLen)
	if int(schemaLen) > h.limits.MaxSchemaName {
		log.Warnf("Schema name too long: %d > %d", schemaLen, h.limits.MaxSchemaName)
		s.Write([]byte{RespReject})
		return
	}

	// Read schema name
	schemaName := make([]byte, schemaLen)
	if _, err := io.ReadFull(s, schemaName); err != nil {
		log.Warnf("Failed to read schema name: %v", err)
		return
	}

	// Validate schema name to prevent path traversal and injection attacks
	if err := sds.ValidateSchemaName(string(schemaName)); err != nil {
		log.Warnf("Invalid schema name from %s: %v", s.Conn().RemotePeer().ShortString(), err)
		s.Write([]byte{RespReject})
		return
	}

	// Read CID length (2 bytes)
	cidLen := make([]byte, 2)
	if _, err := io.ReadFull(s, cidLen); err != nil {
		log.Warnf("Failed to read CID length: %v", err)
		return
	}

	// Read CID
	cid := make([]byte, binary.BigEndian.Uint16(cidLen))
	if _, err := io.ReadFull(s, cid); err != nil {
		log.Warnf("Failed to read CID: %v", err)
		return
	}

	// Edge-mode fail-safe: no local store means nothing to serve. Reject
	// without dereferencing the nil store (would SIGSEGV).
	if h.store == nil {
		n := h.droppedNoStore.Add(1)
		log.Debugf("edge mode (no store): rejecting data request for %s from %s (dropped=%d)", schemaName, s.Conn().RemotePeer().ShortString(), n)
		s.Write([]byte{RespReject})
		return
	}

	// Lookup data
	data, err := h.store.Get(string(schemaName), string(cid))
	if err != nil {
		log.Debugf("Data not found: %s/%s", schemaName, cid)
		s.Write([]byte{RespReject})
		return
	}

	// Send response
	s.Write([]byte{RespAccept})

	// Send data length (4 bytes)
	dataLen := make([]byte, 4)
	binary.BigEndian.PutUint32(dataLen, uint32(len(data)))
	s.Write(dataLen)

	// Send data
	s.Write(data)

	log.Debugf("Sent %d bytes for %s/%s", len(data), schemaName, cid)
}

func (h *SDSExchangeHandler) handleDataPush(ctx context.Context, s network.Stream) {
	// Read schema name length (2 bytes)
	schemaNameLen := make([]byte, 2)
	if _, err := io.ReadFull(s, schemaNameLen); err != nil {
		log.Warnf("Failed to read schema name length: %v", err)
		s.Write([]byte{RespReject})
		return
	}

	// Validate schema name length
	schemaLen := binary.BigEndian.Uint16(schemaNameLen)
	if int(schemaLen) > h.limits.MaxSchemaName {
		log.Warnf("Schema name too long: %d > %d", schemaLen, h.limits.MaxSchemaName)
		s.Write([]byte{RespReject})
		return
	}

	// Read schema name
	schemaName := make([]byte, schemaLen)
	if _, err := io.ReadFull(s, schemaName); err != nil {
		log.Warnf("Failed to read schema name: %v", err)
		s.Write([]byte{RespReject})
		return
	}

	// Validate schema name to prevent path traversal and injection attacks
	if err := sds.ValidateSchemaName(string(schemaName)); err != nil {
		log.Warnf("Invalid schema name from %s: %v", s.Conn().RemotePeer().ShortString(), err)
		s.Write([]byte{RespReject})
		return
	}

	// Read data length (4 bytes)
	dataLenBuf := make([]byte, 4)
	if _, err := io.ReadFull(s, dataLenBuf); err != nil {
		log.Warnf("Failed to read data length: %v", err)
		s.Write([]byte{RespReject})
		return
	}

	// Validate data length before allocation
	dataLen := binary.BigEndian.Uint32(dataLenBuf)
	if int(dataLen) > h.limits.MaxMessageSize {
		log.Warnf("Message too large: %d > %d bytes", dataLen, h.limits.MaxMessageSize)
		s.Write([]byte{RespReject})
		return
	}

	// Read data
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(s, data); err != nil {
		log.Warnf("Failed to read data: %v", err)
		s.Write([]byte{RespReject})
		return
	}

	// Get peer ID
	peerID := s.Conn().RemotePeer()

	// Validate data against schema with timeout
	validationCtx, validationCancel := context.WithTimeout(ctx, DefaultValidationTimeout)
	defer validationCancel()

	if err := h.validator.Validate(validationCtx, string(schemaName), data); err != nil {
		log.Warnf("Validation failed for %s from %s: %v", schemaName, peerID, err)
		s.Write([]byte{RespReject})
		return
	}

	// Edge-mode fail-safe: no local store means we cannot accept a push.
	// Reject without dereferencing the nil store (would SIGSEGV).
	if h.store == nil {
		n := h.droppedNoStore.Add(1)
		log.Debugf("edge mode (no store): rejecting data push for %s from %s (dropped=%d)", schemaName, peerID.ShortString(), n)
		s.Write([]byte{RespReject})
		return
	}

	// Store data
	cid, err := h.store.Store(string(schemaName), data, peerID.String(), nil)
	if err != nil {
		log.Warnf("Failed to store data: %v", err)
		s.Write([]byte{RespReject})
		return
	}

	// Send ACK with CID
	s.Write([]byte{RespAccept})
	s.Write([]byte(cid))

	log.Infof("Stored %s record from %s: %s", schemaName, peerID.ShortString(), cid[:16]+"...")
}

func (h *SDSExchangeHandler) handleQuery(ctx context.Context, s network.Stream) {
	// Read schema name length (2 bytes)
	schemaNameLen := make([]byte, 2)
	if _, err := io.ReadFull(s, schemaNameLen); err != nil {
		log.Warnf("Failed to read schema name length: %v", err)
		return
	}

	// Validate schema name length
	schemaLen := binary.BigEndian.Uint16(schemaNameLen)
	if int(schemaLen) > h.limits.MaxSchemaName {
		log.Warnf("Schema name too long: %d > %d", schemaLen, h.limits.MaxSchemaName)
		s.Write([]byte{RespReject})
		return
	}

	// Read schema name
	schemaName := make([]byte, schemaLen)
	if _, err := io.ReadFull(s, schemaName); err != nil {
		log.Warnf("Failed to read schema name: %v", err)
		return
	}

	// Validate schema name to prevent path traversal and injection attacks
	if err := sds.ValidateSchemaName(string(schemaName)); err != nil {
		log.Warnf("Invalid schema name from %s: %v", s.Conn().RemotePeer().ShortString(), err)
		s.Write([]byte{RespReject})
		return
	}

	// Read query length (4 bytes)
	queryLenBuf := make([]byte, 4)
	if _, err := io.ReadFull(s, queryLenBuf); err != nil {
		log.Warnf("Failed to read query length: %v", err)
		return
	}

	// Validate query length before allocation
	queryLen := binary.BigEndian.Uint32(queryLenBuf)
	if int(queryLen) > h.limits.MaxQuerySize {
		log.Warnf("Query too large: %d > %d bytes", queryLen, h.limits.MaxQuerySize)
		s.Write([]byte{RespReject})
		return
	}

	// Read query (ignored — raw SQL queries from peers are not supported for security)
	query := make([]byte, queryLen)
	if _, err := io.ReadFull(s, query); err != nil {
		log.Warnf("Failed to read query: %v", err)
		return
	}

	// Edge-mode fail-safe: no local store means nothing to query. Reject
	// without dereferencing the nil store (would SIGSEGV).
	if h.store == nil {
		n := h.droppedNoStore.Add(1)
		log.Debugf("edge mode (no store): rejecting query for %s from %s (dropped=%d)", schemaName, s.Conn().RemotePeer().ShortString(), n)
		s.Write([]byte{RespReject})
		return
	}

	// Execute safe bounded query — peer-provided SQL is not used to prevent injection.
	// Enforce a strict row/byte budget to avoid response amplification and memory pressure.
	results, err := h.store.QueryAllBounded(string(schemaName), DefaultQueryRecordLimit, DefaultQueryResponseMaxBytes)
	if err != nil {
		log.Warnf("Query failed: %v", err)
		s.Write([]byte{RespReject})
		return
	}

	// Send response
	s.Write([]byte{RespAccept})

	// Send result count (4 bytes)
	countBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(countBuf, uint32(len(results)))
	s.Write(countBuf)

	// Send each result
	for _, data := range results {
		// Send data length (4 bytes)
		dataLen := make([]byte, 4)
		binary.BigEndian.PutUint32(dataLen, uint32(len(data)))
		s.Write(dataLen)

		// Send data
		s.Write(data)
	}

	log.Debugf("Sent %d results for query on %s", len(results), schemaName)
}

// SetSyncHandler registers a handler for MsgSyncLog requests.
func (h *SDSExchangeHandler) SetSyncHandler(handler SyncLogHandler) {
	h.syncHandler = handler
}

// SetPubSubPNMHandler registers an optional handler for validated PNM messages.
func (h *SDSExchangeHandler) SetPubSubPNMHandler(handler PubSubPNMHandler) {
	h.pnmHandler = handler
}

// HandlePubSubMessage processes a message received via PubSub.
func (h *SDSExchangeHandler) HandlePubSubMessage(schema string, data []byte, from peer.ID) error {
	// Check rate limit before processing
	if h.rateLimiter != nil && !h.rateLimiter.Allow(from) {
		log.Warnf("Rate limit exceeded for peer %s, rejecting PubSub message", from.ShortString())
		return ErrRateLimited
	}

	// Validate schema name to prevent path traversal and injection attacks
	if err := sds.ValidateSchemaName(schema); err != nil {
		log.Warnf("PubSub message rejected: invalid schema name from %s: %v", from.ShortString(), err)
		return fmt.Errorf("invalid schema name: %w", err)
	}

	if len(data) == 0 {
		return errors.New("message too short")
	}

	// Validate message size.
	if len(data) > h.limits.MaxMessageSize {
		return fmt.Errorf("message too large: %d > %d bytes", len(data), h.limits.MaxMessageSize)
	}

	// Verify the schema name is in the list of supported schemas
	if !h.validator.HasSchema(schema) {
		log.Warnf("PubSub message rejected: unknown schema %s from %s", schema, from.ShortString())
		return fmt.Errorf("unknown schema: %s", schema)
	}

	// Create context with timeout for PubSub message handling.
	ctx, cancel := context.WithTimeout(context.Background(), DefaultValidationTimeout)
	defer cancel()

	if hasSizePrefixedPNMIdentifier(data) {
		if err := h.validator.Validate(ctx, "PNM.fbs", data); err != nil {
			log.Warnf("PubSub PNM rejected: validation failed from %s on %s: %v", from.ShortString(), schema, err)
			return fmt.Errorf("PNM validation failed: %w", err)
		}
		// Edge-mode fail-safe: a node with no local store (config mode: edge)
		// still subscribes to every schema pubsub topic, so PNM announcements
		// reach here with h.store == nil. Skip the store write (and the
		// downstream materialize handler, which also needs the store), count
		// the drop, and return nil so the caller does not re-log per message.
		if h.store == nil {
			n := h.droppedNoStore.Add(1)
			log.Debugf("edge mode (no store): dropping PNM announcement from %s on %s (dropped=%d)", from.ShortString(), schema, n)
			return nil
		}
		if _, err := h.store.Store("PNM.fbs", data, from.String(), nil); err != nil {
			return fmt.Errorf("failed to store PNM: %w", err)
		}
		if h.pnmHandler != nil {
			if err := h.pnmHandler(ctx, schema, data, from); err != nil {
				return fmt.Errorf("failed to handle PNM announcement: %w", err)
			}
		}
		log.Debugf("PubSub PNM announcement accepted from %s on %s", from.ShortString(), schema)
		return nil
	}

	// SDS v1 message format: [data...]
	msgData := data

	// Validate data against schema
	if err := h.validator.Validate(ctx, schema, msgData); err != nil {
		log.Warnf("PubSub message rejected: validation failed for %s from %s: %v", schema, from.ShortString(), err)
		return fmt.Errorf("validation failed: %w", err)
	}

	// Edge-mode fail-safe: a node with no local store (config mode: edge)
	// still subscribes to every schema pubsub topic, so SDS record messages
	// reach this store boundary with h.store == nil. Dereferencing it in
	// Store()/storeOne() SIGSEGVs — this was the prod host-01 (sdn.spaceaware.io)
	// crash loop (~one panic every 30 min from the remaining external
	// publisher, systemd restart ~15s outage). Skip the store write, count
	// the drop, log at debug (this fires on every publication reaching an edge
	// node — a per-message warn would be noise), and return nil so the caller
	// does not re-log per message.
	if h.store == nil {
		n := h.droppedNoStore.Add(1)
		log.Debugf("edge mode (no store): dropping %s record from %s (dropped=%d)", schema, from.ShortString(), n)
		return nil
	}

	// Store data
	_, err := h.store.Store(schema, msgData, from.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to store: %w", err)
	}

	log.Debugf("PubSub message accepted: %s record from %s", schema, from.ShortString())
	return nil
}

func hasSizePrefixedPNMIdentifier(data []byte) (ok bool) {
	if len(data) < 8 {
		return false
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return PNM.SizePrefixedPNMBufferHasIdentifier(data)
}

// PushData sends data to a remote peer.
func PushData(ctx context.Context, s network.Stream, schemaName string, data []byte) (string, error) {
	// Write message type
	if _, err := s.Write([]byte{MsgPushData}); err != nil {
		return "", fmt.Errorf("failed to write message type: %w", err)
	}

	// Write schema name length and name
	schemaNameLen := make([]byte, 2)
	binary.BigEndian.PutUint16(schemaNameLen, uint16(len(schemaName)))
	s.Write(schemaNameLen)
	s.Write([]byte(schemaName))

	// Write data length and data
	dataLen := make([]byte, 4)
	binary.BigEndian.PutUint32(dataLen, uint32(len(data)))
	s.Write(dataLen)
	s.Write(data)

	// Read response
	resp := make([]byte, 1)
	if _, err := io.ReadFull(s, resp); err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp[0] != RespAccept {
		return "", errors.New("push rejected")
	}

	// Read CID
	cidBuf := make([]byte, 64) // SHA256 hex = 64 bytes
	n, err := s.Read(cidBuf)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("failed to read CID: %w", err)
	}

	return string(cidBuf[:n]), nil
}

// RequestData requests data from a remote peer.
func RequestData(ctx context.Context, s network.Stream, schemaName, cid string) ([]byte, error) {
	// Write message type
	if _, err := s.Write([]byte{MsgRequestData}); err != nil {
		return nil, fmt.Errorf("failed to write message type: %w", err)
	}

	// Write schema name length and name
	schemaNameLen := make([]byte, 2)
	binary.BigEndian.PutUint16(schemaNameLen, uint16(len(schemaName)))
	s.Write(schemaNameLen)
	s.Write([]byte(schemaName))

	// Write CID length and CID
	cidLen := make([]byte, 2)
	binary.BigEndian.PutUint16(cidLen, uint16(len(cid)))
	s.Write(cidLen)
	s.Write([]byte(cid))

	// Read response
	resp := make([]byte, 1)
	if _, err := io.ReadFull(s, resp); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp[0] != RespAccept {
		return nil, errors.New("request rejected")
	}

	// Read data length
	dataLenBuf := make([]byte, 4)
	if _, err := io.ReadFull(s, dataLenBuf); err != nil {
		return nil, fmt.Errorf("failed to read data length: %w", err)
	}

	dataLen := binary.BigEndian.Uint32(dataLenBuf)
	if int(dataLen) > DefaultMessageLimits().MaxMessageSize {
		return nil, fmt.Errorf("response too large: %d bytes", dataLen)
	}

	// Read data
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(s, data); err != nil {
		return nil, fmt.Errorf("failed to read data: %w", err)
	}

	return data, nil
}
