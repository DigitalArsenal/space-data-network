package flowrt

// od_asset_bake_test.go — bakes the DEPLOY OD flow (feeder[host] -> od.fit -> store
// [host]) and writes the composed runtime.wasm to SDN_ODFLOW_ASSET_OUT. This is how
// the shippable node asset is produced at build time (Docker), so the node loads a
// FlowPool over it with no flowcc toolchain. The feeder + store are host-model nodes
// (Go handlers the runner supplies via RunOEMBatch), so the wasm contains od.fit +
// the flow scheduler; its node ids MUST match the runner's OEMBatchConfig.
//
// Run under the Docker bake env with SDN_ODFLOW_ASSET_OUT set; skipped otherwise.

import (
	"context"
	"os"
	"testing"

	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

// The DEPLOY flow's node plugin ids — the runner's FlowRunEngine OEMBatchConfig must
// name these exactly (FeederPluginID / StorePluginID), and od.fit is the OD module.
const (
	odAssetFeederPluginID = "io.spacedatanetwork.object-feeder"
	odAssetStorePluginID  = "io.spacedatanetwork.store"
)

func TestBakeODRuntimeAsset(t *testing.T) {
	out := os.Getenv("SDN_ODFLOW_ASSET_OUT")
	if out == "" {
		t.Skip("SDN_ODFLOW_ASSET_OUT not set; this test emits the shippable OD runtime.wasm asset")
	}
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

	feeder := FlowNodeSpec{NodeID: "n-feed", PluginID: odAssetFeederPluginID, MethodID: "emit", Kind: "source", DispatchModel: "host", UIX: 40, UIY: 160}
	fit := FlowNodeSpec{NodeID: "n-fit", PluginID: odFlowODPluginID, MethodID: "fit", Kind: "transform", UIX: 320, UIY: 160}
	storeNode := FlowNodeSpec{NodeID: "n-store", PluginID: odAssetStorePluginID, MethodID: "persist", Kind: "sink", DispatchModel: "host", UIX: 600, UIY: 160}
	timer := FlowTriggerSpec{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}
	spec := FlowSpec{
		ProgramID: "org.sdn.flows.od-supplemental-omm", Name: "OD Supplemental-OMM", Version: "1.0.0",
		Description: "fetch(provider)->$OEM(in-memory)->od.fit->store $OMM/$OCM/$OBD. Ephemeris never persisted.",
		Nodes:       []FlowNodeSpec{feeder, fit, storeNode},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n-feed", FromPortID: "oem", ToNodeID: "n-fit", ToPortID: "oem"},
			{EdgeID: "e1", FromNodeID: "n-fit", FromPortID: "omm", ToNodeID: "n-store", ToPortID: "in"},
		},
		Triggers:        []FlowTriggerSpec{timer},
		TriggerBindings: []FlowTriggerBindingSpec{{TriggerID: "t0", TargetNodeID: "n-feed", TargetPortID: "tick"}},
	}

	res, _, err := mgr.BakeAndDeploy(context.Background(), BakeRequest{FlowPLG: BuildFlowPLG(spec)})
	if err != nil {
		t.Fatalf("BakeAndDeploy: %v", err)
	}
	if len(res.Wasm) == 0 {
		t.Fatalf("bake produced empty runtime.wasm")
	}
	if err := os.WriteFile(out, res.Wasm, 0o644); err != nil {
		t.Fatalf("write asset %s: %v", out, err)
	}
	t.Logf("★ OD deploy runtime.wasm asset written: %s (%d bytes) — flow %s, feeder=%s store=%s od=%s",
		out, len(res.Wasm), spec.ProgramID, odAssetFeederPluginID, odAssetStorePluginID, odFlowODPluginID)
}
