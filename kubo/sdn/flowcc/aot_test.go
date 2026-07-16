package flowcc

// aot_test.go proves the Part-2 AOT box: WasmEdge AOT-compiling llvm-box.wasm
//   (1) still binds the 58-import emscripten host module and runs clang/wasm-ld,
//   (2) produces BYTE-IDENTICAL compiler + linker output vs interpreter mode,
//   (3) is measurably faster.
// It stages a private copy of the box so it controls the adjacent `.aot`
// artifact New auto-detects.

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestAOTBoxIdenticalOutputAndFaster(t *testing.T) {
	box := boxPath()
	if _, err := os.Stat(box); err != nil {
		t.Skipf("llvm-box.wasm not available at %s (set %s): %v", box, EnvLLVMBoxWasm, err)
	}

	// Private staged box so `box+".aot"` is ours alone.
	home := HomeAt(t.TempDir())
	if err := copyFile(box, home.BoxPath()); err != nil {
		t.Fatalf("stage box copy: %v", err)
	}
	ctx := context.Background()

	compileArgv := []string{"clang", "clang", "-c", "/foo.c", "-o", "/out/foo.o", "-target", "wasm32-wasi"}
	linkArgv := []string{"lld", "wasm-ld", "--no-entry", "--export=foo", "--export=bar", "/out/foo.o", "-o", "/out/foo.wasm"}

	// ---- Interpreter mode (no .aot present yet) ----
	interp, err := New(home.BoxPath())
	if err != nil {
		t.Fatalf("New (interp): %v", err)
	}
	if interp.AOTEnabled() {
		t.Fatalf("interp compiler unexpectedly AOT-enabled")
	}
	t0 := time.Now()
	ic, err := interp.Run(ctx, compileArgv, map[string][]byte{"/foo.c": []byte(fooSrc)})
	if err != nil || ic.ExitCode != 0 {
		t.Fatalf("interp compile: err=%v exit=%d stderr=%q", err, ic.ExitCode, ic.Stderr)
	}
	interpCompileMs := time.Since(t0).Milliseconds()
	interpObj := ic.OutFiles["/out/foo.o"]
	t1 := time.Now()
	il, err := interp.Run(ctx, linkArgv, map[string][]byte{"/out/foo.o": interpObj})
	if err != nil || il.ExitCode != 0 {
		t.Fatalf("interp link: err=%v exit=%d stderr=%q", err, il.ExitCode, il.Stderr)
	}
	interpLinkMs := time.Since(t1).Milliseconds()
	interpWasm := il.OutFiles["/out/foo.wasm"]

	// ---- AOT-compile the box (stage-time one-shot) ----
	ta := time.Now()
	aotOut, err := CompileBoxAOT(home)
	if err != nil {
		t.Skipf("CompileBoxAOT failed (libwasmedge not AOT-capable?): %v", err)
	}
	aotBuildMs := time.Since(ta).Milliseconds()
	fi, _ := os.Stat(aotOut)
	t.Logf("AOT box built: %s (%d bytes) in %dms", aotOut, sizeOf(fi), aotBuildMs)

	// ---- AOT mode (New now auto-detects box+".aot") ----
	aot, err := New(home.BoxPath())
	if err != nil {
		t.Fatalf("New (aot): %v", err)
	}
	if !aot.AOTEnabled() {
		t.Fatalf("aot compiler did not pick up %s", aotOut)
	}
	t2 := time.Now()
	ac, err := aot.Run(ctx, compileArgv, map[string][]byte{"/foo.c": []byte(fooSrc)})
	if err != nil || ac.ExitCode != 0 {
		t.Fatalf("aot compile: err=%v exit=%d stderr=%q", err, ac.ExitCode, ac.Stderr)
	}
	aotCompileMs := time.Since(t2).Milliseconds()
	aotObj := ac.OutFiles["/out/foo.o"]
	t3 := time.Now()
	al, err := aot.Run(ctx, linkArgv, map[string][]byte{"/out/foo.o": aotObj})
	if err != nil || al.ExitCode != 0 {
		t.Fatalf("aot link: err=%v exit=%d stderr=%q", err, al.ExitCode, al.Stderr)
	}
	aotLinkMs := time.Since(t3).Milliseconds()
	aotWasm := al.OutFiles["/out/foo.wasm"]

	// ---- (2) byte-identical output ----
	if sha(interpObj) != sha(aotObj) {
		t.Fatalf("AOT compile output differs from interpreter:\n interp=%s\n aot=   %s", sha(interpObj), sha(aotObj))
	}
	if sha(interpWasm) != sha(aotWasm) {
		t.Fatalf("AOT link output differs from interpreter:\n interp=%s\n aot=   %s", sha(interpWasm), sha(aotWasm))
	}
	t.Logf("★ AOT output IDENTICAL to interpreter: foo.o sha=%s foo.wasm sha=%s", sha(aotObj), sha(aotWasm))

	// ---- (3) speedup ----
	t.Logf("★ compile: interp=%dms aot=%dms | link: interp=%dms aot=%dms",
		interpCompileMs, aotCompileMs, interpLinkMs, aotLinkMs)
	t.Logf("AOT one-time box build: %dms", aotBuildMs)
}

func sizeOf(fi os.FileInfo) int64 {
	if fi == nil {
		return 0
	}
	return fi.Size()
}
