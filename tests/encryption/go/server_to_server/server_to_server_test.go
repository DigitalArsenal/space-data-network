// Package server_to_server provides tests for encrypted communication between SDN servers.
package server_to_server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/tests/encryption/go/ecies"
)

// MockSDNServer represents a simulated SDN server for testing
type MockSDNServer struct {
	ID        string
	KeyPair   *ecies.KeyPair
	Peers     map[string]*ecies.KeyPair // Peer ID -> Public Key
	Messages  []ReceivedMessage
	mu        sync.Mutex
	OnMessage func(from string, msg *EncryptedSDNMessage)
}

// ReceivedMessage represents a message received by a server
type ReceivedMessage struct {
	From      string
	Schema    string
	Data      []byte
	Timestamp time.Time
}

// EncryptedSDNMessage represents an encrypted message in the SDN protocol
type EncryptedSDNMessage struct {
	// Unencrypted routing header
	Header RoutingHeader
	// ECIES encrypted payload
	Payload *ecies.EncryptedMessage
}

// RoutingHeader contains unencrypted routing information
type RoutingHeader struct {
	SchemaType       string   `json:"schema_type"`
	DestinationPeers []string `json:"destination_peers"`
	TTL              uint8    `json:"ttl"`
	Priority         uint8    `json:"priority"`
	Encrypted        bool     `json:"encrypted"`
}

// SDSPayload represents the decrypted message payload
type SDSPayload struct {
	Schema    string          `json:"schema"`
	Data      json.RawMessage `json:"data"`
	Timestamp int64           `json:"timestamp"`
	Signature []byte          `json:"signature,omitempty"`
}

// NewMockSDNServer creates a new mock SDN server
func NewMockSDNServer(id string, curveType ecies.CurveType) (*MockSDNServer, error) {
	kp, err := ecies.GenerateKeyPair(curveType)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key pair: %w", err)
	}

	return &MockSDNServer{
		ID:       id,
		KeyPair:  kp,
		Peers:    make(map[string]*ecies.KeyPair),
		Messages: make([]ReceivedMessage, 0),
	}, nil
}

// RegisterPeer registers a peer's public key
func (s *MockSDNServer) RegisterPeer(peerID string, publicKey []byte, curveType ecies.CurveType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Peers[peerID] = &ecies.KeyPair{
		PublicKey: publicKey,
		CurveType: curveType,
	}
}

// SendEncrypted sends an encrypted message to a peer
func (s *MockSDNServer) SendEncrypted(ctx context.Context, peerID string, schema string, data []byte) (*EncryptedSDNMessage, error) {
	s.mu.Lock()
	peer, ok := s.Peers[peerID]
	s.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("peer %s not registered", peerID)
	}

	// Create payload
	payload := SDSPayload{
		Schema:    schema,
		Data:      data,
		Timestamp: time.Now().UnixNano(),
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Encrypt payload
	encryptedPayload, err := ecies.Encrypt(peer.PublicKey, payloadBytes, peer.CurveType)
	if err != nil {
		return nil, fmt.Errorf("encryption failed: %w", err)
	}

	// Create message with routing header
	msg := &EncryptedSDNMessage{
		Header: RoutingHeader{
			SchemaType:       schema,
			DestinationPeers: []string{peerID},
			TTL:              64,
			Priority:         1,
			Encrypted:        true,
		},
		Payload: encryptedPayload,
	}

	return msg, nil
}

// ReceiveEncrypted receives and decrypts an encrypted message
func (s *MockSDNServer) ReceiveEncrypted(ctx context.Context, from string, msg *EncryptedSDNMessage) (*SDSPayload, error) {
	if !msg.Header.Encrypted {
		return nil, fmt.Errorf("message is not encrypted")
	}

	// Decrypt payload
	decrypted, err := ecies.Decrypt(s.KeyPair.PrivateKey, msg.Payload)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	// Parse payload
	var payload SDSPayload
	if err := json.Unmarshal(decrypted, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	// Record message
	s.mu.Lock()
	s.Messages = append(s.Messages, ReceivedMessage{
		From:      from,
		Schema:    payload.Schema,
		Data:      payload.Data,
		Timestamp: time.Now(),
	})
	if s.OnMessage != nil {
		s.OnMessage(from, msg)
	}
	s.mu.Unlock()

	return &payload, nil
}

// GetMessages returns all received messages
func (s *MockSDNServer) GetMessages() []ReceivedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ReceivedMessage{}, s.Messages...)
}

func TestDirectServerToServerEncryption(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name      string
		curveType ecies.CurveType
	}{
		{"X25519", ecies.CurveX25519},
		{"secp256k1", ecies.CurveSecp256k1},
		{"P-256", ecies.CurveP256},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create two servers
			server1, err := NewMockSDNServer("server-1", tc.curveType)
			if err != nil {
				t.Fatalf("Failed to create server1: %v", err)
			}

			server2, err := NewMockSDNServer("server-2", tc.curveType)
			if err != nil {
				t.Fatalf("Failed to create server2: %v", err)
			}

			// Register each other as peers
			server1.RegisterPeer(server2.ID, server2.KeyPair.PublicKey, tc.curveType)
			server2.RegisterPeer(server1.ID, server1.KeyPair.PublicKey, tc.curveType)

			// Test sending OMM message
			ommData := json.RawMessage(`{
				"OBJECT_NAME": "ISS (ZARYA)",
				"OBJECT_ID": "1998-067A",
				"EPOCH": "2024-01-25T12:00:00.000Z",
				"MEAN_MOTION": 15.72125391,
				"ECCENTRICITY": 0.0006703,
				"INCLINATION": 51.6416
			}`)

			// Server 1 sends to Server 2
			msg, err := server1.SendEncrypted(ctx, server2.ID, "OMM", ommData)
			if err != nil {
				t.Fatalf("SendEncrypted failed: %v", err)
			}

			// Verify routing header is accessible (unencrypted)
			if msg.Header.SchemaType != "OMM" {
				t.Errorf("Header schema type mismatch: got %s, want OMM", msg.Header.SchemaType)
			}
			if !msg.Header.Encrypted {
				t.Error("Header should indicate encrypted payload")
			}

			// Server 2 receives and decrypts
			payload, err := server2.ReceiveEncrypted(ctx, server1.ID, msg)
			if err != nil {
				t.Fatalf("ReceiveEncrypted failed: %v", err)
			}

			if payload.Schema != "OMM" {
				t.Errorf("Payload schema mismatch: got %s, want OMM", payload.Schema)
			}

			// Compare JSON content (formatting may differ)
			var originalJSON, decryptedJSON map[string]interface{}
			json.Unmarshal(ommData, &originalJSON)
			json.Unmarshal(payload.Data, &decryptedJSON)

			originalBytes, _ := json.Marshal(originalJSON)
			decryptedBytes, _ := json.Marshal(decryptedJSON)

			if !bytes.Equal(originalBytes, decryptedBytes) {
				t.Errorf("Decrypted data does not match original:\nOriginal: %s\nDecrypted: %s", originalBytes, decryptedBytes)
			}

			// Verify message was recorded
			messages := server2.GetMessages()
			if len(messages) != 1 {
				t.Errorf("Expected 1 message, got %d", len(messages))
			}
		})
	}
}

func TestBidirectionalServerCommunication(t *testing.T) {
	ctx := context.Background()

	// Create two servers
	server1, err := NewMockSDNServer("server-1", ecies.CurveX25519)
	if err != nil {
		t.Fatalf("Failed to create server1: %v", err)
	}

	server2, err := NewMockSDNServer("server-2", ecies.CurveX25519)
	if err != nil {
		t.Fatalf("Failed to create server2: %v", err)
	}

	// Register each other
	server1.RegisterPeer(server2.ID, server2.KeyPair.PublicKey, ecies.CurveX25519)
	server2.RegisterPeer(server1.ID, server1.KeyPair.PublicKey, ecies.CurveX25519)

	// Server 1 -> Server 2 (CDM)
	cdmData := json.RawMessage(`{
		"MESSAGE_ID": "CDM-001",
		"CREATION_DATE": "2024-01-25T12:00:00.000Z",
		"TCA": "2024-01-26T08:30:00.000Z",
		"MISS_DISTANCE": 500.0
	}`)

	msg1, err := server1.SendEncrypted(ctx, server2.ID, "CDM", cdmData)
	if err != nil {
		t.Fatalf("Server1 -> Server2 failed: %v", err)
	}

	_, err = server2.ReceiveEncrypted(ctx, server1.ID, msg1)
	if err != nil {
		t.Fatalf("Server2 receive failed: %v", err)
	}

	// Server 2 -> Server 1 (EPM)
	epmData := json.RawMessage(`{
		"ENTITY_ID": "ORG-001",
		"ENTITY_TYPE": "OPERATOR",
		"NAME": "Space Corp",
		"COUNTRY": "US"
	}`)

	msg2, err := server2.SendEncrypted(ctx, server1.ID, "EPM", epmData)
	if err != nil {
		t.Fatalf("Server2 -> Server1 failed: %v", err)
	}

	_, err = server1.ReceiveEncrypted(ctx, server2.ID, msg2)
	if err != nil {
		t.Fatalf("Server1 receive failed: %v", err)
	}

	// Verify both servers received messages
	if len(server2.GetMessages()) != 1 {
		t.Error("Server2 should have 1 message")
	}
	if len(server1.GetMessages()) != 1 {
		t.Error("Server1 should have 1 message")
	}
}

func TestPubSubEncryptedBroadcast(t *testing.T) {
	ctx := context.Background()

	// Create publisher and multiple subscribers
	publisher, _ := NewMockSDNServer("publisher", ecies.CurveX25519)
	subscriber1, _ := NewMockSDNServer("subscriber-1", ecies.CurveX25519)
	subscriber2, _ := NewMockSDNServer("subscriber-2", ecies.CurveX25519)
	subscriber3, _ := NewMockSDNServer("subscriber-3", ecies.CurveX25519)

	subscribers := []*MockSDNServer{subscriber1, subscriber2, subscriber3}

	// Register subscribers with publisher
	for _, sub := range subscribers {
		publisher.RegisterPeer(sub.ID, sub.KeyPair.PublicKey, ecies.CurveX25519)
	}

	// Broadcast OMM data to all subscribers
	ommData := json.RawMessage(`{"OBJECT_NAME": "STARLINK-1234"}`)

	// Publisher creates individual encrypted messages for each subscriber
	// (In real implementation, this could use session keys for efficiency)
	for _, sub := range subscribers {
		msg, err := publisher.SendEncrypted(ctx, sub.ID, "OMM", ommData)
		if err != nil {
			t.Fatalf("Broadcast to %s failed: %v", sub.ID, err)
		}

		payload, err := sub.ReceiveEncrypted(ctx, publisher.ID, msg)
		if err != nil {
			t.Fatalf("%s receive failed: %v", sub.ID, err)
		}

		if payload.Schema != "OMM" {
			t.Errorf("%s received wrong schema", sub.ID)
		}
	}

	// Verify all subscribers received the message
	for _, sub := range subscribers {
		if len(sub.GetMessages()) != 1 {
			t.Errorf("%s should have 1 message", sub.ID)
		}
	}
}

func TestRelayMediatedEncryption(t *testing.T) {
	ctx := context.Background()

	// Simulate relay-mediated connection where relay cannot decrypt
	server1, _ := NewMockSDNServer("server-1", ecies.CurveX25519)
	relay, _ := NewMockSDNServer("relay", ecies.CurveX25519)
	server2, _ := NewMockSDNServer("server-2", ecies.CurveX25519)

	// Servers know each other's keys, relay only forwards
	server1.RegisterPeer(server2.ID, server2.KeyPair.PublicKey, ecies.CurveX25519)
	server2.RegisterPeer(server1.ID, server1.KeyPair.PublicKey, ecies.CurveX25519)

	// Server 1 creates message for Server 2
	data := json.RawMessage(`{"test": "relay-mediated"}`)
	msg, err := server1.SendEncrypted(ctx, server2.ID, "OMM", data)
	if err != nil {
		t.Fatalf("Encryption failed: %v", err)
	}

	// Relay can see header but NOT decrypt payload
	if msg.Header.SchemaType != "OMM" {
		t.Error("Relay should be able to read header")
	}

	// Relay cannot decrypt (doesn't have server2's private key)
	_, err = relay.ReceiveEncrypted(ctx, server1.ID, msg)
	if err == nil {
		t.Error("Relay should NOT be able to decrypt message intended for server2")
	}

	// Server 2 can decrypt
	payload, err := server2.ReceiveEncrypted(ctx, server1.ID, msg)
	if err != nil {
		t.Fatalf("Server2 decryption failed: %v", err)
	}

	if payload.Schema != "OMM" {
		t.Error("Server2 received wrong schema")
	}
}

func TestMultipleMessageTypes(t *testing.T) {
	ctx := context.Background()

	server1, _ := NewMockSDNServer("server-1", ecies.CurveX25519)
	server2, _ := NewMockSDNServer("server-2", ecies.CurveX25519)

	server1.RegisterPeer(server2.ID, server2.KeyPair.PublicKey, ecies.CurveX25519)
	server2.RegisterPeer(server1.ID, server1.KeyPair.PublicKey, ecies.CurveX25519)

	testMessages := []struct {
		schema string
		data   json.RawMessage
	}{
		{"OMM", json.RawMessage(`{"OBJECT_NAME": "ISS"}`)},
		{"CDM", json.RawMessage(`{"MESSAGE_ID": "CDM-001", "TCA": "2024-01-26T00:00:00Z"}`)},
		{"EPM", json.RawMessage(`{"ENTITY_ID": "ORG-001", "NAME": "Test Org"}`)},
		{"OEM", json.RawMessage(`{"ORIGINATOR": "NASA", "REF_FRAME": "ICRF"}`)},
		{"TDM", json.RawMessage(`{"PARTICIPANT_1": "SAT-001", "MODE": "RANGE"}`)},
	}

	for _, tm := range testMessages {
		msg, err := server1.SendEncrypted(ctx, server2.ID, tm.schema, tm.data)
		if err != nil {
			t.Fatalf("SendEncrypted %s failed: %v", tm.schema, err)
		}

		if msg.Header.SchemaType != tm.schema {
			t.Errorf("Header schema mismatch for %s", tm.schema)
		}

		payload, err := server2.ReceiveEncrypted(ctx, server1.ID, msg)
		if err != nil {
			t.Fatalf("ReceiveEncrypted %s failed: %v", tm.schema, err)
		}

		if payload.Schema != tm.schema {
			t.Errorf("Payload schema mismatch for %s: got %s", tm.schema, payload.Schema)
		}

		// Compare JSON content (formatting may differ)
		var originalJSON, decryptedJSON map[string]interface{}
		json.Unmarshal(tm.data, &originalJSON)
		json.Unmarshal(payload.Data, &decryptedJSON)

		originalBytes, _ := json.Marshal(originalJSON)
		decryptedBytes, _ := json.Marshal(decryptedJSON)

		if !bytes.Equal(originalBytes, decryptedBytes) {
			t.Errorf("Data mismatch for %s:\nOriginal: %s\nDecrypted: %s", tm.schema, originalBytes, decryptedBytes)
		}
	}

	// Verify all messages received
	messages := server2.GetMessages()
	if len(messages) != len(testMessages) {
		t.Errorf("Expected %d messages, got %d", len(testMessages), len(messages))
	}
}

func TestLargePayload(t *testing.T) {
	ctx := context.Background()

	server1, _ := NewMockSDNServer("server-1", ecies.CurveX25519)
	server2, _ := NewMockSDNServer("server-2", ecies.CurveX25519)

	server1.RegisterPeer(server2.ID, server2.KeyPair.PublicKey, ecies.CurveX25519)

	// Create large ephemeris data (simulate OEM with many data points)
	type EphemerisPoint struct {
		Epoch    string  `json:"epoch"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		Z        float64 `json:"z"`
		VX       float64 `json:"vx"`
		VY       float64 `json:"vy"`
		VZ       float64 `json:"vz"`
	}

	type LargeOEM struct {
		Originator string           `json:"ORIGINATOR"`
		ObjectName string           `json:"OBJECT_NAME"`
		RefFrame   string           `json:"REF_FRAME"`
		DataPoints []EphemerisPoint `json:"data_points"`
	}

	// Generate 10000 ephemeris points (typical for 7-day propagation at 1-minute intervals)
	oem := LargeOEM{
		Originator: "NASA",
		ObjectName: "ISS",
		RefFrame:   "ICRF",
		DataPoints: make([]EphemerisPoint, 10000),
	}

	for i := 0; i < 10000; i++ {
		oem.DataPoints[i] = EphemerisPoint{
			Epoch: fmt.Sprintf("2024-01-25T%02d:%02d:00.000Z", i/60, i%60),
			X:     float64(i) * 1.5,
			Y:     float64(i) * 2.5,
			Z:     float64(i) * 3.5,
			VX:    0.001,
			VY:    0.002,
			VZ:    0.003,
		}
	}

	oemBytes, _ := json.Marshal(oem)
	t.Logf("Large OEM payload size: %d bytes (%.2f MB)", len(oemBytes), float64(len(oemBytes))/1024/1024)

	start := time.Now()
	msg, err := server1.SendEncrypted(ctx, server2.ID, "OEM", oemBytes)
	encryptDuration := time.Since(start)
	if err != nil {
		t.Fatalf("Encryption of large payload failed: %v", err)
	}
	t.Logf("Encryption took: %v", encryptDuration)

	start = time.Now()
	payload, err := server2.ReceiveEncrypted(ctx, server1.ID, msg)
	decryptDuration := time.Since(start)
	if err != nil {
		t.Fatalf("Decryption of large payload failed: %v", err)
	}
	t.Logf("Decryption took: %v", decryptDuration)

	if payload.Schema != "OEM" {
		t.Error("Schema mismatch")
	}

	// Verify data integrity
	var decryptedOEM LargeOEM
	if err := json.Unmarshal(payload.Data, &decryptedOEM); err != nil {
		t.Fatalf("Failed to unmarshal decrypted OEM: %v", err)
	}

	if len(decryptedOEM.DataPoints) != 10000 {
		t.Errorf("Data point count mismatch: got %d, want 10000", len(decryptedOEM.DataPoints))
	}
}

func BenchmarkServerToServerEncryption(b *testing.B) {
	ctx := context.Background()

	server1, _ := NewMockSDNServer("server-1", ecies.CurveX25519)
	server2, _ := NewMockSDNServer("server-2", ecies.CurveX25519)
	server1.RegisterPeer(server2.ID, server2.KeyPair.PublicKey, ecies.CurveX25519)

	ommData := json.RawMessage(`{
		"OBJECT_NAME": "ISS (ZARYA)",
		"OBJECT_ID": "1998-067A",
		"EPOCH": "2024-01-25T12:00:00.000Z",
		"MEAN_MOTION": 15.72125391,
		"ECCENTRICITY": 0.0006703,
		"INCLINATION": 51.6416
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg, _ := server1.SendEncrypted(ctx, server2.ID, "OMM", ommData)
		server2.ReceiveEncrypted(ctx, server1.ID, msg)
	}
}
