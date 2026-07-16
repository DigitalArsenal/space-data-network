package flowrt

// subflow_test.go is the Phase-4 make-or-break proof: a COMPOSED FLOW published
// as a MODULE, then RE-COMPOSED as a single node inside ANOTHER flow that bakes
// and RUNS — the "a module is a degenerate flow" recursion closing end-to-end.
//
// Flow F (the sub-flow) is a 2-node inner graph clock.now -> omm-json.encode with
// aggregate ports fin (-> clock.trigger) and fout (<- clock.time). EmitSubflowModule
// wraps F as a relocatable guest-link object. Flow G links that object as one node
// (F) wired to decision-gate.dispatch. Driving G's trigger must fire F (its wrapper
// runs the inner clock->omm-json graph over the shared shim) and route F's aggregate
// output into decision-gate — proving F executed its inner flow as a black-box node.
//
// Part A stages F locally and proves the recursion. Part B proves the full publish
// path: F is content-addressed + Ed25519-signed + published, and G bakes it via
// fetch-to-bake (signed-only) — reusing the exact module signature/publish pipeline.
//
// Skips cleanly unless the toolchain + modules monorepo are present (same gate as
// bake_test.go).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/flowcc"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

const subflowFPluginID = "com.digitalarsenal.flows.subflow-clockjson"

// subflowFFlowJSON: F's inner graph — clock.now(c) -> omm-json.encode(j). Its
// aggregate ports are declared explicitly by the test (fin -> c.trigger,
// fout <- c.time). c.time is BOTH wired to j.stream AND tapped as fout, so F emits
// a guaranteed clock timestamp on fout while still routing into its second inner
// node (proving inner routing, not a 1-node passthrough).
const subflowFFlowJSON = `{
  "programId": "com.digitalarsenal.flows.subflow-clockjson",
  "name": "Subflow Clock->Json",
  "version": "0.1.0",
  "nodes": [
    { "nodeId": "c", "pluginId": "com.digitalarsenal.hostcap.clock",        "methodId": "now",    "kind": "transform" },
    { "nodeId": "j", "pluginId": "com.digitalarsenal.foundation.omm-json",  "methodId": "encode", "kind": "transform" }
  ],
  "edges": [
    { "edgeId": "e0", "fromNodeId": "c", "fromPortId": "time", "toNodeId": "j", "toPortId": "stream" }
  ],
  "triggers": [],
  "triggerBindings": []
}`

// subflowGFlowJSON: the OUTER flow. Node f is the flow-module F; node d is an
// ordinary module (decision-gate.dispatch). A timer trigger feeds F.fin; F's fout
// feeds d.decision. Baking G must link F's guest-link object + decision-gate's and
// run both.
const subflowGFlowJSON = `{
  "programId": "com.digitalarsenal.flows.subflow-g",
  "name": "Subflow G (re-composes F)",
  "version": "0.1.0",
  "nodes": [
    { "nodeId": "f", "pluginId": "com.digitalarsenal.flows.subflow-clockjson", "methodId": "run",      "kind": "transform" },
    { "nodeId": "d", "pluginId": "com.digitalarsenal.foundation.decision-gate", "methodId": "dispatch", "kind": "transform" }
  ],
  "edges": [
    { "edgeId": "e0", "fromNodeId": "f", "fromPortId": "fout", "toNodeId": "d", "toPortId": "decision" }
  ],
  "triggers": [ { "triggerId": "t0", "kind": "timer", "source": "host-cron" } ],
  "triggerBindings": [ { "triggerId": "t0", "targetNodeId": "f", "targetPortId": "fin" } ]
}`

func subflowSpec() SubflowSpec {
	return SubflowSpec{
		PluginID:     subflowFPluginID,
		Method:       "run",
		FlowJSON:     json.RawMessage(subflowFFlowJSON),
		Inputs:       []SubflowExternalPort{{ExtPort: "fin", NodeID: "c", Port: "trigger", Any: true}},
		Outputs:      []SubflowExternalPort{{ExtPort: "fout", NodeID: "c", Port: "time", Any: true}},
		FinalizeWasm: true,
	}
}

// TestSubflowRecompositionEndToEnd is the ★ make-or-break: publish flow F as a
// module, bake a NEW flow G that uses F as a node, G bakes + RUNS with F executing
// its inner flow.
func TestSubflowRecompositionEndToEnd(t *testing.T) {
	a := resolveBakeAssets(t)
	home := stageBakeHome(t, a)

	storeDir := t.TempDir()
	cfg := flowconfig.FlowsConfig{Enabled: true, StoragePath: storeDir, MaxMemoryPages: 2048}
	mgr, err := NewFlowManager(cfg, plugins.New(), HandlerMap{})
	if err != nil {
		t.Fatalf("NewFlowManager: %v", err)
	}
	baker, err := NewBaker(home, 2048)
	if err != nil {
		t.Fatalf("NewBaker: %v", err)
	}
	mgr.SetBaker(baker)
	ctx := context.Background()

	// Emit F as a re-composable guest-link module.
	fmod, err := baker.EmitSubflowModule(ctx, subflowSpec())
	if err != nil {
		t.Fatalf("EmitSubflowModule(F): %v", err)
	}
	if len(fmod.GuestLinkObj) < 8 || fmod.GuestLinkObj[0] != 0x00 || string(fmod.GuestLinkObj[1:4]) != "asm" {
		t.Fatalf("F guest-link object is not a WASM object (%d bytes)", len(fmod.GuestLinkObj))
	}
	if fmod.EntrySymbol != "sdm_guest_636f6d2e6469676974616c61_run" {
		t.Fatalf("F entry symbol = %q, want sdm_guest_636f6d2e6469676974616c61_run", fmod.EntrySymbol)
	}
	if len(fmod.Finalized) < 8 {
		t.Errorf("F finalized module.wasm not produced (%d bytes)", len(fmod.Finalized))
	}
	t.Logf("★ emitted F as a module: pluginId=%s method=%s entry=%s guestLink=%dB finalizedWasm=%dB innerModules=%v",
		fmod.PluginID, fmod.Method, fmod.EntrySymbol, len(fmod.GuestLinkObj), len(fmod.Finalized), fmod.Modules)

	// Part A — stage F locally and prove re-composition + run.
	if err := flowcc.StageModuleBytes(home, fmod.PluginID, fmod.GuestLinkObj, fmod.Metadata, fmod.Manifest, "local"); err != nil {
		t.Fatalf("stage F module: %v", err)
	}
	fAdvanced, dAdvanced := bakeRunG(t, ctx, mgr, nil)
	if !fAdvanced {
		t.Fatalf("F node did not advance in G — the flow-module did not execute")
	}
	if !dAdvanced {
		t.Fatalf("decision-gate did not advance — F produced no aggregate output that G routed")
	}
	t.Logf("★★ RECURSION PROVEN (local stage): G baked + ran; F executed its inner clock->omm-json flow and emitted fout into decision-gate")

	// Part B — full publish loop: content-address + Ed25519-sign F, publish it,
	// then bake a fresh G via fetch-to-bake (signed-only), and run.
	bs := newMemBlockstore()
	bundleBytes, err := MarshalBundle(&ModuleBundle{PluginID: fmod.PluginID, Object: fmod.GuestLinkObj, Metadata: fmod.Metadata, Manifest: fmod.Manifest})
	if err != nil {
		t.Fatalf("MarshalBundle(F): %v", err)
	}
	contentHash, _, err := appmanifest.StoreModuleBytes(ctx, bs, bundleBytes)
	if err != nil {
		t.Fatalf("StoreModuleBytes(F): %v", err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	digest, _ := hex.DecodeString(contentHash)
	ref := BakeModuleRef{
		PluginID:     fmod.PluginID,
		BundleHash:   contentHash,
		Signature:    base64.StdEncoding.EncodeToString(ed25519.Sign(priv, digest)),
		SignerPubKey: hex.EncodeToString(pub),
	}

	// Fresh manager/baker whose home has NOT staged F: it must fetch F from the
	// blockstore, verify the signature against the trusted signer, and stage it.
	home2 := stageBakeHome(t, a) // toolchain + inner + decision-gate, but NOT F
	store2 := t.TempDir()
	mgr2, err := NewFlowManager(flowconfig.FlowsConfig{Enabled: true, StoragePath: store2, MaxMemoryPages: 2048}, plugins.New(), HandlerMap{})
	if err != nil {
		t.Fatalf("NewFlowManager2: %v", err)
	}
	baker2, err := NewBaker(home2, 2048)
	if err != nil {
		t.Fatalf("NewBaker2: %v", err)
	}
	baker2.SetNetModules(NewNetModuleFetcher(bs, []ed25519.PublicKey{pub}))
	mgr2.SetBaker(baker2)

	// Publish F into baker2's catalog (verifies the signed bundle).
	entry, err := baker2.PublishNetworkModule(ctx, ref)
	if err != nil {
		t.Fatalf("PublishNetworkModule(F): %v", err)
	}
	if entry.PluginID != fmod.PluginID || len(entry.Methods) == 0 {
		t.Fatalf("published F entry unexpected: %+v", entry)
	}
	t.Logf("published F to catalog: pluginId=%s bundleHash=%s methods=%d (signed-only, trusted signer)", entry.PluginID, entry.BundleHash[:12], len(entry.Methods))

	// Bake G on the second node with the signed ref: F is fetched + verified +
	// staged at bake time, then linked and run.
	fAdv2, dAdv2 := bakeRunG(t, ctx, mgr2, []BakeModuleRef{ref})
	if !fAdv2 || !dAdv2 {
		t.Fatalf("fetch-to-bake recursion failed: fAdvanced=%v dAdvanced=%v", fAdv2, dAdv2)
	}
	t.Logf("★★★ FULL LOOP PROVEN: F published (content-addressed + Ed25519-signed) -> G baked via signed fetch-to-bake -> ran with F executing its inner flow")
}

// bakeRunG bakes + deploys G, drives its trigger once, and reports whether the
// F node (index 0) and the decision-gate node (index 1) advanced.
func bakeRunG(t *testing.T, ctx context.Context, mgr *FlowManager, refs []BakeModuleRef) (fAdvanced, dAdvanced bool) {
	t.Helper()
	req := BakeRequest{FlowJSON: json.RawMessage(subflowGFlowJSON), ModuleRefs: refs}
	_, programID, err := mgr.BakeAndDeploy(ctx, req)
	if err != nil {
		t.Fatalf("BakeAndDeploy(G): %v", err)
	}
	mgr.mu.Lock()
	fp := mgr.running[programID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("G did not load into a running FlowRuntime")
	}
	rt := fp.runtime
	if rt.NodeCount != 2 {
		t.Fatalf("G NodeCount=%d, want 2", rt.NodeCount)
	}
	before := invocationCounts(t, rt)
	rt.ResetState()
	rt.EnqueueTrigger(0)
	result, err := rt.Drain(ctx, HandlerMap{}, DrainOptions{MaxIterations: 256})
	if err != nil {
		t.Fatalf("Drain(G): %v", err)
	}
	after := invocationCounts(t, rt)
	t.Logf("G drain: iterations=%d nodesInvoked=%d  f: %d->%d  d: %d->%d",
		result.Iterations, result.NodesInvoked, before[0], after[0], before[1], after[1])
	return after[0] > before[0], after[1] > before[1]
}
