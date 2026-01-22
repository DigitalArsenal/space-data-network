// Package protocol provides the SDS exchange protocol handlers.
package protocol

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
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
	RespAccept byte = 0x01
	RespReject byte = 0x00
)

// SDSExchangeHandler handles the SDS exchange protocol.
type SDSExchangeHandler struct {
	store     *storage.FlatSQLStore
	validator *sds.Validator
	flatc     *wasm.FlatcModule
}

// NewSDSExchangeHandler creates a new SDS exchange handler.
func NewSDSExchangeHandler(store *storage.FlatSQLStore, validator *sds.Validator, flatc *wasm.FlatcModule) *SDSExchangeHandler {
	return &SDSExchangeHandler{
		store:     store,
		validator: validator,
		flatc:     flatc,
	}
}

// HandleStream handles an incoming SDS exchange stream.
func (h *SDSExchangeHandler) HandleStream(s network.Stream) {
	defer s.Close()

	// Read message type
	msgType := make([]byte, 1)
	if _, err := io.ReadFull(s, msgType); err != nil {
		log.Warnf("Failed to read message type: %v", err)
		return
	}

	ctx := context.Background()

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

	// Read schema name
	schemaName := make([]byte, binary.BigEndian.Uint16(schemaNameLen))
	if _, err := io.ReadFull(s, schemaName); err != nil {
		log.Warnf("Failed to read schema name: %v", err)
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

	// Read schema name
	schemaName := make([]byte, binary.BigEndian.Uint16(schemaNameLen))
	if _, err := io.ReadFull(s, schemaName); err != nil {
		log.Warnf("Failed to read schema name: %v", err)
		s.Write([]byte{RespReject})
		return
	}

	// Read data length (4 bytes)
	dataLen := make([]byte, 4)
	if _, err := io.ReadFull(s, dataLen); err != nil {
		log.Warnf("Failed to read data length: %v", err)
		s.Write([]byte{RespReject})
		return
	}

	// Read data
	data := make([]byte, binary.BigEndian.Uint32(dataLen))
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

	// Validate data against schema
	if err := h.validator.Validate(ctx, string(schemaName), data); err != nil {
		log.Warnf("Validation failed for %s from %s: %v", schemaName, peerID, err)
		s.Write([]byte{RespReject})
		return
	}

	// Verify signature (if WASM is available)
	if h.flatc != nil {
		pubKey := extractPubKeyFromPeerID(peerID)
		if pubKey != nil {
			valid, err := h.flatc.Verify(ctx, pubKey, data, signature)
			if err != nil || !valid {
				log.Warnf("Invalid signature from %s: %v", peerID, err)
				s.Write([]byte{RespReject})
				return
			}
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

	// Read schema name
	schemaName := make([]byte, binary.BigEndian.Uint16(schemaNameLen))
	if _, err := io.ReadFull(s, schemaName); err != nil {
		log.Warnf("Failed to read schema name: %v", err)
		return
	}

	// Read query length (4 bytes)
	queryLen := make([]byte, 4)
	if _, err := io.ReadFull(s, queryLen); err != nil {
		log.Warnf("Failed to read query length: %v", err)
		return
	}

	// Read query
	query := make([]byte, binary.BigEndian.Uint32(queryLen))
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
	if len(data) < 65 {
		return errors.New("message too short")
	}

	// Message format: [data...][signature(64 bytes)]
	msgData := data[:len(data)-64]
	signature := data[len(data)-64:]

	ctx := context.Background()

	// Validate data against schema
	if err := h.validator.Validate(ctx, schema, msgData); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Store data
	_, err := h.store.Store(schema, msgData, from.String(), signature)
	if err != nil {
		return fmt.Errorf("failed to store: %w", err)
	}

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
