package flowrt

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	logging "github.com/ipfs/go-log/v2"
	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/flowcc"
	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/wasmrt"
)

var log = logging.Logger("flowrt")

// FlowRuntime hosts a compiled SDN runtime WASM artifact. It binds the flow
// runtime ABI exports and provides a drain loop that dispatches node
// invocations to registered handlers.
type FlowRuntime struct {
	mod *wasmrt.Module
	mu  sync.Mutex

	// store is the per-flow dedicated-thread in-wasm FlatSQL engine, attached
	// when the composed artifact is engine-linked (imports module "flatsql").
	// nil for bridge-mode / non-store flows. Closed (snapshotted) on Release.
	store *LinkedStore

	// Descriptor counts cached at init
	NodeCount    uint32
	EdgeCount    uint32
	TriggerCount uint32
	DepCount     uint32

	// nodeInfo caches each node's STATIC dispatch identity (plugin/method/
	// node/dependency ids + dispatch model — constant data compiled into the
	// artifact) so the drain loop does not re-read descriptor structs and
	// C strings out of linear memory on every dispatch (loop C.5c: those
	// reads were ~40 cgo round-trips per request on an 8-node flow).
	nodeInfo []flowNodeInfo

	// hasDrainLinked reports the artifact exports the C.5c in-wasm scheduler
	// loop (space_data_module_runtime_drain_linked); probed once at load.
	hasDrainLinked bool

	// linkedSection, when set (loop C.7 direct linkage), brackets every
	// in-wasm drain_linked execution: the mount supplies a wrapper that holds
	// the store engine lock for the duration of the linked calls
	// (SQLITE_THREADSAFE=0) and harvests engine body-references before
	// releasing it. run() returns the drain execution error so the wrapper
	// can poison the engine on a trap.
	linkedSection func(run func() error) error
}

// SetLinkedSection installs the linked-drain wrapper (see linkedSection).
func (rt *FlowRuntime) SetLinkedSection(fn func(run func() error) error) {
	rt.linkedSection = fn
}

// flowNodeInfo is the cached static identity of one flow node.
type flowNodeInfo struct {
	PluginID      string
	MethodID      string
	DependencyID  string
	NodeID        string
	DispatchModel string
}

// aotCompileComposedArtifact AOT-compiles the ONE composed flow artifact for the
// node's WasmEdge, caching by content hash under the flowcc home. It REUSES the
// shared EnsureAOTArtifact path (threads-enabled) — never a per-provider-module
// AOT. Returns (aotBytes, true) on success; on any failure it logs LOUDLY and
// returns (nil, false) so the caller interprets the portable bytes.
func aotCompileComposedArtifact(wasm []byte) ([]byte, bool) {
	cacheDir := filepath.Join(flowcc.ResolveHome().CacheDir(), "flow-aot")
	aot, err := flatsqlrt.EnsureAOTArtifact(cacheDir, "flowrt-composed", wasm)
	if err != nil || len(aot) == 0 {
		log.Warnf("Flow runtime: AOT compile of the composed wasi-threads artifact FAILED (%v) — falling back to INTERPRETED, which is ~95x slower and does NOT parallelize threads. This must be fixed for prod.", err)
		return nil, false
	}
	return aot, true
}

// NewFlowRuntime loads a compiled flow WASM artifact and binds the runtime ABI.
// Extra wasmrt options (e.g. additional host import modules such as the
// module-SDK hostcall bridge) are appended after the defaults.
func NewFlowRuntime(wasmBytes []byte, maxMemoryPages uint32, extraOpts ...wasmrt.Option) (*FlowRuntime, error) {
	// Accept publication-protected artifacts directly: the runtime payload is
	// the bytes before any appended SDS $REC trailer (module publication
	// standard). No-op for plain wasm.
	wasmBytes = modulert.StripPublicationTrailer(wasmBytes)
	opts := []wasmrt.Option{
		wasmrt.WithWASI(),
		wasmrt.WithHostModule("sdn", buildFlowHostFuncs("sdn")),
		wasmrt.WithHostModule("env", buildFlowHostFuncs("env")),
	}
	if maxMemoryPages > 0 {
		opts = append(opts, wasmrt.WithMaxMemoryPages(maxMemoryPages))
	}
	// Auto-enable wasi-threads for a baked artifact that declares the isomorphic
	// pthreads contract (shared imported env.memory + wasi.thread-spawn import +
	// wasi_thread_start export, no emscripten hooks). The wasi-threads host merges
	// the shared memory into the flow's "env" host module, so the sdn/env plugin
	// ABI and the shared memory coexist. A single-thread emscripten flow lacks the
	// contract and loads EXACTLY as before (unchanged path).
	var linkedStore *LinkedStore
	if scanWasmThreadFeatures(wasmBytes).isIsomorphicPthreads() {
		opts = append(opts, wasmrt.WithWASIThreads())
		log.Infof("Flow runtime: artifact declares the isomorphic wasi-threads contract — enabling WithWASIThreads (guest pthreads run under WasmEdge)")
		// Engine-linked in-wasm FlatSQL store: the composed flow imports the ONE
		// symbol flatsql.exec_envelope. Attach a dedicated-thread flatsqlrt engine
		// and resolve that import to the exec_envelope host trampoline (proven-safe
		// composition — the engine can NOT join the WithWASIThreads executor). ALL
		// record logic is in-wasm; the host moves opaque bytes + opaque snapshots.
		if wasmImportsModule(wasmBytes, engineImportModule) {
			home := flowcc.ResolveHome()
			storeDir := filepath.Join(home.CacheDir(), "flow-store")
			sum := sha256.Sum256(wasmBytes)
			snap := filepath.Join(storeDir, fmt.Sprintf("%x.snapshot", sum[:16]))
			ls, serr := OpenLinkedStore(filepath.Join(storeDir, "aot"), snap)
			if serr != nil {
				return nil, fmt.Errorf("flowrt: attach in-wasm FlatSQL store: %w", serr)
			}
			linkedStore = ls
			opts = append(opts, wasmrt.WithHostModule(engineImportModule, []wasmrt.HostFunc{ls.ExecEnvelopeHostFunc(), ls.IngestRecordHostFunc()}))
			log.Infof("Flow runtime: engine-linked in-wasm FlatSQL store attached (flatsql.exec_envelope; opaque snapshot %s)", snap)
		}
		// AOT-at-load: a wasi-threads composed artifact runs ~95x slower AND does
		// not parallelize when INTERPRETED (measured); AOT is required for both
		// speed and real thread scaling. Compile once, cache by content hash
		// (sha(wasm)+WasmEdge version); on failure fall back to interpreted with a
		// LOUD warning. Scope: the ONE composed artifact only (C3).
		if aot, ok := aotCompileComposedArtifact(wasmBytes); ok {
			wasmBytes = aot
			log.Infof("Flow runtime: loaded AOT-compiled wasi-threads artifact (native code; threads parallelize)")
		}
	}
	opts = append(opts, extraOpts...)

	mod, err := wasmrt.NewModule(wasmBytes, opts...)
	if err != nil {
		if linkedStore != nil {
			linkedStore.Close()
		}
		return nil, fmt.Errorf("failed to create flow WASM module: %w", err)
	}

	// Call _initialize if present (WASI reactor pattern)
	mod.Execute("_initialize")

	rt := &FlowRuntime{mod: mod, store: linkedStore}

	// Cache descriptor counts
	rt.NodeCount = rt.callUint32(runtimeExportNodeDescriptorCount)
	rt.EdgeCount = rt.callUint32(runtimeExportEdgeDescriptorCount)
	rt.TriggerCount = rt.callUint32(runtimeExportTriggerDescriptorCount)
	rt.DepCount = rt.callUint32(runtimeExportDependencyDescriptorCount)

	// Cache the static per-node dispatch identities once.
	rt.nodeInfo = make([]flowNodeInfo, rt.NodeCount)
	for i := uint32(0); i < rt.NodeCount; i++ {
		if dd, err := rt.GetNodeDispatchDescriptor(i); err == nil {
			rt.nodeInfo[i] = flowNodeInfo{
				PluginID:      rt.readCStringAt(dd.PluginIDPointer),
				MethodID:      rt.readCStringAt(dd.MethodIDPointer),
				DependencyID:  rt.readCStringAt(dd.DependencyIDPointer),
				NodeID:        rt.readCStringAt(dd.NodeIDPointer),
				DispatchModel: rt.readCStringAt(dd.DispatchModelPointer),
			}
		}
	}

	// Probe for the optional in-wasm scheduler loop (0 iterations = no-op).
	if _, err := rt.mod.Execute(runtimeExportDrainLinked, int32(0)); err == nil {
		rt.hasDrainLinked = true
	} else if _, err := rt.mod.Execute(underscoreRuntimeExportName(runtimeExportDrainLinked), int32(0)); err == nil {
		rt.hasDrainLinked = true
	}

	log.Infof("Flow runtime loaded: %d nodes, %d edges, %d triggers, %d deps (in-wasm linked drain: %v)",
		rt.NodeCount, rt.EdgeCount, rt.TriggerCount, rt.DepCount, rt.hasDrainLinked)

	return rt, nil
}

// ThreadPeak / ThreadSpawnCount / WorkerOSThreadIDs expose the wasi-threads host
// counters for the running artifact (0/empty for a single-thread flow). They are
// the live proof that guest pthreads spawn + run under WasmEdge on the node.
func (rt *FlowRuntime) ThreadPeak() int {
	if rt.mod == nil {
		return 0
	}
	return rt.mod.PeakConcurrentThreads()
}

func (rt *FlowRuntime) ThreadSpawnCount() int {
	if rt.mod == nil {
		return 0
	}
	return rt.mod.ThreadSpawnCount()
}

func (rt *FlowRuntime) WorkerOSThreadIDs() []int64 {
	if rt.mod == nil {
		return nil
	}
	return rt.mod.WorkerOSThreadIDs()
}

// Release frees all WasmEdge resources (snapshotting the store first).
func (rt *FlowRuntime) Release() {
	if rt.store != nil {
		rt.store.Close()
		rt.store = nil
	}
	if rt.mod != nil {
		rt.mod.Release()
		rt.mod = nil
	}
}

// SnapshotStore persists the in-wasm FlatSQL store's opaque snapshot (no-op when
// the flow has no linked store). Call after a drain that wrote records.
func (rt *FlowRuntime) SnapshotStore() error {
	if rt.store == nil {
		return nil
	}
	return rt.store.Snapshot()
}

// Module returns the underlying wasmrt.Module for advanced use.
func (rt *FlowRuntime) Module() *wasmrt.Module { return rt.mod }

// ---------------------------------------------------------------------------
// ABI wrappers — all calls are serialized by the caller's mu.Lock
// ---------------------------------------------------------------------------

// callUint32 calls a no-arg export returning a uint32. Returns 0 on error.
func (rt *FlowRuntime) callUint32(name string) uint32 {
	res, err := rt.mod.Execute(name)
	if err != nil {
		res, err = rt.mod.Execute(underscoreRuntimeExportName(name))
		if err != nil {
			return 0
		}
	}
	return uint32(wasmrt.ToInt32(res[0]))
}

// callVoid calls a no-arg void export.
func (rt *FlowRuntime) callVoid(name string) {
	if _, err := rt.mod.Execute(name); err != nil {
		rt.mod.Execute(underscoreRuntimeExportName(name))
	}
}

// readCStringAt reads a null-terminated string from the module's memory.
func (rt *FlowRuntime) readCStringAt(ptr uint32) string {
	if ptr == 0 {
		return ""
	}
	s, _ := rt.mod.ReadCString(ptr, 1024)
	return s
}

// ResetState resets the flow runtime state.
func (rt *FlowRuntime) ResetState() {
	rt.callVoid(runtimeExportResetState)
}

// GetReadyNodeIndex returns the next node index ready for invocation,
// or InvalidIndex if none are ready.
func (rt *FlowRuntime) GetReadyNodeIndex() uint32 {
	return rt.callUint32(runtimeExportReadyNode)
}

// BeginInvocation begins invocation for the given node with a frame budget.
// Returns the number of consumed frames.
func (rt *FlowRuntime) BeginInvocation(nodeIndex uint32, frameBudget int32) int32 {
	res, err := rt.mod.Execute(runtimeExportBeginInvocation, int32(nodeIndex), frameBudget)
	if err != nil {
		res, err = rt.mod.Execute(underscoreRuntimeExportName(runtimeExportBeginInvocation), int32(nodeIndex), frameBudget)
		if err != nil {
			return 0
		}
	}
	return wasmrt.ToInt32(res[0])
}

// GetCurrentInvocationDescriptor reads the current invocation descriptor.
func (rt *FlowRuntime) GetCurrentInvocationDescriptor() (*FlowInvocationDescriptor, error) {
	ptr := rt.callUint32(runtimeExportCurrentInvocation)
	if ptr == 0 || ptr == InvalidIndex {
		return nil, errors.New("no current invocation descriptor")
	}
	return readInvocationDescriptor(rt.mod, ptr)
}

// ApplyInvocationResult applies a handler's result back to the flow runtime.
// Returns the number of routed output frames.
func (rt *FlowRuntime) ApplyInvocationResult(nodeIndex uint32, result *InvocationResult, framesPtr uint32, frameCount uint32) uint32 {
	yielded := int32(0)
	if result.Yielded {
		yielded = 1
	}
	res, err := rt.mod.Execute(runtimeExportApplyInvocationResult,
		int32(nodeIndex),
		result.StatusCode,
		int32(result.BacklogRemaining),
		yielded,
		int32(framesPtr),
		int32(frameCount),
	)
	if err != nil {
		res, err = rt.mod.Execute(underscoreRuntimeExportName(runtimeExportApplyInvocationResult),
			int32(nodeIndex),
			result.StatusCode,
			int32(result.BacklogRemaining),
			yielded,
			int32(framesPtr),
			int32(frameCount),
		)
		if err != nil {
			return 0
		}
	}
	return uint32(wasmrt.ToInt32(res[0]))
}

// CompleteInvocation completes the invocation for a node.
func (rt *FlowRuntime) CompleteInvocation(nodeIndex uint32) {
	rt.mod.Execute(runtimeExportCompleteInvocation, int32(nodeIndex))
}

// EnqueueTrigger enqueues a trigger without frame data.
func (rt *FlowRuntime) EnqueueTrigger(triggerIndex uint32) {
	rt.mod.Execute(runtimeExportEnqueueTriggerFrames, int32(triggerIndex))
}

// EnqueueTriggerFrame enqueues a frame to a trigger's input.
func (rt *FlowRuntime) EnqueueTriggerFrame(triggerIndex uint32, framePtr uint32) {
	rt.mod.Execute(runtimeExportEnqueueTriggerFrame, int32(triggerIndex), int32(framePtr))
}

// GetNodeDispatchDescriptor reads the dispatch descriptor at the given index.
func (rt *FlowRuntime) GetNodeDispatchDescriptor(index uint32) (*FlowNodeDispatchDescriptor, error) {
	basePtr := rt.callUint32(runtimeExportNodeDispatchDescriptors)
	if basePtr == 0 {
		return nil, errors.New("no dispatch descriptors")
	}
	ptr := basePtr + index*flowNodeDispatchDescriptorSize
	return readNodeDispatchDescriptor(rt.mod, ptr)
}

// GetDependencyDescriptor reads the dependency descriptor at the given index.
func (rt *FlowRuntime) GetDependencyDescriptor(index uint32) (*SignedArtifactDependencyDescriptor, error) {
	basePtr := rt.callUint32(runtimeExportDependencyDescriptors)
	if basePtr == 0 {
		return nil, errors.New("no dependency descriptors")
	}
	ptr := basePtr + index*signedArtifactDependencyDescriptorSize
	return readDependencyDescriptor(rt.mod, ptr)
}

// GetNodeRuntimeState reads the runtime state for a node.
func (rt *FlowRuntime) GetNodeRuntimeState(index uint32) (*FlowNodeRuntimeState, error) {
	basePtr := rt.callUint32(runtimeExportNodeStates)
	if basePtr == 0 {
		return nil, errors.New("no node states")
	}
	ptr := basePtr + index*flowNodeRuntimeStateSize
	return readNodeRuntimeState(rt.mod, ptr)
}

// GetIngressRuntimeState reads the runtime state for an ingress.
func (rt *FlowRuntime) GetIngressRuntimeState(index uint32) (*FlowIngressRuntimeState, error) {
	basePtr := rt.callUint32(runtimeExportIngressStates)
	if basePtr == 0 {
		return nil, errors.New("no ingress states")
	}
	ptr := basePtr + index*flowIngressRuntimeStateSize
	return readIngressRuntimeState(rt.mod, ptr)
}

// ---------------------------------------------------------------------------
// Drain loop — the core scheduling loop
// ---------------------------------------------------------------------------

// Drain runs the flow scheduling loop: get ready node → begin invocation →
// dispatch to handler → apply result → repeat until idle or maxIterations.
func (rt *FlowRuntime) Drain(ctx context.Context, handlers HandlerMap, opts DrainOptions) (*DrainResult, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	maxIter := opts.MaxIterations
	if maxIter <= 0 {
		maxIter = 10000 // safety limit
	}

	result := &DrainResult{}

	for i := 0; i < maxIter; i++ {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// In-wasm scheduler loop (loop C.5c): run every ready linked-direct
		// node inside ONE guest call — ready-node selection, dispatch, and
		// frame routing never cross the host boundary. The host loop below
		// then only services host-model nodes (and acts as the fallback for
		// artifacts predating the export). Engine-linked artifacts (loop
		// C.7) run the whole drain inside the mount's linkedSection: the
		// store engine lock is held while direct engine calls execute, and
		// engine body-refs are harvested before it is released.
		if rt.hasDrainLinked {
			runLinked := func() error {
				res, err := rt.mod.Execute(runtimeExportDrainLinked, int32(maxIter))
				if err != nil {
					return err
				}
				if dispatched := wasmrt.ToInt32(res[0]); dispatched > 0 {
					result.NodesInvoked += int(dispatched)
					result.Iterations += int(dispatched)
				}
				return nil
			}
			if rt.linkedSection != nil {
				if err := rt.linkedSection(runLinked); err != nil {
					return result, fmt.Errorf("linked drain: %w", err)
				}
			} else {
				_ = runLinked() // best-effort, host loop below is the fallback
			}
		}

		nodeIndex := rt.GetReadyNodeIndex()
		if nodeIndex == InvalidIndex {
			break // no more ready nodes
		}

		result.Iterations++

		// Begin invocation with default frame budget
		consumed := rt.BeginInvocation(nodeIndex, 64)
		if consumed < 0 {
			log.Warnf("BeginInvocation(%d) returned %d", nodeIndex, consumed)
			rt.CompleteInvocation(nodeIndex)
			continue
		}

		// The node's dispatch identity is static per artifact — served from
		// the init-time cache instead of re-reading descriptor structs and C
		// strings out of linear memory on every dispatch (loop C.5c).
		var info flowNodeInfo
		if nodeIndex < uint32(len(rt.nodeInfo)) {
			info = rt.nodeInfo[nodeIndex]
		}
		pluginID := info.PluginID
		methodID := info.MethodID
		dependencyID := info.DependencyID
		nodeID := info.NodeID

		// Resolve handler BEFORE touching descriptor/input frames:
		// linked-direct nodes consume their (possibly multi-megabyte) frames
		// entirely inside the artifact's linear memory — copying them out
		// here just to discard them was a per-dispatch stream-sized copy.
		handler := handlers.Resolve(pluginID, methodID, dependencyID, nodeID)
		if handler == nil {
			log.Debugf("No handler for %s:%s (node=%s, dep=%s)", pluginID, methodID, nodeID, dependencyID)
			result.HandlersSkipped++

			if info.DispatchModel == "linked-direct" {
				rt.dispatchDirect(nodeIndex)
				result.NodesInvoked++
				continue
			}

			rt.CompleteInvocation(nodeIndex)
			continue
		}

		// Host-model dispatch: read the invocation descriptor + input frames.
		invDesc, err := rt.GetCurrentInvocationDescriptor()
		if err != nil {
			log.Warnf("GetCurrentInvocationDescriptor: %v", err)
			rt.CompleteInvocation(nodeIndex)
			continue
		}
		frames, err := rt.readInputFrames(invDesc)
		if err != nil {
			log.Warnf("readInputFrames: %v", err)
			rt.CompleteInvocation(nodeIndex)
			continue
		}

		// Invoke handler
		args := &InvocationArgs{
			NodeIndex:    nodeIndex,
			PluginID:     pluginID,
			MethodID:     methodID,
			DependencyID: dependencyID,
			NodeID:       nodeID,
			Frames:       frames,
		}

		handlerResult, err := handler(ctx, args)
		if err != nil {
			log.Warnf("Handler %s:%s error: %v", pluginID, methodID, err)
			handlerResult = &InvocationResult{StatusCode: -1}
		}

		// Write output frames and apply result
		framesPtr, frameCount := rt.writeOutputFrames(handlerResult.Outputs)
		rt.ApplyInvocationResult(nodeIndex, handlerResult, framesPtr, frameCount)
		rt.CompleteInvocation(nodeIndex)
		result.NodesInvoked++
	}

	return result, nil
}

// DrainOnce runs a single drain pass (convenience for trigger handlers).
func (rt *FlowRuntime) DrainOnce(ctx context.Context, handlers HandlerMap) (*DrainResult, error) {
	return rt.Drain(ctx, handlers, DrainOptions{MaxIterations: 1000})
}

// readInputFrames reads the input frames from an invocation descriptor.
func (rt *FlowRuntime) readInputFrames(inv *FlowInvocationDescriptor) ([]FrameData, error) {
	if inv.FrameCount == 0 || inv.FramesPointer == 0 {
		return nil, nil
	}

	frames := make([]FrameData, 0, inv.FrameCount)
	for i := uint32(0); i < inv.FrameCount; i++ {
		ptr := inv.FramesPointer + i*flowFrameDescriptorSize
		fd, err := readFrameDescriptor(rt.mod, ptr)
		if err != nil {
			return frames, err
		}
		if !fd.Occupied {
			continue
		}

		portID := rt.readCStringAt(fd.PortIDPointer)

		var payload []byte
		if fd.Size > 0 && fd.Offset > 0 {
			payload, err = rt.mod.ReadMemory(fd.Offset, fd.Size)
			if err != nil {
				return frames, fmt.Errorf("read frame payload at %d len %d: %w", fd.Offset, fd.Size, err)
			}
		}

		frames = append(frames, FrameData{
			PortID:      portID,
			Bytes:       payload,
			StreamID:    fd.StreamID,
			Sequence:    fd.Sequence,
			EndOfStream: fd.EndOfStream,
		})
	}
	return frames, nil
}

// writeOutputFrames allocates output frame descriptors in WASM memory.
// Returns the pointer to the frame array and the count.
func (rt *FlowRuntime) writeOutputFrames(outputs []FrameOutput) (uint32, uint32) {
	if len(outputs) == 0 {
		return 0, 0
	}

	count := uint32(len(outputs))
	totalSize := count * flowFrameDescriptorSize
	arrPtr, err := rt.mod.AllocateSize(totalSize)
	if err != nil {
		log.Warnf("Failed to allocate output frames: %v", err)
		return 0, 0
	}

	for i, out := range outputs {
		// Allocate payload in WASM memory
		var payloadOffset uint32
		if len(out.Bytes) > 0 {
			payloadOffset, err = rt.mod.Allocate(out.Bytes)
			if err != nil {
				log.Warnf("Failed to allocate output payload: %v", err)
				continue
			}
		}

		// Allocate portID string
		var portIDPtr uint32
		if out.PortID != "" {
			portIDPtr, err = rt.mod.AllocateString(out.PortID)
			if err != nil {
				log.Warnf("Failed to allocate portID: %v", err)
				continue
			}
		}

		fd := &FlowFrameDescriptor{
			PortIDPointer: portIDPtr,
			Offset:        payloadOffset,
			Size:          uint32(len(out.Bytes)),
			StreamID:      out.StreamID,
			Sequence:      out.Sequence,
			EndOfStream:   out.EndOfStream,
			Occupied:      true,
		}

		framePtr := arrPtr + uint32(i)*flowFrameDescriptorSize
		if err := writeFrameDescriptor(rt.mod, framePtr, fd); err != nil {
			log.Warnf("Failed to write output frame descriptor: %v", err)
		}
	}

	return arrPtr, count
}

// dispatchDirect calls the linked-direct dispatch for the current invocation.
func (rt *FlowRuntime) dispatchDirect(nodeIndex uint32) {
	res, err := rt.mod.Execute(runtimeExportDispatchCurrentInvocation, int32(64))
	if err != nil {
		rt.mod.Execute(underscoreRuntimeExportName(runtimeExportDispatchCurrentInvocation), int32(64))
	}
	_ = res
	rt.CompleteInvocation(nodeIndex)
}
