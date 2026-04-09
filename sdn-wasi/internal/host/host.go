// Package host provides the host-side implementation for running the SDN WASI module.
// This package is used by the Go-based host runtime to provide network and I/O
// capabilities to the WASM module.
package host

import (
	"context"
	"fmt"
	"sync"

	"github.com/second-state/WasmEdge-go/wasmedge"
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
	conf    *wasmedge.Configure
	vm      *wasmedge.VM
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
	conf := wasmedge.NewConfigure(wasmedge.WASI)
	vm := wasmedge.NewVMWithConfig(conf)
	if vm == nil {
		conf.Release()
		return nil, fmt.Errorf("failed to create WasmEdge VM")
	}

	h := &Host{
		conf:    conf,
		vm:      vm,
		network: cfg.Network,
		storage: cfg.Storage,
	}

	// Initialize WASI — the VM auto-registers a WASI module when WASI is in the config.
	wasiMod := vm.GetImportModule(wasmedge.WASI)
	if wasiMod == nil {
		h.Release()
		return nil, fmt.Errorf("failed to get WASI import module")
	}
	wasiMod.InitWasi([]string{}, []string{}, []string{})

	// Build host module with env functions
	envMod := wasmedge.NewModule("env")
	if envMod == nil {
		h.Release()
		return nil, fmt.Errorf("failed to create env module")
	}

	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }

	// host_log(ptr i32, length i32) -> void
	addHostFunc(envMod, "host_log", []*wasmedge.ValType{i32(), i32()}, nil,
		func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			ptr := uint32(params[0].(int32))
			length := uint32(params[1].(int32))
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return nil, wasmedge.Result_Success
			}
			data, err := mem.GetData(uint(ptr), uint(length))
			if err != nil {
				return nil, wasmedge.Result_Success
			}
			fmt.Printf("[WASM] %s\n", string(data))
			return nil, wasmedge.Result_Success
		})

	// host_send_message(topicPtr i32, topicLen i32, dataPtr i32, dataLen i32) -> i32
	addHostFunc(envMod, "host_send_message", []*wasmedge.ValType{i32(), i32(), i32(), i32()}, []*wasmedge.ValType{i32()},
		func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			topicPtr := uint32(params[0].(int32))
			topicLen := uint32(params[1].(int32))
			dataPtr := uint32(params[2].(int32))
			dataLen := uint32(params[3].(int32))
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return []interface{}{int32(1)}, wasmedge.Result_Success
			}
			topic, err := mem.GetData(uint(topicPtr), uint(topicLen))
			if err != nil {
				return []interface{}{int32(1)}, wasmedge.Result_Success
			}
			data, err := mem.GetData(uint(dataPtr), uint(dataLen))
			if err != nil {
				return []interface{}{int32(2)}, wasmedge.Result_Success
			}
			if h.network == nil {
				return []interface{}{int32(3)}, wasmedge.Result_Success
			}
			if err := h.network.SendMessage(string(topic), data); err != nil {
				fmt.Printf("[Host] Send error: %v\n", err)
				return []interface{}{int32(4)}, wasmedge.Result_Success
			}
			return []interface{}{int32(0)}, wasmedge.Result_Success
		})

	// host_subscribe(topicPtr i32, topicLen i32) -> i32
	addHostFunc(envMod, "host_subscribe", []*wasmedge.ValType{i32(), i32()}, []*wasmedge.ValType{i32()},
		func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			topicPtr := uint32(params[0].(int32))
			topicLen := uint32(params[1].(int32))
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return []interface{}{int32(1)}, wasmedge.Result_Success
			}
			topic, err := mem.GetData(uint(topicPtr), uint(topicLen))
			if err != nil {
				return []interface{}{int32(1)}, wasmedge.Result_Success
			}
			if h.network == nil {
				return []interface{}{int32(2)}, wasmedge.Result_Success
			}
			if err := h.network.Subscribe(string(topic)); err != nil {
				fmt.Printf("[Host] Subscribe error: %v\n", err)
				return []interface{}{int32(3)}, wasmedge.Result_Success
			}
			return []interface{}{int32(0)}, wasmedge.Result_Success
		})

	// host_get_peer_id(bufPtr i32, bufLen i32) -> i32
	addHostFunc(envMod, "host_get_peer_id", []*wasmedge.ValType{i32(), i32()}, []*wasmedge.ValType{i32()},
		func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			bufPtr := uint32(params[0].(int32))
			bufLen := uint32(params[1].(int32))
			if h.network == nil {
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			peerID := h.network.GetPeerID()
			if uint32(len(peerID)) > bufLen {
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			mem.SetData([]byte(peerID), uint(bufPtr), uint(len(peerID)))
			return []interface{}{int32(len(peerID))}, wasmedge.Result_Success
		})

	// host_store_data(schemaPtr i32, schemaLen i32, dataPtr i32, dataLen i32) -> i64
	addHostFunc(envMod, "host_store_data", []*wasmedge.ValType{i32(), i32(), i32(), i32()}, []*wasmedge.ValType{i64()},
		func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			schemaPtr := uint32(params[0].(int32))
			schemaLen := uint32(params[1].(int32))
			dataPtr := uint32(params[2].(int32))
			dataLen := uint32(params[3].(int32))
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return []interface{}{int64(0)}, wasmedge.Result_Success
			}
			schema, err := mem.GetData(uint(schemaPtr), uint(schemaLen))
			if err != nil {
				return []interface{}{int64(0)}, wasmedge.Result_Success
			}
			data, err := mem.GetData(uint(dataPtr), uint(dataLen))
			if err != nil {
				return []interface{}{int64(0)}, wasmedge.Result_Success
			}
			if h.storage == nil {
				return []interface{}{int64(0)}, wasmedge.Result_Success
			}
			cid, err := h.storage.Store(string(schema), data)
			if err != nil {
				fmt.Printf("[Host] Store error: %v\n", err)
				return []interface{}{int64(0)}, wasmedge.Result_Success
			}
			return []interface{}{int64(cid)}, wasmedge.Result_Success
		})

	// host_load_data(cidPtr i32, cidLen i32, bufPtr i32, bufLen i32) -> i32
	addHostFunc(envMod, "host_load_data", []*wasmedge.ValType{i32(), i32(), i32(), i32()}, []*wasmedge.ValType{i32()},
		func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
			cidPtr := uint32(params[0].(int32))
			cidLen := uint32(params[1].(int32))
			bufPtr := uint32(params[2].(int32))
			bufLen := uint32(params[3].(int32))
			mem := cf.GetMemoryByIndex(0)
			if mem == nil {
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			cidBytes, err := mem.GetData(uint(cidPtr), uint(cidLen))
			if err != nil {
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			if h.storage == nil {
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			data, err := h.storage.Load(string(cidBytes))
			if err != nil {
				fmt.Printf("[Host] Load error: %v\n", err)
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			if uint32(len(data)) > bufLen {
				return []interface{}{int32(0)}, wasmedge.Result_Success
			}
			mem.SetData(data, uint(bufPtr), uint(len(data)))
			return []interface{}{int32(len(data))}, wasmedge.Result_Success
		})

	if err := vm.RegisterModule(envMod); err != nil {
		envMod.Release()
		h.Release()
		return nil, fmt.Errorf("failed to register env module: %w", err)
	}

	// Load, validate, and instantiate the WASM module.
	// WasmEdge will run _start automatically for WASI command modules.
	if err := vm.LoadWasmBuffer(wasmBytes); err != nil {
		h.Release()
		return nil, fmt.Errorf("failed to load WASM module: %w", err)
	}
	if err := vm.Validate(); err != nil {
		h.Release()
		return nil, fmt.Errorf("failed to validate WASM module: %w", err)
	}
	if err := vm.Instantiate(); err != nil {
		h.Release()
		return nil, fmt.Errorf("failed to instantiate WASM module: %w", err)
	}

	return h, nil
}

// addHostFunc is a helper to register a host function on a module.
func addHostFunc(mod *wasmedge.Module, name string, params, returns []*wasmedge.ValType,
	fn func(interface{}, *wasmedge.CallingFrame, []interface{}) ([]interface{}, wasmedge.Result)) {
	ft := wasmedge.NewFunctionType(params, returns)
	hostfn := wasmedge.NewFunction(ft, fn, nil, 0)
	ft.Release()
	mod.AddFunction(name, hostfn)
}

// Release frees all WasmEdge resources.
func (h *Host) Release() {
	if h.vm != nil {
		h.vm.Release()
		h.vm = nil
	}
	if h.conf != nil {
		h.conf.Release()
		h.conf = nil
	}
}

// Close releases resources
func (h *Host) Close(ctx context.Context) error {
	h.Release()
	return nil
}

// Call invokes an exported function
func (h *Host) Call(ctx context.Context, name string, args ...interface{}) ([]interface{}, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.vm.Execute(name, args...)
}

// WriteString writes a string to module memory and returns the pointer
func (h *Host) WriteString(ctx context.Context, s string) (uint32, uint32, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	results, err := h.vm.Execute("sdn_get_buffer_ptr")
	if err != nil {
		return 0, 0, fmt.Errorf("sdn_get_buffer_ptr not found: %w", err)
	}

	ptr := uint32(results[0].(int32))
	data := []byte(s)

	mod := h.vm.GetActiveModule()
	if mod == nil {
		return 0, 0, fmt.Errorf("no active module")
	}
	mem := mod.FindMemory("memory")
	if mem == nil {
		return 0, 0, fmt.Errorf("memory not found")
	}
	if err := mem.SetData(data, uint(ptr), uint(len(data))); err != nil {
		return 0, 0, fmt.Errorf("failed to write to memory: %w", err)
	}

	return ptr, uint32(len(data)), nil
}

// ReadBuffer reads from the module's shared buffer
func (h *Host) ReadBuffer(ctx context.Context, length uint32) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	results, err := h.vm.Execute("sdn_get_buffer_ptr")
	if err != nil {
		return nil, fmt.Errorf("sdn_get_buffer_ptr not found: %w", err)
	}

	ptr := uint32(results[0].(int32))

	mod := h.vm.GetActiveModule()
	if mod == nil {
		return nil, fmt.Errorf("no active module")
	}
	mem := mod.FindMemory("memory")
	if mem == nil {
		return nil, fmt.Errorf("memory not found")
	}
	data, err := mem.GetData(uint(ptr), uint(length))
	if err != nil {
		return nil, fmt.Errorf("failed to read from memory: %w", err)
	}

	return data, nil
}

// Version returns the WASM module version
func (h *Host) Version(ctx context.Context) (string, error) {
	result, err := h.Call(ctx, "sdn_version")
	if err != nil {
		return "", err
	}

	data, err := h.ReadBuffer(ctx, uint32(result[0].(int32)))
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

	result, err := h.Call(ctx, "sdn_register_schema",
		int32(namePtr), int32(nameLen),
		int32(0), int32(0),
	)
	if err != nil {
		return -1, err
	}

	return result[0].(int32), nil
}

// ProcessMessage processes an incoming message
func (h *Host) ProcessMessage(ctx context.Context, schema string, data, signature []byte, from string) error {
	schemaPtr, schemaLen, err := h.WriteString(ctx, schema)
	if err != nil {
		return err
	}

	result, err := h.Call(ctx, "sdn_process_message",
		int32(schemaPtr), int32(schemaLen),
		int32(0), int32(0), // data placeholder
		int32(0), int32(0), // signature placeholder
		int32(0), int32(0), // from placeholder
	)
	if err != nil {
		return err
	}

	if result[0].(int32) != 0 {
		return fmt.Errorf("process_message returned error: %d", result[0].(int32))
	}

	return nil
}
