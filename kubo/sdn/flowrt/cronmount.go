package flowrt

// Timer-served flow services (loop C.8a "ingest-as-flow", ported to the kubo
// node): a compiled flow bundle whose triggers are cron TIMERS is loaded with
// WASI + the flow host funcs + the sdn/modulert hostcall bridge, its declared
// capabilities provisioned from a registry (REJECTING an unsatisfiable or
// operator-unapproved grant — fail closed), and exposed as a CronModule so the
// sdncron.Scheduler fires each timer on its effective interval. Firing a timer
// enqueues one tick frame at the trigger's bound port and drains the flow; the
// host contributes ZERO ingest decisions — fetch URLs, parsing, attribution
// and archive naming all live in the wasm nodes. The host supplies timers, the
// capability hostcalls (http.request, storage.ingest_with_source,
// plugin.getConfig) and this dumb tick pump.
//
// This is the sdn-server cronmount.go ServiceFlow adapted to kubo, plus the
// bridge-mode core of httpmount.go's loadFlowInstance. The AOT cache, HTTP
// serving/pooling and loop-C.7 direct engine linkage are intentionally left
// behind (see engine_link.go): timer-served ingest flows are bridge-mode and
// land records through the storage cap into sdnstore.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/plugins"
	"github.com/ipfs/kubo/sdn/wasmrt"
)

// FlowServiceDeps carries the host services a timer-served flow's declared
// capabilities are satisfied from. Nothing here makes request-level decisions.
type FlowServiceDeps struct {
	// CapRegistry provides capability handlers for the flow's manifest
	// capability set. Loading REJECTS if a declared capability has no factory.
	CapRegistry *modulert.CapabilityRegistry
	// NodeCtx is the node identity/config/policy context exposed to built-in
	// hostcalls (plugin.getConfig, node.peerId, ...) and the capability-policy
	// gate. May be nil.
	NodeCtx *modulert.NodeContext
	// MaxMemoryPages caps the flow instance's linear memory (64KB pages,
	// 0 => 1024).
	MaxMemoryPages uint32
}

// serviceTrigger is one TIMER trigger of a service flow: its runtime trigger
// index, bound target port, and effective interval.
type serviceTrigger struct {
	TriggerID  string
	Index      uint32
	PortID     string
	IntervalMs int
}

// flowInstance is one flow runtime plus its hostcall bridge (bridge-mode; the
// loop-C.7 direct-linkage state is not ported — see engine_link.go).
type flowInstance struct {
	rt     *FlowRuntime
	bridge *modulert.HostBridge
}

// ServiceFlow is one timer-served flow bundle exposed as a CronModule. A single
// instance serializes all trigger firings (ingest flows are sequential by
// nature; a firing that overlaps a slow predecessor waits). It satisfies
// sdncron.CronModule (ID/CronMethods/InvokeCron) and the plugins runtime
// descriptor surface, so registering it with the scheduler both fires its
// timers and lists it at GET /sdn/v1/modules.
type ServiceFlow struct {
	programID string
	name      string
	version   string
	manifest  *modulert.Manifest
	triggers  []serviceTrigger
	egress    []string
	// sourceProviderPluginIDs is the plugin id of every $PLG "source"-kind node
	// in the bundle's topology (the flow's declared providers), captured at
	// load from flow.plg — read-only settings-surface metadata, never used to
	// gate a fetch. Nil for a bundle with no source nodes or no bundle dir.
	sourceProviderPluginIDs []string

	mu   sync.Mutex
	inst *flowInstance

	statsMu         sync.Mutex
	startedAt       time.Time
	timerRunCount   uint64
	errorCount      uint64
	lastTimerStatus string
	lastInvokeAt    time.Time
}

// resolveFlowArtifact maps a flow reference (a bundle directory or a direct
// runtime.wasm path) to the artifact wasm path and the bundle directory holding
// flow.plg.
func resolveFlowArtifact(flowRef string) (wasmPath, bundleDir string, err error) {
	info, statErr := os.Stat(flowRef)
	if statErr != nil {
		return "", "", fmt.Errorf("flow reference %q is not a filesystem path: %w", flowRef, statErr)
	}
	if info.IsDir() {
		return filepath.Join(flowRef, "runtime.wasm"), flowRef, nil
	}
	return flowRef, filepath.Dir(flowRef), nil
}

// loadFlowInstance instantiates one flow instance: standard flowrt load (WASI +
// flow host funcs + the module-SDK hostcall bridge), manifest read, and
// capability provisioning from the registry — REJECTING the load if the host
// cannot satisfy a required capability OR if the flow bundle requests a
// sensitive capability the operator has not approved for its content hash
// (default-deny, fail closed). Bridge-mode only: a bundle that imports the
// store engine ("flatsql") is rejected by the caller before this runs.
func loadFlowInstance(wasmBytes []byte, pages uint32, deps FlowServiceDeps, contentHash, flowID string, declaredCaps []string) (*flowInstance, *modulert.Manifest, error) {
	bridge := modulert.NewHostBridge(deps.NodeCtx, nil)

	rt, err := NewFlowRuntime(wasmBytes, pages,
		wasmrt.WithHostModule(modulert.HostcallImportModule, bridge.BuildWasmEdgeHostFuncs()))
	if err != nil {
		return nil, nil, fmt.Errorf("load flow module: %w", err)
	}

	var manifest *modulert.Manifest
	var capabilities []string
	if wasmImportsModule(wasmBytes, engineImportModule) {
		// Composed engine-linked flow (the OD write lane): it is a flow REACTOR,
		// not a single module, so it exposes no plugin-manifest ABI (ReadManifest
		// would fail). Its identity comes from the $PLG (flowID) and its declared
		// capability set from the install spec — the first-party role reference set
		// that also recorded the operator approval for THIS content hash. The mount
		// provisions EXACTLY those declared caps. The engine-link itself is wired by
		// NewFlowRuntime (the in-wasm FlatSQL store), never a bridge capability, so
		// EngineLinkCapability must not appear in declaredCaps.
		for _, c := range declaredCaps {
			if c == EngineLinkCapability {
				rt.Release()
				return nil, nil, fmt.Errorf("flow %q: %s is not a bridge capability (the engine link is wired by NewFlowRuntime)", flowID, EngineLinkCapability)
			}
		}
		manifest = &modulert.Manifest{PluginID: flowID, Capabilities: append([]string(nil), declaredCaps...)}
		capabilities = manifest.Capabilities
	} else {
		manifest, err = modulert.ReadManifest(rt.Module())
		if err != nil {
			rt.Release()
			return nil, nil, fmt.Errorf("read flow manifest: %w", err)
		}
		// The engine-link capability would be satisfied by a mount's engine link,
		// not a hostcall handler — a non-engine-linked bridge-mode service flow that
		// declares it is mis-compiled for this path.
		capabilities = make([]string, 0, len(manifest.Capabilities))
		for _, capability := range manifest.Capabilities {
			if capability == EngineLinkCapability {
				rt.Release()
				return nil, nil, fmt.Errorf("flow %q declares %s; engine-linked flows are not supported on the timer-served path (bridge-mode only)", manifest.PluginID, EngineLinkCapability)
			}
			capabilities = append(capabilities, capability)
		}
	}

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

	return &flowInstance{rt: rt, bridge: bridge}, manifest, nil
}

// LoadFlowService loads one timer-served flow bundle as a ServiceFlow. flowRef
// is a bundle directory (or a direct runtime.wasm path). intervals maps
// triggerId -> Go duration string to override the bundle default; config is the
// per-flow node CONFIG the bridge builtin plugin.getConfig serves the flow's
// nodes (URL overrides etc. — configuration, never host code).
func LoadFlowService(flowRef string, intervals map[string]string, config map[string]interface{}, deps FlowServiceDeps, declaredCaps []string) (*ServiceFlow, error) {
	wasmPath, bundleDir, err := resolveFlowArtifact(flowRef)
	if err != nil {
		return nil, err
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read flow artifact: %w", err)
	}

	// Publication-trailer signature gate (nil policy is inert: strips the
	// trailer, requires no signature) — sourced from NodeCtx.ModuleSignaturePolicy.
	var sigPolicy *modulert.ModuleSignaturePolicy
	if deps.NodeCtx != nil {
		sigPolicy = deps.NodeCtx.ModuleSignaturePolicy
	}
	portableBytes, _, sigErr := modulert.EnforceModuleSignaturePolicy(sigPolicy, wasmBytes)
	if sigErr != nil {
		return nil, fmt.Errorf("flow service %q: %w", flowRef, sigErr)
	}
	wasmBytes = portableBytes

	// Content-hash identity for the operator capability-policy gate (computed
	// on the portable bytes, before any AOT compile — matches what an operator
	// hashes to record an approval).
	contentHash := modulert.ContentHashHex(wasmBytes)

	// Engine-linked wasi-threads flows (the supplemental-OMM OD write lane) are
	// ADMITTED: NewFlowRuntime attaches a dedicated-thread in-wasm FlatSQL engine
	// and resolves flatsql.exec_envelope to the store trampoline (all record logic
	// in-wasm; host moves opaque bytes). Non-threaded engine-linked flows are not
	// a supported shape and will fail to instantiate cleanly at load.
	if wasmImportsModule(wasmBytes, engineImportModule) {
		log.Infof("flow service %q is engine-linked (imports %q) — in-wasm FlatSQL store wired at load", flowRef, engineImportModule)
	}

	pages := deps.MaxMemoryPages
	if pages == 0 {
		pages = 1024
	}

	// Per-flow node CONFIG: clone the NodeCtx so plugin.getConfig serves this
	// flow's config block without mutating the shared context.
	instDeps := deps
	if len(config) > 0 {
		nodeCtx := modulert.NodeContext{}
		if deps.NodeCtx != nil {
			nodeCtx = *deps.NodeCtx
		}
		nodeCtx.Config = config
		instDeps.NodeCtx = &nodeCtx
	}

	// Engine-linked composed flows carry no module manifest; their identity is the
	// $PLG ProgramID. Read it up front for the mount's provision identity.
	flowID := ""
	if bundleDir != "" {
		if data, e := os.ReadFile(filepath.Join(bundleDir, "flow.plg")); e == nil {
			if topo, pe := parsePLGGraph(data); pe == nil {
				flowID = topo.ProgramID
			}
		}
	}

	inst, manifest, err := loadFlowInstance(wasmBytes, pages, instDeps, contentHash, flowID, declaredCaps)
	if err != nil {
		return nil, err
	}

	sf := &ServiceFlow{
		programID:       manifest.PluginID,
		name:            manifest.Name,
		version:         manifest.Version,
		manifest:        manifest,
		inst:            inst,
		lastTimerStatus: "never-run",
	}

	// Timer triggers + bound ports come from the bundle's flow.plg ($PLG
	// FlatBuffer) topology (mechanical lookup, no interpretation).
	if bundleDir != "" {
		if data, readErr := os.ReadFile(filepath.Join(bundleDir, "flow.plg")); readErr == nil {
			if topo, perr := parsePLGGraph(data); perr == nil {
				if topo.ProgramID != "" {
					sf.programID = topo.ProgramID
				}
				if topo.Name != "" {
					sf.name = topo.Name
				}
				if topo.Version != "" {
					sf.version = topo.Version
				}
				for ti, trig := range topo.Triggers {
					if trig.Kind != "timer" {
						continue
					}
					st := serviceTrigger{
						TriggerID:  trig.TriggerID,
						Index:      uint32(ti),
						PortID:     "tick",
						IntervalMs: trig.DefaultIntervalMs,
					}
					for _, binding := range topo.TriggerBindings {
						if binding.TriggerID == trig.TriggerID && binding.TargetPortID != "" {
							st.PortID = binding.TargetPortID
						}
					}
					if override, ok := intervals[trig.TriggerID]; ok {
						d, derr := time.ParseDuration(override)
						if derr != nil {
							inst.rt.Release()
							return nil, fmt.Errorf("flow service %q: invalid interval override for trigger %q: %v", flowRef, trig.TriggerID, derr)
						}
						st.IntervalMs = int(d.Milliseconds())
					}
					sf.triggers = append(sf.triggers, st)
				}
				// Declared providers: every "source"-kind node's plugin id, in
				// topology order — settings-surface read metadata only (e.g. the
				// operator config panel's default enabled set). Never consulted to
				// gate a fetch; the flow always drives every source node it wires.
				for _, n := range topo.Nodes {
					if n.Kind == "source" && n.PluginID != "" {
						sf.sourceProviderPluginIDs = append(sf.sourceProviderPluginIDs, n.PluginID)
					}
				}
			}
		}
	}
	if len(sf.triggers) == 0 {
		inst.rt.Release()
		return nil, fmt.Errorf("flow service %q declares no timer triggers (bundle flow.plg required)", flowRef)
	}

	// Egress sinks = the artifact's host-dispatch nodes.
	for ni := uint32(0); ni < inst.rt.NodeCount; ni++ {
		dd, ddErr := inst.rt.GetNodeDispatchDescriptor(ni)
		if ddErr != nil {
			continue
		}
		if inst.rt.readCStringAt(dd.DispatchModelPointer) != "host" {
			continue
		}
		pluginID := inst.rt.readCStringAt(dd.PluginIDPointer)
		methodID := inst.rt.readCStringAt(dd.MethodIDPointer)
		if pluginID != "" && methodID != "" {
			sf.egress = append(sf.egress, pluginID+":"+methodID)
		}
	}

	return sf, nil
}

// --- plugins.Plugin interface ---

func (sf *ServiceFlow) ID() string { return sf.programID }

func (sf *ServiceFlow) Start(ctx context.Context, runtime plugins.RuntimeContext) error {
	sf.statsMu.Lock()
	sf.startedAt = time.Now().UTC()
	sf.statsMu.Unlock()
	log.Infof("Flow service %q started (%d timer triggers, egress %v)", sf.programID, len(sf.triggers), sf.egress)
	return nil
}

func (sf *ServiceFlow) RegisterRoutes(mux *http.ServeMux) {}

func (sf *ServiceFlow) Close() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.inst != nil {
		sf.inst.rt.Release()
		sf.inst = nil
	}
	return nil
}

// --- sdncron.CronModule / plugins.CronProvider interface ---

func (sf *ServiceFlow) CronMethods() []plugins.CronMethodSpec {
	specs := make([]plugins.CronMethodSpec, 0, len(sf.triggers))
	for _, trigger := range sf.triggers {
		interval := fmt.Sprintf("%dms", trigger.IntervalMs)
		if trigger.IntervalMs >= 1000 {
			interval = fmt.Sprintf("%ds", trigger.IntervalMs/1000)
		}
		specs = append(specs, plugins.CronMethodSpec{
			Method:          trigger.TriggerID,
			Description:     fmt.Sprintf("Flow timer trigger: %s", trigger.TriggerID),
			DefaultInterval: interval,
			Input:           "none",
			Output:          "json",
		})
	}
	return specs
}

// InvokeCron fires one timer trigger (the scheduled-fire entry point): a tick
// JSON frame enters the bound port, the flow drains, and every egress result
// frame is collected into the returned summary.
func (sf *ServiceFlow) InvokeCron(ctx context.Context, method string, _ []byte) ([]byte, error) {
	out, err := sf.FireTrigger(ctx, method)
	sf.recordTimerResult(err)
	return out, err
}

// FireTrigger runs one named timer trigger to completion (also the direct entry
// point for tests and the admin surface).
func (sf *ServiceFlow) FireTrigger(ctx context.Context, triggerID string) ([]byte, error) {
	var trigger *serviceTrigger
	for i := range sf.triggers {
		if sf.triggers[i].TriggerID == triggerID {
			trigger = &sf.triggers[i]
			break
		}
	}
	if trigger == nil {
		return nil, fmt.Errorf("flow service %q has no timer trigger %q", sf.programID, triggerID)
	}

	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.inst == nil {
		return nil, fmt.Errorf("flow service %q is closed", sf.programID)
	}
	rt := sf.inst.rt
	defer sf.inst.bridge.ResetBodyRefs()

	tick, _ := json.Marshal(map[string]string{
		"trigger": trigger.TriggerID,
		"firedAt": time.Now().UTC().Format(time.RFC3339),
	})

	// One tick frame into the flow's linear memory at the trigger's bound port
	// ([48B descriptor][port id\0][payload]).
	portBytes := append([]byte(trigger.PortID), 0)
	buf := make([]byte, flowFrameDescriptorSize+len(portBytes)+len(tick))
	copy(buf[flowFrameDescriptorSize:], portBytes)
	copy(buf[flowFrameDescriptorSize+len(portBytes):], tick)
	framePtr, err := rt.Module().AllocateSize(uint32(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("allocate tick frame: %w", err)
	}
	encodeFrameDescriptor(buf[:flowFrameDescriptorSize], &FlowFrameDescriptor{
		PortIDPointer: framePtr + flowFrameDescriptorSize,
		Offset:        framePtr + flowFrameDescriptorSize + uint32(len(portBytes)),
		Size:          uint32(len(tick)),
		Occupied:      true,
	})
	if err := rt.Module().WriteMemory(framePtr, buf); err != nil {
		return nil, fmt.Errorf("write tick frame: %w", err)
	}
	rt.EnqueueTriggerFrame(trigger.Index, framePtr)

	// Drain, collecting every egress result frame verbatim.
	var results []json.RawMessage
	collect := func(_ context.Context, args *InvocationArgs) (*InvocationResult, error) {
		for _, frame := range args.Frames {
			if json.Valid(frame.Bytes) {
				results = append(results, json.RawMessage(append([]byte(nil), frame.Bytes...)))
			} else {
				encoded, _ := json.Marshal(string(frame.Bytes))
				results = append(results, json.RawMessage(encoded))
			}
		}
		return &InvocationResult{StatusCode: 0}, nil
	}
	handlers := make(HandlerMap, len(sf.egress))
	for _, key := range sf.egress {
		handlers[key] = collect
	}

	if _, err := rt.Drain(ctx, handlers, DrainOptions{MaxIterations: 1000}); err != nil {
		return nil, fmt.Errorf("flow service %q trigger %q: %w", sf.programID, triggerID, err)
	}

	// Persist the engine-linked store's arena after a drain that wrote records
	// (opaque whole-arena snapshot via the fs connector; no-op for non-store
	// flows). Best-effort: a snapshot failure does not fail the fired trigger —
	// the rows are still live in-process and the next fire re-snapshots.
	if serr := rt.SnapshotStore(); serr != nil {
		log.Warnf("flow service %q: store snapshot after trigger %q failed: %v", sf.programID, triggerID, serr)
	}

	summary, _ := json.Marshal(map[string]interface{}{
		"trigger": triggerID,
		"results": results,
	})
	return summary, nil
}

func (sf *ServiceFlow) recordTimerResult(err error) {
	sf.statsMu.Lock()
	defer sf.statsMu.Unlock()
	sf.timerRunCount++
	sf.lastInvokeAt = time.Now().UTC()
	if err != nil {
		sf.errorCount++
		sf.lastTimerStatus = "error"
	} else {
		sf.lastTimerStatus = "ok"
	}
}

// Name / Version expose the bundle display metadata (for scheduler
// registration and the module list).
func (sf *ServiceFlow) Name() string    { return sf.name }
func (sf *ServiceFlow) Version() string { return sf.version }

// Triggers lists the service's timer triggers (id + effective interval).
func (sf *ServiceFlow) Triggers() []serviceTrigger {
	out := make([]serviceTrigger, len(sf.triggers))
	copy(out, sf.triggers)
	return out
}

// SourceProviderPluginIDs lists the plugin id of every $PLG "source"-kind node
// in the bundle's topology (the flow's declared providers), in topology order.
// Read-only settings-surface metadata (e.g. the operator config panel's
// default enabled set) — never used to gate a fetch.
func (sf *ServiceFlow) SourceProviderPluginIDs() []string {
	out := make([]string, len(sf.sourceProviderPluginIDs))
	copy(out, sf.sourceProviderPluginIDs)
	return out
}

// SetNodeConfig replaces the per-flow node CONFIG served to this flow's wasm
// nodes via plugin.getConfig, effective from the NEXT trigger fire onward (it
// never forces one). This is the operator settings API's write path for
// opaque, module-defined tuning keys (e.g. enabled_providers) — the reserved
// scheduling keys (interval_ms/timers) stay the cron scheduler's concern, not
// this flow's node CONFIG. A no-op when the flow is closed.
func (sf *ServiceFlow) SetNodeConfig(cfg map[string]interface{}) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.inst != nil {
		sf.inst.bridge.SetConfigLive(cfg)
	}
}

// --- plugins.UIProvider interface ---

func (sf *ServiceFlow) UIDescriptor() plugins.UIDescriptor {
	name := sf.name
	if name == "" {
		name = sf.programID
	}
	return plugins.UIDescriptor{
		Title:       name,
		Description: fmt.Sprintf("Timer-served flow service %s", sf.programID),
		Icon:        "⏱",
		Color:       "#0ea5e9",
		TextColor:   "#ffffff",
	}
}

// RuntimeDescriptor summarizes the service for the dashboard.
func (sf *ServiceFlow) RuntimeDescriptor() plugins.RuntimeModuleDescriptor {
	descriptor := plugins.RuntimeModuleDescriptor{
		Manifest: &plugins.RuntimeModuleManifest{
			PluginID:     sf.programID,
			Name:         sf.name,
			Version:      sf.version,
			PluginFamily: "FLOW",
		},
	}
	for _, trigger := range sf.triggers {
		descriptor.Manifest.Timers = append(descriptor.Manifest.Timers, plugins.RuntimeModuleTimer{
			TimerID:           trigger.TriggerID,
			MethodID:          trigger.TriggerID,
			DefaultIntervalMs: uint64(nonNegativeInt(trigger.IntervalMs)),
			Description:       fmt.Sprintf("timer trigger: %s", trigger.TriggerID),
		})
	}
	sf.statsMu.Lock()
	if !sf.startedAt.IsZero() {
		descriptor.Stats.UptimeMs = time.Since(sf.startedAt).Milliseconds()
	}
	descriptor.Stats.TimerRunCount = sf.timerRunCount
	descriptor.Stats.ErrorCount = sf.errorCount
	descriptor.Stats.LastTimerStatus = sf.lastTimerStatus
	if !sf.lastInvokeAt.IsZero() {
		descriptor.Stats.LastInvokeAt = sf.lastInvokeAt.UTC().Format(time.RFC3339)
	}
	sf.statsMu.Unlock()
	sf.mu.Lock()
	if sf.inst != nil && sf.inst.rt != nil && sf.inst.rt.Module() != nil {
		if stats, err := sf.inst.rt.Module().MemoryStats(); err == nil {
			descriptor.Stats.MemoryPages = stats.Pages
			descriptor.Stats.MemoryBytes = stats.Bytes
			descriptor.Stats.MaxMemoryPages = stats.MaxPages
			descriptor.Stats.MaxMemoryBytes = stats.MaxBytes
		}
	}
	sf.mu.Unlock()
	return descriptor
}
