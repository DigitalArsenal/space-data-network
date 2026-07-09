package flowrt

// Timer-served flow services (loop C.8a "ingest-as-flow"): a compiled flow
// bundle whose triggers are cron TIMERS is loaded exactly like an HTTP flow
// mount (WASI + flow host funcs + the module-SDK hostcall bridge, capability
// provisioning REJECTING unsatisfiable grants, shared AOT cache) and exposed
// to the SDN plugin manager as a CronProvider: each timer trigger becomes a
// cron method; firing it enqueues one tick frame at the trigger's bound port
// and drains the flow. The host contributes ZERO ingest decisions — fetch
// URLs, parsing, attribution, reconcile modes and archive naming all live in
// the wasm nodes; the host supplies timers, capability hostcalls
// (http.request, storage.ingest_with_source, plugin.getConfig) and this
// dumb tick pump.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// serviceTrigger is one TIMER trigger of a service flow: its runtime trigger
// index, bound target port, and effective interval.
type serviceTrigger struct {
	TriggerID  string
	Index      uint32
	PortID     string
	IntervalMs int
}

// ServiceFlow is one timer-served flow bundle registered as an SDN plugin.
// A single instance serializes all trigger firings (ingest flows are
// sequential by nature; a firing that overlaps a slow predecessor waits).
type ServiceFlow struct {
	programID string
	manifest  *modulert.Manifest
	aot       bool
	triggers  []serviceTrigger
	egress    []string

	mu   sync.Mutex
	inst *flowInstance

	statsMu         sync.Mutex
	startedAt       time.Time
	timerRunCount   uint64
	errorCount      uint64
	lastTimerStatus string
	lastInvokeAt    time.Time
}

// serviceBundleTopology is the flow.json subset needed for timer triggers.
type serviceBundleTopology struct {
	ProgramID string `json:"programId"`
	Triggers  []struct {
		TriggerID         string `json:"triggerId"`
		Kind              string `json:"kind"`
		DefaultIntervalMs int    `json:"defaultIntervalMs"`
	} `json:"triggers"`
	TriggerBindings []struct {
		TriggerID    string `json:"triggerId"`
		TargetPortID string `json:"targetPortId"`
	} `json:"triggerBindings"`
}

// LoadFlowService loads one timer-served flow bundle (mirrors LoadMountedFlow
// minus the HTTP specifics; pool is always 1). Interval overrides map
// triggerId -> Go duration string.
func LoadFlowService(flowRef string, intervals map[string]string, deps FlowMountDeps) (*ServiceFlow, error) {
	wasmPath, bundleDir, err := resolveFlowArtifact(flowRef, deps.Store)
	if err != nil {
		return nil, err
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read flow artifact: %w", err)
	}
	wasmBytes = modulert.StripPublicationTrailer(wasmBytes)

	// Content-hash identity for the operator capability-policy gate (loop
	// B1-followup — same requirement as LoadMountedFlow/httpmount.go):
	// computed on the portable bytes, before AOT compilation, so it matches
	// what an operator hashes to record an approval.
	contentHash := modulert.ContentHashHex(wasmBytes)

	runBytes := wasmBytes
	aot := false
	if deps.AOTCacheDir != "" {
		var compiled []byte
		var aotErr error
		if deps.AOTCompileOnMiss {
			compiled, aotErr = flatsqlrt.EnsureAOTArtifact(deps.AOTCacheDir, flowAOTCachePrefix, wasmBytes)
		} else {
			compiled, aotErr = flatsqlrt.LoadAOTArtifact(deps.AOTCacheDir, flowAOTCachePrefix, wasmBytes)
		}
		if aotErr == nil {
			runBytes = compiled
			aot = true
		} else {
			log.Warnf("Flow service %q: AOT artifact unavailable, interpreting: %v", flowRef, aotErr)
		}
	}
	if wasmImportsModule(wasmBytes, flatsqlrt.EngineImportModule) {
		// Ingest flows are bridge-mode by design (loop directive: new flows
		// default to bridge); a linked service flow would need the mount
		// machinery's poison/epoch handling — reject until needed.
		return nil, fmt.Errorf("flow service %q links the store engine; timer-served flows support bridge-mode artifacts only", flowRef)
	}

	pages := deps.MaxMemoryPages
	if pages == 0 {
		pages = 1024
	}

	inst, manifest, err := loadFlowInstance(runBytes, pages, false, nil, deps, contentHash)
	if err != nil {
		return nil, err
	}

	sf := &ServiceFlow{
		manifest:        manifest,
		aot:             aot,
		inst:            inst,
		lastTimerStatus: "never-run",
	}
	sf.programID = manifest.PluginID

	// Timer triggers + bound ports come from the bundle topology (mechanical
	// lookup, no interpretation).
	if bundleDir != "" {
		if data, readErr := os.ReadFile(filepath.Join(bundleDir, "flow.json")); readErr == nil {
			var topo serviceBundleTopology
			if json.Unmarshal(data, &topo) == nil {
				if topo.ProgramID != "" {
					sf.programID = topo.ProgramID
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
			}
		}
	}
	if len(sf.triggers) == 0 {
		inst.rt.Release()
		return nil, fmt.Errorf("flow service %q declares no timer triggers (bundle flow.json required)", flowRef)
	}

	// Egress sinks = the artifact's host-dispatch nodes (same discovery as
	// HTTP mounts).
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
	log.Infof("Flow service %q started (%d timer triggers, aot %v, egress %v)",
		sf.programID, len(sf.triggers), sf.aot, sf.egress)
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

// --- plugins.CronProvider interface ---

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

// InvokeCron fires one timer trigger: a tick JSON frame enters the bound
// port, the flow drains, and every egress result frame is collected into the
// returned summary.
func (sf *ServiceFlow) InvokeCron(ctx context.Context, method string, _ []byte) ([]byte, error) {
	out, err := sf.FireTrigger(ctx, method)
	sf.recordTimerResult(err)
	return out, err
}

// FireTrigger runs one named timer trigger to completion (also the direct
// entry point for tests and the admin surface).
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

	// One tick frame into the flow's linear memory at the trigger's bound
	// port ([48B descriptor][port id\0][payload], same packing as HTTP
	// mounts).
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

// Triggers lists the service's timer triggers (id + effective interval).
func (sf *ServiceFlow) Triggers() []serviceTrigger {
	out := make([]serviceTrigger, len(sf.triggers))
	copy(out, sf.triggers)
	return out
}

// --- plugins.UIProvider interface ---

func (sf *ServiceFlow) UIDescriptor() plugins.UIDescriptor {
	name := sf.programID
	if sf.manifest != nil && sf.manifest.Name != "" {
		name = sf.manifest.Name
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
			Name:         sf.programID,
			PluginFamily: "FLOW",
		},
	}
	if sf.manifest != nil {
		descriptor.Manifest.Name = sf.manifest.Name
		descriptor.Manifest.Version = sf.manifest.Version
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

// LoadFlowServices loads every configured timer-served flow. A service whose
// artifact is not installed is SKIPPED with an error log (module delivery
// may install it later); any other failure closes what loaded and fails.
func LoadFlowServices(services []config.FlowService, deps FlowMountDeps) ([]*ServiceFlow, error) {
	loaded := make([]*ServiceFlow, 0, len(services))
	fail := func(err error) ([]*ServiceFlow, error) {
		for _, sf := range loaded {
			sf.Close()
		}
		return nil, err
	}
	for _, service := range services {
		if service.Flow == "" {
			return fail(fmt.Errorf("flow service requires a flow reference"))
		}
		serviceDeps := deps
		if service.MemoryPages > 0 {
			serviceDeps.MaxMemoryPages = service.MemoryPages
		}
		// Per-service node CONFIG: the bridge builtin plugin.getConfig serves
		// this block to the flow's nodes (URL overrides etc.) — configuration,
		// never host code.
		if len(service.Config) > 0 {
			nodeCtx := modulert.NodeContext{}
			if deps.NodeCtx != nil {
				nodeCtx = *deps.NodeCtx
			}
			nodeCtx.Config = service.Config
			serviceDeps.NodeCtx = &nodeCtx
		}
		sf, err := LoadFlowService(service.Flow, service.Intervals, serviceDeps)
		if err != nil {
			if os.IsNotExist(err) || isFlowNotInstalled(err) {
				log.Errorf("Flow service %q skipped: %v", service.Flow, err)
				continue
			}
			return fail(fmt.Errorf("flow service %q: %w", service.Flow, err))
		}
		loaded = append(loaded, sf)
		log.Infof("Flow service %q loaded (%d timers, aot %v)", sf.ID(), len(sf.triggers), sf.aot)
	}
	return loaded, nil
}

func isFlowNotInstalled(err error) bool {
	for e := err; e != nil; {
		if e == ErrFlowNotInstalled {
			return true
		}
		unwrapper, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = unwrapper.Unwrap()
	}
	return false
}
