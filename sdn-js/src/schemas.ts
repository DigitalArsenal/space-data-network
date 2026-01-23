/**
 * SDS Schema definitions and constants
 */

export const SUPPORTED_SCHEMAS = [
  'ATM.fbs',   // Attitude Message
  'BOV.fbs',   // Body Orientation and Velocity
  'CAT.fbs',   // Catalog
  'CDM.fbs',   // Conjunction Data Message
  'CRM.fbs',   // Collision Risk Message
  'CSM.fbs',   // Conjunction Summary Message
  'CTR.fbs',   // Contact Report
  'EME.fbs',   // Electromagnetic Emissions
  'EOO.fbs',   // Earth Orientation
  'EOP.fbs',   // Earth Orientation Parameters
  'EPM.fbs',   // Entity Profile Manifest
  'HYP.fbs',   // Hyperbolic Orbit
  'IDM.fbs',   // Initial Data Message
  'LCC.fbs',   // Launch Collision Corridor
  'LDM.fbs',   // Launch Data Message
  'MET.fbs',   // Meteorological Data
  'MPE.fbs',   // Maneuver Planning Ephemeris
  'OCM.fbs',   // Orbit Comprehensive Message
  'OEM.fbs',   // Orbit Ephemeris Message
  'OMM.fbs',   // Orbit Mean-Elements Message
  'OSM.fbs',   // Orbit State Message
  'PLD.fbs',   // Payload
  'PNM.fbs',   // Peer Network Manifest
  'PRG.fbs',   // Propagation Settings
  'REC.fbs',   // Records
  'RFM.fbs',   // Reference Frame Message
  'ROC.fbs',   // Re-entry Operations Corridor
  'SCM.fbs',   // Spacecraft Message
  'SIT.fbs',   // Satellite Impact Table
  'TDM.fbs',   // Tracking Data Message
  'TIM.fbs',   // Time Message
  'VCM.fbs',   // Vector Covariance Message
] as const;

export type SchemaName = typeof SUPPORTED_SCHEMAS[number];

/**
 * Schema descriptions
 */
export const SCHEMA_DESCRIPTIONS: Record<SchemaName, string> = {
  'ATM.fbs': 'Attitude Message - Spacecraft attitude information',
  'BOV.fbs': 'Body Orientation and Velocity - Attitude and angular velocity',
  'CAT.fbs': 'Catalog - Space object catalog entries',
  'CDM.fbs': 'Conjunction Data Message - Close approach warnings',
  'CRM.fbs': 'Collision Risk Message - Collision probability assessments',
  'CSM.fbs': 'Conjunction Summary Message - Brief conjunction events',
  'CTR.fbs': 'Contact Report - Communication contact reports',
  'EME.fbs': 'Electromagnetic Emissions - RF and electromagnetic data',
  'EOO.fbs': 'Earth Orientation - Earth orientation parameters',
  'EOP.fbs': 'Earth Orientation Parameters - Polar motion and UT1-UTC',
  'EPM.fbs': 'Entity Profile Manifest - Organization identity and contact information',
  'HYP.fbs': 'Hyperbolic Orbit - Hyperbolic trajectory parameters',
  'IDM.fbs': 'Initial Data Message - Initial orbit determination',
  'LCC.fbs': 'Launch Collision Corridor - Launch trajectory corridors',
  'LDM.fbs': 'Launch Data Message - Launch event information',
  'MET.fbs': 'Meteorological Data - Atmospheric and weather data',
  'MPE.fbs': 'Maneuver Planning Ephemeris - Planned maneuvers',
  'OCM.fbs': 'Orbit Comprehensive Message - Full orbit data',
  'OEM.fbs': 'Orbit Ephemeris Message - Time-series position/velocity data',
  'OMM.fbs': 'Orbit Mean-Elements Message - Satellite orbital parameters',
  'OSM.fbs': 'Orbit State Message - Orbit state vectors',
  'PLD.fbs': 'Payload - Spacecraft payload information',
  'PNM.fbs': 'Peer Network Manifest - Peer identity and network capabilities',
  'PRG.fbs': 'Propagation Settings - Orbit propagation parameters',
  'REC.fbs': 'Records - Data records and observations',
  'RFM.fbs': 'Reference Frame Message - Coordinate frame definitions',
  'ROC.fbs': 'Re-entry Operations Corridor - Re-entry trajectory corridors',
  'SCM.fbs': 'Spacecraft Message - Spacecraft characteristics',
  'SIT.fbs': 'Satellite Impact Table - Impact assessments',
  'TDM.fbs': 'Tracking Data Message - Radar/optical observations',
  'TIM.fbs': 'Time Message - Time synchronization data',
  'VCM.fbs': 'Vector Covariance Message - State vector with covariance',
};

/**
 * Bundled schema content (populated at build time)
 */
export const SDS_SCHEMAS: Record<SchemaName, string> = {
  'ATM.fbs': '',
  'BOV.fbs': '',
  'CAT.fbs': '',
  'CDM.fbs': '',
  'CRM.fbs': '',
  'CSM.fbs': '',
  'CTR.fbs': '',
  'EME.fbs': '',
  'EOO.fbs': '',
  'EOP.fbs': '',
  'EPM.fbs': '',
  'HYP.fbs': '',
  'IDM.fbs': '',
  'LCC.fbs': '',
  'LDM.fbs': '',
  'MET.fbs': '',
  'MPE.fbs': '',
  'OCM.fbs': '',
  'OEM.fbs': '',
  'OMM.fbs': '',
  'OSM.fbs': '',
  'PLD.fbs': '',
  'PNM.fbs': '',
  'PRG.fbs': '',
  'REC.fbs': '',
  'RFM.fbs': '',
  'ROC.fbs': '',
  'SCM.fbs': '',
  'SIT.fbs': '',
  'TDM.fbs': '',
  'TIM.fbs': '',
  'VCM.fbs': '',
};

/**
 * Get topic name for a schema
 */
export function getTopicName(schema: SchemaName): string {
  return `/spacedatanetwork/sds/${schema}`;
}

/**
 * Get schema name from topic
 */
export function getSchemaFromTopic(topic: string): SchemaName | null {
  const prefix = '/spacedatanetwork/sds/';
  if (!topic.startsWith(prefix)) {
    return null;
  }
  const schema = topic.slice(prefix.length) as SchemaName;
  return SUPPORTED_SCHEMAS.includes(schema) ? schema : null;
}

/**
 * Validate that a string is a valid schema name
 */
export function isValidSchema(name: string): name is SchemaName {
  return SUPPORTED_SCHEMAS.includes(name as SchemaName);
}
