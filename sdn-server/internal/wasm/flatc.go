// Package wasm provides WebAssembly integration for FlatBuffers operations.
package wasm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// ErrNoModule is returned when the WASM module is not loaded.
var ErrNoModule = errors.New("WASM module not loaded")

// FlatcModule wraps the flatc WASM module for FlatBuffer operations.
type FlatcModule struct {
	mod *wasmrt.Module
	mu  sync.Mutex

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

	mod, err := wasmrt.NewModule(wasmBytes,
		wasmrt.WithWASI(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create WASM module: %w", err)
	}

	return &FlatcModule{
		mod:     mod,
		schemas: make(map[string]int),
	}, nil
}

// Close releases the WASM runtime resources.
func (fm *FlatcModule) Close(ctx context.Context) error {
	if fm.mod != nil {
		fm.mod.Release()
	}
	return nil
}

// AddSchema loads a schema into the WASM module.
func (fm *FlatcModule) AddSchema(ctx context.Context, name string, content []byte) (int, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.mod == nil {
		// No WASM module — track schema locally
		fm.schemaCounter++
		fm.schemas[name] = fm.schemaCounter
		return fm.schemaCounter, nil
	}

	// Allocate memory for name and content
	namePtr, err := fm.mod.Allocate([]byte(name))
	if err != nil {
		fm.schemaCounter++
		fm.schemas[name] = fm.schemaCounter
		return fm.schemaCounter, nil
	}
	defer fm.mod.Deallocate(namePtr)

	contentPtr, err := fm.mod.Allocate(content)
	if err != nil {
		return 0, err
	}
	defer fm.mod.Deallocate(contentPtr)

	results, err := fm.mod.Execute("wasi_add_schema",
		int32(namePtr), int32(len(name)),
		int32(contentPtr), int32(len(content)),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to add schema: %w", err)
	}

	schemaID := int(wasmrt.ToInt32(results[0]))
	fm.schemas[name] = schemaID
	return schemaID, nil
}

// JSONToBinary converts JSON data to FlatBuffer binary format.
func (fm *FlatcModule) JSONToBinary(ctx context.Context, schemaID int, jsonData []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.mod == nil {
		return nil, ErrNoModule
	}

	// Allocate memory for input
	inputPtr, err := fm.mod.Allocate(jsonData)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(inputPtr)

	// Allocate output buffer (max size)
	outputSize := uint32(len(jsonData) * 2) // Estimate: binary is usually smaller but allocate 2x
	if outputSize < 1024 {
		outputSize = 1024
	}
	outputPtr, err := fm.mod.AllocateSize(outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(outputPtr)

	results, err := fm.mod.Execute("wasi_json_to_binary",
		int32(schemaID),
		int32(inputPtr), int32(len(jsonData)),
		int32(outputPtr), int32(outputSize),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to convert JSON to binary: %w", err)
	}

	resultSize := uint32(wasmrt.ToInt32(results[0]))
	if resultSize == 0 {
		return nil, errors.New("conversion produced empty result")
	}

	return fm.mod.ReadMemory(outputPtr, resultSize)
}

// BinaryToJSON converts FlatBuffer binary data to JSON format.
func (fm *FlatcModule) BinaryToJSON(ctx context.Context, schemaID int, binaryData []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.mod == nil {
		return nil, ErrNoModule
	}

	inputPtr, err := fm.mod.Allocate(binaryData)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(inputPtr)

	outputSize := uint32(len(binaryData) * 4) // JSON is usually larger
	if outputSize < 4096 {
		outputSize = 4096
	}
	outputPtr, err := fm.mod.AllocateSize(outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(outputPtr)

	results, err := fm.mod.Execute("wasi_binary_to_json",
		int32(schemaID),
		int32(inputPtr), int32(len(binaryData)),
		int32(outputPtr), int32(outputSize),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to convert binary to JSON: %w", err)
	}

	resultSize := uint32(wasmrt.ToInt32(results[0]))
	if resultSize == 0 {
		return nil, errors.New("conversion produced empty result")
	}

	return fm.mod.ReadMemory(outputPtr, resultSize)
}

// Encrypt encrypts data using AES-GCM.
func (fm *FlatcModule) Encrypt(ctx context.Context, key, plaintext []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.mod == nil {
		return nil, ErrNoModule
	}

	keyPtr, err := fm.mod.Allocate(key)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(keyPtr)

	plaintextPtr, err := fm.mod.Allocate(plaintext)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(plaintextPtr)

	outputSize := uint32(len(plaintext) + 28) // Nonce (12) + tag (16)
	outputPtr, err := fm.mod.AllocateSize(outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(outputPtr)

	results, err := fm.mod.Execute("wasi_encrypt_bytes",
		int32(keyPtr), int32(len(key)),
		int32(plaintextPtr), int32(len(plaintext)),
		int32(outputPtr), int32(outputSize),
	)
	if err != nil {
		return nil, fmt.Errorf("encryption failed: %w", err)
	}

	resultSize := uint32(wasmrt.ToInt32(results[0]))
	return fm.mod.ReadMemory(outputPtr, resultSize)
}

// Decrypt decrypts data using AES-GCM.
func (fm *FlatcModule) Decrypt(ctx context.Context, key, ciphertext []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.mod == nil {
		return nil, ErrNoModule
	}

	keyPtr, err := fm.mod.Allocate(key)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(keyPtr)

	ciphertextPtr, err := fm.mod.Allocate(ciphertext)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(ciphertextPtr)

	outputSize := uint32(len(ciphertext))
	outputPtr, err := fm.mod.AllocateSize(outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(outputPtr)

	results, err := fm.mod.Execute("wasi_decrypt_bytes",
		int32(keyPtr), int32(len(key)),
		int32(ciphertextPtr), int32(len(ciphertext)),
		int32(outputPtr), int32(outputSize),
	)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	resultSize := uint32(wasmrt.ToInt32(results[0]))
	return fm.mod.ReadMemory(outputPtr, resultSize)
}

// Sign signs data using Ed25519.
func (fm *FlatcModule) Sign(ctx context.Context, privateKey, message []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.mod == nil {
		return nil, ErrNoModule
	}

	keyPtr, err := fm.mod.Allocate(privateKey)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(keyPtr)

	msgPtr, err := fm.mod.Allocate(message)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(msgPtr)

	outputSize := uint32(64) // Ed25519 signature size
	outputPtr, err := fm.mod.AllocateSize(outputSize)
	if err != nil {
		return nil, err
	}
	defer fm.mod.Deallocate(outputPtr)

	results, err := fm.mod.Execute("wasi_ed25519_sign",
		int32(keyPtr), int32(len(privateKey)),
		int32(msgPtr), int32(len(message)),
		int32(outputPtr),
	)
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	if wasmrt.ToInt32(results[0]) == 0 {
		return nil, errors.New("signing failed")
	}

	return fm.mod.ReadMemory(outputPtr, outputSize)
}

// Verify verifies an Ed25519 signature.
func (fm *FlatcModule) Verify(ctx context.Context, publicKey, message, signature []byte) (bool, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.mod == nil {
		return false, ErrNoModule
	}

	keyPtr, err := fm.mod.Allocate(publicKey)
	if err != nil {
		return false, err
	}
	defer fm.mod.Deallocate(keyPtr)

	msgPtr, err := fm.mod.Allocate(message)
	if err != nil {
		return false, err
	}
	defer fm.mod.Deallocate(msgPtr)

	sigPtr, err := fm.mod.Allocate(signature)
	if err != nil {
		return false, err
	}
	defer fm.mod.Deallocate(sigPtr)

	results, err := fm.mod.Execute("wasi_ed25519_verify",
		int32(keyPtr), int32(len(publicKey)),
		int32(msgPtr), int32(len(message)),
		int32(sigPtr), int32(len(signature)),
	)
	if err != nil {
		return false, fmt.Errorf("verification failed: %w", err)
	}

	return wasmrt.ToInt32(results[0]) != 0, nil
}

// GetSchemaID returns the ID for a named schema.
func (fm *FlatcModule) GetSchemaID(name string) (int, bool) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	id, ok := fm.schemas[name]
	return id, ok
}
