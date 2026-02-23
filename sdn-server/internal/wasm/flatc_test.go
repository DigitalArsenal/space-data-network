// Package wasm provides WebAssembly integration for FlatBuffers operations.
package wasm

import (
	"context"
	"testing"
)

func TestNewFlatcModuleNoPath(t *testing.T) {
	ctx := context.Background()

	// Test with empty path - should return error
	_, err := NewFlatcModule(ctx, "")
	if err == nil {
		t.Error("Expected error for empty WASM path, got nil")
	}
}

func TestNewFlatcModuleInvalidPath(t *testing.T) {
	ctx := context.Background()

	// Test with non-existent path - should return error
	_, err := NewFlatcModule(ctx, "/nonexistent/path/to/flatc.wasm")
	if err == nil {
		t.Error("Expected error for non-existent WASM path, got nil")
	}
}

func TestFlatcModuleGetSchemaID(t *testing.T) {
	// Create a module with nil WASM (schema tracking only)
	fm := &FlatcModule{
		schemas: make(map[string]int),
	}

	// Add a schema
	fm.schemas["test.fbs"] = 1

	// Test GetSchemaID
	id, ok := fm.GetSchemaID("test.fbs")
	if !ok {
		t.Error("Expected to find schema test.fbs")
	}
	if id != 1 {
		t.Errorf("Expected schema ID 1, got %d", id)
	}

	// Test non-existent schema
	_, ok = fm.GetSchemaID("nonexistent.fbs")
	if ok {
		t.Error("Expected not to find nonexistent.fbs")
	}
}

func TestFlatcModuleAddSchemaWithoutWASM(t *testing.T) {
	ctx := context.Background()

	// Create a module without WASM (for testing schema management)
	fm := &FlatcModule{
		schemas: make(map[string]int),
	}

	// Add schema without WASM - should track locally
	id, err := fm.AddSchema(ctx, "test.fbs", []byte("// test schema"))
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if id != 1 {
		t.Errorf("Expected schema ID 1, got %d", id)
	}

	// Verify schema was tracked
	storedID, ok := fm.GetSchemaID("test.fbs")
	if !ok || storedID != id {
		t.Error("Schema was not properly tracked")
	}
}

func TestFlatcModuleClose(t *testing.T) {
	ctx := context.Background()

	// Test Close on nil runtime
	fm := &FlatcModule{}
	err := fm.Close(ctx)
	if err != nil {
		t.Errorf("Unexpected error closing nil runtime: %v", err)
	}
}

func TestErrNoModule(t *testing.T) {
	ctx := context.Background()
	fm := &FlatcModule{
		schemas: make(map[string]int),
	}

	// Test JSONToBinary without WASM
	_, err := fm.JSONToBinary(ctx, 1, []byte(`{"test": true}`))
	if err != ErrNoModule {
		t.Errorf("Expected ErrNoModule, got %v", err)
	}

	// Test BinaryToJSON without WASM
	_, err = fm.BinaryToJSON(ctx, 1, []byte{0x01, 0x02})
	if err != ErrNoModule {
		t.Errorf("Expected ErrNoModule, got %v", err)
	}

	// Test Encrypt without WASM
	_, err = fm.Encrypt(ctx, make([]byte, 32), []byte("test"))
	if err != ErrNoModule {
		t.Errorf("Expected ErrNoModule, got %v", err)
	}

	// Test Decrypt without WASM
	_, err = fm.Decrypt(ctx, make([]byte, 32), []byte("test"))
	if err != ErrNoModule {
		t.Errorf("Expected ErrNoModule, got %v", err)
	}

	// Test Sign without WASM
	_, err = fm.Sign(ctx, make([]byte, 64), []byte("test"))
	if err != ErrNoModule {
		t.Errorf("Expected ErrNoModule, got %v", err)
	}

	// Test Verify without WASM
	_, err = fm.Verify(ctx, make([]byte, 32), []byte("test"), make([]byte, 64))
	if err != ErrNoModule {
		t.Errorf("Expected ErrNoModule, got %v", err)
	}
}

func TestAllocateWithoutMalloc(t *testing.T) {
	ctx := context.Background()
	fm := &FlatcModule{
		schemas: make(map[string]int),
	}

	// Test allocate without malloc function
	_, err := fm.allocate(ctx, []byte("test"))
	if err != ErrNoModule {
		t.Errorf("Expected ErrNoModule, got %v", err)
	}

	// Test allocateSize without malloc function
	_, err = fm.allocateSize(ctx, 1024)
	if err != ErrNoModule {
		t.Errorf("Expected ErrNoModule, got %v", err)
	}
}
