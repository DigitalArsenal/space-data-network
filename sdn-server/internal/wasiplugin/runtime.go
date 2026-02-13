// Package wasiplugin provides a Wazero-based WASI plugin runtime for loading
// C++ plugins compiled to WASM/WASI by wasi-sdk. The runtime provides host
// functions (time, random, logging) and exposes the plugin's exported API.
package wasiplugin

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

var log = logging.Logger("wasiplugin")

var (
	ErrNotLoaded        = errors.New("WASI plugin module not loaded")
	ErrAllocationFailed = errors.New("WASM memory allocation failed")
)

// Runtime wraps a single WASI plugin module loaded via Wazero.
type Runtime struct {
	wazRuntime wazero.Runtime
	module     api.Module
	mu         sync.Mutex

	mallocFn api.Function
	freeFn   api.Function

	initFn          api.Function
	handleRequestFn api.Function
	getPublicKeyFn  api.Function
	getMetadataFn   api.Function
}

// New loads a WASI plugin from raw WASM bytes. The module must export
// malloc, free, plugin_init, plugin_handle_request, plugin_get_public_key,
// and plugin_get_metadata. Host functions (sdn.clock_now_ms, sdn.random_bytes,
// sdn.log) are registered automatically.
func New(ctx context.Context, wasmBytes []byte) (*Runtime, error) {
	r := wazero.NewRuntime(ctx)

	// Standard WASI imports (libc may call fd_write, proc_exit, etc.)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate WASI: %w", err)
	}

	// Custom "sdn" host module with plugin-specific host functions.
	_, err := r.NewHostModuleBuilder("sdn").
		NewFunctionBuilder().
		WithGoModuleFunction(
			api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
				stack[0] = api.EncodeI64(time.Now().UnixMilli())
			}),
			nil, // no params
			[]api.ValueType{api.ValueTypeI64},
		).
		Export("clock_now_ms").
		NewFunctionBuilder().
		WithGoModuleFunction(
			api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
				ptr := api.DecodeU32(stack[0])
				length := api.DecodeU32(stack[1])
				buf := make([]byte, length)
				if _, err := rand.Read(buf); err != nil {
					stack[0] = api.EncodeI32(-1)
					return
				}
				if !mod.Memory().Write(ptr, buf) {
					stack[0] = api.EncodeI32(-1)
					return
				}
				stack[0] = 0
			}),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
			[]api.ValueType{api.ValueTypeI32},
		).
		Export("random_bytes").
		NewFunctionBuilder().
		WithGoModuleFunction(
			api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
				level := api.DecodeI32(stack[0])
				ptr := api.DecodeU32(stack[1])
				length := api.DecodeU32(stack[2])
				data, ok := mod.Memory().Read(ptr, length)
				if !ok {
					return
				}
				msg := string(data)
				switch {
				case level <= 0:
					log.Debugf("[plugin] %s", msg)
				case level == 1:
					log.Infof("[plugin] %s", msg)
				case level == 2:
					log.Warnf("[plugin] %s", msg)
				default:
					log.Errorf("[plugin] %s", msg)
				}
			}),
			[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
			nil, // void return
		).
		Export("log").
		Instantiate(ctx)
	if err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to register sdn host module: %w", err)
	}

	module, err := r.Instantiate(ctx, wasmBytes)
	if err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate WASM module: %w", err)
	}

	rt := &Runtime{
		wazRuntime:      r,
		module:          module,
		mallocFn:        module.ExportedFunction("malloc"),
		freeFn:          module.ExportedFunction("free"),
		initFn:          module.ExportedFunction("plugin_init"),
		handleRequestFn: module.ExportedFunction("plugin_handle_request"),
		getPublicKeyFn:  module.ExportedFunction("plugin_get_public_key"),
		getMetadataFn:   module.ExportedFunction("plugin_get_metadata"),
	}

	if rt.mallocFn == nil || rt.freeFn == nil {
		r.Close(ctx)
		return nil, fmt.Errorf("WASM module missing malloc/free exports")
	}
	if rt.initFn == nil || rt.handleRequestFn == nil ||
		rt.getPublicKeyFn == nil || rt.getMetadataFn == nil {
		r.Close(ctx)
		return nil, fmt.Errorf("WASM module missing required plugin_* exports")
	}

	return rt, nil
}

// Close releases the Wazero runtime and module.
func (rt *Runtime) Close(ctx context.Context) error {
	if rt.wazRuntime != nil {
		return rt.wazRuntime.Close(ctx)
	}
	return nil
}

// Init calls plugin_init with the binary config blob.
// Config format: privateKey(32) + publicKey(65) + secretLen(4 LE) + secret(N)
//   + domainsCsv(NUL-terminated) + epochPeriodMs(8 LE) + maxSkewMs(8 LE) + leaseMs(8 LE)
func (rt *Runtime) Init(ctx context.Context, config []byte) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	configPtr, err := rt.allocate(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to allocate config: %w", err)
	}
	defer rt.deallocate(ctx, configPtr)

	results, err := rt.initFn.Call(ctx, uint64(configPtr), uint64(len(config)))
	if err != nil {
		return fmt.Errorf("plugin_init call failed: %w", err)
	}

	if status := api.DecodeI32(results[0]); status != 0 {
		return fmt.Errorf("plugin_init returned error status %d", status)
	}
	return nil
}

// GetPublicKey returns the server's P-256 uncompressed public key (65 bytes).
func (rt *Runtime) GetPublicKey(ctx context.Context) ([]byte, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	const outCap = 128
	outPtr, err := rt.allocateSize(ctx, outCap)
	if err != nil {
		return nil, err
	}
	defer rt.deallocate(ctx, outPtr)

	results, err := rt.getPublicKeyFn.Call(ctx, uint64(outPtr), uint64(outCap))
	if err != nil {
		return nil, fmt.Errorf("plugin_get_public_key call failed: %w", err)
	}

	length := api.DecodeI32(results[0])
	if length < 0 {
		return nil, fmt.Errorf("plugin_get_public_key returned error %d", length)
	}

	return rt.readMemory(outPtr, uint32(length))
}

// GetMetadata returns the binary metadata blob from the plugin.
// Format: domainCount(4 LE) + [domainLen(2 LE) + domain(N)]...
func (rt *Runtime) GetMetadata(ctx context.Context) ([]byte, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	const outCap = 4096
	outPtr, err := rt.allocateSize(ctx, outCap)
	if err != nil {
		return nil, err
	}
	defer rt.deallocate(ctx, outPtr)

	results, err := rt.getMetadataFn.Call(ctx, uint64(outPtr), uint64(outCap))
	if err != nil {
		return nil, fmt.Errorf("plugin_get_metadata call failed: %w", err)
	}

	length := api.DecodeI32(results[0])
	if length < 0 {
		return nil, fmt.Errorf("plugin_get_metadata returned error %d", length)
	}

	return rt.readMemory(outPtr, uint32(length))
}

// HandleRequest processes a binary OrbPro key exchange packet.
// Returns (response_bytes, status_code, error). The response contains the
// binary protocol response including error status when status != 0.
func (rt *Runtime) HandleRequest(ctx context.Context, packet []byte, hostHeader string) ([]byte, int32, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	reqPtr, err := rt.allocate(ctx, packet)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate request: %w", err)
	}
	defer rt.deallocate(ctx, reqPtr)

	hostBytes := append([]byte(hostHeader), 0) // NUL-terminated
	hostPtr, err := rt.allocate(ctx, hostBytes)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate host header: %w", err)
	}
	defer rt.deallocate(ctx, hostPtr)

	const outCap = 8192
	outPtr, err := rt.allocateSize(ctx, outCap)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate output: %w", err)
	}
	defer rt.deallocate(ctx, outPtr)

	// size_t on wasm32 is 4 bytes
	outLenPtr, err := rt.allocateSize(ctx, 4)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate output length: %w", err)
	}
	defer rt.deallocate(ctx, outLenPtr)

	results, err := rt.handleRequestFn.Call(ctx,
		uint64(reqPtr), uint64(len(packet)),
		uint64(hostPtr),
		uint64(outPtr), uint64(outCap),
		uint64(outLenPtr),
	)
	if err != nil {
		return nil, -1, fmt.Errorf("plugin_handle_request call failed: %w", err)
	}

	status := api.DecodeI32(results[0])

	outLenBytes, ok := rt.module.Memory().Read(outLenPtr, 4)
	if !ok {
		return nil, status, fmt.Errorf("failed to read output length from WASM memory")
	}
	outLen := binary.LittleEndian.Uint32(outLenBytes)

	if outLen == 0 {
		return nil, status, nil
	}

	output, err := rt.readMemory(outPtr, outLen)
	if err != nil {
		return nil, status, fmt.Errorf("failed to read output: %w", err)
	}

	return output, status, nil
}

// --- memory helpers ---

func (rt *Runtime) allocate(ctx context.Context, data []byte) (uint32, error) {
	results, err := rt.mallocFn.Call(ctx, uint64(len(data)))
	if err != nil {
		return 0, fmt.Errorf("malloc failed: %w", err)
	}
	ptr := uint32(results[0])
	if ptr == 0 {
		return 0, ErrAllocationFailed
	}
	if !rt.module.Memory().Write(ptr, data) {
		return 0, fmt.Errorf("failed to write %d bytes to WASM memory at %d", len(data), ptr)
	}
	return ptr, nil
}

func (rt *Runtime) allocateSize(ctx context.Context, size uint32) (uint32, error) {
	results, err := rt.mallocFn.Call(ctx, uint64(size))
	if err != nil {
		return 0, fmt.Errorf("malloc failed: %w", err)
	}
	ptr := uint32(results[0])
	if ptr == 0 {
		return 0, ErrAllocationFailed
	}
	return ptr, nil
}

func (rt *Runtime) deallocate(ctx context.Context, ptr uint32) {
	if rt.freeFn != nil {
		_, _ = rt.freeFn.Call(ctx, uint64(ptr))
	}
}

func (rt *Runtime) readMemory(ptr, size uint32) ([]byte, error) {
	data, ok := rt.module.Memory().Read(ptr, size)
	if !ok {
		return nil, fmt.Errorf("failed to read %d bytes at offset %d from WASM memory", size, ptr)
	}
	result := make([]byte, size)
	copy(result, data)
	return result, nil
}
