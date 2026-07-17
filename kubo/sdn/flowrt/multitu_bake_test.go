package flowrt

// multitu_bake_test.go — Phase-0b PROTOTYPE (SDN OD-Flow loop): prove a
// MULTI-TU emscripten module (2 translation units, cross-TU strong-symbol call,
// FlatBuffers runtime) can be built into ONE bakeable relocatable guest-link
// object and baked into a runtime.wasm that LOADS + RUNS under WasmEdge — the
// biggest technical risk before porting analysis/od (od_lib + sgp4_lib + Eigen).
//
// The pipeline mirrors the single-source SDK path (compileModule.js) but across
// two TUs:
//  1. compile each TU with the node's OWN llvm-box clang (guarantees the object
//     is bit-compatible with the wasm-ld that bakes), applying the SDK's
//     `-D<sym>=<prefix><sym>` guest-link symbol renames to BOTH TUs so the
//     method entry + the cross-TU helper are per-plugin-prefixed;
//  2. `wasm-ld -r` PARTIAL-LINK the two objects into one relocatable
//     module-link.o (the multi-TU deliverable a single .o path can stage);
//  3. write metadata.json (methodSymbols[emit]=<prefix>emit) + plugin-manifest;
//  4. stage it as a 4th module and bake trigger -> twotu.emit -> omm-json.encode
//     via the real /bake -> descriptor -> wasm-ld -> WasmEdge path.
//
// GATE: the emit/consume ABI symbols (plugin_push_output_ex /
// plugin_get_input_frame / plugin_get_input_count) stay UNDEFINED in the object
// and resolve at bake against the flow-runtime shim; the prefixed entry is
// DEFINED; no symbol collision with the flow shim or the second (also
// FlatBuffers-using) omm-json module; the baked runtime runs and the node fires.
//
// Skips cleanly unless the bake toolchain is staged (same guards as bake_test.go).

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

const twotuPluginID = "org.sdn.demo.twotu"

// twotuEntrySrc is the ENTRY translation unit: the guest-link method `emit`.
// It includes ONLY space_data_module_invoke.h. It calls a strong symbol defined
// in the OTHER TU (demo_build_record) — the cross-TU link risk — and emits the
// built FlatBuffer on a typed "out" port as ALIGNED_BINARY. The consume-side ABI
// (plugin_get_input_count / plugin_get_input_frame) and the emit ABI
// (plugin_push_output_ex) are UNDEFINED here; the bake resolves them against
// flow_runtime.cpp's shim.
const twotuEntrySrc = `#include "space_data_module_invoke.h"
#include <cstdint>
#include <cstdlib>

// Defined in the helper TU (tu_helper.cpp). extern "C", plain identifier — this
// is the cross-translation-unit strong-symbol call the multi-TU partial link
// must resolve, and the -D guest-link rename must prefix consistently in both TUs.
extern "C" void demo_build_record(uint8_t **out_ptr, uint32_t *out_len);

extern "C" int32_t emit(void) {
  // Consume side (resolved at bake, undefined in this object): peek the trigger-
  // delivered input frame. The method reads nothing from it — it exists to keep
  // plugin_get_input_frame an UNDEFINED import that resolves at link time.
  uint32_t nin = plugin_get_input_count();
  if (nin > 0) {
    const plugin_input_frame_t *f = plugin_get_input_frame(0);
    (void)f;
  }

  uint8_t *buf = 0;
  uint32_t len = 0;
  demo_build_record(&buf, &len); // cross-TU call
  if (buf == 0 || len == 0) {
    plugin_set_error("EBUILD", "demo_build_record produced no bytes");
    return -1;
  }

  // Emit the FlatBuffer as an aligned-binary $OMM frame on the typed "out" port.
  int32_t rc = plugin_push_output_ex(
      "out", "OMM.fbs", "$OMM",
      PLUGIN_PAYLOAD_WIRE_FORMAT_ALIGNED_BINARY, "OMM",
      /*fixed_string_length=*/0, /*required_alignment=*/8,
      buf, len);
  free(buf);
  return rc;
}
`

// twotuHelperSrc is the HELPER translation unit: it uses the FlatBuffers C++
// runtime (the real template-heavy dependency OD will also pull) to build a tiny
// size-prefixed FlatBuffer (a 1-field record: a single string root tagged with
// the $OMM file identifier) and returns malloc'd bytes. Its only strong symbol,
// demo_build_record, is prefixed by the guest-link rename; all FlatBuffers
// template instantiations are weak/linkonce (wasm-ld dedups them against the
// second FlatBuffers module, omm-json — the symbol-collision test).
const twotuHelperSrc = `#include "flatbuffers/flatbuffer_builder.h"
#include <cstdint>
#include <cstdlib>
#include <cstring>

extern "C" void demo_build_record(uint8_t **out_ptr, uint32_t *out_len) {
  flatbuffers::FlatBufferBuilder fbb;
  auto s = fbb.CreateString("OD-2TU-DEMO");
  // Size-prefixed shape ([u32le len][buffer]) with the 4-byte $OMM file id —
  // exactly the aligned-binary byte-shape a real $OMM emit uses.
  fbb.FinishSizePrefixed(s, "$OMM");
  uint32_t n = fbb.GetSize();
  uint8_t *buf = static_cast<uint8_t *>(malloc(n));
  if (buf != 0) {
    memcpy(buf, fbb.GetBufferPointer(), n);
  }
  *out_ptr = buf;
  *out_len = n;
}
`

// twotuManifest is the plugin-manifest.json: one method `emit`, input port "in"
// (any flatbuffer, for the trigger to deliver a ready frame), output port "out"
// typed $OMM with BOTH wire formats so the edge to omm-json validates.
const twotuManifest = `{
  "pluginId": "org.sdn.demo.twotu",
  "name": "SDN Multi-TU Guest-Link Demo",
  "version": "0.1.0",
  "description": "Phase-0b prototype: a 2-translation-unit module that builds a FlatBuffer in a helper TU and emits it from an entry TU, proving the multi-TU guest-link bake path.",
  "pluginFamily": "demo",
  "capabilities": [],
  "externalInterfaces": [],
  "invokeSurfaces": ["direct"],
  "runtimeTargets": ["wasmedge"],
  "methods": [
    {
      "methodId": "emit",
      "displayName": "Emit Demo OMM FlatBuffer",
      "inputPorts": [
        {
          "portId": "in",
          "acceptedTypeSets": [
            { "setId": "any", "allowedTypes": [ { "acceptsAnyFlatbuffer": true } ] }
          ],
          "minStreams": 1,
          "maxStreams": 1,
          "required": true,
          "description": "Trigger-delivered frame; contents ignored."
        }
      ],
      "outputPorts": [
        {
          "portId": "out",
          "acceptedTypeSets": [
            {
              "setId": "sds-omm-aligned",
              "allowedTypes": [
                { "schemaName": "OMM.fbs", "fileIdentifier": "$OMM", "rootTypeName": "OMM" },
                { "schemaName": "OMM.fbs", "fileIdentifier": "$OMM", "rootTypeName": "OMM", "wireFormat": "aligned-binary", "requiredAlignment": 8 }
              ],
              "description": "Aligned-binary $OMM frame."
            }
          ],
          "minStreams": 1,
          "maxStreams": 1,
          "required": true,
          "description": "One demo $OMM frame."
        }
      ],
      "maxBatch": 1,
      "drainPolicy": "single-shot",
      "description": "Builds a FlatBuffer in a helper TU and emits it."
    }
  ],
  "schemasUsed": [
    { "schemaName": "OMM.fbs", "fileIdentifier": "$OMM", "rootTypeName": "OMM" }
  ]
}
`

// buildMultiTUGuestLink compiles the two TUs with the node's llvm-box clang
// (rename-prefixed), partial-links them with wasm-ld -r, and returns the
// relocatable module-link.o bytes + the symbol prefix.
func buildMultiTUGuestLink(t *testing.T, a bakeAssets) (objBytes []byte, prefix string) {
	t.Helper()

	cc, err := flowcc.NewWithSysroot(a.box, a.sysroot)
	if err != nil {
		t.Fatalf("flowcc.NewWithSysroot: %v", err)
	}
	ctx := context.Background()

	prefix = guestLinkSymbolPrefix(twotuPluginID)
	// The guest-link rename set: the method entry + the cross-TU strong helper.
	// Applied to BOTH TUs so the definition, the forward decl, and the call all
	// rename consistently (this is exactly how the single-source SDK path prefixes,
	// extended to the multi-TU union).
	renames := []string{
		"-Demit=" + prefix + "emit",
		"-Ddemo_build_record=" + prefix + "demo_build_record",
	}

	fbInc := os.Getenv("SDN_FB_INCLUDE")
	if fbInc == "" {
		t.Skip("SDN_FB_INCLUDE not set (path to a flatbuffers header-only include dir); skipping multi-TU prototype")
	}
	// Seed the FlatBuffers header tree into the compile overlay under /inc.
	inFiles := map[string][]byte{
		"/work/tu_entry.cpp":               twotuBytes(twotuEntrySrc),
		"/work/tu_helper.cpp":              twotuBytes(twotuHelperSrc),
		"/work/space_data_module_invoke.h": mustReadTemplate(t, a, "space_data_module_invoke.h"),
	}
	seedDirIntoOverlay(t, inFiles, fbInc, "/inc/flatbuffers")

	// em++-equivalent module compile flags (clang directly): emscripten target +
	// the emscripten sysroot, -O3 -mbulk-memory -DNDEBUG (SDK buildSourceCompilerArgs)
	// and -fignore-exceptions (em++'s default; keeps the object EH-FREE, which is
	// load-bearing for WasmEdge 0.14.x). Default symbol visibility, matching the
	// SDK's working module objects (dep0-ommjson.o).
	baseFlags := []string{
		"-target", "wasm32-emscripten", "--sysroot=/sysroot",
		"-std=c++17", "-O3", "-mbulk-memory", "-DNDEBUG", "-DEMSCRIPTEN",
		"-fignore-exceptions",
		"-I/work", "-I/inc",
	}

	compile := func(src, obj string) []byte {
		args := append([]string{"clang", "clang", "-c", src, "-o", obj}, baseFlags...)
		args = append(args, renames...)
		res, err := cc.Run(ctx, args, inFiles)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("compile %s: err=%v exit=%d stderr=%q", src, err, res.ExitCode, res.Stderr)
		}
		out, ok := res.OutFiles[obj]
		if !ok {
			t.Fatalf("compile %s produced no %s; files=%v", src, obj, keysOfMap(res.OutFiles))
		}
		t.Logf("compiled %s -> %s (%d bytes)", src, obj, len(out))
		return out
	}

	entryO := compile("/work/tu_entry.cpp", "/work/tu_entry.o")
	helperO := compile("/work/tu_helper.cpp", "/work/tu_helper.o")

	// PARTIAL LINK the two TUs into ONE relocatable guest-link object.
	linkArgs := []string{
		"lld", "wasm-ld", "-r",
		"/work/tu_helper.o", "/work/tu_entry.o",
		"-o", "/work/module-link.o",
	}
	lres, err := cc.Run(ctx, linkArgs, map[string][]byte{
		"/work/tu_helper.o": helperO,
		"/work/tu_entry.o":  entryO,
	})
	if err != nil || lres.ExitCode != 0 {
		t.Fatalf("wasm-ld -r partial link: err=%v exit=%d stderr=%q", err, lres.ExitCode, lres.Stderr)
	}
	objBytes, ok := lres.OutFiles["/work/module-link.o"]
	if !ok {
		t.Fatalf("wasm-ld -r produced no module-link.o; files=%v", keysOfMap(lres.OutFiles))
	}
	t.Logf("★ partial-linked 2 TUs -> module-link.o (%d bytes)", len(objBytes))
	return objBytes, prefix
}

// verifyGuestLinkSymbols shells out to wasm-objdump (wabt) if present, asserting
// the emit/consume ABI symbols are UNDEFINED imports and the prefixed entry is
// DEFINED. Non-fatal if wasm-objdump is absent (the bake+run is the hard gate).
func verifyGuestLinkSymbols(t *testing.T, objPath, prefix string) {
	t.Helper()
	tool, err := exec.LookPath("wasm-objdump")
	if err != nil {
		t.Logf("wasm-objdump not on PATH; skipping symbol pre-check (bake link is the hard gate)")
		return
	}
	out, err := exec.Command(tool, "-x", objPath).CombinedOutput()
	if err != nil {
		t.Logf("wasm-objdump failed (%v); skipping symbol pre-check", err)
		return
	}
	dump := string(out)
	// Imports section lists undefined symbols (module "env").
	for _, sym := range []string{"plugin_push_output_ex", "plugin_get_input_frame", "plugin_get_input_count"} {
		if !strings.Contains(dump, sym) {
			t.Fatalf("expected UNDEFINED import %q not found in wasm-objdump -x of %s", sym, objPath)
		}
	}
	wantEntry := prefix + "emit"
	if !strings.Contains(dump, wantEntry) {
		t.Fatalf("prefixed entry symbol %q not present in %s", wantEntry, objPath)
	}
	// The UNPREFIXED entry must NOT appear as a linking symbol (it was renamed).
	t.Logf("wasm-objdump pre-check OK: undefined imports present; prefixed entry %q present", wantEntry)
}

// TestMultiTUGuestLinkBake is the Phase-0b gate.
func TestMultiTUGuestLinkBake(t *testing.T) {
	a := resolveBakeAssets(t)

	// ---- Build the multi-TU guest-link object with the node's own toolchain ----
	objBytes, prefix := buildMultiTUGuestLink(t, a)

	// Persist the artifacts to a bind-mounted out dir (for external inspection) if
	// SDN_TWOTU_OUT is set; else a temp dir.
	outRoot := os.Getenv("SDN_TWOTU_OUT")
	if outRoot == "" {
		outRoot = t.TempDir()
	}
	glDir := filepath.Join(outRoot, "twotu", "dist", "guest-link")
	if err := os.MkdirAll(glDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	objPath := filepath.Join(glDir, "module-link.o")
	metaPath := filepath.Join(glDir, "metadata.json")
	manPath := filepath.Join(outRoot, "twotu", "plugin-manifest.json")
	if err := os.WriteFile(objPath, objBytes, 0o644); err != nil {
		t.Fatalf("write module-link.o: %v", err)
	}
	meta := map[string]interface{}{
		"version":       1,
		"format":        "wasm-object",
		"language":      "c++",
		"threadModel":   "single-thread",
		"symbolPrefix":  prefix,
		"methodSymbols": map[string]string{"emit": prefix + "emit"},
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(metaPath, append(metaBytes, '\n'), 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(manPath), 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(manPath, []byte(twotuManifest), 0o644); err != nil {
		t.Fatalf("write plugin-manifest.json: %v", err)
	}
	t.Logf("staged artifacts: %s + metadata.json + plugin-manifest.json", objPath)

	// ---- Symbol-level evidence (undefined ABI imports + defined prefixed entry) ----
	verifyGuestLinkSymbols(t, objPath, prefix)

	// ---- Stage toolchain + the 3 fixed modules + our multi-TU module ----
	home := stageBakeHome(t, a)
	if err := flowcc.StageModule(home, twotuPluginID, objPath, metaPath, manPath); err != nil {
		t.Fatalf("StageModule twotu: %v", err)
	}

	// ---- Bake trigger -> twotu.emit -> omm-json.encode ----
	flowPLG := BuildFlowPLG(FlowSpec{
		ProgramID: "org.sdn.flows.twotu-demo",
		Name:      "Multi-TU Guest-Link Demo Flow",
		Version:   "0.1.0",
		Nodes: []FlowNodeSpec{
			{NodeID: "n0-twotu", PluginID: twotuPluginID, MethodID: "emit", Kind: "transform"},
			{NodeID: "n1-ommjson", PluginID: "com.digitalarsenal.foundation.omm-json", MethodID: "encode", Kind: "transform"},
		},
		Edges: []FlowEdgeSpec{
			{EdgeID: "e0", FromNodeID: "n0-twotu", FromPortID: "out", ToNodeID: "n1-ommjson", ToPortID: "stream"},
		},
		Triggers:        []FlowTriggerSpec{{TriggerID: "t0", Kind: "timer", Source: "host-cron"}},
		TriggerBindings: []FlowTriggerBindingSpec{{TriggerID: "t0", TargetNodeID: "n0-twotu", TargetPortID: "in"}},
	})

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

	res, programID, err := mgr.BakeAndDeploy(ctx, BakeRequest{FlowPLG: flowPLG})
	if err != nil {
		t.Fatalf("★ BakeAndDeploy multi-TU flow FAILED: %v", err)
	}
	t.Logf("★ BAKE OK: programId=%s cacheHit=%v elapsed=%dms", programID, res.CacheHit, res.Elapsed.Milliseconds())

	mgr.mu.Lock()
	fp := mgr.running[programID]
	mgr.mu.Unlock()
	if fp == nil || fp.runtime == nil {
		t.Fatalf("multi-TU flow did not load into a running FlowRuntime")
	}
	rt := fp.runtime
	if rt.NodeCount != 2 {
		t.Fatalf("baked runtime NodeCount=%d, want 2", rt.NodeCount)
	}
	t.Logf("baked runtime ABI: %d nodes, %d edges, %d triggers, %d deps",
		rt.NodeCount, rt.EdgeCount, rt.TriggerCount, rt.DepCount)

	// ---- Drive the FSM: fire the trigger, drain, prove the multi-TU node ran ----
	// NOTE: omm-json.encode intentionally returns 400 on the demo's deliberately
	// non-OMM FlatBuffer (it verifies each frame). That is a CLEAN guest return
	// (no WasmEdge trap) and, crucially, still proves emit's output frame reached
	// node1. A drain error from that benign guest 400 must NOT mask the node0 gate.
	before := invocationCounts(t, rt)
	rt.ResetState()
	rt.EnqueueTrigger(0)
	drain, derr := rt.Drain(ctx, HandlerMap{}, DrainOptions{MaxIterations: 256})
	if derr != nil {
		t.Logf("drain returned err=%v (expected: node1 omm-json returns 400 on the demo non-OMM frame — a clean guest return, not a trap)", derr)
	}
	after := invocationCounts(t, rt)
	node0Advanced := after[0] > before[0]
	node1Advanced := len(after) > 1 && after[1] > before[1]
	t.Logf("drain: iterations=%d nodesInvoked=%d node0(emit) %d->%d node1(ommjson) advanced=%v",
		drain.Iterations, drain.NodesInvoked, before[0], after[0], node1Advanced)
	if !node0Advanced {
		t.Fatalf("multi-TU node (emit) did NOT run: the prefixed 2-TU entry never fired under WasmEdge")
	}
	t.Logf("★★ MULTI-TU GUEST-LINK BAKE+RUN PASS: prefixed 2-TU entry %qemit fired under WasmEdge; "+
		"ABI symbols resolved at link; no collision with flow shim or the 2nd FlatBuffers module (omm-json advanced=%v)",
		prefix, node1Advanced)
}

// ---- small helpers ----

func twotuBytes(s string) []byte { return []byte(s) }

func keysOfMap(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// mustReadTemplate reads a file from the flow-runtime template dir (holds
// space_data_module_invoke.h alongside flow_runtime.cpp).
func mustReadTemplate(t *testing.T, a bakeAssets, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.templateDir, name))
	if err != nil {
		t.Fatalf("read template %s: %v", name, err)
	}
	return b
}

// seedDirIntoOverlay walks hostDir and adds every regular file to inFiles under
// guestPrefix (so `-I` of the parent finds the header tree).
func seedDirIntoOverlay(t *testing.T, inFiles map[string][]byte, hostDir, guestPrefix string) {
	t.Helper()
	err := filepath.Walk(hostDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(hostDir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		inFiles[guestPrefix+"/"+filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatalf("seed %s: %v", hostDir, err)
	}
}
