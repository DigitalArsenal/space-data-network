//go:build linux

package flowrt

// od_supplemental_bake_test.go — bakes the AUTHORED supplemental-OMM $PLG
// (ODSupplementalOMMSpec) from the SHIPPED wasi-threads guest-links: 5 providers
// + threaded OD + the wasi-threads store node. It PROVES the real composition
// bakes into a shared-memory reactor that threads under WasmEdge (C1) and that the
// in-memory-only invariant holds structurally (no $OEM edge to the store). The
// live full-catalog drain (fetch + fit + store + record verification) is the
// separate local full-stack verify + prod run.
//
// SKIPS cleanly until the wasi-threads store guest-link is staged (its
// threadModel must be "wasi-threads" or the dual-path gate hard-errors on the
// emscripten store). Ready to run the moment the storage-ingest recompile lands.
//
// Env: the v2 toolchain (SDN_LLVM_BOX_WASM, SDN_LLVM_SYSROOT, SDN_FLOWCC_BAKE_DIR,
// SDN_WASI_THREADS_SYSROOT, SDN_FLOW_RUNTIME_THREADS_O) + SDN_ODSUP_MODULES_ROOT
// (space-data-network-modules with the staged guest-links).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ipfs/kubo/sdn/flowcc"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

type odSupModule struct{ pluginID, relDir string }

var odSupModules = []odSupModule{
	{"com.orbpro.spacex-starlink-source", "data-source/spacex-starlink-source"},
	{"com.orbpro.glonass-source", "data-source/glonass-source"},
	{"com.orbpro.intelsat-source", "data-source/intelsat-source"},
	{"com.orbpro.cpf-source", "data-source/cpf-source"},
	{"com.orbpro.iss-source", "data-source/iss-source"},
	{ODSupplementalODPluginID, "analysis/od"},
	{ODSupplementalStorePluginID, "hostcap/flatsql-store"},
}

func TestODSupplementalOMMBakes(t *testing.T) {
	box := os.Getenv("SDN_LLVM_BOX_WASM")
	sysroot := os.Getenv("SDN_LLVM_SYSROOT")
	tpl := os.Getenv("SDN_FLOWCC_BAKE_DIR")
	wasiSysroot := os.Getenv("SDN_WASI_THREADS_SYSROOT")
	frtObj := os.Getenv("SDN_FLOW_RUNTIME_THREADS_O")
	modsRoot := os.Getenv("SDN_ODSUP_MODULES_ROOT")
	if box == "" || sysroot == "" || tpl == "" || wasiSysroot == "" || frtObj == "" || modsRoot == "" {
		t.Skip("set the v2 toolchain env + SDN_ODSUP_MODULES_ROOT to bake the supplemental-OMM flow")
	}

	home := flowcc.HomeAt(t.TempDir())
	if err := flowcc.StageToolchain(home, box, sysroot, tpl); err != nil {
		t.Fatalf("StageToolchain: %v", err)
	}
	if err := os.Symlink(wasiSysroot, home.SysrootWasiThreadsDir()); err != nil {
		t.Fatalf("symlink wasi sysroot: %v", err)
	}
	if b, err := os.ReadFile(frtObj); err != nil {
		t.Fatalf("read flow_runtime.threads.o: %v", err)
	} else if err := os.WriteFile(home.FlowRuntimeThreadsObjPath(), b, 0o644); err != nil {
		t.Fatalf("write flow_runtime.threads.o: %v", err)
	}

	for _, m := range odSupModules {
		gl := filepath.Join(modsRoot, m.relDir, "dist", "guest-link")
		obj := filepath.Join(gl, "module-link.o")
		meta := filepath.Join(gl, "metadata.json")
		manifest := filepath.Join(modsRoot, m.relDir, "dist", "plugin-manifest.json")
		if _, err := os.Stat(manifest); err != nil {
			manifest = filepath.Join(gl, "plugin-manifest.json")
		}
		if _, err := os.Stat(obj); err != nil {
			t.Skipf("guest-link not staged for %s at %s — waiting on the module node", m.pluginID, obj)
		}
		// Gate: the store node MUST be wasi-threads or the dual-path gate rejects.
		if tm, _ := os.ReadFile(meta); m.pluginID == ODSupplementalStorePluginID && !containsWasiThreads(tm) {
			t.Skipf("store node %s is not yet threadModel=wasi-threads (recompile pending) — %s", m.pluginID, meta)
		}
		if err := flowcc.StageModule(home, m.pluginID, obj, meta, manifest); err != nil {
			t.Fatalf("StageModule %s: %v", m.pluginID, err)
		}
	}

	// In-memory-only invariant (structural): $OEM is consumed ONLY by od.fit.oem.
	spec := ODSupplementalOMMSpec()
	consumers := odSupplementalOEMConsumers(spec)
	if len(consumers) != len(odSupplementalProviders) {
		t.Fatalf("expected %d provider $OEM edges, got %v", len(odSupplementalProviders), consumers)
	}
	for _, c := range consumers {
		if c != "n-od.oem" {
			t.Fatalf("in-memory-only invariant VIOLATED: $OEM consumed by %q (must be only n-od.oem)", c)
		}
	}

	cfg := flowconfig.FlowsConfig{Enabled: true, StoragePath: t.TempDir(), MaxMemoryPages: 8192}
	mgr, err := NewFlowManager(cfg, plugins.New(), HandlerMap{})
	if err != nil {
		t.Fatalf("NewFlowManager: %v", err)
	}
	baker, err := NewBaker(home, 8192)
	if err != nil {
		t.Fatalf("NewBaker: %v", err)
	}
	mgr.SetBaker(baker)

	res, programID, err := mgr.BakeAndDeploy(context.Background(), BakeRequest{FlowPLG: BuildFlowPLG(spec)})
	if err != nil {
		t.Fatalf("★ BakeAndDeploy supplemental-OMM flow FAILED: %v", err)
	}
	feat := scanWasmThreadFeatures(res.Wasm)
	if !feat.isIsomorphicPthreads() {
		t.Fatalf("baked supplemental-OMM artifact does NOT declare the wasi-threads contract")
	}
	// Positive store invariant: the composed artifact is ENGINE-LINKED (imports
	// module "flatsql" — the in-wasm FlatSQL store), NOT the Go storage sink.
	if !wasmImportsModule(res.Wasm, engineImportModule) {
		t.Fatalf("composed artifact does NOT import module %q — the in-wasm FlatSQL store is not linked", engineImportModule)
	}
	if wasmImportsModule(res.Wasm, "storage") {
		t.Fatalf("composed artifact imports a 'storage' module — the repudiated Go storage sink must not be present")
	}
	mgr.mu.Lock()
	fp := mgr.running[programID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("supplemental-OMM flow did not load into a running FlowRuntime")
	}
	t.Logf("★★★ SUPPLEMENTAL-OMM $PLG BAKED: %d nodes, %d bytes, wasi-threads contract OK, WithWASIThreads-loaded; $OEM consumed only by od.fit (in-memory). Live full-catalog fetch+fit+store is the local full-stack verify.",
		fp.runtime.NodeCount, len(res.Wasm))
}

func containsWasiThreads(metaJSON []byte) bool {
	return len(metaJSON) > 0 && (bytesContains(metaJSON, []byte(`"wasi-threads"`)))
}

func bytesContains(h, n []byte) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if string(h[i:i+len(n)]) == string(n) {
			return true
		}
	}
	return false
}
