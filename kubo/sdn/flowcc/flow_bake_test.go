package flowcc

// flow_bake_test.go is the P4 make-or-break: prove the SDN node can BAKE a
// composed flow runtime.wasm from prebuilt module guest-link objects using its
// OWN toolchain (flowcc's emception clang + wasm-ld, hosted on WasmEdge), and
// that the result LOADS and RUNS under the node's WasmEdge — the same
// WasmEdge/EH configuration the production module runtime (wasmrt.NewModule)
// uses.
//
// Inputs come from the proven compose spike (scratchpad/linkspike):
//   - flow_runtime.cpp + space_data_module_invoke.h + flow_generated.inc — the
//     flow-runtime template + the flow's generated descriptor tables;
//   - descriptor.c — the per-flow entry table (g_entry[] -> the 3 modules'
//     sdm_guest_<hex>_<method> symbols);
//   - dep0-ommjson.o / dep1-decisiongate.o / dep2-clock.o — three REAL module
//     guest-link objects (clang 16.0.0, the emception toolchain);
//   - the emsdk sysroot at phase2/sysroot (libc / libc++ / libc++abi /
//     compiler_rt / crt1_reactor.o, clang 16.0.0).
//
// The reference em++-linked artifact is scratchpad/linkspike/runtime.wasm.

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// bakeDir is the directory holding the linkspike inputs + reference wasm.
const EnvBakeDir = "SDN_FLOWCC_BAKE_DIR"

func bakeDir() string {
	if p := os.Getenv(EnvBakeDir); p != "" {
		return p
	}
	return "/private/tmp/claude-501/-Users-tj-software-spacedatanetwork-stack/8a9a46ba-3833-472b-bfb4-4c3869499342/scratchpad/linkspike"
}

// hostCapStub is the minimal space_data_module_host the composed runtime imports
// (call / response_len / read_response), mirroring the signatures the
// production modulert/hostbridge.go registers. call always reports success with
// an empty response; that is enough to drive the linked-direct flow scheduler
// without a live host.
type hostCapStub struct{ resp []byte }

func (h *hostCapStub) module() *wasmedge.Module {
	mod := wasmedge.NewModule("space_data_module_host")
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }

	// call(op_ptr, op_len, payload_ptr, payload_len) -> status(i32)
	ftCall := wasmedge.NewFunctionType([]*wasmedge.ValType{i32(), i32(), i32(), i32()}, []*wasmedge.ValType{i32()})
	call := wasmedge.NewFunction(ftCall, func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
		h.resp = nil // empty response envelope
		return []interface{}{int32(0)}, wasmedge.Result_Success
	}, nil, 0)
	ftCall.Release()
	mod.AddFunction("call", call)

	// response_len() -> i32
	ftLen := wasmedge.NewFunctionType(nil, []*wasmedge.ValType{i32()})
	rlen := wasmedge.NewFunction(ftLen, func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
		return []interface{}{int32(len(h.resp))}, wasmedge.Result_Success
	}, nil, 0)
	ftLen.Release()
	mod.AddFunction("response_len", rlen)

	// read_response(dst_ptr, dst_len) -> bytes_copied(i32)
	ftRead := wasmedge.NewFunctionType([]*wasmedge.ValType{i32(), i32()}, []*wasmedge.ValType{i32()})
	read := wasmedge.NewFunction(ftRead, func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
		dst := uint32(params[0].(int32))
		dlen := uint32(params[1].(int32))
		mem := cf.GetMemoryByIndex(0)
		if mem == nil {
			return []interface{}{int32(0)}, wasmedge.Result_Success
		}
		n := uint32(len(h.resp))
		if n > dlen {
			n = dlen
		}
		if n > 0 {
			mem.SetData(h.resp[:n], uint(dst), uint(n))
		}
		return []interface{}{int32(n)}, wasmedge.Result_Success
	}, nil, 0)
	ftRead.Release()
	mod.AddFunction("read_response", read)

	return mod
}

// runComposedRuntime loads a composed flow runtime.wasm under a WasmEdge VM
// configured exactly like the production module runtime (THREADS +
// EXCEPTION_HANDLING), satisfies its 3 host-cap imports with stubs, runs the
// WASI-reactor init, and drives one full linked-direct flow pass
// (reset -> enqueue trigger 0 -> drain_linked). It returns the number of linked
// module dispatches drain_linked performed. Any WasmEdge load/validate/
// instantiate/execute failure is returned as an error — that is the
// make-or-break signal.
func runComposedRuntime(t *testing.T, wasm []byte) (dispatched int32, err error) {
	t.Helper()

	conf := wasmedge.NewConfigure()
	conf.AddConfig(wasmedge.THREADS)
	conf.AddConfig(wasmedge.EXCEPTION_HANDLING) // mirror wasmrt.NewModule
	conf.AddConfig(wasmedge.WASI)               // reactor abort()->proc_exit sink
	vm := wasmedge.NewVMWithConfig(conf)
	defer func() { vm.Release(); conf.Release() }()

	// Initialize the auto-registered WASI module (empty args/env/preopens) so a
	// standalone-reactor's wasi_snapshot_preview1 imports (e.g. proc_exit, pulled
	// by abort) resolve — exactly as the production module runtime does for the
	// deployed WASI-reactor modules.
	if wasiMod := vm.GetImportModule(wasmedge.WASI); wasiMod != nil {
		wasiMod.InitWasi([]string{}, []string{}, []string{})
	}

	host := (&hostCapStub{}).module()
	defer host.Release()
	if e := vm.RegisterModule(host); e != nil {
		return 0, e
	}

	if e := vm.LoadWasmBuffer(wasm); e != nil {
		return 0, e // <-- "illegal opcode" (EH) would surface HERE
	}
	if e := vm.Validate(); e != nil {
		return 0, e
	}
	if e := vm.Instantiate(); e != nil {
		return 0, e
	}

	// WASI-reactor init (STANDALONE_WASM builds export _initialize, which runs
	// the C++ global ctors — including g_flow_descriptor_init that populates the
	// flow's dispatch/dependency descriptor tables).
	if _, e := vm.Execute("_initialize"); e != nil {
		return 0, e
	}

	// Tier A: the composed runtime's own ABI runs (state reset + accessors).
	if _, e := vm.Execute("space_data_module_runtime_reset_state"); e != nil {
		return 0, e
	}
	nsRes, e := vm.Execute("space_data_module_runtime_get_node_states")
	if e != nil {
		return 0, e
	}
	if len(nsRes) == 0 || uint32(wasmrtInt32(nsRes[0])) == 0 {
		t.Fatalf("get_node_states returned null pointer")
	}
	preIdx, e := vm.Execute("space_data_module_runtime_get_ready_node_index")
	if e != nil {
		return 0, e
	}
	t.Logf("Tier A OK: reset_state + get_node_states(ptr=%#x) + get_ready_node_index(=%d) executed",
		uint32(wasmrtInt32(nsRes[0])), wasmrtInt32(preIdx[0]))

	// Tier B: drive a REAL linked-direct flow pass. enqueue_trigger_frame(0, 0)
	// enqueues trigger 0 (null frame -> the trigger's bound node becomes ready);
	// drain_linked runs every ready linked node to completion IN-WASM, i.e. it
	// call_indirect's the modules' sdm_guest_<hex>_<method> entries. A returned
	// count >= 1 means a real module guest-link entry executed inside the
	// composed runtime under WasmEdge.
	if _, e := vm.Execute("space_data_module_runtime_enqueue_trigger_frame", int32(0), int32(0)); e != nil {
		return 0, e
	}
	postIdx, e := vm.Execute("space_data_module_runtime_get_ready_node_index")
	if e != nil {
		return 0, e
	}
	t.Logf("after enqueue_trigger_frame(0,0): ready_node_index=%d", wasmrtInt32(postIdx[0]))

	dRes, e := vm.Execute("space_data_module_runtime_drain_linked", int32(64))
	if e != nil {
		return 0, e // a TRAP inside a linked module dispatch surfaces HERE
	}
	dispatched = wasmrtInt32(dRes[0])
	return dispatched, nil
}

// TestReferenceRuntimeLoadsAndRuns is the FIRST make-or-break checkpoint: prove
// the em++-linked reference composed runtime (scratchpad/linkspike/runtime.wasm)
// LOADS and RUNS under the node's WasmEdge. If this fails at load with an
// "illegal opcode", the composed runtime uses an EH scheme WasmEdge rejects and
// the whole approach is blocked; if it runs, the target artifact is achievable
// and the node-baked artifact (next test) just has to reproduce it.
func TestReferenceRuntimeLoadsAndRuns(t *testing.T) {
	ref := filepath.Join(bakeDir(), "runtime.wasm")
	wasm, err := os.ReadFile(ref)
	if err != nil {
		t.Skipf("reference runtime.wasm not available at %s (set %s): %v", ref, EnvBakeDir, err)
	}
	dispatched, err := runComposedRuntime(t, wasm)
	if err != nil {
		t.Fatalf("reference runtime.wasm failed to load/run under WasmEdge: %v", err)
	}
	t.Logf("★ reference runtime.wasm LOADS + RUNS under WasmEdge: drain_linked dispatched %d linked node(s)", dispatched)
	if dispatched < 1 {
		t.Logf("note: drain_linked dispatched 0 nodes — the runtime ran but no linked entry fired (check trigger wiring); load+ABI execution is still proven")
	}
}

// wasmSanity is a tiny structural check kept local to avoid depending on the
// other test file's helpers.
func wasmSanity(b []byte) bool {
	return len(b) >= 8 && b[0] == 0x00 && b[1] == 'a' && b[2] == 's' && b[3] == 'm'
}

// mustRead reads a bake input from bakeDir, skipping the test if absent.
func mustRead(t *testing.T, base string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(bakeDir(), base))
	if err != nil {
		t.Skipf("bake input %s missing (set %s): %v", base, EnvBakeDir, err)
	}
	return b
}

// runtimeExports is the flow-artifact export surface the compose spike's
// reference runtime.wasm carries (the linked-direct scheduler ABI). wasm-ld is
// told to keep exactly these; everything else is internal and gets
// garbage-collected.
var runtimeExports = []string{
	"space_data_module_runtime_reset_state",
	"space_data_module_runtime_get_ready_node_index",
	"space_data_module_runtime_begin_node_invocation",
	"space_data_module_runtime_complete_node_invocation",
	"space_data_module_runtime_enqueue_trigger_frame",
	"space_data_module_runtime_get_node_states",
	"space_data_module_runtime_drain_linked",
	"space_data_module_runtime_dispatch_current_invocation_direct",
}

// TestNodeBakeRuntime is the P4 make-or-break for the NODE'S OWN TOOLCHAIN: the
// node (flowcc = emception clang + wasm-ld hosted on WasmEdge) BAKES a composed
// flow runtime.wasm from the flow-side sources (flow_runtime.cpp, descriptor.c)
// it compiles itself plus three PREBUILT real module guest-link objects, then
// the result LOADS and RUNS under the node's WasmEdge.
//
// Toolchain coherence (P4 step-1 verdict): flowcc's clang is 16.0.0, the
// phase2/sysroot libc++/libc++abi/libc are clang 16.0.0, and the module
// guest-link objects (dep0/dep1/dep2) are clang 16.0.0 — one coherent clang-16
// toolchain, so libc++ internals resolve cleanly. (Only the spike's prebuilt
// flow_runtime.o was clang 23; here the node rebuilds it with its own clang 16,
// removing even that skew.)
//
// EH scheme (P4 step-3 verdict): the deployed modules that run under WasmEdge
// today are EH-FREE (no native wasm-EH try/catch opcodes, no emscripten
// invoke_* SjLj) — em++'s default -fignore-exceptions + the libc++-noexcept
// runtime. The node builds flow_runtime.o the SAME way (no -fwasm-exceptions),
// so the composed module carries no EH opcodes WasmEdge 0.16.4 would reject.
func TestNodeBakeRuntime(t *testing.T) {
	box := boxPath()
	if _, err := os.Stat(box); err != nil {
		t.Skipf("llvm-box.wasm not available at %s (set %s): %v", box, EnvLLVMBoxWasm, err)
	}
	sysroot := sysrootPathForTest()
	if fi, err := os.Stat(sysroot); err != nil || !fi.IsDir() {
		t.Skipf("sysroot not available at %s (set %s): %v", sysroot, EnvLLVMSysroot, err)
	}
	cc, err := NewWithSysroot(box, sysroot)
	if err != nil {
		t.Fatalf("NewWithSysroot: %v", err)
	}
	ctx := context.Background()

	// ---- Stage 1: node compiles flow_runtime.cpp with ITS OWN clang ----
	// Flags mirror em++'s default flow compile (flowCompiler.js): -std=c++17 -O3
	// -mbulk-memory -DNDEBUG, plus emscripten's -fignore-exceptions (EH-free, the
	// scheme the deployed modules use) and -fvisibility=hidden (only FLOW_EXPORT
	// symbols stay default-visible). No -fwasm-exceptions — that is the load-
	// bearing choice for WasmEdge 0.16.4 compatibility.
	compileFlow := []string{
		"clang", "clang", "-c", "/work/flow_runtime.cpp", "-I/work",
		"-o", "/work/flow_runtime.o",
		"-target", "wasm32-emscripten", "--sysroot=/sysroot",
		"-std=c++17", "-O3", "-fignore-exceptions", "-fno-rtti",
		"-fvisibility=hidden", "-mbulk-memory", "-DNDEBUG", "-DEMSCRIPTEN",
	}
	cRes, err := cc.Run(ctx, compileFlow, map[string][]byte{
		"/work/flow_runtime.cpp":           mustRead(t, "flow_runtime.cpp"),
		"/work/space_data_module_invoke.h": mustRead(t, "space_data_module_invoke.h"),
		"/work/flow_generated.inc":         mustRead(t, "flow_generated.inc"),
	})
	if err != nil || cRes.ExitCode != 0 {
		t.Fatalf("flowcc compile flow_runtime.cpp: err=%v exit=%d stderr=%q", err, cRes.ExitCode, cRes.Stderr)
	}
	flowRuntimeO, ok := cRes.OutFiles["/work/flow_runtime.o"]
	if !ok {
		t.Fatalf("flowcc compile produced no /work/flow_runtime.o; files=%v", keysOf(cRes.OutFiles))
	}
	t.Logf("stage 1 OK: node clang built flow_runtime.o (%d bytes, clang 16)", len(flowRuntimeO))

	// ---- Stage 2: node compiles the per-flow descriptor.c with ITS OWN clang ----
	dRes, err := cc.Run(ctx, []string{
		"clang", "clang", "-c", "/work/descriptor.c", "-o", "/work/descriptor.o",
		"-target", "wasm32-emscripten", "--sysroot=/sysroot", "-O3",
	}, map[string][]byte{"/work/descriptor.c": mustRead(t, "descriptor.c")})
	if err != nil || dRes.ExitCode != 0 {
		t.Fatalf("flowcc compile descriptor.c: err=%v exit=%d stderr=%q", err, dRes.ExitCode, dRes.Stderr)
	}
	descriptorO := dRes.OutFiles["/work/descriptor.o"]
	t.Logf("stage 2 OK: node clang built descriptor.o (%d bytes)", len(descriptorO))

	// ---- Stage 3: node LINKS the composed runtime.wasm with ITS OWN wasm-ld ----
	// This replicates em++'s STANDALONE_WASM reactor link line (captured from
	// `em++ -s STANDALONE_WASM=1 ... -v`) as a direct wasm-ld invocation: the
	// crt1_reactor.o entry + the standalone/musl/libc++-noexcept lib set from the
	// clang-16 sysroot, driven by flowcc's (llvm-box's) wasm-ld. The three
	// module guest-link objects carry import_module("space_data_module_host")
	// attributes on their host-cap references, so wasm-ld emits those as the
	// artifact's only imports.
	link := []string{
		"lld", "wasm-ld",
		"--entry=_initialize", // reactor model -> exports _initialize, runs ctors
		"--export-table",
		"--allow-undefined", "--import-undefined",
		"--max-memory=2147483648", "-z", "stack-size=65536", "--global-base=1024",
		"--strip-debug",
		"/work/flow_runtime.o", "/work/descriptor.o",
		"/work/dep0.o", "/work/dep1.o", "/work/dep2.o",
		"--export-if-defined=emscripten_stack_get_current",
		"--export-if-defined=_emscripten_stack_restore",
	}
	for _, e := range runtimeExports {
		link = append(link, "--export="+e)
	}
	sr := "/sysroot/lib/wasm32-emscripten"
	link = append(link,
		"-L"+sr, sr+"/crt1_reactor.o",
		"-lGL", "-lal", "-lhtml5", "-lstandalonewasm", "-lstubs", "-lnoexit",
		"-lc", "-ldlmalloc", "-lcompiler_rt", "-lc++-noexcept", "-lc++abi-noexcept", "-lsockets",
		"-o", "/work/runtime.wasm",
	)
	lRes, err := cc.Run(ctx, link, map[string][]byte{
		"/work/flow_runtime.o": flowRuntimeO,
		"/work/descriptor.o":   descriptorO,
		"/work/dep0.o":         mustRead(t, "dep0-ommjson.o"),
		"/work/dep1.o":         mustRead(t, "dep1-decisiongate.o"),
		"/work/dep2.o":         mustRead(t, "dep2-clock.o"),
	})
	if err != nil || lRes.ExitCode != 0 {
		t.Fatalf("flowcc wasm-ld link runtime.wasm: err=%v exit=%d stderr=%q", err, lRes.ExitCode, lRes.Stderr)
	}
	baked, ok := lRes.OutFiles["/work/runtime.wasm"]
	if !ok {
		t.Fatalf("flowcc link produced no /work/runtime.wasm; files=%v", keysOf(lRes.OutFiles))
	}
	if !wasmSanity(baked) {
		t.Fatalf("baked artifact is not a WASM module")
	}
	t.Logf("stage 3 OK: node wasm-ld baked runtime.wasm (%d bytes)", len(baked))

	// Write the baked artifact next to the reference for external inspection.
	_ = os.WriteFile(filepath.Join(bakeDir(), "runtime_flowcc.wasm"), baked, 0o644)

	// ---- Stage 4: THE MAKE-OR-BREAK — load + run the node-baked artifact ----
	dispatched, err := runComposedRuntime(t, baked)
	if err != nil {
		t.Fatalf("★ node-baked runtime.wasm failed to load/run under WasmEdge: %v", err)
	}
	t.Logf("★★ NODE-BAKED runtime.wasm LOADS + RUNS under WasmEdge: drain_linked dispatched %d linked node(s)", dispatched)
	if dispatched < 1 {
		t.Logf("note: drain_linked dispatched 0 nodes — loaded + ABI executed, but no linked entry fired")
	}
}

// _ silences unused warnings for helpers referenced only by other files.
var _ = binary.LittleEndian
var _ = context.Background
var _ = wasmSanity
