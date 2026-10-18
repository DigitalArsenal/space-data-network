// Package sds provides Space Data Standards validation and schema handling.
package sds

import (
	"context"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/fbverify"
	"github.com/spacedatanetwork/sdn-server/internal/storage/recordalign"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// Schema name validation constants
const (
	// MaxSchemaNameLength is the maximum allowed length for a schema name
	MaxSchemaNameLength = 64
)

// Schema name validation errors
var (
	// ErrSchemaNameEmpty is returned when the schema name is empty
	ErrSchemaNameEmpty = errors.New("schema name cannot be empty")
	// ErrSchemaNameTooLong is returned when the schema name exceeds the maximum length
	ErrSchemaNameTooLong = errors.New("schema name exceeds maximum length")
	// ErrSchemaNameInvalidChars is returned when the schema name contains invalid characters
	ErrSchemaNameInvalidChars = errors.New("schema name contains invalid characters (only alphanumeric, dots, and underscores allowed)")
	// ErrSchemaNamePathTraversal is returned when the schema name contains path traversal sequences
	ErrSchemaNamePathTraversal = errors.New("schema name contains path traversal sequences")
)

// validSchemaNameRegex matches valid schema names: alphanumeric, dots, and underscores only
var validSchemaNameRegex = regexp.MustCompile(`^[a-zA-Z0-9._]+$`)

// ValidateSchemaName validates a schema name to prevent path traversal attacks,
// SQL injection through table names, and other security issues.
// Valid schema names:
// - Are not empty
// - Are at most MaxSchemaNameLength characters
// - Contain only alphanumeric characters, dots, and underscores
// - Do not contain path separators or traversal sequences
func ValidateSchemaName(name string) error {
	// Check for empty name
	if name == "" {
		return ErrSchemaNameEmpty
	}

	// Check maximum length
	if len(name) > MaxSchemaNameLength {
		return ErrSchemaNameTooLong
	}

	// Check for path traversal sequences (before character validation for better error messages)
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return ErrSchemaNamePathTraversal
	}

	// Check for valid characters (alphanumeric, dots, underscores only)
	if !validSchemaNameRegex.MatchString(name) {
		return ErrSchemaNameInvalidChars
	}

	return nil
}

var log = logging.Logger("sds")

//go:embed schemas/*.fbs
var schemasFS embed.FS

// SupportedSchemas is every schema file the node embeds, sorted: every Space
// Data Standards (spacedatastandards.org) standard plus the SDN-internal
// schemas (PGR, PLHD, PLOG, RHD). The embedded directory is the one authority
// for the standard set; adding a standard is copying its IDL into schemas/.
var SupportedSchemas = embeddedSchemaNames()

func embeddedSchemaNames() []string {
	entries, err := schemasFS.ReadDir("schemas")
	if err != nil {
		panic(fmt.Sprintf("sds: embedded schemas unreadable: %v", err))
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".fbs") {
			names = append(names, entry.Name())
		}
	}
	return names // ReadDir returns entries sorted by name
}

// FieldLevelValidationEnv is the named switch for Validate's field-level parse
// through the flatc converter ("1"/"true" on, "0"/"false" off). The default is
// DefaultFieldLevelValidation.
const FieldLevelValidationEnv = "SDN_VALIDATE_FIELD_LEVEL"

// DefaultFieldLevelValidation is off, on measurement (2026-09-28, 385,046
// stored records of the 13 standards the dev node holds, via
// TestValidatorFieldLevelSampleMeasurement):
//
//   - 289,716 false rejections: every stored OMM, MPE and 146,687 of 150,000
//     CAT records fail the verifier's alignment check (8-byte fields at 4 mod
//     8, the layout a stripped size prefix leaves) and all of them verify once
//     realigned behind a 4-byte prefix;
//   - cost under the WasmEdge interpreter: 0.26-1.2 ms per record for most
//     standards, 5 ms for CNP and 149 ms for PRR, against ~1 ms of ingest.
//
// The false rejections are gone: the parse now verifies a bare stored record
// at its alignment origin (VerifyRecord, recordalign). The cost remains, so
// the default stays off until the converter is AOT-compiled.
const DefaultFieldLevelValidation = false

// Validator validates data against SDS schemas.
type Validator struct {
	flatc   *wasm.FlatcModule
	schemas map[string]int // schema name -> local schema number
	// Converter state, guarded by convMu. Every schema's source sits in the
	// converter's file map from AddSchema on; it is parsed on first use
	// (flatcPending -> flatcIDs, or flatcErrs when it cannot be), because
	// parsing all 236 embedded schemas up front costs ~6.5 s of boot. A
	// schema the converter cannot parse stays registered for envelope
	// validation and is only unavailable for conversion.
	convMu       sync.Mutex
	flatcPending map[string]bool
	flatcIDs     map[string]int
	flatcErrs    map[string]error
	// fieldLevel turns on Validate's field-level parse (FieldLevelValidationEnv).
	fieldLevel  atomic.Bool
	identifiers map[string]string // schema name -> 4-byte FlatBuffers file_identifier
	// identifierSchemas is the REVERSE index that makes header-only routing
	// possible (route.go): 4-byte file_identifier -> schema name. An
	// identifier claimed by more than one schema is removed rather than
	// resolved, because in-band bytes cannot disambiguate it.
	identifierSchemas map[string]string
	ambiguousIdents   map[string]bool
	mu                sync.RWMutex
	// structure is the always-on structural verifier (structure.go): every
	// schema AddSchema registers is also compiled here, and Validate refuses
	// any buffer it cannot prove memory-safe.
	structure *fbverify.Registry
}

// NewValidator creates a new SDS validator. flatc is the JSON⇄FlatBuffer
// converter; nil gives an envelope-only validator with no conversion.
func NewValidator(flatc *wasm.FlatcModule) (*Validator, error) {
	v := &Validator{
		flatc:             flatc,
		schemas:           make(map[string]int),
		flatcPending:      make(map[string]bool),
		flatcIDs:          make(map[string]int),
		flatcErrs:         make(map[string]error),
		identifiers:       make(map[string]string),
		identifierSchemas: make(map[string]string),
		ambiguousIdents:   make(map[string]bool),
		structure:         fbverify.NewRegistry(),
	}
	fieldLevel, err := fieldLevelValidationFromEnv()
	if err != nil {
		return nil, err
	}
	v.fieldLevel.Store(fieldLevel)

	ctx := context.Background()

	// Try to load embedded schemas
	if err := v.loadEmbeddedSchemas(ctx); err != nil {
		log.Warnf("Failed to load embedded schemas: %v", err)
		// Continue without embedded schemas - they may be loaded later
	}

	if flatc != nil {
		v.convMu.Lock()
		staged := len(v.flatcPending)
		v.convMu.Unlock()
		log.Infof("flatc converter %s: %d schema(s) staged (parsed on first use); field-level validation %s (%s)",
			flatc.Version(), staged, onOff(fieldLevel), FieldLevelValidationEnv)
	}
	return v, nil
}

func fieldLevelValidationFromEnv() (bool, error) {
	raw := strings.TrimSpace(os.Getenv(FieldLevelValidationEnv))
	if raw == "" {
		return DefaultFieldLevelValidation, nil
	}
	on, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s=%q: %w", FieldLevelValidationEnv, raw, err)
	}
	return on, nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// SetFieldLevelValidation turns Validate's field-level parse on or off. It
// has effect only with a converter.
func (v *Validator) SetFieldLevelValidation(on bool) { v.fieldLevel.Store(on) }

// FieldLevelValidation reports whether Validate runs the field-level parse:
// the switch is on and a converter is loaded.
func (v *Validator) FieldLevelValidation() bool {
	return v.flatc != nil && v.fieldLevel.Load()
}

// HasConverter reports whether a JSON⇄FlatBuffer converter is loaded.
func (v *Validator) HasConverter() bool { return v.flatc != nil }

// ConverterError parses the schema in the converter if it has not been yet,
// and reports why the converter cannot load it (nil when it can).
func (v *Validator) ConverterError(ctx context.Context, schemaName string) error {
	_, err := v.converterID(ctx, schemaName)
	return err
}

// PreloadConverterSchemas parses every staged schema in the converter and
// returns how many it holds and how many it cannot load.
func (v *Validator) PreloadConverterSchemas(ctx context.Context) (loaded, failed int) {
	v.convMu.Lock()
	defer v.convMu.Unlock()
	for name := range v.flatcPending {
		v.parseConverterSchemaLocked(ctx, name)
	}
	return len(v.flatcIDs), len(v.flatcErrs)
}

// parseConverterSchemaLocked turns a staged schema into a converter id or a
// recorded error. convMu must be held.
func (v *Validator) parseConverterSchemaLocked(ctx context.Context, name string) {
	delete(v.flatcPending, name)
	id, err := v.flatc.AddSchema(ctx, converterSchemaPath(name), nil)
	if err != nil {
		v.flatcErrs[name] = err
		log.Warnf("flatc converter cannot load schema %s (envelope validation only): %v", name, err)
		return
	}
	v.flatcIDs[name] = id
}

// converterSchemaPath is where a schema sits in the converter's file map:
// <FAMILY>/main.fbs, the layout the SDS includes ("../MET/main.fbs") expect.
func converterSchemaPath(schemaName string) string {
	return strings.TrimSuffix(schemaName, ".fbs") + "/main.fbs"
}

func (v *Validator) loadEmbeddedSchemas(ctx context.Context) error {
	entries, err := schemasFS.ReadDir("schemas")
	if err != nil {
		return fmt.Errorf("failed to read schemas directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".fbs") {
			continue
		}

		content, err := schemasFS.ReadFile(embeddedSchemaPath(entry.Name()))
		if err != nil {
			log.Warnf("Failed to read schema %s: %v", entry.Name(), err)
			continue
		}

		if err := v.AddSchema(ctx, entry.Name(), content); err != nil {
			log.Warnf("Failed to add schema %s: %v", entry.Name(), err)
			continue
		}

		log.Debugf("Loaded schema: %s", entry.Name())
	}

	return nil
}

// AddSchema adds a schema to the validator.
func (v *Validator) AddSchema(ctx context.Context, name string, content []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Record the schema's declared FlatBuffers file_identifier (if any). This is
	// what makes envelope verification schema-bound rather than merely
	// structural: an OMM record must actually carry "$OMM". 174 of the 175
	// embedded SDS schemas declare one.
	if ident, ok := parseFileIdentifier(content); ok {
		v.identifiers[name] = ident
		switch {
		case v.ambiguousIdents[ident]:
			// already refused
		case v.identifierSchemas[ident] == "" || v.identifierSchemas[ident] == name:
			v.identifierSchemas[ident] = name
		default:
			log.Warnf("file identifier %q is declared by both %s and %s: header-only routing disabled for it",
				ident, v.identifierSchemas[ident], name)
			delete(v.identifierSchemas, ident)
			v.ambiguousIdents[ident] = true
		}
	}

	if _, ok := v.schemas[name]; !ok {
		v.schemas[name] = len(v.schemas) + 1
	}
	v.structure.AddSource(name, content)

	// Stage the source in the converter's file map; it is parsed on first
	// use. A schema the converter cannot parse (REC.fbs includes standards
	// the node does not embed) stays registered for envelope validation.
	if v.flatc != nil {
		v.convMu.Lock()
		defer v.convMu.Unlock()
		if err := v.flatc.PutFile(ctx, converterSchemaPath(name), content); err != nil {
			v.flatcErrs[name] = err
			delete(v.flatcPending, name)
			log.Warnf("flatc converter cannot stage schema %s (envelope validation only): %v", name, err)
			return nil
		}
		if old, ok := v.flatcIDs[name]; ok {
			_ = v.flatc.RemoveSchema(ctx, old)
			delete(v.flatcIDs, name)
		}
		delete(v.flatcErrs, name)
		v.flatcPending[name] = true
	}
	return nil
}

// fileIdentifierRegex matches a FlatBuffers `file_identifier "$OMM";` declaration.
var fileIdentifierRegex = regexp.MustCompile(`(?m)^\s*file_identifier\s*"([^"]{4})"\s*;`)

// parseFileIdentifier extracts the 4-byte file_identifier declared by an .fbs schema.
func parseFileIdentifier(content []byte) (string, bool) {
	m := fileIdentifierRegex.FindSubmatch(content)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// FileIdentifier returns the FlatBuffers file_identifier declared by a schema.
func (v *Validator) FileIdentifier(schemaName string) (string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	ident, ok := v.identifiers[schemaName]
	return ident, ok
}

// Validate validates data against a schema.
//
// Envelope verification (VerifyEnvelope) runs on EVERY path, with or without
// the flatc converter: structure plus the schema's declared file_identifier are
// always enforced, so a 1-byte junk body is never stored as a valid SDS record.
// Structural verification (structure.go) also runs on every path: every offset
// a generated accessor could follow must land inside the buffer, in every
// envelope form the bytes could be read in. The converter's field-level parse
// (full conformance against the schema) runs additionally only when
// FieldLevelValidation is on.
func (v *Validator) Validate(ctx context.Context, schemaName string, data []byte) error {
	v.mu.RLock()
	_, ok := v.schemas[schemaName]
	v.mu.RUnlock()

	if !ok {
		return fmt.Errorf("unknown schema: %s", schemaName)
	}

	if err := v.VerifyEnvelope(schemaName, data); err != nil {
		return err
	}

	// Structural verification: always on, whatever the field-level switch
	// says (structure.go). This is what keeps a malformed offset from ever
	// reaching a generated accessor.
	if err := verifyStructureForms(v.structure, schemaName, data); err != nil {
		return fmt.Errorf("validation failed for %s: %w", schemaName, err)
	}

	// Field-level parse: only for a schema the converter can load; one it
	// cannot keeps the envelope verdict.
	if v.FieldLevelValidation() {
		if flatcID, err := v.converterID(ctx, schemaName); err == nil {
			buf, opts := v.placeForVerifier(schemaName, data, 0)
			if err := v.flatc.VerifyBinary(ctx, flatcID, buf, opts); err != nil {
				return fmt.Errorf("validation failed for %s: %w", schemaName, err)
			}
		}
	}

	return nil
}

// VerifyRecord runs the converter's full verification of a record (flatc's
// reflection verifier, then nested buffers and unions) in either accepted
// envelope form, whatever the field-level switch says. A bare stored record
// is verified at its alignment origin, so one whose size prefix was stripped
// at publish verifies as its builder produced it. Its bytes never change.
func (v *Validator) VerifyRecord(ctx context.Context, schemaName string, data []byte) error {
	id, err := v.converterID(ctx, schemaName)
	if err != nil {
		return err
	}
	buf, opts := v.placeForVerifier(schemaName, data, 0)
	if err := v.flatc.VerifyBinary(ctx, id, buf, opts); err != nil {
		return fmt.Errorf("verify %s: %w", schemaName, err)
	}
	return nil
}

// placeForVerifier returns data as the stock verifier must see it, with opts
// carrying the matching size-prefix bit. A size-prefixed buffer is its own
// origin. A bare stored record goes through recordalign.Record: itself when
// its length is 0 mod 8, else one copy behind its u32 length (the builder's
// original frame).
func (v *Validator) placeForVerifier(schemaName string, data []byte, opts wasm.FlatcOption) ([]byte, wasm.FlatcOption) {
	opts &^= wasm.FlatcSizePrefixed
	if form, err := v.DetectEnvelopeForm(schemaName, data); err == nil && form == EnvelopeSizePrefixed {
		return data, opts | wasm.FlatcSizePrefixed
	}
	view := recordalign.Record(data)
	if view.SizePrefixed {
		opts |= wasm.FlatcSizePrefixed
	}
	return view.Buf, opts
}

// FlatBuffers envelope constants.
const (
	fileIdentifierLength = 4
	sizePrefixLength     = 4
	// minFlatBufferLength is a root uoffset32 (4) + the vtable it must point at (4).
	minFlatBufferLength = 8
)

// VerifyEnvelope checks that data is a structurally valid FlatBuffer for
// schemaName, without requiring the flatc WASM module.
//
// Two wire forms are accepted structurally:
//
//   - bare: a plain finished buffer, file identifier at bytes 4..8. This is the
//     canonical STORED record: its CID is the sha256 of exactly these bytes and
//     every stream reader hands consumers exactly these bytes after stripping
//     the transport's own u32 length (verified across the serving fleet on
//     2026-09-10: CAT, NCD, OMM, MPE, SPW and every other data schema).
//   - size-prefixed: a builder's FinishSizePrefixed<X>Buffer output, the same
//     buffer behind its own u32 length, identifier at bytes 8..12. The engine
//     strips that prefix at ingest (engineRecordPayload) and the store's
//     parsers accept both, but a record STORED in this form reaches clients
//     behind two length prefixes. The publish boundary therefore refuses it
//     (see DetectEnvelopeForm and PublishHandler.refuseSizePrefixed).
//
// In both forms the root table offset, the vtable it points at, and the table's
// inline size must all land inside the buffer, and — when the schema declares a
// file_identifier — the buffer must actually carry it. Junk bytes fail all of
// these, which is the point.
func (v *Validator) VerifyEnvelope(schemaName string, data []byte) error {
	v.mu.RLock()
	ident, hasIdent := v.identifiers[schemaName]
	v.mu.RUnlock()

	if len(data) == 0 {
		return fmt.Errorf("empty data for schema %s", schemaName)
	}
	if len(data) < minFlatBufferLength {
		return fmt.Errorf(
			"invalid %s record: %d bytes is shorter than the minimum FlatBuffer (%d bytes)",
			schemaName, len(data), minFlatBufferLength,
		)
	}

	// Canonical form first: size-prefixed.
	if inner, ok := sizePrefixedPayload(data); ok {
		if err := verifyFlatBufferRoot(inner); err == nil {
			if !hasIdent || bufferHasIdentifier(inner, ident) {
				return nil
			}
		}
	}

	// Tolerated form: a plain finished buffer.
	if err := verifyFlatBufferRoot(data); err == nil {
		if !hasIdent || bufferHasIdentifier(data, ident) {
			return nil
		}
	}

	if hasIdent {
		return fmt.Errorf(
			"invalid %s record: not a FlatBuffer carrying file identifier %q (%d bytes, %s)",
			schemaName, ident, len(data), describeIdentifier(data),
		)
	}
	return fmt.Errorf("invalid %s record: not a structurally valid FlatBuffer (%d bytes)", schemaName, len(data))
}

// EnvelopeForm names which accepted wire form a record arrived in.
type EnvelopeForm string

const (
	// EnvelopeBare is the canonical stored record (identifier at bytes 4..8).
	EnvelopeBare EnvelopeForm = "bare"
	// EnvelopeSizePrefixed is builder output behind its own u32 length
	// (identifier at bytes 8..12); refused at the publish boundary.
	EnvelopeSizePrefixed EnvelopeForm = "size-prefixed"
)

// DetectEnvelopeForm reports which accepted wire form data is in for
// schemaName, applying the same structural and identifier checks as
// VerifyEnvelope. A buffer that satisfies neither form returns an error.
func (v *Validator) DetectEnvelopeForm(schemaName string, data []byte) (EnvelopeForm, error) {
	v.mu.RLock()
	ident, hasIdent := v.identifiers[schemaName]
	v.mu.RUnlock()

	if len(data) < minFlatBufferLength {
		return "", fmt.Errorf(
			"invalid %s record: %d bytes is shorter than the minimum FlatBuffer (%d bytes)",
			schemaName, len(data), minFlatBufferLength,
		)
	}
	if verifyFlatBufferRoot(data) == nil && (!hasIdent || bufferHasIdentifier(data, ident)) {
		return EnvelopeBare, nil
	}
	if inner, ok := sizePrefixedPayload(data); ok && verifyFlatBufferRoot(inner) == nil && (!hasIdent || bufferHasIdentifier(inner, ident)) {
		return EnvelopeSizePrefixed, nil
	}
	if hasIdent {
		return "", fmt.Errorf("invalid %s record: neither a bare nor a size-prefixed FlatBuffer carrying file identifier %q", schemaName, ident)
	}
	return "", fmt.Errorf("invalid %s record: not a structurally valid FlatBuffer (%d bytes)", schemaName, len(data))
}

// sizePrefixedPayload returns the inner buffer of a size-prefixed FlatBuffer when
// the leading uint32 exactly accounts for the remaining bytes.
func sizePrefixedPayload(data []byte) ([]byte, bool) {
	if len(data) < sizePrefixLength+minFlatBufferLength {
		return nil, false
	}
	size := binary.LittleEndian.Uint32(data[:sizePrefixLength])
	if int64(size) != int64(len(data))-sizePrefixLength {
		return nil, false
	}
	return data[sizePrefixLength:], true
}

// bufferHasIdentifier reports whether a plain finished buffer carries identifier.
func bufferHasIdentifier(buf []byte, identifier string) bool {
	if len(buf) < minFlatBufferLength {
		return false
	}
	return string(buf[sizePrefixLength:sizePrefixLength+fileIdentifierLength]) == identifier
}

// verifyFlatBufferRoot walks the root table header of a plain finished buffer and
// checks that every offset it declares stays inside the buffer.
func verifyFlatBufferRoot(buf []byte) error {
	n := int64(len(buf))
	if n < minFlatBufferLength {
		return fmt.Errorf("buffer too short: %d bytes", n)
	}

	root := int64(binary.LittleEndian.Uint32(buf[:4]))
	if root < 4 || root+4 > n {
		return fmt.Errorf("root table offset %d outside buffer of %d bytes", root, n)
	}

	// The root table starts with an soffset32 back to its vtable.
	soffset := int64(int32(binary.LittleEndian.Uint32(buf[root : root+4])))
	vtable := root - soffset
	if vtable < 0 || vtable+4 > n {
		return fmt.Errorf("vtable offset %d outside buffer of %d bytes", vtable, n)
	}

	vtableSize := int64(binary.LittleEndian.Uint16(buf[vtable : vtable+2]))
	if vtableSize < 4 || vtable+vtableSize > n {
		return fmt.Errorf("vtable size %d outside buffer of %d bytes", vtableSize, n)
	}

	tableSize := int64(binary.LittleEndian.Uint16(buf[vtable+2 : vtable+4]))
	if tableSize < 4 || root+tableSize > n {
		return fmt.Errorf("root table size %d outside buffer of %d bytes", tableSize, n)
	}

	return nil
}

// describeIdentifier renders the identifier bytes actually present, for errors.
func describeIdentifier(data []byte) string {
	if inner, ok := sizePrefixedPayload(data); ok && len(inner) >= minFlatBufferLength {
		return fmt.Sprintf("size-prefixed identifier %q", sanitizeIdentifier(inner[sizePrefixLength:sizePrefixLength+fileIdentifierLength]))
	}
	if len(data) >= minFlatBufferLength {
		return fmt.Sprintf("identifier %q", sanitizeIdentifier(data[sizePrefixLength:sizePrefixLength+fileIdentifierLength]))
	}
	return "no identifier"
}

// sanitizeIdentifier renders non-printable identifier bytes as dots.
func sanitizeIdentifier(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			out[i] = '.'
			continue
		}
		out[i] = c
	}
	return string(out)
}

// StoredRecordOptions converts JSON into the node's canonical stored record:
// a bare finished buffer (the publish boundary strips a size prefix, see
// api.PublishHandler.canonicalRecordBytes) with every field the JSON gives
// stored even when it equals its default. Add wasm.FlatcSizePrefixed for a
// length-framed wire copy.
const StoredRecordOptions = wasm.FlatcForceDefaults

// converterID resolves a schema to its converter id, parsing it on first use.
func (v *Validator) converterID(ctx context.Context, schemaName string) (int, error) {
	v.mu.RLock()
	_, known := v.schemas[schemaName]
	v.mu.RUnlock()
	if !known {
		return 0, fmt.Errorf("unknown schema: %s", schemaName)
	}
	if v.flatc == nil {
		return 0, wasm.ErrNoModule
	}

	v.convMu.Lock()
	defer v.convMu.Unlock()
	if v.flatcPending[schemaName] {
		v.parseConverterSchemaLocked(ctx, schemaName)
	}
	if id, ok := v.flatcIDs[schemaName]; ok {
		return id, nil
	}
	if cause := v.flatcErrs[schemaName]; cause != nil {
		return 0, fmt.Errorf("schema %s is not loaded in the flatc converter: %w", schemaName, cause)
	}
	return 0, fmt.Errorf("schema %s is not staged in the flatc converter", schemaName)
}

// JSONToFlatBuffer converts JSON to a FlatBuffer with per-call options
// (StoredRecordOptions for a record to store). flatc lays fields out by size,
// so the values round-trip but the bytes need not match another builder's.
func (v *Validator) JSONToFlatBuffer(ctx context.Context, schemaName string, jsonData []byte, opts wasm.FlatcOption) ([]byte, error) {
	id, err := v.converterID(ctx, schemaName)
	if err != nil {
		return nil, err
	}
	return v.flatc.JSONToBinary(ctx, id, jsonData, opts)
}

// FlatBufferToJSON verifies a record and prints it as JSON. Either stored
// form reads: the size-prefix option is taken from the record's envelope, a
// bare record is verified at its alignment origin (placeForVerifier), and
// opts supplies the rest (wasm.FlatcCompactJSON, wasm.FlatcForceDefaults, ...).
func (v *Validator) FlatBufferToJSON(ctx context.Context, schemaName string, binaryData []byte, opts wasm.FlatcOption) ([]byte, error) {
	id, err := v.converterID(ctx, schemaName)
	if err != nil {
		return nil, err
	}
	buf, opts := v.placeForVerifier(schemaName, binaryData, opts)
	return v.flatc.BinaryToJSON(ctx, id, buf, opts)
}

// Schemas returns the list of loaded schema names.
func (v *Validator) Schemas() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()

	schemas := make([]string, 0, len(v.schemas))
	for name := range v.schemas {
		schemas = append(schemas, name)
	}
	return schemas
}

// HasSchema checks if a schema is loaded.
func (v *Validator) HasSchema(name string) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, ok := v.schemas[name]
	return ok
}

// SchemaNameFromExtension derives the schema name from a file extension or type.
func SchemaNameFromExtension(ext string) string {
	ext = strings.TrimPrefix(ext, ".")
	ext = strings.ToUpper(ext)
	if !strings.HasSuffix(ext, ".fbs") {
		ext = ext + ".fbs"
	}
	return ext
}

// SchemaNameToTable converts a schema name to a table name for storage.
// It validates the schema name first to prevent SQL injection via dynamic table names.
func SchemaNameToTable(schemaName string) (string, error) {
	if err := ValidateSchemaName(schemaName); err != nil {
		return "", fmt.Errorf("invalid schema name for table: %w", err)
	}
	return strings.TrimSuffix(schemaName, ".fbs"), nil
}
