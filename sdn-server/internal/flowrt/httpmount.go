package flowrt

// HTTP flow mounts (loop C.3d, pooled in C.4): a compiled flow bundle is just
// a WASM module — it loads through the standard flowrt instantiation path
// like any other module, with the module-SDK hostcall bridge satisfying its
// declared capabilities. There is NO gateway: the only host glue is socket
// plumbing with zero decisions —
//
//	HTTP request  → one $HTQ FlatBuffer frame (method/path/raw query/headers/
//	                body/remote, verbatim) enqueued at the flow's HTTP trigger
//	$HTR frame(s) → status/headers/body written verbatim to the
//	                ResponseWriter, flushed per frame
//
// All routing, query parsing, format selection, profile resolution, caching,
// and ETag logic live inside the wasm flow. Which flow owns which listener
// path is configuration (config.FlowMount), never Go code.
//
// Concurrency: requests are serialized per flow INSTANCE (one linear memory
// each), so every mount runs a small instance pool (config.FlowMount.Pool,
// default 4) — a request checks an idle instance out of the pool for the
// duration of the exchange.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/httpabi"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// DefaultMountPool is the instance-pool size used when a mount does not
// configure one.
const DefaultMountPool = 4

// flowAOTCachePrefix is the NAMESPACE for flow-mount AOT artifacts inside a
// shared AOT cache directory (distinct from the engine's "flatsql-" prefix).
// It is never used as a cache key on its own: the cache prunes by prefix, and
// many flows share this namespace, so keys are built per flow by flowAOTPrefix
// (see prewarm.go).
const flowAOTCachePrefix = "flowmount"

// ErrFlowNotInstalled reports a flow reference that resolves to neither a
// filesystem path nor an installed flow artifact. Mount registration skips
// (rather than aborts on) these so a default-config node without the module
// installed yet still boots; delivery of the module later + restart mounts
// it.
var ErrFlowNotInstalled = errors.New("flow is not installed")

// FlowMountDeps carries the host services a mounted flow's declared
// capabilities are satisfied from. Nothing here makes request-level decisions.
type FlowMountDeps struct {
	// CapRegistry provides capability handlers for the flow's manifest
	// capability set. Loading REJECTS if a declared capability has no factory.
	CapRegistry *modulert.CapabilityRegistry

	// NodeCtx is the node identity/config context exposed to built-in
	// hostcalls (plugin.getConfig, node.peerId, ...). May be empty. Also
	// carries the operator-controlled trust policies consulted before a
	// flow bundle's wasm bytes are admitted: NodeCtx.CapabilityPolicy (loop
	// B1-followup, see loadFlowInstance) and NodeCtx.ModuleSignaturePolicy
	// (loop I2 — mirrors modulert's module load path, loop I1; see
	// LoadMountedFlow/LoadFlowService). A nil NodeCtx, or a non-nil NodeCtx
	// with ModuleSignaturePolicy left nil (the zero value, same as today),
	// means signature enforcement is not configured for this node: flow
	// bundles load exactly as before that gate existed.
	NodeCtx *modulert.NodeContext

	// MaxMemoryPages caps each flow instance's linear memory (64KB pages,
	// 0 = 1024). Per-mount config.FlowMount.MemoryPages overrides it.
	MaxMemoryPages uint32

	// PoolSize is the number of flow instances to load for the mount
	// (<= 0: DefaultMountPool via RegisterFlowMounts, 1 when LoadMountedFlow
	// is called directly).
	PoolSize int

	// AOTCacheDir, when set, loads a precompiled flow artifact from the same
	// sha256-keyed disk cache the FlatSQL engine uses. Cache miss falls back
	// to interpretation unless AOTCompileOnMiss is explicitly enabled by a
	// test or maintenance prewarm path. Daemon startup must not compile wasm
	// artifacts.
	AOTCacheDir string

	// AOTCompileOnMiss allows tests/benchmarks/prewarm tooling to populate
	// AOTCacheDir. Production daemon startup leaves this false.
	AOTCompileOnMiss bool

	// Store optionally resolves installed flow program IDs to artifacts.
	Store *FlowStore

	// ReadinessCheck optionally rejects a request before lazy load/body read.
	// Daemons use this to return 503 while shared storage is hydrating instead
	// of blocking browser requests behind startup catch-up work.
	ReadinessCheck func() error

	// EngineLink shares the host store's LIVE FlatSQL engine with
	// engine-linked flow artifacts (loop C.7 direct linkage,
	// engine_link.go). Mount registration HARD-FAILS when an artifact
	// imports the "flatsql" module and no link is provided: linking grants
	// full store-memory access, so it is an explicit, first-party grant —
	// never inferred. Bridge-mode artifacts ignore it.
	EngineLink EngineLinkProvider
}

// MountedFlow is one flow module bound to one HTTP listener path, served by a
// pool of identical instances. It is the dumb pipe between the socket and
// the flow's ingress trigger / egress sink.
type MountedFlow struct {
	manifest *modulert.Manifest
	aot      bool
	linked   bool

	// mountPath is the mux pattern the flow is bound to (RegisterFlowMounts).
	// apiDoc/flowVersion carry the bundle flow.json "api" extension (loop
	// G.1, apidoc.go) for the OpenAPI-from-mounted-flows generator.
	mountPath   string
	apiDoc      *FlowAPIDoc
	flowVersion string

	triggerIndex  uint32
	triggerPortID string
	egressKeys    []string

	// runBytes/pages/deps/linkShim are retained so linked instances can be
	// rebuilt against a replacement engine after poisoning (loop C.7).
	runBytes []byte
	pages    uint32
	deps     FlowMountDeps
	linkShim []byte

	// contentHash is the SHA-256 content-hash identity (loop B1-followup) of
	// the flow bundle's portable, canonical WASM bytes — computed once in
	// LoadMountedFlow and reused for every pooled instance and every
	// re-instantiation (e.g. the engine-epoch rebuild in ServeHTTP) so the
	// capability-policy gate always keys off the same identity as the
	// artifact an operator approved.
	contentHash string

	pool chan *flowInstance

	lazy     bool
	flowRef  string
	lazyDeps FlowMountDeps
	lazyMu   sync.RWMutex

	closeMu sync.Mutex
	closed  bool
}

// flowInstance is one pooled flow runtime plus its hostcall bridge (the
// egress needs the bridge to resolve $HTR body references registered by
// capability handlers during the exchange) and, for engine-linked artifacts,
// the direct-linkage state (loop C.7).
type flowInstance struct {
	rt     *FlowRuntime
	bridge *modulert.HostBridge

	// Direct linkage (nil/zero for bridge-mode artifacts):
	linked       bool
	engineEpoch  uint64              // store engine epoch this instance was built against
	engineRT     *flatsqlrt.Runtime  // the borrowed live engine
	engineDB     *flatsqlrt.Database // its database (lock + harvest surface)
	refTablePtr  uint32              // sdn_flatsql_link_ref_table (static)
	refSlots     uint32              // sdn_flatsql_link_ref_slots
	engineMu     sync.Mutex          // guards engineBodies (egress runs on the request goroutine)
	engineBodies map[uint64][]byte   // exchange-scoped harvested bodies by token
}

// takeEngineBody resolves (and removes) a harvested engine body-ref.
func (inst *flowInstance) takeEngineBody(token uint64) ([]byte, bool) {
	inst.engineMu.Lock()
	defer inst.engineMu.Unlock()
	b, ok := inst.engineBodies[token]
	if ok {
		delete(inst.engineBodies, token)
	}
	return b, ok
}

// resetEngineBodies drops any unconsumed harvested bodies (end of exchange).
func (inst *flowInstance) resetEngineBodies() {
	inst.engineMu.Lock()
	defer inst.engineMu.Unlock()
	inst.engineBodies = map[uint64][]byte{}
}

// harvestEngineRefs reads the flow's engine body-reference table and
// resolves every fresh entry through the store's mirror — INSIDE the locked
// linked-drain section, so the generation cannot move and every engine
// pointer is valid by construction. Slots are cleared after harvest
// (single-use references).
func (inst *flowInstance) harvestEngineRefs(h *flatsqlrt.EngineLinkHarvest) {
	if inst.refTablePtr == 0 || inst.refSlots == 0 {
		return
	}
	data, err := inst.rt.Module().ReadMemory(inst.refTablePtr, inst.refSlots*engineRefEntrySize)
	if err != nil {
		log.Warnf("engine-link harvest: read ref table: %v", err)
		return
	}
	for i := uint32(0); i < inst.refSlots; i++ {
		entry := decodeEngineRefEntry(data[i*engineRefEntrySize : (i+1)*engineRefEntrySize])
		if !entry.Used {
			continue
		}
		stream, err := h.ResolveRef(entry.Generation, entry.EnginePtr, entry.Size, entry.FNV1a64, int(entry.Frames))
		if err != nil {
			log.Warnf("engine-link harvest: resolve token %d: %v", entry.Token, err)
		} else {
			inst.engineMu.Lock()
			inst.engineBodies[entry.Token] = stream.Bytes
			inst.engineMu.Unlock()
		}
		// Clear the slot: references are single-use and must not be
		// rescanned by later drains.
		if err := inst.rt.Module().WriteMemory(inst.refTablePtr+i*engineRefEntrySize+36, []byte{0, 0, 0, 0}); err != nil {
			log.Warnf("engine-link harvest: clear slot %d: %v", i, err)
		}
	}
}

// flowBundleTopology is the subset of the compiled bundle's flow.json needed
// to locate the HTTP ingress trigger and its bound port.
type flowBundleTopology struct {
	Triggers []struct {
		TriggerID string `json:"triggerId"`
		Kind      string `json:"kind"`
	} `json:"triggers"`
	TriggerBindings []struct {
		TriggerID    string `json:"triggerId"`
		TargetPortID string `json:"targetPortId"`
	} `json:"triggerBindings"`
}

// resolveFlowArtifact maps a config flow reference to the artifact wasm path
// and (when known) the bundle directory holding flow.json.
func resolveFlowArtifact(flowRef string, store *FlowStore) (wasmPath, bundleDir string, err error) {
	if info, statErr := os.Stat(flowRef); statErr == nil {
		if info.IsDir() {
			return filepath.Join(flowRef, "runtime.wasm"), flowRef, nil
		}
		return flowRef, filepath.Dir(flowRef), nil
	}
	if store != nil {
		if _, getErr := store.Get(flowRef); getErr == nil {
			wasmPath = store.WASMPath(flowRef)
			return wasmPath, filepath.Dir(wasmPath), nil
		}
	}
	return "", "", fmt.Errorf("flow reference %q is neither a filesystem path nor an installed flow: %w", flowRef, ErrFlowNotInstalled)
}

// loadFlowInstance instantiates one pooled flow instance: standard flowrt
// load (WASI + flow host funcs + the module-SDK hostcall bridge), manifest
// read, capability provisioning from the registry — rejecting the load if
// the host cannot satisfy a required capability OR (loop B1-followup) if the
// flow bundle requests a sensitive capability the operator has not approved
// for its content hash (default-deny, fail closed — see
// modulert.checkCapabilityPolicy).
//
// contentHash is the SHA-256 content-hash identity (modulert.ContentHashHex)
// of the flow bundle's portable, canonical WASM bytes — computed ONCE by the
// caller (LoadMountedFlow, from the artifact bytes before AOT compilation)
// and threaded through every pooled instance and every re-instantiation
// (e.g. the engine-epoch rebuild in ServeHTTP) so they all key the capability
// policy off the same identity. wasmBytes here is deliberately a SEPARATE
// parameter from contentHash: wasmBytes may be an AOT-compiled variant of the
// artifact (platform/runtime-version-specific bytes), which must never be
// hashed for policy purposes — see ProvisionIdentity.ContentHash.
//
// Engine-linked artifacts (they import the "flatsql" wasm module — loop C.7)
// additionally get the store's LIVE engine instance and the flatsql_link
// shim registered into their VM before instantiation, the store db handle
// wired in, and a linked-drain section that holds the store engine lock
// around every in-wasm drain and harvests engine body-references before
// releasing it. Loading a linked artifact WITHOUT deps.EngineLink is a hard
// error (first-party grant, never inferred).
func loadFlowInstance(wasmBytes []byte, pages uint32, linked bool, linkShim []byte, deps FlowMountDeps, contentHash string) (*flowInstance, *modulert.Manifest, error) {
	// The hostcall bridge is created before the manifest is readable
	// (chicken-and-egg, same as modulert.Module); its capability grants are
	// applied right after the manifest parse below.
	bridge := modulert.NewHostBridge(deps.NodeCtx, nil)

	opts := []wasmrt.Option{
		wasmrt.WithHostModule(modulert.HostcallImportModule, bridge.BuildWasmEdgeHostFuncs()),
	}

	var engineRT *flatsqlrt.Runtime
	var engineDB *flatsqlrt.Database
	var engineEpoch uint64
	if linked {
		if deps.EngineLink == nil {
			return nil, nil, fmt.Errorf("flow artifact links the store engine (imports module %q) but the host provided no EngineLink — linked mounts are a first-party grant", flatsqlrt.EngineImportModule)
		}
		engineEpoch = deps.EngineLink.EngineEpoch()
		engineRT, engineDB = deps.EngineLink.EngineRuntime()
		if engineRT == nil || engineDB == nil {
			return nil, nil, fmt.Errorf("engine link provider returned no live engine")
		}
		if len(linkShim) == 0 {
			linkShim = flatsqlLinkShimWasm
		}
		opts = append(opts,
			wasmrt.WithLinkedModuleFrom(engineRT.WasmModule()),
			wasmrt.WithNamedWasm(LinkShimModuleName, linkShim),
		)
	}

	rt, err := NewFlowRuntime(wasmBytes, pages, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("load flow module: %w", err)
	}

	manifest, err := modulert.ReadManifest(rt.Module())
	if err != nil {
		rt.Release()
		return nil, nil, fmt.Errorf("read flow manifest: %w", err)
	}

	// The engine-link capability is satisfied by the mount's engine link,
	// not by a hostcall handler — exclude it from bridge provisioning.
	capabilities := make([]string, 0, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		if capability == EngineLinkCapability {
			if !linked {
				rt.Release()
				return nil, nil, fmt.Errorf("flow %q declares %s but the artifact does not import the engine module", manifest.PluginID, EngineLinkCapability)
			}
			continue
		}
		capabilities = append(capabilities, capability)
	}
	// Operator capability policy gate (loop B1-followup — defensive
	// hardening, FAIL CLOSED): flow bundles have no *Module, so the identity
	// checkCapabilityPolicy keys off (content hash / plugin id / policy
	// store) is supplied explicitly here rather than derived from mod
	// (which stays nil — flow bundles are not driven through the Module
	// invocation surface). See ProvisionIdentity for why contentHash must be
	// the caller-computed, pre-AOT hash and not derived from wasmBytes here.
	var policy *modulert.CapabilityPolicyStore
	if deps.NodeCtx != nil {
		policy = deps.NodeCtx.CapabilityPolicy
	}
	identity := modulert.ProvisionIdentity{
		ContentHash: contentHash,
		PluginID:    manifest.PluginID,
		Policy:      policy,
	}
	if err := modulert.ProvisionBridge(bridge, deps.CapRegistry, capabilities, nil, identity); err != nil {
		rt.Release()
		return nil, nil, fmt.Errorf("flow %q: %w", manifest.PluginID, err)
	}

	inst := &flowInstance{rt: rt, bridge: bridge, engineBodies: map[uint64][]byte{}}
	if linked {
		if _, err := rt.Module().Execute("sdn_flatsql_link_init", int32(engineDB.Handle())); err != nil {
			rt.Release()
			return nil, nil, fmt.Errorf("flow %q: sdn_flatsql_link_init: %w", manifest.PluginID, err)
		}
		tableRes, err := rt.Module().Execute("sdn_flatsql_link_ref_table")
		if err != nil {
			rt.Release()
			return nil, nil, fmt.Errorf("flow %q: sdn_flatsql_link_ref_table: %w", manifest.PluginID, err)
		}
		slotsRes, err := rt.Module().Execute("sdn_flatsql_link_ref_slots")
		if err != nil {
			rt.Release()
			return nil, nil, fmt.Errorf("flow %q: sdn_flatsql_link_ref_slots: %w", manifest.PluginID, err)
		}
		inst.linked = true
		inst.engineEpoch = engineEpoch
		inst.engineRT = engineRT
		inst.engineDB = engineDB
		inst.refTablePtr = uint32(wasmrt.ToInt32(tableRes[0]))
		inst.refSlots = uint32(wasmrt.ToInt32(slotsRes[0]))

		// Every in-wasm drain of this instance runs under the store engine
		// lock (SQLITE_THREADSAFE=0; linked calls are one contiguous
		// executor invocation — safe on the request thread, C.5c §7.1). A
		// trap inside the drain may have corrupted engine state exactly like
		// a host-invoked trap: poison the engine so the store replaces it.
		rt.SetLinkedSection(func(run func() error) error {
			return engineDB.WithLinkedDrain(func(h *flatsqlrt.EngineLinkHarvest) error {
				if err := run(); err != nil {
					engineRT.MarkPoisoned()
					return err
				}
				inst.harvestEngineRefs(h)
				return nil
			})
		})
	}
	return inst, manifest, nil
}

// LoadMountedFlow loads a compiled flow bundle as a pool of deps.PoolSize
// identical instances (minimum 1). When deps.AOTCacheDir is set the loader
// uses an existing AOT artifact; only explicit AOTCompileOnMiss callers build
// cache entries. Cache miss or compile failure falls back to interpretation.
func LoadMountedFlow(flowRef string, deps FlowMountDeps) (*MountedFlow, error) {
	wasmPath, bundleDir, err := resolveFlowArtifact(flowRef, deps.Store)
	if err != nil {
		return nil, err
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read flow artifact: %w", err)
	}
	// Publication-trailer signature gate (loop I2 — defensive hardening,
	// FAIL CLOSED once configured): admitted here, before the trailer is
	// stripped or the bytes reach the AOT cache/runtime, reusing the exact
	// same gate the module load path applies (modulert.instantiateWASM,
	// loop I1) via the exported wrapper. Sourced from deps.NodeCtx (mirrors
	// how loadFlowInstance below sources NodeCtx.CapabilityPolicy) rather
	// than a separate FlowMountDeps field, so a node.go wiring that already
	// builds one NodeContext for a mount attaches both trust policies in
	// one place. A nil NodeCtx / nil ModuleSignaturePolicy (today's
	// default) makes this inert: EnforceModuleSignaturePolicy always
	// strips and returns the portable payload but never rejects.
	//
	// Published artifacts carry an appended SDS $REC publication trailer
	// (MBL+PNM per the module publication standard); the runtime payload is
	// the bytes before it. Stripping BEFORE the AOT cache keeps the cache
	// keyed on the executable payload, so a precompiled artifact shipped
	// into the cache dir matches regardless of publication metadata.
	var sigPolicy *modulert.ModuleSignaturePolicy
	if deps.NodeCtx != nil {
		sigPolicy = deps.NodeCtx.ModuleSignaturePolicy
	}
	portableBytes, _, sigErr := modulert.EnforceModuleSignaturePolicy(sigPolicy, wasmBytes)
	if sigErr != nil {
		return nil, fmt.Errorf("flow %q: %w", flowRef, sigErr)
	}
	wasmBytes = portableBytes

	// Content-hash identity for the operator capability-policy gate (loop
	// B1-followup) — computed HERE, on the portable bytes, before any AOT
	// compilation below. AOT-compiled bytes are platform/runtime-version-
	// specific, so hashing them instead would make a recorded operator
	// approval silently stop matching on a different host; see
	// modulert.ProvisionIdentity.ContentHash.
	contentHash := modulert.ContentHashHex(wasmBytes)

	runBytes := wasmBytes
	aot := false
	if deps.AOTCacheDir != "" {
		var compiled []byte
		var aotErr error
		if deps.AOTCompileOnMiss {
			compiled, aotErr = flatsqlrt.EnsureAOTArtifact(deps.AOTCacheDir, flowAOTPrefix(flowRef), wasmBytes)
		} else {
			compiled, aotErr = flatsqlrt.LoadAOTArtifact(deps.AOTCacheDir, flowAOTPrefix(flowRef), wasmBytes)
		}
		if aotErr == nil {
			runBytes = compiled
			aot = true
		} else {
			log.Warnf("Flow mount %q: AOT artifact unavailable, interpreting: %v", flowRef, aotErr)
		}
	}

	pages := deps.MaxMemoryPages
	if pages == 0 {
		pages = 1024
	}
	poolSize := deps.PoolSize
	if poolSize <= 0 {
		poolSize = 1
	}

	// Engine-linked artifact detection is mechanical: it imports the live
	// engine's wasm module, so it cannot instantiate without the link.
	// Scanned on the PORTABLE bytes (AOT universal artifacts wrap them).
	linked := wasmImportsModule(wasmBytes, flatsqlrt.EngineImportModule)

	// libwasmedge 0.14 LIMITATION (loop C.7, docs/flatsql-component-linkage.md
	// §8): an AOT-compiled flow making direct cross-instance calls into the
	// AOT engine falsely traps "out of bounds memory access" once the real
	// query sequence runs (interpreted flows against the AOT engine are
	// proven byte-verbatim green, and every isolated mechanism — own-memory
	// AOT->AOT calls, three-module chains, callee memory growth, locked and
	// unlocked threads — passes; the trigger is the full workload's call
	// pattern, same bug class as the C.5b nested-execution corruption). The
	// heavy work (query execution, stream materialization) runs INSIDE the
	// AOT engine either way, so linked flows interpret the small flow
	// artifact ON AFFECTED RUNTIMES ONLY. Loop C.9 retested the repro
	// (SDN_C7_FORCE_LINKED_AOT=1 + flowrt TestAOTMountRepro) on libwasmedge
	// 0.16.4: FIXED — linked mounts run AOT there
	// (flatsqlrt.RuntimeHasLinkedAOTFix). Overrides for A/B measurement and
	// upstream retests: SDN_C7_FORCE_LINKED_AOT=1 forces AOT on any runtime;
	// SDN_C7_FORCE_LINKED_INTERP=1 forces interpretation on any runtime.
	linkShim := flatsqlLinkShimWasm
	forceInterp := linked && aot && os.Getenv("SDN_C7_FORCE_LINKED_INTERP") != ""
	if linked && aot && !forceInterp &&
		!flatsqlrt.RuntimeHasLinkedAOTFix() && os.Getenv("SDN_C7_FORCE_LINKED_AOT") == "" {
		forceInterp = true
	}
	if forceInterp {
		runBytes = wasmBytes
		aot = false
		log.Warnf("Flow mount %q: engine-linked artifact runs INTERPRETED (libwasmedge %s AOT cross-instance limitation, fixed in >=0.16.4; engine stays AOT)", flowRef, flatsqlrt.RuntimeVersion())
	}
	if linked && aot {
		// Keep the whole linked chain AOT when forcing AOT (repro/upgrades).
		var compiledShim []byte
		var shimErr error
		if deps.AOTCompileOnMiss {
			compiledShim, shimErr = flatsqlrt.EnsureAOTArtifact(deps.AOTCacheDir, linkShimAOTCachePrefix, flatsqlLinkShimWasm)
		} else {
			compiledShim, shimErr = flatsqlrt.LoadAOTArtifact(deps.AOTCacheDir, linkShimAOTCachePrefix, flatsqlLinkShimWasm)
		}
		if shimErr == nil {
			linkShim = compiledShim
		} else {
			log.Warnf("Flow mount %q: link-shim AOT artifact unavailable, interpreting shim: %v", flowRef, shimErr)
		}
	}

	mf := &MountedFlow{
		aot:           aot,
		linked:        linked,
		triggerIndex:  0,
		triggerPortID: "request",
		runBytes:      runBytes,
		pages:         pages,
		deps:          deps,
		linkShim:      linkShim,
		contentHash:   contentHash,
		pool:          make(chan *flowInstance, poolSize),
	}

	for i := 0; i < poolSize; i++ {
		inst, manifest, err := loadFlowInstance(runBytes, pages, linked, linkShim, deps, contentHash)
		if err != nil {
			mf.Close()
			return nil, err
		}
		rt := inst.rt
		if mf.manifest == nil {
			mf.manifest = manifest

			// Ingress trigger index + bound port come from the bundle's
			// flow.json topology when present (mechanical lookup, no
			// interpretation). The same read carries the "api" extension
			// (loop G.1) for the OpenAPI-from-mounted-flows generator.
			if bundleDir != "" {
				if data, readErr := os.ReadFile(filepath.Join(bundleDir, "flow.json")); readErr == nil {
					var topo flowBundleTopology
					if json.Unmarshal(data, &topo) == nil {
						for ti, trig := range topo.Triggers {
							if trig.Kind != "http-request" {
								continue
							}
							mf.triggerIndex = uint32(ti)
							for _, binding := range topo.TriggerBindings {
								if binding.TriggerID == trig.TriggerID && binding.TargetPortID != "" {
									mf.triggerPortID = binding.TargetPortID
								}
							}
							break
						}
					}
					mf.apiDoc, mf.flowVersion = parseFlowAPIDoc(data)
				}
			}

			// Egress sinks are the artifact's host-dispatch nodes (in the
			// linked-direct model every other node runs inside the artifact
			// and all hostcalls are capabilities). Identical across pool
			// instances (same artifact bytes).
			for ni := uint32(0); ni < rt.NodeCount; ni++ {
				dd, ddErr := rt.GetNodeDispatchDescriptor(ni)
				if ddErr != nil {
					continue
				}
				if rt.readCStringAt(dd.DispatchModelPointer) != "host" {
					continue
				}
				pluginID := rt.readCStringAt(dd.PluginIDPointer)
				methodID := rt.readCStringAt(dd.MethodIDPointer)
				if pluginID == "" || methodID == "" {
					continue
				}
				mf.egressKeys = append(mf.egressKeys, pluginID+":"+methodID)
			}
			if len(mf.egressKeys) == 0 {
				rt.Release()
				mf.Close()
				return nil, fmt.Errorf("flow %q has no host-model egress sink to deliver HTTP responses", manifest.PluginID)
			}
		}
		mf.pool <- inst
	}

	return mf, nil
}

func newLazyMountedFlow(flowRef string, deps FlowMountDeps) *MountedFlow {
	apiDoc, flowVersion := loadFlowAPIMetadata(flowRef, deps.Store)
	return &MountedFlow{
		lazy:        true,
		flowRef:     flowRef,
		lazyDeps:    deps,
		apiDoc:      apiDoc,
		flowVersion: flowVersion,
	}
}

func loadFlowAPIMetadata(flowRef string, store *FlowStore) (*FlowAPIDoc, string) {
	_, bundleDir, err := resolveFlowArtifact(flowRef, store)
	if err != nil || bundleDir == "" {
		return nil, ""
	}
	data, err := os.ReadFile(filepath.Join(bundleDir, "flow.json"))
	if err != nil {
		return nil, ""
	}
	return parseFlowAPIDoc(data)
}

func (mf *MountedFlow) ensureLoaded() error {
	if mf == nil || !mf.lazy {
		return nil
	}

	mf.lazyMu.RLock()
	loaded := mf.pool != nil
	mf.lazyMu.RUnlock()
	if loaded {
		return nil
	}

	mf.lazyMu.Lock()
	defer mf.lazyMu.Unlock()
	if mf.pool != nil {
		return nil
	}
	mf.closeMu.Lock()
	closed := mf.closed
	mf.closeMu.Unlock()
	if closed {
		return errors.New("flow module is closed")
	}

	loadedFlow, err := LoadMountedFlow(mf.flowRef, mf.lazyDeps)
	if err != nil {
		return err
	}
	loadedFlow.mountPath = mf.mountPath

	mf.manifest = loadedFlow.manifest
	mf.aot = loadedFlow.aot
	mf.linked = loadedFlow.linked
	if loadedFlow.apiDoc != nil {
		mf.apiDoc = loadedFlow.apiDoc
	}
	if loadedFlow.flowVersion != "" {
		mf.flowVersion = loadedFlow.flowVersion
	}
	mf.triggerIndex = loadedFlow.triggerIndex
	mf.triggerPortID = loadedFlow.triggerPortID
	mf.egressKeys = loadedFlow.egressKeys
	mf.runBytes = loadedFlow.runBytes
	mf.pages = loadedFlow.pages
	mf.deps = loadedFlow.deps
	mf.linkShim = loadedFlow.linkShim
	mf.contentHash = loadedFlow.contentHash
	mf.pool = loadedFlow.pool
	programID := ""
	if mf.manifest != nil {
		programID = mf.manifest.PluginID
	}
	log.Infof("Flow %q loaded lazily at %s (pool %d, aot %v, trigger %d, egress %v)",
		programID, mf.mountPath, cap(mf.pool), mf.aot, mf.triggerIndex, mf.egressKeys)
	return nil
}

func (mf *MountedFlow) readinessCheck() func() error {
	mf.lazyMu.RLock()
	defer mf.lazyMu.RUnlock()
	if mf.pool != nil {
		return mf.deps.ReadinessCheck
	}
	return mf.lazyDeps.ReadinessCheck
}

// acquire checks an idle instance out of the pool, waiting until one frees
// up or the request context ends.
func (mf *MountedFlow) acquire(ctx context.Context) (*flowInstance, error) {
	select {
	case inst, ok := <-mf.pool:
		if !ok || inst == nil {
			return nil, errors.New("flow module is closed")
		}
		return inst, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// release returns an instance to the pool (or frees it if the mount closed
// while the request was in flight).
func (mf *MountedFlow) release(inst *flowInstance) {
	mf.closeMu.Lock()
	closed := mf.closed
	mf.closeMu.Unlock()
	if closed {
		inst.rt.Release()
		return
	}
	mf.pool <- inst
}

// releaseAfterExchange returns an instance to the pool with the ingress queue
// it was handed CLEAN, and is the only path an HTTP exchange may use.
//
// THE DEFECT THIS CLOSES. The host enqueues exactly ONE $HTQ frame per request
// (ServeHTTP above) and then hands the pooled instance to the NEXT request. The
// trigger queue is INSTANCE state, so a frame the flow never consumed survives
// the exchange that created it. Nothing consumes it whenever the drain ends
// early — a client that disconnected (Drain returns ctx.Err()), a mid-drain
// error, or a guest entry node that refused before reading its input port.
//
// The next request then enqueues its own frame ON TOP of the residue and the
// entry node sees TWO frames on a port its manifest declares single-stream.
// A flow that enforces that contract in code — `refuse_batched()` in the
// cellular aggregate's cache_plan — refuses, and because every node of a
// linked-direct artifact runs INSIDE the guest, the refusal is a wasm return
// value: Drain reports no error, no handler is invoked, and the mount answers a
// bare 502 "flow produced no HTTP response". The residue is never consumed, so
// the SECOND request poisons the instance PERMANENTLY: every later request on
// it fails identically until the daemon restarts.
//
// Measured on host-01 2026-08-27 (graph sdn-host01-cellular-providers-502):
// /api/v1/cellular/providers, /tiles/meta and /tiles/0/0/0 all answered 502 in
// ~13 ms while the flow was loaded and healthy (17 nodes, 34 edges) and every
// capability it needs was approved — one poisoned pool, three dead routes, zero
// host log lines.
//
// Resetting is exactly scoped: the guest's reset_state clears the node queues,
// the ingress states, the current invocation frames and the routing state
// (flow_runtime.cpp space_data_module_runtime_reset_state) and touches no
// dependency, plugin or allocator state, so a reset instance is the same
// instance with its exchange bookkeeping zeroed.
//
// The reset is CONDITIONAL and LOGGED because residue is itself a defect
// signal: an exchange that left its own request frame behind is one the flow
// never answered, and an operator has to be able to see that happening.
func (mf *MountedFlow) releaseAfterExchange(inst *flowInstance) {
	if inst != nil && inst.rt != nil {
		if queued, reset := discardIngressResidue(inst.rt, mf.triggerIndex); reset {
			log.Warnf(
				"Flow mount %q: exchange left %d unconsumed ingress frame(s) on trigger %d — "+
					"resetting the instance before it returns to the pool. An instance pooled with "+
					"residue makes the NEXT request arrive as a batched input on a single-stream "+
					"port, which flows refuse in-guest and the mount can only report as 502 "+
					"\"flow produced no HTTP response\" (graph sdn-host01-cellular-providers-502).",
				mf.mountPath, queued, mf.triggerIndex,
			)
		}
	}
	mf.release(inst)
}

// exchangeRuntime is the per-exchange runtime surface the residue guard needs.
// *FlowRuntime satisfies it; tests substitute a fake so the guarantee is
// asserted without a compiled artifact.
type exchangeRuntime interface {
	GetIngressRuntimeState(index uint32) (*FlowIngressRuntimeState, error)
	ResetState()
}

// discardIngressResidue clears the trigger queue when an exchange left frames
// on it, and reports how many it cleared.
//
// A clean exchange consumed the one frame the host enqueued for it, so anything
// still queued is residue that would batch onto the NEXT request's frame. A
// runtime that cannot answer is treated as clean: this is a diagnostic read and
// it may never turn a served request into a failed one.
func discardIngressResidue(rt exchangeRuntime, triggerIndex uint32) (uint32, bool) {
	state, err := rt.GetIngressRuntimeState(triggerIndex)
	if err != nil || state == nil || state.QueuedFrames == 0 {
		return 0, false
	}
	rt.ResetState()
	return state.QueuedFrames, true
}

// Close releases every pooled instance. Instances currently serving a
// request are released when their request finishes.
func (mf *MountedFlow) Close() {
	mf.lazyMu.Lock()
	defer mf.lazyMu.Unlock()

	mf.closeMu.Lock()
	if mf.closed {
		mf.closeMu.Unlock()
		return
	}
	mf.closed = true
	mf.closeMu.Unlock()

	if mf.pool == nil {
		return
	}

	for {
		select {
		case inst := <-mf.pool:
			if inst != nil {
				inst.rt.Release()
			}
		default:
			return
		}
	}
}

// ProgramID returns the flow's plugin/program identifier.
func (mf *MountedFlow) ProgramID() string {
	mf.lazyMu.RLock()
	defer mf.lazyMu.RUnlock()
	if mf.manifest != nil {
		return mf.manifest.PluginID
	}
	return ""
}

// AOT reports whether the pooled instances execute an AOT-compiled artifact.
func (mf *MountedFlow) AOT() bool {
	mf.lazyMu.RLock()
	defer mf.lazyMu.RUnlock()
	return mf.aot
}

// PoolSize reports the mount's instance-pool capacity.
func (mf *MountedFlow) PoolSize() int {
	mf.lazyMu.RLock()
	defer mf.lazyMu.RUnlock()
	if mf.pool != nil {
		return cap(mf.pool)
	}
	return mf.lazyDeps.PoolSize
}

// htrPipe streams the flow's $HTR egress frames to the ResponseWriter
// verbatim: the first frame carries status + headers + the first body bytes;
// any following frames append body bytes. Each frame is flushed as it
// arrives. A frame carrying a BODY_REF (out-of-band body reference, loop
// C.5c) has its body substituted from the instance's hostcall-bridge
// registry — the buffer a capability handler registered during this same
// exchange; the bytes never traversed the flow's linear memory.
//
// The bytes are the flow's, but the WIRE CONTRACT is the host's: when the
// flow labels the response JSON, the pipe will not write a body that
// contradicts that label (invalid UTF-8, or no bytes at all). See
// httpmount_json_wire.go — that guard is the whole of the host's opinion
// about a body, and it applies to every mount.
type htrPipe struct {
	w           http.ResponseWriter
	bridge      *modulert.HostBridge
	inst        *flowInstance
	method      string
	status      int
	wroteHeader bool
	// headerPending: the flow's status+headers are staged in w.Header() but
	// WriteHeader is HELD BACK because the response declares a JSON body and
	// no body byte has arrived yet. It is written the moment one does, and
	// refused at finish() if none ever does.
	headerPending bool
	// staged maps each canonical header name THIS FLOW added to the number of
	// values that name already carried BEFORE the flow ran, so a response the
	// host ends up refusing can drop the flow's fields without touching the
	// values an outer middleware (CORS, security headers) set on the same
	// writer — including for a name they BOTH set (Vary, Cache-Control), where
	// deleting by name alone silently took the middleware's value with it.
	staged map[string]int
	json   jsonWireGuard
	frames int
	err    error
}

// ensureHeader emits the staged status line, once, at the last moment.
func (p *htrPipe) ensureHeader() {
	if p.wroteHeader {
		return
	}
	p.w.WriteHeader(p.status)
	p.wroteHeader = true
	p.headerPending = false
}

// writeBody streams one run of body bytes under the wire guard.
func (p *htrPipe) writeBody(b []byte) {
	if len(b) == 0 {
		return
	}
	if p.json.active {
		if b = p.json.chunk(b); len(b) == 0 {
			return // held as a split-rune carry; flushed at finish()
		}
	}
	p.ensureHeader()
	if _, err := p.w.Write(b); err != nil && p.err == nil {
		p.err = err
	}
}

// stageHeaders writes the flow's header fields onto the writer, remembering
// what each name carried beforehand so a refusal can put it back exactly.
//
// CONTENT-LENGTH IS THE ONE FIELD THE HOST WILL NOT FORWARD UNDER THE GUARD.
// The JSON wire guard can REWRITE body bytes (one invalid byte becomes a
// three-byte U+FFFD), so a length the flow computed before that rewrite is a
// number the host is about to contradict: net/http truncates every write past
// a declared Content-Length, which would cut the body at an arbitrary offset —
// malformed JSON, possibly with a U+FFFD sliced in half, i.e. exactly the
// invalid UTF-8 the guard exists to prevent. Dropping the field lets Go frame
// the response it actually sends (chunked, or a length it computes itself).
// No flow bundle declares one today; the guard's contract is that it holds for
// every mount, including the one that will.
func (p *htrPipe) stageHeaders(headers []httpabi.Header) {
	dst := p.w.Header()
	for _, h := range headers {
		if p.json.active && strings.EqualFold(h.Name, "Content-Length") {
			continue
		}
		key := textproto.CanonicalMIMEHeaderKey(h.Name)
		if p.staged == nil {
			p.staged = make(map[string]int, len(headers))
		}
		if _, seen := p.staged[key]; !seen {
			p.staged[key] = len(dst.Values(key))
		}
		dst.Add(h.Name, h.Value)
	}
}

// dropStagedHeaders discards header fields staged for a response that will
// never be sent, so an error answer written by ServeHTTP does not inherit the
// flow's Content-Type/ETag. Each name is TRUNCATED back to the values it
// carried before the flow ran rather than deleted outright — deleting by name
// removes an outer middleware's value for that same name too.
func (p *htrPipe) dropStagedHeaders() {
	if p.wroteHeader {
		return
	}
	dst := p.w.Header()
	for key, keep := range p.staged {
		if keep == 0 {
			dst.Del(key)
			continue
		}
		if values := dst[key]; len(values) > keep {
			dst[key] = values[:keep]
		}
	}
	p.staged = nil
	p.headerPending = false
}

// finish closes the exchange: flush any carried bytes, then hold the flow to
// the contract it declared. A JSON-labelled, body-bearing response that
// produced NO bytes is not a JSON text, so the host answers an honest 502
// instead of a silent empty 200 that reads like "no records".
func (p *htrPipe) finish() {
	if p.json.active {
		if tail := p.json.flush(); len(tail) > 0 {
			p.ensureHeader()
			if _, err := p.w.Write(tail); err != nil && p.err == nil {
				p.err = err
			}
			if flusher, ok := p.w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
	if p.wroteHeader || !p.headerPending {
		return
	}
	p.dropStagedHeaders()
	http.Error(p.w,
		fmt.Sprintf("flow declared a JSON body on status %d and produced no bytes", p.status),
		http.StatusBadGateway)
	p.wroteHeader = true
}

func (p *htrPipe) emit(_ context.Context, args *InvocationArgs) (*InvocationResult, error) {
	for _, frame := range args.Frames {
		resp, err := httpabi.DecodeResponse(frame.Bytes)
		if err != nil {
			if p.err == nil {
				p.err = fmt.Errorf("egress frame is not a $HTR envelope: %w", err)
			}
			continue
		}
		p.frames++

		var refBody []byte
		if resp.BodyRefSize > 0 {
			b, ok := p.bridge.TakeBodyRef(resp.BodyRefToken)
			if !ok && p.inst != nil && resp.BodyRefToken&0xFFFFFFFF00000000 == engineBodyRefTokenMagic {
				// Engine body-reference (loop C.7 direct linkage): the bytes
				// were harvested from engine memory under the store engine
				// lock during the linked drain.
				b, ok = p.inst.takeEngineBody(resp.BodyRefToken)
			}
			if !ok || uint64(len(b)) != resp.BodyRefSize {
				if p.err == nil {
					p.err = fmt.Errorf("egress $HTR references body token %d (%d bytes) not registered on this exchange",
						resp.BodyRefToken, resp.BodyRefSize)
				}
				continue
			}
			refBody = b
		}

		if !p.wroteHeader && !p.headerPending {
			p.status = int(resp.Status)
			p.json.active = statusCarriesBody(p.status, p.method) && declaresJSONBody(resp.Headers)
			p.stageHeaders(resp.Headers)
			if p.json.active && len(resp.Body) == 0 && len(refBody) == 0 {
				// Nothing to write yet under a JSON label: stage the header
				// and decide at finish() whether this becomes a real response
				// or an honest refusal.
				p.headerPending = true
			} else {
				p.ensureHeader()
			}
		}
		p.writeBody(resp.Body)
		p.writeBody(refBody)
		if p.wroteHeader {
			if flusher, ok := p.w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
	return &InvocationResult{StatusCode: 0}, nil
}

// ServeHTTP pipes one HTTP exchange through the flow: encode the request
// verbatim as a single $HTQ ingress frame, drain the flow, stream the $HTR
// egress verbatim. Each request runs on an instance checked out of the
// mount's pool for the duration of the exchange.
func (mf *MountedFlow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if check := mf.readinessCheck(); check != nil {
		if err := check(); err != nil {
			http.Error(w, fmt.Sprintf("flow unavailable: %v", err), http.StatusServiceUnavailable)
			return
		}
	}

	if err := mf.ensureLoaded(); err != nil {
		if !errors.Is(err, ErrFlowNotInstalled) {
			log.Warnf("Flow mount %q lazy load failed: %v", mf.mountPath, err)
		}
		http.Error(w, fmt.Sprintf("flow unavailable: %v", err), http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
		return
	}

	// Headers exactly as received: lower-cased names (the schema's canonical
	// form; NAME is a FlatBuffers key field so the vector is sorted by the
	// encoder), one entry per value. Go promotes Host out of Header — restore
	// it so the module sees the full wire request.
	headers := make([]httpabi.Header, 0, len(r.Header)+1)
	if r.Host != "" {
		headers = append(headers, httpabi.Header{Name: "host", Value: r.Host})
	}
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		for _, value := range values {
			headers = append(headers, httpabi.Header{Name: lower, Value: value})
		}
	}
	sort.SliceStable(headers, func(i, j int) bool { return headers[i].Name < headers[j].Name })

	htq := httpabi.EncodeRequest(&httpabi.Request{
		Method:  r.Method,
		Path:    r.URL.EscapedPath(),
		Query:   r.URL.RawQuery,
		Headers: headers,
		Body:    body,
		Remote:  r.RemoteAddr,
	})

	inst, err := mf.acquire(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("acquire flow instance: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer func() { mf.releaseAfterExchange(inst) }()

	// Linked instances borrow the store's live engine: if the engine was
	// poisoned, recover it (idempotent), and if the engine epoch moved,
	// re-instantiate this instance against the replacement before serving
	// (loop C.7 poison recovery).
	if mf.linked {
		if engineRT, _ := mf.deps.EngineLink.EngineRuntime(); engineRT != nil && engineRT.Poisoned() {
			if _, rerr := mf.deps.EngineLink.RecoverPoisonedEngine(); rerr != nil {
				http.Error(w, fmt.Sprintf("store engine recovery failed: %v", rerr), http.StatusServiceUnavailable)
				return
			}
		}
		if epoch := mf.deps.EngineLink.EngineEpoch(); inst.engineEpoch != epoch {
			fresh, _, lerr := loadFlowInstance(mf.runBytes, mf.pages, true, mf.linkShim, mf.deps, mf.contentHash)
			if lerr != nil {
				http.Error(w, fmt.Sprintf("rebuild flow instance against replacement engine: %v", lerr), http.StatusServiceUnavailable)
				return
			}
			inst.rt.Release()
			inst = fresh
			log.Infof("Flow %q instance re-instantiated against engine epoch %d", mf.ProgramID(), epoch)
		}
	}

	rt := inst.rt
	// References are exchange-scoped: drop anything a 304/error path left
	// unconsumed before the instance goes back to the pool.
	defer inst.bridge.ResetBodyRefs()
	defer inst.resetEngineBodies()

	// One $HTQ frame into the flow's linear memory, enqueued at the ingress
	// trigger. Descriptor + NUL-terminated port id + payload are packed into
	// ONE allocation/write (one malloc + one memory write instead of three
	// each — loop C.5c host-crossing reduction). Layout:
	// [48B descriptor][port id\0][payload]. The descriptor itself is built by
	// newIngressFrameDescriptor, which is where the "no type claim" encoding is
	// stated and pinned.
	portBytes := append([]byte(mf.triggerPortID), 0)
	buf := make([]byte, flowFrameDescriptorSize+len(portBytes)+len(htq))
	copy(buf[flowFrameDescriptorSize:], portBytes)
	copy(buf[flowFrameDescriptorSize+len(portBytes):], htq)
	framePtr, err := rt.Module().AllocateSize(uint32(len(buf)))
	if err == nil {
		encodeFrameDescriptor(
			buf[:flowFrameDescriptorSize],
			newIngressFrameDescriptor(framePtr, len(portBytes), len(htq)),
		)
		if err = rt.Module().WriteMemory(framePtr, buf); err == nil {
			rt.EnqueueTriggerFrame(mf.triggerIndex, framePtr)
		}
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("enqueue request frame: %v", err), http.StatusInternalServerError)
		return
	}

	pipe := &htrPipe{w: w, bridge: inst.bridge, inst: inst, method: r.Method}
	handlers := make(HandlerMap, len(mf.egressKeys))
	for _, key := range mf.egressKeys {
		handlers[key] = pipe.emit
	}

	if _, err := rt.Drain(r.Context(), handlers, DrainOptions{MaxIterations: 1000}); err != nil && !pipe.wroteHeader {
		pipe.dropStagedHeaders()
		http.Error(w, fmt.Sprintf("flow drain: %v", err), http.StatusBadGateway)
		return
	}
	// Close the exchange under the wire contract the flow declared (carried
	// bytes flushed; a JSON label with no bytes refused honestly).
	pipe.finish()
	if !pipe.wroteHeader {
		detail := ""
		if pipe.err != nil {
			detail = ": " + pipe.err.Error()
		}
		// A mounted flow that answered nothing is a HOST-VISIBLE refusal and
		// may not be reported as a bare status line. Every node of a
		// linked-direct artifact runs inside the guest, so a node that refused
		// its input (`plugin_set_error(...); return 400`) returns through the
		// in-wasm scheduler: Drain reports no error and no handler fires. The
		// artifact's own node-state table is the only place the cause exists,
		// and the host already knows how to read it — the timer lane has since
		// e322cc4c (readNodeRunDigest). The HTTP lane threw it away, which is
		// how three live cellular routes served 502 for hours with not one log
		// line naming a node (graph sdn-host01-cellular-providers-502).
		//
		// This is a CONNECTOR read: node id, plugin, method, invocation count
		// and last status are copied out and reported. The host forms no
		// opinion about what any node means.
		log.Warnf("Flow mount %q: %s %s produced no HTTP response%s — %s",
			mf.mountPath, r.Method, r.URL.Path, detail, describeNodeRunDigest(readNodeRunDigest(rt)))
		http.Error(w, "flow produced no HTTP response"+detail, http.StatusBadGateway)
	}
}

// describeNodeRunDigest renders one drain's per-node outcome for an operator.
//
// It reports the nodes that were actually dispatched and, separately, the nodes
// that never ran at all — a flow that stalls answers nothing precisely because
// the node that would have answered was never reached, so "who did NOT run" is
// the load-bearing half of the diagnosis.
func describeNodeRunDigest(digest []nodeRunOutcome) string {
	if len(digest) == 0 {
		return "no per-node state available from the artifact"
	}
	var ran, idle []string
	for _, n := range digest {
		if n.Invocations == 0 {
			idle = append(idle, n.NodeID)
			continue
		}
		ran = append(ran, fmt.Sprintf("%s(%s:%s) x%d last_status=%d",
			n.NodeID, n.PluginID, n.MethodID, n.Invocations, n.LastStatus))
	}
	switch {
	case len(ran) == 0:
		return fmt.Sprintf("NO node ran; %d node(s) idle: %s",
			len(idle), strings.Join(idle, ", "))
	case len(idle) == 0:
		return "nodes ran: " + strings.Join(ran, "; ")
	default:
		return fmt.Sprintf("nodes ran: %s; never reached: %s",
			strings.Join(ran, "; "), strings.Join(idle, ", "))
	}
}

// newIngressFrameDescriptor builds the FlowFrameDescriptor for ONE host-pumped
// $HTQ request frame.
//
// TypeDescriptorIdx and Alignment are DECLARED here, never left at their Go
// zero value.
//
// Zero is a VALID descriptor index, so leaving the field unset did not mean
// "the host makes no type claim" — it claimed descriptor 0. The guest then
// validated the request frame against whatever edge happens to be first in the
// compiled table (flow_runtime.cpp enqueue_trigger_frame ->
// flow_binding_accepts_descriptor). Whether that passed was luck of table
// order: it held for the generation-1 bundles and broke the moment any of them
// was recompiled, turning every HTTP flow inert with 502 "flow produced no HTTP
// response". The module SDK's JS host has always written FLOW_INVALID_INDEX
// here (flowRuntimeHost.js), so the SAME artifact served correctly in the
// browser and answered 502 under WasmEdge — a cross-runtime divergence, which
// is a P1 by the module-SDK charter, not a platform quirk.
//
// Alignment 0 is the same defect one field over: the guest reads it through
// flow_effective_alignment and the JS host defaults it to 1. A $HTQ envelope
// makes no alignment claim, so it says 1 explicitly.
func newIngressFrameDescriptor(framePtr uint32, portIDLen, payloadLen int) *FlowFrameDescriptor {
	return &FlowFrameDescriptor{
		TypeDescriptorIdx: InvalidIndex,
		Alignment:         1,
		PortIDPointer:     framePtr + flowFrameDescriptorSize,
		Offset:            framePtr + flowFrameDescriptorSize + uint32(portIDLen),
		Size:              uint32(payloadLen),
		Occupied:          true,
	}
}

// RegisterFlowMounts loads every configured flow mount and registers its
// handler on the mux. A mount whose flow artifact is not installed is
// SKIPPED with an error log (module delivery may install it later); any
// other load failure — including a flow declaring a capability the host
// cannot satisfy — fails registration and closes anything already mounted.
func RegisterFlowMounts(mux *http.ServeMux, mounts []config.FlowMount, deps FlowMountDeps) ([]*MountedFlow, error) {
	mounted := make([]*MountedFlow, 0, len(mounts))
	fail := func(err error) ([]*MountedFlow, error) {
		for _, mf := range mounted {
			mf.Close()
		}
		return nil, err
	}
	for _, mount := range mounts {
		if strings.TrimSpace(mount.Path) == "" || strings.TrimSpace(mount.Flow) == "" {
			return fail(fmt.Errorf("flow mount requires both path and flow (got path=%q flow=%q)", mount.Path, mount.Flow))
		}
		mountDeps := deps
		if mount.Pool > 0 {
			mountDeps.PoolSize = mount.Pool
		} else if mountDeps.PoolSize <= 0 {
			mountDeps.PoolSize = DefaultMountPool
		}
		if mount.MemoryPages > 0 {
			mountDeps.MaxMemoryPages = mount.MemoryPages
		}
		mountDeps.NodeCtx = mountNodeContext(deps.NodeCtx, mount.Config)
		mf, err := LoadMountedFlow(mount.Flow, mountDeps)
		if err != nil {
			if errors.Is(err, ErrFlowNotInstalled) {
				log.Errorf("Flow mount %q skipped: %v", mount.Path, err)
				continue
			}
			return fail(fmt.Errorf("mount %q: %w", mount.Path, err))
		}
		mf.mountPath = mount.Path
		mux.Handle(mount.Path, mf)
		// A trailing-slash mount is a Go mux SUBTREE: without an exact-path
		// alias, GET on the bare path (e.g. /api/v1/peers for the
		// /api/v1/peers/ mount) answers 301 — and API paths never redirect
		// (docs/gateway-api.md §4.1). Register the trimmed alias to the
		// same handler; the wasm route node treats both spellings alike.
		// Best-effort: if a native exact handler already owns the bare
		// path, that registration wins and the alias is skipped.
		if trimmed := strings.TrimSuffix(mount.Path, "/"); trimmed != "" && trimmed != mount.Path {
			registerMountAlias(mux, trimmed, mf)
		}
		mounted = append(mounted, mf)
		log.Infof("Flow %q mounted at %s (pool %d, aot %v, trigger %d, egress %v)",
			mf.ProgramID(), mount.Path, mf.PoolSize(), mf.AOT(), mf.triggerIndex, mf.egressKeys)
	}
	return mounted, nil
}

// RegisterLazyFlowMounts registers configured flow mount handlers without
// instantiating their WASM modules. The first request to a mount loads the
// existing compiled artifact and precompiled AOT cache entry when available;
// daemon startup never compiles or rebuilds module artifacts.
func RegisterLazyFlowMounts(mux *http.ServeMux, mounts []config.FlowMount, deps FlowMountDeps) ([]*MountedFlow, error) {
	mounted := make([]*MountedFlow, 0, len(mounts))
	fail := func(err error) ([]*MountedFlow, error) {
		for _, mf := range mounted {
			mf.Close()
		}
		return nil, err
	}
	for _, mount := range mounts {
		if strings.TrimSpace(mount.Path) == "" || strings.TrimSpace(mount.Flow) == "" {
			return fail(fmt.Errorf("flow mount requires both path and flow (got path=%q flow=%q)", mount.Path, mount.Flow))
		}
		// Defense in depth (gap B10.2): config.Load already rejects any
		// flows.mounts[].path outside /api/ at load time, since the
		// top-level auth wall's isAPIOrPlugin check (cmd/spacedatanetwork/
		// main.go) only evaluates declared route policy for /api/ and
		// /orbpro-key-broker/ paths. Refuse here too — fail closed — so a
		// future bypass of that config-time check (a hand-built
		// FlowMountDeps caller, a validation regression, etc.) cannot
		// silently register an ungated HTTP surface.
		if !strings.HasPrefix(mount.Path, "/api/") {
			return fail(fmt.Errorf("flow mount path %q must begin with /api/ — mounts outside the auth wall's gated prefix are refused", mount.Path))
		}
		mountDeps := deps
		if mount.Pool > 0 {
			mountDeps.PoolSize = mount.Pool
		} else if mountDeps.PoolSize <= 0 {
			mountDeps.PoolSize = DefaultMountPool
		}
		if mount.MemoryPages > 0 {
			mountDeps.MaxMemoryPages = mount.MemoryPages
		}
		mountDeps.NodeCtx = mountNodeContext(deps.NodeCtx, mount.Config)
		mf := newLazyMountedFlow(mount.Flow, mountDeps)
		mf.mountPath = mount.Path
		mux.Handle(mount.Path, mf)
		if trimmed := strings.TrimSuffix(mount.Path, "/"); trimmed != "" && trimmed != mount.Path {
			registerMountAlias(mux, trimmed, mf)
		}
		mounted = append(mounted, mf)
		log.Infof("Flow %q lazy-mounted at %s (pool %d; artifact loads on first request)",
			mount.Flow, mount.Path, mf.PoolSize())
	}
	return mounted, nil
}

// mountNodeContext returns the node context one mount's flow nodes see,
// carrying that mount's config block into the builtin plugin.getConfig
// hostcall. It COPIES the shared context so one mount's config can never
// leak into another's or into the node-wide one, mirroring exactly what
// LoadFlowServices does for flows.services[].config.
//
// An empty config block returns the shared context unchanged: absence of
// configuration must stay indistinguishable from the behaviour before this
// existed.
func mountNodeContext(shared *modulert.NodeContext, cfg map[string]interface{}) *modulert.NodeContext {
	if len(cfg) == 0 {
		return shared
	}
	nodeCtx := modulert.NodeContext{}
	if shared != nil {
		nodeCtx = *shared
	}
	nodeCtx.Config = cfg
	return &nodeCtx
}

// registerMountAlias registers the exact-path alias for a subtree mount.
// http.ServeMux has no introspection API and panics on duplicate patterns,
// so a conflicting pre-existing exact route (which should win — exact
// native routes take precedence by design) is absorbed here.
func registerMountAlias(mux *http.ServeMux, pattern string, handler http.Handler) {
	defer func() {
		if r := recover(); r != nil {
			log.Warnf("Flow mount alias %q skipped (pattern already registered): %v", pattern, r)
		}
	}()
	mux.Handle(pattern, handler)
}
