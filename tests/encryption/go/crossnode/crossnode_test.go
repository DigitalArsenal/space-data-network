// Package crossnode provides comprehensive cross-node encryption tests.
// This simulates all node type combinations: browser-to-server, server-to-server,
// relay pass-through, desktop-to-desktop, and wallet-derived key encryption.
// Payloads use FlatBuffer-like binary serialization to match the actual SDN protocol.
package crossnode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/tests/encryption/go/ecies"
)

// FlatBufferPayload simulates a FlatBuffer-serialized SDS message.
// In production, flatc-generated code handles this. Here we use a simplified
// binary format: [schema_tag(4)] [payload_len(4)] [payload_bytes]
type FlatBufferPayload struct {
	SchemaTag [4]byte // e.g., "OMM\x00", "CDM\x00", "EPM\x00"
	Data      []byte
}

func NewFlatBufferPayload(schema string, data []byte) *FlatBufferPayload {
	var tag [4]byte
	copy(tag[:], schema)
	return &FlatBufferPayload{SchemaTag: tag, Data: data}
}

func (p *FlatBufferPayload) Serialize() []byte {
	buf := make([]byte, 4+4+len(p.Data))
	copy(buf[:4], p.SchemaTag[:])
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(p.Data)))
	copy(buf[8:], p.Data)
	return buf
}

func DeserializeFlatBufferPayload(data []byte) (*FlatBufferPayload, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("payload too short: %d bytes", len(data))
	}
	var tag [4]byte
	copy(tag[:], data[:4])
	payloadLen := binary.LittleEndian.Uint32(data[4:8])
	if uint32(len(data)-8) < payloadLen {
		return nil, fmt.Errorf("payload length mismatch: header says %d, have %d", payloadLen, len(data)-8)
	}
	return &FlatBufferPayload{
		SchemaTag: tag,
		Data:      data[8 : 8+payloadLen],
	}, nil
}

// NodeType represents different SDN node types
type NodeType int

const (
	NodeBrowser NodeType = iota
	NodeServer
	NodeRelay
	NodeDesktop
	NodeWallet
)

func (n NodeType) String() string {
	switch n {
	case NodeBrowser:
		return "browser"
	case NodeServer:
		return "server"
	case NodeRelay:
		return "relay"
	case NodeDesktop:
		return "desktop"
	case NodeWallet:
		return "wallet"
	default:
		return "unknown"
	}
}

// TestNode represents any SDN participant
type TestNode struct {
	ID       string
	Type     NodeType
	KeyPairs map[ecies.CurveType]*ecies.KeyPair
}

func NewTestNode(id string, nodeType NodeType) (*TestNode, error) {
	node := &TestNode{
		ID:       id,
		Type:     nodeType,
		KeyPairs: make(map[ecies.CurveType]*ecies.KeyPair),
	}
	for _, ct := range []ecies.CurveType{ecies.CurveX25519, ecies.CurveSecp256k1, ecies.CurveP256} {
		kp, err := ecies.GenerateKeyPair(ct)
		if err != nil {
			return nil, err
		}
		node.KeyPairs[ct] = kp
	}
	return node, nil
}

// EncryptFlatBuffer encrypts a FlatBuffer payload for a recipient
func (n *TestNode) EncryptFlatBuffer(recipient *TestNode, curve ecies.CurveType, schema string, jsonData []byte) ([]byte, error) {
	fb := NewFlatBufferPayload(schema, jsonData)
	serialized := fb.Serialize()
	enc, err := ecies.Encrypt(recipient.KeyPairs[curve].PublicKey, serialized, curve)
	if err != nil {
		return nil, err
	}
	return enc.Serialize(), nil
}

// DecryptFlatBuffer decrypts and deserializes a FlatBuffer payload
func (n *TestNode) DecryptFlatBuffer(encrypted []byte, curve ecies.CurveType) (*FlatBufferPayload, error) {
	msg, err := ecies.DeserializeEncryptedMessage(encrypted)
	if err != nil {
		return nil, err
	}
	decrypted, err := ecies.Decrypt(n.KeyPairs[curve].PrivateKey, msg)
	if err != nil {
		return nil, err
	}
	return DeserializeFlatBufferPayload(decrypted)
}

// --- Tests ---

func TestBrowserToServerFlatBufferEncryption(t *testing.T) {
	// Simulates sdn-js browser sending ECIES-encrypted FlatBuffer to Go server
	curves := []struct {
		name  string
		curve ecies.CurveType
	}{
		{"X25519", ecies.CurveX25519},
		{"secp256k1", ecies.CurveSecp256k1},
		{"P-256", ecies.CurveP256},
	}

	schemas := []struct {
		name string
		data string
	}{
		{"OMM", `{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","EPOCH":"2024-01-25T12:00:00.000Z","MEAN_MOTION":15.72125391,"ECCENTRICITY":0.0006703,"INCLINATION":51.6416}`},
		{"CDM", `{"MESSAGE_ID":"CDM-2024-001","TCA":"2024-01-26T08:30:45.123Z","MISS_DISTANCE":524.7,"COLLISION_PROBABILITY":1.23e-5}`},
		{"EPM", `{"ENTITY_ID":"ORG-001-NASA","ENTITY_TYPE":"OPERATOR","NAME":"NASA","COUNTRY":"US"}`},
	}

	for _, curve := range curves {
		for _, schema := range schemas {
			t.Run(fmt.Sprintf("%s/%s", curve.name, schema.name), func(t *testing.T) {
				browser, err := NewTestNode("browser-client", NodeBrowser)
				if err != nil {
					t.Fatalf("Failed to create browser node: %v", err)
				}

				server, err := NewTestNode("go-server", NodeServer)
				if err != nil {
					t.Fatalf("Failed to create server node: %v", err)
				}

				// Browser encrypts FlatBuffer for server
				encrypted, err := browser.EncryptFlatBuffer(server, curve.curve, schema.name, []byte(schema.data))
				if err != nil {
					t.Fatalf("Browser encryption failed: %v", err)
				}

				// Server decrypts
				fb, err := server.DecryptFlatBuffer(encrypted, curve.curve)
				if err != nil {
					t.Fatalf("Server decryption failed: %v", err)
				}

				// Verify schema tag
				expectedTag := [4]byte{}
				copy(expectedTag[:], schema.name)
				if fb.SchemaTag != expectedTag {
					t.Errorf("Schema tag mismatch: got %q, want %q", fb.SchemaTag, expectedTag)
				}

				// Verify JSON data roundtrip
				var original, decrypted map[string]interface{}
				json.Unmarshal([]byte(schema.data), &original)
				json.Unmarshal(fb.Data, &decrypted)
				origBytes, _ := json.Marshal(original)
				decBytes, _ := json.Marshal(decrypted)
				if !bytes.Equal(origBytes, decBytes) {
					t.Errorf("Data mismatch:\n  original:  %s\n  decrypted: %s", origBytes, decBytes)
				}
			})
		}
	}
}

func TestServerToServerFlatBufferMultiCurve(t *testing.T) {
	ctx := context.Background()

	alice, _ := NewTestNode("alice", NodeServer)
	bob, _ := NewTestNode("bob", NodeServer)
	carol, _ := NewTestNode("carol", NodeServer)

	servers := []*TestNode{alice, bob, carol}

	// Each server sends to every other server with each curve type
	curves := []ecies.CurveType{ecies.CurveX25519, ecies.CurveSecp256k1, ecies.CurveP256}

	for _, sender := range servers {
		for _, receiver := range servers {
			if sender.ID == receiver.ID {
				continue
			}
			for _, curve := range curves {
				t.Run(fmt.Sprintf("%s->%s/%d", sender.ID, receiver.ID, curve), func(t *testing.T) {
					data := fmt.Sprintf(`{"from":"%s","to":"%s","time":%d}`, sender.ID, receiver.ID, time.Now().UnixNano())
					encrypted, err := sender.EncryptFlatBuffer(receiver, curve, "OMM", []byte(data))
					if err != nil {
						t.Fatalf("Encrypt failed: %v", err)
					}

					fb, err := receiver.DecryptFlatBuffer(encrypted, curve)
					if err != nil {
						t.Fatalf("Decrypt failed: %v", err)
					}

					if string(fb.SchemaTag[:3]) != "OMM" {
						t.Error("Schema tag mismatch")
					}
				})
			}
		}
	}
	_ = ctx
}

func TestRelayCannotDecryptFlatBuffer(t *testing.T) {
	sender, _ := NewTestNode("sender-server", NodeServer)
	relay, _ := NewTestNode("edge-relay", NodeRelay)
	receiver, _ := NewTestNode("receiver-server", NodeServer)

	data := `{"OBJECT_NAME":"STARLINK-1234","NORAD_CAT_ID":99999}`
	encrypted, err := sender.EncryptFlatBuffer(receiver, ecies.CurveX25519, "OMM", []byte(data))
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Relay cannot decrypt
	_, err = relay.DecryptFlatBuffer(encrypted, ecies.CurveX25519)
	if err == nil {
		t.Fatal("Relay should NOT be able to decrypt traffic not addressed to it")
	}

	// Receiver can decrypt
	fb, err := receiver.DecryptFlatBuffer(encrypted, ecies.CurveX25519)
	if err != nil {
		t.Fatalf("Receiver decryption failed: %v", err)
	}

	if !bytes.Contains(fb.Data, []byte("STARLINK-1234")) {
		t.Error("Decrypted payload does not contain expected data")
	}
}

func TestDesktopToDesktopLargePayload(t *testing.T) {
	desktop1, _ := NewTestNode("desktop-1", NodeDesktop)
	desktop2, _ := NewTestNode("desktop-2", NodeDesktop)

	// Generate large ephemeris payload (simulating 7-day propagation)
	type StateVector struct {
		Epoch string  `json:"epoch"`
		X     float64 `json:"x"`
		Y     float64 `json:"y"`
		Z     float64 `json:"z"`
		VX    float64 `json:"vx"`
		VY    float64 `json:"vy"`
		VZ    float64 `json:"vz"`
	}

	points := make([]StateVector, 10080) // 7 days * 24 hours * 60 minutes
	for i := range points {
		points[i] = StateVector{
			Epoch: fmt.Sprintf("2024-01-25T%02d:%02d:00Z", i/60%24, i%60),
			X:     6778.0 * float64(i),
			Y:     float64(i) * 2.5,
			Z:     float64(i) * 3.5,
			VX:    0.001, VY: 0.002, VZ: 0.003,
		}
	}

	payload := struct {
		Object string        `json:"object_name"`
		Points []StateVector `json:"data_points"`
	}{Object: "ISS", Points: points}

	data, _ := json.Marshal(payload)
	t.Logf("Large payload: %.2f MB", float64(len(data))/1024/1024)

	// Test with multiple sizes by slicing
	sizes := []struct {
		name  string
		bytes int
	}{
		{"100KB", 100 * 1024},
		{"1MB", 1024 * 1024},
		{"full", len(data)},
	}

	for _, size := range sizes {
		t.Run(size.name, func(t *testing.T) {
			testData := data
			if size.bytes < len(data) {
				testData = data[:size.bytes]
			}

			start := time.Now()
			encrypted, err := desktop1.EncryptFlatBuffer(desktop2, ecies.CurveX25519, "OEM", testData)
			encDur := time.Since(start)
			if err != nil {
				t.Fatalf("Encrypt failed: %v", err)
			}

			start = time.Now()
			fb, err := desktop2.DecryptFlatBuffer(encrypted, ecies.CurveX25519)
			decDur := time.Since(start)
			if err != nil {
				t.Fatalf("Decrypt failed: %v", err)
			}

			if !bytes.Equal(fb.Data, testData) {
				t.Error("Large payload data mismatch after encrypt/decrypt")
			}

			mbps := float64(len(testData)) / 1024 / 1024 / encDur.Seconds()
			t.Logf("Size=%d enc=%v dec=%v throughput=%.1f MB/s", len(testData), encDur, decDur, mbps)
		})
	}
}

func TestWalletDerivedKeyEncryption(t *testing.T) {
	// Simulate Phantom/MetaMask wallet-derived key for encryption
	curves := []ecies.CurveType{ecies.CurveX25519, ecies.CurveSecp256k1, ecies.CurveP256}

	for _, curve := range curves {
		t.Run(fmt.Sprintf("curve-%d", curve), func(t *testing.T) {
			// Simulate wallet signature (65 bytes like Ethereum personal_sign)
			walletSig := make([]byte, 65)
			rand.Read(walletSig)

			// Derive key from signature
			walletKP, err := ecies.DeriveKeyFromWallet(walletSig, curve)
			if err != nil {
				t.Fatalf("Wallet key derivation failed: %v", err)
			}

			// Server generates its own key
			serverKP, err := ecies.GenerateKeyPair(curve)
			if err != nil {
				t.Fatalf("Server keygen failed: %v", err)
			}

			// Wallet encrypts to server
			fb := NewFlatBufferPayload("OMM", []byte(`{"from":"wallet-user","OBJECT_NAME":"ISS"}`))
			enc, err := ecies.Encrypt(serverKP.PublicKey, fb.Serialize(), curve)
			if err != nil {
				t.Fatalf("Wallet encryption failed: %v", err)
			}

			// Server decrypts
			dec, err := ecies.Decrypt(serverKP.PrivateKey, enc)
			if err != nil {
				t.Fatalf("Server decryption failed: %v", err)
			}

			result, err := DeserializeFlatBufferPayload(dec)
			if err != nil {
				t.Fatalf("FlatBuffer deserialization failed: %v", err)
			}
			if string(result.SchemaTag[:3]) != "OMM" {
				t.Error("Schema mismatch")
			}

			// Server encrypts back to wallet
			enc2, err := ecies.Encrypt(walletKP.PublicKey, fb.Serialize(), curve)
			if err != nil {
				t.Fatalf("Server->wallet encryption failed: %v", err)
			}

			dec2, err := ecies.Decrypt(walletKP.PrivateKey, enc2)
			if err != nil {
				t.Fatalf("Wallet decryption failed: %v", err)
			}

			result2, _ := DeserializeFlatBufferPayload(dec2)
			if !bytes.Equal(result2.Data, result.Data) {
				t.Error("Bidirectional wallet<->server data mismatch")
			}
		})
	}
}

func TestConcurrentPubSubBroadcast(t *testing.T) {
	// Publisher broadcasts encrypted messages to multiple subscribers concurrently
	publisher, _ := NewTestNode("publisher", NodeServer)

	numSubscribers := 10
	subscribers := make([]*TestNode, numSubscribers)
	for i := range subscribers {
		subscribers[i], _ = NewTestNode(fmt.Sprintf("sub-%d", i), NodeServer)
	}

	data := `{"OBJECT_NAME":"GPS-BIIF-12","NORAD_CAT_ID":40294}`
	var wg sync.WaitGroup
	errors := make(chan error, numSubscribers)

	for _, sub := range subscribers {
		wg.Add(1)
		go func(s *TestNode) {
			defer wg.Done()
			encrypted, err := publisher.EncryptFlatBuffer(s, ecies.CurveX25519, "OMM", []byte(data))
			if err != nil {
				errors <- fmt.Errorf("encrypt for %s: %w", s.ID, err)
				return
			}
			fb, err := s.DecryptFlatBuffer(encrypted, ecies.CurveX25519)
			if err != nil {
				errors <- fmt.Errorf("decrypt at %s: %w", s.ID, err)
				return
			}
			if !bytes.Contains(fb.Data, []byte("GPS-BIIF-12")) {
				errors <- fmt.Errorf("data mismatch at %s", s.ID)
			}
		}(sub)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

func TestMultiHopRelayFlatBuffer(t *testing.T) {
	sender, _ := NewTestNode("origin-server", NodeServer)
	receiver, _ := NewTestNode("dest-server", NodeServer)

	relays := make([]*TestNode, 3)
	for i := range relays {
		relays[i], _ = NewTestNode(fmt.Sprintf("relay-%d", i), NodeRelay)
	}

	// Encrypt end-to-end (sender -> receiver)
	data := `{"MESSAGE_ID":"CDM-MULTIHOP","MISS_DISTANCE":100.5}`
	encrypted, err := sender.EncryptFlatBuffer(receiver, ecies.CurveX25519, "CDM", []byte(data))
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Simulate relay forwarding -- relays only see opaque bytes + TTL
	ttl := byte(64)
	for _, relay := range relays {
		ttl--
		// Relay cannot decrypt
		_, err := relay.DecryptFlatBuffer(encrypted, ecies.CurveX25519)
		if err == nil {
			t.Fatalf("Relay %s should not decrypt", relay.ID)
		}
	}

	// Receiver decrypts at the end
	fb, err := receiver.DecryptFlatBuffer(encrypted, ecies.CurveX25519)
	if err != nil {
		t.Fatalf("Receiver decrypt failed: %v", err)
	}

	if !bytes.Contains(fb.Data, []byte("CDM-MULTIHOP")) {
		t.Error("Data mismatch after multi-hop relay")
	}

	if ttl != 61 {
		t.Errorf("TTL should be 61 after 3 hops, got %d", ttl)
	}
}

func TestFlatBufferSerialization(t *testing.T) {
	schemas := []string{"OMM", "CDM", "EPM", "OEM", "TDM"}

	for _, schema := range schemas {
		t.Run(schema, func(t *testing.T) {
			data := fmt.Sprintf(`{"schema":"%s","test":true}`, schema)
			fb := NewFlatBufferPayload(schema, []byte(data))

			serialized := fb.Serialize()
			if len(serialized) < 8 {
				t.Fatal("Serialized too short")
			}

			deserialized, err := DeserializeFlatBufferPayload(serialized)
			if err != nil {
				t.Fatalf("Deserialize failed: %v", err)
			}

			expectedTag := [4]byte{}
			copy(expectedTag[:], schema)
			if deserialized.SchemaTag != expectedTag {
				t.Errorf("Tag mismatch: got %v, want %v", deserialized.SchemaTag, expectedTag)
			}
			if !bytes.Equal(deserialized.Data, []byte(data)) {
				t.Error("Data mismatch")
			}
		})
	}
}

func BenchmarkCrossNodeEncryption(b *testing.B) {
	browser, _ := NewTestNode("browser", NodeBrowser)
	server, _ := NewTestNode("server", NodeServer)

	data := `{"OBJECT_NAME":"ISS","MEAN_MOTION":15.72}`

	b.Run("X25519", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			enc, _ := browser.EncryptFlatBuffer(server, ecies.CurveX25519, "OMM", []byte(data))
			server.DecryptFlatBuffer(enc, ecies.CurveX25519)
		}
	})

	b.Run("P256", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			enc, _ := browser.EncryptFlatBuffer(server, ecies.CurveP256, "OMM", []byte(data))
			server.DecryptFlatBuffer(enc, ecies.CurveP256)
		}
	})
}
