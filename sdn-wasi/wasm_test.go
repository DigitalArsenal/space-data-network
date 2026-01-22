package sdn_wasi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// TestWazeroLoadModule tests loading the WASM module with wazero
func TestWazeroLoadModule(t *testing.T) {
	wasmPath := findWASMFile(t)

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("Failed to read WASM file: %v", err)
	}

	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	// Compile the module (doesn't instantiate yet)
	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		t.Fatalf("Failed to compile module: %v", err)
	}

	t.Logf("Module compiled successfully")

	// Check exported functions
	exports := compiled.ExportedFunctions()
	t.Logf("Exported functions: %d", len(exports))
	for name := range exports {
		t.Logf("  - %s", name)
	}

	// Check imported functions
	imports := compiled.ImportedFunctions()
	t.Logf("Imported functions: %d", len(imports))
}

// TestWazeroWithWASI tests running with WASI support
func TestWazeroWithWASI(t *testing.T) {
	wasmPath := findWASMFile(t)

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("Failed to read WASM file: %v", err)
	}

	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	// Instantiate WASI
	wasi, err := wasi_snapshot_preview1.Instantiate(ctx, r)
	if err != nil {
		t.Fatalf("Failed to instantiate WASI: %v", err)
	}
	defer wasi.Close(ctx)

	t.Log("WASI instantiated successfully")

	// Create mock host functions
	envBuilder := r.NewHostModuleBuilder("env")

	// Add required host functions
	envBuilder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, ptr, length uint32) {
			t.Logf("[host_log] ptr=%d, len=%d", ptr, length)
		}).
		Export("host_log")

	envBuilder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, topicPtr, topicLen, dataPtr, dataLen uint32) uint32 {
			t.Logf("[host_send_message] topic=%d/%d, data=%d/%d", topicPtr, topicLen, dataPtr, dataLen)
			return 0
		}).
		Export("host_send_message")

	envBuilder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, topicPtr, topicLen uint32) uint32 {
			t.Logf("[host_subscribe] topic=%d/%d", topicPtr, topicLen)
			return 0
		}).
		Export("host_subscribe")

	envBuilder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, bufPtr, bufLen uint32) uint32 {
			return 0
		}).
		Export("host_get_peer_id")

	envBuilder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, schemaPtr, schemaLen, dataPtr, dataLen uint32) uint64 {
			t.Logf("[host_store_data] schema=%d/%d, data=%d/%d", schemaPtr, schemaLen, dataPtr, dataLen)
			return 1
		}).
		Export("host_store_data")

	envBuilder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, cidPtr, cidLen, bufPtr, bufLen uint32) uint32 {
			return 0
		}).
		Export("host_load_data")

	env, err := envBuilder.Instantiate(ctx)
	if err != nil {
		t.Fatalf("Failed to instantiate env module: %v", err)
	}
	defer env.Close(ctx)

	t.Log("Host functions registered")

	// Compile module
	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		t.Fatalf("Failed to compile module: %v", err)
	}

	// Instantiate with WASI config
	config := wazero.NewModuleConfig().
		WithStdout(os.Stdout).
		WithStderr(os.Stderr).
		WithArgs("sdn-wasi") // No command = library mode

	module, err := r.InstantiateModule(ctx, compiled, config)
	if err != nil {
		t.Fatalf("Failed to instantiate module: %v", err)
	}
	defer module.Close(ctx)

	t.Log("Module instantiated successfully")
}

// TestWazeroVersionCommand tests running the version command
func TestWazeroVersionCommand(t *testing.T) {
	wasmPath := findWASMFile(t)

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("Failed to read WASM file: %v", err)
	}

	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	// Instantiate WASI
	_, err = wasi_snapshot_preview1.Instantiate(ctx, r)
	if err != nil {
		t.Fatalf("Failed to instantiate WASI: %v", err)
	}

	// Create mock host functions (minimal)
	envBuilder := r.NewHostModuleBuilder("env")
	envBuilder.NewFunctionBuilder().WithFunc(func(ctx context.Context, ptr, length uint32) {}).Export("host_log")
	envBuilder.NewFunctionBuilder().WithFunc(func(ctx context.Context, a, b, c, d uint32) uint32 { return 0 }).Export("host_send_message")
	envBuilder.NewFunctionBuilder().WithFunc(func(ctx context.Context, a, b uint32) uint32 { return 0 }).Export("host_subscribe")
	envBuilder.NewFunctionBuilder().WithFunc(func(ctx context.Context, a, b uint32) uint32 { return 0 }).Export("host_get_peer_id")
	envBuilder.NewFunctionBuilder().WithFunc(func(ctx context.Context, a, b, c, d uint32) uint64 { return 0 }).Export("host_store_data")
	envBuilder.NewFunctionBuilder().WithFunc(func(ctx context.Context, a, b, c, d uint32) uint32 { return 0 }).Export("host_load_data")

	_, err = envBuilder.Instantiate(ctx)
	if err != nil {
		t.Fatalf("Failed to instantiate env module: %v", err)
	}

	// Compile and run with version command
	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		t.Fatalf("Failed to compile module: %v", err)
	}

	// Create a buffer to capture stdout
	var stdout, stderr captureWriter

	config := wazero.NewModuleConfig().
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithArgs("sdn-wasi", "version")

	module, err := r.InstantiateModule(ctx, compiled, config)
	if err != nil {
		t.Fatalf("Failed to instantiate module: %v", err)
	}
	defer module.Close(ctx)

	output := stdout.String()
	if output == "" {
		output = stderr.String()
	}

	t.Logf("Version output: %s", output)

	if output == "" {
		t.Error("Expected version output")
	}
}

// captureWriter captures written data
type captureWriter struct {
	data []byte
}

func (w *captureWriter) Write(p []byte) (n int, err error) {
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *captureWriter) String() string {
	return string(w.data)
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
