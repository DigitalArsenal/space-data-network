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

	standardsCLM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CLM"
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

// publishedBindingOnlySchemas are newly ratified standards whose published Go
// binding is available before the next embedded-IDL refresh. They are admitted
// by their binding's canonical file identifier, never by a repo-local shadow
// schema. Full field validation for code that decodes one remains the typed
// binding's responsibility (the claims handler does that for $CLM).
var publishedBindingOnlySchemas = map[string]string{
	"CLM.fbs": standardsCLM.CLMIdentifier,
}

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
	"ACI.fbs",  // Access Interval - one contiguous access window with its RF link samples (SDS v1.196.0)
	"ACL.fbs",  // Access Control List - Data access grants
	"ACM.fbs",  // Attitude Comprehensive Message
	"ACR.fbs",  // Aircraft Dynamics
	"ACW.fbs",  // Access Windows
	"AEM.fbs",  // Attitude Ephemeris Message
	"ANI.fbs",  // Analytic Imagery Product
	"AOF.fbs",  // AOS Transfer Frame (CCSDS 732.0-B-3)
	"APL.fbs",  // Atmospheric Propagation Loss Statistics (SDS v1.196.0)
	"APP.fbs",  // Application Package Manifest
	"APM.fbs",  // Attitude Parameter Message
	"ARM.fbs",  // Armor and Protection
	"AST.fbs",  // Astrodynamics
	"ATD.fbs",  // Attitude Data Point
	"ATM.fbs",  // Attitude Message
	"AVL.fbs",  // Airspace Volume (SDS v1.196.0)
	"BAL.fbs",  // Ballistics
	"BEM.fbs",  // Antenna Beam
	"BMC.fbs",  // Beam Contour
	"BOV.fbs",  // Body Orientation and Velocity
	"BSP.fbs",  // Body State Propagation
	"BUS.fbs",  // Satellite Bus Specification
	"CAQ.fbs",  // Catalog Query - Catalog query envelope
	"CAT.fbs",  // Catalog
	"CCT.fbs",  // Capability Category Taxonomy - the ratified category vocabulary ($PLG/$APP/$PMM/$STF include it)
	"CDM.fbs",  // Conjunction Data Message
	"CES.fbs",  // Catalog Embedding Shard
	"CMR.fbs",  // Constellation Membership Record (SDS v1.186.0; included by REC)
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
	"CVP.fbs",  // Coverage Geometry (SDS v1.196.0)
	"CZM.fbs",  // CZML Document
	"DFH.fbs",  // GEO Drift History
	"DMG.fbs",  // Damage Models
	"DOA.fbs",  // Difference of Arrival Geolocation
	"DPM.fbs",  // Dataset Publication Manifest
	"DSS.fbs",  // Data Sync Status
	"DTT.fbs",  // Digital Terrain Tile - one addressable tile of a terrain elevation pyramid (SDS v1.196.0)
	"EME.fbs",  // Electromagnetic Emissions
	"ENC.fbs",  // Encryption Header
	"ENT.fbs",  // Entitlement - provider subscription or account entitlement state
	"ENV.fbs",  // Atmosphere and Environment
	"EOO.fbs",  // Earth Orientation
	"EGP.fbs",  // Entity Group - publishable set of entity references
	"EMC.fbs",  // Electromagnetic Compatibility Assessment (SDS v1.196.0)
	"EOP.fbs",  // Earth Orientation Parameters
	"EPF.fbs",  // Aggregate Interference and Flux-Density Compliance (SDS v1.196.0)
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
	"GEL.fbs",  // Emitter Geolocation Solution (SDS v1.196.0)
	"GEO.fbs",  // GEO Spacecraft Status
	"GJN.fbs",  // GeoJSON FeatureCollection
	"GNO.fbs",  // GNSS Observation
	"GNP.fbs",  // Gazetteer Named Place (SDS v1.196.0)
	"GPX.fbs",  // GPX Document
	"GRV.fbs",  // Gravity Models
	"GST.fbs",  // Ground/Tracking Station Definition
	"GVH.fbs",  // Ground Vehicles
	"HEL.fbs",  // Helicopter Dynamics
	"HFC.fbs",  // Hypersonic Flight Conditions
	"HYP.fbs",  // Hyperbolic Orbit
	"ICN.fbs",  // Ingest Connector
	"IDM.fbs",  // Initial Data Message
	"ION.fbs",  // Ionospheric Observation
	"IQC.fbs",  // IQ Capture - archived baseband recording (SDS v1.177.0)
	"IRM.fbs",  // Ingest Resume Mark - durable checkpoint of one bulk-ingest job (REC 223) (SDS v1.196.0)
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
	"NCD.fbs",  // Native container descriptors (SDS v1.215.0)
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
	"PAP.fbs",  // Phased Array Pattern Synthesis (SDS v1.196.0)
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
	"RFL.fbs",  // RF Link Sample (SDS v1.196.0)
	"RFM.fbs",  // Reference Frame Message
	"RFO.fbs",  // RF Observation
	"RFS.fbs",  // RF Surface Material (SDS v1.196.0)
	"RHD.fbs",  // Routing Header - Message routing metadata (SDN-internal)
	"ROC.fbs",  // Re-entry Operations Corridor
	"RPT.fbs",  // Verifiable Report descriptor
	"RSD.fbs",  // Radar Sensitivity and Detection Performance (SDS v1.196.0)
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
	"STX.fbs",  // Scheduled Transmission - one broadcast schedule row for a terrestrial transmitter (REC 225) (SDS v1.198.0)
	"SUB.fbs",  // Crypto-native Subscription Authorization
	"SWR.fbs",  // Short-Wave Infrared Observation
	"TAB.fbs",  // Typed Arena Buffer
	"TBS.fbs",  // Terrestrial Base Station Site (SDS v1.186.0; included by REC)
	"TCF.fbs",  // Telecommand Transfer Frame (CCSDS 232.0-B-3)
	"TDM.fbs",  // Tracking Data Message
	"TFN.fbs",  // Transport Facility Node (SDS v1.196.0)
	"TIM.fbs",  // Time Message
	"TKG.fbs",  // Tracking and Data Fusion
	"TME.fbs",  // Time Systems
	"TMF.fbs",  // Telemetry Transfer Frame (CCSDS 132.0-B-2)
	"TMS.fbs",  // Track Model State (SDS v1.196.0)
	"TNR.fbs",  // Trust Node Record
	"TPN.fbs",  // Transponder
	"TRE.fbs",  // Trust Edge Record
	"TRP.fbs",  // Trust Rule Policy
	"TRV.fbs",  // Trust Rule Verdict
	"TRK.fbs",  // Track
	"TRN.fbs",  // Terrain Models
	"TRS.fbs",  // Terrain Raster Solve (SDS v1.196.0)
	"TXS.fbs",  // Terrestrial Transmitter Site - merged, source-attributed transmitter facility (REC 226) (SDS v1.198.0)
	"VAM.fbs",  // Visual Asset Manifest - ranked visual representations for one entity
	"VCF.fbs",  // vCard Projection Card - canonical contact-card projection of one published EPM (REC 224) (SDS v1.197.0)
	"VCM.fbs",  // Vector Covariance Message
	"VEP.fbs",  // Vehicle Endurance Profile (SDS v1.196.0)
	"VST.fbs",  // Viewer State - display and camera state for a scenario
	"WKS.fbs",  // Workspace - scene snapshot + FlatSQL query state + share grants
	"WPN.fbs",  // Weapons and Munitions
	"WTH.fbs",  // Weather Data
	"WXF.fbs",  // Weather forecast fields (SDS v1.215.0)
	"XTC.fbs",  // XTCE SpaceSystem Document
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
// It can be enabled when both are fixed: an alignment-tolerant verify in the
// converter, and an AOT-compiled converter.
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
	v.registerPublishedBindingSchemas()

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

func (v *Validator) registerPublishedBindingSchemas() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for name, ident := range publishedBindingOnlySchemas {
		v.identifiers[name] = ident
		switch {
		case v.ambiguousIdents[ident]:
		case v.identifierSchemas[ident] == "" || v.identifierSchemas[ident] == name:
			v.identifierSchemas[ident] = name
		default:
			delete(v.identifierSchemas, ident)
			v.ambiguousIdents[ident] = true
		}
	}
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
// The converter's field-level parse (full verification against the schema)
// runs additionally only when FieldLevelValidation is on.
func (v *Validator) Validate(ctx context.Context, schemaName string, data []byte) error {
	v.mu.RLock()
	_, ok := v.schemas[schemaName]
	v.mu.RUnlock()

	_, bindingOnly := publishedBindingOnlySchemas[schemaName]
	if !ok && !bindingOnly {
		return fmt.Errorf("unknown schema: %s", schemaName)
	}

	if err := v.VerifyEnvelope(schemaName, data); err != nil {
		return err
	}

	// Field-level parse: only for a schema the converter can load; one it
	// cannot keeps the envelope verdict.
	if ok && v.FieldLevelValidation() {
		if flatcID, err := v.converterID(ctx, schemaName); err == nil {
			opts := wasm.FlatcOption(0)
			if form, ferr := v.DetectEnvelopeForm(schemaName, data); ferr == nil && form == EnvelopeSizePrefixed {
				opts = wasm.FlatcSizePrefixed
			}
			if err := v.flatc.VerifyBinary(ctx, flatcID, data, opts); err != nil {
				return fmt.Errorf("validation failed for %s: %w", schemaName, err)
			}
		}
	}

	return nil
}

// IsPublishedBindingSchema reports whether name is admitted directly from a
// published generated binding while its IDL is intentionally not copied into
// this repository.
func IsPublishedBindingSchema(name string) bool {
	_, ok := publishedBindingOnlySchemas[name]
	return ok
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
// form reads: the size-prefix option is taken from the record's envelope, and
// opts supplies the rest (wasm.FlatcCompactJSON, wasm.FlatcForceDefaults, ...).
func (v *Validator) FlatBufferToJSON(ctx context.Context, schemaName string, binaryData []byte, opts wasm.FlatcOption) ([]byte, error) {
	id, err := v.converterID(ctx, schemaName)
	if err != nil {
		return nil, err
	}
	opts &^= wasm.FlatcSizePrefixed
	if form, ferr := v.DetectEnvelopeForm(schemaName, binaryData); ferr == nil && form == EnvelopeSizePrefixed {
		opts |= wasm.FlatcSizePrefixed
	}
	return v.flatc.BinaryToJSON(ctx, id, binaryData, opts)
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
