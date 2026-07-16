package flowcc

// aot.go AOT-compiles llvm-box.wasm with WasmEdge's own compiler
// (WasmEdge_CompilerCompile, exported by the AOT-capable libwasmedge the node
// ships) so clang + wasm-ld run as NATIVE code instead of the interpreter. The
// 58 emscripten host imports (module "a") are UNCHANGED — AOT compiles only the
// guest's own code; imports are still resolved at instantiation exactly as in
// interpreter mode, so the emshim host module binds identically and the compiler
// output is byte-for-byte the same. The output is universal-wasm format (native
// code in a custom section of a still-valid .wasm), loaded via LoadWasmFile.

import (
	"fmt"
	"os"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// CompileBoxAOT AOT-compiles the staged llvm-box.wasm into home.BoxAotPath().
// It is a STAGE-TIME (or boot-time) one-shot: the artifact is reused read-only
// forever after. Returns the output path. Safe to call when already compiled
// (it overwrites atomically). Requires an AOT-capable libwasmedge (the shipped
// 0.16.4 build is); returns an error otherwise so the caller falls back to the
// interpreted box.
func CompileBoxAOT(home Home) (string, error) {
	box := home.BoxPath()
	if !fileExists(box) {
		return "", fmt.Errorf("flowcc: cannot AOT-compile: box not staged at %s", box)
	}
	out := home.BoxAotPath()

	conf := wasmedge.NewConfigure()
	if conf == nil {
		return "", fmt.Errorf("flowcc: create configure")
	}
	defer conf.Release()
	// Universal-wasm output keeps a valid .wasm the loader can pick up as AOT.
	conf.SetCompilerOutputFormat(wasmedge.CompilerOutputFormat_Wasm)
	conf.SetCompilerOptimizationLevel(wasmedge.CompilerOptLevel_O2)

	comp := wasmedge.NewCompilerWithConfig(conf)
	if comp == nil {
		return "", fmt.Errorf("flowcc: create AOT compiler (libwasmedge not AOT-capable?)")
	}
	defer comp.Release()

	tmp := out + ".tmp"
	_ = os.Remove(tmp)
	if err := comp.Compile(box, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("flowcc: AOT compile llvm-box: %w", err)
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("flowcc: install AOT box: %w", err)
	}
	return out, nil
}
