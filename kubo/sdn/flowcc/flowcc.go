// Package flowcc hosts emception's llvm-box.wasm (a single WASM artifact
// containing clang + wasm-ld, selected by argv[0]) inside the SDN kubo node's
// own WasmEdge runtime, and reproduces byte-identical compiler output.
//
// It is a faithful Go port of the proven Stage-2 C host shim (scratchpad
// phase2/emshim2.c) built against the WasmEdge 0.16.4 C SDK. The port uses the
// pure-Go WasmEdge binding (github.com/second-state/WasmEdge-go, already the
// kubo dependency) rather than cgo, because that binding cleanly exposes every
// primitive the shim needs:
//
//   - host-module registration with N host functions (wasmedge.NewModule /
//     Module.AddFunction);
//   - in-callback linear-memory read/write (CallingFrame.GetMemoryByIndex →
//     Memory.GetData/SetData/GrowPage);
//   - arbitrary export invocation from the host (VM.Execute) and, crucially,
//     re-entrant invocation from INSIDE a host callback via the executor
//     (CallingFrame.GetExecutor + GetModule + Module.FindFunction/FindTable,
//     Table.GetData → FuncRef.GetRef, Executor.Invoke) — the exact mechanism
//     emscripten SjLj's invoke_* trampolines require.
//
// The host module is named "a" and registers all 58 minified imports that
// llvm-box.wasm needs; only ~11 carry real behavior (fd_write→captured
// stdout/host file, path __syscall_*→host FS, resize_heap→grow memory,
// memcpy_big→memmove, fstat64→real-stat-or-char-device), the rest are
// present-but-stub/trap, mirroring emshim2.c exactly. SjLj is implemented by
// having emscripten_throw_longjmp trap the guest; the invoke_* trampoline
// catches the trap, runs stackRestore + setThrew, and returns 0 — again
// exactly as emshim2.c does.
//
// P1 scope: the FS is backed by a per-Run temp directory (mirroring
// emshim2.c's host-rooted FS). Real sysroot mounting for full compiles is P2.
package flowcc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// Minified guest export names for THIS llvm-box.wasm build (identical to the
// names emshim2.c drives; they are stable for the pinned 57.9 MB artifact).
const (
	expCtors       = "fa" // ___wasm_call_ctors
	expMalloc      = "ka" // _malloc
	expMain        = "ga" // _main
	expMemory      = "ea" // exported linear memory
	expTable       = "ia" // indirect-function table (SjLj funcref source)
	expStackSave   = "na" // stackSave
	expStackRestor = "oa" // stackRestore
	expSetThrew    = "ma" // setThrew
)

// hostModuleName is the minified emscripten import module name.
const hostModuleName = "a"

// EnvLLVMBoxWasm names the environment variable pointing at llvm-box.wasm when
// New is called with an empty path.
const EnvLLVMBoxWasm = "SDN_LLVM_BOX_WASM"

// Compiler holds the loaded llvm-box.wasm bytes. It is safe for concurrent
// use: each Run builds a fresh, isolated VM (matching the "fresh process per
// invocation" model of emshim2.c and the Node reference driver), so no guest
// state leaks between calls.
type Compiler struct {
	wasm []byte
}

// Result is the outcome of a single Run.
type Result struct {
	// ExitCode is the guest _main return value, or the code passed to
	// exit()/proc_exit() if the guest exited.
	ExitCode int
	// Stdout / Stderr are the bytes the guest wrote to fd 1 / fd 2.
	Stdout []byte
	Stderr []byte
	// OutFiles maps every regular file present under the run root after
	// execution to its contents, keyed by guest-absolute path (leading "/").
	// Input files are included; the caller selects the outputs it wants.
	OutFiles map[string][]byte
	// Diagnostic counters (invoke_* trampoline calls / longjmp unwinds).
	InvokeCount  int64
	LongjmpCount int64
}

// New loads llvm-box.wasm from llvmBoxPath. If llvmBoxPath is empty it falls
// back to the SDN_LLVM_BOX_WASM environment variable. Embedding/shipping the
// artifact is P2/P4 and intentionally out of scope here.
func New(llvmBoxPath string) (*Compiler, error) {
	if llvmBoxPath == "" {
		llvmBoxPath = os.Getenv(EnvLLVMBoxWasm)
	}
	if llvmBoxPath == "" {
		return nil, fmt.Errorf("flowcc: no llvm-box.wasm path (pass a path or set %s)", EnvLLVMBoxWasm)
	}
	b, err := os.ReadFile(llvmBoxPath)
	if err != nil {
		return nil, fmt.Errorf("flowcc: read llvm-box.wasm: %w", err)
	}
	if len(b) < 8 || b[0] != 0x00 || b[1] != 'a' || b[2] != 's' || b[3] != 'm' {
		return nil, errors.New("flowcc: file is not a WASM module")
	}
	return &Compiler{wasm: b}, nil
}

// Run executes one tool invocation. args is the full guest argv (args[0]
// selects the tool: "clang" → clang, "lld" → wasm-ld). inFiles seeds the run
// root keyed by guest-absolute path (e.g. "/foo.c"). Any missing parent
// directory of an O_CREAT open is created automatically so callers need not
// pre-mkdir output dirs.
//
// ctx cancellation is honored between the coarse execution phases (ctors /
// malloc / main); the guest _main call itself is synchronous and not
// interruptible mid-instruction in this P1 port.
func (c *Compiler) Run(ctx context.Context, args []string, inFiles map[string][]byte) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(args) == 0 {
		return Result{}, errors.New("flowcc: empty args")
	}

	root, err := os.MkdirTemp("", "flowcc-root-")
	if err != nil {
		return Result{}, fmt.Errorf("flowcc: mktemp: %w", err)
	}
	defer os.RemoveAll(root)

	if err := seedRoot(root, inFiles); err != nil {
		return Result{}, err
	}

	rs := &runState{root: root, verbose: os.Getenv("FLOWCC_VERBOSE") != ""}

	conf := wasmedge.NewConfigure() // bare config, exactly like emshim2.c
	vm := wasmedge.NewVMWithConfig(conf)
	defer func() { vm.Release(); conf.Release() }()

	host := buildHostModule(rs)
	defer host.Release()
	if err := vm.RegisterModule(host); err != nil {
		return Result{}, fmt.Errorf("flowcc: register host module: %w", err)
	}

	if err := vm.LoadWasmBuffer(c.wasm); err != nil {
		return Result{}, fmt.Errorf("flowcc: load: %w", err)
	}
	if err := vm.Validate(); err != nil {
		return Result{}, fmt.Errorf("flowcc: validate: %w", err)
	}
	if err := vm.Instantiate(); err != nil {
		return Result{}, fmt.Errorf("flowcc: instantiate: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// Reactor-entry sequence (emshim2.c main): ctors → malloc argv → _main.
	if _, err := vm.Execute(expCtors); err != nil {
		return Result{}, fmt.Errorf("flowcc: %s (ctors): %w", expCtors, err)
	}

	active := vm.GetActiveModule()
	if active == nil {
		return Result{}, errors.New("flowcc: no active module after instantiate")
	}
	mem := active.FindMemory(expMemory)
	if mem == nil {
		return Result{}, fmt.Errorf("flowcc: memory export %q not found", expMemory)
	}

	// Lay out argv: [ptr0..ptrN-1, NULL] then the NUL-terminated strings.
	argc := len(args)
	var strBytes uint32
	for _, a := range args {
		strBytes += uint32(len(a)) + 1
	}
	need := strBytes + uint32(argc+1)*4 + 16

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	mres, err := vm.Execute(expMalloc, int32(need))
	if err != nil {
		return Result{}, fmt.Errorf("flowcc: %s (malloc): %w", expMalloc, err)
	}
	base := uint32(wasmrtInt32(mres[0]))
	argvArr := base
	strArea := base + uint32(argc+1)*4
	sp := strArea
	for i, a := range args {
		buf := append([]byte(a), 0)
		if e := mem.SetData(buf, uint(sp), uint(len(buf))); e != nil {
			return Result{}, fmt.Errorf("flowcc: write argv[%d]: %w", i, e)
		}
		var p [4]byte
		binary.LittleEndian.PutUint32(p[:], sp)
		if e := mem.SetData(p[:], uint(argvArr+uint32(i)*4), 4); e != nil {
			return Result{}, fmt.Errorf("flowcc: write argvptr[%d]: %w", i, e)
		}
		sp += uint32(len(buf))
	}
	var znull [4]byte
	if e := mem.SetData(znull[:], uint(argvArr+uint32(argc)*4), 4); e != nil {
		return Result{}, fmt.Errorf("flowcc: write argv NULL: %w", e)
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	mainRes, mainErr := vm.Execute(expMain, int32(argc), int32(argvArr))

	res := Result{
		Stdout:       rs.stdout,
		Stderr:       rs.stderr,
		InvokeCount:  rs.invokeCount,
		LongjmpCount: rs.longjmpCount,
	}
	switch {
	case mainErr == nil:
		res.ExitCode = int(wasmrtInt32(mainRes[0]))
	case rs.exited:
		// exit()/proc_exit() traps out of _main by design.
		res.ExitCode = rs.exitCode
	default:
		return res, fmt.Errorf("flowcc: %s (_main) trapped: %w", expMain, mainErr)
	}

	out, err := collectFiles(root)
	if err != nil {
		return res, err
	}
	res.OutFiles = out
	return res, nil
}

// runState is the per-Run mutable state closed over by the host callbacks
// (replaces emshim2.c's process globals; scoping it per Run makes Compiler
// concurrency-safe).
type runState struct {
	root         string
	stdout       []byte
	stderr       []byte
	longjmp      bool
	exited       bool
	exitCode     int
	invokeCount  int64
	longjmpCount int64
	verbose      bool
}

// ---- musl/emscripten O_* flag values (guest ABI, not host) ----
const (
	mOCreat     = 0o100
	mOExcl      = 0o200
	mOTrunc     = 0o1000
	mOAppend    = 0o2000
	mODirectory = 0o200000
)

func mflagsToHost(mf int32) int {
	hf := int(mf) & 3 // access mode shares low bits with host O_RDONLY/WRONLY/RDWR
	if mf&mOCreat != 0 {
		hf |= syscall.O_CREAT
	}
	if mf&mOExcl != 0 {
		hf |= syscall.O_EXCL
	}
	if mf&mOTrunc != 0 {
		hf |= syscall.O_TRUNC
	}
	if mf&mOAppend != 0 {
		hf |= syscall.O_APPEND
	}
	if mf&mODirectory != 0 {
		hf |= syscall.O_DIRECTORY
	}
	return hf
}

// hostPath maps a guest path to a host path under root, with containment.
func (rs *runState) hostPath(guest string) string {
	clean := filepath.Clean(guest)
	joined := filepath.Join(rs.root, clean)
	// Containment guard: never escape the run root.
	if joined != rs.root && !strings.HasPrefix(joined, rs.root+string(os.PathSeparator)) {
		return filepath.Join(rs.root, "__blocked__")
	}
	return joined
}

func seedRoot(root string, inFiles map[string][]byte) error {
	// Provide /dev/null and /dev/urandom. LLVM's RNG opens /dev/urandom at
	// startup and aborts if the open fails; its CONTENT does not affect
	// compiler output (the emception Node reference used a crypto-random
	// MEMFS urandom that differs every run yet produced byte-identical
	// objects), so a deterministic fill is sufficient and keeps our own runs
	// reproducible. Sized to match the POC fsroot (262144 bytes) so any read
	// length succeeds.
	if err := os.MkdirAll(filepath.Join(root, "dev"), 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(root, "dev", "null"), nil, 0o644)
		urnd := make([]byte, 262144)
		for i := range urnd {
			urnd[i] = byte(uint32(i) * 2654435761)
		}
		_ = os.WriteFile(filepath.Join(root, "dev", "urandom"), urnd, 0o644)
	}
	for gpath, data := range inFiles {
		hp := filepath.Join(root, filepath.Clean(gpath))
		if !strings.HasPrefix(hp, root) {
			return fmt.Errorf("flowcc: inFile path escapes root: %q", gpath)
		}
		if err := os.MkdirAll(filepath.Dir(hp), 0o755); err != nil {
			return fmt.Errorf("flowcc: mkdir for %q: %w", gpath, err)
		}
		if err := os.WriteFile(hp, data, 0o644); err != nil {
			return fmt.Errorf("flowcc: write %q: %w", gpath, err)
		}
	}
	return nil
}

func collectFiles(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out["/"+filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("flowcc: collect out-files: %w", err)
	}
	return out, nil
}

func wasmrtInt32(v interface{}) int32 {
	switch x := v.(type) {
	case int32:
		return x
	case uint32:
		return int32(x)
	case int64:
		return int32(x)
	case uint64:
		return int32(x)
	default:
		return 0
	}
}
