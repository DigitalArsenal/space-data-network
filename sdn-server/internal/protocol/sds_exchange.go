// Package protocol provides the SDS exchange protocol handlers.
package protocol

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// Protocol timeouts
const (
	// DefaultHandlerTimeout is the default timeout for protocol handlers
	DefaultHandlerTimeout = 30 * time.Second
	// DefaultReadTimeout is the timeout for reading from streams
	DefaultReadTimeout = 10 * time.Second
	// DefaultValidationTimeout is the timeout for validation operations
	DefaultValidationTimeout = 5 * time.Second
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

// SDSExchangeHandler handles the SDS exchange protocol.
type SDSExchangeHandler struct {
	store        *storage.FlatSQLStore
	validator    *sds.Validator
	flatc        *wasm.FlatcModule
	limits       MessageLimits
	rateLimiter  *PeerRateLimiter
	insecureMode bool // WARNING: Disables mandatory signature verification (development only)
}

// ErrSignatureVerificationUnavailable is returned when signature verification is required
// but the WASM crypto module is not available.
var ErrSignatureVerificationUnavailable = errors.New("signature verification unavailable: WASM crypto module not loaded")

// ErrRateLimited is returned when a peer exceeds the rate limit.
var ErrRateLimited = errors.New("rate limit exceeded")

// ErrInsecureModeActive is logged when insecure mode is enabled.
const insecureModeWarning = "SECURITY WARNING: Insecure mode is enabled. Signature verification is disabled. DO NOT use in production!"

// NewSDSExchangeHandler creates a new SDS exchange handler.
// If flatc is nil and insecureMode is false, all data push operations will be rejected.
func NewSDSExchangeHandler(store *storage.FlatSQLStore, validator *sds.Validator, flatc *wasm.FlatcModule) *SDSExchangeHandler {
	return NewSDSExchangeHandlerWithOptions(store, validator, flatc, DefaultMessageLimits(), false, nil)
}

// NewSDSExchangeHandlerWithLimits creates a new SDS exchange handler with custom limits.
// If flatc is nil and insecureMode is false, all data push operations will be rejected.
func NewSDSExchangeHandlerWithLimits(store *storage.FlatSQLStore, validator *sds.Validator, flatc *wasm.FlatcModule, limits MessageLimits) *SDSExchangeHandler {
	return NewSDSExchangeHandlerWithOptions(store, validator, flatc, limits, false, nil)
}

// NewSDSExchangeHandlerWithOptions creates a new SDS exchange handler with all options.
// If flatc is nil and insecureMode is false, all data push operations will be rejected.
// WARNING: insecureMode should ONLY be used for development and testing.
// If rateLimiter is nil, rate limiting will be disabled.
func NewSDSExchangeHandlerWithOptions(store *storage.FlatSQLStore, validator *sds.Validator, flatc *wasm.FlatcModule, limits MessageLimits, insecureMode bool, rateLimiter *PeerRateLimiter) *SDSExchangeHandler {
	// Log security warnings at initialization
	if flatc == nil {
		if insecureMode {
			log.Warnf(insecureModeWarning)
			log.Warnf("WASM crypto module not available - signature verification is DISABLED")
		} else {
			log.Warnf("SECURITY: WASM crypto module not available - all data push operations will be REJECTED")
			log.Warnf("SECURITY: To enable insecure mode for development, set security.insecure_mode: true in config")
		}
	} else if insecureMode {
		log.Warnf(insecureModeWarning)
		log.Warnf("WASM crypto module is available but insecure mode is enabled - signature verification is DISABLED")
	}

	if rateLimiter != nil {
		log.Infof("Rate limiting enabled: %.1f msg/s, %d msg/min, burst %d",
			rateLimiter.config.MaxMessagesPerSecond,
			rateLimiter.config.MaxMessagesPerMinute,
			rateLimiter.config.Burst)
	} else {
		log.Warnf("Rate limiting is DISABLED - server may be vulnerable to DoS attacks")
	}

	return &SDSExchangeHandler{
		store:        store,
		validator:    validator,
		flatc:        flatc,
		limits:       limits,
		rateLimiter:  rateLimiter,
		insecureMode: insecureMode,
	}
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

	// Read signature (64 bytes for Ed25519)
	signature := make([]byte, 64)
	if _, err := io.ReadFull(s, signature); err != nil {
		log.Warnf("Failed to read signature: %v", err)
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

	// Verify signature - MANDATORY unless insecure mode is enabled
	if h.flatc == nil {
		if h.insecureMode {
			log.Warnf("INSECURE: Accepting data from %s without signature verification (insecure mode)", peerID.ShortString())
		} else {
			log.Errorf("SECURITY: Rejecting data from %s - signature verification unavailable (WASM not loaded)", peerID.ShortString())
			s.Write([]byte{RespReject})
			return
		}
	} else {
		pubKey := extractPubKeyFromPeerID(peerID)
		if pubKey == nil {
			log.Warnf("SECURITY: Could not extract public key from peer %s", peerID.ShortString())
			s.Write([]byte{RespReject})
			return
		}
		valid, err := h.flatc.Verify(ctx, pubKey, data, signature)
		if err != nil || !valid {
			log.Warnf("SECURITY: Invalid signature from %s: %v", peerID, err)
			s.Write([]byte{RespReject})
			return
		}
	}

	// Store data
	cid, err := h.store.Store(string(schemaName), data, peerID.String(), signature)
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

	// Read query
	query := make([]byte, queryLen)
	if _, err := io.ReadFull(s, query); err != nil {
		log.Warnf("Failed to read query: %v", err)
		return
	}

	// Execute query
	results, err := h.store.Query(string(schemaName), string(query))
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

	if len(data) < 65 {
		return errors.New("message too short")
	}

	// Validate message size (including signature)
	if len(data) > h.limits.MaxMessageSize+64 {
		return fmt.Errorf("message too large: %d > %d bytes", len(data), h.limits.MaxMessageSize+64)
	}

	// Verify the schema name is in the list of supported schemas
	if !h.validator.HasSchema(schema) {
		log.Warnf("PubSub message rejected: unknown schema %s from %s", schema, from.ShortString())
		return fmt.Errorf("unknown schema: %s", schema)
	}

	// Message format: [data...][signature(64 bytes)]
	msgData := data[:len(data)-64]
	signature := data[len(data)-64:]

	// Create context with timeout for PubSub message handling
	ctx, cancel := context.WithTimeout(context.Background(), DefaultValidationTimeout)
	defer cancel()

	// Verify signature - MANDATORY unless insecure mode is enabled
	if h.flatc == nil {
		if h.insecureMode {
			log.Warnf("INSECURE: Accepting PubSub message from %s without signature verification (insecure mode)", from.ShortString())
		} else {
			log.Errorf("SECURITY: PubSub message rejected from %s - signature verification unavailable (WASM not loaded)", from.ShortString())
			return ErrSignatureVerificationUnavailable
		}
	} else {
		pubKey := extractPubKeyFromPeerID(from)
		if pubKey == nil {
			log.Warnf("SECURITY: PubSub message rejected - could not extract public key from peer %s", from.ShortString())
			return errors.New("could not extract public key from peer ID")
		}
		valid, err := h.flatc.Verify(ctx, pubKey, msgData, signature)
		if err != nil || !valid {
			log.Warnf("SECURITY: PubSub message rejected - invalid signature from %s: %v", from.ShortString(), err)
			return fmt.Errorf("invalid signature: %w", err)
		}
	}

	// Validate data against schema
	if err := h.validator.Validate(ctx, schema, msgData); err != nil {
		log.Warnf("PubSub message rejected: validation failed for %s from %s: %v", schema, from.ShortString(), err)
		return fmt.Errorf("validation failed: %w", err)
	}

	// Store data
	_, err := h.store.Store(schema, msgData, from.String(), signature)
	if err != nil {
		return fmt.Errorf("failed to store: %w", err)
	}

	log.Debugf("PubSub message accepted: %s record from %s", schema, from.ShortString())
	return nil
}

// extractPubKeyFromPeerID extracts the Ed25519 public key from a peer ID.
func extractPubKeyFromPeerID(peerID peer.ID) []byte {
	pubKey, err := peerID.ExtractPublicKey()
	if err != nil {
		return nil
	}

	raw, err := pubKey.Raw()
	if err != nil {
		return nil
	}

	return raw
}

// PushData sends data to a remote peer.
func PushData(ctx context.Context, s network.Stream, schemaName string, data, signature []byte) (string, error) {
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

	// Write signature
	s.Write(signature)

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

	// Read data
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(s, data); err != nil {
		return nil, fmt.Errorf("failed to read data: %w", err)
	}

	return data, nil
}
