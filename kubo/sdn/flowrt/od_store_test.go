package flowrt

// od_store_test.go — proves the OD flow PERSISTS its $OMM via the host-model store
// node (store_node.go): trigger -> oem-source -> od.fit -> store, where `store` is a
// host handler that writes the $OMM to a StoreSink. Verifies the persisted record is
// a valid ISS $OMM (NORAD 25544) under (source="supplemental-omm", type="OMM"), the
// type came from the record's own file_identifier, and NO JSON control was involved.

import (
	"context"
	"sync"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

const odFlowStorePluginID = "io.spacedatanetwork.store"

type storedRec struct {
	source  string
	sdsType string
	bytes   []byte
}

type recordingSink struct {
	mu   sync.Mutex
	recs []storedRec
}

func (r *recordingSink) Store(_ context.Context, source, sdsType string, fb []byte) (cid.Cid, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, storedRec{source, sdsType, append([]byte(nil), fb...)})
	return cid.Undef, nil
}

func TestODFlowStorePersistsOMM(t *testing.T) {
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
	storeNode := FlowNodeSpec{NodeID: "n-store", PluginID: odFlowStorePluginID, MethodID: "persist", Kind: "sink", DispatchModel: "host", UIX: 600, UIY: 160}
	timer := FlowTriggerSpec{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}
	toSrc := FlowTriggerBindingSpec{TriggerID: "t0", TargetNodeID: "n-src", TargetPortID: "tick"}
	spec := FlowSpec{
		ProgramID: "org.sdn.flows.od-store", Name: "OD Store", Version: "0.1.0",
		Nodes: []FlowNodeSpec{src, fit, storeNode},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n-src", FromPortID: "oem", ToNodeID: "n-fit", ToPortID: "oem"},
			{EdgeID: "e1", FromNodeID: "n-fit", FromPortID: "omm", ToNodeID: "n-store", ToPortID: "in"},
		},
		Triggers:        []FlowTriggerSpec{timer},
		TriggerBindings: []FlowTriggerBindingSpec{toSrc},
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
		t.Fatalf("flow did not load into a running FlowRuntime")
	}
	rt := fp.runtime
	rt.SetLinkedSection(func(run func() error) error { return run() })

	sink := &recordingSink{}
	handlers := HandlerMap{
		odFlowStorePluginID + ":persist": NewStoreHandler(sink, "supplemental-omm"),
	}
	rt.ResetState()
	rt.EnqueueTrigger(0)
	if _, derr := rt.Drain(ctx, handlers, DrainOptions{MaxIterations: 256}); derr != nil {
		t.Fatalf("Drain: %v", derr)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.recs) != 1 {
		t.Fatalf("store received %d records, want 1", len(sink.recs))
	}
	r := sink.recs[0]
	if r.source != "supplemental-omm" {
		t.Fatalf("stored source = %q, want supplemental-omm", r.source)
	}
	if r.sdsType != "OMM" {
		t.Fatalf("stored type = %q (from file_identifier), want OMM", r.sdsType)
	}
	rec := OMM.GetRootAsOMM(r.bytes, 0) // non-size-prefixed content-addressed form
	if rec.NORAD_CAT_ID() != 25544 || rec.MEAN_MOTION() <= 0 {
		t.Fatalf("stored $OMM invalid: NORAD=%d MEAN_MOTION=%v", rec.NORAD_CAT_ID(), rec.MEAN_MOTION())
	}
	t.Logf("★ OD flow PERSISTS via host-model store node: (source=%q, type=%q from file_identifier) valid ISS $OMM NORAD=%d MEAN_MOTION=%.6f — no JSON control",
		r.source, r.sdsType, rec.NORAD_CAT_ID(), rec.MEAN_MOTION())
}
