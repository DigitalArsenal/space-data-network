package flowrt

// od_multiobject_test.go — proves the OD flow fits MANY DISTINCT objects, not just
// ISS (the reported prod bug). It drives feeder[host] -> od.fit -> store[host] over
// a FlowPool: a shared ObjectFeeder hands each concurrent drain a DISTINCT $OEM, and
// the test asserts one distinct-NORAD $OMM per object is persisted. The distinct
// $OEM records are synthesized from the EXACT record oem-source-iss emits (proven
// framing od.fit accepts) by rewriting NORAD on a copy — so identity flows
// $OEM.NORAD -> fit -> $OMM.NORAD and distinctness at the output proves per-object
// routing, not just throughput (which od_pool_scale_test already covers with copies).

import (
	"context"
	"sync"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

const odFlowFeederPluginID = "io.spacedatanetwork.object-feeder"

// captureBaseOEM bakes oem-source-iss -> capture[host], drives it once, and returns
// the exact size-prefixed $OEM the source emits.
func captureBaseOEM(t *testing.T, mgr *FlowManager) []byte {
	t.Helper()
	src := FlowNodeSpec{NodeID: "n-src", PluginID: odFlowSrcPluginID, MethodID: "emit", Kind: "source", UIX: 40, UIY: 160}
	capNode := FlowNodeSpec{NodeID: "n-cap", PluginID: odFlowCaptureID, MethodID: "sink", Kind: "sink", DispatchModel: "host", UIX: 320, UIY: 160}
	timer := FlowTriggerSpec{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}
	spec := FlowSpec{
		ProgramID: "org.sdn.flows.oem-capture", Name: "OEM Capture", Version: "0.1.0",
		Nodes:           []FlowNodeSpec{src, capNode},
		Edges:           []FlowEdgeSpec{{EdgeID: "e0", FromNodeID: "n-src", FromPortID: "oem", ToNodeID: "n-cap", ToPortID: "in"}},
		Triggers:        []FlowTriggerSpec{timer},
		TriggerBindings: []FlowTriggerBindingSpec{{TriggerID: "t0", TargetNodeID: "n-src", TargetPortID: "tick"}},
	}
	ctx := context.Background()
	_, programID, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowPLG: BuildFlowPLG(spec)})
	if err != nil {
		t.Fatalf("capture bake: %v", err)
	}
	mgr.mu.Lock()
	fp := mgr.running[programID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("capture flow did not load")
	}
	rt := fp.runtime
	rt.SetLinkedSection(func(run func() error) error { return run() })
	var got [][]byte
	handlers := HandlerMap{odFlowCaptureID + ":sink": func(_ context.Context, args *InvocationArgs) (*InvocationResult, error) {
		for _, f := range args.Frames {
			got = append(got, append([]byte(nil), f.Bytes...))
		}
		return &InvocationResult{StatusCode: 0}, nil
	}}
	rt.ResetState()
	rt.EnqueueTrigger(0)
	if _, err := rt.Drain(ctx, handlers, DrainOptions{MaxIterations: 256}); err != nil {
		t.Fatalf("capture drain: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("captured no $OEM from oem-source-iss")
	}
	oem := got[0]
	// oem-source emits a NON-size-prefixed $OEM (file_identifier at [4:8]); od.fit
	// accepts this framing (proven in od_flow_e2e_test PROBE1). od.fit's own $OMM
	// output is size-prefixed (file_id at [8:12]) — the two hops differ, which is why
	// the store node handles size-prefixed and the feeder replays this exact form.
	if len(oem) < 8 || string(oem[4:8]) != "$OEM" {
		t.Fatalf("captured frame is not a $OEM (len=%d)", len(oem))
	}
	return oem
}

func TestODFlowMultiObjectDistinctNORAD(t *testing.T) {
	a := resolveBakeAssets(t)
	home := stageODFlowHome(t, a)

	store := envOr("SDN_ODFLOW_STORE", t.TempDir())
	cfg := flowconfig.FlowsConfig{Enabled: true, StoragePath: store, MaxMemoryPages: 2048}
	mgr, err := NewFlowManager(cfg, plugins.New(), HandlerMap{})
	if err != nil {
		t.Fatalf("NewFlowManager: %v", err)
	}
	baker, err := NewBaker(home, 2048)
	if err != nil {
		t.Fatalf("NewBaker: %v", err)
	}
	mgr.SetBaker(baker)

	base := captureBaseOEM(t, mgr)

	// Synthesize K DISTINCT $OEM records by rewriting NORAD on copies of the proven
	// record — different objects, framing od.fit already accepts.
	const K = 12
	const baseNORAD = 90000
	records := make([][]byte, K)
	wantN := map[uint32]bool{}
	for k := 0; k < K; k++ {
		buf := append([]byte(nil), base...)
		oem := OEM.GetRootAsOEM(buf, 0) // non-size-prefixed (see captureBaseOEM)
		norad := uint32(baseNORAD + k)
		if !oem.MutateBlockNORAD(0, norad) {
			t.Fatalf("MutateBlockNORAD failed for k=%d (base NORAD field absent?)", k)
		}
		if got, ok := oem.BlockNORAD(0); !ok || got != norad {
			t.Fatalf("NORAD mutate readback k=%d: got %d ok=%v want %d", k, got, ok, norad)
		}
		records[k] = buf
		wantN[norad] = true
	}

	// feeder[host] -> od.fit -> store[host]
	feeder := FlowNodeSpec{NodeID: "n-feed", PluginID: odFlowFeederPluginID, MethodID: "emit", Kind: "source", DispatchModel: "host", UIX: 40, UIY: 160}
	fit := FlowNodeSpec{NodeID: "n-fit", PluginID: odFlowODPluginID, MethodID: "fit", Kind: "transform", UIX: 320, UIY: 160}
	storeNode := FlowNodeSpec{NodeID: "n-store", PluginID: odFlowStorePluginID, MethodID: "persist", Kind: "sink", DispatchModel: "host", UIX: 600, UIY: 160}
	timer := FlowTriggerSpec{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}
	spec := FlowSpec{
		ProgramID: "org.sdn.flows.od-multiobject", Name: "OD MultiObject", Version: "0.1.0",
		Nodes: []FlowNodeSpec{feeder, fit, storeNode},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n-feed", FromPortID: "oem", ToNodeID: "n-fit", ToPortID: "oem"},
			{EdgeID: "e1", FromNodeID: "n-fit", FromPortID: "omm", ToNodeID: "n-store", ToPortID: "in"},
		},
		Triggers:        []FlowTriggerSpec{timer},
		TriggerBindings: []FlowTriggerBindingSpec{{TriggerID: "t0", TargetNodeID: "n-feed", TargetPortID: "tick"}},
	}

	ctx := context.Background()
	res, _, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowPLG: BuildFlowPLG(spec)})
	if err != nil {
		t.Fatalf("feeder bake: %v", err)
	}

	const N = 4
	pool := NewFlowPool(res.Wasm, N, 2048)
	feed := NewObjectFeeder(records)
	sink := &recordingSink{}
	handlers := HandlerMap{
		odFlowFeederPluginID + ":emit":   NewObjectFeederHandler(feed, "oem"),
		odFlowStorePluginID + ":persist": NewStoreHandler(sink, "supplemental-omm"),
	}

	var wg sync.WaitGroup
	for i := 0; i < K; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = pool.Run(ctx, 0, handlers, DrainOptions{MaxIterations: 256})
		}()
	}
	wg.Wait()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	gotN := map[uint32]bool{}
	for _, r := range sink.recs {
		if r.sdsType != "OMM" {
			t.Fatalf("stored non-OMM type %q", r.sdsType)
		}
		rec := OMM.GetRootAsOMM(r.bytes, 0)
		if rec.MEAN_MOTION() <= 0 {
			t.Fatalf("stored $OMM NORAD=%d has non-positive mean motion", rec.NORAD_CAT_ID())
		}
		gotN[rec.NORAD_CAT_ID()] = true
	}
	if len(sink.recs) != K {
		t.Fatalf("stored %d $OMM records, want %d", len(sink.recs), K)
	}
	if len(gotN) != K {
		t.Fatalf("stored %d DISTINCT NORADs, want %d (distinct-object routing broken)", len(gotN), K)
	}
	for n := range wantN {
		if !gotN[n] {
			t.Fatalf("expected NORAD %d not among stored $OMMs", n)
		}
	}
	t.Logf("★ OD flow fits %d DISTINCT objects across a %d-instance pool: NORAD %d..%d each -> its own $OMM, persisted (source=supplemental-omm) — NOT just ISS", K, N, baseNORAD, baseNORAD+K-1)
}
