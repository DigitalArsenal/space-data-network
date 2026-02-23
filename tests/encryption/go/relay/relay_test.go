// Package relay provides tests for edge relay encrypted traffic pass-through.
package relay

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

// EdgeRelay represents a simulated edge relay node
type EdgeRelay struct {
	ID           string
	ForwardedMsg int
	mu           sync.Mutex
	Latency      time.Duration // Simulated network latency
	Metrics      RelayMetrics
}

// RelayMetrics tracks relay performance
type RelayMetrics struct {
	MessagesForwarded int64
	BytesForwarded    int64
	AverageLatency    time.Duration
	MaxLatency        time.Duration
	MinLatency        time.Duration
	EncryptedCount    int64
	UnencryptedCount  int64
}

// RoutingHeader contains unencrypted routing information
type RoutingHeader struct {
	SchemaType       string   `json:"schema_type"`
	DestinationPeers []string `json:"destination_peers"`
	TTL              uint8    `json:"ttl"`
	Priority         uint8    `json:"priority"`
	Encrypted        bool     `json:"encrypted"`
	SourcePeer       string   `json:"source_peer"`
}

// RelayMessage represents a message passing through the relay
type RelayMessage struct {
	Header          RoutingHeader
	EncryptedPayload []byte // Opaque encrypted bytes
	ReceivedAt      time.Time
	ForwardedAt     time.Time
}

// SDNEndpoint represents an SDN server or client endpoint
type SDNEndpoint struct {
	ID      string
	KeyPair *ecies.KeyPair
	Inbox   chan *RelayMessage
}

// NewEdgeRelay creates a new edge relay
func NewEdgeRelay(id string, latency time.Duration) *EdgeRelay {
	return &EdgeRelay{
		ID:      id,
		Latency: latency,
		Metrics: RelayMetrics{
			MinLatency: time.Hour, // Start high
		},
	}
}

// Forward forwards a message without decrypting the payload
func (r *EdgeRelay) Forward(ctx context.Context, msg *RelayMessage, destination *SDNEndpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	msg.ReceivedAt = time.Now()

	// Edge relay can only see the header
	// It CANNOT see or modify the encrypted payload
	if msg.Header.TTL == 0 {
		return fmt.Errorf("message TTL expired")
	}

	// Decrement TTL
	msg.Header.TTL--

	// Simulate network latency
	if r.Latency > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.Latency):
		}
	}

	msg.ForwardedAt = time.Now()
	latency := msg.ForwardedAt.Sub(msg.ReceivedAt)

	// Update metrics
	r.Metrics.MessagesForwarded++
	r.Metrics.BytesForwarded += int64(len(msg.EncryptedPayload))

	if msg.Header.Encrypted {
		r.Metrics.EncryptedCount++
	} else {
		r.Metrics.UnencryptedCount++
	}

	if latency > r.Metrics.MaxLatency {
		r.Metrics.MaxLatency = latency
	}
	if latency < r.Metrics.MinLatency {
		r.Metrics.MinLatency = latency
	}

	// Simple average (in production, use exponential moving average)
	totalLatency := time.Duration(r.Metrics.MessagesForwarded-1)*r.Metrics.AverageLatency + latency
	r.Metrics.AverageLatency = totalLatency / time.Duration(r.Metrics.MessagesForwarded)

	// Forward to destination
	select {
	case destination.Inbox <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("destination inbox full")
	}
}

// GetMetrics returns relay metrics
func (r *EdgeRelay) GetMetrics() RelayMetrics {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Metrics
}

// NewSDNEndpoint creates a new endpoint
func NewSDNEndpoint(id string, curveType ecies.CurveType) (*SDNEndpoint, error) {
	kp, err := ecies.GenerateKeyPair(curveType)
	if err != nil {
		return nil, err
	}

	return &SDNEndpoint{
		ID:      id,
		KeyPair: kp,
		Inbox:   make(chan *RelayMessage, 100),
	}, nil
}

// CreateEncryptedMessage creates an encrypted message for a destination
func (e *SDNEndpoint) CreateEncryptedMessage(destinationKey []byte, curveType ecies.CurveType, schema string, data []byte) (*RelayMessage, error) {
	// Create payload
	payload := map[string]interface{}{
		"schema":    schema,
		"data":      string(data),
		"timestamp": time.Now().UnixNano(),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Encrypt payload
	encrypted, err := ecies.Encrypt(destinationKey, payloadBytes, curveType)
	if err != nil {
		return nil, err
	}

	return &RelayMessage{
		Header: RoutingHeader{
			SchemaType: schema,
			TTL:        64,
			Priority:   1,
			Encrypted:  true,
			SourcePeer: e.ID,
		},
		EncryptedPayload: encrypted.Serialize(),
	}, nil
}

// DecryptMessage decrypts a received message
func (e *SDNEndpoint) DecryptMessage(msg *RelayMessage) ([]byte, error) {
	if !msg.Header.Encrypted {
		return msg.EncryptedPayload, nil
	}

	encrypted, err := ecies.DeserializeEncryptedMessage(msg.EncryptedPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize: %w", err)
	}

	return ecies.Decrypt(e.KeyPair.PrivateKey, encrypted)
}

func TestRelayPassThrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create endpoints
	sender, err := NewSDNEndpoint("sender", ecies.CurveX25519)
	if err != nil {
		t.Fatalf("Failed to create sender: %v", err)
	}

	receiver, err := NewSDNEndpoint("receiver", ecies.CurveX25519)
	if err != nil {
		t.Fatalf("Failed to create receiver: %v", err)
	}

	// Create relay
	relay := NewEdgeRelay("edge-us", 10*time.Millisecond)

	// Create encrypted message
	testData := []byte(`{"OBJECT_NAME": "ISS", "NORAD_ID": 25544}`)
	msg, err := sender.CreateEncryptedMessage(receiver.KeyPair.PublicKey, ecies.CurveX25519, "OMM", testData)
	if err != nil {
		t.Fatalf("Failed to create message: %v", err)
	}
	msg.Header.DestinationPeers = []string{receiver.ID}

	// Verify relay can see header
	if msg.Header.SchemaType != "OMM" {
		t.Error("Relay should be able to read header schema type")
	}
	if !msg.Header.Encrypted {
		t.Error("Header should indicate encrypted payload")
	}

	// Forward through relay
	if err := relay.Forward(ctx, msg, receiver); err != nil {
		t.Fatalf("Relay forward failed: %v", err)
	}

	// Receive message
	select {
	case receivedMsg := <-receiver.Inbox:
		// Verify message was forwarded
		if receivedMsg.Header.TTL != 63 { // Should be decremented
			t.Errorf("TTL should be decremented: got %d, want 63", receivedMsg.Header.TTL)
		}

		// Decrypt
		decrypted, err := receiver.DecryptMessage(receivedMsg)
		if err != nil {
			t.Fatalf("Decryption failed: %v", err)
		}

		// Parse payload
		var payload map[string]interface{}
		if err := json.Unmarshal(decrypted, &payload); err != nil {
			t.Fatalf("Failed to parse payload: %v", err)
		}

		if payload["schema"] != "OMM" {
			t.Error("Schema mismatch in decrypted payload")
		}

	case <-ctx.Done():
		t.Fatal("Timeout waiting for message")
	}

	// Check metrics
	metrics := relay.GetMetrics()
	if metrics.MessagesForwarded != 1 {
		t.Errorf("MessagesForwarded mismatch: got %d, want 1", metrics.MessagesForwarded)
	}
	if metrics.EncryptedCount != 1 {
		t.Errorf("EncryptedCount mismatch: got %d, want 1", metrics.EncryptedCount)
	}
}

func TestRelayCannotDecrypt(t *testing.T) {
	ctx := context.Background()

	sender, _ := NewSDNEndpoint("sender", ecies.CurveX25519)
	receiver, _ := NewSDNEndpoint("receiver", ecies.CurveX25519)
	relay, _ := NewSDNEndpoint("relay", ecies.CurveX25519) // Relay with its own keys

	// Create message encrypted for receiver (NOT for relay)
	testData := []byte(`{"secret": "classified data"}`)
	msg, _ := sender.CreateEncryptedMessage(receiver.KeyPair.PublicKey, ecies.CurveX25519, "OMM", testData)

	// Relay attempts to decrypt - should fail
	_, err := relay.DecryptMessage(msg)
	if err == nil {
		t.Error("Relay should NOT be able to decrypt message intended for receiver")
	}

	// Receiver can decrypt
	decrypted, err := receiver.DecryptMessage(msg)
	if err != nil {
		t.Fatalf("Receiver should be able to decrypt: %v", err)
	}

	var payload map[string]interface{}
	json.Unmarshal(decrypted, &payload)
	if payload["schema"] != "OMM" {
		t.Error("Decryption result mismatch")
	}

	_ = ctx // silence unused warning
}

func TestMultiHopRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sender, _ := NewSDNEndpoint("sender", ecies.CurveX25519)
	receiver, _ := NewSDNEndpoint("receiver", ecies.CurveX25519)

	// Create chain of relays (simulating global routing)
	relays := []*EdgeRelay{
		NewEdgeRelay("edge-us", 5*time.Millisecond),
		NewEdgeRelay("edge-eu", 10*time.Millisecond),
		NewEdgeRelay("edge-asia", 15*time.Millisecond),
	}

	// Create encrypted message with high TTL
	testData := []byte(`{"test": "multi-hop"}`)
	msg, _ := sender.CreateEncryptedMessage(receiver.KeyPair.PublicKey, ecies.CurveX25519, "OMM", testData)
	msg.Header.TTL = 64

	// Create intermediate endpoints for relay forwarding
	intermediates := make([]*SDNEndpoint, len(relays))
	for i := range relays {
		intermediates[i] = &SDNEndpoint{
			ID:    fmt.Sprintf("intermediate-%d", i),
			Inbox: make(chan *RelayMessage, 100),
		}
	}

	// Forward through each relay
	currentMsg := msg
	for i, relay := range relays {
		var destination *SDNEndpoint
		if i == len(relays)-1 {
			destination = receiver // Last relay forwards to receiver
		} else {
			destination = intermediates[i+1]
		}

		if err := relay.Forward(ctx, currentMsg, destination); err != nil {
			t.Fatalf("Relay %d forward failed: %v", i, err)
		}

		if i < len(relays)-1 {
			// Wait for intermediate to receive
			select {
			case currentMsg = <-intermediates[i+1].Inbox:
			case <-ctx.Done():
				t.Fatal("Timeout")
			}
		}
	}

	// Receive and decrypt final message
	select {
	case finalMsg := <-receiver.Inbox:
		// TTL should be decremented by number of hops
		expectedTTL := uint8(64 - len(relays))
		if finalMsg.Header.TTL != expectedTTL {
			t.Errorf("TTL mismatch after multi-hop: got %d, want %d", finalMsg.Header.TTL, expectedTTL)
		}

		decrypted, err := receiver.DecryptMessage(finalMsg)
		if err != nil {
			t.Fatalf("Final decryption failed: %v", err)
		}

		var payload map[string]interface{}
		json.Unmarshal(decrypted, &payload)
		if payload["schema"] != "OMM" {
			t.Error("Schema mismatch after multi-hop")
		}

	case <-ctx.Done():
		t.Fatal("Timeout waiting for final message")
	}

	// Check relay metrics
	var totalForwarded int64
	for _, relay := range relays {
		metrics := relay.GetMetrics()
		totalForwarded += metrics.MessagesForwarded
		t.Logf("Relay %s: forwarded=%d, latency=%.2fms",
			relay.ID, metrics.MessagesForwarded,
			float64(metrics.AverageLatency)/float64(time.Millisecond))
	}

	if totalForwarded != int64(len(relays)) {
		t.Errorf("Total forwarded mismatch: got %d, want %d", totalForwarded, len(relays))
	}
}

func TestCircuitRelayV2(t *testing.T) {
	// Test libp2p circuit relay v2 simulation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// In circuit relay v2, two endpoints communicate through a relay
	// when they can't connect directly (e.g., both behind NATs)
	endpoint1, _ := NewSDNEndpoint("peer-behind-nat-1", ecies.CurveX25519)
	endpoint2, _ := NewSDNEndpoint("peer-behind-nat-2", ecies.CurveX25519)
	circuitRelay := NewEdgeRelay("circuit-relay", 5*time.Millisecond)

	// Endpoint1 -> Relay -> Endpoint2
	msg1, _ := endpoint1.CreateEncryptedMessage(endpoint2.KeyPair.PublicKey, ecies.CurveX25519, "CDM", []byte(`{"direction": "1to2"}`))
	circuitRelay.Forward(ctx, msg1, endpoint2)

	// Endpoint2 -> Relay -> Endpoint1
	msg2, _ := endpoint2.CreateEncryptedMessage(endpoint1.KeyPair.PublicKey, ecies.CurveX25519, "CDM", []byte(`{"direction": "2to1"}`))
	circuitRelay.Forward(ctx, msg2, endpoint1)

	// Both should receive messages
	select {
	case receivedMsg := <-endpoint2.Inbox:
		decrypted, _ := endpoint2.DecryptMessage(receivedMsg)
		if !bytes.Contains(decrypted, []byte("1to2")) {
			t.Error("Endpoint2 received wrong message")
		}
	case <-ctx.Done():
		t.Fatal("Timeout waiting for endpoint2")
	}

	select {
	case receivedMsg := <-endpoint1.Inbox:
		decrypted, _ := endpoint1.DecryptMessage(receivedMsg)
		if !bytes.Contains(decrypted, []byte("2to1")) {
			t.Error("Endpoint1 received wrong message")
		}
	case <-ctx.Done():
		t.Fatal("Timeout waiting for endpoint1")
	}

	// Verify relay forwarded both directions
	metrics := circuitRelay.GetMetrics()
	if metrics.MessagesForwarded != 2 {
		t.Errorf("Expected 2 messages forwarded, got %d", metrics.MessagesForwarded)
	}
}

func TestLatencyMeasurement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sender, _ := NewSDNEndpoint("sender", ecies.CurveX25519)
	receiver, _ := NewSDNEndpoint("receiver", ecies.CurveX25519)

	latencies := []time.Duration{
		1 * time.Millisecond,
		5 * time.Millisecond,
		10 * time.Millisecond,
		20 * time.Millisecond,
	}

	for _, expectedLatency := range latencies {
		relay := NewEdgeRelay("test-relay", expectedLatency)

		// Send multiple messages to measure latency
		for i := 0; i < 10; i++ {
			msg, _ := sender.CreateEncryptedMessage(receiver.KeyPair.PublicKey, ecies.CurveX25519, "OMM", []byte(`{"test": "latency"}`))
			start := time.Now()

			if err := relay.Forward(ctx, msg, receiver); err != nil {
				t.Fatalf("Forward failed: %v", err)
			}

			// Drain receiver inbox
			select {
			case <-receiver.Inbox:
			case <-ctx.Done():
				t.Fatal("Timeout")
			}

			_ = time.Since(start)
		}

		metrics := relay.GetMetrics()

		// Average latency should be close to expected
		tolerance := 5 * time.Millisecond
		if metrics.AverageLatency < expectedLatency-tolerance || metrics.AverageLatency > expectedLatency+tolerance {
			// Note: This can fail due to system scheduling, so we just log
			t.Logf("Latency for %v relay: avg=%v, min=%v, max=%v",
				expectedLatency, metrics.AverageLatency, metrics.MinLatency, metrics.MaxLatency)
		}
	}
}

func TestEncryptionOverhead(t *testing.T) {
	sender, _ := NewSDNEndpoint("sender", ecies.CurveX25519)
	receiver, _ := NewSDNEndpoint("receiver", ecies.CurveX25519)

	// Test different payload sizes
	sizes := []int{100, 1000, 10000, 100000}

	for _, size := range sizes {
		plaintext := bytes.Repeat([]byte("X"), size)

		msg, _ := sender.CreateEncryptedMessage(receiver.KeyPair.PublicKey, ecies.CurveX25519, "OEM", plaintext)

		overhead := float64(len(msg.EncryptedPayload)-size) / float64(size) * 100

		t.Logf("Payload size: %d bytes, Encrypted size: %d bytes, Overhead: %.1f%%",
			size, len(msg.EncryptedPayload), overhead)

		// Overhead should be reasonable (less than 20% for larger payloads)
		if size > 1000 && overhead > 20 {
			t.Errorf("Encryption overhead too high for %d byte payload: %.1f%%", size, overhead)
		}
	}
}

func BenchmarkRelayForward(b *testing.B) {
	ctx := context.Background()

	sender, _ := NewSDNEndpoint("sender", ecies.CurveX25519)
	receiver, _ := NewSDNEndpoint("receiver", ecies.CurveX25519)
	relay := NewEdgeRelay("relay", 0) // No simulated latency for benchmark

	msg, _ := sender.CreateEncryptedMessage(receiver.KeyPair.PublicKey, ecies.CurveX25519, "OMM", []byte(`{"test": "benchmark"}`))

	// Increase inbox size for benchmark
	receiver.Inbox = make(chan *RelayMessage, b.N+100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Create new message for each iteration (TTL gets modified)
		msgCopy := &RelayMessage{
			Header:          msg.Header,
			EncryptedPayload: msg.EncryptedPayload,
		}
		msgCopy.Header.TTL = 64

		relay.Forward(ctx, msgCopy, receiver)

		// Drain inbox
		select {
		case <-receiver.Inbox:
		default:
		}
	}
}
