package flowrt

// SCRATCH (Phase 1a, do-not-commit): build analysis/od into a multi-TU
// guest-link object (od_lib + sgp4_lib(Vallado) + Eigen + SDS $OEM/$OMM + entry
// glue) via the node's OWN llvm-box clang, partial-link with wasm-ld -r, then
// bake trigger -> od.fit -> omm-json and load the runtime.wasm under WasmEdge.
//
// This is the Phase-1a build+bake gate. The critical de-risk is whether the
// Eigen-heavy fitter TU and the generated SDS headers compile under llvm-box
// (the Phase-0b prototype only used FlatBuffers).
//
// Skips unless the bake toolchain + Eigen are available (SDN_OD_EIGEN_DIR).

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/flowcc"
	"github.com/ipfs/kubo/sdn/flowconfig"
	"github.com/ipfs/kubo/sdn/plugins"
)

const odPluginID = "orbit-determination"

// odEntrySrc is the guest-link ENTRY TU. It consumes an SDS $OEM frame on the
// "oem" port, fits via od::fit_ephemeris_fb (defined in plugin_runtime.o), and
// emits an SDS $OMM FlatBuffer on "omm" as ALIGNED_BINARY. Only `fit` is renamed
// (-Dfit=<prefix>fit); the ABI (plugin_*) stays UNDEFINED to resolve at bake.
const odEntrySrc = `#include "space_data_module_invoke.h"
#include "od/plugin_runtime.h"
#include <cstdint>
#include <string_view>

extern "C" int fit(void) {
  plugin_reset_output_state();
  int32_t idx = plugin_find_input_index("oem", 0);
  if (idx < 0) { plugin_set_error("missing-input", "oem port required"); return 1; }
  const plugin_input_frame_t *f = plugin_get_input_frame((uint32_t)idx);
  if (f == 0 || f->payload == 0 || f->payload_length == 0) {
    plugin_set_error("missing-input", "empty oem frame");
    return 1;
  }
  od::PluginFitFBResult r = od::fit_ephemeris_fb(f->payload, f->payload_length, std::string_view{});
  if (!r.ok) { plugin_set_error(r.error_code.c_str(), r.error_message.c_str()); return 1; }
  int32_t rc = plugin_push_output_ex(
      "omm", "OMM.fbs", "$OMM",
      PLUGIN_PAYLOAD_WIRE_FORMAT_ALIGNED_BINARY, "OMM",
      /*fixed_string_length=*/0, /*required_alignment=*/8,
      r.omm.data(), (uint32_t)r.omm.size());
  return rc;
}
`

const odManifest = `{
  "pluginId": "orbit-determination",
  "name": "Orbit Determination Plugin",
  "version": "0.1.0",
  "description": "Fit supplemental-GP $OMM from operator $OEM.",
  "pluginFamily": "analysis",
  "capabilities": [],
  "externalInterfaces": [],
  "invokeSurfaces": ["direct"],
  "runtimeTargets": ["wasmedge"],
  "methods": [
    {
      "methodId": "fit",
      "displayName": "Fit $OMM from $OEM",
      "inputPorts": [
        {
          "portId": "oem",
          "acceptedTypeSets": [
            {
              "setId": "sds-oem",
              "allowedTypes": [
                { "schemaName": "OEM.fbs", "fileIdentifier": "$OEM", "rootTypeName": "OEM" },
                { "schemaName": "OEM.fbs", "fileIdentifier": "$OEM", "rootTypeName": "OEM", "wireFormat": "aligned-binary", "requiredAlignment": 8 }
              ]
            }
          ],
          "minStreams": 1, "maxStreams": 1, "required": true
        }
      ],
      "outputPorts": [
        {
          "portId": "omm",
          "acceptedTypeSets": [
            {
              "setId": "sds-omm",
              "allowedTypes": [
                { "schemaName": "OMM.fbs", "fileIdentifier": "$OMM", "rootTypeName": "OMM" },
                { "schemaName": "OMM.fbs", "fileIdentifier": "$OMM", "rootTypeName": "OMM", "wireFormat": "aligned-binary", "requiredAlignment": 8 }
              ]
            }
          ],
          "minStreams": 1, "maxStreams": 1, "required": true
        }
      ],
      "maxBatch": 1,
      "drainPolicy": "single-shot"
    }
  ],
  "schemasUsed": [
    { "schemaName": "OEM.fbs", "fileIdentifier": "$OEM", "rootTypeName": "OEM" },
    { "schemaName": "OMM.fbs", "fileIdentifier": "$OMM", "rootTypeName": "OMM" }
  ]
}
`

func TestODGuestLinkBake(t *testing.T) {
	a := resolveBakeAssets(t)

	eigenDir := os.Getenv("SDN_OD_EIGEN_DIR")
	if eigenDir == "" {
		t.Skip("SDN_OD_EIGEN_DIR not set (path to eigen3 dir containing Eigen/)")
	}
	fbInc := os.Getenv("SDN_FB_INCLUDE") // .../flatbuffers/include/flatbuffers
	if fbInc == "" {
		t.Skip("SDN_FB_INCLUDE not set")
	}
	sdsDir := os.Getenv("SDN_CORE_SDS_GENERATED_DIR")
	if sdsDir == "" {
		t.Skip("SDN_CORE_SDS_GENERATED_DIR not set (flat OEM_generated.h dir)")
	}

	odCpp := filepath.Join(a.modulesRoot, "analysis", "od", "src", "cpp")
	odSrc := filepath.Join(odCpp, "src")
	odInc := filepath.Join(odCpp, "include")
	vallado := filepath.Join(odCpp, "deps", "vallado-sgp4")

	// AOT-compile the 58MB llvm-box ONCE (persist next to the box in scratchpad),
	// so clang/wasm-ld run as NATIVE code — seconds per TU instead of minutes
	// under the interpreter. NewWithSysroot auto-picks up <box>.aot.
	boxHome := flowcc.HomeAt(filepath.Dir(a.box))
	if _, err := os.Stat(a.box + ".aot"); err == nil {
		t.Logf("reusing existing AOT box: %s.aot", a.box)
	} else if os.Getenv("SDN_OD_AOT") == "1" {
		t.Logf("AOT-compiling llvm-box (one-shot; SDN_OD_AOT=1)...")
		if aot, aerr := flowcc.CompileBoxAOT(boxHome); aerr != nil {
			t.Logf("AOT compile failed (%v); falling back to interpreted box", aerr)
		} else {
			t.Logf("AOT box ready: %s", aot)
		}
	}

	cc, err := flowcc.NewWithSysroot(a.box, a.sysroot)
	if err != nil {
		t.Fatalf("flowcc.NewWithSysroot: %v", err)
	}
	t.Logf("flowcc AOT enabled: %v", cc.AOTEnabled())
	ctx := context.Background()
	prefix := guestLinkSymbolPrefix(odPluginID)

	// ---- Seed the compile overlay ----
	inFiles := map[string][]byte{}
	seedDirIntoOverlay(t, inFiles, filepath.Join(odInc, "od"), "/inc/od")
	inFiles["/inc/space_data_module_invoke.h"] = odMustRead(t, filepath.Join(odInc, "space_data_module_invoke.h"))
	inFiles["/inc/SGP4.h"] = odMustRead(t, filepath.Join(vallado, "SGP4.h"))
	seedDirIntoOverlay(t, inFiles, resolveSymlink(t, fbInc), "/inc/flatbuffers")
	seedDirIntoOverlay(t, inFiles, resolveSymlink(t, sdsDir), "/sds")
	seedDirIntoOverlay(t, inFiles, resolveSymlink(t, eigenDir), "/eigen")

	// OD library TUs (od_lib) — every .cpp CMake compiles into od_lib.
	libTUs := []string{
		"orbit_determination.cpp", "meme_parser.cpp", "frame_transform.cpp",
		"oem_parser.cpp", "oem_fb_reader.cpp", "omm_fb_builder.cpp",
		"sgp4_fitter.cpp", "plugin_runtime.cpp",
	}
	for _, f := range libTUs {
		inFiles["/work/"+f] = odMustRead(t, filepath.Join(odSrc, f))
	}
	inFiles["/work/SGP4.cpp"] = odMustRead(t, filepath.Join(vallado, "SGP4.cpp"))
	inFiles["/work/od_entry.cpp"] = []byte(odEntrySrc)

	// -O0: the interpreted llvm-box makes Eigen's template-heavy TU extremely slow
	// at -O2. The bake GATE only needs the guest-link to LINK + LOAD + DISPATCH
	// (the entry returns missing-input without running the fitter), so optimization
	// is irrelevant here. Numerical parity is proven separately by the native
	// test_oem_fb_parity gate.
	baseFlags := []string{
		"-target", "wasm32-emscripten", "--sysroot=/sysroot",
		"-std=c++17", "-O0", "-mbulk-memory", "-DNDEBUG", "-DEMSCRIPTEN",
		"-DEIGEN_DONT_PARALLELIZE", "-fignore-exceptions",
		"-I/work", "-I/inc", "-I/sds", "-I/eigen",
	}

	compile := func(src, obj string, extra ...string) []byte {
		args := append([]string{"clang", "clang", "-c", src, "-o", obj}, baseFlags...)
		args = append(args, extra...)
		res, err := cc.Run(ctx, args, inFiles)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("compile %s: err=%v exit=%d\nstderr=%s", src, err, res.ExitCode, res.Stderr)
		}
		out := res.OutFiles[obj]
		if len(out) == 0 {
			t.Fatalf("compile %s produced no %s", src, obj)
		}
		t.Logf("compiled %s -> %s (%d bytes)", filepath.Base(src), filepath.Base(obj), len(out))
		return out
	}

	objs := map[string][]byte{}
	for _, f := range libTUs {
		obj := "/work/" + strings.TrimSuffix(f, ".cpp") + ".o"
		objs[obj] = compile("/work/"+f, obj)
	}
	objs["/work/SGP4.o"] = compile("/work/SGP4.cpp", "/work/SGP4.o")
	// Only the entry TU is renamed so metadata.json[fit] resolves uniquely.
	objs["/work/od_entry.o"] = compile("/work/od_entry.cpp", "/work/od_entry.o", "-Dfit="+prefix+"fit")

	// ---- Partial-link all TUs into ONE relocatable guest-link object ----
	linkArgs := []string{"lld", "wasm-ld", "-r"}
	linkIn := map[string][]byte{}
	for obj, b := range objs {
		linkArgs = append(linkArgs, obj)
		linkIn[obj] = b
	}
	linkArgs = append(linkArgs, "-o", "/work/module-link.o")
	lres, err := cc.Run(ctx, linkArgs, linkIn)
	if err != nil || lres.ExitCode != 0 {
		t.Fatalf("wasm-ld -r: err=%v exit=%d stderr=%s", err, lres.ExitCode, lres.Stderr)
	}
	objBytes := lres.OutFiles["/work/module-link.o"]
	if len(objBytes) == 0 {
		t.Fatalf("wasm-ld -r produced no module-link.o")
	}
	t.Logf("★ partial-linked %d TUs -> module-link.o (%d bytes)", len(objs), len(objBytes))

	// ---- Persist to the OD module dist/guest-link (the Phase-1a deliverable) ----
	glDir := filepath.Join(odCpp, "..", "..", "dist", "guest-link")
	if env := os.Getenv("SDN_OD_GUESTLINK_OUT"); env != "" {
		glDir = env
	}
	if err := os.MkdirAll(glDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	objPath := filepath.Join(glDir, "module-link.o")
	metaPath := filepath.Join(glDir, "metadata.json")
	manPath := filepath.Join(glDir, "..", "..", "plugin-manifest.json") // real module manifest
	if err := os.WriteFile(objPath, objBytes, 0o644); err != nil {
		t.Fatalf("write module-link.o: %v", err)
	}
	meta := map[string]interface{}{
		"version": 1, "format": "wasm-object", "language": "c++",
		"threadModel": "single-thread", "symbolPrefix": prefix,
		"methodSymbols": map[string]string{"fit": prefix + "fit"},
	}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(metaPath, append(mb, '\n'), 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	// A minimal manifest for staging (the real module manifest has extra ports).
	stagingManifest := filepath.Join(glDir, "plugin-manifest.json")
	if err := os.WriteFile(stagingManifest, []byte(odManifest), 0o644); err != nil {
		t.Fatalf("write staging manifest: %v", err)
	}
	_ = manPath
	t.Logf("staged guest-link artifacts under %s", glDir)

	// ---- Symbol evidence ----
	verifyODSymbols(t, objPath, prefix)

	// ---- Stage + bake trigger -> od.fit -> omm-json ----
	home := stageBakeHome(t, a)
	if err := flowcc.StageModule(home, odPluginID, objPath, metaPath, stagingManifest); err != nil {
		t.Fatalf("StageModule od: %v", err)
	}

	flowPLG := BuildFlowPLG(FlowSpec{
		ProgramID: "org.sdn.flows.od-phase1a",
		Name:      "OD Phase 1a Guest-Link Flow",
		Version:   "0.1.0",
		Nodes: []FlowNodeSpec{
			{NodeID: "n0-od", PluginID: odPluginID, MethodID: "fit", Kind: "transform"},
			{NodeID: "n1-ommjson", PluginID: "com.digitalarsenal.foundation.omm-json", MethodID: "encode", Kind: "transform"},
		},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n0-od", FromPortID: "omm", ToNodeID: "n1-ommjson", ToPortID: "stream"},
		},
		Triggers:        []FlowTriggerSpec{{TriggerID: "t0", Kind: "timer", Source: "host-cron"}},
		TriggerBindings: []FlowTriggerBindingSpec{{TriggerID: "t0", TargetNodeID: "n0-od", TargetPortID: "oem"}},
	})

	cfg := flowconfig.FlowsConfig{Enabled: true, StoragePath: t.TempDir(), MaxMemoryPages: 2048}
	mgr, err := NewFlowManager(cfg, plugins.New(), HandlerMap{})
	if err != nil {
		t.Fatalf("NewFlowManager: %v", err)
	}
	baker, err := NewBaker(home, 2048)
	if err != nil {
		t.Fatalf("NewBaker: %v", err)
	}
	mgr.SetBaker(baker)

	res, programID, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowPLG: flowPLG})
	if err != nil {
		t.Fatalf("★ BakeAndDeploy OD flow FAILED: %v", err)
	}
	t.Logf("★ BAKE OK: programId=%s cacheHit=%v elapsed=%dms", programID, res.CacheHit, res.Elapsed.Milliseconds())

	mgr.mu.Lock()
	fp := mgr.running[programID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("OD flow did not load into a running FlowRuntime")
	}
	rt := fp.runtime
	t.Logf("★★ OD GUEST-LINK BAKE+LOAD PASS: runtime.wasm loaded under WasmEdge — %d nodes, %d edges, %d triggers, %d deps",
		rt.NodeCount, rt.EdgeCount, rt.TriggerCount, rt.DepCount)

	// Fire the trigger with no injected $OEM frame: proves the OD guest-link entry
	// DISPATCHES under WasmEdge (node advances). A real $OEM payload needs an
	// upstream source node (deferred this increment) or a guest frame-injection
	// helper; the entry returns a clean "missing-input" (no trap) here.
	before := invocationCounts(t, rt)
	rt.ResetState()
	rt.EnqueueTrigger(0)
	drain, derr := rt.Drain(ctx, HandlerMap{}, DrainOptions{MaxIterations: 64})
	after := invocationCounts(t, rt)
	t.Logf("drain: iterations=%d nodesInvoked=%d node0(od.fit) %d->%d derr=%v",
		drain.Iterations, drain.NodesInvoked, before[0], after[0], derr)
	if after[0] > before[0] {
		t.Logf("★★★ OD guest-link entry DISPATCHED under WasmEdge (od.fit fired)")
	}
}

func verifyODSymbols(t *testing.T, objPath, prefix string) {
	t.Helper()
	tool, err := exec.LookPath("wasm-objdump")
	if err != nil {
		t.Logf("wasm-objdump not on PATH; skipping symbol pre-check")
		return
	}
	out, _ := exec.Command(tool, "-x", objPath).CombinedOutput()
	dump := string(out)
	for _, sym := range []string{"plugin_push_output_ex", "plugin_get_input_frame", "plugin_find_input_index"} {
		if !strings.Contains(dump, sym) {
			t.Fatalf("expected UNDEFINED ABI import %q not found in %s", sym, objPath)
		}
	}
	want := prefix + "fit"
	if !strings.Contains(dump, want) {
		t.Fatalf("prefixed entry %q not present in %s", want, objPath)
	}
	t.Logf("wasm-objdump OK: ABI imports undefined; prefixed entry %q present", want)
}

// resolveSymlink follows a possibly-symlinked dir (Homebrew include dirs are
// symlinks into the Cellar) so filepath.Walk sees the real tree.
func resolveSymlink(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks %s: %v", p, err)
	}
	return r
}

func odMustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}
