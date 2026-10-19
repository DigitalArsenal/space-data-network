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
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/typedfb/flownotice"
	"github.com/spacedatanetwork/sdn-server/internal/typedfb/timertick"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
	"github.com/spacedatanetwork/sdn-server/plugins"

	flatbuffers "github.com/google/flatbuffers/go"
)

// serviceTrigger is one TIMER trigger of a service flow: its runtime trigger
// index, bound target port, and effective interval.
type serviceTrigger struct {
	TriggerID  string
	Index      uint32
	PortID     string
	IntervalMs int

	// IntervalPinned records that IntervalMs came from the node config file
	// (flows.services[].intervals), not from the bundle's declared default.
	// It travels into CronMethodSpec so the plugin manager's scheduler knows
	// the cadence is operator-declared and must outrank persisted runtime
	// state.
	IntervalPinned bool

	// Fired counts this trigger's firings since the flow loaded: the next
	// TimerTick's SEQUENCE (0-based).
	Fired uint64
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
	// closed: Close ran. inst is also nil between runs of a service that
	// releases its instance after each run (releaseAfterRun).
	closed bool
	// What a fresh instance is built from (releaseAfterRun).
	runBytes    []byte
	pages       uint32
	deps        FlowMountDeps
	contentHash string
	// releaseAfterRun tears the WASM instance down after every run and builds
	// a fresh one for the next (flows.services[].release_after_run): linear
	// memory never shrinks, so a retrieval that once held a 112 MB download
	// would otherwise keep that high-water mark for the life of the node.
	releaseAfterRun bool
	// exclusive is shared by the services of one flows.services[].
	// exclusive_group: one run at a time among them (nil: no group).
	exclusive      chan struct{}
	exclusiveGroup string

	statsMu          sync.Mutex
	retrievalGate    RetrievalGate
	retrievalOutcome RetrievalOutcome
	manualRunGate    ManualRunGate
	startedAt        time.Time
	timerRunCount    uint64
	errorCount       uint64
	lastTimerStatus  string
	lastInvokeAt     time.Time

	schedulePhase SchedulePhase
	// scheduledIntervals: the interval the plugin manager actually resolved
	// for each trigger (config pin, persisted dashboard schedule, declared
	// minimum), learned when it asked for the first-run phase.
	scheduledIntervals map[string]time.Duration

	// What the LAST drain's nodes reported, read straight off the artifact's
	// node-state table. See nodeRunDigest.
	lastNodeDigest []nodeRunOutcome
	// lastRunUnchanged: the last drain's egress carried an "unchanged" notice —
	// the conditional fetch answered 304 and the flow landed nothing on purpose.
	lastRunUnchanged bool
}

// nodeRunOutcome is one node's mechanism-level result for one drain: how many
// times it was dispatched and what status it last returned. It carries NO
// interpretation — the host does not know what any node means, and nothing
// here decides whether the run was good.
type nodeRunOutcome struct {
	NodeID      string
	PluginID    string
	MethodID    string
	Invocations uint64
	LastStatus  uint32
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
	// Publication-trailer signature gate (loop I2 — see the identical
	// comment in httpmount.go's LoadMountedFlow): admitted here, before the
	// trailer is stripped, reusing modulert's I1 module-load gate via the
	// exported wrapper. Sourced from deps.NodeCtx.ModuleSignaturePolicy
	// (nil NodeCtx or nil policy is inert), mirroring how loadFlowInstance
	// sources NodeCtx.CapabilityPolicy.
	var sigPolicy *modulert.ModuleSignaturePolicy
	if deps.NodeCtx != nil {
		sigPolicy = deps.NodeCtx.ModuleSignaturePolicy
	}
	portableBytes, _, sigErr := modulert.EnforceModuleSignaturePolicy(sigPolicy, wasmBytes)
	if sigErr != nil {
		return nil, fmt.Errorf("flow service %q: %w", flowRef, sigErr)
	}
	wasmBytes = portableBytes

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
			compiled, aotErr = flatsqlrt.EnsureAOTArtifact(deps.AOTCacheDir, flowAOTPrefix(flowRef), wasmBytes)
		} else {
			compiled, aotErr = flatsqlrt.LoadAOTArtifact(deps.AOTCacheDir, flowAOTPrefix(flowRef), wasmBytes)
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
		runBytes:        runBytes,
		pages:           pages,
		deps:            deps,
		contentHash:     contentHash,
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
						// The operator wrote this in the node config file, so
						// it outranks any schedule persisted from the
						// dashboard (plugins.CronMethodSpec.IntervalPinned).
						st.IntervalPinned = true
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
	sf.closed = true
	if sf.inst != nil {
		sf.inst.rt.Release()
		sf.inst = nil
	}
	return nil
}

// SetReleaseAfterRun makes every run build a fresh WASM instance and tear it
// down when the run ends (flows.services[].release_after_run). The instance
// loaded at start is released at once, so an idle service holds no linear
// memory at all.
func (sf *ServiceFlow) SetReleaseAfterRun(release bool) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.releaseAfterRun = release
	if release && sf.inst != nil {
		sf.inst.rt.Release()
		sf.inst = nil
	}
}

// SetExclusiveGroup puts the service in a group whose services never run at
// the same time (flows.services[].exclusive_group): a run waits for the one
// ahead of it, whether a timer, a first fire or a run-now started it.
func (sf *ServiceFlow) SetExclusiveGroup(name string, slot chan struct{}) {
	sf.statsMu.Lock()
	defer sf.statsMu.Unlock()
	sf.exclusiveGroup, sf.exclusive = name, slot
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
			IntervalPinned:  trigger.IntervalPinned,
			Input:           "none",
			Output:          "json",
		})
	}
	return specs
}

// RetrievalGate reports whether this service may pull from its sources right
// now, and why not when it may not. It is host egress policy — the same family
// as the per-host request pacer — expressed against the node's own retrieval
// ledger. nil means ungated.
type RetrievalGate func() (allowed bool, reason string)

// RetrievalOutcome reports how a gated retrieval ended, so the ledger that
// counted the attempt as a failure can record WHY it stayed one.
type RetrievalOutcome func(runErr error)

// SetRetrievalOutcome installs the outcome sink. Only invoked for runs the
// gate actually admitted — a forced run books no attempt, so it explains none.
func (sf *ServiceFlow) SetRetrievalOutcome(outcome RetrievalOutcome) {
	sf.statsMu.Lock()
	defer sf.statsMu.Unlock()
	sf.retrievalOutcome = outcome
}

// ManualRunGate admits an operator's manual run (plugins.WithManualRun) in
// place of the RetrievalGate: the failure backoff does not hold it, a
// provider's minimum interval does. Admitted, it stamps the attempt and
// returns the func that records how the run ended (nil when it ran clean).
// Otherwise finish is nil and allowedAt is when the run may start. Injected by
// the node like the gate; nil admits every manual run and books nothing.
type ManualRunGate func() (finish func(runErr error), allowedAt time.Time)

// SetManualRunGate installs the manual-run gate.
func (sf *ServiceFlow) SetManualRunGate(gate ManualRunGate) {
	sf.statsMu.Lock()
	defer sf.statsMu.Unlock()
	sf.manualRunGate = gate
}

// SchedulePhase reports how long after scheduling a trigger's FIRST timer run
// should come, given the interval the scheduler resolved; see
// plugins.CronFirstRunDelayer. It is host policy expressed against the node's
// retrieval ledger (when the flow's sources were last retrieved), injected like
// the RetrievalGate. nil keeps the plain ticker.
type SchedulePhase func(triggerID string, interval time.Duration) time.Duration

// SetSchedulePhase installs the first-run phase. It must be installed before
// the flow is scheduled (plugins.Manager.StartLateRegistered) to take effect.
func (sf *ServiceFlow) SetSchedulePhase(phase SchedulePhase) {
	sf.statsMu.Lock()
	defer sf.statsMu.Unlock()
	sf.schedulePhase = phase
}

// CronFirstRunDelay implements plugins.CronFirstRunDelayer.
func (sf *ServiceFlow) CronFirstRunDelay(method string, interval time.Duration) time.Duration {
	sf.statsMu.Lock()
	if sf.scheduledIntervals == nil {
		sf.scheduledIntervals = make(map[string]time.Duration)
	}
	sf.scheduledIntervals[method] = interval
	phase := sf.schedulePhase
	sf.statsMu.Unlock()
	if phase == nil {
		return 0
	}
	return phase(method, interval)
}

// ScheduledInterval is the cadence a trigger actually runs at: the interval
// the scheduler resolved when it scheduled the trigger, or, before that, the
// trigger's own interval (bundle default or config pin), which the scheduler
// never undercuts.
func (sf *ServiceFlow) ScheduledInterval(triggerID string) time.Duration {
	sf.statsMu.Lock()
	resolved, ok := sf.scheduledIntervals[triggerID]
	sf.statsMu.Unlock()
	if ok && resolved > 0 {
		return resolved
	}
	for _, trigger := range sf.triggers {
		if trigger.TriggerID == triggerID {
			return time.Duration(trigger.IntervalMs) * time.Millisecond
		}
	}
	return 0
}

// SetRetrievalGate installs the debounce gate. Injected by the node so this
// package never learns what a "source" is.
func (sf *ServiceFlow) SetRetrievalGate(gate RetrievalGate) {
	sf.statsMu.Lock()
	sf.retrievalGate = gate
	sf.statsMu.Unlock()
}

// InvokeCron fires one timer trigger: a tick JSON frame enters the bound
// port, the flow drains, and every egress result frame is collected into the
// returned summary.
//
// DEBOUNCE. Every automatic entry point that can start a pull comes through
// here — the cron ticker, the boot-time first fire and a connector lane's
// FETCH NOW. A publisher's fetch policy is a promise about how often this node
// asks it for the same bytes, and a promise only a ticker keeps is not kept at
// all: firing run-now twice in three minutes pulled the CelesTrak full catalog
// twice and earned a 403, which is precisely the outcome the 3 h window exists
// to prevent. So the window is ENFORCED here, not merely reported on the board.
//
// An operator's manual run (plugins.WithManualRun) passes the ManualRunGate
// instead: it skips the failure backoff, never the provider's minimum
// interval, which defers it (plugins.DeferredError). It books its own attempt
// and outcome; a clean manual run clears the failure streak, so the backoff it
// overrode does not outlive it. A payload of {"force":true} bypasses both
// gates, booking nothing.
func (sf *ServiceFlow) InvokeCron(ctx context.Context, method string, input []byte) (out []byte, err error) {
	attempted := false
	var finishManual func(error)
	if cronInvokeForced(input) {
		// A forced run bypasses both gates and books no attempt or outcome.
	} else if plugins.ManualRun(ctx) {
		sf.statsMu.Lock()
		gate := sf.manualRunGate
		sf.statsMu.Unlock()
		if gate != nil {
			var allowedAt time.Time
			if finishManual, allowedAt = gate(); finishManual == nil {
				deferred := &plugins.DeferredError{Reason: "provider minimum interval", Until: allowedAt}
				log.Infof("Flow service %q: trigger %q manual run %s", sf.programID, method, deferred)
				return nil, deferred
			}
		}
		log.Infof("Flow service %q: trigger %q manual run: failure backoff bypassed (operator intent)", sf.programID, method)
	} else {
		sf.statsMu.Lock()
		gate := sf.retrievalGate
		sf.statsMu.Unlock()
		if gate != nil {
			allowed, reason := gate()
			if !allowed && plugins.ScheduledRun(ctx) {
				allowed, reason = sf.waitOutWindow(ctx, method, gate, reason)
			}
			if !allowed {
				log.Infof("Flow service %q: trigger %q skipped (%s)", sf.programID, method, reason)
				summary, _ := json.Marshal(map[string]interface{}{
					"trigger": method,
					"skipped": true,
					"reason":  reason,
					"results": []interface{}{},
				})
				sf.recordTimerResult(nil)
				return summary, nil
			}
			// The gate stamped an ATTEMPT, which counts as a failure until an
			// ingest proves otherwise. Whatever happens next is the only chance
			// to say why that attempt did or did not pay off.
			attempted = true
		}
	}
	var releaseSlot func()
	defer func() {
		if releaseSlot != nil {
			defer releaseSlot()
		}
		// A guest may translate a canceled hostcall into a status frame and
		// drain successfully. The caller's cancellation remains authoritative.
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		sf.recordTimerResult(err)
		if finishManual != nil {
			diagnosis := sf.retrievalDiagnosis(err)
			if errors.Is(diagnosis, ErrRetrievalUnchanged) || errors.Is(diagnosis, ErrNothingToStore) {
				diagnosis = nil
			}
			finishManual(diagnosis)
		}
		if attempted {
			sf.statsMu.Lock()
			outcome := sf.retrievalOutcome
			sf.statsMu.Unlock()
			if outcome != nil {
				outcome(sf.retrievalDiagnosis(err))
			}
		}
	}()
	sf.statsMu.Lock()
	slot, group := sf.exclusive, sf.exclusiveGroup
	sf.statsMu.Unlock()
	if slot != nil {
		select {
		case slot <- struct{}{}:
		default:
			log.Infof("Flow service %q: trigger %q waits for another run of exclusive group %q", sf.programID, method, group)
			select {
			case slot <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		releaseSlot = func() { <-slot }
	}
	return sf.FireTrigger(ctx, method)
}

// windowRecheck is how often a scheduled run that the gate refused asks again.
var windowRecheck = 15 * time.Second

// waitOutWindow lets a scheduled run that the gate refused wait for the
// publisher window to close, then pull.
//
// A trigger's interval and the window it serves are the same length, so a
// tick can land just inside the window: a boot-time first pull that came a
// few seconds after the ticker took its phase, or plain timer jitter.
// Dropping that tick cost a whole cycle (host-02, 2026-10-05: the GP lane
// skipped at 2h59m40s and ran every 6 h) and spared the publisher nothing.
// The window itself is never shortened: the run waits, re-asking the gate,
// for up to a twentieth of the interval, then pulls or is skipped as before.
// An unscheduled gated run (first fire, FETCH NOW) never comes through here
// and is refused at once.
func (sf *ServiceFlow) waitOutWindow(ctx context.Context, method string, gate RetrievalGate, reason string) (bool, string) {
	deadline := time.Now().Add(sf.ScheduledInterval(method) / 20)
	for time.Now().Before(deadline) {
		timer := time.NewTimer(windowRecheck)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, reason
		case <-timer.C:
		}
		allowed, again := gate()
		if allowed {
			log.Infof("Flow service %q: trigger %q waited out its window and runs (%s)", sf.programID, method, reason)
			return true, again
		}
		reason = again
	}
	return false, reason
}

// cronInvokeForced reads the optional {"force":true} escape from a cron
// invocation payload. Anything unparseable is NOT a force.
func cronInvokeForced(input []byte) bool {
	if len(input) == 0 {
		return false
	}
	var payload struct {
		Force bool `json:"force"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return false
	}
	return payload.Force
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
	if sf.closed || (sf.inst == nil && len(sf.runBytes) == 0) {
		return nil, fmt.Errorf("flow service %q is closed", sf.programID)
	}
	if sf.inst == nil {
		// Released after the last run: this run gets a fresh instance.
		inst, _, err := loadFlowInstance(sf.runBytes, sf.pages, false, nil, sf.deps, sf.contentHash)
		if err != nil {
			return nil, fmt.Errorf("flow service %q: load a fresh instance: %w", sf.programID, err)
		}
		sf.inst = inst
	}
	if sf.releaseAfterRun {
		defer func() {
			if sf.inst != nil {
				sf.inst.rt.Release()
				sf.inst = nil
			}
		}()
	}
	rt := sf.inst.rt
	sf.inst.bridge.SetInvocationContext(ctx)
	defer sf.inst.bridge.SetInvocationContext(nil)
	defer sf.inst.bridge.ResetBodyRefs()

	tick := timerTickFrame(trigger.TriggerID, time.Now(), trigger.Fired)
	trigger.Fired++

	// One tick frame into the flow's linear memory at the trigger's bound
	// port ([48B descriptor][port id\0][pad][payload], the HTTP mounts'
	// packing). The TimerTick carries uint64 fields, so its first byte sits
	// on an 8-byte boundary of guest memory: the allocation is 8-aligned
	// (the guest allocator's minimum) and the port id is padded to 8.
	portBytes := append([]byte(trigger.PortID), 0)
	payloadAt := flowFrameDescriptorSize + (len(portBytes)+7)&^7
	buf := make([]byte, payloadAt+len(tick))
	copy(buf[flowFrameDescriptorSize:], portBytes)
	copy(buf[payloadAt:], tick)
	framePtr, err := rt.Module().AllocateSize(uint32(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("allocate tick frame: %w", err)
	}
	// The descriptor names no type (InvalidIndex), as the HTTP mounts' does:
	// the guest gives the frame each binding's own type. Index 0 is a real
	// descriptor, and a typed port refuses a frame that claims it.
	encodeFrameDescriptor(buf[:flowFrameDescriptorSize], &FlowFrameDescriptor{
		TypeDescriptorIdx: InvalidIndex,
		PortIDPointer:     framePtr + flowFrameDescriptorSize,
		Alignment:         8,
		Offset:            framePtr + uint32(payloadAt),
		Size:              uint32(len(tick)),
		Occupied:          true,
	})
	if err := rt.Module().WriteMemory(framePtr, buf); err != nil {
		return nil, fmt.Errorf("write tick frame: %w", err)
	}
	// A refused frame is a run that cannot start: say so instead of draining
	// an empty flow and reporting that nothing ran.
	res, err := rt.Module().Execute(runtimeExportEnqueueTriggerFrame, int32(trigger.Index), int32(framePtr))
	if err != nil {
		return nil, fmt.Errorf("flow service %q trigger %q: enqueue the tick: %w", sf.programID, triggerID, err)
	}
	if len(res) > 0 {
		if code := wasmrt.ToInt32(res[0]); code < 0 {
			return nil, fmt.Errorf("flow service %q trigger %q: the flow refused the tick frame (code %d)", sf.programID, triggerID, code)
		}
	}

	// Drain, collecting every egress result frame verbatim.
	var frames [][]byte
	collect := func(_ context.Context, args *InvocationArgs) (*InvocationResult, error) {
		for _, frame := range args.Frames {
			frames = append(frames, append([]byte(nil), frame.Bytes...))
		}
		return &InvocationResult{StatusCode: 0}, nil
	}
	handlers := make(HandlerMap, len(sf.egress))
	for _, key := range sf.egress {
		handlers[key] = collect
	}

	_, drainErr := rt.Drain(ctx, handlers, DrainOptions{MaxIterations: 1000})
	if ctx.Err() != nil {
		drainErr = ctx.Err()
	}
	// Read the node-state table BEFORE returning on a drain error: a run that
	// died mid-drain is exactly the one whose per-node record is worth having.
	sf.statsMu.Lock()
	sf.lastNodeDigest = readNodeRunDigest(rt)
	sf.lastRunUnchanged = egressReportsUnchanged(frames)
	sf.statsMu.Unlock()
	if drainErr != nil {
		return nil, fmt.Errorf("flow service %q trigger %q: %w", sf.programID, triggerID, drainErr)
	}

	summary, _ := json.Marshal(map[string]interface{}{
		"trigger": triggerID,
		"results": egressSummary(frames),
	})
	return summary, nil
}

// timerTickFrame is the frame a timer trigger hands its bound port: an SDK
// TimerTick (TICK). The host fires a trigger when it is due, so SCHEDULED_MS
// and FIRED_MS are the same instant.
func timerTickFrame(triggerID string, firedAt time.Time, sequence uint64) []byte {
	builder := flatbuffers.NewBuilder(64)
	id := builder.CreateString(triggerID)
	ms := uint64(firedAt.UnixMilli())
	timertick.TimerTickStart(builder)
	timertick.TimerTickAddTriggerId(builder, id)
	timertick.TimerTickAddScheduledMs(builder, ms)
	timertick.TimerTickAddFiredMs(builder, ms)
	timertick.TimerTickAddSequence(builder, sequence)
	timertick.FinishTimerTickBuffer(builder, timertick.TimerTickEnd(builder))
	return builder.FinishedBytes()
}

// egressSummary is the run summary's view of the egress frames: a JSON frame
// as itself, a FlatBuffer frame as its file identifier and bytes (base64), so
// a typed IngestResult or FlowNotice reaches the caller whole.
func egressSummary(frames [][]byte) []json.RawMessage {
	results := make([]json.RawMessage, 0, len(frames))
	for _, frame := range frames {
		if json.Valid(frame) {
			results = append(results, json.RawMessage(frame))
			continue
		}
		entry := map[string]interface{}{"bytes": frame}
		if id, ok := frameFileIdentifier(frame); ok {
			entry["fileIdentifier"] = id
		}
		encoded, _ := json.Marshal(entry)
		results = append(results, json.RawMessage(encoded))
	}
	return results
}

// frameFileIdentifier reads a FlatBuffer frame's 4-character file identifier
// (bytes 4..8 after the root offset), when those bytes are printable ASCII.
func frameFileIdentifier(frame []byte) (string, bool) {
	if len(frame) < 8 {
		return "", false
	}
	for _, c := range frame[4:8] {
		if c < 0x21 || c > 0x7e {
			return "", false
		}
	}
	return string(frame[4:8]), true
}

// readNodeRunDigest copies the artifact's per-node state table out of guest
// memory after a drain.
//
// WHY THE HOST READS THIS AT ALL. Every node of a linked-direct artifact runs
// INSIDE the guest (`space_data_module_runtime_drain_linked`), so a node that
// refuses its input — `plugin_set_error(...); return 400` — returns through the
// in-wasm scheduler and never reaches the host loop below. Drain returns nil,
// FireTrigger returns nil, and until this existed the host could say nothing
// about the run beyond "it finished". That is how the cellular worldwide ingest
// fetched 3 MiB and stored nothing THREE times with no cause recorded anywhere
// (graph: sdn-cellular-ingest-lands-no-batch): the guest knew which node
// refused and why; the host threw the answer away.
//
// This is a CONNECTOR read and nothing more. The host copies node index ->
// (id, plugin, method, invocation count, last status) and forms no opinion
// about what any of them mean. A non-zero status is NOT treated as a run
// failure: hostcap/http-request answers a dead mirror with 502 and no frame BY
// DESIGN, and flows that fan out across mirrors depend on that being survivable.
// The status is carried, not judged.
func readNodeRunDigest(rt *FlowRuntime) []nodeRunOutcome {
	if rt == nil || rt.NodeCount == 0 {
		return nil
	}
	digest := make([]nodeRunOutcome, 0, rt.NodeCount)
	for i := uint32(0); i < rt.NodeCount; i++ {
		state, err := rt.GetNodeRuntimeState(i)
		if err != nil || state == nil {
			continue
		}
		var info flowNodeInfo
		if int(i) < len(rt.nodeInfo) {
			info = rt.nodeInfo[i]
		}
		digest = append(digest, nodeRunOutcome{
			NodeID:      info.NodeID,
			PluginID:    info.PluginID,
			MethodID:    info.MethodID,
			Invocations: state.InvocationCount,
			LastStatus:  state.LastStatus,
		})
	}
	return digest
}

// ErrRetrievalUnchanged is the diagnosis of a run whose conditional fetch
// answered 304: the publisher had nothing new, the node keeps the set it has,
// and the ledger must count the attempt as a success without a batch rather
// than widen the publisher's window. A typed retrieval module says so with a
// FlowNotice (FNTC) of KIND Unchanged; a JSON-era one with an egress frame
// {"status":304,"unchanged":true,...}.
var ErrRetrievalUnchanged = errors.New("upstream unchanged: the conditional fetch answered 304 and there was nothing new to land")

// ErrNothingToStore is the diagnosis of a clean run of a flow that declares no
// storage_ingest capability (record-retention): it can never land a batch, so
// "no batch landed" is no evidence against it. The run's own result decides,
// and this one succeeded; the ledger counts it as a success without a batch.
var ErrNothingToStore = errors.New("run succeeded: the flow declares no storage_ingest, so no batch was owed")

// storesRecords reports whether the flow can land a batch at all. Only a flow
// whose manifest declares storage_ingest is granted that hostcall.
func (sf *ServiceFlow) storesRecords() bool {
	if sf.manifest == nil {
		return true
	}
	for _, capability := range sf.manifest.Capabilities {
		if capability == "storage_ingest" {
			return true
		}
	}
	return false
}

// egressReportsUnchanged reports whether any egress result frame of the run
// is such an unchanged notice.
func egressReportsUnchanged(frames [][]byte) bool {
	for _, raw := range frames {
		if flownotice.FlowNoticeBufferHasIdentifier(raw) {
			if flownotice.GetRootAsFlowNotice(raw, 0).Kind().String() == "Unchanged" {
				return true
			}
			continue
		}
		var frame struct {
			Unchanged bool `json:"unchanged"`
			Status    int  `json:"status"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}
		if frame.Unchanged || frame.Status == http.StatusNotModified {
			return true
		}
	}
	return false
}

// retrievalDiagnosis is what the run tells the source-metrics ledger.
//
// A drain error is already an answer, so it passes through untouched. The hard
// case is the CLEAN run — the flow returned 0 and landed nothing — because the
// ledger's fallback text ("run completed but landed no batch") names a symptom
// and no cause. The node digest holds the cause the guest already computed, so
// the clean case reports the nodes that refused and, failing that, the nodes
// that never ran at all: a pipeline whose tail never fired is starved, and
// which node the frames stopped at is the whole diagnosis.
//
// A flow that stores nothing is judged by its own result instead: ok, error
// (a drain error or a node that refused) or cancelled. Starved nodes and the
// absence of a batch say nothing about it (ErrNothingToStore).
//
// This error is HANDED TO THE LEDGER, NOT RETURNED FROM THE RUN. The ledger
// records a reason only while an attempt's failure streak is still standing —
// i.e. only when nothing landed — so a run that DID store a batch discards this
// entirely and no successful run is ever recolored by a 502 from one mirror.
func (sf *ServiceFlow) retrievalDiagnosis(runErr error) error {
	if runErr != nil {
		return runErr
	}
	sf.statsMu.Lock()
	digest := sf.lastNodeDigest
	unchanged := sf.lastRunUnchanged
	sf.statsMu.Unlock()

	var refused, starved []string
	for _, node := range digest {
		label := node.NodeID
		if node.PluginID != "" {
			label = fmt.Sprintf("%s (%s:%s)", node.NodeID, node.PluginID, node.MethodID)
		}
		switch {
		case node.Invocations > 0 && node.LastStatus != 0:
			refused = append(refused, fmt.Sprintf("%s status %d", label, node.LastStatus))
		case node.Invocations == 0:
			starved = append(starved, label)
		}
	}
	stores := sf.storesRecords()
	switch {
	case len(refused) > 0 && !stores:
		return fmt.Errorf("node(s) refused: %s", strings.Join(refused, "; "))
	case len(refused) > 0:
		return fmt.Errorf("run completed but landed no batch; node(s) refused: %s",
			strings.Join(refused, "; "))
	case !stores:
		return ErrNothingToStore
	case unchanged:
		// The tail of the pipeline is starved BY DESIGN: the fetch answered 304
		// and the parser said so on egress. Nothing to store is not a failure.
		return ErrRetrievalUnchanged
	case len(starved) > 0:
		return fmt.Errorf("run completed but landed no batch; node(s) never ran: %s",
			strings.Join(starved, ", "))
	}
	return nil
}

func (sf *ServiceFlow) recordTimerResult(err error) {
	sf.statsMu.Lock()
	defer sf.statsMu.Unlock()
	sf.timerRunCount++
	sf.lastInvokeAt = time.Now().UTC()
	if errors.Is(err, context.Canceled) {
		sf.lastTimerStatus = "cancelled"
	} else if err != nil {
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
	// Memory stats need the RUN mutex, which FireTrigger holds for the WHOLE
	// duration of a flow run — tens of minutes for a full CelesTrak SATCAT
	// ingest on a small host. Blocking here made the node's entire runtime
	// snapshot (and with it /api/apps and the dashboard's module list)
	// unavailable for exactly as long as an app was busy, which is precisely
	// when an operator asks whether it is working. Memory stats are a nicety;
	// answering is not. Take them only if the flow is idle.
	if sf.mu.TryLock() {
		if sf.inst != nil && sf.inst.rt != nil && sf.inst.rt.Module() != nil {
			if stats, err := sf.inst.rt.Module().MemoryStats(); err == nil {
				descriptor.Stats.MemoryPages = stats.Pages
				descriptor.Stats.MemoryBytes = stats.Bytes
				descriptor.Stats.MaxMemoryPages = stats.MaxPages
				descriptor.Stats.MaxMemoryBytes = stats.MaxBytes
			}
		}
		sf.mu.Unlock()
	} else {
		// Busy is itself status worth reporting.
		descriptor.Stats.LastTimerStatus = "running"
	}
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
	groups := map[string]chan struct{}{}
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
				// NOT an error. The default config declares the CelesTrak
				// services so an operator can enable them by installing the
				// bundle, and skipping an uninstalled one is the DESIGNED
				// path — see config.Default()'s Services block. Logged at
				// ERROR, a stock node printed three red lines on its first
				// boot, which is how people learn to ignore red lines.
				log.Infof("Flow service %q not started: its bundle is not installed (install it to enable this service)", service.Flow)
				continue
			}
			return fail(fmt.Errorf("flow service %q: %w", service.Flow, err))
		}
		if group := strings.TrimSpace(service.ExclusiveGroup); group != "" {
			if groups[group] == nil {
				groups[group] = make(chan struct{}, 1)
			}
			sf.SetExclusiveGroup(group, groups[group])
		}
		if service.ReleaseAfterRun {
			sf.SetReleaseAfterRun(true)
		}
		loaded = append(loaded, sf)
		log.Infof("Flow service %q loaded (%d timers, aot %v, exclusive group %q, release after run %v)", sf.ID(), len(sf.triggers), sf.aot,
			service.ExclusiveGroup, service.ReleaseAfterRun)
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
