// Package wasm provides WebAssembly integration for FlatBuffers operations.
package wasm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// ErrNoModule is returned when the WASM module is not loaded.
var ErrNoModule = errors.New("WASM module not loaded")

// FlatcModule wraps the flatc WASM module for FlatBuffer operations.
type FlatcModule struct {
	runtime wazero.Runtime
	module  api.Module
	mu      sync.Mutex

	// Exported functions from WASM
	malloc         api.Function
	free           api.Function
	jsonToBinary   api.Function
	binaryToJSON   api.Function
	validateSchema api.Function
	addSchema      api.Function

	// Crypto functions
	encrypt api.Function
	decrypt api.Function
	sign    api.Function
	verify  api.Function

	// Schema ID counter
	schemaCounter int
	schemas       map[string]int
}

// NewFlatcModule creates a new FlatcModule from a WASM file.
func NewFlatcModule(ctx context.Context, wasmPath string) (*FlatcModule, error) {
	if wasmPath == "" {
		return nil, fmt.Errorf("no WASM path provided: %w", ErrNoModule)
	}

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read WASM file: %w", err)
	}

	r := wazero.NewRuntime(ctx)

	// Instantiate WASI for standard I/O
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate WASI: %w", err)
	}

	// Compile and instantiate the module
	module, err := r.Instantiate(ctx, wasmBytes)
	if err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate WASM module: %w", err)
	}

	fm := &FlatcModule{
		runtime: r,
		module:  module,
		schemas: make(map[string]int),
	}

	// Get exported functions
	fm.malloc = module.ExportedFunction("malloc")
	fm.free = module.ExportedFunction("free")
	fm.jsonToBinary = module.ExportedFunction("wasi_json_to_binary")
	fm.binaryToJSON = module.ExportedFunction("wasi_binary_to_json")
	fm.validateSchema = module.ExportedFunction("wasi_validate_schema")
	fm.addSchema = module.ExportedFunction("wasi_add_schema")

	// Crypto functions
	fm.encrypt = module.ExportedFunction("wasi_encrypt_bytes")
	fm.decrypt = module.ExportedFunction("wasi_decrypt_bytes")
	fm.sign = module.ExportedFunction("wasi_ed25519_sign")
	fm.verify = module.ExportedFunction("wasi_ed25519_verify")

	return fm, nil
}

// Close releases the WASM runtime resources.
func (fm *FlatcModule) Close(ctx context.Context) error {
	if fm.runtime != nil {
		return fm.runtime.Close(ctx)
	}
	return nil
}

// AddSchema loads a schema into the WASM module.
func (fm *FlatcModule) AddSchema(ctx context.Context, name string, content []byte) (int, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.addSchema == nil {
		// If WASM function not available, just track schema locally
		fm.schemaCounter++
		fm.schemas[name] = fm.schemaCounter
		return fm.schemaCounter, nil
	}

	// Allocate memory for name and content
	namePtr, err := fm.allocate(ctx, []byte(name))
	if err != nil {
		return 0, err
	}
	defer fm.deallocate(ctx, namePtr, uint32(len(name)))

	contentPtr, err := fm.allocate(ctx, content)
	if err != nil {
		return 0, err
	}
	defer fm.deallocate(ctx, contentPtr, uint32(len(content)))

	// Call WASM function
	results, err := fm.addSchema.Call(ctx, uint64(namePtr), uint64(len(name)), uint64(contentPtr), uint64(len(content)))
	if err != nil {
		return 0, fmt.Errorf("failed to add schema: %w", err)
	}

	schemaID := int(results[0])
	fm.schemas[name] = schemaID
	return schemaID, nil
}

// JSONToBinary converts JSON data to FlatBuffer binary format.
func (fm *FlatcModule) JSONToBinary(ctx context.Context, schemaID int, jsonData []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.jsonToBinary == nil {
		return nil, ErrNoModule
	}

	// Allocate memory for input
	inputPtr, err := fm.allocate(ctx, jsonData)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, inputPtr, uint32(len(jsonData)))

	// Allocate output buffer (max size)
	outputSize := uint32(len(jsonData) * 2) // Estimate: binary is usually smaller but allocate 2x
	if outputSize < 1024 {
		outputSize = 1024
	}
	outputPtr, err := fm.allocateSize(ctx, outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, outputPtr, outputSize)

	// Call WASM function
	results, err := fm.jsonToBinary.Call(ctx,
		uint64(schemaID),
		uint64(inputPtr), uint64(len(jsonData)),
		uint64(outputPtr), uint64(outputSize),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to convert JSON to binary: %w", err)
	}

	resultSize := uint32(results[0])
	if resultSize == 0 {
		return nil, errors.New("conversion produced empty result")
	}

	// Read output from WASM memory
	return fm.readMemory(ctx, outputPtr, resultSize)
}

// BinaryToJSON converts FlatBuffer binary data to JSON format.
func (fm *FlatcModule) BinaryToJSON(ctx context.Context, schemaID int, binaryData []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.binaryToJSON == nil {
		return nil, ErrNoModule
	}

	// Allocate memory for input
	inputPtr, err := fm.allocate(ctx, binaryData)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, inputPtr, uint32(len(binaryData)))

	// Allocate output buffer
	outputSize := uint32(len(binaryData) * 4) // JSON is usually larger
	if outputSize < 4096 {
		outputSize = 4096
	}
	outputPtr, err := fm.allocateSize(ctx, outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, outputPtr, outputSize)

	// Call WASM function
	results, err := fm.binaryToJSON.Call(ctx,
		uint64(schemaID),
		uint64(inputPtr), uint64(len(binaryData)),
		uint64(outputPtr), uint64(outputSize),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to convert binary to JSON: %w", err)
	}

	resultSize := uint32(results[0])
	if resultSize == 0 {
		return nil, errors.New("conversion produced empty result")
	}

	return fm.readMemory(ctx, outputPtr, resultSize)
}

// Encrypt encrypts data using AES-GCM.
func (fm *FlatcModule) Encrypt(ctx context.Context, key, plaintext []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.encrypt == nil {
		return nil, ErrNoModule
	}

	keyPtr, err := fm.allocate(ctx, key)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, keyPtr, uint32(len(key)))

	plaintextPtr, err := fm.allocate(ctx, plaintext)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, plaintextPtr, uint32(len(plaintext)))

	outputSize := uint32(len(plaintext) + 28) // Nonce (12) + tag (16)
	outputPtr, err := fm.allocateSize(ctx, outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, outputPtr, outputSize)

	results, err := fm.encrypt.Call(ctx,
		uint64(keyPtr), uint64(len(key)),
		uint64(plaintextPtr), uint64(len(plaintext)),
		uint64(outputPtr), uint64(outputSize),
	)
	if err != nil {
		return nil, fmt.Errorf("encryption failed: %w", err)
	}

	resultSize := uint32(results[0])
	return fm.readMemory(ctx, outputPtr, resultSize)
}

// Decrypt decrypts data using AES-GCM.
func (fm *FlatcModule) Decrypt(ctx context.Context, key, ciphertext []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.decrypt == nil {
		return nil, ErrNoModule
	}

	keyPtr, err := fm.allocate(ctx, key)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, keyPtr, uint32(len(key)))

	ciphertextPtr, err := fm.allocate(ctx, ciphertext)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, ciphertextPtr, uint32(len(ciphertext)))

	outputSize := uint32(len(ciphertext))
	outputPtr, err := fm.allocateSize(ctx, outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, outputPtr, outputSize)

	results, err := fm.decrypt.Call(ctx,
		uint64(keyPtr), uint64(len(key)),
		uint64(ciphertextPtr), uint64(len(ciphertext)),
		uint64(outputPtr), uint64(outputSize),
	)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	resultSize := uint32(results[0])
	return fm.readMemory(ctx, outputPtr, resultSize)
}

// Sign signs data using Ed25519.
func (fm *FlatcModule) Sign(ctx context.Context, privateKey, message []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.sign == nil {
		return nil, ErrNoModule
	}

	keyPtr, err := fm.allocate(ctx, privateKey)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, keyPtr, uint32(len(privateKey)))

	msgPtr, err := fm.allocate(ctx, message)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, msgPtr, uint32(len(message)))

	outputSize := uint32(64) // Ed25519 signature size
	outputPtr, err := fm.allocateSize(ctx, outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.deallocate(ctx, outputPtr, outputSize)

	results, err := fm.sign.Call(ctx,
		uint64(keyPtr), uint64(len(privateKey)),
		uint64(msgPtr), uint64(len(message)),
		uint64(outputPtr),
	)
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	if results[0] == 0 {
		return nil, errors.New("signing failed")
	}

	return fm.readMemory(ctx, outputPtr, outputSize)
}

// Verify verifies an Ed25519 signature.
func (fm *FlatcModule) Verify(ctx context.Context, publicKey, message, signature []byte) (bool, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.verify == nil {
		return false, ErrNoModule
	}

	keyPtr, err := fm.allocate(ctx, publicKey)
	if err != nil {
		return false, err
	}
	defer fm.deallocate(ctx, keyPtr, uint32(len(publicKey)))

	msgPtr, err := fm.allocate(ctx, message)
	if err != nil {
		return false, err
	}
	defer fm.deallocate(ctx, msgPtr, uint32(len(message)))

	sigPtr, err := fm.allocate(ctx, signature)
	if err != nil {
		return false, err
	}
	defer fm.deallocate(ctx, sigPtr, uint32(len(signature)))

	results, err := fm.verify.Call(ctx,
		uint64(keyPtr), uint64(len(publicKey)),
		uint64(msgPtr), uint64(len(message)),
		uint64(sigPtr), uint64(len(signature)),
	)
	if err != nil {
		return false, fmt.Errorf("verification failed: %w", err)
	}

	return results[0] != 0, nil
}

// Helper functions for WASM memory management

func (fm *FlatcModule) allocate(ctx context.Context, data []byte) (uint32, error) {
	if fm.malloc == nil {
		return 0, ErrNoModule
	}

	results, err := fm.malloc.Call(ctx, uint64(len(data)))
	if err != nil {
		return 0, err
	}

	ptr := uint32(results[0])
	if ptr == 0 {
		return 0, errors.New("allocation failed")
	}

	// Write data to WASM memory
	ok := fm.module.Memory().Write(ptr, data)
	if !ok {
		return 0, errors.New("failed to write to WASM memory")
	}

	return ptr, nil
}

func (fm *FlatcModule) allocateSize(ctx context.Context, size uint32) (uint32, error) {
	if fm.malloc == nil {
		return 0, ErrNoModule
	}

	results, err := fm.malloc.Call(ctx, uint64(size))
	if err != nil {
		return 0, err
	}

	ptr := uint32(results[0])
	if ptr == 0 {
		return 0, errors.New("allocation failed")
	}

	return ptr, nil
}

func (fm *FlatcModule) deallocate(ctx context.Context, ptr, size uint32) {
	if fm.free != nil {
		fm.free.Call(ctx, uint64(ptr))
	}
}

func (fm *FlatcModule) readMemory(ctx context.Context, ptr, size uint32) ([]byte, error) {
	data, ok := fm.module.Memory().Read(ptr, size)
	if !ok {
		return nil, errors.New("failed to read from WASM memory")
	}
	result := make([]byte, size)
	copy(result, data)
	return result, nil
}

// GetSchemaID returns the ID for a named schema.
func (fm *FlatcModule) GetSchemaID(name string) (int, bool) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	id, ok := fm.schemas[name]
	return id, ok
}
