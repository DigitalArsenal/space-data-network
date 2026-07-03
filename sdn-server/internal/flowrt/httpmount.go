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
}

// MountedFlow is one flow module bound to one HTTP listener path, served by a
// pool of identical instances. It is the dumb pipe between the socket and
// the flow's ingress trigger / egress sink.
type MountedFlow struct {
	manifest *modulert.Manifest
	aot      bool

	triggerIndex  uint32
	triggerPortID string
	egressKeys    []string

	pool chan *FlowRuntime

	closeMu sync.Mutex
	closed  bool
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
func loadFlowInstance(wasmBytes []byte, pages uint32, deps FlowMountDeps) (*FlowRuntime, *modulert.Manifest, error) {
	// The hostcall bridge is created before the manifest is readable
	// (chicken-and-egg, same as modulert.Module); its capability grants are
	// applied right after the manifest parse below.
	bridge := modulert.NewHostBridge(deps.NodeCtx, nil)

	rt, err := NewFlowRuntime(wasmBytes, pages,
		wasmrt.WithHostModule(modulert.HostcallImportModule, bridge.BuildWasmEdgeHostFuncs()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("load flow module: %w", err)
	}

	manifest, err := modulert.ReadManifest(rt.Module())
	if err != nil {
		rt.Release()
		return nil, nil, fmt.Errorf("read flow manifest: %w", err)
	}

	if err := modulert.ProvisionBridge(bridge, deps.CapRegistry, manifest.Capabilities, nil); err != nil {
		rt.Release()
		return nil, nil, fmt.Errorf("flow %q: %w", manifest.PluginID, err)
	}
	return rt, manifest, nil
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

	mf := &MountedFlow{
		aot:           aot,
		triggerIndex:  0,
		triggerPortID: "request",
		pool:          make(chan *FlowRuntime, poolSize),
	}

	for i := 0; i < poolSize; i++ {
		rt, manifest, err := loadFlowInstance(runBytes, pages, deps)
		if err != nil {
			mf.Close()
			return nil, err
		}
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
		mf.pool <- rt
	}

	return mf, nil
}

// acquire checks an idle instance out of the pool, waiting until one frees
// up or the request context ends.
func (mf *MountedFlow) acquire(ctx context.Context) (*FlowRuntime, error) {
	select {
	case rt, ok := <-mf.pool:
		if !ok || rt == nil {
			return nil, errors.New("flow module is closed")
		}
		return rt, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// release returns an instance to the pool (or frees it if the mount closed
// while the request was in flight).
func (mf *MountedFlow) release(rt *FlowRuntime) {
	mf.closeMu.Lock()
	closed := mf.closed
	mf.closeMu.Unlock()
	if closed {
		rt.Release()
		return
	}
	mf.pool <- rt
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
		case rt := <-mf.pool:
			if rt != nil {
				rt.Release()
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
// arrives.
type htrPipe struct {
	w           http.ResponseWriter
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

	rt, err := mf.acquire(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("acquire flow instance: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer mf.release(rt)

	// One $HTQ frame into the flow's linear memory, enqueued at the ingress
	// trigger.
	payloadPtr, err := rt.Module().Allocate(htq)
	if err == nil {
		var portPtr, framePtr uint32
		if portPtr, err = rt.Module().AllocateString(mf.triggerPortID); err == nil {
			if framePtr, err = rt.Module().AllocateSize(flowFrameDescriptorSize); err == nil {
				err = writeFrameDescriptor(rt.Module(), framePtr, &FlowFrameDescriptor{
					PortIDPointer: portPtr,
					Offset:        payloadPtr,
					Size:          uint32(len(htq)),
					Occupied:      true,
				})
				if err == nil {
					rt.EnqueueTriggerFrame(mf.triggerIndex, framePtr)
				}
			}
		}
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("enqueue request frame: %v", err), http.StatusInternalServerError)
		return
	}

	pipe := &htrPipe{w: w}
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
