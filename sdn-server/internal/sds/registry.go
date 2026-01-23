// Package sds provides Space Data Standards schema registry and management.
package sds

import (
	"embed"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// Note: The embed directive expects schemas to be in schemas/sds/*.fbs
// These are loaded from the submodule at ../../schemas/sds/

//go:embed schemas/*.fbs
var sdsSchemasFS embed.FS

// SchemaRegistry manages SDS schema files and metadata.
type SchemaRegistry struct {
	schemas      map[string][]byte // schema name -> content
	descriptions map[string]string // schema name -> description
	mu           sync.RWMutex
}

// SchemaInfo contains information about a schema.
type SchemaInfo struct {
	Name        string
	Description string
	Size        int
}

// NewSchemaRegistry creates a new schema registry with embedded schemas.
func NewSchemaRegistry() (*SchemaRegistry, error) {
	r := &SchemaRegistry{
		schemas:      make(map[string][]byte),
		descriptions: make(map[string]string),
	}

	// Load embedded schemas
	if err := r.loadEmbedded(); err != nil {
		log.Warnf("Failed to load embedded schemas: %v", err)
		// Continue with default schemas
		r.loadDefaults()
	}

	return r, nil
}

func (r *SchemaRegistry) loadEmbedded() error {
	entries, err := sdsSchemasFS.ReadDir("schemas")
	if err != nil {
		return fmt.Errorf("failed to read schemas directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".fbs") {
			continue
		}

		content, err := sdsSchemasFS.ReadFile(filepath.Join("schemas", entry.Name()))
		if err != nil {
			log.Warnf("Failed to read schema %s: %v", entry.Name(), err)
			continue
		}

		r.schemas[entry.Name()] = content
		r.descriptions[entry.Name()] = extractDescription(content)
	}

	log.Infof("Loaded %d embedded schemas", len(r.schemas))
	return nil
}

func (r *SchemaRegistry) loadDefaults() {
	// Add placeholder entries for required schemas
	for _, schema := range SupportedSchemas {
		if _, ok := r.schemas[schema]; !ok {
			r.schemas[schema] = nil // No content yet
			r.descriptions[schema] = schemaDescriptions[schema]
		}
	}
}

// Get returns the content of a schema.
func (r *SchemaRegistry) Get(name string) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	content, ok := r.schemas[name]
	return content, ok
}

// Has checks if a schema exists.
func (r *SchemaRegistry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.schemas[name]
	return ok
}

// List returns all schema names.
func (r *SchemaRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.schemas))
	for name := range r.schemas {
		names = append(names, name)
	}
	return names
}

// Info returns information about all schemas.
func (r *SchemaRegistry) Info() []SchemaInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info := make([]SchemaInfo, 0, len(r.schemas))
	for name, content := range r.schemas {
		info = append(info, SchemaInfo{
			Name:        name,
			Description: r.descriptions[name],
			Size:        len(content),
		})
	}
	return info
}

// Add adds a schema to the registry.
func (r *SchemaRegistry) Add(name string, content []byte, description string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.schemas[name] = content
	if description != "" {
		r.descriptions[name] = description
	}
}

// extractDescription extracts the schema description from FlatBuffer comments.
func extractDescription(content []byte) string {
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "///") {
			return strings.TrimPrefix(line, "/// ")
		}
		if strings.HasPrefix(line, "//") {
			return strings.TrimPrefix(line, "// ")
		}
	}
	return ""
}

// Default schema descriptions
var schemaDescriptions = map[string]string{
	"ATM.fbs": "Attitude Message - Spacecraft attitude information",
	"BOV.fbs": "Body Orientation and Velocity - Attitude and angular velocity",
	"CAT.fbs": "Catalog - Space object catalog entries",
	"CDM.fbs": "Conjunction Data Message - Close approach warnings between objects",
	"CRM.fbs": "Collision Risk Message - Collision probability assessments",
	"CSM.fbs": "Conjunction Summary Message - Brief conjunction event summary",
	"CTR.fbs": "Contact Report - Communication contact reports",
	"EME.fbs": "Electromagnetic Emissions - RF and electromagnetic data",
	"EOO.fbs": "Earth Orientation - Earth orientation parameters",
	"EOP.fbs": "Earth Orientation Parameters - Polar motion and UT1-UTC",
	"EPM.fbs": "Entity Profile Manifest - Organization identity and contact information",
	"HYP.fbs": "Hyperbolic Orbit - Hyperbolic trajectory parameters",
	"IDM.fbs": "Initial Data Message - Initial orbit determination data",
	"LCC.fbs": "Launch Collision Corridor - Launch trajectory corridors",
	"LDM.fbs": "Launch Data Message - Launch event information and parameters",
	"MET.fbs": "Meteorological Data - Atmospheric and weather data",
	"MPE.fbs": "Maneuver Planning Ephemeris - Planned maneuver data",
	"OCM.fbs": "Orbit Comprehensive Message - Full orbit data package",
	"OEM.fbs": "Orbit Ephemeris Message - Time-series position/velocity data",
	"OMM.fbs": "Orbit Mean-Elements Message - Satellite orbital parameters (TLE/3LE)",
	"OSM.fbs": "Orbit State Message - Orbit state vectors",
	"PLD.fbs": "Payload - Spacecraft payload information",
	"PNM.fbs": "Peer Network Manifest - Peer identity and network capabilities",
	"PRG.fbs": "Propagation Settings - Orbit propagation parameters",
	"REC.fbs": "Records - Data records and observations",
	"RFM.fbs": "Reference Frame Message - Coordinate frame definitions",
	"ROC.fbs": "Re-entry Operations Corridor - Re-entry trajectory corridors",
	"SCM.fbs": "Spacecraft Message - Spacecraft characteristics",
	"SIT.fbs": "Satellite Impact Table - Impact risk assessments",
	"TDM.fbs": "Tracking Data Message - Radar/optical observations",
	"TIM.fbs": "Time Message - Time synchronization data",
	"VCM.fbs": "Vector Covariance Message - State vector with covariance",
}

// GetDescription returns the description for a schema.
func (r *SchemaRegistry) GetDescription(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.descriptions[name]
}
