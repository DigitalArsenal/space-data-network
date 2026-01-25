// Package sds provides Space Data Standards validation and schema handling.
package sds

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	logging "github.com/ipfs/go-log/v2"

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

func init() {
	// Suppress unused variable warning
	_ = schemasFS
}

// SupportedSchemas lists all SDS schema files.
var SupportedSchemas = []string{
	"ACL.fbs",  // Access Control List - Data access grants
	"ATM.fbs",  // Attitude Message
	"BOV.fbs",  // Body Orientation and Velocity
	"CAT.fbs",  // Catalog
	"CDM.fbs",  // Conjunction Data Message
	"CRM.fbs",  // Collision Risk Message
	"CSM.fbs",  // Conjunction Summary Message
	"CTR.fbs",  // Contact Report
	"EME.fbs",  // Electromagnetic Emissions
	"EOO.fbs",  // Earth Orientation
	"EOP.fbs",  // Earth Orientation Parameters
	"EPM.fbs",  // Entity Profile Manifest
	"HYP.fbs",  // Hyperbolic Orbit
	"IDM.fbs",  // Initial Data Message
	"LCC.fbs",  // Launch Collision Corridor
	"LDM.fbs",  // Launch Data Message
	"MET.fbs",  // Meteorological Data
	"MPE.fbs",  // Maneuver Planning Ephemeris
	"OCM.fbs",  // Orbit Comprehensive Message
	"OEM.fbs",  // Orbit Ephemeris Message
	"OMM.fbs",  // Orbit Mean-Elements Message
	"OSM.fbs",  // Orbit State Message
	"PLD.fbs",  // Payload
	"PNM.fbs",  // Peer Network Manifest
	"PRG.fbs",  // Propagation Settings
	"PUR.fbs",  // Purchase Request - Marketplace purchases
	"REC.fbs",  // Records
	"REV.fbs",  // Review - Marketplace reviews
	"RFM.fbs",  // Reference Frame Message
	"RHD.fbs",  // Routing Header - Message routing metadata
	"ROC.fbs",  // Re-entry Operations Corridor
	"SCM.fbs",  // Spacecraft Message
	"SIT.fbs",  // Satellite Impact Table
	"STF.fbs",  // Storefront Listing - Marketplace listings
	"TDM.fbs",  // Tracking Data Message
	"TIM.fbs",  // Time Message
	"VCM.fbs",  // Vector Covariance Message
}

// Validator validates data against SDS schemas.
type Validator struct {
	flatc   *wasm.FlatcModule
	schemas map[string]int // schema name -> schema ID
	mu      sync.RWMutex
}

// NewValidator creates a new SDS validator.
func NewValidator(flatc *wasm.FlatcModule) (*Validator, error) {
	v := &Validator{
		flatc:   flatc,
		schemas: make(map[string]int),
	}

	ctx := context.Background()

	// Try to load embedded schemas
	if err := v.loadEmbeddedSchemas(ctx); err != nil {
		log.Warnf("Failed to load embedded schemas: %v", err)
		// Continue without embedded schemas - they may be loaded later
	}

	return v, nil
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

		content, err := schemasFS.ReadFile(filepath.Join("schemas", entry.Name()))
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

	// If WASM module is available, use it
	if v.flatc != nil {
		id, err := v.flatc.AddSchema(ctx, name, content)
		if err != nil {
			return fmt.Errorf("failed to add schema to WASM: %w", err)
		}
		v.schemas[name] = id
		return nil
	}

	// Without WASM, just track schema names
	v.schemas[name] = len(v.schemas) + 1
	return nil
}

// Validate validates data against a schema.
func (v *Validator) Validate(ctx context.Context, schemaName string, data []byte) error {
	v.mu.RLock()
	schemaID, ok := v.schemas[schemaName]
	v.mu.RUnlock()

	if !ok {
		return fmt.Errorf("unknown schema: %s", schemaName)
	}

	// If WASM module is available, use it to validate
	if v.flatc != nil {
		// Try to parse as FlatBuffer - if it succeeds, data is valid
		_, err := v.flatc.BinaryToJSON(ctx, schemaID, data)
		if err != nil {
			return fmt.Errorf("validation failed for %s: %w", schemaName, err)
		}
		return nil
	}

	// Without WASM, perform basic validation
	// Just check that data is not empty
	if len(data) == 0 {
		return fmt.Errorf("empty data for schema %s", schemaName)
	}

	return nil
}

// JSONToFlatBuffer converts JSON data to FlatBuffer binary.
func (v *Validator) JSONToFlatBuffer(ctx context.Context, schemaName string, jsonData []byte) ([]byte, error) {
	v.mu.RLock()
	schemaID, ok := v.schemas[schemaName]
	v.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown schema: %s", schemaName)
	}

	if v.flatc == nil {
		return nil, wasm.ErrNoModule
	}

	return v.flatc.JSONToBinary(ctx, schemaID, jsonData)
}

// FlatBufferToJSON converts FlatBuffer binary to JSON data.
func (v *Validator) FlatBufferToJSON(ctx context.Context, schemaName string, binaryData []byte) ([]byte, error) {
	v.mu.RLock()
	schemaID, ok := v.schemas[schemaName]
	v.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown schema: %s", schemaName)
	}

	if v.flatc == nil {
		return nil, wasm.ErrNoModule
	}

	return v.flatc.BinaryToJSON(ctx, schemaID, binaryData)
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
func SchemaNameToTable(schemaName string) string {
	name := strings.TrimSuffix(schemaName, ".fbs")
	name = strings.ToLower(name)
	return "sds_" + name
}
