// Package wasm provides WebAssembly integration for HD wallet operations.
package wasm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// wasmCallTimeout is the maximum duration for a single WASM function call.
const wasmCallTimeout = 5 * time.Second

// zeroBytes overwrites a byte slice with zeros to clear sensitive key material.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// HD wallet errors
var (
	ErrHDWalletNoModule     = errors.New("HD wallet WASM module not loaded")
	ErrHDWalletNoEntropy    = errors.New("entropy not available - inject entropy first")
	ErrHDWalletInvalidSeed  = errors.New("invalid seed length")
	ErrHDWalletInvalidPath  = errors.New("invalid derivation path")
	ErrHDWalletSigningError = errors.New("signing operation failed")
)

// HDWalletModule wraps the hd-wallet-wasm module for HD wallet operations.
type HDWalletModule struct {
	runtime wazero.Runtime
	module  api.Module
	mu      sync.Mutex

	// Memory management
	malloc api.Function
	free   api.Function

	// Mnemonic functions
	mnemonicGenerate api.Function
	mnemonicValidate api.Function
	mnemonicToSeed   api.Function

	// Key derivation functions
	slip10Ed25519DerivePath api.Function
	ed25519PubkeyFromSeed   api.Function

	// Signing functions
	ed25519Sign   api.Function
	ed25519Verify api.Function

	// X25519 functions
	x25519Pubkey api.Function
	ecdhX25519   api.Function

	// Entropy management
	injectEntropy    api.Function
	getEntropyStatus api.Function

	// Version info
	getVersion api.Function
}

// NewHDWalletModule creates a new HDWalletModule from a WASM file path.
func NewHDWalletModule(ctx context.Context, wasmPath string) (*HDWalletModule, error) {
	if wasmPath == "" {
		return nil, fmt.Errorf("no WASM path provided: %w", ErrHDWalletNoModule)
	}

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read WASM file: %w", err)
	}

	return NewHDWalletModuleFromBytes(ctx, wasmBytes)
}

// NewHDWalletModuleFromBytes creates a new HDWalletModule from WASM bytes.
// NOTE: Requires a pure WASI build (built with wasi-sdk, not Emscripten).
// Emscripten builds require JS glue code and won't work with wazero.
func NewHDWalletModuleFromBytes(ctx context.Context, wasmBytes []byte) (*HDWalletModule, error) {
	// H8: Limit WASM memory to 256 pages (16MB) to prevent unbounded allocation.
	cfg := wazero.NewRuntimeConfig().WithMemoryLimitPages(256)
	r := wazero.NewRuntimeWithConfig(ctx, cfg)

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

	hw := &HDWalletModule{
		runtime: r,
		module:  module,
	}

	// Get exported functions
	hw.malloc = module.ExportedFunction("hd_alloc")
	hw.free = module.ExportedFunction("hd_dealloc")

	// Mnemonic functions
	hw.mnemonicGenerate = module.ExportedFunction("hd_mnemonic_generate")
	hw.mnemonicValidate = module.ExportedFunction("hd_mnemonic_validate")
	hw.mnemonicToSeed = module.ExportedFunction("hd_mnemonic_to_seed")

	// Key derivation
	hw.slip10Ed25519DerivePath = module.ExportedFunction("hd_slip10_ed25519_derive_path")
	hw.ed25519PubkeyFromSeed = module.ExportedFunction("hd_ed25519_pubkey_from_seed")

	// Signing
	hw.ed25519Sign = module.ExportedFunction("hd_ed25519_sign")
	hw.ed25519Verify = module.ExportedFunction("hd_ed25519_verify")

	// X25519
	hw.x25519Pubkey = module.ExportedFunction("hd_x25519_pubkey")
	hw.ecdhX25519 = module.ExportedFunction("hd_ecdh_x25519")

	// Entropy
	hw.injectEntropy = module.ExportedFunction("hd_inject_entropy")
	hw.getEntropyStatus = module.ExportedFunction("hd_get_entropy_status")

	// Version
	hw.getVersion = module.ExportedFunction("hd_get_version")

	return hw, nil
}

// Close releases the WASM runtime resources.
func (hw *HDWalletModule) Close(ctx context.Context) error {
	if hw.runtime != nil {
		return hw.runtime.Close(ctx)
	}
	return nil
}

// InjectEntropy injects entropy into the WASM module for random operations.
// Must be called before GenerateMnemonic in WASI environments.
func (hw *HDWalletModule) InjectEntropy(ctx context.Context, entropy []byte) error {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.injectEntropy == nil {
		return ErrHDWalletNoModule
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	entropyPtr, err := hw.allocate(ctx, entropy)
	if err != nil {
		return err
	}
	defer hw.deallocate(ctx, entropyPtr, uint32(len(entropy)))

	_, err = hw.injectEntropy.Call(ctx, uint64(entropyPtr), uint64(len(entropy)))
	return err
}

// HasEntropy checks if the WASM module has sufficient entropy.
func (hw *HDWalletModule) HasEntropy(ctx context.Context) (bool, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.getEntropyStatus == nil {
		return false, ErrHDWalletNoModule
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	results, err := hw.getEntropyStatus.Call(ctx)
	if err != nil {
		return false, err
	}

	// Status >= 2 means entropy is available
	return results[0] >= 2, nil
}

// GenerateMnemonic generates a BIP-39 mnemonic phrase.
// wordCount must be 12, 15, 18, 21, or 24.
// Returns the mnemonic as a space-separated string.
func (hw *HDWalletModule) GenerateMnemonic(ctx context.Context, wordCount int) (string, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.mnemonicGenerate == nil {
		return "", ErrHDWalletNoModule
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	// Allocate output buffer (max ~240 chars for 24-word mnemonic)
	outputSize := uint32(512)
	outputPtr, err := hw.allocateSize(ctx, outputSize)
	if err != nil {
		return "", err
	}
	defer hw.deallocate(ctx, outputPtr, outputSize)

	// Call: hd_mnemonic_generate(output, output_size, word_count, language)
	// language 0 = English
	results, err := hw.mnemonicGenerate.Call(ctx,
		uint64(outputPtr), uint64(outputSize),
		uint64(wordCount), uint64(0),
	)
	if err != nil {
		return "", fmt.Errorf("mnemonic generation failed: %w", err)
	}

	resultLen := int32(results[0])
	if resultLen < 0 {
		// Error code
		switch resultLen {
		case -1:
			return "", ErrHDWalletNoEntropy
		default:
			return "", fmt.Errorf("mnemonic generation error: %d", resultLen)
		}
	}

	// Read the mnemonic string
	mnemonic, err := hw.readString(ctx, outputPtr, uint32(resultLen))
	if err != nil {
		return "", err
	}

	return mnemonic, nil
}

// ValidateMnemonic validates a BIP-39 mnemonic phrase.
func (hw *HDWalletModule) ValidateMnemonic(ctx context.Context, mnemonic string) (bool, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.mnemonicValidate == nil {
		return false, ErrHDWalletNoModule
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	mnemonicPtr, err := hw.allocateString(ctx, mnemonic)
	if err != nil {
		return false, err
	}
	defer hw.deallocate(ctx, mnemonicPtr, uint32(len(mnemonic)+1))

	// Call: hd_mnemonic_validate(mnemonic, language)
	results, err := hw.mnemonicValidate.Call(ctx, uint64(mnemonicPtr), uint64(0))
	if err != nil {
		return false, err
	}

	// 0 = valid (Error::OK)
	return results[0] == 0, nil
}

// MnemonicToSeed converts a mnemonic to a 64-byte seed using PBKDF2.
func (hw *HDWalletModule) MnemonicToSeed(ctx context.Context, mnemonic, passphrase string) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.mnemonicToSeed == nil {
		return nil, ErrHDWalletNoModule
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	mnemonicPtr, err := hw.allocateString(ctx, mnemonic)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, mnemonicPtr, uint32(len(mnemonic)+1))

	passphrasePtr, err := hw.allocateString(ctx, passphrase)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, passphrasePtr, uint32(len(passphrase)+1))

	// Allocate output buffer for 64-byte seed
	seedSize := uint32(64)
	seedPtr, err := hw.allocateSize(ctx, seedSize)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, seedPtr, seedSize)

	// Call: hd_mnemonic_to_seed(mnemonic, passphrase, seed_out, seed_size)
	results, err := hw.mnemonicToSeed.Call(ctx,
		uint64(mnemonicPtr), uint64(passphrasePtr),
		uint64(seedPtr), uint64(seedSize),
	)
	if err != nil {
		return nil, fmt.Errorf("seed derivation failed: %w", err)
	}

	if results[0] != 0 {
		return nil, fmt.Errorf("seed derivation error: %d", int32(results[0]))
	}

	return hw.readMemory(ctx, seedPtr, seedSize)
}

// DerivedKey represents a derived Ed25519 key with chain code.
type DerivedKey struct {
	PrivateKey []byte // 32 bytes
	ChainCode  []byte // 32 bytes
}

// DeriveEd25519Key derives an Ed25519 key at the given path using SLIP-10.
// Path format: "m/44'/1957'/0'/0'/0'" (all components must be hardened for Ed25519)
func (hw *HDWalletModule) DeriveEd25519Key(ctx context.Context, seed []byte, path string) (*DerivedKey, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.slip10Ed25519DerivePath == nil {
		return nil, ErrHDWalletNoModule
	}

	if len(seed) != 64 {
		return nil, ErrHDWalletInvalidSeed
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	seedPtr, err := hw.allocate(ctx, seed)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, seedPtr, uint32(len(seed)))

	pathPtr, err := hw.allocateString(ctx, path)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, pathPtr, uint32(len(path)+1))

	// Allocate output buffers
	keySize := uint32(32)
	keyPtr, err := hw.allocateSize(ctx, keySize)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, keyPtr, keySize)

	chainCodePtr, err := hw.allocateSize(ctx, keySize)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, chainCodePtr, keySize)

	// Call: hd_slip10_ed25519_derive_path(seed, seed_len, path, key_out, chain_code_out)
	results, err := hw.slip10Ed25519DerivePath.Call(ctx,
		uint64(seedPtr), uint64(len(seed)),
		uint64(pathPtr),
		uint64(keyPtr), uint64(chainCodePtr),
	)
	if err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	if results[0] != 0 {
		return nil, fmt.Errorf("key derivation error: %d", int32(results[0]))
	}

	key, err := hw.readMemory(ctx, keyPtr, keySize)
	if err != nil {
		return nil, err
	}

	chainCode, err := hw.readMemory(ctx, chainCodePtr, keySize)
	if err != nil {
		return nil, err
	}

	return &DerivedKey{
		PrivateKey: key,
		ChainCode:  chainCode,
	}, nil
}

// Ed25519PublicKeyFromSeed derives Ed25519 public key from a 32-byte seed.
func (hw *HDWalletModule) Ed25519PublicKeyFromSeed(ctx context.Context, seed []byte) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.ed25519PubkeyFromSeed == nil {
		return nil, ErrHDWalletNoModule
	}

	if len(seed) != 32 {
		return nil, ErrHDWalletInvalidSeed
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	seedPtr, err := hw.allocate(ctx, seed)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, seedPtr, uint32(len(seed)))

	pubKeySize := uint32(32)
	pubKeyPtr, err := hw.allocateSize(ctx, pubKeySize)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, pubKeyPtr, pubKeySize)

	// Call: hd_ed25519_pubkey_from_seed(seed, public_key_out, public_key_size)
	results, err := hw.ed25519PubkeyFromSeed.Call(ctx,
		uint64(seedPtr),
		uint64(pubKeyPtr), uint64(pubKeySize),
	)
	if err != nil {
		return nil, fmt.Errorf("public key derivation failed: %w", err)
	}

	if results[0] != 0 {
		return nil, fmt.Errorf("public key derivation error: %d", int32(results[0]))
	}

	return hw.readMemory(ctx, pubKeyPtr, pubKeySize)
}

// Ed25519Sign signs a message using Ed25519.
// seed must be 32 bytes.
func (hw *HDWalletModule) Ed25519Sign(ctx context.Context, seed, message []byte) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.ed25519Sign == nil {
		return nil, ErrHDWalletNoModule
	}

	if len(seed) != 32 {
		return nil, ErrHDWalletInvalidSeed
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	seedPtr, err := hw.allocate(ctx, seed)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, seedPtr, uint32(len(seed)))

	msgPtr, err := hw.allocate(ctx, message)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, msgPtr, uint32(len(message)))

	sigSize := uint32(64)
	sigPtr, err := hw.allocateSize(ctx, sigSize)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, sigPtr, sigSize)

	// Call: hd_ed25519_sign(seed, seed_len, message, message_len, signature_out, signature_size)
	results, err := hw.ed25519Sign.Call(ctx,
		uint64(seedPtr), uint64(len(seed)),
		uint64(msgPtr), uint64(len(message)),
		uint64(sigPtr), uint64(sigSize),
	)
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	// Returns signature length (64) on success, negative on error
	if int32(results[0]) < 0 {
		return nil, ErrHDWalletSigningError
	}

	return hw.readMemory(ctx, sigPtr, sigSize)
}

// Ed25519Verify verifies an Ed25519 signature.
func (hw *HDWalletModule) Ed25519Verify(ctx context.Context, publicKey, message, signature []byte) (bool, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.ed25519Verify == nil {
		return false, ErrHDWalletNoModule
	}

	if len(publicKey) != 32 {
		return false, errors.New("invalid public key length")
	}
	if len(signature) != 64 {
		return false, errors.New("invalid signature length")
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	pubKeyPtr, err := hw.allocate(ctx, publicKey)
	if err != nil {
		return false, err
	}
	defer hw.deallocate(ctx, pubKeyPtr, uint32(len(publicKey)))

	msgPtr, err := hw.allocate(ctx, message)
	if err != nil {
		return false, err
	}
	defer hw.deallocate(ctx, msgPtr, uint32(len(message)))

	sigPtr, err := hw.allocate(ctx, signature)
	if err != nil {
		return false, err
	}
	defer hw.deallocate(ctx, sigPtr, uint32(len(signature)))

	// Call: hd_ed25519_verify(public_key, public_key_len, message, message_len, signature, signature_len)
	results, err := hw.ed25519Verify.Call(ctx,
		uint64(pubKeyPtr), uint64(len(publicKey)),
		uint64(msgPtr), uint64(len(message)),
		uint64(sigPtr), uint64(len(signature)),
	)
	if err != nil {
		return false, err
	}

	// Returns 1 for valid, 0 for invalid
	return results[0] == 1, nil
}

// X25519PublicKey derives the X25519 public key from a private key.
func (hw *HDWalletModule) X25519PublicKey(ctx context.Context, privateKey []byte) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.x25519Pubkey == nil {
		return nil, ErrHDWalletNoModule
	}

	if len(privateKey) != 32 {
		return nil, errors.New("invalid private key length")
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	privKeyPtr, err := hw.allocate(ctx, privateKey)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, privKeyPtr, uint32(len(privateKey)))

	pubKeySize := uint32(32)
	pubKeyPtr, err := hw.allocateSize(ctx, pubKeySize)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, pubKeyPtr, pubKeySize)

	// Call: hd_x25519_pubkey(private_key, public_key_out, public_key_size)
	results, err := hw.x25519Pubkey.Call(ctx,
		uint64(privKeyPtr),
		uint64(pubKeyPtr), uint64(pubKeySize),
	)
	if err != nil {
		return nil, fmt.Errorf("X25519 public key derivation failed: %w", err)
	}

	if results[0] != 0 {
		return nil, fmt.Errorf("X25519 public key derivation error: %d", int32(results[0]))
	}

	return hw.readMemory(ctx, pubKeyPtr, pubKeySize)
}

// X25519ECDH performs X25519 key exchange.
func (hw *HDWalletModule) X25519ECDH(ctx context.Context, privateKey, publicKey []byte) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.ecdhX25519 == nil {
		return nil, ErrHDWalletNoModule
	}

	if len(privateKey) != 32 || len(publicKey) != 32 {
		return nil, errors.New("invalid key length")
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	privKeyPtr, err := hw.allocate(ctx, privateKey)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, privKeyPtr, uint32(len(privateKey)))

	pubKeyPtr, err := hw.allocate(ctx, publicKey)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, pubKeyPtr, uint32(len(publicKey)))

	sharedSize := uint32(32)
	sharedPtr, err := hw.allocateSize(ctx, sharedSize)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, sharedPtr, sharedSize)

	// Call: hd_ecdh_x25519(private_key, public_key, shared_secret_out, shared_secret_size)
	results, err := hw.ecdhX25519.Call(ctx,
		uint64(privKeyPtr),
		uint64(pubKeyPtr),
		uint64(sharedPtr), uint64(sharedSize),
	)
	if err != nil {
		return nil, fmt.Errorf("X25519 ECDH failed: %w", err)
	}

	if results[0] != 0 {
		return nil, fmt.Errorf("X25519 ECDH error: %d", int32(results[0]))
	}

	return hw.readMemory(ctx, sharedPtr, sharedSize)
}

// GetVersion returns the WASM module version string.
func (hw *HDWalletModule) GetVersion(ctx context.Context) (string, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.getVersion == nil {
		return "", ErrHDWalletNoModule
	}

	// H9: Wrap context with execution timeout inside locked section.
	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	results, err := hw.getVersion.Call(ctx)
	if err != nil {
		return "", err
	}

	ptr := uint32(results[0])
	if ptr == 0 {
		return "", errors.New("failed to get version")
	}

	// Read null-terminated string
	return hw.readCString(ctx, ptr, 64)
}

// Memory management helpers

func (hw *HDWalletModule) allocate(ctx context.Context, data []byte) (uint32, error) {
	if hw.malloc == nil {
		return 0, ErrHDWalletNoModule
	}

	results, err := hw.malloc.Call(ctx, uint64(len(data)))
	if err != nil {
		return 0, err
	}

	ptr := uint32(results[0])
	if ptr == 0 {
		return 0, errors.New("allocation failed")
	}

	ok := hw.module.Memory().Write(ptr, data)
	if !ok {
		return 0, errors.New("failed to write to WASM memory")
	}

	return ptr, nil
}

func (hw *HDWalletModule) allocateSize(ctx context.Context, size uint32) (uint32, error) {
	if hw.malloc == nil {
		return 0, ErrHDWalletNoModule
	}

	results, err := hw.malloc.Call(ctx, uint64(size))
	if err != nil {
		return 0, err
	}

	ptr := uint32(results[0])
	if ptr == 0 {
		return 0, errors.New("allocation failed")
	}

	return ptr, nil
}

func (hw *HDWalletModule) allocateString(ctx context.Context, s string) (uint32, error) {
	// Add null terminator
	data := append([]byte(s), 0)
	// H10: Zero the temporary buffer after copying to WASM memory,
	// since it may contain sensitive material (mnemonics, passphrases).
	defer zeroBytes(data)
	return hw.allocate(ctx, data)
}

// deallocate frees WASM memory at the given pointer.
// M11: The size parameter is accepted for API consistency but is not passed to
// the WASM hd_dealloc function, which wraps libc free() and only requires the
// pointer. The allocator tracks block sizes internally.
func (hw *HDWalletModule) deallocate(ctx context.Context, ptr, size uint32) {
	if hw.free != nil {
		hw.free.Call(ctx, uint64(ptr))
	}
}

func (hw *HDWalletModule) readMemory(ctx context.Context, ptr, size uint32) ([]byte, error) {
	data, ok := hw.module.Memory().Read(ptr, size)
	if !ok {
		return nil, errors.New("failed to read from WASM memory")
	}
	result := make([]byte, size)
	copy(result, data)
	return result, nil
}

func (hw *HDWalletModule) readString(ctx context.Context, ptr, length uint32) (string, error) {
	data, err := hw.readMemory(ctx, ptr, length)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (hw *HDWalletModule) readCString(ctx context.Context, ptr, maxLen uint32) (string, error) {
	data, ok := hw.module.Memory().Read(ptr, maxLen)
	if !ok {
		return "", errors.New("failed to read from WASM memory")
	}

	// Find null terminator
	for i, b := range data {
		if b == 0 {
			return string(data[:i]), nil
		}
	}
	return string(data), nil
}

