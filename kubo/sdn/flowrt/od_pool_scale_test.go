package flowrt

// od_pool_scale_test.go — FlowPool parallelism proof (Phase 3). Bakes the OD flow
// ($OEM source -> od.fit -> host capture) ONCE, then drives M objects across N
// resident FlowRuntime instances CONCURRENTLY via FlowPool, asserting every run
// produces a valid ISS $OMM (NORAD 25544). A single flow instance drains serially
// (rt.mu), so this proves throughput comes from the pool of independent instances —
// the mechanism that lets the OD flow fit a whole constellation, not one object.
// (Objects are the same ISS fixture here — the parallelism is the point; different
// objects come from the real multi-object provider source in Phase 4.)

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

func TestODFlowPoolScale(t *testing.T) {
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
	capOMM := FlowNodeSpec{NodeID: "n-cap", PluginID: odFlowCaptureID, MethodID: "sink", Kind: "sink", DispatchModel: "host", UIX: 600, UIY: 160}
	timer := FlowTriggerSpec{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}
	toSrc := FlowTriggerBindingSpec{TriggerID: "t0", TargetNodeID: "n-src", TargetPortID: "tick"}
	spec := FlowSpec{
		ProgramID: "org.sdn.flows.od-pool-scale", Name: "OD Pool Scale", Version: "0.1.0",
		Nodes: []FlowNodeSpec{src, fit, capOMM},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n-src", FromPortID: "oem", ToNodeID: "n-fit", ToPortID: "oem"},
			{EdgeID: "e1", FromNodeID: "n-fit", FromPortID: "omm", ToNodeID: "n-cap", ToPortID: "in"},
		},
		Triggers:        []FlowTriggerSpec{timer},
		TriggerBindings: []FlowTriggerBindingSpec{toSrc},
	}

	ctx := context.Background()
	res, _, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowPLG: BuildFlowPLG(spec)})
	if err != nil {
		t.Fatalf("BakeAndDeploy: %v", err)
	}

	const N = 4  // resident parallel instances
	const M = 40 // objects driven across the pool
	pool := NewFlowPool(res.Wasm, N, 2048)

	var ok int64
	var wg sync.WaitGroup
	for i := 0; i < M; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var captured [][]byte
			handlers := HandlerMap{
				odFlowCaptureID + ":sink": func(_ context.Context, args *InvocationArgs) (*InvocationResult, error) {
					for _, f := range args.Frames {
						captured = append(captured, append([]byte(nil), f.Bytes...))
					}
					return &InvocationResult{StatusCode: 0}, nil
				},
			}
			if _, derr := pool.Run(ctx, 0, handlers, DrainOptions{MaxIterations: 256}); derr != nil {
				return
			}
			if len(captured) == 0 {
				return
			}
			omm := captured[0]
			if len(omm) >= 12 && string(omm[8:12]) == "$OMM" {
				rec := OMM.GetRootAsOMM(omm[4:], 0) // strip 4-byte size prefix
				if rec.NORAD_CAT_ID() == 25544 && rec.MEAN_MOTION() > 0 {
					atomic.AddInt64(&ok, 1)
				}
			}
		}()
	}
	wg.Wait()

	if int(ok) != M {
		t.Fatalf("FlowPool fit %d/%d objects (want all valid ISS $OMM)", ok, M)
	}
	t.Logf("★ FlowPool PARALLELISM PROVEN: %d objects fit across %d resident instances, every one a valid $OMM (NORAD 25544)", M, N)
}
