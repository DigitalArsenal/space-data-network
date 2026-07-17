package flowrt

// od_obd_test.go — proves od.fit emits an SDS $OBD (Orbit Determination Results)
// record on its "obd" output port from the SAME fit that produces the $OMM: the
// OD-run-result-with-RMS record. Flow: oem-source-iss -> od.fit -> capture[host],
// wiring od.fit's "obd" port (the "omm" port is left unwired/dropped). Validates
// the captured $OBD carries the ISS fit telemetry (SAT_NO 25544, WRMS ~0.0709 km,
// DIFFERENTIAL_CORRECTION, iterations, fit span) — typed FlatBuffer, no JSON.

import (
	"context"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OBD"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

func TestODFlowEmitsOBD(t *testing.T) {
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

	src := FlowNodeSpec{NodeID: "n-src", PluginID: odFlowSrcPluginID, MethodID: "emit", Kind: "source", UIX: 40, UIY: 160}
	fit := FlowNodeSpec{NodeID: "n-fit", PluginID: odFlowODPluginID, MethodID: "fit", Kind: "transform", UIX: 320, UIY: 160}
	capNode := FlowNodeSpec{NodeID: "n-cap", PluginID: odFlowCaptureID, MethodID: "sink", Kind: "sink", DispatchModel: "host", UIX: 600, UIY: 160}
	timer := FlowTriggerSpec{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}
	spec := FlowSpec{
		ProgramID: "org.sdn.flows.od-obd", Name: "OD OBD", Version: "0.1.0",
		Nodes: []FlowNodeSpec{src, fit, capNode},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n-src", FromPortID: "oem", ToNodeID: "n-fit", ToPortID: "oem"},
			{EdgeID: "e1", FromNodeID: "n-fit", FromPortID: "obd", ToNodeID: "n-cap", ToPortID: "in"},
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
		t.Fatalf("od.fit emitted no $OBD on the obd port")
	}
	obd := got[0]
	if len(obd) < 12 || string(obd[8:12]) != "$OBD" {
		t.Fatalf("captured frame is not a size-prefixed $OBD (len=%d)", len(obd))
	}
	rec := OBD.GetRootAsOBD(obd[4:], 0) // strip 4-byte size prefix, like $OMM
	if rec.SAT_NO() != 25544 {
		t.Fatalf("$OBD SAT_NO=%d, want 25544 (ISS)", rec.SAT_NO())
	}
	if int8(rec.METHOD()) != 6 { // odMethod::DIFFERENTIAL_CORRECTION == 6
		t.Fatalf("$OBD METHOD=%d, want 6 (DIFFERENTIAL_CORRECTION)", int8(rec.METHOD()))
	}
	if rec.NUM_ITERATIONS() == 0 {
		t.Fatalf("$OBD NUM_ITERATIONS=0 (the differential-correction fit must iterate)")
	}
	wrms := rec.WRMS()
	if wrms <= 0 || wrms > 1.0 {
		t.Fatalf("$OBD WRMS=%v km outside expected ISS range (0,1]", wrms)
	}
	if rec.FIT_SPAN() <= 0 {
		t.Fatalf("$OBD FIT_SPAN=%v, want >0 (ephemeris span in days)", rec.FIT_SPAN())
	}
	t.Logf("★ od.fit emits $OBD (OD run result): SAT_NO=%d METHOD=DIFFERENTIAL_CORRECTION WRMS=%.9f km NUM_ITERATIONS=%d FIT_SPAN=%.4f d — same fit, typed telemetry, no JSON",
		rec.SAT_NO(), wrms, rec.NUM_ITERATIONS(), rec.FIT_SPAN())
}
