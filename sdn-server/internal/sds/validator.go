// Package sds provides Space Data Standards validation and schema handling.
package sds

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

var log = logging.Logger("sds")

//go:embed schemas/*.fbs
var schemasFS embed.FS

func init() {
	// Suppress unused variable warning
	_ = schemasFS
}

// SupportedSchemas lists all SDS schema files.
var SupportedSchemas = []string{
	"EPM.fbs",  // Entity Profile Manifest
	"PNM.fbs",  // Peer Network Manifest
	"OMM.fbs",  // Orbit Mean-Elements Message
	"OEM.fbs",  // Orbit Ephemeris Message
	"CDM.fbs",  // Conjunction Data Message
	"CAT.fbs",  // Catalog
	"CSM.fbs",  // Conjunction Summary Message
	"LDM.fbs",  // Launch Data Message
	"IDM.fbs",  // Initial Data Message
	"PLD.fbs",  // Payload
	"BOV.fbs",  // Body Orientation and Velocity
	"EOO.fbs",  // Earth Orientation
	"RFM.fbs",  // Reference Frame Message
	"TDM.fbs",  // Tracking Data Message
	"AEM.fbs",  // Attitude Ephemeris Message
	"APM.fbs",  // Attitude Parameter Message
	"OPM.fbs",  // Orbit Parameter Message
	"MPE.fbs",  // Maneuver Planning Ephemeris
	"OCM.fbs",  // Orbit Comprehensive Message
	"RDM.fbs",  // Re-entry Data Message
	"SIT.fbs",  // Satellite Impact Table
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
