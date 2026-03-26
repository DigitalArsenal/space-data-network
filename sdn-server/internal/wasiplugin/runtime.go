// Package wasiplugin hosts standalone plugin artifacts through the canonical
// Space Data module invoke ABI on a direct WasmEdge runtime.
package wasiplugin

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/second-state/WasmEdge-go/wasmedge"
)

var log = logging.Logger("wasiplugin")

var (
	ErrNotLoaded        = errors.New("WASI plugin module not loaded")
	ErrAllocationFailed = errors.New("WASM memory allocation failed")
)

const (
	maxWasmMemoryPages = 512

	initializeExportName        = "_initialize"
	commandExportName           = "_start"
	invokeExportName            = "plugin_invoke_stream"
	allocExportName             = "plugin_alloc"
	freeExportName              = "plugin_free"
	manifestBytesExportName     = "plugin_get_manifest_flatbuffer"
	manifestBytesSizeExportName = "plugin_get_manifest_flatbuffer_size"

	asyncExecutePollInterval = 25 * time.Millisecond
)

// pluginCallTimeout is the maximum duration for a single plugin function call.
const pluginCallTimeout = 10 * time.Second

// Runtime wraps one standalone WASM plugin module instantiated in WasmEdge.
type Runtime struct {
	mu sync.Mutex

	configure  *wasmedge.Configure
	vm         *wasmedge.VM
	module     *wasmedge.Module
	memory     *wasmedge.Memory
	hostBridge *jsonHostcallBridge
	manifest   []byte
}

// New loads a standalone plugin from raw WASM bytes. The module must expose the
// canonical invoke ABI and embedded-manifest exports, and it is hosted through
// WasmEdge with the canonical sdn_host sync JSON bridge.
func New(ctx context.Context, wasmBytes []byte) (*Runtime, error) {
	conf := wasmedge.NewConfigure(wasmedge.WASI)
	if conf == nil {
		return nil, fmt.Errorf("create WasmEdge configuration")
	}
	conf.SetMaxMemoryPage(maxWasmMemoryPages)

	vm := wasmedge.NewVMWithConfig(conf)
	if vm == nil {
		conf.Release()
		return nil, fmt.Errorf("create WasmEdge VM")
	}

	if wasiModule := vm.GetImportModule(wasmedge.WASI); wasiModule != nil {
		wasiModule.InitWasi([]string{"orbpro-licensing-server"}, nil, nil)
	}

	hostBridge := newJSONHostcallBridge()
	hostModule, err := hostBridge.newImportModule()
	if err != nil {
		vm.Release()
		conf.Release()
		return nil, err
	}
	if err := vm.RegisterModule(hostModule); err != nil {
		hostModule.Release()
		vm.Release()
		conf.Release()
		return nil, fmt.Errorf("register %s host module: %w", hostcallImportModuleName, err)
	}

	if err := vm.LoadWasmBuffer(wasmBytes); err != nil {
		vm.Release()
		conf.Release()
		return nil, fmt.Errorf("load WASM module: %w", err)
	}
	if err := vm.Validate(); err != nil {
		vm.Release()
		conf.Release()
		return nil, fmt.Errorf("validate WASM module: %w", err)
	}
	if err := vm.Instantiate(); err != nil {
		vm.Release()
		conf.Release()
		return nil, fmt.Errorf("instantiate WASM module: %w", err)
	}

	module := vm.GetActiveModule()
	if module == nil {
		vm.Release()
		conf.Release()
		return nil, fmt.Errorf("WasmEdge did not expose an active module")
	}

	memory, err := resolveGuestMemory(module)
	if err != nil {
		vm.Release()
		conf.Release()
		return nil, err
	}

	rt := &Runtime{
		configure:  conf,
		vm:         vm,
		module:     module,
		memory:     memory,
		hostBridge: hostBridge,
	}

	if module.FindFunction(initializeExportName) != nil {
		if _, err := rt.executeWithTimeoutLocked(ctx, initializeExportName); err != nil {
			rt.Close(ctx)
			return nil, fmt.Errorf("run %s: %w", initializeExportName, err)
		}
	}

	if err := rt.requireCanonicalExports(); err != nil {
		rt.Close(ctx)
		return nil, err
	}

	manifestBytes, err := rt.readEmbeddedManifestLocked(ctx)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	rt.manifest = manifestBytes

	return rt, nil
}

// Close releases the WasmEdge runtime and module.
func (rt *Runtime) Close(ctx context.Context) error {
	if rt == nil {
		return nil
	}
	if rt.vm != nil {
		rt.vm.Release()
		rt.vm = nil
	}
	if rt.configure != nil {
		rt.configure.Release()
		rt.configure = nil
	}
	return nil
}

// ManifestBytes returns a copy of the embedded FlatBuffer manifest exported by
// the loaded standalone artifact.
func (rt *Runtime) ManifestBytes() []byte {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return bytesClone(rt.manifest)
}

func (rt *Runtime) requireCanonicalExports() error {
	if rt.module == nil {
		return ErrNotLoaded
	}

	requiredFunctions := []string{
		allocExportName,
		freeExportName,
		invokeExportName,
		manifestBytesExportName,
		manifestBytesSizeExportName,
	}
	for _, exportName := range requiredFunctions {
		if rt.module.FindFunction(exportName) == nil {
			return fmt.Errorf("WASM module missing required export %q", exportName)
		}
	}
	if rt.module.FindFunction(commandExportName) == nil {
		log.Debugf("standalone module does not export %s; continuing on direct invoke surface only", commandExportName)
	}
	return nil
}

func (rt *Runtime) readEmbeddedManifestLocked(ctx context.Context) ([]byte, error) {
	manifestPtr, err := rt.executeUint32Locked(ctx, manifestBytesExportName)
	if err != nil {
		return nil, fmt.Errorf("read manifest pointer: %w", err)
	}
	manifestSize, err := rt.executeUint32Locked(ctx, manifestBytesSizeExportName)
	if err != nil {
		return nil, fmt.Errorf("read manifest size: %w", err)
	}
	if manifestSize == 0 {
		return nil, fmt.Errorf("embedded manifest export returned zero bytes")
	}
	return rt.readMemory(manifestPtr, manifestSize)
}

func resolveGuestMemory(module *wasmedge.Module) (*wasmedge.Memory, error) {
	if module == nil {
		return nil, ErrNotLoaded
	}
	if memory := module.FindMemory("memory"); memory != nil {
		return memory, nil
	}
	memoryNames := module.ListMemory()
	if len(memoryNames) == 0 {
		return nil, fmt.Errorf("WASM module does not export memory")
	}
	memory := module.FindMemory(memoryNames[0])
	if memory == nil {
		return nil, fmt.Errorf("failed to resolve exported memory %q", memoryNames[0])
	}
	return memory, nil
}

func (rt *Runtime) executeWithTimeoutLocked(
	ctx context.Context,
	functionName string,
	params ...interface{},
) ([]interface{}, error) {
	if rt.vm == nil {
		return nil, ErrNotLoaded
	}

	callCtx := ctx
	if callCtx == nil {
		callCtx = context.Background()
	}
	var cancel context.CancelFunc
	if _, ok := callCtx.Deadline(); !ok {
		callCtx, cancel = context.WithTimeout(callCtx, pluginCallTimeout)
	} else {
		cancel = func() {}
	}
	defer cancel()

	async := rt.vm.AsyncExecute(functionName, params...)
	if async == nil {
		return nil, fmt.Errorf("start async execute for %s", functionName)
	}
	defer async.Release()

	for {
		if async.WaitFor(int(asyncExecutePollInterval / time.Millisecond)) {
			return async.GetResult()
		}
		if err := callCtx.Err(); err != nil {
			async.Cancel()
			return nil, fmt.Errorf("%s aborted: %w", functionName, err)
		}
	}
}

func (rt *Runtime) executeUint32Locked(
	ctx context.Context,
	functionName string,
	params ...interface{},
) (uint32, error) {
	results, err := rt.executeWithTimeoutLocked(ctx, functionName, params...)
	if err != nil {
		return 0, err
	}
	if len(results) == 0 {
		return 0, fmt.Errorf("%s returned no values", functionName)
	}
	return coerceUint32Result(results[0])
}

func (rt *Runtime) allocateLocked(ctx context.Context, size uint32) (uint32, error) {
	ptr, err := rt.executeUint32Locked(ctx, allocExportName, size)
	if err != nil {
		return 0, fmt.Errorf("%s failed: %w", allocExportName, err)
	}
	if ptr == 0 {
		return 0, ErrAllocationFailed
	}
	return ptr, nil
}

func (rt *Runtime) freeLocked(ctx context.Context, ptr uint32, size uint32) {
	if rt == nil || rt.vm == nil || ptr == 0 {
		return
	}
	_, _ = rt.executeWithTimeoutLocked(ctx, freeExportName, ptr, size)
}

func (rt *Runtime) readMemory(ptr uint32, length uint32) ([]byte, error) {
	if rt.memory == nil {
		return nil, ErrNotLoaded
	}
	if length == 0 {
		return []byte{}, nil
	}
	data, err := rt.memory.GetData(uint(ptr), uint(length))
	if err != nil {
		return nil, fmt.Errorf("read guest memory [%d:%d]: %w", ptr, ptr+length, err)
	}
	return bytesClone(data), nil
}

func (rt *Runtime) writeMemory(ptr uint32, data []byte) error {
	if rt.memory == nil {
		return ErrNotLoaded
	}
	if len(data) == 0 {
		return nil
	}
	if err := rt.memory.SetData(data, uint(ptr), uint(len(data))); err != nil {
		return fmt.Errorf("write guest memory at %d: %w", ptr, err)
	}
	return nil
}

func (rt *Runtime) Invoke(
	ctx context.Context,
	methodID string,
	inputs []InvokeInputFrame,
) (*InvokeResult, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	requestBytes, err := encodePluginInvokeRequest(methodID, inputs)
	if err != nil {
		return nil, err
	}

	requestSize := uint32(len(requestBytes))
	if requestSize == 0 {
		requestSize = 1
	}

	requestPtr, err := rt.allocateLocked(ctx, requestSize)
	if err != nil {
		return nil, fmt.Errorf("allocate invoke request: %w", err)
	}
	defer rt.freeLocked(ctx, requestPtr, requestSize)

	responseLenPtr, err := rt.allocateLocked(ctx, 4)
	if err != nil {
		return nil, fmt.Errorf("allocate invoke response length: %w", err)
	}
	defer rt.freeLocked(ctx, responseLenPtr, 4)

	if err := rt.writeMemory(requestPtr, requestBytes); err != nil {
		return nil, err
	}
	if err := rt.writeMemory(responseLenPtr, []byte{0, 0, 0, 0}); err != nil {
		return nil, err
	}

	responsePtr, err := rt.executeUint32Locked(
		ctx,
		invokeExportName,
		requestPtr,
		uint32(len(requestBytes)),
		responseLenPtr,
	)
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", invokeExportName, err)
	}

	responseLenBytes, err := rt.readMemory(responseLenPtr, 4)
	if err != nil {
		return nil, err
	}
	responseLen := binary.LittleEndian.Uint32(responseLenBytes)
	if responseLen == 0 {
		return &InvokeResult{}, nil
	}
	if responsePtr == 0 {
		return nil, fmt.Errorf("%s returned a null pointer for %d response bytes", invokeExportName, responseLen)
	}
	defer rt.freeLocked(ctx, responsePtr, responseLen)

	responseBytes, err := rt.readMemory(responsePtr, responseLen)
	if err != nil {
		return nil, fmt.Errorf("read invoke response: %w", err)
	}
	return decodePluginInvokeResponse(responseBytes)
}

func (rt *Runtime) ConfigureRuntime(
	ctx context.Context,
	request RuntimeConfiguration,
) (*RuntimeState, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal configure_runtime request: %w", err)
	}
	result, err := rt.Invoke(
		ctx,
		"configure_runtime",
		[]InvokeInputFrame{
			{
				PortID:  requestPortID,
				TypeID:  requestPayloadTypeID,
				Payload: payload,
			},
		},
	)
	if err != nil {
		return nil, err
	}
	if err := invokeStatusError("configure_runtime", result); err != nil {
		return nil, err
	}
	frame, err := singleOutputFrame(result, "status")
	if err != nil {
		return nil, err
	}
	var state RuntimeState
	if err := decodeJSONPayload(frame.Payload, &state); err != nil {
		return nil, fmt.Errorf("decode configure_runtime response: %w", err)
	}
	return &state, nil
}

func (rt *Runtime) GetRuntimeState(ctx context.Context) (*RuntimeState, error) {
	result, err := rt.Invoke(ctx, "get_public_key", nil)
	if err != nil {
		return nil, err
	}
	if err := invokeStatusError("get_public_key", result); err != nil {
		return nil, err
	}
	frame, err := singleOutputFrame(result, "public_key")
	if err != nil {
		return nil, err
	}
	var state RuntimeState
	if err := decodeJSONPayload(frame.Payload, &state); err != nil {
		return nil, fmt.Errorf("decode get_public_key response: %w", err)
	}
	return &state, nil
}

// GetPublicKey returns the configured P-256 uncompressed public key bytes.
func (rt *Runtime) GetPublicKey(ctx context.Context) ([]byte, error) {
	state, err := rt.GetRuntimeState(ctx)
	if err != nil {
		return nil, err
	}
	return state.PublicKeyBytes()
}

// HandleRequest processes an OrbPro key-exchange packet through the standalone
// invoke ABI. The hostHeader argument is ignored because domain validation is
// no longer part of the canonical guest contract.
func (rt *Runtime) HandleRequest(ctx context.Context, packet []byte, hostHeader string) ([]byte, int32, error) {
	_ = hostHeader
	result, err := rt.Invoke(
		ctx,
		"handle_key_request",
		[]InvokeInputFrame{
			{
				PortID:  requestPortID,
				TypeID:  requestPacketPayloadTypeID,
				Payload: packet,
			},
		},
	)
	if err != nil {
		return nil, -1, err
	}
	frame, err := singleOutputFrame(result, "response")
	if err != nil {
		if result.StatusCode != 0 {
			return nil, result.StatusCode, nil
		}
		return nil, result.StatusCode, err
	}
	return frame.Payload, result.StatusCode, nil
}

// RequestChallenge asks the guest to issue challenge material for the current
// key version. The payload is JSON bytes wrapped in the canonical RawData frame.
func (rt *Runtime) RequestChallenge(ctx context.Context, requestPayload []byte) ([]byte, int32, error) {
	result, err := rt.Invoke(
		ctx,
		"request_challenge",
		[]InvokeInputFrame{
			{
				PortID:  requestPortID,
				TypeID:  requestPayloadTypeID,
				Payload: requestPayload,
			},
		},
	)
	if err != nil {
		return nil, -1, err
	}
	frame, err := singleOutputFrame(result, "challenge")
	if err != nil {
		if result.StatusCode != 0 {
			return nil, result.StatusCode, nil
		}
		return nil, result.StatusCode, err
	}
	return frame.Payload, result.StatusCode, nil
}

func coerceUint32Result(value interface{}) (uint32, error) {
	switch typed := value.(type) {
	case int32:
		if typed < 0 {
			return 0, fmt.Errorf("unexpected negative i32 result %d", typed)
		}
		return uint32(typed), nil
	case uint32:
		return typed, nil
	case int64:
		if typed < 0 {
			return 0, fmt.Errorf("unexpected negative i64 result %d", typed)
		}
		return uint32(typed), nil
	case uint64:
		return uint32(typed), nil
	case int:
		if typed < 0 {
			return 0, fmt.Errorf("unexpected negative int result %d", typed)
		}
		return uint32(typed), nil
	case uint:
		return uint32(typed), nil
	default:
		return 0, fmt.Errorf("unsupported WasmEdge return type %T", value)
	}
}

func fillRandomBytes(length int) ([]byte, error) {
	if length < 0 {
		return nil, fmt.Errorf("Random byte length must be a non-negative integer.")
	}
	if length == 0 {
		return []byte{}, nil
	}
	out := make([]byte, length)
	if _, err := time.Now().UTC().MarshalBinary(); err != nil {
		return nil, err
	}
	// crypto/rand is the canonical source for the host random capability.
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return out, nil
}

func bytesClone(data []byte) []byte {
	if len(data) == 0 {
		return []byte{}
	}
	return append([]byte(nil), data...)
}

func minUint32(left, right uint32) uint32 {
	if left < right {
		return left
	}
	return right
}
