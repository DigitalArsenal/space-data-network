// Package main provides a host runtime for the SDN WASI module.
// This command loads and runs the WASM module with network and storage capabilities.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spacedatanetwork/sdn-wasi/internal/host"
)

// MockNetwork implements a simple network handler for testing
type MockNetwork struct {
	peerID string
}

func (m *MockNetwork) SendMessage(topic string, data []byte) error {
	fmt.Printf("[Network] Send to %s: %d bytes\n", topic, len(data))
	return nil
}

func (m *MockNetwork) Subscribe(topic string) error {
	fmt.Printf("[Network] Subscribed to: %s\n", topic)
	return nil
}

func (m *MockNetwork) GetPeerID() string {
	return m.peerID
}

// MockStorage implements a simple storage handler for testing
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
	fmt.Printf("[Storage] Stored %d bytes for schema %s, CID: %d\n", len(data), schema, cid)
	return cid, nil
}

func (m *MockStorage) Load(cidStr string) ([]byte, error) {
	var cid uint64
	fmt.Sscanf(cidStr, "%d", &cid)
	if data, ok := m.data[cid]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("not found: %s", cidStr)
}

func main() {
	wasmPath := flag.String("wasm", "dist/sdn-wasi.wasm", "Path to WASM module")
	peerID := flag.String("peer-id", "test-peer-12345", "Peer ID for this node")
	flag.Parse()

	// Load WASM bytes
	wasmBytes, err := os.ReadFile(*wasmPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load WASM module: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Loaded WASM module: %s (%d bytes)\n", *wasmPath, len(wasmBytes))

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		cancel()
	}()

	// Create host
	h, err := host.New(ctx, wasmBytes, host.Config{
		Network: &MockNetwork{peerID: *peerID},
		Storage: NewMockStorage(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create host: %v\n", err)
		os.Exit(1)
	}
	defer h.Close(ctx)

	// Get version
	version, err := h.Version(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get version: %v\n", err)
	} else {
		fmt.Printf("Module version: %s\n", version)
	}

	// Register a test schema
	id, err := h.RegisterSchema(ctx, "TestSchema", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to register schema: %v\n", err)
	} else {
		fmt.Printf("Registered schema with ID: %d\n", id)
	}

	// Interactive mode or run tests
	if len(flag.Args()) > 0 && flag.Args()[0] == "test" {
		runTests(ctx, h)
	} else {
		fmt.Println("\nHost runtime ready. Press Ctrl+C to exit.")
		<-ctx.Done()
	}
}

func runTests(ctx context.Context, h *host.Host) {
	fmt.Println("\n=== Running Tests ===")

	// Test 1: Version
	fmt.Print("Test 1 - Version... ")
	version, err := h.Version(ctx)
	if err != nil {
		fmt.Printf("FAIL: %v\n", err)
	} else if version == "" {
		fmt.Println("FAIL: empty version")
	} else {
		fmt.Printf("PASS (%s)\n", version)
	}

	// Test 2: Schema registration
	fmt.Print("Test 2 - Schema registration... ")
	id, err := h.RegisterSchema(ctx, "EPM.fbs", nil)
	if err != nil {
		fmt.Printf("FAIL: %v\n", err)
	} else if id < 0 {
		fmt.Println("FAIL: invalid ID")
	} else {
		fmt.Printf("PASS (id=%d)\n", id)
	}

	// Test 3: Call exported function
	fmt.Print("Test 3 - Message count... ")
	result, err := h.Call(ctx, "sdn_get_message_count")
	if err != nil {
		fmt.Printf("FAIL: %v\n", err)
	} else {
		fmt.Printf("PASS (count=%d)\n", result[0])
	}

	// Test 4: List schemas
	fmt.Print("Test 4 - List schemas... ")
	result, err = h.Call(ctx, "sdn_list_schemas")
	if err != nil {
		fmt.Printf("FAIL: %v\n", err)
	} else {
		data, _ := h.ReadBuffer(ctx, uint32(result[0]))
		fmt.Printf("PASS (%s)\n", string(data))
	}

	fmt.Println("\n=== Tests Complete ===")
}
