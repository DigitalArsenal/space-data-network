package flowcc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"testing"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// Canonical inputs + expected hashes from the proven POC (scratchpad phase2).
// fooSrc is byte-identical to fsroot/foo.c (65 bytes). shaFooO / shaFooWasm are
// the sha256 of we-foo.o / we-foo.wasm, which are byte-identical to the Node
// (emception) reference artifacts node-foo.o / node-foo.wasm.
const (
	fooSrc     = "int foo(int x){ return x+1; }\nint bar(int x){ return foo(x)*2; }\n"
	shaFooO    = "62a7a6d4f0ad88b9a89cab65ad7ab3abb495c599867a367c5abfc1c83890cd9e"
	shaFooWasm = "3101328f6f590b67aa49ff42754a66851accc55aa6f241ae6b749bf2970e2a6a"

	// tplSrc is byte-identical to fsroot/tpl.cpp (253 bytes); a C++ template +
	// exception unit that exercises clang's exception codegen (and its own
	// internal emscripten SjLj invoke_* path). shaTplSrc guards the embedded
	// copy; shaTplO is the reference we-tpl.o produced with -fwasm-exceptions.
	tplSrc = `struct E { int v; };
template<class T> T addN(T a, T b){ return a + b; }
extern "C" int compute(int x){
  if (x < 0) throw E{ x };
  return addN<int>(x, 100);
}
extern "C" int safe(int x){
  try { return compute(x); } catch (E& e) { return e.v - 1; }
}
`
	shaTplSrc = "ec2dac725b3ddfc898cd6970ef7bfa9a6e986b5cb9a0c3a1ca9a2a417ba8117d"
	shaTplO   = "32da7c92a74a05ecd643da1678d2c73d019df3b380c4a8feb2d47232cc8b5694"

	defaultBox = "/private/tmp/claude-501/-Users-tj-software-spacedatanetwork-stack/8a9a46ba-3833-472b-bfb4-4c3869499342/scratchpad/phase2/llvm-box.wasm"
)

func boxPath() string {
	if p := os.Getenv(EnvLLVMBoxWasm); p != "" {
		return p
	}
	return defaultBox
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func keysOf(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// TestParityClangAndLLD proves that llvm-box.wasm, hosted by this Go WasmEdge
// shim, reproduces byte-identical compiler + linker output vs the proven POC:
//   - clang -c foo.c -o foo.o -target wasm32-wasi  => sha256 == shaFooO
//   - wasm-ld --no-entry --export=foo --export=bar => sha256 == shaFooWasm
//
// and that the linked module actually RUNS (foo(41)=42, bar(20)=42).
func TestParityClangAndLLD(t *testing.T) {
	box := boxPath()
	if _, err := os.Stat(box); err != nil {
		t.Skipf("llvm-box.wasm not available at %s (set %s): %v", box, EnvLLVMBoxWasm, err)
	}

	cc, err := New(box)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// 1) clang -c foo.c -> /out/foo.o
	cRes, err := cc.Run(ctx,
		[]string{"clang", "clang", "-c", "/foo.c", "-o", "/out/foo.o", "-target", "wasm32-wasi"},
		map[string][]byte{"/foo.c": []byte(fooSrc)})
	if err != nil {
		t.Fatalf("compile Run: %v (stderr=%q)", err, cRes.Stderr)
	}
	if cRes.ExitCode != 0 {
		t.Fatalf("compile exit=%d stderr=%q", cRes.ExitCode, cRes.Stderr)
	}
	obj, ok := cRes.OutFiles["/out/foo.o"]
	if !ok {
		t.Fatalf("compile produced no /out/foo.o; files=%v", keysOf(cRes.OutFiles))
	}
	gotO := sha(obj)
	t.Logf("clang -c: obj=%d bytes sha256=%s invoke=%d longjmp=%d stderr=%q",
		len(obj), gotO, cRes.InvokeCount, cRes.LongjmpCount, cRes.Stderr)
	if gotO != shaFooO {
		t.Fatalf("foo.o sha mismatch:\n got %s\nwant %s", gotO, shaFooO)
	}

	// 2) wasm-ld foo.o -> /out/foo.wasm
	lRes, err := cc.Run(ctx,
		[]string{"lld", "wasm-ld", "--no-entry", "--export=foo", "--export=bar", "/out/foo.o", "-o", "/out/foo.wasm"},
		map[string][]byte{"/out/foo.o": obj})
	if err != nil {
		t.Fatalf("link Run: %v (stderr=%q)", err, lRes.Stderr)
	}
	if lRes.ExitCode != 0 {
		t.Fatalf("link exit=%d stderr=%q", lRes.ExitCode, lRes.Stderr)
	}
	linked, ok := lRes.OutFiles["/out/foo.wasm"]
	if !ok {
		t.Fatalf("link produced no /out/foo.wasm; files=%v", keysOf(lRes.OutFiles))
	}
	gotW := sha(linked)
	t.Logf("wasm-ld: wasm=%d bytes sha256=%s invoke=%d longjmp=%d stderr=%q",
		len(linked), gotW, lRes.InvokeCount, lRes.LongjmpCount, lRes.Stderr)
	if gotW != shaFooWasm {
		t.Fatalf("foo.wasm sha mismatch:\n got %s\nwant %s", gotW, shaFooWasm)
	}

	// 3) The linked module must run: foo(41)=42, bar(20)=42.
	if got := runExport(t, linked, "foo", 41); got != 42 {
		t.Fatalf("foo(41)=%d want 42", got)
	}
	if got := runExport(t, linked, "bar", 20); got != 42 {
		t.Fatalf("bar(20)=%d want 42", got)
	}
	t.Logf("linked module runs: foo(41)=42 bar(20)=42 OK")
}

// TestParityTplExceptions proves byte-identical parity for a C++ template +
// exception translation unit (clang -c tpl.cpp -fwasm-exceptions), which
// exercises clang's exception codegen and its internal SjLj invoke_* path:
//
//	sha256(tpl.o) == shaTplO (== we-tpl.o / node-tpl.o).
func TestParityTplExceptions(t *testing.T) {
	if got := sha([]byte(tplSrc)); got != shaTplSrc {
		t.Fatalf("embedded tplSrc drifted from fsroot/tpl.cpp:\n got %s\nwant %s", got, shaTplSrc)
	}
	box := boxPath()
	if _, err := os.Stat(box); err != nil {
		t.Skipf("llvm-box.wasm not available at %s (set %s): %v", box, EnvLLVMBoxWasm, err)
	}
	cc, err := New(box)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cc.Run(context.Background(),
		[]string{"clang", "clang", "-c", "/tpl.cpp", "-o", "/out/tpl.o", "-target", "wasm32-wasi", "-fwasm-exceptions"},
		map[string][]byte{"/tpl.cpp": []byte(tplSrc)})
	if err != nil {
		t.Fatalf("tpl compile Run: %v (stderr=%q)", err, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("tpl compile exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	obj, ok := res.OutFiles["/out/tpl.o"]
	if !ok {
		t.Fatalf("tpl compile produced no /out/tpl.o; files=%v", keysOf(res.OutFiles))
	}
	got := sha(obj)
	t.Logf("clang -c tpl.cpp -fwasm-exceptions: obj=%d bytes sha256=%s invoke=%d longjmp=%d",
		len(obj), got, res.InvokeCount, res.LongjmpCount)
	if got != shaTplO {
		t.Fatalf("tpl.o sha mismatch:\n got %s\nwant %s", got, shaTplO)
	}
}

// runExport instantiates a self-contained wasm (no imports) and calls fn(arg).
func runExport(t *testing.T, wasm []byte, fn string, arg int32) int32 {
	t.Helper()
	conf := wasmedge.NewConfigure()
	vm := wasmedge.NewVMWithConfig(conf)
	defer func() { vm.Release(); conf.Release() }()
	if err := vm.LoadWasmBuffer(wasm); err != nil {
		t.Fatalf("load linked wasm: %v", err)
	}
	if err := vm.Validate(); err != nil {
		t.Fatalf("validate linked wasm: %v", err)
	}
	if err := vm.Instantiate(); err != nil {
		t.Fatalf("instantiate linked wasm: %v", err)
	}
	res, err := vm.Execute(fn, arg)
	if err != nil {
		t.Fatalf("execute %s: %v", fn, err)
	}
	return wasmrtInt32(res[0])
}
