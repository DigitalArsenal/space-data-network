package flatsqlrt

// Loop C.3c3 proof-of-concept: the FlatSQL engine as a LINKED COMPONENT
// dependency of another wasm module — the dependent imports the engine
// instance's exports (functions AND memory) and calls flatsql_* directly
// in-wasm, with zero hostcalls and zero cross-memory copies.
//
// Two shapes are proven against WasmEdge-go 0.14:
//
//  1. Same-VM linking: the engine is registered as a NAMED module
//     (VM.RegisterWasmBuffer("flatsql", ...)); a dependent instantiated in
//     the same VM resolves its (import "flatsql" ...) directly against the
//     live engine instance. State is shared bidirectionally: a db created
//     by the dependent is queryable by the host through the same instance.
//
//  2. Cross-VM instance linking: the live engine instance owned by VM 1
//     (the store's VM) is registered into VM 2 via VM.RegisterModule
//     (C API WasmEdge_VMRegisterModuleFromImport) WITHOUT re-instantiation
//     — the dependent in VM 2 (modulert's per-module VM shape) calls into
//     the engine instance that holds VM 1's data.
//
// The dependent is hand-assembled WAT (source below). Its key property is
// position independence WITHOUT a PIC toolchain: it has no memory of its
// own — it imports the engine's memory — and all its constant data are
// PASSIVE segments that it memory.init's into engine-malloc'd space at
// runtime. Bulk-memory ops are baseline in WasmEdge 0.14 and in every
// browser, so the identical artifact works under WebAssembly.instantiate
// with {flatsql: engineInstance.exports}.
//
// See docs/flatsql-component-linkage.md for the full option analysis.

import (
	"encoding/hex"
	"os"
	"runtime"
	"testing"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// linkagePOCToyHex is the compiled toy dependent. Regenerate with
// `wat2wasm toy.wat -o toy.wasm` (wabt; bulk-memory is on by default)
// from this source:
//
//	(module
//	  (import "flatsql" "memory" (memory 1))
//	  (import "flatsql" "malloc" (func $malloc (param i32) (result i32)))
//	  (import "flatsql" "free" (func $free (param i32)))
//	  (import "flatsql" "flatsql_create_db" (func $create_db (param i32 i32) (result i32)))
//	  (import "flatsql" "flatsql_query" (func $query (param i32 i32) (result i32)))
//	  (import "flatsql" "flatsql_result_row_count" (func $row_count (result i32)))
//	  (import "flatsql" "flatsql_result_cell_number" (func $cell_number (param i32 i32) (result f64)))
//
//	  ;; Passive segments: not placed at any fixed address at instantiation.
//	  (data $schema "table Item { id: int; name: string; } root_type Item;\00")  ;; 54 bytes
//	  (data $dbname "linkpoc\00")                                                ;; 8 bytes
//	  (data $sql    "SELECT 6*7\00")                                             ;; 11 bytes
//
//	  ;; make_db() -> i32: creates a database in the LIVE engine instance,
//	  ;; entirely in-wasm, and returns the engine db handle (0 on failure).
//	  (func $make_db (export "make_db") (result i32)
//	    (local $schemaPtr i32) (local $namePtr i32) (local $db i32)
//	    (local.set $schemaPtr (call $malloc (i32.const 54)))
//	    (if (i32.eqz (local.get $schemaPtr)) (then (return (i32.const 0))))
//	    (memory.init $schema (local.get $schemaPtr) (i32.const 0) (i32.const 54))
//	    (local.set $namePtr (call $malloc (i32.const 8)))
//	    (if (i32.eqz (local.get $namePtr)) (then (return (i32.const 0))))
//	    (memory.init $dbname (local.get $namePtr) (i32.const 0) (i32.const 8))
//	    (local.set $db (call $create_db (local.get $schemaPtr) (local.get $namePtr)))
//	    (call $free (local.get $schemaPtr))
//	    (call $free (local.get $namePtr))
//	    (local.get $db)
//	  )
//
//	  ;; run_query(db) -> f64: runs SELECT 6*7 against an EXISTING engine db
//	  ;; handle (e.g. one owned by the host store) and returns the cell (42),
//	  ;; or a negative sentinel on failure.
//	  (func $run_query (export "run_query") (param $db i32) (result f64)
//	    (local $sqlPtr i32)
//	    (local.set $sqlPtr (call $malloc (i32.const 11)))
//	    (if (i32.eqz (local.get $sqlPtr)) (then (return (f64.const -12))))
//	    (memory.init $sql (local.get $sqlPtr) (i32.const 0) (i32.const 11))
//	    (if (i32.eqz (call $query (local.get $db) (local.get $sqlPtr)))
//	      (then
//	        (call $free (local.get $sqlPtr))
//	        (return (f64.const -2))))
//	    (call $free (local.get $sqlPtr))
//	    (if (i32.ne (call $row_count) (i32.const 1)) (then (return (f64.const -3))))
//	    (call $cell_number (i32.const 0) (i32.const 0))
//	  )
//
//	  ;; run() -> f64: end-to-end convenience (create db + query -> 42).
//	  (func (export "run") (result f64)
//	    (local $db i32)
//	    (local.set $db (call $make_db))
//	    (if (i32.eqz (local.get $db)) (then (return (f64.const -1))))
//	    (call $run_query (local.get $db))
//	  )
//	)
const linkagePOCToyHex = "0061736d0100000001230760017f017f60017f0060027f7f017f6000017f60027f7f017c60017f017c6000017c02af010707666c617473716c066d656d6f727902000107666c617473716c066d616c6c6f63000007666c617473716c0466726565000107666c617473716c11666c617473716c5f6372656174655f6462000207666c617473716c0d666c617473716c5f7175657279000207666c617473716c18666c617473716c5f726573756c745f726f775f636f756e74000307666c617473716c1a666c617473716c5f726573756c745f63656c6c5f6e756d6265720004030403030506071d03076d616b655f646200060972756e5f717565727900070372756e00080c01030ac001034801037f413610002100200045044041000f0b200041004136fc080000410810002101200145044041000f0b200141004108fc0801002000200110022102200010012001100120020b5801017f410b1000210120014504404400000000000028c00f0b20014100410bfc080200200020011003450440200110014400000000000000c00f0b20011001100441014704404400000000000008c00f0b4100410010050b1c01017f10062100200045044044000000000000f0bf0f0b200010070b0b500301367461626c65204974656d207b2069643a20696e743b206e616d653a20737472696e673b207d20726f6f745f74797065204974656d3b0001086c696e6b706f6300010b53454c45435420362a3700"

func linkagePOCToyWasm(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(linkagePOCToyHex)
	if err != nil {
		t.Fatalf("decode toy wasm hex: %v", err)
	}
	return b
}

// newEngineVM creates a VM with WASI, registers the embedded engine under
// the import-module name "flatsql", and runs its _initialize.
func newEngineVM(t *testing.T) *wasmedge.VM {
	t.Helper()
	return newEngineVMWithBytes(t, flatsqlWasm)
}

func newEngineVMWithBytes(t *testing.T, engineBytes []byte) *wasmedge.VM {
	t.Helper()
	conf := wasmedge.NewConfigure(wasmedge.WASI)
	conf.SetMaxMemoryPage(DefaultMaxMemoryPages)
	vm := wasmedge.NewVMWithConfig(conf)
	if vm == nil {
		conf.Release()
		t.Fatal("create engine VM")
	}
	t.Cleanup(func() { vm.Release(); conf.Release() })

	wasi := vm.GetImportModule(wasmedge.WASI)
	if wasi == nil {
		t.Fatal("WASI import module missing")
	}
	wasi.InitWasi([]string{}, []string{}, []string{})

	if err := vm.RegisterWasmBuffer("flatsql", engineBytes); err != nil {
		t.Fatalf("register engine as named module: %v", err)
	}
	if _, err := vm.ExecuteRegistered("flatsql", "_initialize"); err != nil {
		t.Fatalf("engine _initialize: %v", err)
	}
	return vm
}

// TestLinkedComponent_SameVM: dependent instantiated in the SAME VM as the
// named engine module; imports resolve to the live engine instance.
func TestLinkedComponent_SameVM(t *testing.T) {
	vm := newEngineVM(t)
	toy := linkagePOCToyWasm(t)

	// Instantiate the dependent as the VM's active (anonymous) module. Its
	// (import "flatsql" ...) resolve against the registered engine instance.
	if err := vm.LoadWasmBuffer(toy); err != nil {
		t.Fatalf("load toy: %v", err)
	}
	if err := vm.Validate(); err != nil {
		t.Fatalf("validate toy: %v", err)
	}
	if err := vm.Instantiate(); err != nil {
		t.Fatalf("instantiate toy (imports from live engine instance): %v", err)
	}

	// Dependent creates a db in the engine — pure in-wasm calls.
	res, err := vm.Execute("make_db")
	if err != nil {
		t.Fatalf("toy make_db: %v", err)
	}
	handle := ToInt32ForTest(res[0])
	if handle == 0 {
		t.Fatal("toy make_db returned 0 (engine error)")
	}
	t.Logf("toy created engine db handle=%d via direct in-wasm call", handle)

	// Dependent queries through the live engine — zero hostcalls, zero copies.
	res, err = vm.Execute("run_query", handle)
	if err != nil {
		t.Fatalf("toy run_query: %v", err)
	}
	if got := res[0].(float64); got != 42 {
		t.Fatalf("toy run_query = %v, want 42", got)
	}
	t.Logf("toy run_query(handle) = %v (in-wasm through engine instance)", res[0])

	// Shared-state proof, other direction: the HOST queries the db that the
	// DEPENDENT created, through the same engine instance.
	engine := vm.GetRegisteredModule("flatsql")
	if engine == nil {
		t.Fatal("GetRegisteredModule(flatsql) = nil")
	}
	mem := engine.FindMemory("memory")
	if mem == nil {
		t.Fatal("engine memory export missing")
	}
	sql := append([]byte("SELECT 40+2"), 0)
	ptrRes, err := vm.ExecuteRegistered("flatsql", "malloc", int32(len(sql)))
	if err != nil {
		t.Fatalf("engine malloc: %v", err)
	}
	sqlPtr := ToInt32ForTest(ptrRes[0])
	if err := mem.SetData(sql, uint(uint32(sqlPtr)), uint(len(sql))); err != nil {
		t.Fatalf("write sql into engine memory: %v", err)
	}
	okRes, err := vm.ExecuteRegistered("flatsql", "flatsql_query", handle, sqlPtr)
	if err != nil {
		t.Fatalf("host flatsql_query on toy-created db: %v", err)
	}
	if ToInt32ForTest(okRes[0]) == 0 {
		t.Fatal("host flatsql_query failed on toy-created db")
	}
	cell, err := vm.ExecuteRegistered("flatsql", "flatsql_result_cell_number", int32(0), int32(0))
	if err != nil {
		t.Fatalf("host read cell: %v", err)
	}
	if got := cell[0].(float64); got != 42 {
		t.Fatalf("host query on toy-created db = %v, want 42", got)
	}
	vm.ExecuteRegistered("flatsql", "free", sqlPtr)
	t.Logf("host queried toy-created db through same instance: %v", cell[0])
}

// TestLinkedComponent_CrossVM: the engine instance lives in VM 1 (the store's
// VM); the dependent lives in VM 2 (modulert's per-module VM shape). VM 2
// registers the EXISTING instance — no re-instantiation, no data copy — via
// WasmEdge_VMRegisterModuleFromImport.
func TestLinkedComponent_CrossVM(t *testing.T) {
	vm1 := newEngineVM(t)

	engine := vm1.GetRegisteredModule("flatsql")
	if engine == nil {
		t.Fatal("GetRegisteredModule(flatsql) = nil")
	}

	conf2 := wasmedge.NewConfigure()
	conf2.SetMaxMemoryPage(DefaultMaxMemoryPages)
	vm2 := wasmedge.NewVMWithConfig(conf2)
	if vm2 == nil {
		conf2.Release()
		t.Fatal("create dependent VM")
	}
	// vm2 must be released before vm1 (it borrows vm1's engine instance);
	// t.Cleanup runs LIFO after newEngineVM's cleanup registration, so this
	// runs first.
	t.Cleanup(func() { vm2.Release(); conf2.Release() })

	// Register the LIVE instance from vm1 as an import module of vm2.
	if err := vm2.RegisterModule(engine); err != nil {
		t.Fatalf("register live engine instance into second VM: %v", err)
	}

	toy := linkagePOCToyWasm(t)
	if err := vm2.LoadWasmBuffer(toy); err != nil {
		t.Fatalf("load toy: %v", err)
	}
	if err := vm2.Validate(); err != nil {
		t.Fatalf("validate toy: %v", err)
	}
	if err := vm2.Instantiate(); err != nil {
		t.Fatalf("instantiate toy against cross-VM engine instance: %v", err)
	}

	res, err := vm2.Execute("run")
	if err != nil {
		t.Fatalf("toy run (cross-VM): %v", err)
	}
	if got := res[0].(float64); got != 42 {
		t.Fatalf("cross-VM toy run = %v, want 42", got)
	}
	t.Logf("cross-VM: dependent in VM2 ran query in VM1's live engine instance = %v", res[0])
}

// TestLinkedComponent_AOTEngine: same as CrossVM but with the AOT-compiled
// engine (what production runs). Env-gated because the first AOT compile of
// the engine can take minutes; set FLATSQL_LINKAGE_AOT=1 (and optionally
// FLATSQL_AOT_CACHE_DIR to reuse a cache) to run.
func TestLinkedComponent_AOTEngine(t *testing.T) {
	if os.Getenv("FLATSQL_LINKAGE_AOT") == "" {
		t.Skip("set FLATSQL_LINKAGE_AOT=1 to run (first AOT compile of the engine can take minutes)")
	}
	cacheDir := os.Getenv("FLATSQL_AOT_CACHE_DIR")
	if cacheDir == "" {
		cacheDir = t.TempDir()
	}
	compiled, err := ensureAOT(cacheDir, flatsqlWasm)
	if err != nil {
		t.Fatalf("AOT compile engine: %v", err)
	}

	vm1 := newEngineVMWithBytes(t, compiled)
	engine := vm1.GetRegisteredModule("flatsql")
	if engine == nil {
		t.Fatal("GetRegisteredModule(flatsql) = nil")
	}

	conf2 := wasmedge.NewConfigure()
	conf2.SetMaxMemoryPage(DefaultMaxMemoryPages)
	vm2 := wasmedge.NewVMWithConfig(conf2)
	if vm2 == nil {
		conf2.Release()
		t.Fatal("create dependent VM")
	}
	t.Cleanup(func() { vm2.Release(); conf2.Release() })

	if err := vm2.RegisterModule(engine); err != nil {
		t.Fatalf("register AOT engine instance into second VM: %v", err)
	}
	toy := linkagePOCToyWasm(t)
	if err := vm2.LoadWasmBuffer(toy); err != nil {
		t.Fatalf("load toy: %v", err)
	}
	if err := vm2.Validate(); err != nil {
		t.Fatalf("validate toy: %v", err)
	}
	if err := vm2.Instantiate(); err != nil {
		t.Fatalf("instantiate toy against AOT engine: %v", err)
	}
	res, err := vm2.Execute("run")
	if err != nil {
		t.Fatalf("toy run (AOT engine): %v", err)
	}
	if got := res[0].(float64); got != 42 {
		t.Fatalf("AOT-engine toy run = %v, want 42", got)
	}
	t.Logf("interpreted dependent -> AOT-compiled engine instance = %v", res[0])
}

// TestLinkedComponent_AOTDependentAOTEngine answers the C.5c feasibility
// question the C.5b thread-affinity bug raises for DIRECT linkage: an
// AOT-compiled dependent making direct import calls into an AOT-compiled
// engine instance ON THE SAME OS THREAD. This is NOT the trapping C.5b shape
// (there, a nested Executor::execute re-entry ran inside a suspended host
// function); a linked direct call is one contiguous wasm call stack under
// ONE executor invocation. If this passes, linked-direct engine calls do not
// need the engine's dedicated thread; if it traps, direct linkage inherits
// the C.5b workaround. Env-gated like the AOT engine POC.
func TestLinkedComponent_AOTDependentAOTEngine(t *testing.T) {
	if os.Getenv("FLATSQL_LINKAGE_AOT") == "" {
		t.Skip("set FLATSQL_LINKAGE_AOT=1 to run (first AOT compile of the engine can take minutes)")
	}
	cacheDir := os.Getenv("FLATSQL_AOT_CACHE_DIR")
	if cacheDir == "" {
		cacheDir = t.TempDir()
	}
	compiledEngine, err := ensureAOT(cacheDir, flatsqlWasm)
	if err != nil {
		t.Fatalf("AOT compile engine: %v", err)
	}
	compiledToy, err := EnsureAOTArtifact(cacheDir, "linkpoc", linkagePOCToyWasm(t))
	if err != nil {
		t.Fatalf("AOT compile toy dependent: %v", err)
	}

	// Pin the goroutine: both AOT modules execute on THIS one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vm1 := newEngineVMWithBytes(t, compiledEngine)
	engine := vm1.GetRegisteredModule("flatsql")
	if engine == nil {
		t.Fatal("GetRegisteredModule(flatsql) = nil")
	}

	conf2 := wasmedge.NewConfigure()
	conf2.SetMaxMemoryPage(DefaultMaxMemoryPages)
	vm2 := wasmedge.NewVMWithConfig(conf2)
	if vm2 == nil {
		conf2.Release()
		t.Fatal("create dependent VM")
	}
	t.Cleanup(func() { vm2.Release(); conf2.Release() })

	if err := vm2.RegisterModule(engine); err != nil {
		t.Fatalf("register AOT engine instance into second VM: %v", err)
	}
	if err := vm2.LoadWasmBuffer(compiledToy); err != nil {
		t.Fatalf("load AOT toy: %v", err)
	}
	if err := vm2.Validate(); err != nil {
		t.Fatalf("validate AOT toy: %v", err)
	}
	if err := vm2.Instantiate(); err != nil {
		t.Fatalf("instantiate AOT toy against AOT engine: %v", err)
	}

	// Repeat to catch state corruption that only bites on the SECOND pass
	// (the C.5b trap fired on the return to the suspended outer frame).
	for i := 0; i < 3; i++ {
		res, err := vm2.Execute("run")
		if err != nil {
			t.Fatalf("AOT toy run #%d (AOT engine, same thread): %v", i, err)
		}
		if got := res[0].(float64); got != 42 {
			t.Fatalf("AOT->AOT direct-linked run #%d = %v, want 42", i, got)
		}
	}
	t.Logf("AOT dependent -> AOT engine, direct import calls, one OS thread: OK (3 passes)")
}

// ToInt32ForTest converts a WasmEdge return value to int32 (mirrors
// wasmrt.ToInt32 without importing it into this package's tests).
func ToInt32ForTest(v interface{}) int32 {
	switch val := v.(type) {
	case int32:
		return val
	case uint32:
		return int32(val)
	case int64:
		return int32(val)
	case uint64:
		return int32(val)
	default:
		return 0
	}
}
