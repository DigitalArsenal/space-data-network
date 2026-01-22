package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// MockNetwork implements NetworkHandler for testing
type MockNetwork struct {
	messages     []struct{ topic string; data []byte }
	subscriptions []string
	peerID       string
}

func NewMockNetwork(peerID string) *MockNetwork {
	return &MockNetwork{
		peerID:        peerID,
		messages:      make([]struct{ topic string; data []byte }, 0),
		subscriptions: make([]string, 0),
	}
}

func (m *MockNetwork) SendMessage(topic string, data []byte) error {
	m.messages = append(m.messages, struct{ topic string; data []byte }{topic, data})
	return nil
}

func (m *MockNetwork) Subscribe(topic string) error {
	m.subscriptions = append(m.subscriptions, topic)
	return nil
}

func (m *MockNetwork) GetPeerID() string {
	return m.peerID
}

// MockStorage implements StorageHandler for testing
type MockStorage struct {
	data    map[uint64][]byte
	nextCID uint64
}

func NewMockStorage() *MockStorage {
	return &MockStorage{
		data:    make(map[uint64][]byte),
		nextCID: 1,
	}
}

func (m *MockStorage) Store(schema string, data []byte) (uint64, error) {
	cid := m.nextCID
	m.nextCID++
	m.data[cid] = make([]byte, len(data))
	copy(m.data[cid], data)
	return cid, nil
}

func (m *MockStorage) Load(cidStr string) ([]byte, error) {
	var cid uint64
	for c, d := range m.data {
		cid = c
		return d, nil
		_ = cid
	}
	return nil, nil
}

// findWASMFile locates the built WASM file
func findWASMFile(t *testing.T) string {
	// Try relative paths from test location
	paths := []string{
		"../../dist/sdn-wasi.wasm",
		"../../../sdn-wasi/dist/sdn-wasi.wasm",
	}

	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}

	t.Skip("WASM file not found - run ./scripts/build.sh first")
	return ""
}

func TestHostCreation(t *testing.T) {
	wasmPath := findWASMFile(t)

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("Failed to read WASM file: %v", err)
	}

	ctx := context.Background()
	network := NewMockNetwork("test-peer-123")
	storage := NewMockStorage()

	h, err := New(ctx, wasmBytes, Config{
		Network: network,
		Storage: storage,
	})
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close(ctx)

	t.Log("Host created successfully")
}

func TestHostWithNilHandlers(t *testing.T) {
	wasmPath := findWASMFile(t)

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("Failed to read WASM file: %v", err)
	}

	ctx := context.Background()

	// Test with nil handlers - should still work
	h, err := New(ctx, wasmBytes, Config{
		Network: nil,
		Storage: nil,
	})
	if err != nil {
		t.Fatalf("Failed to create host with nil handlers: %v", err)
	}
	defer h.Close(ctx)

	t.Log("Host with nil handlers created successfully")
}

func TestMockNetwork(t *testing.T) {
	network := NewMockNetwork("peer-abc")

	// Test GetPeerID
	if network.GetPeerID() != "peer-abc" {
		t.Errorf("Expected peer-abc, got %s", network.GetPeerID())
	}

	// Test SendMessage
	err := network.SendMessage("/test/topic", []byte("hello"))
	if err != nil {
		t.Errorf("SendMessage failed: %v", err)
	}
	if len(network.messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(network.messages))
	}

	// Test Subscribe
	err = network.Subscribe("/test/topic")
	if err != nil {
		t.Errorf("Subscribe failed: %v", err)
	}
	if len(network.subscriptions) != 1 {
		t.Errorf("Expected 1 subscription, got %d", len(network.subscriptions))
	}
}

func TestMockStorage(t *testing.T) {
	storage := NewMockStorage()

	// Test Store
	cid, err := storage.Store("TestSchema", []byte("test data"))
	if err != nil {
		t.Errorf("Store failed: %v", err)
	}
	if cid != 1 {
		t.Errorf("Expected CID 1, got %d", cid)
	}

	// Store another
	cid2, err := storage.Store("TestSchema", []byte("more data"))
	if err != nil {
		t.Errorf("Second store failed: %v", err)
	}
	if cid2 != 2 {
		t.Errorf("Expected CID 2, got %d", cid2)
	}
}
