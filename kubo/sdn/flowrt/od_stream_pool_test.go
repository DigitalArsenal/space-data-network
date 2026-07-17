package flowrt

// od_stream_pool_test.go — the runner DATA-PATH proof: a provider $OEM STREAM (the
// exact framing spacex run_pull emits) driven end-to-end through RunOEMStream:
// SplitOEMStream -> RunOEMBatch -> feeder -> od.fit -> store. Asserts every framed
// object fits to its own distinct $OMM and is persisted. This is the whole runner
// data path minus the modulert provider-invoke (proven separately by the spacex
// module's own network-free test), so provider->stream->fit->store is proven by
// composition. Real ISS $OEM (oem-source-iss), distinct NORADs, real baked pool.

import (
	"context"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

func TestODFlowStreamToPool(t *testing.T) {
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

	// A real ISS $OEM, mutated into K distinct-NORAD objects, framed into the
	// provider $OEM stream ([u32le count][ [u32le len][$OEM] ]*) — exactly what a
	// provider's run_pull returns.
	base := captureBaseOEM(t, mgr)
	const K = 8
	const baseNORAD = 80000
	records := make([][]byte, K)
	wantN := map[uint32]bool{}
	for k := 0; k < K; k++ {
		buf := append([]byte(nil), base...)
		oem := OEM.GetRootAsOEM(buf, 0) // non-size-prefixed (see captureBaseOEM)
		norad := uint32(baseNORAD + k)
		if !oem.MutateBlockNORAD(0, norad) {
			t.Fatalf("MutateBlockNORAD failed for k=%d", k)
		}
		records[k] = buf
		wantN[norad] = true
	}
	stream := frameStream(records...)

	// Bake the deploy OD flow and drive the stream through RunOEMStream.
	feeder := FlowNodeSpec{NodeID: "n-feed", PluginID: odFlowFeederPluginID, MethodID: "emit", Kind: "source", DispatchModel: "host", UIX: 40, UIY: 160}
	fit := FlowNodeSpec{NodeID: "n-fit", PluginID: odFlowODPluginID, MethodID: "fit", Kind: "transform", UIX: 320, UIY: 160}
	storeNode := FlowNodeSpec{NodeID: "n-store", PluginID: odFlowStorePluginID, MethodID: "persist", Kind: "sink", DispatchModel: "host", UIX: 600, UIY: 160}
	timer := FlowTriggerSpec{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}
	spec := FlowSpec{
		ProgramID: "org.sdn.flows.od-stream-pool", Name: "OD Stream Pool", Version: "1.0.0",
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
		t.Fatalf("BakeAndDeploy: %v", err)
	}
	pool := NewFlowPool(res.Wasm, 4, 2048)
	sink := &recordingSink{}

	out, err := RunOEMStream(ctx, pool, stream, sink, OEMBatchConfig{
		FeederPluginID: odFlowFeederPluginID, FeederPort: "oem",
		StorePluginID: odFlowStorePluginID, StoreSource: "supplemental-omm",
		Drain: DrainOptions{MaxIterations: 256},
	})
	if err != nil {
		t.Fatalf("RunOEMStream: %v", err)
	}
	if out.Objects != K || out.Fitted != K {
		t.Fatalf("RunOEMStream result = %+v, want Objects=Fitted=%d", out, K)
	}

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
	if len(sink.recs) != K || len(gotN) != K {
		t.Fatalf("stored %d records / %d distinct NORADs, want %d each", len(sink.recs), len(gotN), K)
	}
	for n := range wantN {
		if !gotN[n] {
			t.Fatalf("expected NORAD %d not stored", n)
		}
	}
	t.Logf("★ RUNNER DATA PATH: provider $OEM STREAM (%d objects) -> SplitOEMStream -> RunOEMBatch -> od.fit -> store => %d distinct $OMM persisted (NORAD %d..%d). provider->stream->fit->store proven end-to-end.",
		K, len(gotN), baseNORAD, baseNORAD+K-1)
}
