package flowrt

// Phase 2c acceptance: drive a REAL compiled flow bundle's finite-state
// machine directly through FlowRuntime, with the sdn/modulert hostcall bridge
// as the ONLY extra host import — no storage, no engine link, no httpmount.
//
// This proves the storage-free flow CORE: runtime.go loads a flow-wasm, reads
// its node/edge/trigger descriptor tables out of linear memory, and steps the
// FSM (EnqueueTrigger -> GetReadyNodeIndex -> BeginInvocation -> dispatch ->
// CompleteInvocation) so that real per-node runtime state advances. "A module
// is the degenerate flow": the flow's nodes invoke modules through the bridge,
// and the run path never touches the deferred Phase 3 data plane.
//
// Every existing sdn-server flowrt test that runs a REAL flow does so through
// the DEFERRED serving layer (RegisterFlowMounts / LoadMountedFlow in
// httpmount.go, or LoadFlowService in cronmount.go) and/or an engine_link
// EngineLink — none drive FlowRuntime standalone. So per the port plan's
// fallback, this test drives the FSM export ABI directly against the smallest
// timer-triggered production bundle (celestrak-gp: a timer fires a
// celestrak-request transform), which needs no crafted HTTP request frame.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/wasmrt"
)

// findCelestrakGPFlowBundle resolves the compiled celestrak-gp flow bundle's
// runtime.wasm from either a normal checkout or a git worktree, mirroring the
// established skip-if-absent pattern of the other real-bundle flowrt tests.
func findCelestrakGPFlowBundle(t *testing.T) string {
	t.Helper()

	suffix := filepath.Join("space-data-network-modules", "flows",
		"celestrak-ingest", "dist", "gp", "runtime.wasm")

	if env := os.Getenv("SDN_CELESTRAK_GP_FLOW_WASM"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
	}

	_, callerFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	anchor := filepath.Dir(callerFile) // .../space-data-network/kubo/sdn/flowrt

	candidates := []string{
		// kubo/sdn/flowrt -> main-packages (../../../..)
		filepath.Join(anchor, "..", "..", "..", "..", suffix),
		// worktree layout: one extra level up
		filepath.Join(anchor, "..", "..", "..", "..", "..", suffix),
		filepath.Join(anchor, "..", "..", "..", "..", "..", "..", suffix),
	}
	for _, c := range candidates {
		cleaned := filepath.Clean(c)
		if _, err := os.Stat(cleaned); err == nil {
			return cleaned
		}
	}

	t.Skipf("celestrak-gp flow bundle not found (set SDN_CELESTRAK_GP_FLOW_WASM); checked %v", candidates)
	return ""
}

// TestFlowRuntimeStepsRealFlowFSM loads the real celestrak-gp flow bundle and
// drives its FSM through FlowRuntime, asserting real state progression: the
// bundle parses into a non-empty node/edge/trigger graph, and firing the timer
// trigger and draining advances at least one node's InvocationCount from 0.
func TestFlowRuntimeStepsRealFlowFSM(t *testing.T) {
	wasmPath := findCelestrakGPFlowBundle(t)

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read flow wasm: %v", err)
	}

	// The bundle imports the SDK sync-hostcall module (space_data_module_host).
	// Supply it via the sdn/modulert bridge — an empty NodeContext with no
	// granted capabilities is enough to instantiate and step the FSM: any
	// capability hostcall a node makes returns an error envelope (never a
	// trap), so the FSM still advances. NO storage / engine link is wired.
	bridge := modulert.NewHostBridge(&modulert.NodeContext{}, nil)

	rt, err := NewFlowRuntime(wasmBytes, 2048,
		wasmrt.WithHostModule(modulert.HostcallImportModule, bridge.BuildWasmEdgeHostFuncs()))
	if err != nil {
		t.Fatalf("NewFlowRuntime: %v", err)
	}
	defer rt.Release()

	// The descriptor tables come straight out of the compiled artifact's linear
	// memory: a real flow graph, not an empty stub.
	if rt.NodeCount == 0 {
		t.Fatal("NodeCount = 0: flow artifact carries no nodes (not a real flow graph)")
	}
	if rt.EdgeCount == 0 {
		t.Fatal("EdgeCount = 0: flow artifact carries no edges")
	}
	if rt.TriggerCount == 0 {
		t.Fatal("TriggerCount = 0: flow artifact carries no triggers to fire")
	}
	t.Logf("loaded flow: %d nodes, %d edges, %d triggers, %d deps",
		rt.NodeCount, rt.EdgeCount, rt.TriggerCount, rt.DepCount)

	// Snapshot per-node invocation counts BEFORE driving the FSM (all zero on a
	// freshly reset runtime).
	before := nodeInvocationCounts(t, rt)
	var beforeSum uint64
	for _, c := range before {
		beforeSum += c
	}
	if beforeSum != 0 {
		t.Fatalf("pre-drive InvocationCount sum = %d, want 0 on a fresh runtime", beforeSum)
	}

	// Drive the FSM: reset, fire the timer trigger, drain. This is the real run
	// path — GetReadyNodeIndex -> BeginInvocation -> dispatch (in-wasm
	// linked-direct / drain_linked, or host handler) -> CompleteInvocation.
	rt.ResetState()
	rt.EnqueueTrigger(0) // the sole trigger (timer-gp) is index 0

	result, err := rt.Drain(context.Background(), HandlerMap{}, DrainOptions{MaxIterations: 256})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	t.Logf("drain result: iterations=%d nodesInvoked=%d handlersSkipped=%d",
		result.Iterations, result.NodesInvoked, result.HandlersSkipped)

	if result.NodesInvoked == 0 && result.Iterations == 0 {
		t.Fatal("Drain did nothing: the FSM never became ready after firing the trigger")
	}

	// Real state progression: at least one node's InvocationCount must have
	// advanced. This is read back out of the artifact's linear memory, so it
	// reflects the wasm FSM actually executing a node — not a Go-side stub.
	after := nodeInvocationCounts(t, rt)
	var afterSum uint64
	advanced := 0
	for i, c := range after {
		afterSum += c
		if c > before[i] {
			advanced++
			t.Logf("node[%d] InvocationCount %d -> %d", i, before[i], c)
		}
	}
	if afterSum == 0 {
		t.Fatal("no node advanced its InvocationCount: FSM did not step a real node")
	}
	if advanced == 0 {
		t.Fatal("InvocationCount sum grew but no individual node advanced (inconsistent state)")
	}
	t.Logf("FSM stepped: %d node(s) advanced, total invocations %d -> %d",
		advanced, beforeSum, afterSum)
}

// nodeInvocationCounts reads each node's runtime-state InvocationCount out of
// the flow artifact's linear memory.
func nodeInvocationCounts(t *testing.T, rt *FlowRuntime) []uint64 {
	t.Helper()
	counts := make([]uint64, rt.NodeCount)
	for i := uint32(0); i < rt.NodeCount; i++ {
		st, err := rt.GetNodeRuntimeState(i)
		if err != nil {
			t.Fatalf("GetNodeRuntimeState(%d): %v", i, err)
		}
		counts[i] = st.InvocationCount
	}
	return counts
}
