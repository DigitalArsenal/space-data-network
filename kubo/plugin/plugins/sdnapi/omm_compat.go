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

// unfittableProvider is a KNOWN, NAMED provider the board can offer as a
// read-only, permanently-disabled row with an inline reason — never a
// silent drop. This is a static registry (not derived from the flow), since
// these providers have NO flow node at all (there is nothing in
// SourceProviderPluginIDs to derive them from); it exists purely so the
// board can explain, rather than just refuse, an operator's attempt to
// enable them. If a provider here ever becomes fittable (e.g. a GLONASS FTP
// fix), the fix is to DELETE its entry here — the flow's declared provider
// set (ommDeclaredProviderShortIDs) is what actually drives what is
// enable-able, this registry only ever narrows what can be requested.
type unfittableProvider struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Reason   string `json:"reason"`
}

// knownUnfittableProviders: keep in sync with the board's static PROVIDERS
// list in omm_board.html (the "gps"/"oneweb" rows) — this is the single
// source of truth for WHY they cannot be enabled; the board renders exactly
// these reasons rather than hardcoding its own copy.
var knownUnfittableProviders = []unfittableProvider{
	{Provider: "gps", Label: "GPS", Reason: "Almanac feed (SEM/YUMA); not an OD source per data policy."},
	{Provider: "oneweb", Label: "OneWeb", Reason: "LTEF feed is metadata-only (no state vectors); not fittable."},
}

// rejectedProvider is one entry PUT's enabled_providers named that could not
// be enabled, with why — returned alongside the normalized (accepted) set
// rather than silently stripped or a hard failure, so the board can show an
// explicit notice naming exactly what was rejected and why.
type rejectedProvider struct {
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
}

// unfittableReason returns the known reason for provider, or a generic
// fallback for a string the board/operator sent that matches neither a
// flow-declared provider nor a known-unfittable one (e.g. a typo).
func unfittableReason(provider string) string {
	for _, u := range knownUnfittableProviders {
		if u.Provider == provider {
			return u.Reason
		}
	}
	return "not a provider this flow declares"
}

// splitEnabledProviders partitions a PUT's raw enabled_providers array into
// the accepted (declared-fittable) set and a rejected list with reasons —
// deduplicated, accepted set sorted for a deterministic response. Never
// silent: every entry that is not in declared ends up in rejected.
func splitEnabledProviders(raw []interface{}, declared []string) (accepted []string, rejected []rejectedProvider) {
	declaredSet := make(map[string]bool, len(declared))
	for _, d := range declared {
		declaredSet[d] = true
	}
	seen := make(map[string]bool, len(raw))
	for _, v := range raw {
		p, ok := v.(string)
		if !ok || p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if declaredSet[p] {
			accepted = append(accepted, p)
		} else {
			rejected = append(rejected, rejectedProvider{Provider: p, Reason: unfittableReason(p)})
		}
	}
	sort.Strings(accepted)
	return accepted, rejected
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
	// declared_providers is ALWAYS the flow's full fittable set (real $PLG
	// topology metadata), independent of which subset is currently enabled —
	// the board needs this to render every toggleable row even after an
	// operator has saved a partial selection (enabled_providers alone would
	// make every unchecked-but-still-fittable provider vanish from the panel
	// with no way to switch it back on — the load-bearing bug this field
	// exists to prevent).
	out["declared_providers"] = ommDeclaredProviderShortIDs(sf)
	// unfittable_providers is ALWAYS present (never conditional on a PUT) so
	// the board can render the gps/oneweb rows as permanently-disabled with
	// their reason on first load, not just after a rejected save attempt.
	out["unfittable_providers"] = knownUnfittableProviders
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
	var rejected []rejectedProvider
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
		case "enabled_providers":
			// Explicit, never-silent validation: only the flow's DECLARED
			// (fittable) providers persist. Anything else — a known-
			// unfittable id like "gps"/"oneweb", or an unrecognized string —
			// is stripped here AND named back in the response's `rejected`
			// field with why, rather than silently accepted-then-ignored or
			// silently dropped (the reported bug: the UI had no way to know
			// its selection didn't take).
			arr, ok := v.([]interface{})
			if !ok {
				return nil, fmt.Errorf("%w: enabled_providers must be an array of provider ids", sdncron.ErrInvalidConfig)
			}
			accepted, rej := splitEnabledProviders(arr, ommDeclaredProviderShortIDs(sf))
			nodeCfg["enabled_providers"] = accepted
			rejected = rej
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
	applied := a.effectiveConfig(sf)
	if len(rejected) > 0 {
		// Accept-and-strip, never a hard failure: the accepted providers
		// still save; `rejected` lets the board surface an explicit notice
		// naming exactly what didn't take and why, instead of the checkbox
		// just silently snapping back.
		applied["rejected"] = rejected
	}
	return applied, nil
}
