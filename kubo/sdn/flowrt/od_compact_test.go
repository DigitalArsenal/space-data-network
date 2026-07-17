package flowrt

// od_compact_test.go — proves the shared COMPACT-form $OEM builder
// (oem_fb::build_oem_flatbuffer_compact) that uniform-cadence providers (e.g.
// SpaceX MEME) will use: a flat EPHEMERIS_DATA vector + START_TIME + STEP_SIZE,
// from which the OD reader reconstructs epoch[i]=START+i*STEP. Flow:
// oem-source-iss.emit_compact -> od.fit -> capture. Asserts od.fit fits the
// compact $OEM to a valid ISS $OMM (NORAD 25544, ISS mean motion, ORIGINATOR
// SDN-OD). The verbose-form path is covered by TestODFlowEndToEnd.

import (
	"context"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

func TestODFlowCompactOEM(t *testing.T) {
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

	src := FlowNodeSpec{NodeID: "n-src", PluginID: odFlowSrcPluginID, MethodID: "emit_compact", Kind: "source", UIX: 40, UIY: 160}
	fit := FlowNodeSpec{NodeID: "n-fit", PluginID: odFlowODPluginID, MethodID: "fit", Kind: "transform", UIX: 320, UIY: 160}
	capNode := FlowNodeSpec{NodeID: "n-cap", PluginID: odFlowCaptureID, MethodID: "sink", Kind: "sink", DispatchModel: "host", UIX: 600, UIY: 160}
	timer := FlowTriggerSpec{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}
	spec := FlowSpec{
		ProgramID: "org.sdn.flows.od-compact", Name: "OD Compact OEM", Version: "0.1.0",
		Nodes: []FlowNodeSpec{src, fit, capNode},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n-src", FromPortID: "oem", ToNodeID: "n-fit", ToPortID: "oem"},
			{EdgeID: "e1", FromNodeID: "n-fit", FromPortID: "omm", ToNodeID: "n-cap", ToPortID: "in"},
		},
		Triggers:        []FlowTriggerSpec{timer},
		TriggerBindings: []FlowTriggerBindingSpec{{TriggerID: "t0", TargetNodeID: "n-src", TargetPortID: "tick"}},
	}

	ctx := context.Background()
	_, programID, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowPLG: BuildFlowPLG(spec)})
	if err != nil {
		t.Fatalf("BakeAndDeploy: %v", err)
	}
	mgr.mu.Lock()
	fp := mgr.running[programID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("flow did not load")
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
	if _, derr := rt.Drain(ctx, handlers, DrainOptions{MaxIterations: 256}); derr != nil {
		t.Fatalf("Drain: %v", derr)
	}

	if len(got) == 0 {
		t.Fatalf("od.fit produced no $OMM from the compact $OEM")
	}
	omm := got[0]
	if len(omm) < 12 || string(omm[8:12]) != "$OMM" {
		t.Fatalf("captured frame is not a size-prefixed $OMM (len=%d)", len(omm))
	}
	rec := OMM.GetRootAsOMM(omm[4:], 0)
	if rec.NORAD_CAT_ID() != 25544 {
		t.Fatalf("compact $OEM fit: NORAD=%d, want 25544", rec.NORAD_CAT_ID())
	}
	if string(rec.ORIGINATOR()) != "SDN-OD" {
		t.Fatalf("compact $OEM fit: ORIGINATOR=%q, want SDN-OD", string(rec.ORIGINATOR()))
	}
	mm := rec.MEAN_MOTION()
	if mm < 15.4 || mm > 15.6 {
		t.Fatalf("compact $OEM fit: MEAN_MOTION=%v outside ISS range [15.4,15.6]", mm)
	}
	t.Logf("★ COMPACT-form $OEM (flat EPHEMERIS_DATA + START_TIME + STEP_SIZE) -> od.fit -> valid ISS $OMM: NORAD=%d MEAN_MOTION=%.9f ORIGINATOR=%s — the shared compact builder uniform-cadence providers use, proven end-to-end",
		rec.NORAD_CAT_ID(), mm, string(rec.ORIGINATOR()))
}
