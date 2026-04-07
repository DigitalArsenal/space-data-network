package sdn_wasi_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// TestWasmEdgeLoadModule tests loading the WASM module with WasmEdge
func TestWasmEdgeLoadModule(t *testing.T) {
	wasmPath := findWASMFile(t)

	conf := wasmedge.NewConfigure(wasmedge.WASI)
	vm := wasmedge.NewVMWithConfig(conf)
	defer vm.Release()
	defer conf.Release()

	err := vm.LoadWasmFile(wasmPath)
	if err != nil {
		t.Fatalf("Failed to load module: %v", err)
	}

	t.Logf("Module loaded successfully")

	err = vm.Validate()
	if err != nil {
		t.Fatalf("Failed to validate module: %v", err)
	}

	t.Logf("Module validated successfully")
}

// TestWasmEdgeWithWASI tests running with WASI support and host functions
func TestWasmEdgeWithWASI(t *testing.T) {
	wasmPath := findWASMFile(t)

	conf := wasmedge.NewConfigure(wasmedge.WASI)
	vm := wasmedge.NewVMWithConfig(conf)
	defer vm.Release()
	defer conf.Release()

	// Initialize WASI — VM auto-registers WASI when it's in the config
	wasiMod := vm.GetImportModule(wasmedge.WASI)
	if wasiMod == nil {
		t.Fatal("Failed to get WASI import module")
	}
	wasiMod.InitWasi([]string{"sdn-wasi"}, []string{}, []string{})

	t.Log("WASI initialized")

	// Create mock host functions
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }

	envMod := wasmedge.NewModule("env")

	noop := func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
		return nil, wasmedge.Result_Success
	}
	ret0i32 := func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
		return []interface{}{int32(0)}, wasmedge.Result_Success
	}
	ret0i64 := func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
		return []interface{}{int64(0)}, wasmedge.Result_Success
	}

	addFunc := func(name string, params, returns []*wasmedge.ValType,
		fn func(interface{}, *wasmedge.CallingFrame, []interface{}) ([]interface{}, wasmedge.Result)) {
		ft := wasmedge.NewFunctionType(params, returns)
		hostfn := wasmedge.NewFunction(ft, fn, nil, 0)
		ft.Release()
		envMod.AddFunction(name, hostfn)
	}

	addFunc("host_log", []*wasmedge.ValType{i32(), i32()}, nil, noop)
	addFunc("host_send_message", []*wasmedge.ValType{i32(), i32(), i32(), i32()}, []*wasmedge.ValType{i32()}, ret0i32)
	addFunc("host_subscribe", []*wasmedge.ValType{i32(), i32()}, []*wasmedge.ValType{i32()}, ret0i32)
	addFunc("host_get_peer_id", []*wasmedge.ValType{i32(), i32()}, []*wasmedge.ValType{i32()}, ret0i32)
	addFunc("host_store_data", []*wasmedge.ValType{i32(), i32(), i32(), i32()}, []*wasmedge.ValType{i64()}, ret0i64)
	addFunc("host_load_data", []*wasmedge.ValType{i32(), i32(), i32(), i32()}, []*wasmedge.ValType{i32()}, ret0i32)

	if err := vm.RegisterModule(envMod); err != nil {
		t.Fatalf("Failed to register env module: %v", err)
	}
	defer envMod.Release()

	t.Log("Host functions registered")

	// Load, validate, and instantiate
	if err := vm.LoadWasmFile(wasmPath); err != nil {
		t.Fatalf("Failed to load module: %v", err)
	}
	if err := vm.Validate(); err != nil {
		t.Fatalf("Failed to validate module: %v", err)
	}
	if err := vm.Instantiate(); err != nil {
		t.Fatalf("Failed to instantiate module: %v", err)
	}

	t.Log("Module instantiated successfully")
}

// TestDistDirectoryStructure verifies the dist directory structure
func TestDistDirectoryStructure(t *testing.T) {
	root := findProjectRoot(t)
	distDir := filepath.Join(root, "dist")

	expected := []string{
		"sdn-wasi.wasm",
		"module-info.json",
		"runtime/wasmtime.toml",
		"runtime/wasmer.toml",
		"runtime/wasmedge.json",
	}

	for _, file := range expected {
		path := filepath.Join(distDir, file)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Missing: %s", file)
		} else {
			t.Logf("Found: %s", file)
		}
	}
}
