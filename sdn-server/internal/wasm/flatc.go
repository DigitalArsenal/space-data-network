// Package wasm provides WebAssembly integration for FlatBuffers operations.
package wasm

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// ErrNoModule is returned when the WASM module is not loaded.
var ErrNoModule = errors.New("WASM module not loaded")

// The node's JSON⇄FlatBuffer converter is the DA flatbuffers fork's standalone
// WASI build of flatc (CMake target flatc_wasi, src/flatc_wasm_wasi.cpp; ABI
// contract in that repo's wasm/WASI.md). It imports only
// wasi_snapshot_preview1, so WasmEdge instantiates it with no JavaScript glue.
//
// Provenance (published-deps law — the bytes come from the npm package, never
// a sibling checkout):
//
//   - npm: flatc-wasm@25.12.19-wasm.69 (dist-tag "wasm"), file
//     dist/flatc-wasi.wasm, consumed through the root package.json alias
//     "flatc-wasi": "npm:flatc-wasm@25.12.19-wasm.69"
//   - source: DigitalArsenal/flatbuffers master c72a8bed
//   - sha256: FlatcWasiSHA256 below, equal to the package's
//     dist/flatc-wasi.wasm.sha256
//
// To move the pin: bump the alias in package.json, `npm install`, run
// `npm run sync:flatc-wasi` (copies and verifies the artifact), then update
// FlatcWasiPackage and FlatcWasiSHA256. TestEmbeddedFlatcArtifact asserts the
// embed, the constants and package.json agree.
//
//go:embed flatc-wasi.wasm
var flatcWasiWasm []byte

const (
	// FlatcWasiPackage is the npm package the embedded converter comes from.
	FlatcWasiPackage = "flatc-wasm@25.12.19-wasm.69"
	// FlatcWasiSHA256 is the sha256 of the embedded flatc-wasi.wasm.
	FlatcWasiSHA256 = "eaa09b5ecc5c520a716f308b3a659dd05803febf8e60915b3812e2a71a517b73"
	// FlatcABIVersion is the converter ABI this wrapper speaks.
	FlatcABIVersion = 1
)

// EmbeddedFlatcWasm returns the embedded converter bytes.
func EmbeddedFlatcWasm() []byte { return flatcWasiWasm }

// FlatcOption is a per-call conversion option bit (WASI.md "Options").
type FlatcOption uint32

const (
	// FlatcSizePrefixed: json_to_binary writes a 4-byte length prefix;
	// binary_to_json expects one equal to len-4.
	FlatcSizePrefixed FlatcOption = 1 << 0
	// FlatcForceDefaults: json_to_binary stores fields present in the JSON even
	// when equal to their default; binary_to_json prints every scalar.
	FlatcForceDefaults FlatcOption = 1 << 1
	// FlatcStrictJSON rejects non-standard JSON input.
	FlatcStrictJSON FlatcOption = 1 << 2
	// FlatcNaturalUTF8 prints non-ASCII as UTF-8 instead of \u escapes.
	FlatcNaturalUTF8 FlatcOption = 1 << 3
	// FlatcSkipUnknownFields ignores JSON fields the schema lacks.
	FlatcSkipUnknownFields FlatcOption = 1 << 4
	// FlatcCompactJSON prints without indentation or newlines.
	FlatcCompactJSON FlatcOption = 1 << 5
)

// FlatcStatus is a converter status code (WASI.md "Status codes").
type FlatcStatus int32

const (
	FlatcOK              FlatcStatus = 0
	FlatcInvalidArgument FlatcStatus = -1
	FlatcSchemaNotFound  FlatcStatus = -2
	FlatcSchemaParse     FlatcStatus = -3
	FlatcNoRootType      FlatcStatus = -4
	FlatcJSONParse       FlatcStatus = -5
	FlatcInvalidBinary   FlatcStatus = -6
	FlatcJSONGeneration  FlatcStatus = -7
	FlatcBufferTooSmall  FlatcStatus = -8
	FlatcFileNotFound    FlatcStatus = -9
	FlatcUnknownOption   FlatcStatus = -10
)

var flatcStatusNames = map[FlatcStatus]string{
	FlatcOK:              "OK",
	FlatcInvalidArgument: "INVALID_ARGUMENT",
	FlatcSchemaNotFound:  "SCHEMA_NOT_FOUND",
	FlatcSchemaParse:     "SCHEMA_PARSE",
	FlatcNoRootType:      "NO_ROOT_TYPE",
	FlatcJSONParse:       "JSON_PARSE",
	FlatcInvalidBinary:   "INVALID_BINARY",
	FlatcJSONGeneration:  "JSON_GENERATION",
	FlatcBufferTooSmall:  "BUFFER_TOO_SMALL",
	FlatcFileNotFound:    "FILE_NOT_FOUND",
	FlatcUnknownOption:   "UNKNOWN_OPTION",
}

func (s FlatcStatus) String() string {
	if name, ok := flatcStatusNames[s]; ok {
		return name
	}
	return fmt.Sprintf("STATUS_%d", int32(s))
}

// FlatcError is a converter call that returned a negative status. Message is
// the module's own last-error text.
type FlatcError struct {
	Op      string
	Status  FlatcStatus
	Message string
}

func (e *FlatcError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("flatc %s: %s", e.Op, e.Status)
	}
	return fmt.Sprintf("flatc %s: %s: %s", e.Op, e.Status, e.Message)
}

// FlatcStatusOf returns the converter status carried by err, if any.
func FlatcStatusOf(err error) (FlatcStatus, bool) {
	var fe *FlatcError
	if errors.As(err, &fe) {
		return fe.Status, true
	}
	return 0, false
}

// flatcRequiredExports is the ABI 1 surface this wrapper calls.
var flatcRequiredExports = []string{
	"_initialize", "malloc", "free",
	"flatc_abi_version", "flatc_version",
	"flatc_last_error_ptr", "flatc_last_error_len",
	"flatc_vfs_put", "flatc_vfs_remove",
	"flatc_schema_add", "flatc_schema_remove",
	"flatc_json_to_binary", "flatc_binary_to_json",
}

const (
	// maxRetainedGuestBuffer bounds the scratch buffers kept between calls; a
	// larger one (one oversized record) is released after the call.
	maxRetainedGuestBuffer = 4 << 20
	// minGuestBuffer is the smallest scratch allocation.
	minGuestBuffer = 1024
)

// FlatcModule is one instance of the converter. Calls are serialized: the
// module is single-threaded (WASI.md "Loading").
type FlatcModule struct {
	mod *wasmrt.Module
	mu  sync.Mutex

	version string
	schemas map[string]int // schema path -> converter schema id

	// Reusable guest scratch: inputs are staged into in, results land in out,
	// and outLen receives *out_len.
	inPtr, inCap   uint32
	outPtr, outCap uint32
	outLenPtr      uint32

	// retries counts BUFFER_TOO_SMALL round trips (the out_len contract).
	retries uint64
}

// NewEmbeddedFlatcModule instantiates the embedded converter, checking its
// sha256 against the pin before anything runs.
func NewEmbeddedFlatcModule(ctx context.Context) (*FlatcModule, error) {
	return loadFlatcModule(ctx, flatcWasiWasm, FlatcWasiSHA256, "embedded flatc-wasi.wasm ("+FlatcWasiPackage+")")
}

// NewFlatcModule instantiates a converter artifact from a file. An empty path
// or an unreadable file is an error; there is no silent fallback.
func NewFlatcModule(ctx context.Context, wasmPath string) (*FlatcModule, error) {
	if wasmPath == "" {
		return nil, fmt.Errorf("no flatc converter path provided: %w", ErrNoModule)
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("flatc converter %s: %w (%v)", wasmPath, ErrNoModule, err)
	}
	return loadFlatcModule(ctx, wasmBytes, "", wasmPath)
}

// NewFlatcModuleFromBytes instantiates converter bytes the caller supplies.
// Only the ABI is checked; pin the digest with NewEmbeddedFlatcModule.
func NewFlatcModuleFromBytes(ctx context.Context, wasmBytes []byte) (*FlatcModule, error) {
	return loadFlatcModule(ctx, wasmBytes, "", "caller-supplied flatc converter")
}

func loadFlatcModule(ctx context.Context, wasmBytes []byte, wantSHA256, source string) (*FlatcModule, error) {
	if len(wasmBytes) == 0 {
		return nil, fmt.Errorf("%s is missing (0 bytes): %w", source, ErrNoModule)
	}
	if wantSHA256 != "" {
		sum := sha256.Sum256(wasmBytes)
		if got := hex.EncodeToString(sum[:]); got != wantSHA256 {
			return nil, fmt.Errorf("%s is corrupt: sha256 %s, want %s: %w", source, got, wantSHA256, ErrNoModule)
		}
	}

	mod, err := wasmrt.NewModule(wasmBytes, wasmrt.WithWASI())
	if err != nil {
		return nil, fmt.Errorf("%s failed to instantiate: %v: %w", source, err, ErrNoModule)
	}
	fm := &FlatcModule{mod: mod, schemas: make(map[string]int)}
	fail := func(err error) (*FlatcModule, error) {
		mod.Release()
		return nil, fmt.Errorf("%s: %v: %w", source, err, ErrNoModule)
	}

	var missing []string
	for _, name := range flatcRequiredExports {
		if !mod.HasFunction(name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fail(fmt.Errorf("not a flatc ABI %d converter: missing exports %s", FlatcABIVersion, strings.Join(missing, ", ")))
	}
	if _, err := mod.ExecuteContext(ctx, "_initialize"); err != nil {
		return fail(fmt.Errorf("_initialize: %w", err))
	}
	res, err := mod.ExecuteContext(ctx, "flatc_abi_version")
	if err != nil {
		return fail(fmt.Errorf("flatc_abi_version: %w", err))
	}
	if abi := uint32(wasmrt.ToInt32(res[0])); abi != FlatcABIVersion {
		return fail(fmt.Errorf("converter ABI %d, this node speaks ABI %d", abi, FlatcABIVersion))
	}
	res, err = mod.ExecuteContext(ctx, "flatc_version")
	if err != nil {
		return fail(fmt.Errorf("flatc_version: %w", err))
	}
	if fm.version, err = mod.ReadCString(uint32(wasmrt.ToInt32(res[0])), 64); err != nil {
		return fail(fmt.Errorf("flatc_version: %w", err))
	}
	if fm.outLenPtr, err = mod.AllocateSize(4); err != nil {
		return fail(fmt.Errorf("allocate out_len: %w", err))
	}
	return fm, nil
}

// Version returns the FlatBuffers version the converter was built from.
func (fm *FlatcModule) Version() string {
	if fm == nil {
		return ""
	}
	return fm.version
}

// Retries reports how many conversions needed the BUFFER_TOO_SMALL retry.
func (fm *FlatcModule) Retries() uint64 {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	return fm.retries
}

// Close releases the WASM runtime resources.
func (fm *FlatcModule) Close(ctx context.Context) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.mod != nil {
		fm.mod.Release()
		fm.mod = nil
	}
	return nil
}

// PutFile adds or replaces a file in the converter's include file map.
func (fm *FlatcModule) PutFile(ctx context.Context, path string, data []byte) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.mod == nil {
		return ErrNoModule
	}
	ptrs, err := fm.stage(path, string(data))
	if err != nil {
		return err
	}
	return fm.call(ctx, "vfs_put "+path, "flatc_vfs_put",
		int32(ptrs[0]), int32(len(path)), int32(ptrs[1]), int32(len(data)))
}

// RemoveFile removes a file from the converter's include file map.
func (fm *FlatcModule) RemoveFile(ctx context.Context, path string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.mod == nil {
		return ErrNoModule
	}
	ptrs, err := fm.stage(path)
	if err != nil {
		return err
	}
	return fm.call(ctx, "vfs_remove "+path, "flatc_vfs_remove", int32(ptrs[0]), int32(len(path)))
}

// AddSchema parses content as the schema file at path and returns its id.
// Includes resolve relative to path, then from the file-map root, so SDS
// schemas go in at "<FAMILY>/main.fbs" (their includes name
// "../MET/main.fbs"). With empty content the source is read from the file
// map at path. A schema that does not parse is an error; no id is returned
// for anything the converter did not load.
func (fm *FlatcModule) AddSchema(ctx context.Context, path string, content []byte) (int, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.mod == nil {
		return 0, ErrNoModule
	}
	ptrs, err := fm.stage(path, string(content))
	if err != nil {
		return 0, err
	}
	res, err := fm.mod.ExecuteContext(ctx, "flatc_schema_add",
		int32(ptrs[0]), int32(len(path)), int32(ptrs[1]), int32(len(content)))
	if err != nil {
		return 0, fmt.Errorf("flatc schema_add %s: %w", path, err)
	}
	id := wasmrt.ToInt32(res[0])
	if id <= 0 {
		return 0, fm.lastError("schema_add "+path, FlatcStatus(id))
	}
	fm.schemas[path] = int(id)
	return int(id), nil
}

// RemoveSchema drops a schema the converter holds.
func (fm *FlatcModule) RemoveSchema(ctx context.Context, schemaID int) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.mod == nil {
		return ErrNoModule
	}
	if err := fm.call(ctx, "schema_remove", "flatc_schema_remove", int32(schemaID)); err != nil {
		return err
	}
	for path, id := range fm.schemas {
		if id == schemaID {
			delete(fm.schemas, path)
		}
	}
	return nil
}

// JSONToBinary converts JSON to a FlatBuffer with the given per-call options.
// Fields are laid out by size (WASI.md "Layout"): values round-trip, but bytes
// written by another builder are not reproduced byte for byte.
func (fm *FlatcModule) JSONToBinary(ctx context.Context, schemaID int, jsonData []byte, opts FlatcOption) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.mod == nil {
		return nil, ErrNoModule
	}
	guess := 2 * uint32(len(jsonData))
	return fm.convert(ctx, "json_to_binary", "flatc_json_to_binary", schemaID, jsonData, opts, guess, true)
}

// BinaryToJSON verifies a FlatBuffer against the schema and prints it as JSON.
func (fm *FlatcModule) BinaryToJSON(ctx context.Context, schemaID int, binaryData []byte, opts FlatcOption) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.mod == nil {
		return nil, ErrNoModule
	}
	guess := 4 * uint32(len(binaryData))
	return fm.convert(ctx, "binary_to_json", "flatc_binary_to_json", schemaID, binaryData, opts, guess, true)
}

// VerifyBinary runs binary_to_json's full verification (reflection verifier,
// then nested buffers and unions) as a size query, so no JSON is copied out.
// It returns nil when the buffer verifies and prints.
func (fm *FlatcModule) VerifyBinary(ctx context.Context, schemaID int, binaryData []byte, opts FlatcOption) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.mod == nil {
		return ErrNoModule
	}
	_, err := fm.convert(ctx, "binary_to_json", "flatc_binary_to_json", schemaID, binaryData, opts, 0, false)
	return err
}

// convert runs one conversion export under the out_len contract: the first
// attempt uses the retained buffer (grown to guess), and BUFFER_TOO_SMALL is
// retried once at exactly *out_len. With copyOut false it is a size query.
func (fm *FlatcModule) convert(ctx context.Context, op, export string, schemaID int, input []byte, opts FlatcOption, guess uint32, copyOut bool) ([]byte, error) {
	ptrs, err := fm.stage(string(input))
	if err != nil {
		return nil, err
	}
	defer fm.trimScratch()

	var outPtr, outCap uint32
	if copyOut {
		if err := fm.ensureOut(max(guess, minGuestBuffer)); err != nil {
			return nil, err
		}
		outPtr, outCap = fm.outPtr, fm.outCap
	}
	for attempt := 0; attempt < 2; attempt++ {
		res, err := fm.mod.ExecuteContext(ctx, export,
			int32(schemaID), int32(ptrs[0]), int32(len(input)), int32(uint32(opts)),
			int32(outPtr), int32(outCap), int32(fm.outLenPtr))
		if err != nil {
			return nil, fmt.Errorf("flatc %s: %w", op, err)
		}
		status := FlatcStatus(wasmrt.ToInt32(res[0]))
		if status != FlatcOK && status != FlatcBufferTooSmall {
			return nil, fm.lastError(op, status)
		}
		lenBytes, err := fm.mod.ReadMemory(fm.outLenPtr, 4)
		if err != nil {
			return nil, fmt.Errorf("flatc %s: read out_len: %w", op, err)
		}
		n := binary.LittleEndian.Uint32(lenBytes)
		if !copyOut {
			// A size query answers BUFFER_TOO_SMALL for any non-empty result,
			// OK for an empty one; both mean the input verified and printed.
			return nil, nil
		}
		if status == FlatcOK {
			if n > outCap {
				return nil, fmt.Errorf("flatc %s: out_len %d exceeds buffer %d", op, n, outCap)
			}
			return fm.mod.ReadMemory(outPtr, n)
		}
		fm.retries++
		if err := fm.ensureOut(n); err != nil {
			return nil, err
		}
		outPtr, outCap = fm.outPtr, fm.outCap
	}
	return nil, &FlatcError{Op: op, Status: FlatcBufferTooSmall, Message: "result still larger than out_len after retry"}
}

// stage writes parts contiguously into the input scratch buffer and returns a
// pointer per part (0 for an empty part).
func (fm *FlatcModule) stage(parts ...string) ([]uint32, error) {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	ptrs := make([]uint32, len(parts))
	if total == 0 {
		return ptrs, nil
	}
	if uint32(total) > fm.inCap {
		if fm.inPtr != 0 {
			fm.mod.Deallocate(fm.inPtr)
			fm.inPtr, fm.inCap = 0, 0
		}
		size := max(uint32(total), minGuestBuffer)
		ptr, err := fm.mod.AllocateSize(size)
		if err != nil {
			return nil, fmt.Errorf("flatc: allocate %d input bytes: %w", size, err)
		}
		fm.inPtr, fm.inCap = ptr, size
	}
	buf := make([]byte, 0, total)
	off := fm.inPtr
	for i, p := range parts {
		if len(p) > 0 {
			ptrs[i] = off
		}
		buf = append(buf, p...)
		off += uint32(len(p))
	}
	if err := fm.mod.WriteMemory(fm.inPtr, buf); err != nil {
		return nil, fmt.Errorf("flatc: stage input: %w", err)
	}
	return ptrs, nil
}

// ensureOut makes the output scratch buffer at least size bytes.
func (fm *FlatcModule) ensureOut(size uint32) error {
	if size <= fm.outCap {
		return nil
	}
	if fm.outPtr != 0 {
		fm.mod.Deallocate(fm.outPtr)
		fm.outPtr, fm.outCap = 0, 0
	}
	ptr, err := fm.mod.AllocateSize(size)
	if err != nil {
		return fmt.Errorf("flatc: allocate %d output bytes: %w", size, err)
	}
	fm.outPtr, fm.outCap = ptr, size
	return nil
}

// trimScratch releases scratch buffers an oversized record grew past the
// retention bound.
func (fm *FlatcModule) trimScratch() {
	if fm.mod == nil {
		return
	}
	if fm.inCap > maxRetainedGuestBuffer {
		fm.mod.Deallocate(fm.inPtr)
		fm.inPtr, fm.inCap = 0, 0
	}
	if fm.outCap > maxRetainedGuestBuffer {
		fm.mod.Deallocate(fm.outPtr)
		fm.outPtr, fm.outCap = 0, 0
	}
}

// call runs an export that returns a status.
func (fm *FlatcModule) call(ctx context.Context, op, export string, params ...interface{}) error {
	res, err := fm.mod.ExecuteContext(ctx, export, params...)
	if err != nil {
		return fmt.Errorf("flatc %s: %w", op, err)
	}
	if status := FlatcStatus(wasmrt.ToInt32(res[0])); status != FlatcOK {
		return fm.lastError(op, status)
	}
	return nil
}

// lastError builds a FlatcError from the module's last-error message.
func (fm *FlatcModule) lastError(op string, status FlatcStatus) error {
	fe := &FlatcError{Op: op, Status: status}
	ptrRes, err := fm.mod.Execute("flatc_last_error_ptr")
	if err != nil {
		return fe
	}
	lenRes, err := fm.mod.Execute("flatc_last_error_len")
	if err != nil {
		return fe
	}
	n := uint32(wasmrt.ToInt32(lenRes[0]))
	if n == 0 {
		return fe
	}
	if n > 64<<10 {
		n = 64 << 10
	}
	if msg, err := fm.mod.ReadMemory(uint32(wasmrt.ToInt32(ptrRes[0])), n); err == nil {
		fe.Message = string(msg)
	}
	return fe
}

// Encrypt, Decrypt, Sign and Verify call the crypto exports of the fork's
// separate flatc-encryption.wasm. No node code path loads that artifact and
// the converter does not export them, so on the converter they fail with
// ErrNoModule rather than a lookup error.

// Encrypt encrypts data using AES-GCM.
func (fm *FlatcModule) Encrypt(ctx context.Context, key, plaintext []byte) ([]byte, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if err := fm.requireExport("wasi_encrypt_bytes"); err != nil {
		return nil, err
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

	if err := fm.requireExport("wasi_decrypt_bytes"); err != nil {
		return nil, err
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

	if err := fm.requireExport("wasi_ed25519_sign"); err != nil {
		return nil, err
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

	if err := fm.requireExport("wasi_ed25519_verify"); err != nil {
		return false, err
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

// requireExport reports ErrNoModule when no module is loaded or the loaded
// artifact lacks export.
func (fm *FlatcModule) requireExport(export string) error {
	if fm.mod == nil {
		return ErrNoModule
	}
	if !fm.mod.HasFunction(export) {
		return fmt.Errorf("%w: the loaded artifact does not export %s", ErrNoModule, export)
	}
	return nil
}

// GetSchemaID returns the id of a schema added at path.
func (fm *FlatcModule) GetSchemaID(path string) (int, bool) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	id, ok := fm.schemas[path]
	return id, ok
}
