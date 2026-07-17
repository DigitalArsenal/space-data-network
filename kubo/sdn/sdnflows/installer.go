// Package sdnflows is the SDN node FLOW install + register pipeline: it turns a
// compiled flow bundle (chaining WASM module nodes) into a running,
// cron-scheduled flow on a kubo-based SDN node, and persists which flows are
// installed so the set re-registers on the next boot.
//
// It is the flow-side sibling of sdnmodules: where sdnmodules installs a single
// WASM module and registers it as a *modulert.Module with the cron scheduler,
// this installs a timer-served flow bundle (flowrt.ServiceFlow) and registers
// THAT with the same scheduler. Because a ServiceFlow satisfies
// sdncron.CronModule (ID/CronMethods/InvokeCron), a registered flow both fires
// its host-cron timer on its effective interval AND appears at
// GET /sdn/v1/modules alongside modules — flows are runnable units on the node.
//
// Given a flow bundle reference (a directory holding runtime.wasm + flow.plg,
// the deps having been linked into runtime.wasm at compile time) the installer:
//
//  1. loads the bundle through flowrt.LoadFlowService — WASI + flow host funcs +
//     the module-SDK hostcall bridge, capabilities provisioned from the node's
//     services registry FAIL CLOSED (an operator-unapproved sensitive capability,
//     keyed by the bundle's content hash, refuses the whole install);
//  2. registers the resulting ServiceFlow with the cron Scheduler under its
//     timer triggers (interval overridable by home-dir config), so the scheduler
//     fires the flow's real InvokeCron (fetch -> parse -> store) on its cadence;
//  3. records the install in a persisted installed-flows registry so a later
//     boot re-loads the bundle and re-registers it.
//
// # Home-directory layout
//
// The registry lives under the node's SDN flow root, <repo>/sdn/flows:
//
//	installed-flows.json   one entry per installed flow: id, ref (bundle dir),
//	                       name/version, intervals, config, enabled, source.
package sdnflows

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

// Logger is a minimal printf-style sink (nil is silent).
type Logger func(format string, args ...interface{})

// ErrInstallDenied is returned when a flow bundle requests a sensitive
// capability no operator approval covers (fail closed).
var ErrInstallDenied = errors.New("sdnflows: flow install denied by capability policy")

// FlowSpec describes one flow to install: the bundle reference plus optional
// per-trigger interval overrides (triggerId -> Go duration string) and node
// CONFIG (served to the flow's nodes via plugin.getConfig — URL overrides etc.).
type FlowSpec struct {
	Ref       string                 `json:"ref"`
	Intervals map[string]string      `json:"intervals,omitempty"`
	Config    map[string]interface{} `json:"config,omitempty"`
}

// InstalledFlow is the read model for one installed + registered flow.
type InstalledFlow struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Version string   `json:"version,omitempty"`
	Ref     string   `json:"ref"`
	Enabled bool     `json:"enabled"`
	Source  string   `json:"source,omitempty"`
	Timers  []string `json:"timers"`
}

// Config wires an Installer to a live node.
type Config struct {
	// Services is the live SDN services bundle: its CapRegistry provisions the
	// flow's capabilities (fail closed), NodeCtx carries the operator policy,
	// and Scheduler fires the registered flow's timers. Required.
	Services *sdnservices.Services
	// Registry persists the installed-flows set. May be no-persistence (empty
	// dir). Required (non-nil); use NewRegistry("") for no-persistence.
	Registry *Registry
	// MaxMemoryPages caps each flow instance's linear memory (0 => 1024).
	MaxMemoryPages uint32
	// Log is an optional printf sink.
	Log Logger
}

// Installer installs compiled flow bundles onto a live SDN node and registers
// them with the cron scheduler. See the package doc for the flow.
type Installer struct {
	svc *sdnservices.Services
	reg *Registry
	max uint32
	log Logger

	mu     sync.Mutex
	loaded map[string]*flowrt.ServiceFlow // id -> live flow handle
}

// New builds an Installer. Services and Registry are required.
func New(cfg Config) (*Installer, error) {
	if cfg.Services == nil {
		return nil, errors.New("sdnflows: Config.Services is required")
	}
	if cfg.Services.Scheduler == nil {
		return nil, errors.New("sdnflows: Services.Scheduler is required")
	}
	if cfg.Services.CapReg == nil {
		return nil, errors.New("sdnflows: Services.CapReg is required")
	}
	if cfg.Registry == nil {
		return nil, errors.New("sdnflows: Config.Registry is required (use NewRegistry(\"\") for no-persistence)")
	}
	return &Installer{
		svc:    cfg.Services,
		reg:    cfg.Registry,
		max:    cfg.MaxMemoryPages,
		log:    cfg.Log,
		loaded: make(map[string]*flowrt.ServiceFlow),
	}, nil
}

func (in *Installer) logf(format string, args ...interface{}) {
	if in.log != nil {
		in.log(format, args...)
	}
}

func (in *Installer) deps() flowrt.FlowServiceDeps {
	return flowrt.FlowServiceDeps{
		CapRegistry:    in.svc.CapReg,
		NodeCtx:        in.svc.NodeCtx,
		MaxMemoryPages: in.max,
	}
}

// Install loads a flow bundle, registers it with the cron scheduler, and (when
// persist is set) records it in the installed-flows registry. The capability
// policy is enforced FAIL CLOSED inside LoadFlowService — a flow requesting an
// unapproved sensitive capability is refused here and is NOT registered or
// persisted. source is a provenance tag. Idempotent by flow id: re-installing
// an already-registered id refreshes the registry entry and closes the
// freshly-loaded duplicate rather than double-registering.
func (in *Installer) Install(spec FlowSpec, source string) (InstalledFlow, error) {
	return in.install(spec, source, true)
}

func (in *Installer) install(spec FlowSpec, source string, persist bool) (InstalledFlow, error) {
	if strings.TrimSpace(spec.Ref) == "" {
		return InstalledFlow{}, errors.New("sdnflows: flow spec has empty ref")
	}
	sf, err := flowrt.LoadFlowService(spec.Ref, spec.Intervals, spec.Config, in.deps())
	if err != nil {
		if strings.Contains(err.Error(), "capability policy") {
			return InstalledFlow{}, fmt.Errorf("%w: %s: %v", ErrInstallDenied, spec.Ref, err)
		}
		return InstalledFlow{}, fmt.Errorf("sdnflows: load flow %q: %w", spec.Ref, err)
	}
	id := sf.ID()
	if strings.TrimSpace(id) == "" {
		_ = sf.Close()
		return InstalledFlow{}, fmt.Errorf("sdnflows: flow %q has empty id", spec.Ref)
	}

	in.mu.Lock()
	if _, exists := in.loaded[id]; exists {
		in.mu.Unlock()
		_ = sf.Close()
		if persist {
			if err := in.persist(id, spec, source); err != nil {
				return InstalledFlow{}, err
			}
		}
		in.logf("sdnflows: flow %q already installed; refreshed registry entry", id)
		return in.view(sf, source, persist), nil
	}

	if err := in.svc.Scheduler.Register(sdncron.Registration{
		Module:  sf,
		Name:    sf.Name(),
		Version: sf.Version(),
	}); err != nil {
		in.mu.Unlock()
		_ = sf.Close()
		return InstalledFlow{}, fmt.Errorf("sdnflows: register flow %q with scheduler: %w", id, err)
	}
	in.loaded[id] = sf
	in.mu.Unlock()

	if persist {
		if err := in.persist(id, spec, source); err != nil {
			return InstalledFlow{}, err
		}
	}
	in.logf("sdnflows: installed + registered flow %q (%d timer(s)) [source=%s]", id, len(sf.Triggers()), source)
	return in.view(sf, source, persist), nil
}

func (in *Installer) persist(id string, spec FlowSpec, source string) error {
	if err := in.reg.Put(InstalledEntry{
		ID:        id,
		Ref:       spec.Ref,
		Intervals: spec.Intervals,
		Config:    spec.Config,
		Enabled:   true,
		Source:    source,
	}); err != nil {
		return fmt.Errorf("sdnflows: persist registry entry for %q: %w", id, err)
	}
	return nil
}

func (in *Installer) view(sf *flowrt.ServiceFlow, source string, enabled bool) InstalledFlow {
	timers := make([]string, 0)
	for _, t := range sf.Triggers() {
		timers = append(timers, t.TriggerID)
	}
	sort.Strings(timers)
	return InstalledFlow{
		ID:      sf.ID(),
		Name:    sf.Name(),
		Version: sf.Version(),
		Ref:     "",
		Enabled: enabled,
		Source:  source,
		Timers:  timers,
	}
}

// Boot re-establishes the installed-flows set on a fresh Services build: it
// re-loads and re-registers every ENABLED persisted registry entry, then
// installs any additional configured bootSet flows not already installed. It is
// tolerant: an entry whose bundle is missing, or whose sensitive capabilities
// are unapproved, is logged and skipped rather than failing the whole boot.
// Returns the number of flows registered.
//
// Boot registers flows but does NOT start the scheduler — the caller starts it
// after Boot so every timer begins together.
func (in *Installer) Boot(ctx context.Context, bootSet []FlowSpec) (int, error) {
	registered := 0
	seen := map[string]bool{}

	entries, err := in.reg.List()
	if err != nil {
		return 0, fmt.Errorf("sdnflows: read installed registry: %w", err)
	}
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		if _, err := in.install(FlowSpec{Ref: e.Ref, Intervals: e.Intervals, Config: e.Config}, e.Source, false); err != nil {
			in.logf("sdnflows: boot: register %q failed; skipping: %v", e.ID, err)
			continue
		}
		seen[e.ID] = true
		registered++
	}

	for _, spec := range bootSet {
		f, err := in.install(spec, "boot-set", true)
		if err != nil {
			in.logf("sdnflows: boot: install configured flow %q failed; skipping: %v", spec.Ref, err)
			continue
		}
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		registered++
	}
	return registered, nil
}

// List returns the read model for every flow installed in THIS process, sorted
// by id, joined with its persisted registry provenance.
func (in *Installer) List() []InstalledFlow {
	in.mu.Lock()
	ids := make([]string, 0, len(in.loaded))
	flows := make(map[string]*flowrt.ServiceFlow, len(in.loaded))
	for id, f := range in.loaded {
		ids = append(ids, id)
		flows[id] = f
	}
	in.mu.Unlock()

	sort.Strings(ids)
	out := make([]InstalledFlow, 0, len(ids))
	for _, id := range ids {
		sf := flows[id]
		source, ref, enabled := "", "", true
		if e, ok, _ := in.reg.Get(id); ok {
			source, ref, enabled = e.Source, e.Ref, e.Enabled
		}
		v := in.view(sf, source, enabled)
		v.Ref = ref
		out = append(out, v)
	}
	return out
}

// Flow returns the live ServiceFlow handle for id, or nil.
func (in *Installer) Flow(id string) *flowrt.ServiceFlow {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.loaded[id]
}

// Close releases every loaded flow handle. The scheduler is owned by the
// Services bundle (svc.Close stops it); this only closes the flow runtimes.
func (in *Installer) Close() {
	in.mu.Lock()
	flows := make([]*flowrt.ServiceFlow, 0, len(in.loaded))
	for _, f := range in.loaded {
		flows = append(flows, f)
	}
	in.loaded = make(map[string]*flowrt.ServiceFlow)
	in.mu.Unlock()
	for _, f := range flows {
		_ = f.Close()
	}
}
