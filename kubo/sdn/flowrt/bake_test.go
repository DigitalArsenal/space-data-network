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
		if err := flowcc.StageModule(home, m.pluginID, obj, meta); err != nil {
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
