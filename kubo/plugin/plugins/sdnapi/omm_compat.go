package sdnapi

// omm_compat.go bridges the supplemental-OMM board's HARDCODED config-panel id
// ("supplemental-omm", GET/PUT /sdn/v1/modules/supplemental-omm/config) to the
// mounted OD ServiceFlow (org.sdn.flows.od-supplemental-omm — kubo commit
// 3acaf56c, maybeInstallOperatorOMMFlow). Root cause of the "CONFIG UNAVAILABLE"
// banner: the flow registers with the cron scheduler under its OWN $PLG program
// id, never "supplemental-omm", so the board's hardcoded lookup 404s even
// though the flow is live. This is a COMPAT ID SHIM only — read/write settings
// surface, zero run orchestration: it never fetches, fits, batches, or fires a
// trigger out of band.
//
// Two config planes stay cleanly separated:
//   - interval_ms (the reserved cron-scheduling key) always delegates to the
//     REAL *sdncron.Scheduler registration (id org.sdn.flows.od-supplemental-omm,
//     its one "t0" host-cron trigger) — reusing the scheduler's own proven
//     live-reschedule + persistence, not a second implementation of it.
//   - every other (opaque) key, e.g. enabled_providers, is the flow's own
//     per-node CONFIG: persisted in the installed-flows registry and pushed
//     live to the flow's plugin.getConfig via ServiceFlow.SetNodeConfig, so the
//     NEXT trigger fire (never an immediate one) sees it.
//
// GET defaults enabled_providers from the flow's DECLARED provider set (every
// $PLG "source"-kind node) when nothing is stored yet, mapped from the
// provider's plugin id (com.orbpro.<x>-source) to the board's short id (<x>) —
// a mechanical prefix/suffix strip, not a guess; it mirrors exactly the naming
// flowrt.ODSupplementalOMMSpec uses for every provider node it wires.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	pluginsdnruntime "github.com/ipfs/kubo/plugin/plugins/sdnruntime"
	"github.com/ipfs/kubo/sdn/flowrt"
	sdnapihttp "github.com/ipfs/kubo/sdn/sdnapi"
	"github.com/ipfs/kubo/sdn/sdncron"
)

const (
	// ommCompatModuleID is the id the board hardcodes (MODULE_ID in
	// omm_board.html) — kept stable so the board needs no further change.
	ommCompatModuleID = "supplemental-omm"
	// ommFlowProgramID is the OD flow's real $PLG program id / scheduler
	// registration id (flowrt.ODSupplementalOMMSpec().ProgramID).
	ommFlowProgramID = "org.sdn.flows.od-supplemental-omm"
	// ommCompatTimerID is the display id the compat entry's one timer carries.
	// The board looks up timers by id "run" but falls back to timers[0] when
	// that is absent, so this is cosmetic, not load-bearing.
	ommCompatTimerID = "run"
)

// ommFlow resolves the live OD ServiceFlow, or nil when the flow installer
// isn't up yet or the flow was never mounted (a non-"omm"-role node) — the nil
// case is what makes the compat surface report an honest 404, never a fake
// success.
func ommFlow() *flowrt.ServiceFlow {
	fi := pluginsdnruntime.FlowInstaller()
	if fi == nil {
		return nil
	}
	return fi.Flow(ommFlowProgramID)
}

// ommCompatProviderShortID maps a declared provider's plugin id
// (com.orbpro.<x>-source) to the board's short provider id (<x>).
func ommCompatProviderShortID(pluginID string) string {
	id := strings.TrimPrefix(pluginID, "com.orbpro.")
	id = strings.TrimSuffix(id, "-source")
	return id
}

// ommDeclaredProviderShortIDs is the sorted, deduplicated default
// enabled_providers set: every provider the flow declares, short-id form.
func ommDeclaredProviderShortIDs(sf *flowrt.ServiceFlow) []string {
	ids := sf.SourceProviderPluginIDs()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, ommCompatProviderShortID(id))
	}
	sort.Strings(out)
	return out
}

// asPositiveMs coerces a decoded JSON number (float64, the shape
// encoding/json produces for a plain map[string]interface{} decode) or a Go
// integer into a positive int64 millisecond interval.
func asPositiveMs(v interface{}) (int64, bool) {
	switch t := v.(type) {
	case float64:
		if t <= 0 || t != math.Trunc(t) {
			return 0, false
		}
		return int64(t), true
	case int:
		if t <= 0 {
			return 0, false
		}
		return int64(t), true
	case int64:
		if t <= 0 {
			return 0, false
		}
		return t, true
	default:
		return 0, false
	}
}

// ommCompatModuleAdmin decorates the real *sdncron.Scheduler-backed
// ModuleAdmin with the "supplemental-omm" compat id. Every id other than the
// compat one passes straight through to the real admin unchanged.
type ommCompatModuleAdmin struct {
	real sdnapihttp.ModuleAdmin // nil-safe (the scheduler, when the runtime is up)
}

func (a ommCompatModuleAdmin) effectiveIntervalMs(sf *flowrt.ServiceFlow) int64 {
	if a.real != nil {
		for _, v := range a.real.List() {
			if v.ID != ommFlowProgramID {
				continue
			}
			for _, t := range v.Timers {
				return t.IntervalMs // the flow's one host-cron trigger ("t0")
			}
		}
	}
	if ts := sf.Triggers(); len(ts) > 0 {
		return int64(ts[0].IntervalMs) // defensive fallback; should not be reached
	}
	return 0
}

func (a ommCompatModuleAdmin) effectiveConfig(sf *flowrt.ServiceFlow) sdncron.ModuleConfig {
	out := map[string]interface{}{}
	if fi := pluginsdnruntime.FlowInstaller(); fi != nil {
		if stored := fi.StoredConfig(ommFlowProgramID); stored != nil {
			for k, v := range stored {
				out[k] = v
			}
		}
	}
	if _, has := out["enabled_providers"]; !has {
		out["enabled_providers"] = ommDeclaredProviderShortIDs(sf)
	}
	out["interval_ms"] = float64(a.effectiveIntervalMs(sf))
	return sdncron.ModuleConfig(out)
}

func (a ommCompatModuleAdmin) compatView() (sdncron.ModuleView, bool) {
	sf := ommFlow()
	if sf == nil {
		return sdncron.ModuleView{}, false
	}
	return sdncron.ModuleView{
		ID:      ommCompatModuleID,
		Name:    sf.Name(),
		Version: sf.Version(),
		Timers:  []sdncron.TimerView{{ID: ommCompatTimerID, IntervalMs: a.effectiveIntervalMs(sf)}},
		Config:  a.effectiveConfig(sf),
		Running: true,
	}, true
}

// List returns the real admin's list plus the synthesized "supplemental-omm"
// entry (when the OD flow is mounted), sorted by id.
func (a ommCompatModuleAdmin) List() []sdncron.ModuleView {
	var out []sdncron.ModuleView
	if a.real != nil {
		out = a.real.List()
	}
	if v, ok := a.compatView(); ok {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Config resolves the compat id against the mounted flow; every other id
// passes through to the real admin unchanged.
func (a ommCompatModuleAdmin) Config(id string) (sdncron.ModuleConfig, bool) {
	if id != ommCompatModuleID {
		if a.real == nil {
			return nil, false
		}
		return a.real.Config(id)
	}
	sf := ommFlow()
	if sf == nil {
		return nil, false
	}
	return a.effectiveConfig(sf), true
}

// ApplyConfig validates + splits cfg across the two config planes (see the
// package doc) for the compat id; every other id passes through unchanged.
func (a ommCompatModuleAdmin) ApplyConfig(id string, cfg sdncron.ModuleConfig) (sdncron.ModuleConfig, error) {
	if id != ommCompatModuleID {
		if a.real == nil {
			return nil, sdncron.ErrUnknownModule
		}
		return a.real.ApplyConfig(id, cfg)
	}
	sf := ommFlow()
	if sf == nil {
		return nil, sdncron.ErrUnknownModule
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", sdncron.ErrInvalidConfig, err)
	}

	nodeCfg := make(map[string]interface{}, len(cfg))
	var intervalMs int64
	hasInterval := false
	for k, v := range cfg {
		switch k {
		case "interval_ms":
			ms, ok := asPositiveMs(v)
			if !ok {
				return nil, fmt.Errorf("%w: interval_ms must be a positive integer (milliseconds)", sdncron.ErrInvalidConfig)
			}
			intervalMs, hasInterval = ms, true
		case "timers":
			// The reserved per-timer-method override map has no meaning for
			// this flow (one host-cron trigger, aliased as "run" for compat
			// display only): dropped rather than persisted as a misleading
			// opaque node-config key.
		default:
			nodeCfg[k] = v
		}
	}
	if hasInterval && a.real != nil {
		// Retarget the REAL scheduler registration's timer — reuses its proven
		// live-reschedule (ticker reset) + persistence; this compat id owns no
		// ticker of its own.
		if _, err := a.real.ApplyConfig(ommFlowProgramID, sdncron.ModuleConfig{"interval_ms": float64(intervalMs)}); err != nil {
			return nil, err
		}
	}
	if fi := pluginsdnruntime.FlowInstaller(); fi != nil {
		fi.SetFlowNodeConfig(ommFlowProgramID, nodeCfg)
	}
	return a.effectiveConfig(sf), nil
}
