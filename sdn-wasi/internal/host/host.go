// Package host provides the host-side implementation for running the SDN WASI module.
// This package is used by the Go-based host runtime to provide network and I/O
// capabilities to the WASM module.
package host

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// NetworkHandler is implemented by the host to provide network capabilities
type NetworkHandler interface {
	SendMessage(topic string, data []byte) error
	Subscribe(topic string) error
	GetPeerID() string
}

// StorageHandler is implemented by the host to provide storage capabilities
type StorageHandler interface {
	Store(schema string, data []byte) (uint64, error)
	Load(cid string) ([]byte, error)
}

// Host manages the WASM runtime and module
type Host struct {
	runtime wazero.Runtime
	module  api.Module
	network NetworkHandler
	storage StorageHandler
	mu      sync.Mutex
}

// Config contains host configuration
type Config struct {
	Network NetworkHandler
	Storage StorageHandler
}

// New creates a new host runtime
func New(ctx context.Context, wasmBytes []byte, cfg Config) (*Host, error) {
	r := wazero.NewRuntime(ctx)

	// Instantiate WASI
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate WASI: %w", err)
	}

	h := &Host{
		runtime: r,
		network: cfg.Network,
		storage: cfg.Storage,
	}

	// Define host functions
	envBuilder := r.NewHostModuleBuilder("env")

	envBuilder.NewFunctionBuilder().
		WithFunc(h.hostLog).
		Export("host_log")

	envBuilder.NewFunctionBuilder().
		WithFunc(h.hostSendMessage).
		Export("host_send_message")

	envBuilder.NewFunctionBuilder().
		WithFunc(h.hostSubscribe).
		Export("host_subscribe")

	envBuilder.NewFunctionBuilder().
		WithFunc(h.hostGetPeerID).
		Export("host_get_peer_id")

	envBuilder.NewFunctionBuilder().
		WithFunc(h.hostStoreData).
		Export("host_store_data")

	envBuilder.NewFunctionBuilder().
		WithFunc(h.hostLoadData).
		Export("host_load_data")

	if _, err := envBuilder.Instantiate(ctx); err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate env module: %w", err)
	}

	// Compile the WASM module
	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to compile WASM module: %w", err)
	}

	// Instantiate with _start (required for Go WASI modules to initialize runtime)
	// The module will initialize and return quickly in library mode
	config := wazero.NewModuleConfig().
		WithStdout(os.Stdout).
		WithStderr(os.Stderr).
		WithStartFunctions("_start")

	module, err := r.InstantiateModule(ctx, compiled, config)
	if err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate WASM module: %w", err)
	}

	h.module = module

	return h, nil
}

// Close releases resources
func (h *Host) Close(ctx context.Context) error {
	return h.runtime.Close(ctx)
}

// Host function implementations

func (h *Host) hostLog(ctx context.Context, m api.Module, ptr, length uint32) {
	data, ok := m.Memory().Read(ptr, length)
	if ok {
		fmt.Printf("[WASM] %s\n", string(data))
	}
}

func (h *Host) hostSendMessage(ctx context.Context, m api.Module, topicPtr, topicLen, dataPtr, dataLen uint32) uint32 {
	topic, ok := m.Memory().Read(topicPtr, topicLen)
	if !ok {
		return 1
	}

	data, ok := m.Memory().Read(dataPtr, dataLen)
	if !ok {
		return 2
	}

	if h.network == nil {
		return 3
	}

	if err := h.network.SendMessage(string(topic), data); err != nil {
		fmt.Printf("[Host] Send error: %v\n", err)
		return 4
	}

	return 0
}

func (h *Host) hostSubscribe(ctx context.Context, m api.Module, topicPtr, topicLen uint32) uint32 {
	topic, ok := m.Memory().Read(topicPtr, topicLen)
	if !ok {
		return 1
	}

	if h.network == nil {
		return 2
	}

	if err := h.network.Subscribe(string(topic)); err != nil {
		fmt.Printf("[Host] Subscribe error: %v\n", err)
		return 3
	}

	return 0
}

func (h *Host) hostGetPeerID(ctx context.Context, m api.Module, bufPtr, bufLen uint32) uint32 {
	if h.network == nil {
		return 0
	}

	peerID := h.network.GetPeerID()
	if uint32(len(peerID)) > bufLen {
		return 0
	}

	m.Memory().Write(bufPtr, []byte(peerID))
	return uint32(len(peerID))
}

func (h *Host) hostStoreData(ctx context.Context, m api.Module, schemaPtr, schemaLen, dataPtr, dataLen uint32) uint64 {
	schema, ok := m.Memory().Read(schemaPtr, schemaLen)
	if !ok {
		return 0
	}

	data, ok := m.Memory().Read(dataPtr, dataLen)
	if !ok {
		return 0
	}

	if h.storage == nil {
		return 0
	}

	cid, err := h.storage.Store(string(schema), data)
	if err != nil {
		fmt.Printf("[Host] Store error: %v\n", err)
		return 0
	}

	return cid
}

func (h *Host) hostLoadData(ctx context.Context, m api.Module, cidPtr, cidLen, bufPtr, bufLen uint32) uint32 {
	cid, ok := m.Memory().Read(cidPtr, cidLen)
	if !ok {
		return 0
	}

	if h.storage == nil {
		return 0
	}

	data, err := h.storage.Load(string(cid))
	if err != nil {
		fmt.Printf("[Host] Load error: %v\n", err)
		return 0
	}

	if uint32(len(data)) > bufLen {
		return 0
	}

	m.Memory().Write(bufPtr, data)
	return uint32(len(data))
}

// Call invokes an exported function
func (h *Host) Call(ctx context.Context, name string, args ...uint64) ([]uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	fn := h.module.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("function not found: %s", name)
	}

	return fn.Call(ctx, args...)
}

// WriteString writes a string to module memory and returns the pointer
func (h *Host) WriteString(ctx context.Context, s string) (uint32, uint32, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Get buffer pointer from module
	getPtr := h.module.ExportedFunction("sdn_get_buffer_ptr")
	if getPtr == nil {
		return 0, 0, fmt.Errorf("sdn_get_buffer_ptr not found")
	}

	result, err := getPtr.Call(ctx)
	if err != nil {
		return 0, 0, err
	}

	ptr := uint32(result[0])
	data := []byte(s)

	if !h.module.Memory().Write(ptr, data) {
		return 0, 0, fmt.Errorf("failed to write to memory")
	}

	return ptr, uint32(len(data)), nil
}

// ReadBuffer reads from the module's shared buffer
func (h *Host) ReadBuffer(ctx context.Context, length uint32) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	getPtr := h.module.ExportedFunction("sdn_get_buffer_ptr")
	if getPtr == nil {
		return nil, fmt.Errorf("sdn_get_buffer_ptr not found")
	}

	result, err := getPtr.Call(ctx)
	if err != nil {
		return nil, err
	}

	ptr := uint32(result[0])
	data, ok := h.module.Memory().Read(ptr, length)
	if !ok {
		return nil, fmt.Errorf("failed to read from memory")
	}

	return data, nil
}

// Version returns the WASM module version
func (h *Host) Version(ctx context.Context) (string, error) {
	result, err := h.Call(ctx, "sdn_version")
	if err != nil {
		return "", err
	}

	data, err := h.ReadBuffer(ctx, uint32(result[0]))
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// RegisterSchema registers a schema with the module
func (h *Host) RegisterSchema(ctx context.Context, name string, content []byte) (int32, error) {
	namePtr, nameLen, err := h.WriteString(ctx, name)
	if err != nil {
		return -1, err
	}

	// For content, we need a separate write
	// For now, pass 0,0 for empty content
	result, err := h.Call(ctx, "sdn_register_schema",
		uint64(namePtr), uint64(nameLen),
		0, 0,
	)
	if err != nil {
		return -1, err
	}

	return int32(result[0]), nil
}

// ProcessMessage processes an incoming message
func (h *Host) ProcessMessage(ctx context.Context, schema string, data, signature []byte, from string) error {
	// Write schema
	schemaPtr, schemaLen, err := h.WriteString(ctx, schema)
	if err != nil {
		return err
	}

	// For a proper implementation, we'd need multiple buffer areas
	// This is simplified for demonstration

	result, err := h.Call(ctx, "sdn_process_message",
		uint64(schemaPtr), uint64(schemaLen),
		0, 0, // data placeholder
		0, 0, // signature placeholder
		0, 0, // from placeholder
	)
	if err != nil {
		return err
	}

	if int32(result[0]) != 0 {
		return fmt.Errorf("process_message returned error: %d", int32(result[0]))
	}

	return nil
}
