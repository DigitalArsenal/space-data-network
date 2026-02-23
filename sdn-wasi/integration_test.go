package sdn_wasi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// findProjectRoot finds the sdn-wasi directory
func findProjectRoot(t *testing.T) string {
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}

	// Check if we're already in sdn-wasi
	if filepath.Base(cwd) == "sdn-wasi" {
		return cwd
	}

	// Try parent
	parent := filepath.Dir(cwd)
	if filepath.Base(parent) == "sdn-wasi" {
		return parent
	}

	return cwd
}

// findWASMFile locates the built WASM file
func findWASMFile(t *testing.T) string {
	root := findProjectRoot(t)
	wasmPath := filepath.Join(root, "dist", "sdn-wasi.wasm")

	if _, err := os.Stat(wasmPath); err != nil {
		t.Skipf("WASM file not found at %s - run ./scripts/build.sh first", wasmPath)
	}

	return wasmPath
}

// TestWASMFileExists verifies the WASM file was built
func TestWASMFileExists(t *testing.T) {
	wasmPath := findWASMFile(t)

	info, err := os.Stat(wasmPath)
	if err != nil {
		t.Fatalf("Failed to stat WASM file: %v", err)
	}

	// Check minimum size (should be at least 1MB)
	if info.Size() < 1024*1024 {
		t.Errorf("WASM file seems too small: %d bytes", info.Size())
	}

	t.Logf("WASM file: %s (%d bytes)", wasmPath, info.Size())
}

// TestWASMFileValid verifies the WASM file is valid WebAssembly
func TestWASMFileValid(t *testing.T) {
	wasmPath := findWASMFile(t)

	data, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("Failed to read WASM file: %v", err)
	}

	// Check WASM magic number: \0asm
	if len(data) < 4 {
		t.Fatal("WASM file too small")
	}

	magic := data[:4]
	expected := []byte{0x00, 0x61, 0x73, 0x6d} // \0asm
	if !bytes.Equal(magic, expected) {
		t.Errorf("Invalid WASM magic number: got %v, expected %v", magic, expected)
	}

	// Check version (should be 1)
	if len(data) < 8 {
		t.Fatal("WASM file too small for version")
	}
	version := data[4:8]
	expectedVersion := []byte{0x01, 0x00, 0x00, 0x00}
	if !bytes.Equal(version, expectedVersion) {
		t.Errorf("Unexpected WASM version: got %v, expected %v", version, expectedVersion)
	}

	t.Log("WASM file has valid magic number and version")
}

// TestModuleInfoExists verifies module-info.json was generated
func TestModuleInfoExists(t *testing.T) {
	root := findProjectRoot(t)
	infoPath := filepath.Join(root, "dist", "module-info.json")

	data, err := os.ReadFile(infoPath)
	if err != nil {
		t.Skipf("module-info.json not found: %v", err)
	}

	var info struct {
		Name    string   `json:"name"`
		Version string   `json:"version"`
		Exports []string `json:"exports"`
		Imports []string `json:"imports"`
	}

	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("Failed to parse module-info.json: %v", err)
	}

	if info.Name != "sdn-wasi" {
		t.Errorf("Expected name 'sdn-wasi', got '%s'", info.Name)
	}

	if len(info.Exports) == 0 {
		t.Error("Expected exports in module-info.json")
	}

	if len(info.Imports) == 0 {
		t.Error("Expected imports in module-info.json")
	}

	t.Logf("Module info: %s v%s with %d exports and %d imports",
		info.Name, info.Version, len(info.Exports), len(info.Imports))
}

// TestWasmtimeAvailable checks if wasmtime is installed
func TestWasmtimeAvailable(t *testing.T) {
	_, err := exec.LookPath("wasmtime")
	if err != nil {
		t.Skip("wasmtime not installed - skipping runtime tests")
	}
	t.Log("wasmtime is available")
}

// TestWasmtimeVersion runs the version command with wasmtime
func TestWasmtimeVersion(t *testing.T) {
	wasmPath := findWASMFile(t)

	_, err := exec.LookPath("wasmtime")
	if err != nil {
		t.Skip("wasmtime not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wasmtime", "run", wasmPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Command output: %s", string(output))
		// This might fail due to missing host imports, which is expected
		if strings.Contains(string(output), "unknown import") {
			t.Skip("wasmtime doesn't provide host imports (expected)")
		}
		t.Fatalf("wasmtime failed: %v", err)
	}

	result := strings.TrimSpace(string(output))
	if !strings.Contains(result, "sdn-wasi") {
		t.Errorf("Expected version containing 'sdn-wasi', got: %s", result)
	}

	t.Logf("Version: %s", result)
}

// TestWasmtimeListSchemas runs the list-schemas command with wasmtime
func TestWasmtimeListSchemas(t *testing.T) {
	wasmPath := findWASMFile(t)

	_, err := exec.LookPath("wasmtime")
	if err != nil {
		t.Skip("wasmtime not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wasmtime", "run", wasmPath, "list-schemas")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "unknown import") {
			t.Skip("wasmtime doesn't provide host imports (expected)")
		}
		t.Fatalf("wasmtime failed: %v", err)
	}

	result := strings.TrimSpace(string(output))

	// Should be a JSON array
	var schemas []string
	if err := json.Unmarshal([]byte(result), &schemas); err != nil {
		t.Fatalf("Failed to parse schemas: %v (output: %s)", err, result)
	}

	if len(schemas) == 0 {
		t.Error("Expected at least one schema")
	}

	t.Logf("Schemas: %v", schemas)
}

// TestWasmtimeHelp runs the help command with wasmtime
func TestWasmtimeHelp(t *testing.T) {
	wasmPath := findWASMFile(t)

	_, err := exec.LookPath("wasmtime")
	if err != nil {
		t.Skip("wasmtime not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wasmtime", "run", wasmPath, "help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "unknown import") {
			t.Skip("wasmtime doesn't provide host imports (expected)")
		}
		t.Fatalf("wasmtime failed: %v", err)
	}

	result := string(output)
	if !strings.Contains(result, "SDN WASI Module") {
		t.Errorf("Expected help text, got: %s", result)
	}

	t.Logf("Help output:\n%s", result)
}

// TestRuntimeConfigs verifies runtime config files exist
func TestRuntimeConfigs(t *testing.T) {
	root := findProjectRoot(t)

	configs := []string{
		"runtime/wasmtime.toml",
		"runtime/wasmer.toml",
		"runtime/wasmedge.json",
	}

	for _, config := range configs {
		path := filepath.Join(root, config)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Missing runtime config: %s", config)
		} else {
			t.Logf("Found: %s", config)
		}
	}
}

// TestBuildScriptsExist verifies build scripts exist and are executable
func TestBuildScriptsExist(t *testing.T) {
	root := findProjectRoot(t)

	scripts := []string{
		"scripts/build.sh",
		"scripts/test.sh",
		"scripts/test-cli.sh",
	}

	for _, script := range scripts {
		path := filepath.Join(root, script)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("Missing script: %s", script)
			continue
		}

		// Check if executable
		if info.Mode()&0111 == 0 {
			t.Errorf("Script not executable: %s", script)
		} else {
			t.Logf("Found executable: %s", script)
		}
	}
}
