package flowrt

// bake_test.go is the Phase-0 make-or-break for the SDN Flow Platform: prove the
// node-side BAKE deploy path works end-to-end — POST a REAL flow.json + module
// refs to /api/v1/flows/bake, the node resolves the refs to STAGED guest-link
// objects, generates the per-flow descriptor, compiles + links a composed
// runtime.wasm with its OWN flowcc toolchain, installs it, and the installed
// flow RUNS (a node steps its FSM). It reuses flow_bake_test.go's proven inputs
// (the same three byte-identical module objects) and flow_run_test.go's FSM
// drive.
//
// Skips cleanly unless the toolchain assets are present (llvm-box.wasm, sysroot,
// the flow-runtime template) and the modules monorepo is checked out.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/flowcc"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

// scratchpad session dir (matches flowcc/flow_bake_test.go defaults).
const bakeScratch = "/private/tmp/claude-501/-Users-tj-software-spacedatanetwork-stack/8a9a46ba-3833-472b-bfb4-4c3869499342/scratchpad"

// bakeE2EFlowJSON is a real 3-node linked-direct flow: omm-json.encode ->
// decision-gate.dispatch -> clock.now, driven by one timer trigger bound to the
// first node. The pluginIds/methodIds match the staged modules' metadata.json.
const bakeE2EFlowJSON = `{
  "programId": "com.digitalarsenal.flows.bake-e2e",
  "name": "Bake E2E Flow",
  "version": "0.1.0",
  "nodes": [
    { "nodeId": "n0-ommjson", "pluginId": "com.digitalarsenal.foundation.omm-json",     "methodId": "encode",   "kind": "transform" },
    { "nodeId": "n1-gate",    "pluginId": "com.digitalarsenal.foundation.decision-gate", "methodId": "dispatch", "kind": "transform" },
    { "nodeId": "n2-clock",   "pluginId": "com.digitalarsenal.hostcap.clock",            "methodId": "now",      "kind": "transform" }
  ],
  "edges": [
    { "edgeId": "e0", "fromNodeId": "n0-ommjson", "fromPortId": "out", "toNodeId": "n1-gate",  "toPortId": "in" },
    { "edgeId": "e1", "fromNodeId": "n1-gate",    "fromPortId": "out", "toNodeId": "n2-clock", "toPortId": "in" }
  ],
  "triggers": [ { "triggerId": "t0", "kind": "timer", "source": "host-cron" } ],
  "triggerBindings": [ { "triggerId": "t0", "targetNodeId": "n0-ommjson", "targetPortId": "in" } ]
}`

// bakeModule is one staged module's identity + dist location relative to the
// modules monorepo root.
type bakeModule struct {
	pluginID string
	relDir   string // .../dist/guest-link relative to modules root
}

var bakeModules = []bakeModule{
	{"com.digitalarsenal.foundation.omm-json", filepath.Join("foundation", "omm-json", "dist", "guest-link")},
	{"com.digitalarsenal.foundation.decision-gate", filepath.Join("foundation", "decision-gate", "dist", "guest-link")},
	{"com.digitalarsenal.hostcap.clock", filepath.Join("hostcap", "clock", "dist", "guest-link")},
}

// bakeAssets are the resolved host paths the test needs.
type bakeAssets struct {
	box, sysroot, templateDir, modulesRoot string
}

func resolveBakeAssets(t *testing.T) bakeAssets {
	t.Helper()
	box := envOr("SDN_LLVM_BOX_WASM", filepath.Join(bakeScratch, "phase2", "llvm-box.wasm"))
	sysroot := envOr("SDN_LLVM_SYSROOT", filepath.Join(bakeScratch, "phase2", "sysroot"))
	tpl := envOr("SDN_FLOWCC_BAKE_DIR", filepath.Join(bakeScratch, "linkspike"))
	if _, err := os.Stat(box); err != nil {
		t.Skipf("llvm-box.wasm not available at %s (set SDN_LLVM_BOX_WASM): %v", box, err)
	}
	if fi, err := os.Stat(sysroot); err != nil || !fi.IsDir() {
		t.Skipf("sysroot not available at %s (set SDN_LLVM_SYSROOT): %v", sysroot, err)
	}
	if _, err := os.Stat(filepath.Join(tpl, "flow_runtime.cpp")); err != nil {
		t.Skipf("flow-runtime template not available at %s (set SDN_FLOWCC_BAKE_DIR): %v", tpl, err)
	}
	modulesRoot := resolveModulesRoot(t)
	return bakeAssets{box: box, sysroot: sysroot, templateDir: tpl, modulesRoot: modulesRoot}
}

func resolveModulesRoot(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("SDN_BAKE_MODULES_ROOT"); env != "" {
		return env
	}
	_, caller, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("runtime.Caller failed")
	}
	anchor := filepath.Dir(caller) // .../space-data-network/kubo/sdn/flowrt
	candidates := []string{
		filepath.Join(anchor, "..", "..", "..", "..", "space-data-network-modules"),
		filepath.Join(anchor, "..", "..", "..", "..", "..", "space-data-network-modules"),
		filepath.Join(anchor, "..", "..", "..", "..", "..", "..", "space-data-network-modules"),
	}
	for _, c := range candidates {
		cleaned := filepath.Clean(c)
		probe := filepath.Join(cleaned, bakeModules[0].relDir, "module-link.o")
		if _, err := os.Stat(probe); err == nil {
			return cleaned
		}
	}
	t.Skipf("space-data-network-modules not found (set SDN_BAKE_MODULES_ROOT); checked %v", candidates)
	return ""
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// stageBakeHome stages the toolchain + the three modules into a fresh home and
// returns it.
func stageBakeHome(t *testing.T, a bakeAssets) flowcc.Home {
	t.Helper()
	home := flowcc.HomeAt(t.TempDir())
	if err := flowcc.StageToolchain(home, a.box, a.sysroot, a.templateDir); err != nil {
		t.Fatalf("StageToolchain: %v", err)
	}
	for _, m := range bakeModules {
		obj := filepath.Join(a.modulesRoot, m.relDir, "module-link.o")
		meta := filepath.Join(a.modulesRoot, m.relDir, "metadata.json")
		// plugin-manifest.json is the sibling of the dist/guest-link dir; its
		// typed ports enrich the staged metadata (Phase 2). Missing is tolerated.
		manifest := filepath.Join(a.modulesRoot, m.relDir, "..", "plugin-manifest.json")
		if err := flowcc.StageModule(home, m.pluginID, obj, meta, manifest); err != nil {
			t.Fatalf("StageModule %s: %v", m.pluginID, err)
		}
	}
	if !home.Staged() {
		t.Fatalf("home reports not staged after StageToolchain: %s", home.Root())
	}
	return home
}

// TestBakeDeployEndToEnd is the Phase-0 proof. It POSTs the flow to the bake
// endpoint, confirms the node baked + installed a composed runtime.wasm, and
// confirms the installed flow RUNS (a node advances its InvocationCount).
func TestBakeDeployEndToEnd(t *testing.T) {
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

	mux := http.NewServeMux()
	RegisterAPI(mux, mgr)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Build the POST body: the full flow graph + explicit module refs (with
	// content-hash pins to exercise the fail-closed verify path).
	reqBody := map[string]interface{}{
		"flowJson": json.RawMessage(bakeE2EFlowJSON),
		"moduleRefs": []map[string]string{
			{"pluginId": "com.digitalarsenal.foundation.omm-json", "contentHash": sha256File(t, a, bakeModules[0])},
			{"pluginId": "com.digitalarsenal.foundation.decision-gate", "contentHash": sha256File(t, a, bakeModules[1])},
			{"pluginId": "com.digitalarsenal.hostcap.clock", "contentHash": sha256File(t, a, bakeModules[2])},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	resp, err := http.Post(srv.URL+"/api/v1/flows/bake", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("POST /bake: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		t.Fatalf("bake endpoint status=%d body=%s", resp.StatusCode, buf.String())
	}
	var out struct {
		Status         string   `json:"status"`
		ProgramID      string   `json:"programId"`
		Modules        []string `json:"modules"`
		WasmBytes      int      `json:"wasmBytes"`
		FlowRuntimeObj int      `json:"flowRuntimeObj"`
		CacheHit       bool     `json:"cacheHit"`
		BakeMillis     int64    `json:"bakeMillis"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode bake response: %v", err)
	}
	if out.Status != "baked" || out.ProgramID != "com.digitalarsenal.flows.bake-e2e" {
		t.Fatalf("unexpected bake response: %+v", out)
	}
	if out.WasmBytes < 8 || len(out.Modules) != 3 {
		t.Fatalf("bad bake result: wasm=%d modules=%v", out.WasmBytes, out.Modules)
	}
	t.Logf("★ POST /bake OK: programId=%s wasm=%d bytes flow_runtime.o=%d bytes modules=%v cacheHit=%v BAKE_WALLCLOCK=%dms",
		out.ProgramID, out.WasmBytes, out.FlowRuntimeObj, out.Modules, out.CacheHit, out.BakeMillis)

	// The composed runtime.wasm must be installed on disk.
	installed := filepath.Join(storeDir, "com.digitalarsenal.flows.bake-e2e", "runtime.wasm")
	fi, err := os.Stat(installed)
	if err != nil || fi.Size() < 8 {
		t.Fatalf("baked runtime.wasm not installed at %s: %v", installed, err)
	}
	t.Logf("installed baked artifact: %s (%d bytes)", installed, fi.Size())

	// The Deploy path loaded + registered the flow (with the modulert hostcall
	// bridge), so a live FlowRuntime exists for it.
	mgr.mu.Lock()
	fp := mgr.running[out.ProgramID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("baked flow did not load into a running FlowRuntime (deploy start failed)")
	}
	rt := fp.runtime
	if rt.NodeCount != 3 {
		t.Fatalf("baked runtime NodeCount=%d, want 3 (descriptor tables not baked in)", rt.NodeCount)
	}
	t.Logf("baked runtime ABI: %d nodes, %d edges, %d triggers, %d deps",
		rt.NodeCount, rt.EdgeCount, rt.TriggerCount, rt.DepCount)

	// Drive the FSM: reset, fire the timer trigger, drain. A node's
	// InvocationCount advancing proves a real linked-direct module entry fired
	// inside the NODE-BAKED artifact under WasmEdge.
	before := invocationCounts(t, rt)
	rt.ResetState()
	rt.EnqueueTrigger(0)
	result, err := rt.Drain(context.Background(), HandlerMap{}, DrainOptions{MaxIterations: 256})
	if err != nil {
		t.Fatalf("Drain baked flow: %v", err)
	}
	after := invocationCounts(t, rt)
	advanced := 0
	for i := range after {
		if after[i] > before[i] {
			advanced++
			t.Logf("node[%d] InvocationCount %d -> %d", i, before[i], after[i])
		}
	}
	t.Logf("drain: iterations=%d nodesInvoked=%d handlersSkipped=%d nodesAdvanced=%d",
		result.Iterations, result.NodesInvoked, result.HandlersSkipped, advanced)
	if advanced == 0 {
		t.Fatalf("no node advanced: the node-baked flow did not step a real FSM node")
	}
	t.Logf("★★ NODE-BAKED flow POSTed, installed, and RAN: %d node(s) stepped", advanced)

	// Measure the cache-hit re-bake (Task 3): a re-bake of the same flow serves
	// flow_runtime.o from cache, so only the (fast) link runs.
	res2, err := baker.Bake(context.Background(), BakeRequest{FlowJSON: json.RawMessage(bakeE2EFlowJSON)})
	if err != nil {
		t.Fatalf("re-bake: %v", err)
	}
	if !res2.CacheHit {
		t.Logf("note: second bake did not hit the flow_runtime.o cache (expected hit)")
	}
	t.Logf("re-bake (cacheHit=%v) wall-clock: %dms (vs first %dms)",
		res2.CacheHit, res2.Elapsed.Milliseconds(), out.BakeMillis)
}

// bakeNewFlowJSON is a STRUCTURALLY DIFFERENT flow from bakeE2EFlowJSON: a
// 2-node linked-direct flow (omm-json.encode -> clock.now), different node/edge
// counts, different programId. It shares NO per-flow compile input with flow A,
// so a link-only bake must reuse the flow-agnostic flow_runtime.o (CacheHit)
// rather than recompiling the 867-line runtime.
const bakeNewFlowJSON = `{
  "programId": "com.digitalarsenal.flows.bake-new",
  "name": "Bake New Flow",
  "version": "0.1.0",
  "nodes": [
    { "nodeId": "a-ommjson", "pluginId": "com.digitalarsenal.foundation.omm-json", "methodId": "encode", "kind": "transform" },
    { "nodeId": "b-clock",   "pluginId": "com.digitalarsenal.hostcap.clock",       "methodId": "now",    "kind": "transform" }
  ],
  "edges": [
    { "edgeId": "e0", "fromNodeId": "a-ommjson", "fromPortId": "out", "toNodeId": "b-clock", "toPortId": "in" }
  ],
  "triggers": [ { "triggerId": "t0", "kind": "timer", "source": "host-cron" } ],
  "triggerBindings": [ { "triggerId": "t0", "targetNodeId": "a-ommjson", "targetPortId": "in" } ]
}`

// TestBakeNewFlowLinkOnly is the Phase-0b proof: once the flow-agnostic
// flow_runtime.o is warm, baking a BRAND-NEW flow is LINK-ONLY (no 34s runtime
// recompile) and the result runs identically to the current bake_test flow.
func TestBakeNewFlowLinkOnly(t *testing.T) {
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

	// Prewarm the flow-agnostic runtime (pays the one-time ~34s compile off the
	// interactive path — this is what a node would do at boot).
	warmStart := time.Now()
	warmCached, err := baker.PrewarmRuntime(ctx)
	if err != nil {
		t.Fatalf("PrewarmRuntime: %v", err)
	}
	t.Logf("PrewarmRuntime: cachedAlready=%v elapsed=%dms", warmCached, time.Since(warmStart).Milliseconds())

	// Bake+deploy flow A (3-node). With a warm runtime this is already link-only.
	resA, _, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowJSON: json.RawMessage(bakeE2EFlowJSON)})
	if err != nil {
		t.Fatalf("BakeAndDeploy flow A: %v", err)
	}
	if !resA.CacheHit {
		t.Errorf("flow A bake recompiled the runtime (CacheHit=false) after prewarm — runtime not flow-agnostic")
	}
	t.Logf("flow A (3-node) bake: cacheHit=%v elapsed=%dms", resA.CacheHit, resA.Elapsed.Milliseconds())

	// Bake+deploy the STRUCTURALLY DIFFERENT flow B (2-node). THE PROOF: a brand-
	// new flow must be link-only (CacheHit) and fast.
	resB, programB, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowJSON: json.RawMessage(bakeNewFlowJSON)})
	if err != nil {
		t.Fatalf("BakeAndDeploy flow B: %v", err)
	}
	if !resB.CacheHit {
		t.Fatalf("NEW flow B recompiled the runtime (CacheHit=false) — link-only refactor failed")
	}
	if programB != "com.digitalarsenal.flows.bake-new" {
		t.Fatalf("flow B programId=%q", programB)
	}
	t.Logf("★ NEW flow B (2-node) bake: LINK-ONLY cacheHit=%v NEW_FLOW_BAKE=%dms", resB.CacheHit, resB.Elapsed.Milliseconds())

	// Flow B loaded into a running FlowRuntime with the correct, per-flow ABI
	// counts (proves the descriptor — not the shared runtime — carries the graph).
	mgr.mu.Lock()
	fp := mgr.running[programB]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("flow B did not load into a running FlowRuntime")
	}
	rt := fp.runtime
	if rt.NodeCount != 2 || rt.EdgeCount != 1 {
		t.Fatalf("flow B ABI mismatch: NodeCount=%d EdgeCount=%d, want 2/1 (descriptor not baked in)", rt.NodeCount, rt.EdgeCount)
	}
	t.Logf("flow B ABI: %d nodes, %d edges, %d triggers, %d deps", rt.NodeCount, rt.EdgeCount, rt.TriggerCount, rt.DepCount)

	// Drive flow B's FSM: a node advancing proves the link-only artifact runs a
	// real linked-direct entry under WasmEdge — identical behavior to flow A.
	before := invocationCounts(t, rt)
	rt.ResetState()
	rt.EnqueueTrigger(0)
	result, err := rt.Drain(ctx, HandlerMap{}, DrainOptions{MaxIterations: 256})
	if err != nil {
		t.Fatalf("Drain flow B: %v", err)
	}
	after := invocationCounts(t, rt)
	advanced := 0
	for i := range after {
		if after[i] > before[i] {
			advanced++
		}
	}
	t.Logf("flow B drain: iterations=%d nodesInvoked=%d nodesAdvanced=%d", result.Iterations, result.NodesInvoked, advanced)
	if advanced == 0 {
		t.Fatalf("no node advanced: the NEW link-only flow did not step a real FSM node")
	}
	t.Logf("★★ NEW flow baked LINK-ONLY, installed, and RAN: %d node(s) stepped", advanced)
}

// bakeTwoInputGateFlowJSON is a single-node flow whose node is bound to
// decision-gate.branch — a method that declares TWO typed input ports (decision,
// stream) in its plugin-manifest.json (Phase-2 staged). Two triggers each deliver
// a frame to ONE of those ports, so the node's required-ports set (typed inputs ∩
// wired ports) is {decision, stream}. It exists to prove a multi-input node fires
// only once BOTH required inputs have a queued frame.
const bakeTwoInputGateFlowJSON = `{
  "programId": "com.digitalarsenal.flows.two-input-gate",
  "name": "Two-Input Required-Ports Gate",
  "version": "0.1.0",
  "nodes": [
    { "nodeId": "gate", "pluginId": "com.digitalarsenal.foundation.decision-gate", "methodId": "branch", "kind": "transform" }
  ],
  "edges": [],
  "triggers": [
    { "triggerId": "t-decision", "kind": "manual", "source": "test" },
    { "triggerId": "t-stream",   "kind": "manual", "source": "test" }
  ],
  "triggerBindings": [
    { "triggerId": "t-decision", "targetNodeId": "gate", "targetPortId": "decision" },
    { "triggerId": "t-stream",   "targetNodeId": "gate", "targetPortId": "stream" }
  ]
}`

// TestBakeRequiredPortsTwoInputGate is the required-ports correctness proof: a
// node bound to a 2-typed-input method (decision-gate.branch: decision + stream),
// with both ports wired, must NOT become ready on one input and MUST become ready
// once BOTH inputs have arrived. This exercises the fix that replaces bake.go's
// hard-coded required_port_count=0 (which made every node fire on any single
// queued frame) with real per-node required-port rows the in-wasm scheduler's
// flow_node_is_ready gates on.
func TestBakeRequiredPortsTwoInputGate(t *testing.T) {
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

	_, programID, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowJSON: json.RawMessage(bakeTwoInputGateFlowJSON)})
	if err != nil {
		t.Fatalf("BakeAndDeploy two-input gate: %v", err)
	}
	mgr.mu.Lock()
	fp := mgr.running[programID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("two-input gate flow did not load into a running FlowRuntime")
	}
	rt := fp.runtime
	if rt.NodeCount != 1 || rt.TriggerCount != 2 {
		t.Fatalf("gate ABI mismatch: NodeCount=%d TriggerCount=%d, want 1/2", rt.NodeCount, rt.TriggerCount)
	}

	gateReady := func() bool {
		st, err := rt.GetNodeRuntimeState(0)
		if err != nil {
			t.Fatalf("GetNodeRuntimeState(0): %v", err)
		}
		return st.Ready
	}
	gateInvocations := func() uint64 {
		st, err := rt.GetNodeRuntimeState(0)
		if err != nil {
			t.Fatalf("GetNodeRuntimeState(0): %v", err)
		}
		return st.InvocationCount
	}

	rt.ResetState()

	// Deliver ONLY the "decision" input (trigger 0). The gate has TWO required
	// ports, so one input must NOT make it ready — the old required_port_count=0
	// behavior would (incorrectly) mark it ready here.
	rt.EnqueueTrigger(0)
	if gateReady() {
		t.Fatalf("gate became ready with only the \"decision\" input — required-ports gate not applied (fires on any single frame)")
	}
	// A not-ready node is never invoked, even after a full drain.
	if _, err := rt.Drain(ctx, HandlerMap{}, DrainOptions{MaxIterations: 16}); err != nil {
		t.Fatalf("Drain (one input): %v", err)
	}
	if got := gateInvocations(); got != 0 {
		t.Fatalf("gate fired with only one of two required inputs (InvocationCount=%d)", got)
	}
	if gateReady() {
		t.Fatalf("gate ready after draining with one input; must wait for both required ports")
	}
	t.Logf("with 1/2 required inputs: gate ready=false, invocations=0 (correctly waiting)")

	// Now deliver the second required input ("stream", trigger 1). BOTH required
	// ports now have a queued frame, so the gate becomes ready.
	rt.EnqueueTrigger(1)
	if !gateReady() {
		t.Fatalf("gate NOT ready after BOTH required inputs (decision+stream) arrived — required-ports gate over-restricts")
	}
	t.Logf("★ with 2/2 required inputs: gate ready=true — a 2-input node fires only after BOTH inputs arrive")

	// Draining now fires the gate (a real linked-direct entry): proves the ready
	// gate actually opens end-to-end.
	if _, err := rt.Drain(ctx, HandlerMap{}, DrainOptions{MaxIterations: 64}); err != nil {
		t.Fatalf("Drain (both inputs): %v", err)
	}
	if got := gateInvocations(); got == 0 {
		t.Fatalf("gate did not fire after both required inputs were present")
	}
	t.Logf("★★ gate fired once both required inputs were present (InvocationCount=%d)", gateInvocations())
}

// TestBakeNewFlowLinkOnlyAOT measures the Part-1+2 end state (link-only bake on
// an AOT-compiled box). It is OPT-IN (SDN_FLOWCC_AOT_BAKE=1) because AOT-
// compiling the 58 MB box is a multi-minute one-shot; the default suite skips it.
func TestBakeNewFlowLinkOnlyAOT(t *testing.T) {
	if os.Getenv("SDN_FLOWCC_AOT_BAKE") == "" {
		t.Skip("set SDN_FLOWCC_AOT_BAKE=1 to measure the AOT link-only bake (multi-minute box AOT compile)")
	}
	a := resolveBakeAssets(t)
	home := stageBakeHome(t, a)
	ctx := context.Background()

	// One-time: AOT-compile the box into the home so NewBaker's compiler runs
	// clang/wasm-ld native.
	ta := time.Now()
	if _, err := flowcc.CompileBoxAOT(home); err != nil {
		t.Skipf("CompileBoxAOT failed: %v", err)
	}
	t.Logf("AOT box build (one-time): %dms", time.Since(ta).Milliseconds())

	baker, err := NewBaker(home, 2048)
	if err != nil {
		t.Fatalf("NewBaker: %v", err)
	}

	tw := time.Now()
	if _, err := baker.PrewarmRuntime(ctx); err != nil {
		t.Fatalf("PrewarmRuntime: %v", err)
	}
	t.Logf("PrewarmRuntime (AOT box): %dms", time.Since(tw).Milliseconds())

	resA, err := baker.Bake(ctx, BakeRequest{FlowJSON: json.RawMessage(bakeE2EFlowJSON)})
	if err != nil {
		t.Fatalf("bake A: %v", err)
	}
	resB, err := baker.Bake(ctx, BakeRequest{FlowJSON: json.RawMessage(bakeNewFlowJSON)})
	if err != nil {
		t.Fatalf("bake B: %v", err)
	}
	if !resB.CacheHit {
		t.Fatalf("NEW flow B not link-only (CacheHit=false)")
	}
	t.Logf("★ AOT link-only bakes: flowA(3-node)=%dms flowB_NEW(2-node)=%dms (cacheHit=%v)",
		resA.Elapsed.Milliseconds(), resB.Elapsed.Milliseconds(), resB.CacheHit)
}

func sha256File(t *testing.T, a bakeAssets, m bakeModule) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.modulesRoot, m.relDir, "module-link.o"))
	if err != nil {
		t.Fatalf("read module object %s: %v", m.pluginID, err)
	}
	return sha256Hex(b)
}

func invocationCounts(t *testing.T, rt *FlowRuntime) []uint64 {
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
