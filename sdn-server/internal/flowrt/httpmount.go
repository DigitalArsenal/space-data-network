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

// flowAOTCachePrefix names flow-mount AOT artifacts inside a shared AOT
// cache directory (distinct from the engine's "flatsql-" prefix).
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
	// hostcalls (plugin.getConfig, node.peerId, ...). May be empty.
	NodeCtx *modulert.NodeContext

	// MaxMemoryPages caps each flow instance's linear memory (64KB pages,
	// 0 = 1024). Per-mount config.FlowMount.MemoryPages overrides it.
	MaxMemoryPages uint32

	// PoolSize is the number of flow instances to load for the mount
	// (<= 0: DefaultMountPool via RegisterFlowMounts, 1 when LoadMountedFlow
	// is called directly).
	PoolSize int

	// AOTCacheDir, when set, AOT-compiles the flow artifact through the same
	// sha256-keyed disk cache the FlatSQL engine uses (flatsqlrt
	// EnsureAOTArtifact). Compile failure falls back to interpretation.
	AOTCacheDir string

	// Store optionally resolves installed flow program IDs to artifacts.
	Store *FlowStore

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

	triggerIndex  uint32
	triggerPortID string
	egressKeys    []string

	// runBytes/pages/deps/linkShim are retained so linked instances can be
	// rebuilt against a replacement engine after poisoning (loop C.7).
	runBytes []byte
	pages    uint32
	deps     FlowMountDeps
	linkShim []byte

	pool chan *flowInstance

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
	linked      bool
	engineEpoch uint64                // store engine epoch this instance was built against
	engineRT    *flatsqlrt.Runtime    // the borrowed live engine
	engineDB    *flatsqlrt.Database   // its database (lock + harvest surface)
	refTablePtr uint32                // sdn_flatsql_link_ref_table (static)
	refSlots    uint32                // sdn_flatsql_link_ref_slots
	engineMu    sync.Mutex            // guards engineBodies (egress runs on the request goroutine)
	engineBodies map[uint64][]byte    // exchange-scoped harvested bodies by token
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
// the host cannot satisfy a required capability.
//
// Engine-linked artifacts (they import the "flatsql" wasm module — loop C.7)
// additionally get the store's LIVE engine instance and the flatsql_link
// shim registered into their VM before instantiation, the store db handle
// wired in, and a linked-drain section that holds the store engine lock
// around every in-wasm drain and harvests engine body-references before
// releasing it. Loading a linked artifact WITHOUT deps.EngineLink is a hard
// error (first-party grant, never inferred).
func loadFlowInstance(wasmBytes []byte, pages uint32, linked bool, linkShim []byte, deps FlowMountDeps) (*flowInstance, *modulert.Manifest, error) {
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
	if err := modulert.ProvisionBridge(bridge, deps.CapRegistry, capabilities, nil); err != nil {
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
// identical instances (minimum 1). When deps.AOTCacheDir is set the artifact
// is AOT-compiled through the shared cache first; on compile failure the
// portable bytes are interpreted.
func LoadMountedFlow(flowRef string, deps FlowMountDeps) (*MountedFlow, error) {
	wasmPath, bundleDir, err := resolveFlowArtifact(flowRef, deps.Store)
	if err != nil {
		return nil, err
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read flow artifact: %w", err)
	}
	// Published artifacts carry an appended SDS $REC publication trailer
	// (MBL+PNM per the module publication standard); the runtime payload is
	// the bytes before it. Stripping BEFORE the AOT cache keeps the cache
	// keyed on the executable payload, so a precompiled artifact shipped
	// into the cache dir matches regardless of publication metadata.
	wasmBytes = modulert.StripPublicationTrailer(wasmBytes)

	runBytes := wasmBytes
	aot := false
	if deps.AOTCacheDir != "" {
		if compiled, aotErr := flatsqlrt.EnsureAOTArtifact(deps.AOTCacheDir, flowAOTCachePrefix, wasmBytes); aotErr == nil {
			runBytes = compiled
			aot = true
		} else {
			log.Warnf("Flow mount %q: AOT compile failed, interpreting: %v", flowRef, aotErr)
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
		if compiledShim, shimErr := flatsqlrt.EnsureAOTArtifact(deps.AOTCacheDir, "flatsqllink", flatsqlLinkShimWasm); shimErr == nil {
			linkShim = compiledShim
		} else {
			log.Warnf("Flow mount %q: link-shim AOT compile failed, interpreting shim: %v", flowRef, shimErr)
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
		pool:          make(chan *flowInstance, poolSize),
	}

	for i := 0; i < poolSize; i++ {
		inst, manifest, err := loadFlowInstance(runBytes, pages, linked, linkShim, deps)
		if err != nil {
			mf.Close()
			return nil, err
		}
		rt := inst.rt
		if mf.manifest == nil {
			mf.manifest = manifest

			// Ingress trigger index + bound port come from the bundle's
			// flow.json topology when present (mechanical lookup, no
			// interpretation).
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

// Close releases every pooled instance. Instances currently serving a
// request are released when their request finishes.
func (mf *MountedFlow) Close() {
	mf.closeMu.Lock()
	if mf.closed {
		mf.closeMu.Unlock()
		return
	}
	mf.closed = true
	mf.closeMu.Unlock()

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
	if mf.manifest != nil {
		return mf.manifest.PluginID
	}
	return ""
}

// AOT reports whether the pooled instances execute an AOT-compiled artifact.
func (mf *MountedFlow) AOT() bool { return mf.aot }

// PoolSize reports the mount's instance-pool capacity.
func (mf *MountedFlow) PoolSize() int { return cap(mf.pool) }

// htrPipe streams the flow's $HTR egress frames to the ResponseWriter
// verbatim: the first frame carries status + headers + the first body bytes;
// any following frames append body bytes. Each frame is flushed as it
// arrives. A frame carrying a BODY_REF (out-of-band body reference, loop
// C.5c) has its body substituted from the instance's hostcall-bridge
// registry — the buffer a capability handler registered during this same
// exchange; the bytes never traversed the flow's linear memory.
type htrPipe struct {
	w           http.ResponseWriter
	bridge      *modulert.HostBridge
	inst        *flowInstance
	wroteHeader bool
	frames      int
	err         error
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

		if !p.wroteHeader {
			for _, h := range resp.Headers {
				p.w.Header().Add(h.Name, h.Value)
			}
			p.w.WriteHeader(int(resp.Status))
			p.wroteHeader = true
		}
		if len(resp.Body) > 0 {
			if _, err := p.w.Write(resp.Body); err != nil && p.err == nil {
				p.err = err
			}
		}
		if len(refBody) > 0 {
			if _, err := p.w.Write(refBody); err != nil && p.err == nil {
				p.err = err
			}
		}
		if flusher, ok := p.w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	return &InvocationResult{StatusCode: 0}, nil
}

// ServeHTTP pipes one HTTP exchange through the flow: encode the request
// verbatim as a single $HTQ ingress frame, drain the flow, stream the $HTR
// egress verbatim. Each request runs on an instance checked out of the
// mount's pool for the duration of the exchange.
func (mf *MountedFlow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	defer func() { mf.release(inst) }()

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
			fresh, _, lerr := loadFlowInstance(mf.runBytes, mf.pages, true, mf.linkShim, mf.deps)
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
	// [48B descriptor][port id\0][payload].
	portBytes := append([]byte(mf.triggerPortID), 0)
	buf := make([]byte, flowFrameDescriptorSize+len(portBytes)+len(htq))
	copy(buf[flowFrameDescriptorSize:], portBytes)
	copy(buf[flowFrameDescriptorSize+len(portBytes):], htq)
	framePtr, err := rt.Module().AllocateSize(uint32(len(buf)))
	if err == nil {
		encodeFrameDescriptor(buf[:flowFrameDescriptorSize], &FlowFrameDescriptor{
			PortIDPointer: framePtr + flowFrameDescriptorSize,
			Offset:        framePtr + flowFrameDescriptorSize + uint32(len(portBytes)),
			Size:          uint32(len(htq)),
			Occupied:      true,
		})
		if err = rt.Module().WriteMemory(framePtr, buf); err == nil {
			rt.EnqueueTriggerFrame(mf.triggerIndex, framePtr)
		}
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("enqueue request frame: %v", err), http.StatusInternalServerError)
		return
	}

	pipe := &htrPipe{w: w, bridge: inst.bridge, inst: inst}
	handlers := make(HandlerMap, len(mf.egressKeys))
	for _, key := range mf.egressKeys {
		handlers[key] = pipe.emit
	}

	if _, err := rt.Drain(r.Context(), handlers, DrainOptions{MaxIterations: 1000}); err != nil && !pipe.wroteHeader {
		http.Error(w, fmt.Sprintf("flow drain: %v", err), http.StatusBadGateway)
		return
	}
	if !pipe.wroteHeader {
		detail := ""
		if pipe.err != nil {
			detail = ": " + pipe.err.Error()
		}
		http.Error(w, "flow produced no HTTP response"+detail, http.StatusBadGateway)
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
		mf, err := LoadMountedFlow(mount.Flow, mountDeps)
		if err != nil {
			if errors.Is(err, ErrFlowNotInstalled) {
				log.Errorf("Flow mount %q skipped: %v", mount.Path, err)
				continue
			}
			return fail(fmt.Errorf("mount %q: %w", mount.Path, err))
		}
		mux.Handle(mount.Path, mf)
		mounted = append(mounted, mf)
		log.Infof("Flow %q mounted at %s (pool %d, aot %v, trigger %d, egress %v)",
			mf.ProgramID(), mount.Path, mf.PoolSize(), mf.AOT(), mf.triggerIndex, mf.egressKeys)
	}
	return mounted, nil
}
