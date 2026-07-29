package flowrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	logging "github.com/ipfs/go-log/v2"
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

	// edgeInfo is the exact signed type/layout table exported by the parent
	// runtime. Host-model dispatch uses it to preserve schema identity across
	// independently instantiated child WASM memories and to bind every output
	// descriptor back to a signed graph edge.
	edgeInfo []flowEdgeInfo

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

type flowEdgeInfo struct {
	Index                      uint32
	Descriptor                 FlowEdgeDescriptor
	FromPort                   string
	ToPort                     string
	SchemaName                 string
	FileIdentifier             string
	SchemaVersion              string
	SchemaHash                 []byte
	RootTypeName               string
	CanonicalFallbackAvailable bool
	AlignedEligible            bool
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
	if scanWasmThreadFeatures(wasmBytes).isIsomorphicPthreads() {
		opts = append(opts, wasmrt.WithWASIThreads())
		log.Infof("Flow runtime: artifact declares the isomorphic wasi-threads contract — enabling WithWASIThreads (guest pthreads run under WasmEdge)")
	}
	opts = append(opts, extraOpts...)

	mod, err := wasmrt.NewModule(wasmBytes, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create flow WASM module: %w", err)
	}

	// Run a declared initializer before reading any runtime descriptors.
	switch {
	case mod.HasFunction("_initialize"):
		if _, initErr := mod.Execute("_initialize"); initErr != nil {
			mod.Release()
			return nil, fmt.Errorf("flowrt: run WASI reactor initialization: %w", initErr)
		}
	case mod.HasFunction("__wasm_call_ctors"):
		if _, initErr := mod.Execute("__wasm_call_ctors"); initErr != nil {
			mod.Release()
			return nil, fmt.Errorf("flowrt: run command module constructors: %w", initErr)
		}
	}

	rt := &FlowRuntime{mod: mod}

	// Cache descriptor counts, failing load when the required runtime ABI is
	// absent or traps. Zero is a valid count and must not mask an export error.
	counts := []struct {
		name string
		dst  *uint32
	}{
		{runtimeExportNodeDescriptorCount, &rt.NodeCount},
		{runtimeExportEdgeDescriptorCount, &rt.EdgeCount},
		{runtimeExportTriggerDescriptorCount, &rt.TriggerCount},
		{runtimeExportDependencyDescriptorCount, &rt.DepCount},
	}
	for _, count := range counts {
		value, countErr := rt.executeUint32(count.name)
		if countErr != nil {
			rt.Release()
			return nil, fmt.Errorf("flowrt: read descriptor count %s: %w", count.name, countErr)
		}
		*count.dst = value
	}

	if err := rt.cacheFlowEdges(); err != nil {
		rt.Release()
		return nil, fmt.Errorf("flowrt: read signed edge descriptors: %w", err)
	}

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

	// Select optional ABI variants by export presence only. Never probe a
	// stateful export by executing it: a guest can mutate and then trap.
	rt.hasDrainLinked = rt.mod.HasFunction(runtimeExportDrainLinked) ||
		rt.mod.HasFunction(underscoreRuntimeExportName(runtimeExportDrainLinked))

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

// Release frees all WasmEdge resources.
func (rt *FlowRuntime) Release() {
	if rt.mod != nil {
		rt.mod.Release()
		rt.mod = nil
	}
}

// Module returns the underlying wasmrt.Module for advanced use.
func (rt *FlowRuntime) Module() *wasmrt.Module { return rt.mod }

// ---------------------------------------------------------------------------
// ABI wrappers — all calls are serialized by the caller's mu.Lock
// ---------------------------------------------------------------------------

type runtimeExecuteFunc func(name string, params ...interface{}) ([]interface{}, error)
type runtimeHasFunction func(name string) bool

func executeRuntimeExport(hasFunction runtimeHasFunction, execute runtimeExecuteFunc, name string, params ...interface{}) ([]interface{}, error) {
	if hasFunction == nil || execute == nil {
		return nil, errors.New("runtime export resolver is unavailable")
	}
	selected := name
	if !hasFunction(selected) {
		selected = underscoreRuntimeExportName(name)
		if !hasFunction(selected) {
			return nil, fmt.Errorf("required runtime export %q is missing (including underscore variant)", name)
		}
	}
	res, err := execute(selected, params...)
	if err != nil {
		return nil, fmt.Errorf("execute runtime export %q: %w", selected, err)
	}
	return res, nil
}

// executeUint32 calls a required no-arg export returning a uint32.
func (rt *FlowRuntime) executeUint32(name string) (uint32, error) {
	res, err := executeRuntimeExport(rt.mod.HasFunction, rt.mod.Execute, name)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("export %q returned no values", name)
	}
	return uint32(wasmrt.ToInt32(res[0])), nil
}

// callVoid calls a no-arg void export.
func (rt *FlowRuntime) callVoid(name string) {
	_, _ = executeRuntimeExport(rt.mod.HasFunction, rt.mod.Execute, name)
}

// readCStringAt reads a null-terminated string from the module's memory.
func (rt *FlowRuntime) readCStringAt(ptr uint32) string {
	if ptr == 0 {
		return ""
	}
	s, _ := rt.mod.ReadCString(ptr, 1024)
	return s
}

func (rt *FlowRuntime) readRequiredCString(ptr uint32, field string) (string, error) {
	if ptr == 0 {
		return "", fmt.Errorf("%s pointer is null", field)
	}
	value, err := rt.mod.ReadCString(ptr, 4096)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", field, err)
	}
	if value == "" {
		return "", fmt.Errorf("%s is empty", field)
	}
	return value, nil
}

// assertDescriptorABIGeneration refuses an artifact whose descriptor table is
// not the generation this package decodes.
//
// It must run BEFORE the first edge is read, because a stride mismatch is
// silent: the table pointer and the edge count are identical across
// generations, so every check inside the read loop (node ranges, flag domains,
// pointer/size sanity) is applied to fields decoded at the wrong offsets. The
// loop would either accept nonsense or reject a perfectly good artifact for the
// wrong reason.
//
// A missing export is generation 1, not a waiver — every generation-1 bundle
// predates the export, so treating absence as "close enough" would admit
// exactly the artifacts this gate exists to refuse. The SDK's JS host applies
// the same rule; a Go host that read on regardless would misroute where the
// browser fails closed, which is the tri-runtime divergence class that produced
// the alignment=0 defect.
func (rt *FlowRuntime) assertDescriptorABIGeneration() error {
	generation := uint32(1)
	if rt.mod.HasFunction(runtimeExportDescriptorABIGeneration) ||
		rt.mod.HasFunction(underscoreRuntimeExportName(runtimeExportDescriptorABIGeneration)) {
		reported, err := rt.executeUint32(runtimeExportDescriptorABIGeneration)
		if err != nil {
			return fmt.Errorf("read descriptor ABI generation: %w", err)
		}
		generation = reported
	}
	return checkDescriptorABIGeneration(generation)
}

// checkDescriptorABIGeneration is the decision, split from the module call so
// it can be pinned without a compiled artifact.
func checkDescriptorABIGeneration(generation uint32) error {
	if generation != flowEdgeDescriptorABIGeneration {
		return fmt.Errorf(
			"flow artifact declares descriptor ABI generation %d, this host decodes generation %d (%d-byte FlowEdge) — refusing to read the edge table rather than misread it; rebuild the bundle against the current SDK",
			generation, flowEdgeDescriptorABIGeneration, flowEdgeDescriptorSize)
	}
	return nil
}

func (rt *FlowRuntime) cacheFlowEdges() error {
	if rt.EdgeCount == 0 {
		rt.edgeInfo = nil
		return nil
	}
	if err := rt.assertDescriptorABIGeneration(); err != nil {
		return err
	}
	basePtr, err := rt.executeUint32(runtimeExportEdgeDescriptors)
	if err != nil {
		return err
	}
	if basePtr == 0 || uint64(basePtr)+uint64(rt.EdgeCount)*flowEdgeDescriptorSize > uint64(^uint32(0)) {
		return fmt.Errorf("invalid edge descriptor table pointer/count %d/%d", basePtr, rt.EdgeCount)
	}
	rt.edgeInfo = make([]flowEdgeInfo, 0, rt.EdgeCount)
	for index := uint32(0); index < rt.EdgeCount; index++ {
		descriptor, readErr := readFlowEdgeDescriptor(rt.mod, basePtr+index*flowEdgeDescriptorSize)
		if readErr != nil {
			return fmt.Errorf("edge %d: %w", index, readErr)
		}
		if descriptor.FromNode >= rt.NodeCount || descriptor.ToNode >= rt.NodeCount {
			return fmt.Errorf("edge %d has out-of-range nodes %d -> %d", index, descriptor.FromNode, descriptor.ToNode)
		}
		if descriptor.CanonicalFallbackAvailable > 1 || descriptor.AlignedEligible > 1 {
			return fmt.Errorf("edge %d has invalid representation flags canonical=%d aligned=%d", index, descriptor.CanonicalFallbackAvailable, descriptor.AlignedEligible)
		}
		if descriptor.SchemaHashSize == 0 || descriptor.SchemaHashSize > 4096 || descriptor.SchemaHashPointer == 0 {
			return fmt.Errorf("edge %d has invalid schema hash pointer/size %d/%d", index, descriptor.SchemaHashPointer, descriptor.SchemaHashSize)
		}
		if descriptor.AlignedEligible != 0 {
			if descriptor.AlignedByteLength == 0 || !isPowerOfTwo(descriptor.AlignedRequiredAlignment) ||
				descriptor.AlignedFixedStringLength > uint32(^uint16(0)) {
				return fmt.Errorf("edge %d has invalid aligned layout byteLength=%d fixedStringLength=%d requiredAlignment=%d", index, descriptor.AlignedByteLength, descriptor.AlignedFixedStringLength, descriptor.AlignedRequiredAlignment)
			}
		}
		fromPort, readErr := rt.readRequiredCString(descriptor.FromPortPointer, fmt.Sprintf("edge %d from port", index))
		if readErr != nil {
			return readErr
		}
		toPort, readErr := rt.readRequiredCString(descriptor.ToPortPointer, fmt.Sprintf("edge %d to port", index))
		if readErr != nil {
			return readErr
		}
		schemaName, readErr := rt.readRequiredCString(descriptor.SchemaNamePointer, fmt.Sprintf("edge %d schema name", index))
		if readErr != nil {
			return readErr
		}
		fileIdentifier, readErr := rt.readRequiredCString(descriptor.FileIdentifierPointer, fmt.Sprintf("edge %d file identifier", index))
		if readErr != nil {
			return readErr
		}
		schemaVersion, readErr := rt.readRequiredCString(descriptor.SchemaVersionPointer, fmt.Sprintf("edge %d schema version", index))
		if readErr != nil {
			return readErr
		}
		rootTypeName, readErr := rt.readRequiredCString(descriptor.RootTypeNamePointer, fmt.Sprintf("edge %d root type", index))
		if readErr != nil {
			return readErr
		}
		schemaHash, readErr := rt.mod.ReadMemory(descriptor.SchemaHashPointer, descriptor.SchemaHashSize)
		if readErr != nil {
			return fmt.Errorf("edge %d schema hash: %w", index, readErr)
		}
		rt.edgeInfo = append(rt.edgeInfo, flowEdgeInfo{
			Index:                      index,
			Descriptor:                 *descriptor,
			FromPort:                   fromPort,
			ToPort:                     toPort,
			SchemaName:                 schemaName,
			FileIdentifier:             fileIdentifier,
			SchemaVersion:              schemaVersion,
			SchemaHash:                 append([]byte(nil), schemaHash...),
			RootTypeName:               rootTypeName,
			CanonicalFallbackAvailable: descriptor.CanonicalFallbackAvailable != 0,
			AlignedEligible:            descriptor.AlignedEligible != 0,
		})
	}
	return nil
}

func isPowerOfTwo(value uint32) bool {
	return value != 0 && value&(value-1) == 0
}

// ResetState resets the flow runtime state.
func (rt *FlowRuntime) ResetState() {
	rt.callVoid(runtimeExportResetState)
}

// GetReadyNodeIndex returns the next node index ready for invocation,
// or InvalidIndex if none are ready.
func (rt *FlowRuntime) GetReadyNodeIndex() (uint32, error) {
	return rt.executeUint32(runtimeExportReadyNode)
}

// BeginInvocation begins invocation for the given node with a frame budget.
// Returns the number of consumed frames.
func (rt *FlowRuntime) BeginInvocation(nodeIndex uint32, frameBudget int32) int32 {
	consumed, err := rt.beginInvocationChecked(nodeIndex, frameBudget)
	if err != nil {
		return -1
	}
	return consumed
}

func (rt *FlowRuntime) beginInvocationChecked(nodeIndex uint32, frameBudget int32) (int32, error) {
	res, err := executeRuntimeExport(rt.mod.HasFunction, rt.mod.Execute, runtimeExportBeginInvocation, int32(nodeIndex), frameBudget)
	if err != nil {
		return 0, fmt.Errorf("begin invocation for node %d: %w", nodeIndex, err)
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("begin invocation for node %d returned no values", nodeIndex)
	}
	return wasmrt.ToInt32(res[0]), nil
}

// GetCurrentInvocationDescriptor reads the current invocation descriptor.
func (rt *FlowRuntime) GetCurrentInvocationDescriptor() (*FlowInvocationDescriptor, error) {
	ptr, err := rt.executeUint32(runtimeExportCurrentInvocation)
	if err != nil {
		return nil, err
	}
	if ptr == 0 || ptr == InvalidIndex {
		return nil, errors.New("no current invocation descriptor")
	}
	return readInvocationDescriptor(rt.mod, ptr)
}

// ApplyInvocationResult applies a handler's result back to the flow runtime.
// Returns the number of routed output frames.
func (rt *FlowRuntime) ApplyInvocationResult(nodeIndex uint32, result *InvocationResult, framesPtr uint32, frameCount uint32) (uint32, error) {
	if result == nil {
		return 0, errors.New("invocation result is nil")
	}
	yielded := int32(0)
	if result.Yielded {
		yielded = 1
	}
	res, err := executeRuntimeExport(rt.mod.HasFunction, rt.mod.Execute, runtimeExportApplyInvocationResult,
		int32(nodeIndex),
		result.StatusCode,
		int32(result.BacklogRemaining),
		yielded,
		int32(framesPtr),
		int32(frameCount),
	)
	if err != nil {
		return 0, fmt.Errorf("apply invocation result for node %d: %w", nodeIndex, err)
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("apply invocation result for node %d returned no values", nodeIndex)
	}
	return decodeRoutingResult(wasmrt.ToInt32(res[0]))
}

// CompleteInvocation completes the invocation for a node.
func (rt *FlowRuntime) CompleteInvocation(nodeIndex uint32) {
	_ = rt.completeInvocationChecked(nodeIndex)
}

func (rt *FlowRuntime) completeInvocationChecked(nodeIndex uint32) error {
	if _, err := executeRuntimeExport(rt.mod.HasFunction, rt.mod.Execute, runtimeExportCompleteInvocation, int32(nodeIndex)); err != nil {
		return fmt.Errorf("complete invocation for node %d: %w", nodeIndex, err)
	}
	return nil
}

// EnqueueTrigger enqueues a trigger without frame data.
func (rt *FlowRuntime) EnqueueTrigger(triggerIndex uint32) {
	_ = rt.EnqueueTriggerChecked(triggerIndex)
}

// EnqueueTriggerChecked enqueues a generic startup/wakeup trigger and surfaces
// runtime execution failures to lifecycle callers that must fail observably.
func (rt *FlowRuntime) EnqueueTriggerChecked(triggerIndex uint32) error {
	if _, err := executeRuntimeExport(rt.mod.HasFunction, rt.mod.Execute, runtimeExportEnqueueTriggerFrames, int32(triggerIndex)); err != nil {
		return fmt.Errorf("enqueue trigger %d: %w", triggerIndex, err)
	}
	return nil
}

// EnqueueTriggerFrame enqueues a frame to a trigger's input and surfaces any
// parent-runtime rejection. The compiled runtime copies accepted frame bytes.
func (rt *FlowRuntime) EnqueueTriggerFrame(triggerIndex uint32, framePtr uint32) error {
	if _, err := executeRuntimeExport(rt.mod.HasFunction, rt.mod.Execute, runtimeExportEnqueueTriggerFrame, int32(triggerIndex), int32(framePtr)); err != nil {
		return fmt.Errorf("enqueue trigger %d frame at %d: %w", triggerIndex, framePtr, err)
	}
	return nil
}

// GetNodeDispatchDescriptor reads the dispatch descriptor at the given index.
func (rt *FlowRuntime) GetNodeDispatchDescriptor(index uint32) (*FlowNodeDispatchDescriptor, error) {
	basePtr, err := rt.executeUint32(runtimeExportNodeDispatchDescriptors)
	if err != nil {
		return nil, err
	}
	if basePtr == 0 {
		return nil, errors.New("no dispatch descriptors")
	}
	ptr := basePtr + index*flowNodeDispatchDescriptorSize
	return readNodeDispatchDescriptor(rt.mod, ptr)
}

// GetDependencyDescriptor reads the dependency descriptor at the given index.
func (rt *FlowRuntime) GetDependencyDescriptor(index uint32) (*SignedArtifactDependencyDescriptor, error) {
	basePtr, err := rt.executeUint32(runtimeExportDependencyDescriptors)
	if err != nil {
		return nil, err
	}
	if basePtr == 0 {
		return nil, errors.New("no dependency descriptors")
	}
	ptr := basePtr + index*signedArtifactDependencyDescriptorSize
	return readDependencyDescriptor(rt.mod, ptr)
}

// GetNodeRuntimeState reads the runtime state for a node.
func (rt *FlowRuntime) GetNodeRuntimeState(index uint32) (*FlowNodeRuntimeState, error) {
	basePtr, err := rt.executeUint32(runtimeExportNodeStates)
	if err != nil {
		return nil, err
	}
	if basePtr == 0 {
		return nil, errors.New("no node states")
	}
	ptr := basePtr + index*flowNodeRuntimeStateSize
	return readNodeRuntimeState(rt.mod, ptr)
}

// GetIngressRuntimeState reads the runtime state for an ingress.
func (rt *FlowRuntime) GetIngressRuntimeState(index uint32) (*FlowIngressRuntimeState, error) {
	basePtr, err := rt.executeUint32(runtimeExportIngressStates)
	if err != nil {
		return nil, err
	}
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
	quiescent := false

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
				res, err := executeRuntimeExport(rt.mod.HasFunction, rt.mod.Execute, runtimeExportDrainLinked, int32(maxIter))
				if err != nil {
					return err
				}
				if len(res) == 0 {
					return errors.New("linked drain returned no values")
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
				if err := runLinked(); err != nil {
					return result, fmt.Errorf("linked drain: %w", err)
				}
			}
		}

		nodeIndex, readyErr := rt.GetReadyNodeIndex()
		if readyErr != nil {
			return result, fmt.Errorf("flowrt: read ready node: %w", readyErr)
		}
		if nodeIndex == InvalidIndex {
			quiescent = true
			break // no more ready nodes
		}

		result.Iterations++

		// Begin invocation with default frame budget
		consumed, beginErr := rt.beginInvocationChecked(nodeIndex, 64)
		if beginErr != nil {
			return result, fmt.Errorf("flowrt: begin node %d invocation: %w", nodeIndex, beginErr)
		}
		if consumed < 0 {
			beginErr := fmt.Errorf("flowrt: begin node %d invocation rejected with status %d", nodeIndex, consumed)
			return result, errors.Join(beginErr, rt.completeInvocationChecked(nodeIndex))
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
		handler, direct, handlerResolveErr := resolveDrainHandler(handlers, info)
		if handlerResolveErr != nil {
			result.HandlersSkipped++
			return result, errors.Join(handlerResolveErr, rt.completeInvocationChecked(nodeIndex))
		}
		if direct {
			if err := rt.dispatchDirect(nodeIndex); err != nil {
				return result, err
			}
			result.NodesInvoked++
			continue
		}

		// Host-model dispatch: read the invocation descriptor + input frames.
		invDesc, err := rt.GetCurrentInvocationDescriptor()
		if err != nil {
			return result, errors.Join(
				fmt.Errorf("flowrt: read current invocation descriptor for node %d: %w", nodeIndex, err),
				rt.completeInvocationChecked(nodeIndex),
			)
		}
		frames, err := rt.readInputFrames(invDesc)
		if err != nil {
			return result, errors.Join(
				fmt.Errorf("flowrt: read input frames for node %d: %w", nodeIndex, err),
				rt.completeInvocationChecked(nodeIndex),
			)
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

		handlerResult, handlerErr := handler(ctx, args)
		handlerResult, err = validateInvocationHandlerResult(nodeIndex, pluginID, methodID, handlerResult, handlerErr)
		if err != nil {
			return result, errors.Join(err, rt.completeInvocationChecked(nodeIndex))
		}

		// Write output frames and apply result
		framesPtr, frameCount, lease, writeErr := rt.writeOutputFrames(nodeIndex, handlerResult.Outputs)
		if writeErr != nil {
			return result, errors.Join(
				fmt.Errorf("flowrt: prepare outputs for node %d: %w", nodeIndex, writeErr),
				rt.completeInvocationChecked(nodeIndex),
			)
		}
		_, applyErr := rt.ApplyInvocationResult(nodeIndex, handlerResult, framesPtr, frameCount)
		if lease != nil {
			lease.Release()
		}
		completeErr := rt.completeInvocationChecked(nodeIndex)
		if applyErr != nil {
			return result, errors.Join(fmt.Errorf("flowrt: route outputs for node %d: %w", nodeIndex, applyErr), completeErr)
		}
		if completeErr != nil {
			return result, completeErr
		}
		result.NodesInvoked++
	}
	if !quiescent {
		nodeIndex, readyErr := rt.GetReadyNodeIndex()
		if readyErr != nil {
			return result, fmt.Errorf("flowrt: verify drain quiescence: %w", readyErr)
		}
		if nodeIndex != InvalidIndex {
			return result, fmt.Errorf("flowrt: drain did not quiesce within %d iterations (node %d remains ready)", maxIter, nodeIndex)
		}
	}

	return result, nil
}

func validateInvocationHandlerResult(nodeIndex uint32, pluginID, methodID string, handlerResult *InvocationResult, handlerErr error) (*InvocationResult, error) {
	if handlerErr != nil {
		return nil, fmt.Errorf("flowrt: handler %s:%s for node %d failed: %w", pluginID, methodID, nodeIndex, handlerErr)
	}
	if handlerResult == nil {
		return nil, fmt.Errorf("flowrt: handler %s:%s for node %d returned a nil result", pluginID, methodID, nodeIndex)
	}
	if handlerResult.StatusCode != 0 {
		return nil, fmt.Errorf("flowrt: handler %s:%s for node %d returned status %d", pluginID, methodID, nodeIndex, handlerResult.StatusCode)
	}
	return handlerResult, nil
}

func resolveDrainHandler(handlers HandlerMap, info flowNodeInfo) (Handler, bool, error) {
	handler := handlers.Resolve(info.PluginID, info.MethodID, info.DependencyID, info.NodeID)
	if handler != nil {
		return handler, false, nil
	}
	if info.DispatchModel == "linked-direct" {
		return nil, true, nil
	}
	return nil, false, fmt.Errorf(
		"flowrt: no handler for plugin=%q method=%q node=%q dependency=%q dispatch=%q",
		info.PluginID, info.MethodID, info.NodeID, info.DependencyID, info.DispatchModel,
	)
}

// DrainOnce runs a single drain pass (convenience for trigger handlers).
func (rt *FlowRuntime) DrainOnce(ctx context.Context, handlers HandlerMap) (*DrainResult, error) {
	return rt.Drain(ctx, handlers, DrainOptions{MaxIterations: 100000})
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
		if !isPowerOfTwo(fd.Alignment) {
			return frames, fmt.Errorf("input frame %d has invalid alignment %d", i, fd.Alignment)
		}
		if fd.WireFormat > 1 || fd.Ownership > 2 || fd.Mutability > 2 {
			return frames, fmt.Errorf("input frame %d has invalid wire/ownership/mutability %d/%d/%d", i, fd.WireFormat, fd.Ownership, fd.Mutability)
		}
		if fd.Mutability != 0 && fd.Ownership != 2 {
			return frames, fmt.Errorf("input frame %d mutable storage is not transferred", i)
		}
		if fd.Lifetime != 1 {
			return frames, fmt.Errorf("input frame %d has stale/unknown lifetime %d", i, fd.Lifetime)
		}

		portID := rt.readCStringAt(fd.PortIDPointer)
		if portID == "" {
			return frames, fmt.Errorf("input frame %d has an empty port id", i)
		}

		var payload []byte
		if fd.Size > 0 {
			if fd.Offset == 0 || uint64(fd.Offset)+uint64(fd.Size) > uint64(^uint32(0)) {
				return frames, fmt.Errorf("input frame %d has invalid payload range %d+%d", i, fd.Offset, fd.Size)
			}
			payload, err = rt.mod.ReadMemory(fd.Offset, fd.Size)
			if err != nil {
				return frames, fmt.Errorf("read frame payload at %d len %d: %w", fd.Offset, fd.Size, err)
			}
		}

		frame := FrameData{
			PortID:            portID,
			Bytes:             payload,
			TypeDescriptorIdx: fd.TypeDescriptorIdx,
			WireFormat:        fd.WireFormat,
			Alignment:         fd.Alignment,
			Ownership:         fd.Ownership,
			Mutability:        fd.Mutability,
			Lifetime:          fd.Lifetime,
			FrameID:           fd.TraceToken,
			StreamID:          fd.StreamID,
			Sequence:          fd.Sequence,
			EndOfStream:       fd.EndOfStream,
		}
		if err := bindInputFrameType(&frame, fd, inv.NodeIndex, rt.edgeInfo); err != nil {
			return frames, fmt.Errorf("input frame %d: %w", i, err)
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

func bindInputFrameType(frame *FrameData, fd *FlowFrameDescriptor, nodeIndex uint32, edges []flowEdgeInfo) error {
	if frame == nil || fd == nil {
		return errors.New("frame and descriptor are required")
	}
	if fd.TypeDescriptorIdx >= uint32(len(edges)) {
		return fmt.Errorf("canonical/aligned frame has no signed type descriptor (index %d, count %d)", fd.TypeDescriptorIdx, len(edges))
	}
	edge := edges[fd.TypeDescriptorIdx]
	if edge.Descriptor.ToNode != nodeIndex || edge.ToPort != frame.PortID {
		return fmt.Errorf("type descriptor %d is not bound to node %d port %q", fd.TypeDescriptorIdx, nodeIndex, frame.PortID)
	}
	if fd.WireFormat == 0 && !edge.CanonicalFallbackAvailable {
		return fmt.Errorf("canonical fallback is unavailable on descriptor %d", edge.Index)
	}
	if fd.WireFormat == 1 && (!edge.AlignedEligible || fd.Size != edge.Descriptor.AlignedByteLength ||
		fd.Alignment < edge.Descriptor.AlignedRequiredAlignment ||
		fd.Offset%edge.Descriptor.AlignedRequiredAlignment != 0) {
		return fmt.Errorf("aligned layout violates descriptor %d", edge.Index)
	}
	frame.SchemaName = edge.SchemaName
	frame.FileIdentifier = edge.FileIdentifier
	frame.SchemaVersion = edge.SchemaVersion
	frame.SchemaHash = append([]byte(nil), edge.SchemaHash...)
	frame.RootTypeName = edge.RootTypeName
	frame.FixedStringLength = edge.Descriptor.AlignedFixedStringLength
	frame.ByteLength = edge.Descriptor.AlignedByteLength
	frame.RequiredAlignment = edge.Descriptor.AlignedRequiredAlignment
	return nil
}

type outputFrameAllocation struct {
	ptr  uint32
	size uint32
}

type outputFrameLease struct {
	mod         *wasmrt.Module
	deallocate  func(uint32, uint32)
	allocations []outputFrameAllocation
	released    bool
}

func (lease *outputFrameLease) track(ptr, size uint32) {
	if ptr != 0 && size != 0 {
		lease.allocations = append(lease.allocations, outputFrameAllocation{ptr: ptr, size: size})
	}
}

// Release frees every transient parent-memory allocation after the compiled
// router has copied/retained its output frames. Reverse order keeps the frame
// descriptor array alive until all payload/port allocations are released.
func (lease *outputFrameLease) Release() {
	if lease == nil || lease.released || (lease.mod == nil && lease.deallocate == nil) {
		return
	}
	for index := len(lease.allocations) - 1; index >= 0; index-- {
		allocation := lease.allocations[index]
		if lease.deallocate != nil {
			lease.deallocate(allocation.ptr, allocation.size)
		} else {
			lease.mod.SecureDeallocate(allocation.ptr, allocation.size)
		}
	}
	lease.allocations = nil
	lease.released = true
}

func (rt *FlowRuntime) selectOutputEdge(nodeIndex uint32, output FrameOutput) (*flowEdgeInfo, error) {
	if output.PortID == "" {
		return nil, errors.New("output port id is required")
	}
	var selected *flowEdgeInfo
	for index := range rt.edgeInfo {
		edge := &rt.edgeInfo[index]
		if edge.Descriptor.FromNode != nodeIndex || edge.FromPort != output.PortID {
			continue
		}
		if output.SchemaName != edge.SchemaName || output.FileIdentifier != edge.FileIdentifier ||
			output.SchemaVersion != edge.SchemaVersion || output.RootTypeName != edge.RootTypeName ||
			!bytes.Equal(output.SchemaHash, edge.SchemaHash) {
			return nil, fmt.Errorf("output port %q schema identity does not match signed edge %d", output.PortID, edge.Index)
		}
		if output.WireFormat == 0 {
			if !edge.CanonicalFallbackAvailable {
				return nil, fmt.Errorf("output port %q has no signed canonical fallback", output.PortID)
			}
		} else if output.WireFormat == 1 {
			if !edge.AlignedEligible || output.ByteLength != edge.Descriptor.AlignedByteLength ||
				output.FixedStringLength != edge.Descriptor.AlignedFixedStringLength ||
				output.RequiredAlignment != edge.Descriptor.AlignedRequiredAlignment ||
				uint64(len(output.Bytes)) != uint64(edge.Descriptor.AlignedByteLength) {
				return nil, fmt.Errorf("output port %q aligned layout does not match signed edge %d", output.PortID, edge.Index)
			}
		} else {
			return nil, fmt.Errorf("output port %q has invalid wire format %d", output.PortID, output.WireFormat)
		}
		if selected == nil {
			selected = edge
		}
	}
	return selected, nil
}

func allocateAlignedPayload(mod *wasmrt.Module, payload []byte, alignment uint32) (ptr, base, allocationSize uint32, err error) {
	if len(payload) == 0 {
		return 0, 0, 0, nil
	}
	if !isPowerOfTwo(alignment) {
		return 0, 0, 0, fmt.Errorf("payload alignment %d is not a power of two", alignment)
	}
	if uint64(len(payload))+uint64(alignment)-1 > uint64(^uint32(0)) {
		return 0, 0, 0, fmt.Errorf("payload length/alignment exceeds wasm32 memory")
	}
	allocationSize = uint32(len(payload)) + alignment - 1
	base, err = mod.AllocateSize(allocationSize)
	if err != nil {
		return 0, 0, 0, err
	}
	aligned := (uint64(base) + uint64(alignment) - 1) &^ (uint64(alignment) - 1)
	if aligned > uint64(^uint32(0)) || aligned+uint64(len(payload)) > uint64(base)+uint64(allocationSize) {
		mod.SecureDeallocate(base, allocationSize)
		return 0, 0, 0, fmt.Errorf("aligned payload allocation overflow")
	}
	ptr = uint32(aligned)
	if err = mod.WriteMemory(ptr, payload); err != nil {
		mod.SecureDeallocate(base, allocationSize)
		return 0, 0, 0, err
	}
	return ptr, base, allocationSize, nil
}

// writeOutputFrames allocates output frame descriptors in parent WASM memory.
// The returned lease must be released immediately after ApplyInvocationResult,
// whether routing succeeds or rejects the frame.
func (rt *FlowRuntime) writeOutputFrames(nodeIndex uint32, outputs []FrameOutput) (uint32, uint32, *outputFrameLease, error) {
	if len(outputs) == 0 {
		return 0, 0, nil, nil
	}
	if len(outputs) > 64 {
		return 0, 0, nil, fmt.Errorf("output frame count %d exceeds runtime limit 64", len(outputs))
	}

	type preparedOutput struct {
		frame FrameOutput
		edge  *flowEdgeInfo
	}
	prepared := make([]preparedOutput, 0, len(outputs))
	for _, output := range outputs {
		edge, err := rt.selectOutputEdge(nodeIndex, output)
		if err != nil {
			return 0, 0, nil, err
		}
		// An unwired signed output may still be consumed by a declaration-driven
		// host publication route, but it is not a parent graph frame.
		if edge == nil {
			continue
		}
		prepared = append(prepared, preparedOutput{frame: output, edge: edge})
	}
	if len(prepared) == 0 {
		return 0, 0, nil, nil
	}
	count := uint32(len(prepared))
	totalSize := count * flowFrameDescriptorSize
	arrPtr, err := rt.mod.AllocateSize(totalSize)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("allocate output descriptors: %w", err)
	}
	lease := &outputFrameLease{mod: rt.mod}
	lease.track(arrPtr, totalSize)
	fail := func(cause error) (uint32, uint32, *outputFrameLease, error) {
		lease.Release()
		return 0, 0, nil, cause
	}
	generation, err := rt.executeUint32(runtimeExportCurrentInvocationGeneration)
	if err != nil {
		return fail(fmt.Errorf("read active invocation generation: %w", err))
	}
	if generation == 0 {
		return fail(errors.New("runtime has no active invocation generation"))
	}

	for i, preparedFrame := range prepared {
		out := preparedFrame.frame
		edge := preparedFrame.edge
		// Allocate payload in WASM memory
		alignment := out.Alignment
		if alignment == 0 {
			alignment = 1
		}
		if out.WireFormat == 1 && alignment < edge.Descriptor.AlignedRequiredAlignment {
			alignment = edge.Descriptor.AlignedRequiredAlignment
		}
		payloadOffset, payloadBase, payloadAllocationSize, allocErr := allocateAlignedPayload(rt.mod, out.Bytes, alignment)
		if allocErr != nil {
			return fail(fmt.Errorf("allocate output payload for port %q: %w", out.PortID, allocErr))
		}
		lease.track(payloadBase, payloadAllocationSize)

		// Allocate portID string
		portIDPtr, allocErr := rt.mod.AllocateString(out.PortID)
		if allocErr != nil {
			return fail(fmt.Errorf("allocate output port %q: %w", out.PortID, allocErr))
		}
		lease.track(portIDPtr, uint32(len(out.PortID))+1)

		fd := &FlowFrameDescriptor{
			IngressIndex:      generation,
			TypeDescriptorIdx: edge.Index,
			PortIDPointer:     portIDPtr,
			Alignment:         alignment,
			Offset:            payloadOffset,
			Size:              uint32(len(out.Bytes)),
			StreamID:          out.StreamID,
			Sequence:          out.Sequence,
			TraceToken:        out.FrameID,
			EndOfStream:       out.EndOfStream,
			Occupied:          true,
			WireFormat:        out.WireFormat,
			Ownership:         0,
			Mutability:        0,
			Lifetime:          1,
		}

		framePtr := arrPtr + uint32(i)*flowFrameDescriptorSize
		if err := writeFrameDescriptor(rt.mod, framePtr, fd); err != nil {
			return fail(fmt.Errorf("write output frame descriptor %d: %w", i, err))
		}
	}

	return arrPtr, count, lease, nil
}

// dispatchDirect calls the linked-direct dispatch for the current invocation.
func (rt *FlowRuntime) dispatchDirect(nodeIndex uint32) error {
	_, dispatchErr := executeRuntimeExport(rt.mod.HasFunction, rt.mod.Execute, runtimeExportDispatchCurrentInvocation, int32(64))
	completeErr := rt.completeInvocationChecked(nodeIndex)
	if dispatchErr != nil {
		return errors.Join(fmt.Errorf("flowrt: linked-direct dispatch for node %d: %w", nodeIndex, dispatchErr), completeErr)
	}
	return completeErr
}
