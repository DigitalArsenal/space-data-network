// Package sds provides Space Data Standards schema registry and management.
package sds

import (
	"embed"
	"fmt"
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

		content, err := sdsSchemasFS.ReadFile(embeddedSchemaPath(entry.Name()))
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
	"ACL.fbs":  "Access Control List - Data access grants for marketplace purchases",
	"ACM.fbs":  "Attitude Comprehensive Message",
	"ACR.fbs":  "Aircraft Dynamics",
	"ACW.fbs":  "Access Windows",
	"AEM.fbs":  "Attitude Ephemeris Message",
	"ANI.fbs":  "Analytic Imagery Product",
	"AOF.fbs":  "AOS Transfer Frame (CCSDS 732.0-B-3)",
	"APM.fbs":  "Attitude Parameter Message",
	"ARM.fbs":  "Armor and Protection",
	"AST.fbs":  "Astrodynamics",
	"ATD.fbs":  "Attitude Data Point",
	"ATM.fbs":  "Attitude Message - Spacecraft attitude information",
	"BAL.fbs":  "Ballistics",
	"BEM.fbs":  "Antenna Beam",
	"BMC.fbs":  "Beam Contour",
	"BOV.fbs":  "Body Orientation and Velocity - Attitude and angular velocity",
	"BSP.fbs":  "Body State Propagation",
	"BUS.fbs":  "Satellite Bus Specification",
	"CAQ.fbs":  "Catalog Query - Catalog query envelope",
	"CAT.fbs":  "Catalog - Space object catalog entries",
	"CDM.fbs":  "Conjunction Data Message - Close approach warnings between objects",
	"CFP.fbs":  "CCSDS File Delivery Protocol PDU (CCSDS 727.0-B-5)",
	"CHN.fbs":  "Communications Channel",
	"CLT.fbs":  "Command Link Transmission Unit Service (CCSDS 912.3-B-2)",
	"CMS.fbs":  "Communications Payload",
	"COM.fbs":  "Communications Systems",
	"COT.fbs":  "Cursor on Target Event",
	"CRD.fbs":  "Coordinate Systems",
	"CRM.fbs":  "Collision Risk Message - Collision probability assessments",
	"CSM.fbs":  "Conjunction Summary Message - Brief conjunction event summary",
	"CTR.fbs":  "Contact Report - Communication contact reports",
	"CZM.fbs":  "CZML Document",
	"DFH.fbs":  "GEO Drift History",
	"DMG.fbs":  "Damage Models",
	"DOA.fbs":  "Difference of Arrival Geolocation",
	"DPM.fbs":  "Dataset Publication Manifest - Signed dataset publication contract",
	"DSS.fbs":  "Data Sync Status",
	"EME.fbs":  "Electromagnetic Emissions - RF and electromagnetic data",
	"ENC.fbs":  "Encryption Header",
	"ENV.fbs":  "Atmosphere and Environment",
	"EOO.fbs":  "Earth Orientation - Earth orientation parameters",
	"EOP.fbs":  "Earth Orientation Parameters - Polar motion and UT1-UTC",
	"EPM.fbs":  "Entity Profile Manifest - Organization identity and contact information",
	"ESL.fbs":  "Entity/Standards Link",
	"ETM.fbs":  "Entity Metadata",
	"EWR.fbs":  "Electronic Warfare",
	"FCS.fbs":  "Fire Control Systems",
	"FPC.fbs":  "Fastest Path Compute",
	"FRM.fbs":  "Frame Transform",
	"GDI.fbs":  "Ground Imagery",
	"GEO.fbs":  "GEO Spacecraft Status",
	"GJN.fbs":  "GeoJSON FeatureCollection",
	"GNO.fbs":  "GNSS Observation",
	"GPX.fbs":  "GPX Document",
	"GRV.fbs":  "Gravity Models",
	"GVH.fbs":  "Ground Vehicles",
	"HEL.fbs":  "Helicopter Dynamics",
	"HFC.fbs":  "Hypersonic Flight Conditions",
	"HYP.fbs":  "Hyperbolic Orbit - Hyperbolic trajectory parameters",
	"IDM.fbs":  "Initial Data Message - Initial orbit determination data",
	"ION.fbs":  "Ionospheric Observation",
	"IRO.fbs":  "Infrared Observation",
	"KMF.fbs":  "Key Material Frame",
	"KML.fbs":  "KML Document",
	"KRF.fbs":  "Key Reference Frame",
	"LAM.fbs":  "Launch Ascent Message",
	"LCC.fbs":  "Launch Collision Corridor - Launch trajectory corridors",
	"LCF.fbs":  "Licensing Configuration Frame",
	"LCH.fbs":  "Licensing Challenge Message",
	"LDM.fbs":  "Launch Data Message - Launch event information and parameters",
	"LGR.fbs":  "Licensing Grant Message",
	"LKS.fbs":  "Link Status",
	"LMO.fbs":  "Lambert Solve Result",
	"LMR.fbs":  "Module Control Message",
	"LMS.fbs":  "Lambert Solve Request",
	"LND.fbs":  "Launch Detection",
	"LNE.fbs":  "Launch Event",
	"LPF.fbs":  "Licensing Proof Message",
	"LWK.fbs":  "Wrapped Module Content Key",
	"MBL.fbs":  "Module Bundle Listing",
	"MET.fbs":  "Meteorological Data - Atmospheric and weather data",
	"MFE.fbs":  "Manifold Element Set",
	"MNF.fbs":  "Orbit Manifold",
	"MNV.fbs":  "Spacecraft Maneuver",
	"MPE.fbs":  "Maneuver Planning Ephemeris - Planned maneuver data",
	"MSL.fbs":  "Guided Missiles",
	"MST.fbs":  "Missile Track",
	"MTI.fbs":  "Moving Target Indicator",
	"NAV.fbs":  "Naval Vessels",
	"NUM.fbs":  "Numerical Methods",
	"OBD.fbs":  "Orbit Determination Results",
	"OBT.fbs":  "Orbit Track",
	"OCM.fbs":  "Orbit Comprehensive Message - Full orbit data package",
	"OEM.fbs":  "Orbit Ephemeris Message - Time-series position/velocity data",
	"OMM.fbs":  "Orbit Mean-Elements Message - Satellite orbital parameters (TLE/3LE)",
	"OOA.fbs":  "On-Orbit Antenna",
	"OOB.fbs":  "On-Orbit Battery",
	"OOD.fbs":  "On-Orbit Object Details",
	"OOE.fbs":  "On-Orbit Event",
	"OOI.fbs":  "Object of Interest",
	"OOL.fbs":  "On-Orbit Object List",
	"OON.fbs":  "On-Orbit Object",
	"OOS.fbs":  "On-Orbit Solar Array",
	"OOT.fbs":  "On-Orbit Thruster",
	"OPM.fbs":  "Orbit Parameter Message",
	"OSM.fbs":  "Orbit State Message - Orbit state vectors",
	"PCF.fbs":  "Propagator Configuration",
	"PGR.fbs":  "Peer Graph Record - Peer network graph snapshot (SDN-internal)",
	"PHY.fbs":  "Physics and Rigid Body Dynamics",
	"PIV.fbs":  "Plugin Invoke - Plugin request/response envelopes",
	"PLD.fbs":  "Payload - Spacecraft payload information",
	"PLG.fbs":  "Plugin Manifest - Signed plugin distribution record",
	"PLHD.fbs": "Publication Log Head - Lightweight log head announcement via GossipSub (SDN-internal)",
	"PLK.fbs":  "Plugin License Key",
	"PLOG.fbs": "Publication Log Entry - Internal compatibility log record (SDN-internal)",
	"PNM.fbs":  "Publish Notification Message - Signed publication announcement",
	"PPE.fbs":  "Polynomial Ephemeris",
	"PRG.fbs":  "Propagation Settings - Orbit propagation parameters",
	"PRW.fbs":  "Propagator Runtime Wire",
	"PUR.fbs":  "Purchase Request - Marketplace purchase requests",
	"RAF.fbs":  "Return All Frames Service (CCSDS 913.1-B-2)",
	"RBK.fbs":  "Rigid Body Kinematics",
	"RCF.fbs":  "Return Channel Frames Service (CCSDS 913.5-B-2)",
	"RDM.fbs":  "Reentry Data Message",
	"RDO.fbs":  "Radar Observation",
	"REC.fbs":  "Records - Data records and observations",
	"REM.fbs":  "Reentry Evaluation Message",
	"REV.fbs":  "Review - Marketplace listing reviews and ratings",
	"RFB.fbs":  "RF Band Specification",
	"RFE.fbs":  "RF Emitter",
	"RFM.fbs":  "Reference Frame Message - Coordinate frame definitions",
	"RFO.fbs":  "RF Observation",
	"RHD.fbs":  "Routing Header - Message routing metadata for PubSub",
	"ROC.fbs":  "Re-entry Operations Corridor - Re-entry trajectory corridors",
	"SAR.fbs":  "SAR Observation",
	"SCM.fbs":  "Spacecraft Message - Spacecraft characteristics",
	"SCV.fbs":  "Sensor Coverage",
	"SDF.fbs":  "Signed Distance Field",
	"SDL.fbs":  "Space Data Link Security (CCSDS 355.0-B-1)",
	"SDR.fbs":  "Sensor Detection Report",
	"SEN.fbs":  "Sensor Management",
	"SEO.fbs":  "Space Environment Observation",
	"SEV.fbs":  "Space Environment Observation Detail",
	"SHW.fbs":  "Shader Wire",
	"SIT.fbs":  "Satellite Impact Table - Impact risk assessments",
	"SKI.fbs":  "Sky Imagery",
	"SNR.fbs":  "Sensor Systems",
	"SNW.fbs":  "Sensor Runtime Wire",
	"SOI.fbs":  "Space Object Identification Observation Set",
	"SON.fbs":  "Sonar and Underwater Acoustics",
	"SPP.fbs":  "Space Packet Protocol (CCSDS 133.0-B-1)",
	"SPW.fbs":  "Space Weather Data Record - Solar flux, geomagnetic, and space weather indices",
	"SRI.fbs":  "Standards Record Index",
	"STF.fbs":  "Storefront Listing - Marketplace data listings",
	"STR.fbs":  "Star Catalog Entry",
	"STV.fbs":  "State Vector",
	"SWR.fbs":  "Short-Wave Infrared Observation",
	"TAB.fbs":  "Typed Arena Buffer",
	"TCF.fbs":  "Telecommand Transfer Frame (CCSDS 232.0-B-3)",
	"TDM.fbs":  "Tracking Data Message - Radar/optical observations",
	"TIM.fbs":  "Time Message - Time synchronization data",
	"TKG.fbs":  "Tracking and Data Fusion",
	"TME.fbs":  "Time Systems",
	"TMF.fbs":  "Telemetry Transfer Frame (CCSDS 132.0-B-2)",
	"TNR.fbs":  "Trust Node Record - Durable node membership state for an SDN trust graph",
	"TPN.fbs":  "Transponder",
	"TRE.fbs":  "Trust Edge Record - Durable directed trust assertion for an SDN trust graph",
	"TRK.fbs":  "Track",
	"TRN.fbs":  "Terrain Models",
	"VCM.fbs":  "Vector Covariance Message - State vector with covariance",
	"WPN.fbs":  "Weapons and Munitions",
	"WTH.fbs":  "Weather Data",
	"XTC.fbs":  "XTCE SpaceSystem Document",
}

// GetDescription returns the description for a schema.
func (r *SchemaRegistry) GetDescription(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.descriptions[name]
}
