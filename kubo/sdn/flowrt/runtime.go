package flowrt

import (
	"bytes"
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

// aotCompileComposedArtifact AOT-compiles the ONE composed flow artifact for the
// node's WasmEdge, caching by content hash under the flowcc home. It REUSES the
// shared EnsureAOTArtifact path (threads-enabled) — never per-dependency AOT.
// Returns (aotBytes, true) on success; on any failure it logs LOUDLY and
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
	var linkedStoreDescriptor *LinkedStoreDescriptor
	engineLinked := wasmImportsModule(wasmBytes, engineImportModule)
	if engineLinked {
		var descriptorErr error
		linkedStoreDescriptor, descriptorErr = ReadLinkedStoreDescriptor(wasmBytes)
		if descriptorErr != nil {
			return nil, fmt.Errorf("flowrt: read linked-store descriptor: %w", descriptorErr)
		}
		if linkedStoreDescriptor == nil {
			return nil, fmt.Errorf("flowrt: artifact imports %s but has no %s descriptor", engineImportModule, linkedStoreSectionName)
		}
	}
	if scanWasmThreadFeatures(wasmBytes).isIsomorphicPthreads() {
		opts = append(opts, wasmrt.WithWASIThreads())
		log.Infof("Flow runtime: artifact declares the isomorphic wasi-threads contract — enabling WithWASIThreads (guest pthreads run under WasmEdge)")
		// Engine-linked in-wasm FlatSQL store: the composed flow imports the ONE
		// symbol flatsql.exec_envelope. Attach a dedicated-thread flatsqlrt engine
		// and resolve that import to the exec_envelope host trampoline (proven-safe
		// composition — the engine can NOT join the WithWASIThreads executor). ALL
		// record logic is in-wasm; the host moves opaque bytes + opaque snapshots.
		if engineLinked {
			home := flowcc.ResolveHome()
			storeDir := filepath.Join(home.CacheDir(), "flow-store")
			sum := sha256.Sum256(wasmBytes)
			snap := filepath.Join(storeDir, fmt.Sprintf("%x.snapshot", sum[:16]))
			ls, serr := OpenLinkedStore(filepath.Join(storeDir, "aot"), snap, linkedStoreDescriptor)
			if serr != nil {
				return nil, fmt.Errorf("flowrt: attach in-wasm FlatSQL store: %w", serr)
			}
			linkedStore = ls
			opts = append(opts, wasmrt.WithHostModule(engineImportModule, []wasmrt.HostFunc{ls.ExecEnvelopeHostFunc(), ls.IngestRecordHostFunc(), ls.QueryRowsHostFunc(), ls.MarkDeletedBulkHostFunc(), ls.CompactHostFunc()}))
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
	} else if engineLinked {
		return nil, fmt.Errorf("flowrt: engine-linked artifacts must declare the wasi-threads contract")
	}
	opts = append(opts, extraOpts...)

	mod, err := wasmrt.NewModule(wasmBytes, opts...)
	if err != nil {
		if linkedStore != nil {
			linkedStore.Close()
		}
		return nil, fmt.Errorf("failed to create flow WASM module: %w", err)
	}

	// Run a declared initializer before reading any runtime descriptors.
	switch {
	case mod.HasFunction("_initialize"):
		if _, initErr := mod.Execute("_initialize"); initErr != nil {
			mod.Release()
			if linkedStore != nil {
				linkedStore.Close()
			}
			return nil, fmt.Errorf("flowrt: run WASI reactor initialization: %w", initErr)
		}
	case mod.HasFunction("__wasm_call_ctors"):
		if _, initErr := mod.Execute("__wasm_call_ctors"); initErr != nil {
			mod.Release()
			if linkedStore != nil {
				linkedStore.Close()
			}
			return nil, fmt.Errorf("flowrt: run command module constructors: %w", initErr)
		}
	}

	rt := &FlowRuntime{mod: mod, store: linkedStore}

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

// executeUint32 calls a required no-arg export returning a uint32.
func (rt *FlowRuntime) executeUint32(name string) (uint32, error) {
	res, err := rt.mod.Execute(name)
	if err != nil {
		res, err = rt.mod.Execute(underscoreRuntimeExportName(name))
		if err != nil {
			return 0, err
		}
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("export %q returned no values", name)
	}
	return uint32(wasmrt.ToInt32(res[0])), nil
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

func (rt *FlowRuntime) cacheFlowEdges() error {
	if rt.EdgeCount == 0 {
		rt.edgeInfo = nil
		return nil
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
			return 0, fmt.Errorf("apply invocation result for node %d: %w", nodeIndex, err)
		}
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("apply invocation result for node %d returned no values", nodeIndex)
	}
	return decodeRoutingResult(wasmrt.ToInt32(res[0]))
}

// CompleteInvocation completes the invocation for a node.
func (rt *FlowRuntime) CompleteInvocation(nodeIndex uint32) {
	rt.mod.Execute(runtimeExportCompleteInvocation, int32(nodeIndex))
}

// EnqueueTrigger enqueues a trigger without frame data.
func (rt *FlowRuntime) EnqueueTrigger(triggerIndex uint32) {
	rt.mod.Execute(runtimeExportEnqueueTriggerFrames, int32(triggerIndex))
}

// EnqueueTriggerFrame enqueues a frame to a trigger's input and surfaces any
// parent-runtime rejection. The compiled runtime copies accepted frame bytes.
func (rt *FlowRuntime) EnqueueTriggerFrame(triggerIndex uint32, framePtr uint32) error {
	if _, err := rt.mod.Execute(runtimeExportEnqueueTriggerFrame, int32(triggerIndex), int32(framePtr)); err != nil {
		if _, fallbackErr := rt.mod.Execute(underscoreRuntimeExportName(runtimeExportEnqueueTriggerFrame), int32(triggerIndex), int32(framePtr)); fallbackErr != nil {
			return fmt.Errorf("enqueue trigger %d frame at %d: %w", triggerIndex, framePtr, err)
		}
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
		if handlerResult == nil {
			handlerResult = &InvocationResult{StatusCode: -1}
		}

		// Write output frames and apply result
		framesPtr, frameCount, lease, writeErr := rt.writeOutputFrames(nodeIndex, handlerResult.Outputs)
		if writeErr != nil {
			rt.CompleteInvocation(nodeIndex)
			return result, fmt.Errorf("flowrt: prepare outputs for node %d: %w", nodeIndex, writeErr)
		}
		_, applyErr := rt.ApplyInvocationResult(nodeIndex, handlerResult, framesPtr, frameCount)
		if lease != nil {
			lease.Release()
		}
		rt.CompleteInvocation(nodeIndex)
		if applyErr != nil {
			return result, fmt.Errorf("flowrt: route outputs for node %d: %w", nodeIndex, applyErr)
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
		if fd.TypeDescriptorIdx < uint32(len(rt.edgeInfo)) {
			edge := rt.edgeInfo[fd.TypeDescriptorIdx]
			if edge.Descriptor.ToNode != inv.NodeIndex || edge.ToPort != portID {
				return frames, fmt.Errorf("input frame %d type descriptor %d is not bound to node %d port %q", i, fd.TypeDescriptorIdx, inv.NodeIndex, portID)
			}
			if fd.WireFormat == 0 && !edge.CanonicalFallbackAvailable {
				return frames, fmt.Errorf("input frame %d uses unavailable canonical fallback on edge %d", i, edge.Index)
			}
			if fd.WireFormat == 1 {
				if !edge.AlignedEligible || fd.Size != edge.Descriptor.AlignedByteLength ||
					fd.Alignment < edge.Descriptor.AlignedRequiredAlignment ||
					fd.Offset%edge.Descriptor.AlignedRequiredAlignment != 0 {
					return frames, fmt.Errorf("input frame %d violates aligned layout on edge %d", i, edge.Index)
				}
			}
			frame.SchemaName = edge.SchemaName
			frame.FileIdentifier = edge.FileIdentifier
			frame.SchemaVersion = edge.SchemaVersion
			frame.SchemaHash = append([]byte(nil), edge.SchemaHash...)
			frame.RootTypeName = edge.RootTypeName
			frame.FixedStringLength = edge.Descriptor.AlignedFixedStringLength
			frame.ByteLength = edge.Descriptor.AlignedByteLength
			frame.RequiredAlignment = edge.Descriptor.AlignedRequiredAlignment
		} else if fd.WireFormat == 1 {
			return frames, fmt.Errorf("input frame %d aligned representation has no signed edge type descriptor", i)
		}
		frames = append(frames, frame)
	}
	return frames, nil
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
func (rt *FlowRuntime) dispatchDirect(nodeIndex uint32) {
	res, err := rt.mod.Execute(runtimeExportDispatchCurrentInvocation, int32(64))
	if err != nil {
		rt.mod.Execute(underscoreRuntimeExportName(runtimeExportDispatchCurrentInvocation), int32(64))
	}
	_ = res
	rt.CompleteInvocation(nodeIndex)
}
