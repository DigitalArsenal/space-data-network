package modulert

import (
	"encoding/json"
	"testing"
)

func TestHostcallImportModuleUsesSDKName(t *testing.T) {
	if HostcallImportModule != "space_data_module_host" {
		t.Fatalf("HostcallImportModule = %q, want space_data_module_host", HostcallImportModule)
	}
}

// getConfigResult decodes a plugin.getConfig Dispatch response's "result"
// field into a plain map for assertions.
func getConfigResult(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var env struct {
		OK     bool                   `json:"ok"`
		Result map[string]interface{} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode plugin.getConfig response %s: %v", raw, err)
	}
	if !env.OK {
		t.Fatalf("plugin.getConfig response not ok: %s", raw)
	}
	return env.Result
}

// TestHostBridgeSetConfigLive proves the supplemental-OMM config surface's
// core mechanism: SetConfigLive changes what THIS bridge's plugin.getConfig
// hostcall serves from the very next call — no reload, no re-fire — and never
// mutates the NodeContext the bridge was constructed with (so one flow/module
// instance's config update can never bleed into another sharing that pointer).
func TestHostBridgeSetConfigLive(t *testing.T) {
	shared := &NodeContext{Config: map[string]interface{}{"enabled_providers": []interface{}{"iss"}}}
	hb := NewHostBridge(shared, nil)

	before := getConfigResult(t, hb.Dispatch("plugin.getConfig", nil))
	if got := before["enabled_providers"]; got == nil {
		t.Fatalf("initial plugin.getConfig missing enabled_providers: %#v", before)
	}

	hb.SetConfigLive(map[string]interface{}{"enabled_providers": []interface{}{"iss", "cpf"}, "interval_ms": float64(600000)})

	after := getConfigResult(t, hb.Dispatch("plugin.getConfig", nil))
	providers, ok := after["enabled_providers"].([]interface{})
	if !ok || len(providers) != 2 {
		t.Fatalf("plugin.getConfig after SetConfigLive = %#v, want 2 enabled_providers", after)
	}
	if after["interval_ms"] != float64(600000) {
		t.Fatalf("plugin.getConfig after SetConfigLive interval_ms = %#v, want 600000", after["interval_ms"])
	}

	// The ORIGINAL shared NodeContext must be untouched — SetConfigLive detaches
	// onto a private copy, it never mutates a struct another instance might share.
	if len(shared.Config["enabled_providers"].([]interface{})) != 1 {
		t.Fatalf("SetConfigLive mutated the shared NodeContext in place: %#v", shared.Config)
	}
}

// TestHostBridgeSetConfigLiveNilInitial proves SetConfigLive also works when
// the bridge was constructed with a nil NodeContext (e.g. a capability-free
// test bridge) — plugin.getConfig reports an empty object before, and the new
// config after.
func TestHostBridgeSetConfigLiveNilInitial(t *testing.T) {
	hb := NewHostBridge(nil, nil)
	before := getConfigResult(t, hb.Dispatch("plugin.getConfig", nil))
	if len(before) != 0 {
		t.Fatalf("plugin.getConfig with nil NodeContext = %#v, want empty", before)
	}
	hb.SetConfigLive(map[string]interface{}{"foo": "bar"})
	after := getConfigResult(t, hb.Dispatch("plugin.getConfig", nil))
	if after["foo"] != "bar" {
		t.Fatalf("plugin.getConfig after SetConfigLive(nil-initial) = %#v", after)
	}
}
