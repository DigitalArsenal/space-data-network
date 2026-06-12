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

// SupportedSchemas lists all SDS schema files registered with the validator.
// It mirrors the .fbs files embedded in schemas/ — every Space Data Standards
// (spacedatastandards.org) schema plus the SDN-internal schemas
// (PGR, PLHD, PLOG, RHD).
var SupportedSchemas = []string{
	"ACL.fbs",  // Access Control List - Data access grants
	"ACM.fbs",  // Attitude Comprehensive Message
	"ACR.fbs",  // Aircraft Dynamics
	"AEM.fbs",  // Attitude Ephemeris Message
	"ANI.fbs",  // Analytic Imagery Product
	"AOF.fbs",  // AOS Transfer Frame (CCSDS 732.0-B-3)
	"APM.fbs",  // Attitude Parameter Message
	"ARM.fbs",  // Armor and Protection
	"AST.fbs",  // Astrodynamics
	"ATD.fbs",  // Attitude Data Point
	"ATM.fbs",  // Attitude Message
	"BAL.fbs",  // Ballistics
	"BEM.fbs",  // Antenna Beam
	"BMC.fbs",  // Beam Contour
	"BOV.fbs",  // Body Orientation and Velocity
	"BUS.fbs",  // Satellite Bus Specification
	"CAQ.fbs",  // Catalog Query - Catalog query envelope
	"CAT.fbs",  // Catalog
	"CDM.fbs",  // Conjunction Data Message
	"CFP.fbs",  // CCSDS File Delivery Protocol PDU (CCSDS 727.0-B-5)
	"CHN.fbs",  // Communications Channel
	"CLT.fbs",  // Command Link Transmission Unit Service (CCSDS 912.3-B-2)
	"CMS.fbs",  // Communications Payload
	"COM.fbs",  // Communications Systems
	"COT.fbs",  // Cursor on Target Event
	"CRD.fbs",  // Coordinate Systems
	"CRM.fbs",  // Collision Risk Message
	"CSM.fbs",  // Conjunction Summary Message
	"CTR.fbs",  // Contact Report
	"CZM.fbs",  // CZML Document
	"DFH.fbs",  // GEO Drift History
	"DMG.fbs",  // Damage Models
	"DOA.fbs",  // Difference of Arrival Geolocation
	"DPM.fbs",  // Dataset Publication Manifest
	"DSS.fbs",  // Data Sync Status
	"EME.fbs",  // Electromagnetic Emissions
	"ENC.fbs",  // Encryption Header
	"ENV.fbs",  // Atmosphere and Environment
	"EOO.fbs",  // Earth Orientation
	"EOP.fbs",  // Earth Orientation Parameters
	"EPM.fbs",  // Entity Profile Manifest
	"ESL.fbs",  // Entity/Standards Link
	"ETM.fbs",  // Entity Metadata
	"EWR.fbs",  // Electronic Warfare
	"FCS.fbs",  // Fire Control Systems
	"FPC.fbs",  // Fastest Path Compute
	"GDI.fbs",  // Ground Imagery
	"GEO.fbs",  // GEO Spacecraft Status
	"GJN.fbs",  // GeoJSON FeatureCollection
	"GNO.fbs",  // GNSS Observation
	"GPX.fbs",  // GPX Document
	"GRV.fbs",  // Gravity Models
	"GVH.fbs",  // Ground Vehicles
	"HEL.fbs",  // Helicopter Dynamics
	"HFC.fbs",  // Hypersonic Flight Conditions
	"HYP.fbs",  // Hyperbolic Orbit
	"IDM.fbs",  // Initial Data Message
	"ION.fbs",  // Ionospheric Observation
	"IRO.fbs",  // Infrared Observation
	"KMF.fbs",  // Key Material Frame
	"KML.fbs",  // KML Document
	"KRF.fbs",  // Key Reference Frame
	"LAM.fbs",  // Launch Ascent Message
	"LCC.fbs",  // Launch Collision Corridor
	"LCF.fbs",  // Licensing Configuration Frame
	"LCH.fbs",  // Licensing Challenge Message
	"LDM.fbs",  // Launch Data Message
	"LGR.fbs",  // Licensing Grant Message
	"LKS.fbs",  // Link Status
	"LMO.fbs",  // Lambert Solve Result
	"LMR.fbs",  // Module Control Message
	"LMS.fbs",  // Lambert Solve Request
	"LND.fbs",  // Launch Detection
	"LNE.fbs",  // Launch Event
	"LPF.fbs",  // Licensing Proof Message
	"LWK.fbs",  // Wrapped Module Content Key
	"MBL.fbs",  // Module Bundle Listing
	"MET.fbs",  // Meteorological Data
	"MFE.fbs",  // Manifold Element Set
	"MNF.fbs",  // Orbit Manifold
	"MNV.fbs",  // Spacecraft Maneuver
	"MPE.fbs",  // Maneuver Planning Ephemeris
	"MSL.fbs",  // Guided Missiles
	"MST.fbs",  // Missile Track
	"MTI.fbs",  // Moving Target Indicator
	"NAV.fbs",  // Naval Vessels
	"OBD.fbs",  // Orbit Determination Results
	"OBT.fbs",  // Orbit Track
	"OCM.fbs",  // Orbit Comprehensive Message
	"OEM.fbs",  // Orbit Ephemeris Message
	"OMM.fbs",  // Orbit Mean-Elements Message
	"OOA.fbs",  // On-Orbit Antenna
	"OOB.fbs",  // On-Orbit Battery
	"OOD.fbs",  // On-Orbit Object Details
	"OOE.fbs",  // On-Orbit Event
	"OOI.fbs",  // Object of Interest
	"OOL.fbs",  // On-Orbit Object List
	"OON.fbs",  // On-Orbit Object
	"OOS.fbs",  // On-Orbit Solar Array
	"OOT.fbs",  // On-Orbit Thruster
	"OPM.fbs",  // Orbit Parameter Message
	"OSM.fbs",  // Orbit State Message
	"PCF.fbs",  // Propagator Configuration
	"PGR.fbs",  // Peer Graph Record - Peer network graph snapshot (SDN-internal)
	"PHY.fbs",  // Physics and Rigid Body Dynamics
	"PIV.fbs",  // Plugin Invoke - Plugin request/response envelopes
	"PLD.fbs",  // Payload
	"PLG.fbs",  // Plugin Manifest - Signed plugin distribution record
	"PLHD.fbs", // Publication Log Head - Log head announcement (SDN-internal)
	"PLK.fbs",  // Plugin License Key
	"PLOG.fbs", // Publication Log Entry - Hash-chained publication log (SDN-internal)
	"PNM.fbs",  // Peer Network Manifest
	"PPE.fbs",  // Polynomial Ephemeris
	"PRG.fbs",  // Propagation Settings
	"PRW.fbs",  // Propagator Runtime Wire
	"PUR.fbs",  // Purchase Request - Marketplace purchases
	"RAF.fbs",  // Return All Frames Service (CCSDS 913.1-B-2)
	"RCF.fbs",  // Return Channel Frames Service (CCSDS 913.5-B-2)
	"RDM.fbs",  // Reentry Data Message
	"RDO.fbs",  // Radar Observation
	"REC.fbs",  // Records
	"REM.fbs",  // Reentry Evaluation Message
	"REV.fbs",  // Review - Marketplace reviews
	"RFB.fbs",  // RF Band Specification
	"RFE.fbs",  // RF Emitter
	"RFM.fbs",  // Reference Frame Message
	"RFO.fbs",  // RF Observation
	"RHD.fbs",  // Routing Header - Message routing metadata (SDN-internal)
	"ROC.fbs",  // Re-entry Operations Corridor
	"SAR.fbs",  // SAR Observation
	"SCM.fbs",  // Spacecraft Message
	"SDF.fbs",  // Signed Distance Field
	"SDL.fbs",  // Space Data Link Security (CCSDS 355.0-B-1)
	"SDR.fbs",  // Sensor Detection Report
	"SEN.fbs",  // Sensor Management
	"SEO.fbs",  // Space Environment Observation
	"SEV.fbs",  // Space Environment Observation Detail
	"SHW.fbs",  // Shader Wire
	"SIT.fbs",  // Satellite Impact Table
	"SKI.fbs",  // Sky Imagery
	"SNR.fbs",  // Sensor Systems
	"SNW.fbs",  // Sensor Runtime Wire
	"SOI.fbs",  // Space Object Identification Observation Set
	"SON.fbs",  // Sonar and Underwater Acoustics
	"SPP.fbs",  // Space Packet Protocol (CCSDS 133.0-B-1)
	"SPW.fbs",  // Space Weather Data Record
	"SRI.fbs",  // Standards Record Index
	"STF.fbs",  // Storefront Listing - Marketplace listings
	"STR.fbs",  // Star Catalog Entry
	"STV.fbs",  // State Vector
	"SWR.fbs",  // Short-Wave Infrared Observation
	"TAB.fbs",  // Typed Arena Buffer
	"TCF.fbs",  // Telecommand Transfer Frame (CCSDS 232.0-B-3)
	"TDM.fbs",  // Tracking Data Message
	"TIM.fbs",  // Time Message
	"TKG.fbs",  // Tracking and Data Fusion
	"TME.fbs",  // Time Systems
	"TMF.fbs",  // Telemetry Transfer Frame (CCSDS 132.0-B-2)
	"TPN.fbs",  // Transponder
	"TRK.fbs",  // Track
	"TRN.fbs",  // Terrain Models
	"VCM.fbs",  // Vector Covariance Message
	"WPN.fbs",  // Weapons and Munitions
	"WTH.fbs",  // Weather Data
	"XTC.fbs",  // XTCE SpaceSystem Document
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
// It validates the schema name first to prevent SQL injection via dynamic table names.
func SchemaNameToTable(schemaName string) (string, error) {
	if err := ValidateSchemaName(schemaName); err != nil {
		return "", fmt.Errorf("invalid schema name for table: %w", err)
	}
	return strings.TrimSuffix(schemaName, ".fbs"), nil
}
