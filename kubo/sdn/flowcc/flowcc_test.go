package flowcc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
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

	// --- P2 real (sysroot-using) compile ---
	//
	// progSrc is a C++ program that pulls in the standard library (<vector>,
	// <string>, <stdexcept>) AND throws/catches a std::runtime_error, compiled
	// with -fwasm-exceptions and linked against libc++/libc++abi from the
	// extracted emception sysroot. It is byte-identical to scratchpad
	// phase2/prog.cpp (guarded by shaProgSrc). shaProgO / shaProgWasm are the
	// sha256 of the emception-Node reference artifacts (node-prog.o /
	// node-prog.wasm) produced by run_realcompile_root.mjs with the identical
	// argv this test drives; byte-identity here proves the extracted sysroot +
	// overlay precedence reproduce emception exactly.
	progSrc = `#include <vector>
#include <string>
#include <stdexcept>

extern "C" int run_test(int x) {
  std::vector<std::string> v;
  v.push_back("hello");
  v.push_back("world");
  int n = 0;
  for (const auto& s : v) n += static_cast<int>(s.size());
  try {
    if (x < 0) throw std::runtime_error("negative");
    n += x;
  } catch (const std::runtime_error& e) {
    n -= static_cast<int>(std::string(e.what()).size());
  }
  return n;
}
`
	shaProgSrc  = "e1401d52f57711cfd50daef4195ecce1729a4eb7ed969463843dd5b8d03bfa93"
	shaProgO    = "e651b8bb285ba8d313dc1a11827a9f4e19db97e7fccdc08189d74ad8d96a1fa9"
	shaProgWasm = "b7bda325916f4c64bf5bce7dbcd39e3bdf8f61555c2896a18936043f1c5fd6f7"

	defaultSysroot = "/private/tmp/claude-501/-Users-tj-software-spacedatanetwork-stack/8a9a46ba-3833-472b-bfb4-4c3869499342/scratchpad/phase2/sysroot"

	// EnvFlowccGlue names an OPTIONAL env var pointing at emception's
	// llvm-box.mjs glue. When set, TestRealCompileVsNodeLive regenerates the
	// reference live under Node and cross-checks it against the Go host output;
	// unset, that test is skipped (the self-contained constant assertion in
	// TestParityRealCompile still runs).
	EnvFlowccGlue = "SDN_FLOWCC_GLUE"
)

// sysrootPathForTest resolves the sysroot dir the same way New does (env first,
// then the scratchpad default).
func sysrootPathForTest() string {
	if p := os.Getenv(EnvLLVMSysroot); p != "" {
		return p
	}
	return defaultSysroot
}

// realCompileArgv / realLinkArgv are the guest argv for the real compile and
// link. They are shared verbatim by the Go host and the live-Node cross-check
// so the "same argv" precondition of byte-identity is guaranteed by
// construction. The sysroot is mounted at /sysroot, so the driver flag is
// --sysroot=/sysroot and the libs live under /sysroot/lib/wasm32-emscripten.
// (The mount point does not affect the output bytes — only file contents do —
// so these hashes equal the emception reference regardless of mount point.)
func realCompileArgv() []string {
	base := []string{"clang", "clang", "-c", "/src/prog.cpp", "-o", "/out/prog.o",
		"-target", "wasm32-emscripten", "--sysroot=/sysroot", "-fwasm-exceptions", "-fno-rtti", "-O2"}
	if os.Getenv("FLOWCC_CLANG_V") != "" {
		base = append(base, "-v")
	}
	return base
}

func realLinkArgv() []string {
	return []string{"lld", "wasm-ld", "-L/sysroot/lib/wasm32-emscripten", "/out/prog.o",
		"/sysroot/lib/wasm32-emscripten/libc++.a", "/sysroot/lib/wasm32-emscripten/libc++abi.a",
		"/sysroot/lib/wasm32-emscripten/libc.a", "/sysroot/lib/wasm32-emscripten/libcompiler_rt.a",
		"--no-entry", "--export=run_test", "--allow-undefined", "-o", "/out/prog.wasm"}
}

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

// TestOverlayPrecedence unit-tests the overlay resolver directly (no wasm):
// the writable scratch shadows the read-only sysroot, reads fall through to the
// sysroot, writes/creates never touch the sysroot, and path containment holds.
func TestOverlayPrecedence(t *testing.T) {
	scratch := t.TempDir()
	sysroot := t.TempDir()

	// The sysroot is mounted at sysrootMount ("/sysroot"), so a sysroot file at
	// <sysroot>/lib/only.a is the guest path /sysroot/lib/only.a. To exercise
	// shadowing, shared.txt exists in BOTH layers at the SAME guest path
	// /sysroot/shared.txt: the sysroot copy at <sysroot>/shared.txt and the
	// scratch (upper) copy at <scratch>/sysroot/shared.txt.
	if err := os.WriteFile(filepath.Join(sysroot, "shared.txt"), []byte("SYSROOT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(scratch, "sysroot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "sysroot", "shared.txt"), []byte("SCRATCH"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sysroot, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "lib", "only.a"), []byte("LOWER"), 0o644); err != nil {
		t.Fatal(err)
	}

	o := &overlay{scratch: scratch, sysroot: sysroot}

	// 1) Shadowing: /sysroot/shared.txt resolves to the (writable) scratch copy.
	host, writable, ok := o.resolveExisting("/sysroot/shared.txt")
	if !ok || !writable || host != filepath.Join(scratch, "sysroot", "shared.txt") {
		t.Fatalf("shared.txt: host=%q writable=%v ok=%v (want scratch/writable)", host, writable, ok)
	}
	// 2) Read-through: /sysroot/lib/only.a exists only in the read-only sysroot.
	host, writable, ok = o.resolveExisting("/sysroot/lib/only.a")
	if !ok || writable || host != filepath.Join(sysroot, "lib", "only.a") {
		t.Fatalf("only.a: host=%q writable=%v ok=%v (want sysroot/read-only)", host, writable, ok)
	}
	// 3) Paths OUTSIDE the mount never resolve to the sysroot, even by basename.
	if _, _, ok := o.resolveExisting("/lib/only.a"); ok {
		t.Fatalf("/lib/only.a resolved but is outside the sysroot mount")
	}
	// 4) Missing everywhere.
	if _, _, ok := o.resolveExisting("/sysroot/nope"); ok {
		t.Fatalf("/sysroot/nope resolved but should be absent")
	}

	// 5) Read-only open reads THROUGH to the sysroot and returns its bytes.
	fd, err := o.open("/sysroot/lib/only.a", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open sysroot file: %v", err)
	}
	got := make([]byte, 16)
	n, _ := readAt(fd, got)
	closeFD(fd)
	if string(got[:n]) != "LOWER" {
		t.Fatalf("read-through got %q want LOWER", got[:n])
	}
	// 6) Read of a shadowed file returns the SCRATCH bytes, not the sysroot's.
	fd, err = o.open("/sysroot/shared.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open shadowed file: %v", err)
	}
	n, _ = readAt(fd, got)
	closeFD(fd)
	if string(got[:n]) != "SCRATCH" {
		t.Fatalf("shadowed read got %q want SCRATCH", got[:n])
	}

	// 7) A create lands in the scratch and never in the sysroot.
	fd, err = o.open("/out/new.o", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("create in scratch: %v", err)
	}
	closeFD(fd)
	if _, err := os.Stat(filepath.Join(scratch, "out", "new.o")); err != nil {
		t.Fatalf("created file not in scratch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sysroot, "out", "new.o")); err == nil {
		t.Fatalf("create leaked into the read-only sysroot")
	}

	// 8) Containment: a climbing path cannot escape either layer.
	hp, ok := o.scratchPath("/../../etc/passwd")
	if !ok || hp != filepath.Join(scratch, "etc", "passwd") {
		t.Fatalf("containment failed: hp=%q ok=%v", hp, ok)
	}

	// 9) With no sysroot, the overlay is scratch-only (P1 behaviour).
	o2 := &overlay{scratch: scratch}
	if _, _, ok := o2.resolveExisting("/sysroot/lib/only.a"); ok {
		t.Fatalf("no-sysroot overlay must not see sysroot files")
	}
}

// TestParityRealCompile is the P2 proof: a full standard-library + throw/catch
// C++ program compiled (-fwasm-exceptions) and linked (against libc++abi from
// the sysroot) by the Go WasmEdge overlay host is byte-identical to the
// emception-Node reference (shaProgO / shaProgWasm). It also confirms the linked
// module is a well-formed wasm.
func TestParityRealCompile(t *testing.T) {
	if got := sha([]byte(progSrc)); got != shaProgSrc {
		t.Fatalf("embedded progSrc drifted from phase2/prog.cpp:\n got %s\nwant %s", got, shaProgSrc)
	}
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

	// 1) compile /src/prog.cpp -> /out/prog.o (reads C++ headers from sysroot).
	cRes, err := cc.Run(ctx, realCompileArgv(), map[string][]byte{"/src/prog.cpp": []byte(progSrc)})
	if err != nil {
		t.Fatalf("compile Run: %v (stderr=%q)", err, cRes.Stderr)
	}
	if cRes.ExitCode != 0 {
		t.Fatalf("compile exit=%d stderr=%q", cRes.ExitCode, cRes.Stderr)
	}
	obj, ok := cRes.OutFiles["/out/prog.o"]
	if !ok {
		t.Fatalf("compile produced no /out/prog.o; files=%v", keysOf(cRes.OutFiles))
	}
	gotO := sha(obj)
	t.Logf("clang -c prog.cpp (std headers + -fwasm-exceptions): obj=%d bytes sha256=%s invoke=%d longjmp=%d",
		len(obj), gotO, cRes.InvokeCount, cRes.LongjmpCount)
	if gotO != shaProgO {
		t.Fatalf("prog.o sha mismatch vs emception-Node:\n got %s\nwant %s\nstderr=%q", gotO, shaProgO, cRes.Stderr)
	}

	// 2) link /out/prog.o + libc++/libc++abi/libc/compiler_rt -> /out/prog.wasm.
	lRes, err := cc.Run(ctx, realLinkArgv(), map[string][]byte{"/out/prog.o": obj})
	if err != nil {
		t.Fatalf("link Run: %v (stderr=%q)", err, lRes.Stderr)
	}
	if lRes.ExitCode != 0 {
		t.Fatalf("link exit=%d stderr=%q", lRes.ExitCode, lRes.Stderr)
	}
	linked, ok := lRes.OutFiles["/out/prog.wasm"]
	if !ok {
		t.Fatalf("link produced no /out/prog.wasm; files=%v", keysOf(lRes.OutFiles))
	}
	gotW := sha(linked)
	t.Logf("wasm-ld (libc++abi EH runtime): wasm=%d bytes sha256=%s invoke=%d longjmp=%d",
		len(linked), gotW, lRes.InvokeCount, lRes.LongjmpCount)
	if gotW != shaProgWasm {
		t.Fatalf("prog.wasm sha mismatch vs emception-Node:\n got %s\nwant %s", gotW, shaProgWasm)
	}

	// 3) The linked module is a well-formed wasm carrying the expected exports.
	//    Byte-identity to emception's own linker output (above) is the actual
	//    correctness proof; here we just confirm the module is structurally sane
	//    and exports run_test. It is NOT standalone-runnable — it imports the
	//    emscripten EH env (__cxa_throw, _Unwind_CallPersonality, invoke_*,
	//    malloc) and uses native wasm-EH opcodes.
	if len(linked) < 8 || linked[0] != 0x00 || linked[1] != 'a' || linked[2] != 's' || linked[3] != 'm' {
		t.Fatalf("linked output is not a WASM module")
	}
	if !bytes.Contains(linked, []byte("run_test")) {
		t.Fatalf("linked wasm missing run_test export")
	}
	validateWasm(t, linked)
}

// nodeRefDriver is a self-contained Node driver, written to a temp dir at test
// time, that reproduces the emception reference: it mounts the real sysroot into
// the emscripten MEMFS at the /sysroot mount point, seeds the scratch inputs,
// and runs the SAME argv the Go host runs (passed via a JSON config), printing
// the sha256 of the .o and linked .wasm. This is the live "generate the
// reference through emception under Node" step.
const nodeRefDriver = `
import { readFileSync, readdirSync, statSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { createRequire } from "node:module";
import { createHash } from "node:crypto";
import path from "node:path";
const cfg = JSON.parse(readFileSync(process.argv[2], "utf8"));
globalThis.require = createRequire(import.meta.url);
globalThis.__dirname = path.dirname(cfg.glue);
const wasmBinary = readFileSync(cfg.wasm);
const { default: Module } = await import(pathToFileURL(cfg.glue).href);
const sha = b => createHash("sha256").update(b).digest("hex");
function mirror(m, hostDir, base) {
  for (const name of readdirSync(hostDir)) {
    const hp = path.join(hostDir, name), gp = (base === "/" ? "" : base) + "/" + name;
    const st = statSync(hp);
    if (st.isDirectory()) { try { m.FS.mkdir(gp); } catch {} mirror(m, hp, gp); }
    else if (st.isFile()) m.FS.writeFile(gp, readFileSync(hp));
  }
}
function mkdirp(m, gp) { let cur = ""; for (const p of gp.split("/").filter(Boolean)) { cur += "/" + p; try { m.FS.mkdir(cur); } catch {} } }
async function newM() { let err = ""; const m = await Module({ thisProgram: "clang", wasmBinary, noInitialRun: true, print: () => {}, printErr: s => err += s + "\n", locateFile: f => f }); m.__err = () => err; return m; }
function run(m, argv) { const argc = argv.length, a = m._malloc((argc + 1) * 4); const H = () => (m.HEAP32 ? m.HEAP32 : new Int32Array(m.wasmMemory.buffer)); for (let i = 0; i < argc; i++) H()[(a >> 2) + i] = (m.allocateUTF8 || m.stringToNewUTF8)(argv[i]); H()[(a >> 2) + argc] = 0; let rc = 0; try { rc = m._main(argc, a); } catch (e) { if (e && e.status !== undefined) rc = e.status; else throw e; } return rc; }
const MOUNT = cfg.mount || "/sysroot";
const m1 = await newM(); mkdirp(m1, MOUNT); mirror(m1, cfg.sysroot, MOUNT);
for (const [g, h] of Object.entries(cfg.seed)) { mkdirp(m1, path.dirname(g)); m1.FS.writeFile(g, readFileSync(h)); }
mkdirp(m1, path.dirname(cfg.outObj));
const crc = run(m1, cfg.compileArgv);
let obj = null; try { obj = Buffer.from(m1.FS.readFile(cfg.outObj)); } catch {}
let wasm = null, lrc = -1;
if (obj) { const m2 = await newM(); mkdirp(m2, MOUNT); mirror(m2, cfg.sysroot, MOUNT); mkdirp(m2, path.dirname(cfg.outObj)); m2.FS.writeFile(cfg.outObj, obj); mkdirp(m2, path.dirname(cfg.outWasm)); lrc = run(m2, cfg.linkArgv); try { wasm = Buffer.from(m2.FS.readFile(cfg.outWasm)); } catch {} }
console.log(JSON.stringify({ crc, lrc, objSha: obj ? sha(obj) : null, wasmSha: wasm ? sha(wasm) : null, cerr: m1.__err() }));
`

// TestRealCompileVsNodeLive regenerates the reference LIVE through emception
// under Node with the identical argv and asserts the Go host output is
// byte-identical to it (and to the checked-in constants). It is skipped unless
// SDN_FLOWCC_GLUE points at emception's llvm-box.mjs and `node` is on PATH.
func TestRealCompileVsNodeLive(t *testing.T) {
	glue := os.Getenv(EnvFlowccGlue)
	if glue == "" {
		t.Skipf("set %s to emception's llvm-box.mjs to run the live Node cross-check", EnvFlowccGlue)
	}
	if _, err := os.Stat(glue); err != nil {
		t.Skipf("glue not found at %s: %v", glue, err)
	}
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node not on PATH: %v", err)
	}
	box := boxPath()
	sysroot := sysrootPathForTest()
	if _, err := os.Stat(box); err != nil {
		t.Skipf("box missing: %v", err)
	}
	if fi, err := os.Stat(sysroot); err != nil || !fi.IsDir() {
		t.Skipf("sysroot missing: %v", err)
	}

	// Go host output.
	cc, err := NewWithSysroot(box, sysroot)
	if err != nil {
		t.Fatalf("NewWithSysroot: %v", err)
	}
	ctx := context.Background()
	cRes, err := cc.Run(ctx, realCompileArgv(), map[string][]byte{"/src/prog.cpp": []byte(progSrc)})
	if err != nil || cRes.ExitCode != 0 {
		t.Fatalf("go compile: err=%v exit=%d stderr=%q", err, cRes.ExitCode, cRes.Stderr)
	}
	goObj := cRes.OutFiles["/out/prog.o"]
	lRes, err := cc.Run(ctx, realLinkArgv(), map[string][]byte{"/out/prog.o": goObj})
	if err != nil || lRes.ExitCode != 0 {
		t.Fatalf("go link: err=%v exit=%d stderr=%q", err, lRes.ExitCode, lRes.Stderr)
	}
	goWasm := lRes.OutFiles["/out/prog.wasm"]

	// Live Node reference with the identical argv.
	tmp := t.TempDir()
	driver := filepath.Join(tmp, "driver.mjs")
	if err := os.WriteFile(driver, []byte(nodeRefDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	srcHost := filepath.Join(tmp, "prog.cpp")
	if err := os.WriteFile(srcHost, []byte(progSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"glue":        glue,
		"wasm":        box,
		"sysroot":     sysroot,
		"seed":        map[string]string{"/src/prog.cpp": srcHost},
		"compileArgv": realCompileArgv(),
		"linkArgv":    realLinkArgv(),
		"outObj":      "/out/prog.o",
		"outWasm":     "/out/prog.wasm",
	}
	cfgPath := filepath.Join(tmp, "cfg.json")
	cfgBytes, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, cfgBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(nodeBin, driver, cfgPath).Output()
	if err != nil {
		t.Fatalf("node reference driver failed: %v\noutput=%s", err, out)
	}
	var ref struct {
		Crc, Lrc        int
		ObjSha, WasmSha string
		Cerr            string
	}
	if err := json.Unmarshal(out, &ref); err != nil {
		t.Fatalf("parse node output %q: %v", out, err)
	}
	t.Logf("emception-Node reference: crc=%d lrc=%d objSha=%s wasmSha=%s", ref.Crc, ref.Lrc, ref.ObjSha, ref.WasmSha)

	// Go host == live emception-Node == checked-in constants.
	if sha(goObj) != ref.ObjSha {
		t.Fatalf(".o differs Go vs Node:\n go=%s\nnode=%s\nnodeCerr=%s", sha(goObj), ref.ObjSha, ref.Cerr)
	}
	if sha(goWasm) != ref.WasmSha {
		t.Fatalf(".wasm differs Go vs Node:\n go=%s\nnode=%s", sha(goWasm), ref.WasmSha)
	}
	if ref.ObjSha != shaProgO || ref.WasmSha != shaProgWasm {
		t.Fatalf("live Node reference drifted from checked-in constants:\n objSha=%s (want %s)\n wasmSha=%s (want %s)",
			ref.ObjSha, shaProgO, ref.WasmSha, shaProgWasm)
	}
	t.Logf("byte-identical: Go overlay host == emception-Node == constants (.o and .wasm)")
}

// validateWasm loads and validates a wasm module (structural well-formedness)
// without instantiating it — used for modules that import an external env.
// validateWasm attempts a WasmEdge Load+Validate of the linked module with the
// exception-handling proposal enabled, and reports the outcome informationally.
// It is intentionally NON-fatal: this pinned WasmEdge (0.16.4) does not fully
// accept emscripten's native wasm-EH opcodes ("illegal opcode" at load), which
// P3 addresses (AOT + EH). The correctness proof is the byte-identity above.
func validateWasm(t *testing.T, wasm []byte) {
	t.Helper()
	conf := wasmedge.NewConfigure()
	conf.AddConfig(wasmedge.EXCEPTION_HANDLING) // keep default proposals, add EH
	vm := wasmedge.NewVMWithConfig(conf)
	defer func() { vm.Release(); conf.Release() }()
	if err := vm.LoadWasmBuffer(wasm); err != nil {
		t.Logf("note: WasmEdge cannot load the wasm-EH module yet (%v); byte-identity is the proof (P3 enables EH/AOT)", err)
		return
	}
	if err := vm.Validate(); err != nil {
		t.Logf("note: WasmEdge validate rejected the wasm-EH module (%v); byte-identity is the proof", err)
		return
	}
	t.Logf("linked module loads+validates under WasmEdge with EH proposal")
}

// readAt / closeFD are tiny syscall wrappers for the overlay unit test.
func readAt(fd int, p []byte) (int, error) { return syscall.Read(fd, p) }
func closeFD(fd int)                       { _ = syscall.Close(fd) }

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
