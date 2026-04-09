// Package wasm provides WebAssembly integration for HD wallet operations.
package wasm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
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
	mod *wasmrt.Module
	mu  sync.Mutex
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
// NOTE: Requires a WASI-compatible build. Both wasi-sdk builds and
// Emscripten builds with -sSTANDALONE_WASM=1 -sPURE_WASI=1 work with WasmEdge.
// The Emscripten WASI build is preferred as it includes Crypto++ security hardening,
// HMAC-DRBG entropy mixing, MaskedKey protection, and optional FIPS mode.
func NewHDWalletModuleFromBytes(ctx context.Context, wasmBytes []byte) (*HDWalletModule, error) {
	mod, err := wasmrt.NewModule(wasmBytes,
		wasmrt.WithWASI(),
		wasmrt.WithMaxMemoryPages(512),
		wasmrt.WithMallocName("hd_alloc"),
		wasmrt.WithFreeName("hd_dealloc"),
		wasmrt.WithSecureDealloc("hd_secure_dealloc"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create WASM module: %w", err)
	}

	return &HDWalletModule{mod: mod}, nil
}

// Close releases the WASM runtime resources.
func (hw *HDWalletModule) Close(ctx context.Context) error {
	if hw.mod != nil {
		hw.mod.Release()
	}
	return nil
}

// InjectEntropy injects entropy into the WASM module for random operations.
// Must be called before GenerateMnemonic in WASI environments.
func (hw *HDWalletModule) InjectEntropy(ctx context.Context, entropy []byte) error {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	entropyPtr, err := hw.mod.Allocate(entropy)
	if err != nil {
		return err
	}
	defer hw.mod.SecureDeallocate(entropyPtr, uint32(len(entropy)))

	_, err = hw.mod.Execute("hd_inject_entropy", int32(entropyPtr), int32(len(entropy)))
	return err
}

// HasEntropy checks if the WASM module has sufficient entropy.
func (hw *HDWalletModule) HasEntropy(ctx context.Context) (bool, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	results, err := hw.mod.Execute("hd_get_entropy_status")
	if err != nil {
		return false, err
	}

	// Status >= 2 means entropy is available
	return wasmrt.ToInt32(results[0]) >= 2, nil
}

// GenerateMnemonic generates a BIP-39 mnemonic phrase.
// wordCount must be 12, 15, 18, 21, or 24.
// Returns the mnemonic as a space-separated string.
func (hw *HDWalletModule) GenerateMnemonic(ctx context.Context, wordCount int) (string, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	// Allocate output buffer (max ~240 chars for 24-word mnemonic)
	outputSize := uint32(512)
	outputPtr, err := hw.mod.AllocateSize(outputSize)
	if err != nil {
		return "", err
	}
	defer hw.mod.SecureDeallocate(outputPtr, outputSize)

	// Call: hd_mnemonic_generate(output, output_size, word_count, language)
	// language 0 = English
	results, err := hw.mod.Execute("hd_mnemonic_generate",
		int32(outputPtr), int32(outputSize),
		int32(wordCount), int32(0),
	)
	if err != nil {
		return "", fmt.Errorf("mnemonic generation failed: %w", err)
	}

	resultCode := wasmrt.ToInt32(results[0])
	if resultCode != 0 {
		switch {
		case resultCode == -1 || resultCode == -100 || resultCode == 100:
			return "", ErrHDWalletNoEntropy
		default:
			return "", fmt.Errorf("mnemonic generation error: %d", resultCode)
		}
	}

	return hw.mod.ReadCString(outputPtr, outputSize)
}

// ValidateMnemonic validates a BIP-39 mnemonic phrase.
func (hw *HDWalletModule) ValidateMnemonic(ctx context.Context, mnemonic string) (bool, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	mnemonicPtr, err := hw.mod.AllocateString(mnemonic)
	if err != nil {
		return false, err
	}
	defer hw.mod.SecureDeallocate(mnemonicPtr, uint32(len(mnemonic)+1))

	// Call: hd_mnemonic_validate(mnemonic, language)
	results, err := hw.mod.Execute("hd_mnemonic_validate", int32(mnemonicPtr), int32(0))
	if err != nil {
		return false, err
	}

	// 0 = valid (Error::OK)
	return wasmrt.ToInt32(results[0]) == 0, nil
}

// MnemonicToSeed converts a mnemonic to a 64-byte seed using PBKDF2.
func (hw *HDWalletModule) MnemonicToSeed(ctx context.Context, mnemonic, passphrase string) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	mnemonicPtr, err := hw.mod.AllocateString(mnemonic)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(mnemonicPtr, uint32(len(mnemonic)+1))

	passphrasePtr, err := hw.mod.AllocateString(passphrase)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(passphrasePtr, uint32(len(passphrase)+1))

	seedSize := uint32(64)
	seedPtr, err := hw.mod.AllocateSize(seedSize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(seedPtr, seedSize)

	results, err := hw.mod.Execute("hd_mnemonic_to_seed",
		int32(mnemonicPtr), int32(passphrasePtr),
		int32(seedPtr), int32(seedSize),
	)
	if err != nil {
		return nil, fmt.Errorf("seed derivation failed: %w", err)
	}

	if wasmrt.ToInt32(results[0]) != 0 {
		return nil, fmt.Errorf("seed derivation error: %d", wasmrt.ToInt32(results[0]))
	}

	return hw.mod.ReadMemory(seedPtr, seedSize)
}

// DerivedKey represents a derived Ed25519 key with chain code.
type DerivedKey struct {
	PrivateKey []byte // 32 bytes
	ChainCode  []byte // 32 bytes
}

// DeriveEd25519Key derives an Ed25519 key at the given path using SLIP-10 via WASM.
// Path format: "m/44'/0'/0'/0'/0'" (all components must be hardened for Ed25519)
func (hw *HDWalletModule) DeriveEd25519Key(ctx context.Context, seed []byte, path string) (*DerivedKey, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if len(seed) != 64 {
		return nil, ErrHDWalletInvalidSeed
	}

	seedPtr, err := hw.mod.Allocate(seed)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(seedPtr, uint32(len(seed)))

	pathPtr, err := hw.mod.AllocateString(path)
	if err != nil {
		return nil, err
	}
	defer hw.mod.Deallocate(pathPtr)

	keySize := uint32(32)
	keyPtr, err := hw.mod.AllocateSize(keySize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(keyPtr, keySize)

	chainCodePtr, err := hw.mod.AllocateSize(keySize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(chainCodePtr, keySize)

	results, err := hw.mod.Execute("hd_slip10_ed25519_derive_path",
		int32(seedPtr), int32(len(seed)),
		int32(pathPtr),
		int32(keyPtr),
		int32(chainCodePtr),
	)
	if err != nil {
		return nil, fmt.Errorf("SLIP-10 derivation failed: %w", err)
	}

	if wasmrt.ToInt32(results[0]) != 0 {
		return nil, fmt.Errorf("SLIP-10 derivation error: %d", wasmrt.ToInt32(results[0]))
	}

	privKey, err := hw.mod.ReadMemory(keyPtr, keySize)
	if err != nil {
		return nil, err
	}
	chainCode, err := hw.mod.ReadMemory(chainCodePtr, keySize)
	if err != nil {
		return nil, err
	}

	return &DerivedKey{PrivateKey: privKey, ChainCode: chainCode}, nil
}

// DeriveXPub derives a standard BIP-32 extended public key (xpub) from a seed.
// Uses secp256k1 curve at the given BIP-44 account path (e.g., m/44'/0'/0').
// Returns the Base58Check-encoded xpub string (starts with "xpub").
func (hw *HDWalletModule) DeriveXPub(ctx context.Context, seed []byte, account uint32) (string, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if len(seed) != 64 {
		return "", ErrHDWalletInvalidSeed
	}

	seedPtr, err := hw.mod.Allocate(seed)
	if err != nil {
		return "", err
	}
	defer hw.mod.SecureDeallocate(seedPtr, uint32(len(seed)))

	// Create master key from seed using secp256k1 (curve = 0)
	results, err := hw.mod.Execute("hd_key_from_seed", int32(seedPtr), int32(len(seed)), int32(0))
	if err != nil {
		return "", fmt.Errorf("hd_key_from_seed failed: %w", err)
	}
	masterHandle := wasmrt.ToInt32(results[0])
	if masterHandle == 0 {
		return "", fmt.Errorf("hd_key_from_seed returned null handle")
	}
	defer hw.mod.Execute("hd_key_destroy", masterHandle)

	// Derive account key at m/44'/0'/{account}'
	accountPath := fmt.Sprintf("m/44'/0'/%d'", account)
	pathPtr, err := hw.mod.AllocateString(accountPath)
	if err != nil {
		return "", err
	}
	defer hw.mod.Deallocate(pathPtr)

	results, err = hw.mod.Execute("hd_key_derive_path", masterHandle, int32(pathPtr))
	if err != nil {
		return "", fmt.Errorf("hd_key_derive_path failed: %w", err)
	}
	accountHandle := wasmrt.ToInt32(results[0])
	if accountHandle == 0 {
		return "", fmt.Errorf("hd_key_derive_path returned null handle")
	}
	defer hw.mod.Execute("hd_key_destroy", accountHandle)

	// Get neutered (public-only) key
	results, err = hw.mod.Execute("hd_key_neutered", accountHandle)
	if err != nil {
		return "", fmt.Errorf("hd_key_neutered failed: %w", err)
	}
	neuteredHandle := wasmrt.ToInt32(results[0])
	if neuteredHandle == 0 {
		return "", fmt.Errorf("hd_key_neutered returned null handle")
	}
	defer hw.mod.Execute("hd_key_destroy", neuteredHandle)

	// Serialize as xpub
	bufSize := uint32(128)
	bufPtr, err := hw.mod.AllocateSize(bufSize)
	if err != nil {
		return "", err
	}
	defer hw.mod.Deallocate(bufPtr)

	results, err = hw.mod.Execute("hd_key_serialize_xpub", neuteredHandle, int32(bufPtr), int32(bufSize))
	if err != nil {
		return "", fmt.Errorf("hd_key_serialize_xpub failed: %w", err)
	}
	if wasmrt.ToInt32(results[0]) != 0 {
		return "", fmt.Errorf("hd_key_serialize_xpub error: %d", wasmrt.ToInt32(results[0]))
	}

	return hw.mod.ReadCString(bufPtr, bufSize)
}

// DeriveSecp256k1Key derives a secp256k1 key at the given BIP-32 path.
// Returns the raw 32-byte private key and 32-byte chain code.
func (hw *HDWalletModule) DeriveSecp256k1Key(ctx context.Context, seed []byte, path string) (*DerivedKey, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if len(seed) != 64 {
		return nil, ErrHDWalletInvalidSeed
	}

	seedPtr, err := hw.mod.Allocate(seed)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(seedPtr, uint32(len(seed)))

	// Create master key from seed using secp256k1 (curve = 0)
	results, err := hw.mod.Execute("hd_key_from_seed", int32(seedPtr), int32(len(seed)), int32(0))
	if err != nil {
		return nil, fmt.Errorf("hd_key_from_seed failed: %w", err)
	}
	masterHandle := wasmrt.ToInt32(results[0])
	if masterHandle == 0 {
		return nil, fmt.Errorf("hd_key_from_seed returned null handle")
	}
	defer hw.mod.Execute("hd_key_destroy", masterHandle)

	pathPtr, err := hw.mod.AllocateString(path)
	if err != nil {
		return nil, err
	}
	defer hw.mod.Deallocate(pathPtr)

	results, err = hw.mod.Execute("hd_key_derive_path", masterHandle, int32(pathPtr))
	if err != nil {
		return nil, fmt.Errorf("hd_key_derive_path failed: %w", err)
	}
	derivedHandle := wasmrt.ToInt32(results[0])
	if derivedHandle == 0 {
		return nil, fmt.Errorf("hd_key_derive_path returned null handle")
	}
	defer hw.mod.Execute("hd_key_destroy", derivedHandle)

	// Extract 32-byte private key
	privSize := uint32(32)
	privPtr, err := hw.mod.AllocateSize(privSize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(privPtr, privSize)

	results, err = hw.mod.Execute("hd_key_get_private", derivedHandle, int32(privPtr), int32(privSize))
	if err != nil {
		return nil, fmt.Errorf("hd_key_get_private failed: %w", err)
	}
	if wasmrt.ToInt32(results[0]) != 0 {
		return nil, fmt.Errorf("hd_key_get_private error: %d", wasmrt.ToInt32(results[0]))
	}

	privKey, err := hw.mod.ReadMemory(privPtr, privSize)
	if err != nil {
		return nil, err
	}

	// Extract 32-byte chain code
	chainSize := uint32(32)
	chainPtr, err := hw.mod.AllocateSize(chainSize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(chainPtr, chainSize)

	results, err = hw.mod.Execute("hd_key_get_chain_code", derivedHandle, int32(chainPtr), int32(chainSize))
	if err != nil {
		return nil, fmt.Errorf("hd_key_get_chain_code failed: %w", err)
	}
	if wasmrt.ToInt32(results[0]) != 0 {
		return nil, fmt.Errorf("hd_key_get_chain_code error: %d", wasmrt.ToInt32(results[0]))
	}

	chainCode, err := hw.mod.ReadMemory(chainPtr, chainSize)
	if err != nil {
		return nil, err
	}

	return &DerivedKey{PrivateKey: privKey, ChainCode: chainCode}, nil
}

// Secp256k1PublicKey derives the compressed secp256k1 public key (33 bytes) at a BIP-32 path.
func (hw *HDWalletModule) Secp256k1PublicKey(ctx context.Context, seed []byte, path string) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if len(seed) != 64 {
		return nil, ErrHDWalletInvalidSeed
	}

	seedPtr, err := hw.mod.Allocate(seed)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(seedPtr, uint32(len(seed)))

	results, err := hw.mod.Execute("hd_key_from_seed", int32(seedPtr), int32(len(seed)), int32(0))
	if err != nil {
		return nil, fmt.Errorf("hd_key_from_seed failed: %w", err)
	}
	masterHandle := wasmrt.ToInt32(results[0])
	if masterHandle == 0 {
		return nil, fmt.Errorf("hd_key_from_seed returned null handle")
	}
	defer hw.mod.Execute("hd_key_destroy", masterHandle)

	pathPtr, err := hw.mod.AllocateString(path)
	if err != nil {
		return nil, err
	}
	defer hw.mod.Deallocate(pathPtr)

	results, err = hw.mod.Execute("hd_key_derive_path", masterHandle, int32(pathPtr))
	if err != nil {
		return nil, fmt.Errorf("hd_key_derive_path failed: %w", err)
	}
	derivedHandle := wasmrt.ToInt32(results[0])
	if derivedHandle == 0 {
		return nil, fmt.Errorf("hd_key_derive_path returned null handle")
	}
	defer hw.mod.Execute("hd_key_destroy", derivedHandle)

	// Extract 33-byte compressed public key
	pubSize := uint32(33)
	pubPtr, err := hw.mod.AllocateSize(pubSize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.Deallocate(pubPtr)

	results, err = hw.mod.Execute("hd_key_get_public", derivedHandle, int32(pubPtr), int32(pubSize))
	if err != nil {
		return nil, fmt.Errorf("hd_key_get_public failed: %w", err)
	}
	if wasmrt.ToInt32(results[0]) != 0 {
		return nil, fmt.Errorf("hd_key_get_public error: %d", wasmrt.ToInt32(results[0]))
	}

	return hw.mod.ReadMemory(pubPtr, pubSize)
}

// Ed25519PublicKeyFromSeed derives Ed25519 public key from a 32-byte seed via WASM.
func (hw *HDWalletModule) Ed25519PublicKeyFromSeed(ctx context.Context, seed []byte) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if len(seed) != 32 {
		return nil, ErrHDWalletInvalidSeed
	}

	seedPtr, err := hw.mod.Allocate(seed)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(seedPtr, uint32(len(seed)))

	pubSize := uint32(32)
	pubPtr, err := hw.mod.AllocateSize(pubSize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.Deallocate(pubPtr)

	results, err := hw.mod.Execute("hd_ed25519_pubkey_from_seed",
		int32(seedPtr),
		int32(pubPtr), int32(pubSize),
	)
	if err != nil {
		return nil, fmt.Errorf("Ed25519 pubkey derivation failed: %w", err)
	}

	if wasmrt.ToInt32(results[0]) != 0 {
		return nil, fmt.Errorf("Ed25519 pubkey derivation error: %d", wasmrt.ToInt32(results[0]))
	}

	return hw.mod.ReadMemory(pubPtr, pubSize)
}

// Ed25519Sign signs a message using Ed25519.
// seed must be 32 bytes.
func (hw *HDWalletModule) Ed25519Sign(ctx context.Context, seed, message []byte) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if len(seed) != 32 {
		return nil, ErrHDWalletInvalidSeed
	}

	seedPtr, err := hw.mod.Allocate(seed)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(seedPtr, uint32(len(seed)))

	msgPtr, err := hw.mod.Allocate(message)
	if err != nil {
		return nil, err
	}
	defer hw.mod.Deallocate(msgPtr)

	sigSize := uint32(64)
	sigPtr, err := hw.mod.AllocateSize(sigSize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(sigPtr, sigSize)

	// Call: hd_ed25519_sign(message, message_len, private_key, signature_out, out_size)
	results, err := hw.mod.Execute("hd_ed25519_sign",
		int32(msgPtr), int32(len(message)),
		int32(seedPtr),
		int32(sigPtr), int32(sigSize),
	)
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	// Returns signature length (64) on success, negative on error
	if wasmrt.ToInt32(results[0]) < 0 {
		return nil, ErrHDWalletSigningError
	}

	return hw.mod.ReadMemory(sigPtr, sigSize)
}

// Ed25519Verify verifies an Ed25519 signature.
func (hw *HDWalletModule) Ed25519Verify(ctx context.Context, publicKey, message, signature []byte) (bool, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if len(publicKey) != 32 {
		return false, errors.New("invalid public key length")
	}
	if len(signature) != 64 {
		return false, errors.New("invalid signature length")
	}

	pubKeyPtr, err := hw.mod.Allocate(publicKey)
	if err != nil {
		return false, err
	}
	defer hw.mod.Deallocate(pubKeyPtr)

	msgPtr, err := hw.mod.Allocate(message)
	if err != nil {
		return false, err
	}
	defer hw.mod.Deallocate(msgPtr)

	sigPtr, err := hw.mod.Allocate(signature)
	if err != nil {
		return false, err
	}
	defer hw.mod.Deallocate(sigPtr)

	// Call: hd_ed25519_verify(message, message_len, signature, signature_len, public_key, public_key_len)
	results, err := hw.mod.Execute("hd_ed25519_verify",
		int32(msgPtr), int32(len(message)),
		int32(sigPtr), int32(len(signature)),
		int32(pubKeyPtr), int32(len(publicKey)),
	)
	if err != nil {
		return false, err
	}

	// Returns 1 for valid, 0 for invalid
	return wasmrt.ToInt32(results[0]) == 1, nil
}

// X25519PublicKey derives the X25519 public key from a private key via WASM.
func (hw *HDWalletModule) X25519PublicKey(ctx context.Context, privateKey []byte) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if len(privateKey) != 32 {
		return nil, errors.New("invalid private key length")
	}

	privPtr, err := hw.mod.Allocate(privateKey)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(privPtr, uint32(len(privateKey)))

	pubSize := uint32(32)
	pubPtr, err := hw.mod.AllocateSize(pubSize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.Deallocate(pubPtr)

	results, err := hw.mod.Execute("hd_x25519_pubkey",
		int32(privPtr),
		int32(pubPtr), int32(pubSize),
	)
	if err != nil {
		return nil, fmt.Errorf("X25519 pubkey derivation failed: %w", err)
	}

	if wasmrt.ToInt32(results[0]) != 0 {
		return nil, fmt.Errorf("X25519 pubkey derivation error: %d", wasmrt.ToInt32(results[0]))
	}

	return hw.mod.ReadMemory(pubPtr, pubSize)
}

// X25519ECDH performs X25519 key exchange.
func (hw *HDWalletModule) X25519ECDH(ctx context.Context, privateKey, publicKey []byte) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if len(privateKey) != 32 || len(publicKey) != 32 {
		return nil, errors.New("invalid key length")
	}

	privKeyPtr, err := hw.mod.Allocate(privateKey)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(privKeyPtr, uint32(len(privateKey)))

	pubKeyPtr, err := hw.mod.Allocate(publicKey)
	if err != nil {
		return nil, err
	}
	defer hw.mod.Deallocate(pubKeyPtr)

	sharedSize := uint32(32)
	sharedPtr, err := hw.mod.AllocateSize(sharedSize)
	if err != nil {
		return nil, err
	}
	defer hw.mod.SecureDeallocate(sharedPtr, sharedSize)

	// Call: hd_ecdh_x25519(private_key, public_key, shared_secret_out, shared_secret_size)
	results, err := hw.mod.Execute("hd_ecdh_x25519",
		int32(privKeyPtr),
		int32(pubKeyPtr),
		int32(sharedPtr), int32(sharedSize),
	)
	if err != nil {
		return nil, fmt.Errorf("X25519 ECDH failed: %w", err)
	}

	if wasmrt.ToInt32(results[0]) != 0 {
		return nil, fmt.Errorf("X25519 ECDH error: %d", wasmrt.ToInt32(results[0]))
	}

	return hw.mod.ReadMemory(sharedPtr, sharedSize)
}

// GetVersion returns the WASM module version string.
func (hw *HDWalletModule) GetVersion(ctx context.Context) (string, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	results, err := hw.mod.Execute("hd_get_version")
	if err != nil {
		return "", err
	}

	ptr := wasmrt.ToInt32(results[0])
	if ptr == 0 {
		return "", errors.New("failed to get version")
	}

	return hw.mod.ReadCString(uint32(ptr), 64)
}
