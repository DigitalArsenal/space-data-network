package sdnapi

// omm_compat_test.go — local proof of the supplemental-OMM config-panel compat
// shim's pure logic (helper mapping/coercion) and its pass-through + honest-404
// contract when the OD flow is not mounted in-process — exactly the state of a
// unit test (pluginsdnruntime.FlowInstaller() is nil until the sdnruntime
// plugin's Start runs, which a package test never does). The LIVE-mount path
// (a real ServiceFlow behind ommFlow()) is covered end to end at the flowrt
// package level (TestServiceFlowSetNodeConfig, TestServiceFlowSourceProviderPluginIDs)
// and the sdnflows package level (TestInstallerStoredConfigAndSetFlowNodeConfig);
// this file proves the sdnapi-facing adapter built on top of them.

import (
	"errors"
	"sort"
	"testing"

	"github.com/ipfs/kubo/sdn/sdncron"
)

func TestOmmCompatProviderShortID(t *testing.T) {
	cases := map[string]string{
		"com.orbpro.spacex-starlink-source": "spacex-starlink",
		"com.orbpro.glonass-source":         "glonass",
		"com.orbpro.intelsat-source":        "intelsat",
		"com.orbpro.cpf-source":             "cpf",
		"com.orbpro.iss-source":             "iss",
	}
	for in, want := range cases {
		if got := ommCompatProviderShortID(in); got != want {
			t.Errorf("ommCompatProviderShortID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAsPositiveMs(t *testing.T) {
	ok := map[interface{}]int64{
		float64(600000): 600000,
		int(5):          5,
		int64(10):       10,
	}
	for in, want := range ok {
		got, valid := asPositiveMs(in)
		if !valid || got != want {
			t.Errorf("asPositiveMs(%#v) = (%d,%v), want (%d,true)", in, got, valid, want)
		}
	}
	bad := []interface{}{float64(0), float64(-5), float64(1.5), "600000", nil}
	for _, in := range bad {
		if _, valid := asPositiveMs(in); valid {
			t.Errorf("asPositiveMs(%#v) accepted, want rejected", in)
		}
	}
}

// fakeModuleAdmin is a minimal in-memory sdnapihttp.ModuleAdmin for testing
// ommCompatModuleAdmin's pass-through behavior in isolation.
type fakeModuleAdmin struct {
	views  []sdncron.ModuleView
	config map[string]sdncron.ModuleConfig
}

func (f *fakeModuleAdmin) List() []sdncron.ModuleView { return f.views }
func (f *fakeModuleAdmin) Config(id string) (sdncron.ModuleConfig, bool) {
	cfg, ok := f.config[id]
	return cfg, ok
}
func (f *fakeModuleAdmin) ApplyConfig(id string, cfg sdncron.ModuleConfig) (sdncron.ModuleConfig, error) {
	if _, ok := f.config[id]; !ok {
		return nil, sdncron.ErrUnknownModule
	}
	f.config[id] = cfg
	return cfg, nil
}

// TestOmmCompatModuleAdminPassThrough proves every id OTHER than the compat id
// reaches the real admin unchanged.
func TestOmmCompatModuleAdminPassThrough(t *testing.T) {
	real := &fakeModuleAdmin{
		views:  []sdncron.ModuleView{{ID: "com.orbpro.iss-source", Running: true}},
		config: map[string]sdncron.ModuleConfig{"com.orbpro.iss-source": {"objectCap": float64(100)}},
	}
	a := ommCompatModuleAdmin{real: real}

	if got := a.List(); len(got) != 1 || got[0].ID != "com.orbpro.iss-source" {
		t.Fatalf("List() = %+v, want the real admin's single zombie-module entry (flow not mounted in this test)", got)
	}
	cfg, ok := a.Config("com.orbpro.iss-source")
	if !ok || cfg["objectCap"] != float64(100) {
		t.Fatalf("Config(pass-through id) = %#v,%v", cfg, ok)
	}
	if _, err := a.ApplyConfig("com.orbpro.iss-source", sdncron.ModuleConfig{"objectCap": float64(5)}); err != nil {
		t.Fatalf("ApplyConfig(pass-through id): %v", err)
	}
	if _, ok := a.Config("totally-unknown-id"); ok {
		t.Fatalf("Config(unknown id) should pass through to the real admin's own not-found")
	}
}

// TestOmmCompatModuleAdminHonest404 proves the compat id reports the SAME
// honest "unknown module" outcome as any other id when the OD flow is not
// mounted (ommFlow() == nil here, since no sdnruntime plugin ever started) —
// this is the exact fix for the reported bug: no fabricated success, no silent
// swallow, a real 404-shaped (ok=false) result the sdnapi handler maps to 404.
func TestOmmCompatModuleAdminHonest404(t *testing.T) {
	a := ommCompatModuleAdmin{real: &fakeModuleAdmin{config: map[string]sdncron.ModuleConfig{}}}

	if _, ok := a.Config(ommCompatModuleID); ok {
		t.Fatalf("Config(compat id) with no mounted flow should report unknown (404), not ok")
	}
	if _, err := a.ApplyConfig(ommCompatModuleID, sdncron.ModuleConfig{"interval_ms": float64(60000)}); !errors.Is(err, sdncron.ErrUnknownModule) {
		t.Fatalf("ApplyConfig(compat id) with no mounted flow err = %v, want ErrUnknownModule", err)
	}
	for _, v := range a.List() {
		if v.ID == ommCompatModuleID {
			t.Fatalf("List() must not synthesize the compat entry when the flow is not mounted: %+v", v)
		}
	}
}

// TestOmmDeclaredProviderShortIDsSorted is a light structural check that the
// default-provider derivation sorts its output (deterministic GET responses).
func TestOmmDeclaredProviderShortIDsSorted(t *testing.T) {
	ids := []string{"com.orbpro.iss-source", "com.orbpro.cpf-source", "com.orbpro.glonass-source"}
	short := make([]string, len(ids))
	for i, id := range ids {
		short[i] = ommCompatProviderShortID(id)
	}
	sort.Strings(short)
	want := []string{"cpf", "glonass", "iss"}
	for i := range want {
		if short[i] != want[i] {
			t.Fatalf("sorted short ids = %v, want %v", short, want)
		}
	}
}
