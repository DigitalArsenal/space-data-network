// Package sds provides Space Data Standards validation and schema handling.
package sds

import (
	"context"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
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
	"ACW.fbs",  // Access Windows
	"AEM.fbs",  // Attitude Ephemeris Message
	"ANI.fbs",  // Analytic Imagery Product
	"AOF.fbs",  // AOS Transfer Frame (CCSDS 732.0-B-3)
	"APP.fbs",  // Application Package Manifest
	"APM.fbs",  // Attitude Parameter Message
	"ARM.fbs",  // Armor and Protection
	"AST.fbs",  // Astrodynamics
	"ATD.fbs",  // Attitude Data Point
	"ATM.fbs",  // Attitude Message
	"BAL.fbs",  // Ballistics
	"BEM.fbs",  // Antenna Beam
	"BMC.fbs",  // Beam Contour
	"BOV.fbs",  // Body Orientation and Velocity
	"BSP.fbs",  // Body State Propagation
	"BUS.fbs",  // Satellite Bus Specification
	"CAQ.fbs",  // Catalog Query - Catalog query envelope
	"CAT.fbs",  // Catalog
	"CDM.fbs",  // Conjunction Data Message
	"CES.fbs",  // Catalog Embedding Shard
	"CFP.fbs",  // CCSDS File Delivery Protocol PDU (CCSDS 727.0-B-5)
	"CHN.fbs",  // Communications Channel
	"CLT.fbs",  // Command Link Transmission Unit Service (CCSDS 912.3-B-2)
	"CMS.fbs",  // Communications Payload
	"CMT.fbs",  // Commission Terms - revenue split binding a listing, store or publisher
	"CNP.fbs",  // Constellation Network Performance (SDS v1.177.0)
	"COM.fbs",  // Communications Systems
	"COT.fbs",  // Cursor on Target Event
	"CPS.fbs",  // Compressed Packet Stream (CCSDS fixed-length packet run)
	"CRD.fbs",  // Coordinate Systems
	"CRM.fbs",  // Collision Risk Message
	"CSM.fbs",  // Conjunction Summary Message
	"CTR.fbs",  // Contact Report
	"CVG.fbs",  // Coverage Grid Figure-of-Merit
	"CZM.fbs",  // CZML Document
	"DFH.fbs",  // GEO Drift History
	"DMG.fbs",  // Damage Models
	"DOA.fbs",  // Difference of Arrival Geolocation
	"DPM.fbs",  // Dataset Publication Manifest
	"DSS.fbs",  // Data Sync Status
	"EME.fbs",  // Electromagnetic Emissions
	"ENC.fbs",  // Encryption Header
	"ENT.fbs",  // Entitlement - provider subscription or account entitlement state
	"ENV.fbs",  // Atmosphere and Environment
	"EOO.fbs",  // Earth Orientation
	"EOP.fbs",  // Earth Orientation Parameters
	"EPM.fbs",  // Entity Profile Manifest
	"ESL.fbs",  // Entity/Standards Link
	"ETM.fbs",  // Entity Metadata
	"EWR.fbs",  // Electronic Warfare
	"FCS.fbs",  // Fire Control Systems
	"FPC.fbs",  // Fastest Path Compute
	"FRM.fbs",  // Frame Transform
	"FSB.fbs",  // FlatSQL Byte Stream - bounded typed chunk for append/query
	"FSM.fbs",  // Field Stream Message - marketplace-protected live streams
	"FSO.fbs",  // FlatSQL Operation - control, policy and status record
	"FSP.fbs",  // Field Stream Policy - marketplace-protected live streams
	"GDI.fbs",  // Ground Imagery
	"GEO.fbs",  // GEO Spacecraft Status
	"GJN.fbs",  // GeoJSON FeatureCollection
	"GNO.fbs",  // GNSS Observation
	"GPX.fbs",  // GPX Document
	"GRV.fbs",  // Gravity Models
	"GST.fbs",  // Ground/Tracking Station Definition
	"GVH.fbs",  // Ground Vehicles
	"HEL.fbs",  // Helicopter Dynamics
	"HFC.fbs",  // Hypersonic Flight Conditions
	"HYP.fbs",  // Hyperbolic Orbit
	"IDM.fbs",  // Initial Data Message
	"ION.fbs",  // Ionospheric Observation
	"IQC.fbs",  // IQ Capture - archived baseband recording (SDS v1.177.0)
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
	"MDP.fbs",  // Mission Design Problem - patched-conic broad search definition
	"MDS.fbs",  // Mission Design Solution Set - candidate trajectories
	"MET.fbs",  // Meteorological Data
	"MFE.fbs",  // Manifold Element Set
	"MNF.fbs",  // Orbit Manifold
	"MNV.fbs",  // Spacecraft Maneuver
	"MPE.fbs",  // Maneuver Planning Ephemeris
	"MSL.fbs",  // Guided Missiles
	"MST.fbs",  // Missile Track
	"MTI.fbs",  // Moving Target Indicator
	"NAV.fbs",  // Naval Vessels
	"NUM.fbs",  // Numerical Methods
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
	"OPP.fbs",  // Object Physical Properties - sourced physical description
	"OSM.fbs",  // Orbit State Message
	"PCF.fbs",  // Propagator Configuration
	"PGM.fbs",  // Peer Group Membership Record
	"PGR.fbs",  // Peer Graph Record - Peer network graph snapshot (SDN-internal)
	"PHY.fbs",  // Physics and Rigid Body Dynamics
	"PIV.fbs",  // Plugin Invoke - Plugin request/response envelopes
	"PKB.fbs",  // Publisher Key-Broker Descriptor
	"PLD.fbs",  // Payload
	"PLG.fbs",  // Plugin Manifest - Signed plugin distribution record
	"PLHD.fbs", // Publication Log Head - Log head announcement (SDN-internal)
	"PLK.fbs",  // Plugin License Key
	"PLOG.fbs", // Publication Log Entry - Internal compatibility log record (SDN-internal)
	"PMM.fbs",  // Provider Module Manifest - what one provider node offers, signed
	"PNL.fbs",  // Panelled (box-wing) Spacecraft Macro Model
	"PNM.fbs",  // Publish Notification Message
	"PPE.fbs",  // Polynomial Ephemeris
	"PRR.fbs",  // Peer Registry Record
	"PRG.fbs",  // Propagation Settings
	"PRW.fbs",  // Propagator Runtime Wire
	"PUR.fbs",  // Purchase Request - Marketplace purchases
	"QEM.fbs",  // Query Encoder Model
	"RAF.fbs",  // Return All Frames Service (CCSDS 913.1-B-2)
	"RBK.fbs",  // Rigid Body Kinematics
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
	"RPT.fbs",  // Verifiable Report descriptor
	"SAR.fbs",  // SAR Observation
	"SBM.fbs",  // Satellite Breakup Model
	"SCC.fbs",  // Scenario Controls - scenario setup/state message bus envelope
	"SCM.fbs",  // Spacecraft Message
	"SCN.fbs",  // Scenario - canonical scene composition and simulation state
	"SCV.fbs",  // Sensor Coverage
	"SCX.fbs",  // Chain Settlement / Smart-Contract Descriptor
	"SDF.fbs",  // Signed Distance Field
	"SDL.fbs",  // Space Data Link Security (CCSDS 355.0-B-1)
	"SDR.fbs",  // Sensor Detection Report
	"SEN.fbs",  // Sensor Management
	"SEO.fbs",  // Space Environment Observation
	"SEV.fbs",  // Space Environment Observation Detail
	"SHC.fbs",  // Spherical-Harmonic Coefficient Set - a gravity field
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
	"STO.fbs",  // Store Descriptor - storefront identity
	"STR.fbs",  // Star Catalog Entry
	"STV.fbs",  // State Vector
	"SUB.fbs",  // Crypto-native Subscription Authorization
	"SWR.fbs",  // Short-Wave Infrared Observation
	"TAB.fbs",  // Typed Arena Buffer
	"TCF.fbs",  // Telecommand Transfer Frame (CCSDS 232.0-B-3)
	"TDM.fbs",  // Tracking Data Message
	"TIM.fbs",  // Time Message
	"TKG.fbs",  // Tracking and Data Fusion
	"TME.fbs",  // Time Systems
	"TMF.fbs",  // Telemetry Transfer Frame (CCSDS 132.0-B-2)
	"TNR.fbs",  // Trust Node Record
	"TPN.fbs",  // Transponder
	"TRE.fbs",  // Trust Edge Record
	"TRK.fbs",  // Track
	"TRN.fbs",  // Terrain Models
	"VAM.fbs",  // Visual Asset Manifest - ranked visual representations for one entity
	"VCM.fbs",  // Vector Covariance Message
	"VST.fbs",  // Viewer State - display and camera state for a scenario
	"WKS.fbs",  // Workspace - scene snapshot + FlatSQL query state + share grants
	"WPN.fbs",  // Weapons and Munitions
	"WTH.fbs",  // Weather Data
	"XTC.fbs",  // XTCE SpaceSystem Document
}

// Validator validates data against SDS schemas.
type Validator struct {
	flatc       *wasm.FlatcModule
	schemas     map[string]int    // schema name -> schema ID
	identifiers map[string]string // schema name -> 4-byte FlatBuffers file_identifier
	mu          sync.RWMutex
}

// NewValidator creates a new SDS validator.
func NewValidator(flatc *wasm.FlatcModule) (*Validator, error) {
	v := &Validator{
		flatc:       flatc,
		schemas:     make(map[string]int),
		identifiers: make(map[string]string),
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
	}

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
// Envelope verification (VerifyEnvelope) runs on EVERY path, with or without the
// optional flatc WASM module. This is deliberate: findWasmPath() only probes a
// handful of build-tree paths, so in every packaged deployment flatc is nil —
// and the old "no WASM ⇒ just check the data is non-empty" fallback meant a
// 1-byte junk body was stored as if it were a valid SDS record. Structure plus
// the schema's declared file_identifier are now always enforced.
func (v *Validator) Validate(ctx context.Context, schemaName string, data []byte) error {
	v.mu.RLock()
	schemaID, ok := v.schemas[schemaName]
	v.mu.RUnlock()

	if !ok {
		return fmt.Errorf("unknown schema: %s", schemaName)
	}

	if err := v.VerifyEnvelope(schemaName, data); err != nil {
		return err
	}

	// When the WASM module is available, additionally parse the buffer through
	// flatc for full field-level validation.
	if v.flatc != nil {
		if _, err := v.flatc.BinaryToJSON(ctx, schemaID, data); err != nil {
			return fmt.Errorf("validation failed for %s: %w", schemaName, err)
		}
	}

	return nil
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
// Two wire forms are accepted:
//
//   - size-prefixed (the canonical SDN form — every internal builder finishes with
//     FinishSizePrefixed<X>Buffer, and the FlatSQL store only decodes records that
//     satisfy <X>.SizePrefixedXBufferHasIdentifier), and
//   - a plain finished buffer, tolerated for producers that call Finish directly.
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
