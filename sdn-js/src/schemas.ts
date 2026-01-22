/**
 * SDS Schema definitions and constants
 */

export const SUPPORTED_SCHEMAS = [
  'EPM.fbs',   // Entity Profile Manifest
  'PNM.fbs',   // Peer Network Manifest
  'OMM.fbs',   // Orbit Mean-Elements Message
  'OEM.fbs',   // Orbit Ephemeris Message
  'CDM.fbs',   // Conjunction Data Message
  'CAT.fbs',   // Catalog
  'CSM.fbs',   // Conjunction Summary Message
  'LDM.fbs',   // Launch Data Message
  'IDM.fbs',   // Initial Data Message
  'PLD.fbs',   // Payload
  'BOV.fbs',   // Body Orientation and Velocity
  'EOO.fbs',   // Earth Orientation
  'RFM.fbs',   // Reference Frame Message
  'TDM.fbs',   // Tracking Data Message
  'AEM.fbs',   // Attitude Ephemeris Message
  'APM.fbs',   // Attitude Parameter Message
  'OPM.fbs',   // Orbit Parameter Message
  'MPE.fbs',   // Maneuver Planning Ephemeris
  'OCM.fbs',   // Orbit Comprehensive Message
  'RDM.fbs',   // Re-entry Data Message
  'SIT.fbs',   // Satellite Impact Table
] as const;

export type SchemaName = typeof SUPPORTED_SCHEMAS[number];

/**
 * Schema descriptions
 */
export const SCHEMA_DESCRIPTIONS: Record<SchemaName, string> = {
  'EPM.fbs': 'Entity Profile Manifest - Organization identity and contact information',
  'PNM.fbs': 'Peer Network Manifest - Peer identity and network capabilities',
  'OMM.fbs': 'Orbit Mean-Elements Message - Satellite orbital parameters',
  'OEM.fbs': 'Orbit Ephemeris Message - Time-series position/velocity data',
  'CDM.fbs': 'Conjunction Data Message - Close approach warnings',
  'CAT.fbs': 'Catalog - Space object catalog entries',
  'CSM.fbs': 'Conjunction Summary Message - Brief conjunction events',
  'LDM.fbs': 'Launch Data Message - Launch event information',
  'IDM.fbs': 'Initial Data Message - Initial orbit determination',
  'PLD.fbs': 'Payload - Spacecraft payload information',
  'BOV.fbs': 'Body Orientation and Velocity - Attitude data',
  'EOO.fbs': 'Earth Orientation - Earth orientation parameters',
  'RFM.fbs': 'Reference Frame Message - Coordinate frame definitions',
  'TDM.fbs': 'Tracking Data Message - Radar/optical observations',
  'AEM.fbs': 'Attitude Ephemeris Message - Time-series attitude data',
  'APM.fbs': 'Attitude Parameter Message - Attitude state',
  'OPM.fbs': 'Orbit Parameter Message - Orbit state',
  'MPE.fbs': 'Maneuver Planning Ephemeris - Planned maneuvers',
  'OCM.fbs': 'Orbit Comprehensive Message - Full orbit data',
  'RDM.fbs': 'Re-entry Data Message - Reentry predictions',
  'SIT.fbs': 'Satellite Impact Table - Impact assessments',
};

/**
 * Bundled schema content (populated at build time)
 */
export const SDS_SCHEMAS: Record<SchemaName, string> = {
  'EPM.fbs': '',
  'PNM.fbs': '',
  'OMM.fbs': '',
  'OEM.fbs': '',
  'CDM.fbs': '',
  'CAT.fbs': '',
  'CSM.fbs': '',
  'LDM.fbs': '',
  'IDM.fbs': '',
  'PLD.fbs': '',
  'BOV.fbs': '',
  'EOO.fbs': '',
  'RFM.fbs': '',
  'TDM.fbs': '',
  'AEM.fbs': '',
  'APM.fbs': '',
  'OPM.fbs': '',
  'MPE.fbs': '',
  'OCM.fbs': '',
  'RDM.fbs': '',
  'SIT.fbs': '',
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
