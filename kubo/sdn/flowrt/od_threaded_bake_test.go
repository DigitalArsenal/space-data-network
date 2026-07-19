//go:build linux

package flowrt

// od_threaded_bake_test.go — proves the RETARGETED bake path: a flow whose linked
// module is a wasi-threads guest-link bakes into a SHARED-MEMORY reactor
// runtime.wasm (C1 contract-guarded), loads with WithWASIThreads auto-enabled,
// and its guest std::thread workers SPAWN + RUN under the node's WasmEdge when the
// flow drains. This is the threaded analogue of TestMultiTUGuestLinkBake, closing
// the "baked flows can't thread" blocker before the real OD/provider guest-links
// land.
//
// Env (skips cleanly if unset):
//   SDN_LLVM_BOX_WASM, SDN_LLVM_SYSROOT, SDN_FLOWCC_BAKE_DIR  (v1 toolchain)
//   SDN_WASI_THREADS_SYSROOT      wasm32-wasip1-threads sysroot (+ grafted builtins)
//   SDN_FLOW_RUNTIME_THREADS_O    prebuilt native flow_runtime.threads.o
//   SDN_THREADTU_GL               dir with module-link.o + metadata.json(threadModel=wasi-threads) + plugin-manifest.json

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ipfs/kubo/sdn/flowcc"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

const threadTUPluginID = "org.sdn.demo.threadtu"

func TestThreadedGuestLinkBakeThreadsUnderWasmEdge(t *testing.T) {
	box := os.Getenv("SDN_LLVM_BOX_WASM")
	sysroot := os.Getenv("SDN_LLVM_SYSROOT")
	tpl := os.Getenv("SDN_FLOWCC_BAKE_DIR")
	wasiSysroot := os.Getenv("SDN_WASI_THREADS_SYSROOT")
	frtObj := os.Getenv("SDN_FLOW_RUNTIME_THREADS_O")
	glDir := os.Getenv("SDN_THREADTU_GL")
	if box == "" || sysroot == "" || tpl == "" || wasiSysroot == "" || frtObj == "" || glDir == "" {
		t.Skip("set SDN_LLVM_BOX_WASM + SDN_LLVM_SYSROOT + SDN_FLOWCC_BAKE_DIR + SDN_WASI_THREADS_SYSROOT + SDN_FLOW_RUNTIME_THREADS_O + SDN_THREADTU_GL")
	}

	// ── Build a staged home: v1 toolchain + the v2 wasi-threads bits ──────────
	home := flowcc.HomeAt(t.TempDir())
	if err := flowcc.StageToolchain(home, box, sysroot, tpl); err != nil {
		t.Fatalf("StageToolchain: %v", err)
	}
	// v2: symlink the (large) wasi-threads sysroot + copy the prebuilt runtime .o.
	if err := os.Symlink(wasiSysroot, home.SysrootWasiThreadsDir()); err != nil {
		t.Fatalf("symlink wasi sysroot: %v", err)
	}
	if b, err := os.ReadFile(frtObj); err != nil {
		t.Fatalf("read flow_runtime.threads.o: %v", err)
	} else if err := os.WriteFile(home.FlowRuntimeThreadsObjPath(), b, 0o644); err != nil {
		t.Fatalf("write flow_runtime.threads.o: %v", err)
	}
	if !home.ThreadsStaged() {
		t.Fatalf("home not ThreadsStaged after v2 staging")
	}
	if err := flowcc.StageModule(home, threadTUPluginID,
		filepath.Join(glDir, "module-link.o"),
		filepath.Join(glDir, "metadata.json"),
		filepath.Join(glDir, "plugin-manifest.json")); err != nil {
		t.Fatalf("StageModule threadtu: %v", err)
	}

	// ── Manager + baker (baker must have the wasi-threads compiler) ───────────
	cfg := flowconfig.FlowsConfig{Enabled: true, StoragePath: t.TempDir(), MaxMemoryPages: 4096}
	mgr, err := NewFlowManager(cfg, plugins.New(), HandlerMap{})
	if err != nil {
		t.Fatalf("NewFlowManager: %v", err)
	}
	baker, err := NewBaker(home, 4096)
	if err != nil {
		t.Fatalf("NewBaker: %v", err)
	}
	if baker.ccThreads == nil {
		t.Fatalf("baker has no wasi-threads compiler despite ThreadsStaged")
	}
	mgr.SetBaker(baker)

	// ── Flow: trigger -> threadtu.emit -> capture[host] ───────────────────────
	captureID := "org.sdn.probe.capture"
	spec := FlowSpec{
		ProgramID: "org.sdn.flows.threadtu-demo", Name: "Threaded Guest-Link Demo", Version: "0.1.0",
		Nodes: []FlowNodeSpec{
			{NodeID: "n0", PluginID: threadTUPluginID, MethodID: "emit", Kind: "transform"},
			{NodeID: "n1", PluginID: captureID, MethodID: "sink", Kind: "sink", DispatchModel: "host"},
		},
		Edges:           []FlowEdgeSpec{{EdgeID: "e0", FromNodeID: "n0", FromPortID: "out", ToNodeID: "n1", ToPortID: "in"}},
		Triggers:        []FlowTriggerSpec{{TriggerID: "t0", Kind: "timer", Source: "host-cron", DefaultIntervalMs: 3600000}},
		TriggerBindings: []FlowTriggerBindingSpec{{TriggerID: "t0", TargetNodeID: "n0", TargetPortID: "in"}},
	}

	ctx := context.Background()
	res, programID, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowPLG: BuildFlowPLG(spec)})
	if err != nil {
		t.Fatalf("★ BakeAndDeploy threaded flow FAILED: %v", err)
	}
	t.Logf("★ THREADED BAKE OK: programId=%s bytes=%d elapsed=%dms", programID, len(res.Wasm), res.Elapsed.Milliseconds())

	// C1 contract on the emitted artifact.
	feat := scanWasmThreadFeatures(res.Wasm)
	t.Logf("artifact thread contract: sharedImportedMem=%v threadSpawnImport=%v threadStartExport=%v emscriptenHook=%v",
		feat.SharedImportedMemory, feat.ThreadSpawnImport, feat.ThreadStartExport, feat.EmscriptenThreadHook)
	if !feat.isIsomorphicPthreads() {
		t.Fatalf("baked artifact does NOT declare the isomorphic wasi-threads contract")
	}

	mgr.mu.Lock()
	fp := mgr.running[programID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("threaded flow did not load into a running FlowRuntime")
	}
	rt := fp.runtime
	rt.SetLinkedSection(func(run func() error) error { return run() })

	var captured [][]byte
	handlers := HandlerMap{
		captureID + ":sink": func(_ context.Context, args *InvocationArgs) (*InvocationResult, error) {
			for _, f := range args.Frames {
				captured = append(captured, append([]byte(nil), f.Bytes...))
			}
			return &InvocationResult{StatusCode: 0}, nil
		},
	}

	before := invocationCounts(t, rt)
	rt.ResetState()
	rt.EnqueueTrigger(0)
	_, derr := rt.Drain(ctx, handlers, DrainOptions{MaxIterations: 256})
	after := invocationCounts(t, rt)

	n0Advanced := after[0] > before[0]
	t.Logf("drain: n0(emit) %d->%d  captured=%d frames  derr=%v", before[0], after[0], len(captured), derr)
	t.Logf("THREAD PROOF (live in the baked flow): spawn=%d peak=%d tids=%v",
		rt.ThreadSpawnCount(), rt.ThreadPeak(), rt.WorkerOSThreadIDs())

	if !n0Advanced {
		t.Fatalf("threaded emit node did NOT run")
	}
	if rt.ThreadPeak() < 2 {
		t.Fatalf("GATE FAIL: baked threaded flow ran only %d concurrent worker(s) under WasmEdge (no real parallelism)", rt.ThreadPeak())
	}
	if len(captured) == 0 || len(captured[0]) < 12 || string(captured[0][8:12]) != "$OMM" {
		t.Fatalf("threaded emit did not deliver a valid $OMM frame (captured=%d)", len(captured))
	}
	t.Logf("★★★ THREADED BAKE+RUN PROOF: baked wasi-threads flow spawned %d OS threads (peak %d) under WasmEdge, delivered a valid $OMM; single-thread bake path unchanged.",
		rt.ThreadSpawnCount(), rt.ThreadPeak())
}
