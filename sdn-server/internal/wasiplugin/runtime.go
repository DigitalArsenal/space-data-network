// Package wasiplugin provides a WasmEdge-based WASI plugin runtime for loading
// C++ plugins compiled to WASM/WASI. The runtime provides host functions
// (time, random, logging) and exposes the plugin's exported API.
package wasiplugin

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/second-state/WasmEdge-go/wasmedge"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

var log = logging.Logger("wasiplugin")

var (
	ErrNotLoaded        = errors.New("WASI plugin module not loaded")
	ErrAllocationFailed = errors.New("WASM memory allocation failed")
)

// Runtime wraps a single WASI plugin module loaded via WasmEdge.
type Runtime struct {
	mod    *wasmrt.Module
	mu     sync.Mutex
	hasCron bool
}

// pluginCallTimeout is the maximum duration for a single WASI plugin function call.
const pluginCallTimeout = 10 * time.Second

// buildHostFuncs returns the host functions for a given module name ("sdn" or "env").
func buildHostFuncs(name string) []wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }

	funcs := []wasmrt.HostFunc{
		{
			Name: "clock_now_ms",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{time.Now().UnixMilli()}, wasmedge.Result_Success
			},
			Params:  nil,
			Returns: []*wasmedge.ValType{i64()},
		},
		{
			Name: "random_bytes",
			Func: func(_ interface{}, callframe *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				ptr := uint32(params[0].(int32))
				length := uint32(params[1].(int32))
				const maxRandomBytes = 8192
				if length > maxRandomBytes {
					return []interface{}{int32(-1)}, wasmedge.Result_Success
				}
				buf := make([]byte, length)
				if _, err := rand.Read(buf); err != nil {
					return []interface{}{int32(-1)}, wasmedge.Result_Success
				}
				mem := callframe.GetMemoryByIndex(0)
				if mem == nil {
					return []interface{}{int32(-1)}, wasmedge.Result_Success
				}
				if err := mem.SetData(buf, uint(ptr), uint(length)); err != nil {
					return []interface{}{int32(-1)}, wasmedge.Result_Success
				}
				return []interface{}{int32(0)}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), i32()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "log",
			Func: func(_ interface{}, callframe *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				level := params[0].(int32)
				ptr := uint32(params[1].(int32))
				length := uint32(params[2].(int32))
				const maxLogLen = 4096
				if length > maxLogLen {
					length = maxLogLen
				}
				mem := callframe.GetMemoryByIndex(0)
				if mem == nil {
					return nil, wasmedge.Result_Success
				}
				data, err := mem.GetData(uint(ptr), uint(length))
				if err != nil {
					return nil, wasmedge.Result_Success
				}
				msg := strings.Map(func(r rune) rune {
					if r < 0x20 && r != ' ' {
						return '?'
					}
					return r
				}, string(data))
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
				return nil, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), i32(), i32()},
			Returns: nil,
		},
	}

	// The "env" module also needs C++ exception/invoke stubs.
	if name == "env" {
		funcs = append(funcs, buildExceptionStubs()...)
	}

	return funcs
}

// stubReturn0 is a host function that returns int32(0).
func stubReturn0(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
	return []interface{}{int32(0)}, wasmedge.Result_Success
}

// stubNoop is a host function that does nothing.
func stubNoop(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
	return nil, wasmedge.Result_Success
}

// stubReturn0I64 returns int64(0).
func stubReturn0I64(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
	return []interface{}{int64(0)}, wasmedge.Result_Success
}

// buildExceptionStubs returns C++ exception handling stubs for the "env" module.
func buildExceptionStubs() []wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }

	i32n := func(n int) []*wasmedge.ValType {
		v := make([]*wasmedge.ValType, n)
		for j := range v {
			v[j] = i32()
		}
		return v
	}

	ret0 := func(name string, nParams int) wasmrt.HostFunc {
		return wasmrt.HostFunc{Name: name, Func: stubReturn0, Params: i32n(nParams), Returns: []*wasmedge.ValType{i32()}}
	}
	noop := func(name string, params []*wasmedge.ValType) wasmrt.HostFunc {
		return wasmrt.HostFunc{Name: name, Func: stubNoop, Params: params, Returns: nil}
	}

	return []wasmrt.HostFunc{
		noop("invoke_vi", i32n(2)),
		ret0("__cxa_find_matching_catch_3", 1),
		ret0("invoke_iii", 3),
		{Name: "__cxa_find_matching_catch_2", Func: stubReturn0, Params: nil, Returns: []*wasmedge.ValType{i32()}},
		noop("__resumeException", i32n(1)),
		ret0("invoke_iiiiii", 6),
		noop("invoke_viiiii", i32n(6)),
		ret0("invoke_iiiii", 5),
		{Name: "invoke_j", Func: stubReturn0I64, Params: i32n(1), Returns: []*wasmedge.ValType{i64()}},
		ret0("invoke_iiii", 4),
		noop("invoke_vijjj", []*wasmedge.ValType{i32(), i32(), i64(), i64(), i64()}),
		noop("invoke_viiii", i32n(5)),
		noop("invoke_viii", i32n(4)),
		ret0("invoke_ii", 2),
		noop("invoke_v", i32n(1)),
		ret0("invoke_i", 1),
		ret0("invoke_iiiiiiii", 8),
		noop("invoke_vii", i32n(3)),
		noop("invoke_viiiiii", i32n(7)),
		ret0("llvm_eh_typeid_for", 1),
		ret0("__cxa_begin_catch", 1),
		noop("__cxa_end_catch", nil),
		noop("__throw_exception_with_stack_trace", i32n(1)),
		ret0("invoke_iiiiiiiiii", 10),
	}
}

// New loads a WASI plugin from raw WASM bytes. The module must export
// malloc, free, plugin_init, plugin_handle_request, plugin_get_public_key,
// and plugin_get_metadata. Host functions (sdn.clock_now_ms, sdn.random_bytes,
// sdn.log) are registered automatically.
func New(ctx context.Context, wasmBytes []byte) (*Runtime, error) {
	mod, err := wasmrt.NewModule(wasmBytes,
		wasmrt.WithWASI(),
		wasmrt.WithMaxMemoryPages(512),
		wasmrt.WithHostModule("sdn", buildHostFuncs("sdn")),
		wasmrt.WithHostModule("env", buildHostFuncs("env")),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create WASM module: %w", err)
	}

	// Call _initialize if present (WASI reactor pattern).
	if _, initErr := mod.Execute("_initialize"); initErr != nil {
		// Not all modules export _initialize — ignore if missing.
		// WasmEdge returns an error for missing exports, which is fine.
	}

	// Verify required exports exist by attempting a no-op metadata call.
	// The actual validation happens at first use, but we check malloc/free early.
	if _, err := mod.AllocateSize(1); err != nil {
		mod.Release()
		return nil, fmt.Errorf("WASM module missing malloc export: %w", err)
	}

	// Check for optional cron export
	hasCron := false
	if fnames := mod.VM().GetActiveModule().ListFunction(); fnames != nil {
		for _, fn := range fnames {
			if fn == "plugin_cron" {
				hasCron = true
				break
			}
		}
	}

	return &Runtime{
		mod:     mod,
		hasCron: hasCron,
	}, nil
}

// Close releases the WasmEdge runtime and module.
func (rt *Runtime) Close(ctx context.Context) error {
	if rt.mod != nil {
		rt.mod.Release()
	}
	return nil
}

// Init calls plugin_init with the binary config blob.
func (rt *Runtime) Init(ctx context.Context, config []byte) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	configPtr, err := rt.mod.Allocate(config)
	if err != nil {
		return fmt.Errorf("failed to allocate config: %w", err)
	}
	defer rt.mod.Deallocate(configPtr)

	results, err := rt.mod.Execute("plugin_init", int32(configPtr), int32(len(config)))
	if err != nil {
		return fmt.Errorf("plugin_init call failed: %w", err)
	}

	if status := wasmrt.ToInt32(results[0]); status != 0 {
		return fmt.Errorf("plugin_init returned error status %d", status)
	}
	return nil
}

// GetPublicKey returns the server's P-256 uncompressed public key (65 bytes).
func (rt *Runtime) GetPublicKey(ctx context.Context) ([]byte, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	const outCap = 128
	outPtr, err := rt.mod.AllocateSize(outCap)
	if err != nil {
		return nil, err
	}
	defer rt.mod.Deallocate(outPtr)

	results, err := rt.mod.Execute("plugin_get_public_key", int32(outPtr), int32(outCap))
	if err != nil {
		return nil, fmt.Errorf("plugin_get_public_key call failed: %w", err)
	}

	length := wasmrt.ToInt32(results[0])
	if length < 0 {
		return nil, fmt.Errorf("plugin_get_public_key returned error %d", length)
	}
	if uint32(length) > outCap {
		return nil, fmt.Errorf("plugin_get_public_key output length %d exceeds buffer capacity %d", length, outCap)
	}

	return rt.mod.ReadMemory(outPtr, uint32(length))
}

// GetMetadata returns the binary metadata blob from the plugin.
func (rt *Runtime) GetMetadata(ctx context.Context) ([]byte, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	const outCap = 4096
	outPtr, err := rt.mod.AllocateSize(outCap)
	if err != nil {
		return nil, err
	}
	defer rt.mod.Deallocate(outPtr)

	results, err := rt.mod.Execute("plugin_get_metadata", int32(outPtr), int32(outCap))
	if err != nil {
		return nil, fmt.Errorf("plugin_get_metadata call failed: %w", err)
	}

	length := wasmrt.ToInt32(results[0])
	if length < 0 {
		return nil, fmt.Errorf("plugin_get_metadata returned error %d", length)
	}
	if uint32(length) > outCap {
		return nil, fmt.Errorf("plugin_get_metadata output length %d exceeds buffer capacity %d", length, outCap)
	}

	return rt.mod.ReadMemory(outPtr, uint32(length))
}

// HandleRequest processes a binary OrbPro key exchange packet.
// Returns (response_bytes, status_code, error).
func (rt *Runtime) HandleRequest(ctx context.Context, packet []byte, hostHeader string) ([]byte, int32, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	reqPtr, err := rt.mod.Allocate(packet)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate request: %w", err)
	}
	defer rt.mod.Deallocate(reqPtr)

	hostBytes := append([]byte(hostHeader), 0) // NUL-terminated
	hostPtr, err := rt.mod.Allocate(hostBytes)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate host header: %w", err)
	}
	defer rt.mod.Deallocate(hostPtr)

	const outCap = 8192
	outPtr, err := rt.mod.AllocateSize(outCap)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate output: %w", err)
	}
	defer rt.mod.Deallocate(outPtr)

	// size_t on wasm32 is 4 bytes
	outLenPtr, err := rt.mod.AllocateSize(4)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate output length: %w", err)
	}
	defer rt.mod.Deallocate(outLenPtr)

	results, err := rt.mod.Execute("plugin_handle_request",
		int32(reqPtr), int32(len(packet)),
		int32(hostPtr),
		int32(outPtr), int32(outCap),
		int32(outLenPtr),
	)
	if err != nil {
		return nil, -1, fmt.Errorf("plugin_handle_request call failed: %w", err)
	}

	status := wasmrt.ToInt32(results[0])

	outLenBytes, err := rt.mod.ReadMemory(outLenPtr, 4)
	if err != nil {
		return nil, status, fmt.Errorf("failed to read output length from WASM memory: %w", err)
	}
	outLen := binary.LittleEndian.Uint32(outLenBytes)

	if outLen == 0 {
		return nil, status, nil
	}
	if outLen > outCap {
		return nil, status, fmt.Errorf("plugin output length %d exceeds buffer capacity %d", outLen, outCap)
	}

	output, err := rt.mod.ReadMemory(outPtr, outLen)
	if err != nil {
		return nil, status, fmt.Errorf("failed to read output: %w", err)
	}

	return output, status, nil
}

// RequestChallenge asks the guest to issue a challenge token for v3 protocol.
func (rt *Runtime) RequestChallenge(ctx context.Context, requestPayload []byte) ([]byte, int32, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	reqPayload := append([]byte{}, requestPayload...)
	reqPtr, err := rt.mod.Allocate(reqPayload)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate request payload: %w", err)
	}
	defer rt.mod.Deallocate(reqPtr)

	const outCap = 1024
	outPtr, err := rt.mod.AllocateSize(outCap)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate output: %w", err)
	}
	defer rt.mod.Deallocate(outPtr)

	outLenPtr, err := rt.mod.AllocateSize(4)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to allocate output length: %w", err)
	}
	defer rt.mod.Deallocate(outLenPtr)

	results, err := rt.mod.Execute("plugin_request_challenge",
		int32(reqPtr),
		int32(len(requestPayload)),
		int32(outPtr),
		int32(outCap),
		int32(outLenPtr),
	)
	if err != nil {
		return nil, -1, fmt.Errorf("plugin_request_challenge call failed: %w", err)
	}

	status := wasmrt.ToInt32(results[0])
	outLenBytes, err := rt.mod.ReadMemory(outLenPtr, 4)
	if err != nil {
		return nil, status, fmt.Errorf("failed to read output length from WASM memory: %w", err)
	}
	outLen := binary.LittleEndian.Uint32(outLenBytes)

	if outLen == 0 {
		return nil, status, nil
	}
	if outLen > outCap {
		return nil, status, fmt.Errorf("plugin challenge output length %d exceeds buffer capacity %d", outLen, outCap)
	}

	output, err := rt.mod.ReadMemory(outPtr, outLen)
	if err != nil {
		return nil, status, fmt.Errorf("failed to read output: %w", err)
	}

	return output, status, nil
}

// HasCron returns true if the plugin exports plugin_cron.
func (rt *Runtime) HasCron() bool {
	return rt.hasCron
}

// Cron calls the plugin_cron export with a method name and optional input.
//
// Signature: plugin_cron(method_ptr, method_len, in_ptr, in_len, out_ptr, out_cap) → i32
func (rt *Runtime) Cron(ctx context.Context, method string, input []byte) ([]byte, error) {
	if !rt.hasCron {
		return nil, fmt.Errorf("plugin does not export plugin_cron")
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	methodBytes := []byte(method)
	methodPtr, err := rt.mod.Allocate(methodBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate method name: %w", err)
	}
	defer rt.mod.Deallocate(methodPtr)

	var inputPtr uint32
	inputLen := 0
	if len(input) > 0 {
		inputPtr, err = rt.mod.Allocate(input)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate input: %w", err)
		}
		defer rt.mod.Deallocate(inputPtr)
		inputLen = len(input)
	}

	const outCap = 16384 // 16KB output buffer
	outPtr, err := rt.mod.AllocateSize(outCap)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate output: %w", err)
	}
	defer rt.mod.Deallocate(outPtr)

	results, err := rt.mod.Execute("plugin_cron",
		int32(methodPtr), int32(len(methodBytes)),
		int32(inputPtr), int32(inputLen),
		int32(outPtr), int32(outCap),
	)
	if err != nil {
		return nil, fmt.Errorf("plugin_cron(%q) call failed: %w", method, err)
	}

	written := wasmrt.ToInt32(results[0])
	if written < 0 {
		return nil, fmt.Errorf("plugin_cron(%q) returned error %d", method, written)
	}
	if written == 0 {
		return nil, nil
	}
	if uint32(written) > outCap {
		return nil, fmt.Errorf("plugin_cron(%q) output %d exceeds buffer %d", method, written, outCap)
	}

	return rt.mod.ReadMemory(outPtr, uint32(written))
}
