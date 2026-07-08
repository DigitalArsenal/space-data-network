/**
 * SDS Schema definitions and constants
 */

export const SUPPORTED_SCHEMAS = [
  'ACL.fbs',    // Access Control List - Data access grants for marketplace
  'ACM.fbs',    // Attitude Comprehensive Message
  'ACR.fbs',    // Aircraft Dynamics
  'AEM.fbs',    // Attitude Ephemeris Message
  'ANI.fbs',    // Analytic Imagery Product
  'AOF.fbs',    // AOS Transfer Frame (CCSDS 732.0-B-3)
  'APM.fbs',    // Attitude Parameter Message
  'ARM.fbs',    // Armor and Protection
  'AST.fbs',    // Astrodynamics
  'ATD.fbs',    // Attitude Data Point
  'ATM.fbs',    // Attitude Message - Spacecraft attitude information
  'BAL.fbs',    // Ballistics
  'BEM.fbs',    // Antenna Beam
  'BMC.fbs',    // Beam Contour
  'BOV.fbs',    // Body Orientation and Velocity - Attitude and angular velocity
  'BUS.fbs',    // Satellite Bus Specification
  'CAQ.fbs',    // Catalog Query - Catalog query envelope
  'CAT.fbs',    // Catalog - Space object catalog entries
  'CDM.fbs',    // Conjunction Data Message - Close approach warnings
  'CFP.fbs',    // CCSDS File Delivery Protocol PDU (CCSDS 727.0-B-5)
  'CHN.fbs',    // Communications Channel
  'CLT.fbs',    // Command Link Transmission Unit Service (CCSDS 912.3-B-2)
  'CMS.fbs',    // Communications Payload
  'COM.fbs',    // Communications Systems
  'COT.fbs',    // Cursor on Target Event
  'CRD.fbs',    // Coordinate Systems
  'CRM.fbs',    // Collision Risk Message - Collision probability assessments
  'CSM.fbs',    // Conjunction Summary Message - Brief conjunction events
  'CTR.fbs',    // Contact Report - Communication contact reports
  'CZM.fbs',    // CZML Document
  'DFH.fbs',    // GEO Drift History
  'DMG.fbs',    // Damage Models
  'DOA.fbs',    // Difference of Arrival Geolocation
  'DPM.fbs',    // Dataset Publication Manifest
  'DSS.fbs',    // Data Sync Status
  'EME.fbs',    // Electromagnetic Emissions - RF and electromagnetic data
  'ENC.fbs',    // Encryption Header
  'ENV.fbs',    // Atmosphere and Environment
  'EOO.fbs',    // Earth Orientation - Earth orientation parameters
  'EOP.fbs',    // Earth Orientation Parameters - Polar motion and UT1-UTC
  'EPM.fbs',    // Entity Profile Manifest - Organization identity and contact information
  'ESL.fbs',    // Entity/Standards Link
  'ETM.fbs',    // Entity Metadata
  'EWR.fbs',    // Electronic Warfare
  'FCS.fbs',    // Fire Control Systems
  'FPC.fbs',    // Fastest Path Compute
  'GDI.fbs',    // Ground Imagery
  'GEO.fbs',    // GEO Spacecraft Status
  'GJN.fbs',    // GeoJSON FeatureCollection
  'GNO.fbs',    // GNSS Observation
  'GPX.fbs',    // GPX Document
  'GRV.fbs',    // Gravity Models
  'GVH.fbs',    // Ground Vehicles
  'HEL.fbs',    // Helicopter Dynamics
  'HFC.fbs',    // Hypersonic Flight Conditions
  'HYP.fbs',    // Hyperbolic Orbit - Hyperbolic trajectory parameters
  'IDM.fbs',    // Initial Data Message - Initial orbit determination
  'ION.fbs',    // Ionospheric Observation
  'IRO.fbs',    // Infrared Observation
  'KMF.fbs',    // Key Material Frame
  'KML.fbs',    // KML Document
  'KRF.fbs',    // Key Reference Frame
  'LAM.fbs',    // Launch Ascent Message
  'LCC.fbs',    // Launch Collision Corridor - Launch trajectory corridors
  'LCF.fbs',    // Licensing Configuration Frame
  'LCH.fbs',    // Licensing Challenge Message
  'LDM.fbs',    // Launch Data Message - Launch event information
  'LGR.fbs',    // Licensing Grant Message
  'LKS.fbs',    // Link Status
  'LMO.fbs',    // Lambert Solve Result
  'LMR.fbs',    // Module Control Message
  'LMS.fbs',    // Lambert Solve Request
  'LND.fbs',    // Launch Detection
  'LNE.fbs',    // Launch Event
  'LPF.fbs',    // Licensing Proof Message
  'LWK.fbs',    // Wrapped Module Content Key
  'MBL.fbs',    // Module Bundle Listing
  'MET.fbs',    // Meteorological Data - Atmospheric and weather data
  'MFE.fbs',    // Manifold Element Set
  'MNF.fbs',    // Orbit Manifold
  'MNV.fbs',    // Spacecraft Maneuver
  'MPE.fbs',    // Maneuver Planning Ephemeris - Planned maneuvers
  'MSL.fbs',    // Guided Missiles
  'MST.fbs',    // Missile Track
  'MTI.fbs',    // Moving Target Indicator
  'NAV.fbs',    // Naval Vessels
  'OBD.fbs',    // Orbit Determination Results
  'OBT.fbs',    // Orbit Track
  'OCM.fbs',    // Orbit Comprehensive Message - Full orbit data
  'OEM.fbs',    // Orbit Ephemeris Message - Time-series position/velocity data
  'OMM.fbs',    // Orbit Mean-Elements Message - Satellite orbital parameters
  'OOA.fbs',    // On-Orbit Antenna
  'OOB.fbs',    // On-Orbit Battery
  'OOD.fbs',    // On-Orbit Object Details
  'OOE.fbs',    // On-Orbit Event
  'OOI.fbs',    // Object of Interest
  'OOL.fbs',    // On-Orbit Object List
  'OON.fbs',    // On-Orbit Object
  'OOS.fbs',    // On-Orbit Solar Array
  'OOT.fbs',    // On-Orbit Thruster
  'OPM.fbs',    // Orbit Parameter Message
  'OSM.fbs',    // Orbit State Message - Orbit state vectors
  'PCF.fbs',    // Propagator Configuration
  'PGR.fbs',    // Peer Graph Record - Peer network graph snapshot (SDN-internal)
  'PHY.fbs',    // Physics and Rigid Body Dynamics
  'PIV.fbs',    // Plugin Invoke - Plugin request/response envelopes
  'PLD.fbs',    // Payload - Spacecraft payload information
  'PLG.fbs',    // Plugin Manifest - Signed plugin distribution record
  'PLHD.fbs',   // Publication Log Head - Lightweight log head announcement via GossipSub (SDN-internal)
  'PLK.fbs',    // Plugin License Key
  'PLOG.fbs',   // Publication Log Entry - Internal compatibility log record (SDN-internal)
  'PNM.fbs',    // Publish Notification Message - Signed publication announcement
  'PPE.fbs',    // Polynomial Ephemeris
  'PRG.fbs',    // Propagation Settings - Orbit propagation parameters
  'PRW.fbs',    // Propagator Runtime Wire
  'PUR.fbs',    // Purchase Request - Marketplace purchase requests
  'RAF.fbs',    // Return All Frames Service (CCSDS 913.1-B-2)
  'RCF.fbs',    // Return Channel Frames Service (CCSDS 913.5-B-2)
  'RDM.fbs',    // Reentry Data Message
  'RDO.fbs',    // Radar Observation
  'REC.fbs',    // Records - Data records and observations
  'REM.fbs',    // Reentry Evaluation Message
  'REV.fbs',    // Review - Marketplace listing reviews and ratings
  'RFB.fbs',    // RF Band Specification
  'RFE.fbs',    // RF Emitter
  'RFM.fbs',    // Reference Frame Message - Coordinate frame definitions
  'RFO.fbs',    // RF Observation
  'RHD.fbs',    // Routing Header - Message routing metadata for PubSub
  'ROC.fbs',    // Re-entry Operations Corridor - Re-entry trajectory corridors
  'SAR.fbs',    // SAR Observation
  'SCM.fbs',    // Spacecraft Message - Spacecraft characteristics
  'SDF.fbs',    // Signed Distance Field
  'SDL.fbs',    // Space Data Link Security (CCSDS 355.0-B-1)
  'SDR.fbs',    // Sensor Detection Report
  'SEN.fbs',    // Sensor Management
  'SEO.fbs',    // Space Environment Observation
  'SEV.fbs',    // Space Environment Observation Detail
  'SHW.fbs',    // Shader Wire
  'SIT.fbs',    // Satellite Impact Table - Impact assessments
  'SKI.fbs',    // Sky Imagery
  'SNR.fbs',    // Sensor Systems
  'SNW.fbs',    // Sensor Runtime Wire
  'SOI.fbs',    // Space Object Identification Observation Set
  'SON.fbs',    // Sonar and Underwater Acoustics
  'SPP.fbs',    // Space Packet Protocol (CCSDS 133.0-B-1)
  'SPW.fbs',    // Space Weather Data Record - Solar flux, geomagnetic, and space weather indices
  'SRI.fbs',    // Standards Record Index
  'STF.fbs',    // Storefront Listing - Marketplace data listings
  'STR.fbs',    // Star Catalog Entry
  'STV.fbs',    // State Vector
  'SWR.fbs',    // Short-Wave Infrared Observation
  'TAB.fbs',    // Typed Arena Buffer
  'TCF.fbs',    // Telecommand Transfer Frame (CCSDS 232.0-B-3)
  'TDM.fbs',    // Tracking Data Message - Radar/optical observations
  'TIM.fbs',    // Time Message - Time synchronization data
  'TKG.fbs',    // Tracking and Data Fusion
  'TME.fbs',    // Time Systems
  'TMF.fbs',    // Telemetry Transfer Frame (CCSDS 132.0-B-2)
  'TPN.fbs',    // Transponder
  'TRK.fbs',    // Track
  'TRN.fbs',    // Terrain Models
  'VCM.fbs',    // Vector Covariance Message - State vector with covariance
  'WPN.fbs',    // Weapons and Munitions
  'WTH.fbs',    // Weather Data
  'XTC.fbs',    // XTCE SpaceSystem Document
] as const;

export type SchemaName = typeof SUPPORTED_SCHEMAS[number];

/**
 * Schema descriptions
 */
export const SCHEMA_DESCRIPTIONS: Record<SchemaName, string> = {
  'ACL.fbs': 'Access Control List - Data access grants for marketplace',
  'ACM.fbs': 'Attitude Comprehensive Message',
  'ACR.fbs': 'Aircraft Dynamics',
  'AEM.fbs': 'Attitude Ephemeris Message',
  'ANI.fbs': 'Analytic Imagery Product',
  'AOF.fbs': 'AOS Transfer Frame (CCSDS 732.0-B-3)',
  'APM.fbs': 'Attitude Parameter Message',
  'ARM.fbs': 'Armor and Protection',
  'AST.fbs': 'Astrodynamics',
  'ATD.fbs': 'Attitude Data Point',
  'ATM.fbs': 'Attitude Message - Spacecraft attitude information',
  'BAL.fbs': 'Ballistics',
  'BEM.fbs': 'Antenna Beam',
  'BMC.fbs': 'Beam Contour',
  'BOV.fbs': 'Body Orientation and Velocity - Attitude and angular velocity',
  'BUS.fbs': 'Satellite Bus Specification',
  'CAQ.fbs': 'Catalog Query - Catalog query envelope',
  'CAT.fbs': 'Catalog - Space object catalog entries',
  'CDM.fbs': 'Conjunction Data Message - Close approach warnings',
  'CFP.fbs': 'CCSDS File Delivery Protocol PDU (CCSDS 727.0-B-5)',
  'CHN.fbs': 'Communications Channel',
  'CLT.fbs': 'Command Link Transmission Unit Service (CCSDS 912.3-B-2)',
  'CMS.fbs': 'Communications Payload',
  'COM.fbs': 'Communications Systems',
  'COT.fbs': 'Cursor on Target Event',
  'CRD.fbs': 'Coordinate Systems',
  'CRM.fbs': 'Collision Risk Message - Collision probability assessments',
  'CSM.fbs': 'Conjunction Summary Message - Brief conjunction events',
  'CTR.fbs': 'Contact Report - Communication contact reports',
  'CZM.fbs': 'CZML Document',
  'DFH.fbs': 'GEO Drift History',
  'DMG.fbs': 'Damage Models',
  'DOA.fbs': 'Difference of Arrival Geolocation',
  'DPM.fbs': 'Dataset Publication Manifest',
  'DSS.fbs': 'Data Sync Status',
  'EME.fbs': 'Electromagnetic Emissions - RF and electromagnetic data',
  'ENC.fbs': 'Encryption Header',
  'ENV.fbs': 'Atmosphere and Environment',
  'EOO.fbs': 'Earth Orientation - Earth orientation parameters',
  'EOP.fbs': 'Earth Orientation Parameters - Polar motion and UT1-UTC',
  'EPM.fbs': 'Entity Profile Manifest - Organization identity and contact information',
  'ESL.fbs': 'Entity/Standards Link',
  'ETM.fbs': 'Entity Metadata',
  'EWR.fbs': 'Electronic Warfare',
  'FCS.fbs': 'Fire Control Systems',
  'FPC.fbs': 'Fastest Path Compute',
  'GDI.fbs': 'Ground Imagery',
  'GEO.fbs': 'GEO Spacecraft Status',
  'GJN.fbs': 'GeoJSON FeatureCollection',
  'GNO.fbs': 'GNSS Observation',
  'GPX.fbs': 'GPX Document',
  'GRV.fbs': 'Gravity Models',
  'GVH.fbs': 'Ground Vehicles',
  'HEL.fbs': 'Helicopter Dynamics',
  'HFC.fbs': 'Hypersonic Flight Conditions',
  'HYP.fbs': 'Hyperbolic Orbit - Hyperbolic trajectory parameters',
  'IDM.fbs': 'Initial Data Message - Initial orbit determination',
  'ION.fbs': 'Ionospheric Observation',
  'IRO.fbs': 'Infrared Observation',
  'KMF.fbs': 'Key Material Frame',
  'KML.fbs': 'KML Document',
  'KRF.fbs': 'Key Reference Frame',
  'LAM.fbs': 'Launch Ascent Message',
  'LCC.fbs': 'Launch Collision Corridor - Launch trajectory corridors',
  'LCF.fbs': 'Licensing Configuration Frame',
  'LCH.fbs': 'Licensing Challenge Message',
  'LDM.fbs': 'Launch Data Message - Launch event information',
  'LGR.fbs': 'Licensing Grant Message',
  'LKS.fbs': 'Link Status',
  'LMO.fbs': 'Lambert Solve Result',
  'LMR.fbs': 'Module Control Message',
  'LMS.fbs': 'Lambert Solve Request',
  'LND.fbs': 'Launch Detection',
  'LNE.fbs': 'Launch Event',
  'LPF.fbs': 'Licensing Proof Message',
  'LWK.fbs': 'Wrapped Module Content Key',
  'MBL.fbs': 'Module Bundle Listing',
  'MET.fbs': 'Meteorological Data - Atmospheric and weather data',
  'MFE.fbs': 'Manifold Element Set',
  'MNF.fbs': 'Orbit Manifold',
  'MNV.fbs': 'Spacecraft Maneuver',
  'MPE.fbs': 'Maneuver Planning Ephemeris - Planned maneuvers',
  'MSL.fbs': 'Guided Missiles',
  'MST.fbs': 'Missile Track',
  'MTI.fbs': 'Moving Target Indicator',
  'NAV.fbs': 'Naval Vessels',
  'OBD.fbs': 'Orbit Determination Results',
  'OBT.fbs': 'Orbit Track',
  'OCM.fbs': 'Orbit Comprehensive Message - Full orbit data',
  'OEM.fbs': 'Orbit Ephemeris Message - Time-series position/velocity data',
  'OMM.fbs': 'Orbit Mean-Elements Message - Satellite orbital parameters',
  'OOA.fbs': 'On-Orbit Antenna',
  'OOB.fbs': 'On-Orbit Battery',
  'OOD.fbs': 'On-Orbit Object Details',
  'OOE.fbs': 'On-Orbit Event',
  'OOI.fbs': 'Object of Interest',
  'OOL.fbs': 'On-Orbit Object List',
  'OON.fbs': 'On-Orbit Object',
  'OOS.fbs': 'On-Orbit Solar Array',
  'OOT.fbs': 'On-Orbit Thruster',
  'OPM.fbs': 'Orbit Parameter Message',
  'OSM.fbs': 'Orbit State Message - Orbit state vectors',
  'PCF.fbs': 'Propagator Configuration',
  'PGR.fbs': 'Peer Graph Record - Peer network graph snapshot (SDN-internal)',
  'PHY.fbs': 'Physics and Rigid Body Dynamics',
  'PIV.fbs': 'Plugin Invoke - Plugin request/response envelopes',
  'PLD.fbs': 'Payload - Spacecraft payload information',
  'PLG.fbs': 'Plugin Manifest - Signed plugin distribution record',
  'PLHD.fbs': 'Publication Log Head - Lightweight log head announcement via GossipSub (SDN-internal)',
  'PLK.fbs': 'Plugin License Key',
  'PLOG.fbs': 'Publication Log Entry - Internal compatibility log record (SDN-internal)',
  'PNM.fbs': 'Publish Notification Message - Signed publication announcement',
  'PPE.fbs': 'Polynomial Ephemeris',
  'PRG.fbs': 'Propagation Settings - Orbit propagation parameters',
  'PRW.fbs': 'Propagator Runtime Wire',
  'PUR.fbs': 'Purchase Request - Marketplace purchase requests',
  'RAF.fbs': 'Return All Frames Service (CCSDS 913.1-B-2)',
  'RCF.fbs': 'Return Channel Frames Service (CCSDS 913.5-B-2)',
  'RDM.fbs': 'Reentry Data Message',
  'RDO.fbs': 'Radar Observation',
  'REC.fbs': 'Records - Data records and observations',
  'REM.fbs': 'Reentry Evaluation Message',
  'REV.fbs': 'Review - Marketplace listing reviews and ratings',
  'RFB.fbs': 'RF Band Specification',
  'RFE.fbs': 'RF Emitter',
  'RFM.fbs': 'Reference Frame Message - Coordinate frame definitions',
  'RFO.fbs': 'RF Observation',
  'RHD.fbs': 'Routing Header - Message routing metadata for PubSub',
  'ROC.fbs': 'Re-entry Operations Corridor - Re-entry trajectory corridors',
  'SAR.fbs': 'SAR Observation',
  'SCM.fbs': 'Spacecraft Message - Spacecraft characteristics',
  'SDF.fbs': 'Signed Distance Field',
  'SDL.fbs': 'Space Data Link Security (CCSDS 355.0-B-1)',
  'SDR.fbs': 'Sensor Detection Report',
  'SEN.fbs': 'Sensor Management',
  'SEO.fbs': 'Space Environment Observation',
  'SEV.fbs': 'Space Environment Observation Detail',
  'SHW.fbs': 'Shader Wire',
  'SIT.fbs': 'Satellite Impact Table - Impact assessments',
  'SKI.fbs': 'Sky Imagery',
  'SNR.fbs': 'Sensor Systems',
  'SNW.fbs': 'Sensor Runtime Wire',
  'SOI.fbs': 'Space Object Identification Observation Set',
  'SON.fbs': 'Sonar and Underwater Acoustics',
  'SPP.fbs': 'Space Packet Protocol (CCSDS 133.0-B-1)',
  'SPW.fbs': 'Space Weather Data Record - Solar flux, geomagnetic, and space weather indices',
  'SRI.fbs': 'Standards Record Index',
  'STF.fbs': 'Storefront Listing - Marketplace data listings',
  'STR.fbs': 'Star Catalog Entry',
  'STV.fbs': 'State Vector',
  'SWR.fbs': 'Short-Wave Infrared Observation',
  'TAB.fbs': 'Typed Arena Buffer',
  'TCF.fbs': 'Telecommand Transfer Frame (CCSDS 232.0-B-3)',
  'TDM.fbs': 'Tracking Data Message - Radar/optical observations',
  'TIM.fbs': 'Time Message - Time synchronization data',
  'TKG.fbs': 'Tracking and Data Fusion',
  'TME.fbs': 'Time Systems',
  'TMF.fbs': 'Telemetry Transfer Frame (CCSDS 132.0-B-2)',
  'TPN.fbs': 'Transponder',
  'TRK.fbs': 'Track',
  'TRN.fbs': 'Terrain Models',
  'VCM.fbs': 'Vector Covariance Message - State vector with covariance',
  'WPN.fbs': 'Weapons and Munitions',
  'WTH.fbs': 'Weather Data',
  'XTC.fbs': 'XTCE SpaceSystem Document',
};

/**
 * Bundled schema content (populated at build time)
 */
export const SDS_SCHEMAS: Record<SchemaName, string> = {
  'ACL.fbs': '',
  'ACM.fbs': '',
  'ACR.fbs': '',
  'AEM.fbs': '',
  'ANI.fbs': '',
  'AOF.fbs': '',
  'APM.fbs': '',
  'ARM.fbs': '',
  'AST.fbs': '',
  'ATD.fbs': '',
  'ATM.fbs': '',
  'BAL.fbs': '',
  'BEM.fbs': '',
  'BMC.fbs': '',
  'BOV.fbs': '',
  'BUS.fbs': '',
  'CAQ.fbs': '',
  'CAT.fbs': '',
  'CDM.fbs': '',
  'CFP.fbs': '',
  'CHN.fbs': '',
  'CLT.fbs': '',
  'CMS.fbs': '',
  'COM.fbs': '',
  'COT.fbs': '',
  'CRD.fbs': '',
  'CRM.fbs': '',
  'CSM.fbs': '',
  'CTR.fbs': '',
  'CZM.fbs': '',
  'DFH.fbs': '',
  'DMG.fbs': '',
  'DOA.fbs': '',
  'DPM.fbs': '',
  'DSS.fbs': '',
  'EME.fbs': '',
  'ENC.fbs': '',
  'ENV.fbs': '',
  'EOO.fbs': '',
  'EOP.fbs': '',
  'EPM.fbs': '',
  'ESL.fbs': '',
  'ETM.fbs': '',
  'EWR.fbs': '',
  'FCS.fbs': '',
  'FPC.fbs': '',
  'GDI.fbs': '',
  'GEO.fbs': '',
  'GJN.fbs': '',
  'GNO.fbs': '',
  'GPX.fbs': '',
  'GRV.fbs': '',
  'GVH.fbs': '',
  'HEL.fbs': '',
  'HFC.fbs': '',
  'HYP.fbs': '',
  'IDM.fbs': '',
  'ION.fbs': '',
  'IRO.fbs': '',
  'KMF.fbs': '',
  'KML.fbs': '',
  'KRF.fbs': '',
  'LAM.fbs': '',
  'LCC.fbs': '',
  'LCF.fbs': '',
  'LCH.fbs': '',
  'LDM.fbs': '',
  'LGR.fbs': '',
  'LKS.fbs': '',
  'LMO.fbs': '',
  'LMR.fbs': '',
  'LMS.fbs': '',
  'LND.fbs': '',
  'LNE.fbs': '',
  'LPF.fbs': '',
  'LWK.fbs': '',
  'MBL.fbs': '',
  'MET.fbs': '',
  'MFE.fbs': '',
  'MNF.fbs': '',
  'MNV.fbs': '',
  'MPE.fbs': '',
  'MSL.fbs': '',
  'MST.fbs': '',
  'MTI.fbs': '',
  'NAV.fbs': '',
  'OBD.fbs': '',
  'OBT.fbs': '',
  'OCM.fbs': '',
  'OEM.fbs': '',
  'OMM.fbs': '',
  'OOA.fbs': '',
  'OOB.fbs': '',
  'OOD.fbs': '',
  'OOE.fbs': '',
  'OOI.fbs': '',
  'OOL.fbs': '',
  'OON.fbs': '',
  'OOS.fbs': '',
  'OOT.fbs': '',
  'OPM.fbs': '',
  'OSM.fbs': '',
  'PCF.fbs': '',
  'PGR.fbs': '',
  'PHY.fbs': '',
  'PIV.fbs': '',
  'PLD.fbs': '',
  'PLG.fbs': '',
  'PLHD.fbs': '',
  'PLK.fbs': '',
  'PLOG.fbs': '',
  'PNM.fbs': '',
  'PPE.fbs': '',
  'PRG.fbs': '',
  'PRW.fbs': '',
  'PUR.fbs': '',
  'RAF.fbs': '',
  'RCF.fbs': '',
  'RDM.fbs': '',
  'RDO.fbs': '',
  'REC.fbs': '',
  'REM.fbs': '',
  'REV.fbs': '',
  'RFB.fbs': '',
  'RFE.fbs': '',
  'RFM.fbs': '',
  'RFO.fbs': '',
  'RHD.fbs': '',
  'ROC.fbs': '',
  'SAR.fbs': '',
  'SCM.fbs': '',
  'SDF.fbs': '',
  'SDL.fbs': '',
  'SDR.fbs': '',
  'SEN.fbs': '',
  'SEO.fbs': '',
  'SEV.fbs': '',
  'SHW.fbs': '',
  'SIT.fbs': '',
  'SKI.fbs': '',
  'SNR.fbs': '',
  'SNW.fbs': '',
  'SOI.fbs': '',
  'SON.fbs': '',
  'SPP.fbs': '',
  'SPW.fbs': '',
  'SRI.fbs': '',
  'STF.fbs': '',
  'STR.fbs': '',
  'STV.fbs': '',
  'SWR.fbs': '',
  'TAB.fbs': '',
  'TCF.fbs': '',
  'TDM.fbs': '',
  'TIM.fbs': '',
  'TKG.fbs': '',
  'TME.fbs': '',
  'TMF.fbs': '',
  'TPN.fbs': '',
  'TRK.fbs': '',
  'TRN.fbs': '',
  'VCM.fbs': '',
  'WPN.fbs': '',
  'WTH.fbs': '',
  'XTC.fbs': '',
};

/**
 * Get public topic name for a schema or three-letter record code.
 */
export function getTopicName(schema: string): string {
  return `/spacedatanetwork/sds/${standardCodeFromSchema(schema)}`;
}

/**
 * Get schema name from topic
 */
export function getSchemaFromTopic(topic: string): SchemaName | null {
  const prefix = '/spacedatanetwork/sds/';
  if (!topic.startsWith(prefix)) {
    return null;
  }
  const value = topic.slice(prefix.length);
  const schema = (value.endsWith('.fbs') ? value : `${value}.fbs`) as SchemaName;
  return SUPPORTED_SCHEMAS.includes(schema) ? schema : null;
}

/**
 * Validate that a string is a valid schema name
 */
export function isValidSchema(name: string): name is SchemaName {
  return SUPPORTED_SCHEMAS.includes(name as SchemaName);
}

function standardCodeFromSchema(value: string): string {
  return value.endsWith('.fbs') ? value.slice(0, -4) : value;
}
