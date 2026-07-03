package flowrt

// HTTP flow mounts (loop C.3d): a compiled flow bundle is just a WASM module —
// it loads through the standard flowrt instantiation path like any other
// module, with the module-SDK hostcall bridge satisfying its declared
// capabilities. There is NO gateway: the only host glue is socket plumbing
// with zero decisions —
//
//	HTTP request  → one $HTQ FlatBuffer frame (method/path/raw query/headers/
//	                body/remote, verbatim) enqueued at the flow's HTTP trigger
//	$HTR frame(s) → status/headers/body written verbatim to the
//	                ResponseWriter, flushed per frame
//
// All routing, query parsing, format selection, profile resolution, caching,
// and ETag logic live inside the wasm flow. Which flow owns which listener
// path is configuration (config.FlowMount), never Go code.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/httpabi"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// FlowMountDeps carries the host services a mounted flow's declared
// capabilities are satisfied from. Nothing here makes request-level decisions.
type FlowMountDeps struct {
	// CapRegistry provides capability handlers for the flow's manifest
	// capability set. Loading REJECTS if a declared capability has no factory.
	CapRegistry *modulert.CapabilityRegistry

	// NodeCtx is the node identity/config context exposed to built-in
	// hostcalls (plugin.getConfig, node.peerId, ...). May be empty.
	NodeCtx *modulert.NodeContext

	// MaxMemoryPages caps the flow's linear memory (64KB pages, 0 = 1024).
	MaxMemoryPages uint32

	// Store optionally resolves installed flow program IDs to artifacts.
	Store *FlowStore
}

// MountedFlow is one flow module bound to one HTTP listener path. It is the
// dumb pipe between the socket and the flow's ingress trigger / egress sink.
type MountedFlow struct {
	rt       *FlowRuntime
	manifest *modulert.Manifest

	triggerIndex  uint32
	triggerPortID string
	egressKeys    []string

	mu sync.Mutex
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
	return "", "", fmt.Errorf("flow reference %q is neither a filesystem path nor an installed flow", flowRef)
}

// LoadMountedFlow loads a compiled flow bundle through the standard flowrt
// instantiation path (WASI + flow host funcs + the module-SDK hostcall
// bridge), reads its $PLG manifest, and provisions the manifest capability set
// from the registry — rejecting the load if the host cannot satisfy a
// required capability.
func LoadMountedFlow(flowRef string, deps FlowMountDeps) (*MountedFlow, error) {
	wasmPath, bundleDir, err := resolveFlowArtifact(flowRef, deps.Store)
	if err != nil {
		return nil, err
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read flow artifact: %w", err)
	}

	pages := deps.MaxMemoryPages
	if pages == 0 {
		pages = 1024
	}

	// The hostcall bridge is created before the manifest is readable
	// (chicken-and-egg, same as modulert.Module); its capability grants are
	// applied right after the manifest parse below.
	bridge := modulert.NewHostBridge(deps.NodeCtx, nil)

	rt, err := NewFlowRuntime(wasmBytes, pages,
		wasmrt.WithHostModule(modulert.HostcallImportModule, bridge.BuildWasmEdgeHostFuncs()),
	)
	if err != nil {
		return nil, fmt.Errorf("load flow module: %w", err)
	}

	manifest, err := modulert.ReadManifest(rt.Module())
	if err != nil {
		rt.Release()
		return nil, fmt.Errorf("read flow manifest: %w", err)
	}

	if err := modulert.ProvisionBridge(bridge, deps.CapRegistry, manifest.Capabilities, nil); err != nil {
		rt.Release()
		return nil, fmt.Errorf("flow %q: %w", manifest.PluginID, err)
	}

	mf := &MountedFlow{
		rt:            rt,
		manifest:      manifest,
		triggerIndex:  0,
		triggerPortID: "request",
	}

	// Ingress trigger index + bound port come from the bundle's flow.json
	// topology when present (mechanical lookup, no interpretation).
	if bundleDir != "" {
		if data, readErr := os.ReadFile(filepath.Join(bundleDir, "flow.json")); readErr == nil {
			var topo flowBundleTopology
			if json.Unmarshal(data, &topo) == nil {
				for i, trig := range topo.Triggers {
					if trig.Kind != "http-request" {
						continue
					}
					mf.triggerIndex = uint32(i)
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
	// linked-direct model every other node runs inside the artifact and all
	// hostcalls are capabilities). Register the response pipe for each.
	for i := uint32(0); i < rt.NodeCount; i++ {
		dd, ddErr := rt.GetNodeDispatchDescriptor(i)
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
		return nil, fmt.Errorf("flow %q has no host-model egress sink to deliver HTTP responses", manifest.PluginID)
	}

	return mf, nil
}

// Close releases the flow module.
func (mf *MountedFlow) Close() {
	mf.mu.Lock()
	defer mf.mu.Unlock()
	if mf.rt != nil {
		mf.rt.Release()
		mf.rt = nil
	}
}

// ProgramID returns the flow's plugin/program identifier.
func (mf *MountedFlow) ProgramID() string {
	if mf.manifest != nil {
		return mf.manifest.PluginID
	}
	return ""
}

// Runtime returns the underlying flow runtime (tests/diagnostics).
func (mf *MountedFlow) Runtime() *FlowRuntime { return mf.rt }

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
// egress verbatim. Requests are serialized per flow instance (one linear
// memory).
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

	mf.mu.Lock()
	defer mf.mu.Unlock()

	rt := mf.rt
	if rt == nil {
		http.Error(w, "flow module is closed", http.StatusServiceUnavailable)
		return
	}

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
// handler on the mux. It fails (closing anything already mounted) if any
// mount cannot be loaded — including when a flow declares a capability the
// host cannot satisfy.
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
		mf, err := LoadMountedFlow(mount.Flow, deps)
		if err != nil {
			return fail(fmt.Errorf("mount %q: %w", mount.Path, err))
		}
		mux.Handle(mount.Path, mf)
		mounted = append(mounted, mf)
		log.Infof("Flow %q mounted at %s (trigger %d, egress %v)",
			mf.ProgramID(), mount.Path, mf.triggerIndex, mf.egressKeys)
	}
	return mounted, nil
}
