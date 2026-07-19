package flowrt

// servicecfg_test.go — local, toolchain-free proof of the supplemental-OMM
// config surface's ServiceFlow-side plumbing: SourceProviderPluginIDs (the
// GET default provider set) and SetNodeConfig (the PUT live-apply path).
// These exercise the ServiceFlow struct directly (same package, no exported
// constructor besides LoadFlowService, which needs a real compiled bundle) so
// they run everywhere without the wasi-threads bake toolchain — the full
// mount is covered by od_supplemental_bake_test.go (linux, real toolchain)
// and the live node.

import (
	"encoding/json"
	"testing"

	"github.com/ipfs/kubo/sdn/modulert"
)

func TestServiceFlowSourceProviderPluginIDs(t *testing.T) {
	sf := &ServiceFlow{
		sourceProviderPluginIDs: []string{
			"com.orbpro.spacex-starlink-source",
			"com.orbpro.glonass-source",
		},
	}
	got := sf.SourceProviderPluginIDs()
	if len(got) != 2 || got[0] != "com.orbpro.spacex-starlink-source" || got[1] != "com.orbpro.glonass-source" {
		t.Fatalf("SourceProviderPluginIDs() = %v", got)
	}
	// Defensive copy: mutating the returned slice must not affect the ServiceFlow.
	got[0] = "tampered"
	if sf.sourceProviderPluginIDs[0] != "com.orbpro.spacex-starlink-source" {
		t.Fatalf("SourceProviderPluginIDs() leaked its backing array: %v", sf.sourceProviderPluginIDs)
	}
}

func TestServiceFlowSourceProviderPluginIDsEmpty(t *testing.T) {
	sf := &ServiceFlow{}
	if got := sf.SourceProviderPluginIDs(); len(got) != 0 {
		t.Fatalf("SourceProviderPluginIDs() on a bare ServiceFlow = %v, want empty", got)
	}
}

// TestServiceFlowSetNodeConfig proves the PUT-time live-apply mechanism: after
// SetNodeConfig, the flow's bridge serves the NEW config to plugin.getConfig
// immediately (the "next trigger fire" the settings API promises), with no
// reload of sf.inst.
func TestServiceFlowSetNodeConfig(t *testing.T) {
	bridge := modulert.NewHostBridge(&modulert.NodeContext{Config: map[string]interface{}{"enabled_providers": []interface{}{"iss"}}}, nil)
	sf := &ServiceFlow{inst: &flowInstance{bridge: bridge}}

	sf.SetNodeConfig(map[string]interface{}{"enabled_providers": []interface{}{"iss", "cpf", "glonass"}})

	raw := bridge.Dispatch("plugin.getConfig", nil)
	var env struct {
		OK     bool                   `json:"ok"`
		Result map[string]interface{} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("plugin.getConfig after SetNodeConfig: %s (err=%v)", raw, err)
	}
	providers, ok := env.Result["enabled_providers"].([]interface{})
	if !ok || len(providers) != 3 {
		t.Fatalf("plugin.getConfig after SetNodeConfig = %#v, want 3 enabled_providers", env.Result)
	}
}

// TestServiceFlowSetNodeConfigClosedIsNoop proves SetNodeConfig on a closed
// (inst == nil) flow never panics — it is simply a no-op.
func TestServiceFlowSetNodeConfigClosedIsNoop(t *testing.T) {
	sf := &ServiceFlow{}
	sf.SetNodeConfig(map[string]interface{}{"a": 1}) // must not panic
}
