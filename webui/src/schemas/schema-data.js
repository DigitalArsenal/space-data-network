/**
 * Space Data Standards - Schema Registry Data
 * Copied from spacedatastandards-site/schemas.js and app.js for use in WebUI
 */

export const SCHEMA_CATEGORIES = {
  orbital: { label: 'Orbital Data', color: 'orbital' },
  conjunction: { label: 'Conjunction', color: 'conjunction' },
  entity: { label: 'Entity', color: 'entity' },
  telemetry: { label: 'Telemetry', color: 'telemetry' },
  marketplace: { label: 'Marketplace', color: 'marketplace' },
  infrastructure: { label: 'Infrastructure', color: 'infrastructure' },
  routing: { label: 'Routing', color: 'routing' }
}

export const SCHEMAS = [
  {
    name: 'ACL',
    fullName: 'Access Control Grant',
    description: 'Permission to access purchased data. Contains grant ID, buyer peer ID, access type, tier information, payment proof, and provider signature.',
    category: 'marketplace',
    version: '1.0.1',
    includes: ['STF'],
    fileIdentifier: '$ACL',
    fields: [
      { name: 'GRANT_ID', type: 'string', required: true, fbType: 'string', fbId: 1, desc: 'Unique identifier for this grant' },
      { name: 'LISTING_ID', type: 'string', required: true, fbType: 'string', fbId: 2, desc: 'ID of the listing this grant applies to' },
      { name: 'BUYER_PEER_ID', type: 'string', required: true, fbType: 'string', fbId: 3, desc: 'Peer ID of the buyer/grantee' },
      { name: 'BUYER_ENCRYPTION_PUBKEY', type: 'array', required: false, fbType: '[ubyte]', fbId: 4, desc: "Buyer's encryption public key for encrypted delivery" },
      { name: 'ACCESS_TYPE', type: 'string', required: false, fbType: 'accessType', fbId: 5, desc: 'Type of access granted', enum: 'accessType' },
      { name: 'TIER_NAME', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Name of the pricing tier purchased' },
      { name: 'GRANTED_AT', type: 'integer', required: false, fbType: 'uint64', fbId: 7, desc: 'Unix timestamp when access was granted' },
      { name: 'EXPIRES_AT', type: 'integer', required: false, fbType: 'uint64', fbId: 8, desc: 'Unix timestamp when access expires (0 = never)' },
      { name: 'PAYMENT_TX_HASH', type: 'string', required: false, fbType: 'string', fbId: 9, desc: 'Transaction hash or reference proving payment' },
      { name: 'PAYMENT_METHOD', type: 'string', required: false, fbType: 'paymentMethod', fbId: 10, desc: 'Payment method used', enum: 'paymentMethod' },
      { name: 'PROVIDER_SIGNATURE', type: 'array', required: false, fbType: '[ubyte]', fbId: 11, desc: 'Ed25519 signature from provider' }
    ]
  },
  {
    name: 'ATM',
    fullName: 'Atmosphere Model',
    description: 'Atmosphere model definitions from the SANA registry. Used to specify atmospheric drag models for orbit determination.',
    category: 'infrastructure',
    version: '1.0.2',
    includes: [],
    fields: [
      { name: 'ATM', type: 'string', required: false, fbType: 'atmosphereModel (enum)', fbId: 1, desc: 'Atmosphere model type from SANA registry' }
    ]
  },
  {
    name: 'BOV',
    fullName: 'Burn Out Vector',
    description: 'Burn Out Vector Message containing the state vector at the end of a rocket burn. Used for launch trajectory analysis.',
    category: 'orbital',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$BOV',
    fields: [
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Object name' },
      { name: 'BURN_OUT_EPOCH', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Epoch at burnout (ISO 8601)' },
      { name: 'X', type: 'number', required: false, fbType: 'double', fbId: 3, desc: 'X position in km' },
      { name: 'Y', type: 'number', required: false, fbType: 'double', fbId: 4, desc: 'Y position in km' },
      { name: 'Z', type: 'number', required: false, fbType: 'double', fbId: 5, desc: 'Z position in km' },
      { name: 'X_DOT', type: 'number', required: false, fbType: 'double', fbId: 6, desc: 'X velocity in km/s' },
      { name: 'Y_DOT', type: 'number', required: false, fbType: 'double', fbId: 7, desc: 'Y velocity in km/s' },
      { name: 'Z_DOT', type: 'number', required: false, fbType: 'double', fbId: 8, desc: 'Z velocity in km/s' }
    ]
  },
  {
    name: 'CAT',
    fullName: 'Catalog Entry',
    description: 'Catalog entry for a space object. Contains object identification, payload information, and launch country code.',
    category: 'entity',
    version: '1.0.1',
    includes: ['PLD', 'LCC'],
    fileIdentifier: '$CAT',
    fields: [
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Object name' },
      { name: 'OBJECT_ID', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'International designator (YYYY-NNNAAA)' },
      { name: 'NORAD_CAT_ID', type: 'integer', required: false, fbType: 'uint32', fbId: 3, desc: 'NORAD catalog number' },
      { name: 'OBJECT_TYPE', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Type: PAYLOAD, ROCKET BODY, DEBRIS, etc.' },
      { name: 'OPS_STATUS_CODE', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Operational status code' },
      { name: 'OWNER', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Owner/operator' },
      { name: 'LAUNCH_DATE', type: 'string', required: false, fbType: 'string', fbId: 7, desc: 'Launch date (ISO 8601)' },
      { name: 'LAUNCH_SITE', type: 'string', required: false, fbType: 'string', fbId: 8, desc: 'Launch site' },
      { name: 'DECAY_DATE', type: 'string', required: false, fbType: 'string', fbId: 9, desc: 'Decay date if applicable (ISO 8601)' },
      { name: 'PERIOD', type: 'number', required: false, fbType: 'double', fbId: 10, desc: 'Orbital period in minutes' },
      { name: 'INCLINATION', type: 'number', required: false, fbType: 'double', fbId: 11, desc: 'Inclination in degrees' },
      { name: 'APOGEE', type: 'number', required: false, fbType: 'double', fbId: 12, desc: 'Apogee altitude in km' },
      { name: 'PERIGEE', type: 'number', required: false, fbType: 'double', fbId: 13, desc: 'Perigee altitude in km' },
      { name: 'RCS', type: 'number', required: false, fbType: 'double', fbId: 14, desc: 'Radar cross section in m^2' },
      { name: 'PAYLOAD', type: 'object', required: false, fbType: 'PLD', fbId: 15, desc: 'Payload information' },
      { name: 'COUNTRY_CODE', type: 'string', required: false, fbType: 'LCC', fbId: 16, desc: 'Launch country code' }
    ]
  },
  {
    name: 'CDM',
    fullName: 'Conjunction Data Message',
    description: 'Conjunction Data Message per CCSDS 508.0-B-1. Contains conjunction assessment data including time of closest approach, miss distance, collision probability, and object state vectors with covariance.',
    category: 'conjunction',
    version: '1.0.2',
    includes: ['PNM', 'CAT', 'EPM', 'RFM'],
    fileIdentifier: '$CDM',
    fields: [
      { name: 'CCSDS_CDM_VERS', type: 'number', required: false, fbType: 'double', fbId: 1, desc: 'CCSDS CDM Version' },
      { name: 'CREATION_DATE', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Creation date (ISO 8601)' },
      { name: 'ORIGINATOR', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Originator' },
      { name: 'MESSAGE_FOR', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Message intended for' },
      { name: 'MESSAGE_ID', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Message ID' },
      { name: 'TCA', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Time of closest approach (ISO 8601)' },
      { name: 'MISS_DISTANCE', type: 'number', required: false, fbType: 'double', fbId: 7, desc: 'Miss distance in m' },
      { name: 'RELATIVE_SPEED', type: 'number', required: false, fbType: 'double', fbId: 8, desc: 'Relative speed in m/s' },
      { name: 'RELATIVE_POSITION_R', type: 'number', required: false, fbType: 'double', fbId: 9, desc: 'Relative position R in m' },
      { name: 'RELATIVE_POSITION_T', type: 'number', required: false, fbType: 'double', fbId: 10, desc: 'Relative position T in m' },
      { name: 'RELATIVE_POSITION_N', type: 'number', required: false, fbType: 'double', fbId: 11, desc: 'Relative position N in m' },
      { name: 'COLLISION_PROBABILITY', type: 'number', required: false, fbType: 'double', fbId: 12, desc: 'Collision probability' },
      { name: 'COLLISION_PROBABILITY_METHOD', type: 'string', required: false, fbType: 'string', fbId: 13, desc: 'Collision probability method' },
      { name: 'OBJECT1', type: 'object', required: false, fbType: 'CDMObject', fbId: 14, desc: 'Object 1 data' },
      { name: 'OBJECT2', type: 'object', required: false, fbType: 'CDMObject', fbId: 15, desc: 'Object 2 data' }
    ]
  },
  {
    name: 'CRM',
    fullName: 'Collection Request Message',
    description: 'Collection Request Message for tasking sensor collections on space objects.',
    category: 'infrastructure',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$CRM',
    fields: [
      { name: 'REQUEST_ID', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Request identifier' },
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Target object name' },
      { name: 'NORAD_CAT_ID', type: 'integer', required: false, fbType: 'uint32', fbId: 3, desc: 'NORAD catalog ID' },
      { name: 'PRIORITY', type: 'integer', required: false, fbType: 'uint8', fbId: 4, desc: 'Collection priority' },
      { name: 'START_TIME', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Collection window start (ISO 8601)' },
      { name: 'END_TIME', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Collection window end (ISO 8601)' }
    ]
  },
  {
    name: 'CSM',
    fullName: 'Conjunction Summary Message',
    description: 'Conjunction Summary Message providing a high-level overview of conjunction events for a catalog object.',
    category: 'conjunction',
    version: '1.0.1',
    includes: ['CAT'],
    fileIdentifier: '$CSM',
    fields: [
      { name: 'OBJECT', type: 'object', required: false, fbType: 'CAT', fbId: 1, desc: 'Catalog entry for primary object' },
      { name: 'CONJUNCTION_COUNT', type: 'integer', required: false, fbType: 'uint32', fbId: 2, desc: 'Number of conjunctions' },
      { name: 'MAX_PROBABILITY', type: 'number', required: false, fbType: 'double', fbId: 3, desc: 'Maximum collision probability' },
      { name: 'MIN_MISS_DISTANCE', type: 'number', required: false, fbType: 'double', fbId: 4, desc: 'Minimum miss distance in m' },
      { name: 'SUMMARY_EPOCH', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Summary epoch (ISO 8601)' }
    ]
  },
  {
    name: 'CTR',
    fullName: 'Country Identity Message',
    description: 'Country Identity Message containing country code and name information for space object registration.',
    category: 'entity',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$CTR',
    fields: [
      { name: 'COUNTRY_CODE', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'ISO 3166 country code' },
      { name: 'COUNTRY_NAME', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Country name' }
    ]
  },
  {
    name: 'EME',
    fullName: 'Encrypted Message Envelope',
    description: 'Encrypted Message Envelope for wrapping encrypted payloads with ECIES encryption metadata, key exchange information, and authentication tags.',
    category: 'infrastructure',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$EME',
    fields: [
      { name: 'EPHEMERAL_PUBKEY', type: 'array', required: false, fbType: '[ubyte]', fbId: 1, desc: 'Ephemeral public key for key exchange' },
      { name: 'CIPHERTEXT', type: 'array', required: false, fbType: '[ubyte]', fbId: 2, desc: 'Encrypted payload' },
      { name: 'NONCE', type: 'array', required: false, fbType: '[ubyte]', fbId: 3, desc: 'Encryption nonce/IV' },
      { name: 'AUTH_TAG', type: 'array', required: false, fbType: '[ubyte]', fbId: 4, desc: 'Authentication tag' },
      { name: 'ALGORITHM', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Encryption algorithm identifier' },
      { name: 'SCHEMA_TYPE', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Original schema type of encrypted payload' }
    ]
  },
  {
    name: 'EOO',
    fullName: 'Electro-Optical Observation',
    description: 'Electro-Optical Observation message containing observation data from optical sensors including angles, magnitude, and sensor information.',
    category: 'telemetry',
    version: '1.0.9',
    includes: ['RFM', 'IDM'],
    fileIdentifier: '$EOO',
    fields: [
      { name: 'NORAD_CAT_ID', type: 'integer', required: false, fbType: 'uint32', fbId: 1, desc: 'NORAD catalog ID' },
      { name: 'OBSERVATION_EPOCH', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Observation epoch (ISO 8601)' },
      { name: 'RIGHT_ASCENSION', type: 'number', required: false, fbType: 'double', fbId: 3, desc: 'Right ascension in degrees' },
      { name: 'DECLINATION', type: 'number', required: false, fbType: 'double', fbId: 4, desc: 'Declination in degrees' },
      { name: 'VISUAL_MAGNITUDE', type: 'number', required: false, fbType: 'double', fbId: 5, desc: 'Visual magnitude' },
      { name: 'SENSOR_ID', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Sensor identifier' },
      { name: 'REFERENCE_FRAME', type: 'string', required: false, fbType: 'RFM', fbId: 7, desc: 'Reference frame' }
    ]
  },
  {
    name: 'EOP',
    fullName: 'Earth Orientation Parameters',
    description: 'Earth Orientation Parameters including polar motion, UT1-UTC, and precession/nutation corrections from IERS.',
    category: 'telemetry',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$EOP',
    fields: [
      { name: 'DATA_TYPE', type: 'string', required: false, fbType: 'DataType (enum)', fbId: 1, desc: 'Observed or predicted' },
      { name: 'EPOCH', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Epoch (ISO 8601)' },
      { name: 'X_POLE', type: 'number', required: false, fbType: 'double', fbId: 3, desc: 'Polar motion X in arcseconds' },
      { name: 'Y_POLE', type: 'number', required: false, fbType: 'double', fbId: 4, desc: 'Polar motion Y in arcseconds' },
      { name: 'UT1_UTC', type: 'number', required: false, fbType: 'double', fbId: 5, desc: 'UT1-UTC in seconds' },
      { name: 'LOD', type: 'number', required: false, fbType: 'double', fbId: 6, desc: 'Length of day in seconds' },
      { name: 'DPSI', type: 'number', required: false, fbType: 'double', fbId: 7, desc: 'Nutation correction dPsi in arcseconds' },
      { name: 'DEPS', type: 'number', required: false, fbType: 'double', fbId: 8, desc: 'Nutation correction dEps in arcseconds' }
    ]
  },
  {
    name: 'EPM',
    fullName: 'Entity Profile Message',
    description: 'Entity Profile Message containing identity, cryptographic keys, blockchain addresses, and contact information. Used for peer identification in the Space Data Network.',
    category: 'entity',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$EPM',
    fields: [
      { name: 'ENTITY_ID', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Unique entity identifier' },
      { name: 'NAME', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Entity name' },
      { name: 'ORGANIZATION', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Organization name' },
      { name: 'KEYS', type: 'array', required: false, fbType: '[KeyInfo]', fbId: 4, desc: 'Cryptographic keys' },
      { name: 'ADDRESSES', type: 'array', required: false, fbType: '[BlockchainAddress]', fbId: 5, desc: 'Blockchain addresses' },
      { name: 'CONTACT', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Contact information' },
      { name: 'PEER_ID', type: 'string', required: false, fbType: 'string', fbId: 7, desc: 'libp2p Peer ID' },
      { name: 'CREATED_AT', type: 'integer', required: false, fbType: 'uint64', fbId: 8, desc: 'Creation timestamp' },
      { name: 'SIGNATURE', type: 'array', required: false, fbType: '[ubyte]', fbId: 9, desc: 'Ed25519 signature' }
    ]
  },
  {
    name: 'HYP',
    fullName: 'Hypothesis Message',
    description: 'Hypothesis Message for anomaly detection and outlier scoring of space object observations.',
    category: 'telemetry',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$HYP',
    fields: [
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Object name' },
      { name: 'NORAD_CAT_ID', type: 'integer', required: false, fbType: 'uint32', fbId: 2, desc: 'NORAD catalog ID' },
      { name: 'SCORE_TYPE', type: 'string', required: false, fbType: 'ScoreType (enum)', fbId: 3, desc: 'Type of score: OUTLIER' },
      { name: 'SCORE', type: 'number', required: false, fbType: 'double', fbId: 4, desc: 'Hypothesis score' },
      { name: 'EPOCH', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Epoch (ISO 8601)' },
      { name: 'DESCRIPTION', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Hypothesis description' }
    ]
  },
  {
    name: 'IDM',
    fullName: 'Instrument Description Message',
    description: 'Instrument Description Message defining sensor and instrument characteristics including frequency, polarization, and capabilities.',
    category: 'entity',
    version: '1.0.3',
    includes: [],
    fileIdentifier: '$IDM',
    fields: [
      { name: 'INSTRUMENT_ID', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Instrument identifier' },
      { name: 'INSTRUMENT_NAME', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Instrument name' },
      { name: 'INSTRUMENT_TYPE', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Instrument type' },
      { name: 'POLARIZATION', type: 'string', required: false, fbType: 'PolarizationType (enum)', fbId: 4, desc: 'Polarization type' },
      { name: 'FREQUENCY_RANGE', type: 'object', required: false, fbType: 'FrequencyRange', fbId: 5, desc: 'Frequency range with lower/upper limits' },
      { name: 'APERTURE', type: 'number', required: false, fbType: 'double', fbId: 6, desc: 'Aperture diameter in m' },
      { name: 'FOV', type: 'number', required: false, fbType: 'double', fbId: 7, desc: 'Field of view in degrees' }
    ]
  },
  {
    name: 'LCC',
    fullName: 'Legacy Country Code',
    description: 'Legacy country code enumeration for space object registration, based on historical launch registry designations.',
    category: 'infrastructure',
    version: '1.0.1',
    includes: [],
    fields: [
      { name: 'LCC', type: 'string', required: false, fbType: 'legacyCountryCode (enum)', fbId: 1, desc: 'Legacy country code' }
    ]
  },
  {
    name: 'LDM',
    fullName: 'Launch Data Message',
    description: 'Launch Data Message containing launch vehicle, site, trajectory, and window information.',
    category: 'orbital',
    version: '1.0.1',
    includes: ['SIT', 'EPM'],
    fileIdentifier: '$LDM',
    fields: [
      { name: 'LAUNCH_ID', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Launch identifier' },
      { name: 'LAUNCH_VEHICLE', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Launch vehicle name' },
      { name: 'LAUNCH_SITE', type: 'object', required: false, fbType: 'SIT', fbId: 3, desc: 'Launch site information' },
      { name: 'LAUNCH_DATE', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Launch date (ISO 8601)' },
      { name: 'WINDOW_OPEN', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Window open time (ISO 8601)' },
      { name: 'WINDOW_CLOSE', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Window close time (ISO 8601)' },
      { name: 'PROVIDER', type: 'object', required: false, fbType: 'EPM', fbId: 7, desc: 'Launch provider entity' }
    ]
  },
  {
    name: 'MET',
    fullName: 'Mean Element Theory',
    description: 'Mean Element Theory enumeration defining orbital propagation models (SGP4, SGP8, DSST, USM, etc.).',
    category: 'infrastructure',
    version: '1.0.1',
    includes: [],
    fields: [
      { name: 'MET', type: 'string', required: false, fbType: 'meanElementTheory (enum)', fbId: 1, desc: 'Mean element theory type' }
    ]
  },
  {
    name: 'MPE',
    fullName: 'Minimum Propagatable Element Set',
    description: 'Truncated version of the OMM containing only the minimum elements required to propagate an orbit. CCSDS 502x0b2c1e2.',
    category: 'orbital',
    version: '1.0.3',
    includes: ['MET'],
    fileIdentifier: '$MPE',
    fields: [
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Object name' },
      { name: 'OBJECT_ID', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'International designator' },
      { name: 'EPOCH', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Epoch (ISO 8601)' },
      { name: 'MEAN_MOTION', type: 'number', required: false, fbType: 'double', fbId: 4, desc: 'Mean motion in rev/day' },
      { name: 'ECCENTRICITY', type: 'number', required: false, fbType: 'double', fbId: 5, desc: 'Eccentricity' },
      { name: 'INCLINATION', type: 'number', required: false, fbType: 'double', fbId: 6, desc: 'Inclination in degrees' },
      { name: 'RA_OF_ASC_NODE', type: 'number', required: false, fbType: 'double', fbId: 7, desc: 'RA of ascending node in degrees' },
      { name: 'ARG_OF_PERICENTER', type: 'number', required: false, fbType: 'double', fbId: 8, desc: 'Argument of pericenter in degrees' },
      { name: 'MEAN_ANOMALY', type: 'number', required: false, fbType: 'double', fbId: 9, desc: 'Mean anomaly in degrees' },
      { name: 'NORAD_CAT_ID', type: 'integer', required: false, fbType: 'uint32', fbId: 10, desc: 'NORAD catalog ID' },
      { name: 'BSTAR', type: 'number', required: false, fbType: 'double', fbId: 11, desc: 'B* drag term' },
      { name: 'MEAN_ELEMENT_THEORY', type: 'string', required: false, fbType: 'meanElementTheory', fbId: 12, desc: 'Mean element theory' }
    ]
  },
  {
    name: 'OCM',
    fullName: 'Orbit Comprehensive Message',
    description: 'Orbit Comprehensive Message per CCSDS 502.0-B-3 providing comprehensive orbit data including state vectors, covariance, maneuvers, and perturbation models.',
    category: 'orbital',
    version: '1.0.4',
    includes: ['ATM'],
    fileIdentifier: '$OCM',
    fields: [
      { name: 'CCSDS_OCM_VERS', type: 'number', required: false, fbType: 'double', fbId: 1, desc: 'CCSDS OCM version' },
      { name: 'CREATION_DATE', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Creation date (ISO 8601)' },
      { name: 'ORIGINATOR', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Originator' },
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Object name' },
      { name: 'OBJECT_ID', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'International designator' },
      { name: 'CENTER_NAME', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Center name' },
      { name: 'EPOCH', type: 'string', required: false, fbType: 'string', fbId: 7, desc: 'Epoch (ISO 8601)' }
    ]
  },
  {
    name: 'OEM',
    fullName: 'Orbit Ephemeris Message',
    description: 'Orbit Ephemeris Message per CCSDS 502x0b2c1e2 containing ephemeris data as a time-tagged sequence of position and velocity vectors.',
    category: 'orbital',
    version: '1.0.3',
    includes: ['RFM', 'TIM'],
    fileIdentifier: '$OEM',
    fields: [
      { name: 'CCSDS_OEM_VERS', type: 'number', required: false, fbType: 'double', fbId: 1, desc: 'CCSDS OEM version' },
      { name: 'CREATION_DATE', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Creation date (ISO 8601)' },
      { name: 'ORIGINATOR', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Originator' },
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Object name' },
      { name: 'OBJECT_ID', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'International designator' },
      { name: 'CENTER_NAME', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Center name' },
      { name: 'REFERENCE_FRAME', type: 'string', required: false, fbType: 'RFM', fbId: 7, desc: 'Reference frame' },
      { name: 'TIME_SYSTEM', type: 'string', required: false, fbType: 'timeSystem', fbId: 8, desc: 'Time system' },
      { name: 'START_TIME', type: 'string', required: false, fbType: 'string', fbId: 9, desc: 'Start time (ISO 8601)' },
      { name: 'STOP_TIME', type: 'string', required: false, fbType: 'string', fbId: 10, desc: 'Stop time (ISO 8601)' },
      { name: 'EPHEMERIS_DATA', type: 'array', required: false, fbType: '[EphemerisLine]', fbId: 11, desc: 'Ephemeris data lines' }
    ]
  },
  {
    name: 'OMM',
    fullName: 'Orbit Mean-Elements Message',
    description: 'Orbit Mean-Elements Message per CCSDS 502x0b2c1e2. Contains mean Keplerian elements, TLE parameters, covariance matrix, and propagation metadata. The most widely used orbital data format.',
    category: 'orbital',
    version: '1.0.5',
    includes: ['RFM', 'TIM', 'MET'],
    fileIdentifier: '$OMM',
    fields: [
      { name: 'CCSDS_OMM_VERS', type: 'number', required: false, fbType: 'double', fbId: 1, desc: 'CCSDS OMM version' },
      { name: 'CREATION_DATE', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Creation date (ISO 8601)' },
      { name: 'ORIGINATOR', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Originator' },
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Satellite name(s)' },
      { name: 'OBJECT_ID', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'International designator (YYYY-NNNAAA)' },
      { name: 'CENTER_NAME', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Center name (e.g. EARTH)' },
      { name: 'REFERENCE_FRAME', type: 'string', required: false, fbType: 'RFM', fbId: 7, desc: 'Reference frame' },
      { name: 'REFERENCE_FRAME_EPOCH', type: 'string', required: false, fbType: 'string', fbId: 8, desc: 'Reference frame epoch' },
      { name: 'TIME_SYSTEM', type: 'string', required: false, fbType: 'timeSystem', fbId: 9, desc: 'Time system', default: 'UTC' },
      { name: 'MEAN_ELEMENT_THEORY', type: 'string', required: false, fbType: 'meanElementTheory', fbId: 10, desc: 'Mean element theory', default: 'SGP4' },
      { name: 'COMMENT', type: 'string', required: false, fbType: 'string', fbId: 11, desc: 'Comment' },
      { name: 'EPOCH', type: 'string', required: false, fbType: 'string', fbId: 12, desc: 'Epoch of mean Keplerian elements (ISO 8601)' },
      { name: 'SEMI_MAJOR_AXIS', type: 'number', required: false, fbType: 'double', fbId: 13, desc: 'Semi-major axis in km' },
      { name: 'MEAN_MOTION', type: 'number', required: false, fbType: 'double', fbId: 14, desc: 'Mean motion in rev/day' },
      { name: 'ECCENTRICITY', type: 'number', required: false, fbType: 'double', fbId: 15, desc: 'Eccentricity (unitless)' },
      { name: 'INCLINATION', type: 'number', required: false, fbType: 'double', fbId: 16, desc: 'Inclination in degrees' },
      { name: 'RA_OF_ASC_NODE', type: 'number', required: false, fbType: 'double', fbId: 17, desc: 'RA of ascending node in degrees' },
      { name: 'ARG_OF_PERICENTER', type: 'number', required: false, fbType: 'double', fbId: 18, desc: 'Argument of pericenter in degrees' },
      { name: 'MEAN_ANOMALY', type: 'number', required: false, fbType: 'double', fbId: 19, desc: 'Mean anomaly in degrees' },
      { name: 'GM', type: 'number', required: false, fbType: 'double', fbId: 20, desc: 'GM in km^3/s^2' },
      { name: 'MASS', type: 'number', required: false, fbType: 'double', fbId: 21, desc: 'Mass in kg' },
      { name: 'SOLAR_RAD_AREA', type: 'number', required: false, fbType: 'double', fbId: 22, desc: 'Solar radiation area in m^2' },
      { name: 'SOLAR_RAD_COEFF', type: 'number', required: false, fbType: 'double', fbId: 23, desc: 'Solar radiation coefficient' },
      { name: 'DRAG_AREA', type: 'number', required: false, fbType: 'double', fbId: 24, desc: 'Drag area in m^2' },
      { name: 'DRAG_COEFF', type: 'number', required: false, fbType: 'double', fbId: 25, desc: 'Drag coefficient' },
      { name: 'EPHEMERIS_TYPE', type: 'string', required: false, fbType: 'ephemerisType', fbId: 26, desc: 'Ephemeris type', default: 'SGP4' },
      { name: 'CLASSIFICATION_TYPE', type: 'string', required: false, fbType: 'string', fbId: 27, desc: 'Classification type (default U)' },
      { name: 'NORAD_CAT_ID', type: 'integer', required: false, fbType: 'uint32', fbId: 28, desc: 'NORAD catalog ID' },
      { name: 'ELEMENT_SET_NO', type: 'integer', required: false, fbType: 'uint32', fbId: 29, desc: 'Element set number' },
      { name: 'REV_AT_EPOCH', type: 'number', required: false, fbType: 'double', fbId: 30, desc: 'Revolution at epoch' },
      { name: 'BSTAR', type: 'number', required: false, fbType: 'double', fbId: 31, desc: 'B* drag term in 1/Earth radii' },
      { name: 'MEAN_MOTION_DOT', type: 'number', required: false, fbType: 'double', fbId: 32, desc: 'Mean motion dot in rev/day^2' },
      { name: 'MEAN_MOTION_DDOT', type: 'number', required: false, fbType: 'double', fbId: 33, desc: 'Mean motion double dot in rev/day^3' },
      { name: 'COV_REFERENCE_FRAME', type: 'string', required: false, fbType: 'RFM', fbId: 34, desc: 'Covariance reference frame' },
      { name: 'CX_X', type: 'number', required: false, fbType: 'double', fbId: 35, desc: 'CX_X covariance in km^2' },
      { name: 'CY_X', type: 'number', required: false, fbType: 'double', fbId: 36, desc: 'CY_X covariance in km^2' },
      { name: 'CY_Y', type: 'number', required: false, fbType: 'double', fbId: 37, desc: 'CY_Y covariance in km^2' },
      { name: 'CZ_X', type: 'number', required: false, fbType: 'double', fbId: 38, desc: 'CZ_X covariance in km^2' },
      { name: 'CZ_Y', type: 'number', required: false, fbType: 'double', fbId: 39, desc: 'CZ_Y covariance in km^2' },
      { name: 'CZ_Z', type: 'number', required: false, fbType: 'double', fbId: 40, desc: 'CZ_Z covariance in km^2' },
      { name: 'USER_DEFINED_BIP_0044_TYPE', type: 'integer', required: false, fbType: 'uint', fbId: 41, desc: 'User-defined BIP-0044 type' },
      { name: 'USER_DEFINED_OBJECT_DESIGNATOR', type: 'string', required: false, fbType: 'string', fbId: 42, desc: 'User-defined object designator' }
    ]
  },
  {
    name: 'OSM',
    fullName: 'Observation Stability Message',
    description: 'Observation Stability Message for tracking the stability and consistency of observations over time.',
    category: 'telemetry',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$OSM',
    fields: [
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Object name' },
      { name: 'NORAD_CAT_ID', type: 'integer', required: false, fbType: 'uint32', fbId: 2, desc: 'NORAD catalog ID' },
      { name: 'STABILITY_SCORE', type: 'number', required: false, fbType: 'double', fbId: 3, desc: 'Observation stability score' },
      { name: 'EPOCH', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Epoch (ISO 8601)' },
      { name: 'OBS_COUNT', type: 'integer', required: false, fbType: 'uint32', fbId: 5, desc: 'Number of observations' }
    ]
  },
  {
    name: 'PLD',
    fullName: 'Payload Information',
    description: 'Payload information for a space object including mass, dimensions, power, and mission description.',
    category: 'entity',
    version: '1.0.1',
    includes: ['IDM'],
    fileIdentifier: '$PLD',
    fields: [
      { name: 'PAYLOAD_NAME', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Payload name' },
      { name: 'MASS', type: 'number', required: false, fbType: 'double', fbId: 2, desc: 'Mass in kg' },
      { name: 'DIMENSIONS', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Dimensions (LxWxH in m)' },
      { name: 'POWER', type: 'number', required: false, fbType: 'double', fbId: 4, desc: 'Power in watts' },
      { name: 'MISSION', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Mission description' },
      { name: 'INSTRUMENTS', type: 'array', required: false, fbType: '[IDM]', fbId: 6, desc: 'Instruments on board' }
    ]
  },
  {
    name: 'PLG',
    fullName: 'Plugin Definition',
    description: 'Plugin type category and definition for extending the Space Data Network with custom functionality.',
    category: 'infrastructure',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$PLG',
    fields: [
      { name: 'PLUGIN_ID', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Plugin identifier' },
      { name: 'PLUGIN_TYPE', type: 'string', required: false, fbType: 'pluginType (enum)', fbId: 2, desc: 'Plugin category' },
      { name: 'NAME', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Plugin name' },
      { name: 'VERSION', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Plugin version' },
      { name: 'DESCRIPTION', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Plugin description' }
    ]
  },
  {
    name: 'PNM',
    fullName: 'Publish Notification Message',
    description: 'Publish Notification Message sent when new data is published to the Space Data Network.',
    category: 'infrastructure',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$PNM',
    fields: [
      { name: 'NOTIFICATION_ID', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Notification identifier' },
      { name: 'SCHEMA_TYPE', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Schema type of published data' },
      { name: 'CID', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'IPFS Content Identifier' },
      { name: 'PUBLISHER_PEER_ID', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Publisher peer ID' },
      { name: 'TIMESTAMP', type: 'integer', required: false, fbType: 'uint64', fbId: 5, desc: 'Publish timestamp' }
    ]
  },
  {
    name: 'PRG',
    fullName: 'Program Description Message',
    description: 'Program Description Message containing program information, assigned message types, and user metadata.',
    category: 'entity',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$PRG',
    fields: [
      { name: 'PROGRAM_ID', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Program identifier' },
      { name: 'PROGRAM_NAME', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Program name' },
      { name: 'DESCRIPTION', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Program description' },
      { name: 'MESSAGE_TYPES', type: 'array', required: false, fbType: '[string]', fbId: 4, desc: 'Assigned message types' },
      { name: 'ORGANIZATION', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Organization' }
    ]
  },
  {
    name: 'PUR',
    fullName: 'Purchase Request',
    description: 'Purchase Request for data from a storefront listing. Contains buyer identity, payment details, and cryptographic proof.',
    category: 'marketplace',
    version: '1.0.1',
    includes: ['STF'],
    fileIdentifier: '$PUR',
    fields: [
      { name: 'REQUEST_ID', type: 'string', required: true, fbType: 'string', fbId: 1, desc: 'Unique identifier for this purchase request' },
      { name: 'LISTING_ID', type: 'string', required: true, fbType: 'string', fbId: 2, desc: 'ID of the listing being purchased' },
      { name: 'TIER_NAME', type: 'string', required: true, fbType: 'string', fbId: 3, desc: 'Name of the pricing tier selected' },
      { name: 'BUYER_PEER_ID', type: 'string', required: true, fbType: 'string', fbId: 4, desc: 'Peer ID of the buyer' },
      { name: 'BUYER_ENCRYPTION_PUBKEY', type: 'array', required: false, fbType: '[ubyte]', fbId: 5, desc: "Buyer's encryption public key" },
      { name: 'PAYMENT_METHOD', type: 'string', required: false, fbType: 'paymentMethod', fbId: 6, desc: 'Payment method used', enum: 'paymentMethod' },
      { name: 'PAYMENT_AMOUNT', type: 'integer', required: false, fbType: 'uint64', fbId: 7, desc: 'Payment amount in smallest unit' },
      { name: 'PAYMENT_CURRENCY', type: 'string', required: false, fbType: 'string', fbId: 8, desc: 'Currency of payment' },
      { name: 'PAYMENT_TX_HASH', type: 'string', required: false, fbType: 'string', fbId: 9, desc: 'Transaction hash for crypto payments' },
      { name: 'PAYMENT_CHAIN', type: 'string', required: false, fbType: 'string', fbId: 10, desc: 'Blockchain network' },
      { name: 'PAYMENT_REFERENCE', type: 'string', required: false, fbType: 'string', fbId: 11, desc: 'Reference for credit/fiat payments' },
      { name: 'BUYER_SIGNATURE', type: 'array', required: false, fbType: '[ubyte]', fbId: 12, desc: 'Ed25519 signature from buyer' },
      { name: 'TIMESTAMP', type: 'integer', required: false, fbType: 'uint64', fbId: 13, desc: 'Request timestamp' }
    ]
  },
  {
    name: 'REC',
    fullName: 'Record Collection',
    description: 'Record Collection message aggregating multiple schema records. Includes all SDS schema types and atmospheric models.',
    category: 'infrastructure',
    version: '1.44.8',
    includes: ['ACL', 'ATM', 'BOV', 'CAT', 'CDM', 'CRM', 'CSM', 'CTR', 'EME', 'EOO', 'EOP', 'EPM', 'HYP', 'LDM', 'MPE', 'OCM', 'OEM', 'OMM', 'OSM', 'PLD', 'PLG', 'PNM', 'PRG', 'PUR', 'REV', 'ROC', 'SCM', 'SIT', 'STF', 'TDM', 'VCM', 'XTC'],
    fileIdentifier: '$REC',
    fields: [
      { name: 'RECORDS', type: 'array', required: false, fbType: '[Record]', fbId: 1, desc: 'Collection of records' },
      { name: 'RECORD_COUNT', type: 'integer', required: false, fbType: 'uint32', fbId: 2, desc: 'Number of records' }
    ]
  },
  {
    name: 'REV',
    fullName: 'Review',
    description: 'User review of a storefront listing. Contains rating, review text, proof of purchase via ACL grant ID, and reviewer signature.',
    category: 'marketplace',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$REV',
    fields: [
      { name: 'REVIEW_ID', type: 'string', required: true, fbType: 'string', fbId: 1, desc: 'Unique identifier for this review' },
      { name: 'LISTING_ID', type: 'string', required: true, fbType: 'string', fbId: 2, desc: 'ID of the listing being reviewed' },
      { name: 'REVIEWER_PEER_ID', type: 'string', required: true, fbType: 'string', fbId: 3, desc: 'Peer ID of the reviewer' },
      { name: 'RATING', type: 'integer', required: false, fbType: 'uint8', fbId: 4, desc: 'Rating from 1-5 stars' },
      { name: 'TITLE', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Review title' },
      { name: 'CONTENT', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Review content/body' },
      { name: 'ACL_GRANT_ID', type: 'string', required: false, fbType: 'string', fbId: 7, desc: 'ACL grant ID proving purchase' },
      { name: 'TIMESTAMP', type: 'integer', required: false, fbType: 'uint64', fbId: 8, desc: 'Review timestamp' },
      { name: 'REVIEWER_SIGNATURE', type: 'array', required: false, fbType: '[ubyte]', fbId: 9, desc: 'Ed25519 signature from reviewer' }
    ]
  },
  {
    name: 'RFM',
    fullName: 'Reference Frame',
    description: 'Celestial Reference Frames from SANA registry (1.3.112.4.57.2). Defines coordinate reference frames for orbital data.',
    category: 'infrastructure',
    version: '4.0.10',
    includes: [],
    fields: [
      { name: 'RFM', type: 'string', required: false, fbType: 'RFM (enum)', fbId: 1, desc: 'Reference frame identifier' }
    ]
  },
  {
    name: 'ROC',
    fullName: 'Rocket Configuration',
    description: 'Rocket Configuration message describing launch vehicle specifications including stages, thrust, and payload capacity.',
    category: 'entity',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$ROC',
    fields: [
      { name: 'VEHICLE_NAME', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Vehicle name' },
      { name: 'MANUFACTURER', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Manufacturer' },
      { name: 'STAGES', type: 'integer', required: false, fbType: 'uint8', fbId: 3, desc: 'Number of stages' },
      { name: 'THRUST_SEA_LEVEL', type: 'number', required: false, fbType: 'double', fbId: 4, desc: 'Sea level thrust in kN' },
      { name: 'PAYLOAD_LEO', type: 'number', required: false, fbType: 'double', fbId: 5, desc: 'Payload to LEO in kg' },
      { name: 'PAYLOAD_GTO', type: 'number', required: false, fbType: 'double', fbId: 6, desc: 'Payload to GTO in kg' },
      { name: 'HEIGHT', type: 'number', required: false, fbType: 'double', fbId: 7, desc: 'Vehicle height in m' },
      { name: 'DIAMETER', type: 'number', required: false, fbType: 'double', fbId: 8, desc: 'Vehicle diameter in m' }
    ]
  },
  {
    name: 'SCM',
    fullName: 'Schema Standard Definition',
    description: 'Schema Standard Definition and Schema Manifest for describing and versioning SDS schema files.',
    category: 'infrastructure',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$SCM',
    fields: [
      { name: 'SCHEMA_NAME', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Schema name' },
      { name: 'SCHEMA_VERSION', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Schema version' },
      { name: 'HASH', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Schema content hash' },
      { name: 'DESCRIPTION', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Schema description' },
      { name: 'DEPENDENCIES', type: 'array', required: false, fbType: '[string]', fbId: 5, desc: 'Schema dependencies' }
    ]
  },
  {
    name: 'SIT',
    fullName: 'Site Information',
    description: 'Site Information for ground stations, launch sites, and observation facilities including coordinates and type.',
    category: 'entity',
    version: '1.0.1',
    includes: ['IDM'],
    fileIdentifier: '$SIT',
    fields: [
      { name: 'SITE_ID', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Site identifier' },
      { name: 'SITE_NAME', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Site name' },
      { name: 'SITE_TYPE', type: 'string', required: false, fbType: 'siteType (enum)', fbId: 3, desc: 'Site type' },
      { name: 'LATITUDE', type: 'number', required: false, fbType: 'double', fbId: 4, desc: 'Latitude in degrees' },
      { name: 'LONGITUDE', type: 'number', required: false, fbType: 'double', fbId: 5, desc: 'Longitude in degrees' },
      { name: 'ALTITUDE', type: 'number', required: false, fbType: 'double', fbId: 6, desc: 'Altitude in meters' },
      { name: 'COUNTRY', type: 'string', required: false, fbType: 'string', fbId: 7, desc: 'Country' }
    ]
  },
  {
    name: 'STF',
    fullName: 'Storefront Listing',
    description: 'Storefront Listing for the data marketplace. Contains listing details, pricing tiers, payment methods, spatial/temporal coverage, and provider signature.',
    category: 'marketplace',
    version: '1.0.1',
    includes: [],
    fileIdentifier: '$STF',
    fields: [
      { name: 'LISTING_ID', type: 'string', required: true, fbType: 'string', fbId: 1, desc: 'Unique listing identifier' },
      { name: 'PROVIDER_PEER_ID', type: 'string', required: true, fbType: 'string', fbId: 2, desc: 'Peer ID of data provider' },
      { name: 'PROVIDER_EPM_CID', type: 'string', required: false, fbType: 'string', fbId: 3, desc: "IPFS CID of provider's EPM" },
      { name: 'TITLE', type: 'string', required: true, fbType: 'string', fbId: 4, desc: 'Listing title' },
      { name: 'DESCRIPTION', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Detailed description' },
      { name: 'DATA_TYPES', type: 'array', required: false, fbType: '[string]', fbId: 6, desc: 'SDS data types offered' },
      { name: 'COVERAGE', type: 'object', required: false, fbType: 'DataCoverage', fbId: 7, desc: 'Spatial/temporal coverage' },
      { name: 'SAMPLE_CID', type: 'string', required: false, fbType: 'string', fbId: 8, desc: 'IPFS CID of sample data' },
      { name: 'ACCESS_TYPE', type: 'string', required: false, fbType: 'accessType', fbId: 9, desc: 'Access type', enum: 'accessType' },
      { name: 'ENCRYPTION_REQUIRED', type: 'boolean', required: false, fbType: 'bool', fbId: 10, desc: 'Encryption required for delivery' },
      { name: 'PRICING', type: 'array', required: false, fbType: '[PricingTier]', fbId: 11, desc: 'Available pricing tiers' },
      { name: 'ACCEPTED_PAYMENTS', type: 'array', required: false, fbType: '[paymentMethod]', fbId: 12, desc: 'Accepted payment methods' },
      { name: 'CREATED_AT', type: 'integer', required: false, fbType: 'uint64', fbId: 13, desc: 'Creation timestamp' },
      { name: 'UPDATED_AT', type: 'integer', required: false, fbType: 'uint64', fbId: 14, desc: 'Last update timestamp' },
      { name: 'ACTIVE', type: 'boolean', required: false, fbType: 'bool', fbId: 15, desc: 'Whether listing is active' },
      { name: 'SIGNATURE', type: 'array', required: false, fbType: '[ubyte]', fbId: 16, desc: 'Ed25519 signature from provider' }
    ]
  },
  {
    name: 'TDM',
    fullName: 'Tracking Data Message',
    description: 'Tracking Data Message per CCSDS 503.0-B-1 containing range, Doppler, and angles observations from tracking stations.',
    category: 'telemetry',
    version: '1.0.2',
    includes: ['RFM'],
    fileIdentifier: '$TDM',
    fields: [
      { name: 'CCSDS_TDM_VERS', type: 'number', required: false, fbType: 'double', fbId: 1, desc: 'CCSDS TDM version' },
      { name: 'CREATION_DATE', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Creation date (ISO 8601)' },
      { name: 'ORIGINATOR', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Originator' },
      { name: 'PARTICIPANT_1', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Participant 1 (transmitter/sender)' },
      { name: 'PARTICIPANT_2', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Participant 2 (receiver/target)' },
      { name: 'START_TIME', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Start time (ISO 8601)' },
      { name: 'STOP_TIME', type: 'string', required: false, fbType: 'string', fbId: 7, desc: 'Stop time (ISO 8601)' },
      { name: 'REFERENCE_FRAME', type: 'string', required: false, fbType: 'RFM', fbId: 8, desc: 'Reference frame' },
      { name: 'DATA_LINES', type: 'array', required: false, fbType: '[TrackingDataLine]', fbId: 9, desc: 'Tracking data observations' }
    ]
  },
  {
    name: 'TIM',
    fullName: 'Time System',
    description: 'Time System enumeration defining time references (UTC, GMST, GPS, TAI, TT, UT1, MRT, SCLK, etc.).',
    category: 'infrastructure',
    version: '1.0.1',
    includes: [],
    fields: [
      { name: 'TIM', type: 'string', required: false, fbType: 'timeSystem (enum)', fbId: 1, desc: 'Time system type' }
    ]
  },
  {
    name: 'VCM',
    fullName: 'Vector Covariance Message',
    description: 'Vector Covariance Message per CCSDS standards. Contains state vectors (position/velocity) with full covariance matrix and perturbation model details.',
    category: 'orbital',
    version: '1.2.6',
    includes: ['RFM', 'TIM', 'MET'],
    fileIdentifier: '$VCM',
    fields: [
      { name: 'CCSDS_VCM_VERS', type: 'number', required: false, fbType: 'double', fbId: 1, desc: 'CCSDS VCM version' },
      { name: 'CREATION_DATE', type: 'string', required: false, fbType: 'string', fbId: 2, desc: 'Creation date (ISO 8601)' },
      { name: 'ORIGINATOR', type: 'string', required: false, fbType: 'string', fbId: 3, desc: 'Originator' },
      { name: 'OBJECT_NAME', type: 'string', required: false, fbType: 'string', fbId: 4, desc: 'Object name' },
      { name: 'OBJECT_ID', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'International designator' },
      { name: 'CENTER_NAME', type: 'string', required: false, fbType: 'string', fbId: 6, desc: 'Center name' },
      { name: 'REFERENCE_FRAME', type: 'string', required: false, fbType: 'RFM', fbId: 7, desc: 'Reference frame' },
      { name: 'EPOCH', type: 'string', required: false, fbType: 'string', fbId: 8, desc: 'Epoch (ISO 8601)' },
      { name: 'X', type: 'number', required: false, fbType: 'double', fbId: 9, desc: 'X position in km' },
      { name: 'Y', type: 'number', required: false, fbType: 'double', fbId: 10, desc: 'Y position in km' },
      { name: 'Z', type: 'number', required: false, fbType: 'double', fbId: 11, desc: 'Z position in km' },
      { name: 'X_DOT', type: 'number', required: false, fbType: 'double', fbId: 12, desc: 'X velocity in km/s' },
      { name: 'Y_DOT', type: 'number', required: false, fbType: 'double', fbId: 13, desc: 'Y velocity in km/s' },
      { name: 'Z_DOT', type: 'number', required: false, fbType: 'double', fbId: 14, desc: 'Z velocity in km/s' }
    ]
  },
  {
    name: 'XTC',
    fullName: 'XTCE Telemetry/Command Exchange',
    description: 'XML Telemetry and Command Exchange per CCSDS 660.1-G-2. Defines telemetry and command parameters, containers, and encoding for spacecraft communication.',
    category: 'telemetry',
    version: '1.0.3',
    includes: [],
    fileIdentifier: '$XTC',
    fields: [
      { name: 'SPACE_SYSTEM_NAME', type: 'string', required: false, fbType: 'string', fbId: 1, desc: 'Space system name' },
      { name: 'PARAMETERS', type: 'array', required: false, fbType: '[XTCEParameter]', fbId: 2, desc: 'Telemetry parameters' },
      { name: 'COMMANDS', type: 'array', required: false, fbType: '[XTCECommand]', fbId: 3, desc: 'Command definitions' },
      { name: 'CONTAINERS', type: 'array', required: false, fbType: '[XTCEContainer]', fbId: 4, desc: 'Telemetry containers' },
      { name: 'ENCODING', type: 'string', required: false, fbType: 'string', fbId: 5, desc: 'Default encoding type' }
    ]
  }
]

// Code generation functions

export function generateFbs (s) {
  const lines = [`// ${s.fullName}`, `// Version: ${s.version}`, '']
  if (s.includes && s.includes.length) {
    s.includes.forEach(inc => lines.push(`include "../${inc}/main.fbs";`))
    lines.push('')
  }
  lines.push(`/// ${s.description.split('.')[0]}`)
  lines.push(`table ${s.name} {`)
  s.fields.forEach(f => {
    lines.push(`  /// ${f.desc}`)
    const req = f.required ? ' (required)' : ''
    const def = f.default ? ` = ${f.default}` : ''
    lines.push(`  ${f.name}:${f.fbType}${def}${req};`)
  })
  lines.push('}')
  if (s.fileIdentifier) {
    lines.push('')
    lines.push(`root_type ${s.name};`)
    lines.push(`file_identifier "${s.fileIdentifier}";`)
  }
  return lines.join('\n')
}

export function generateTS (s) {
  const typeMap = { string: 'string', number: 'number', integer: 'number', boolean: 'boolean', array: 'any[]', object: 'Record<string, any>' }
  const lines = [`/** ${s.fullName} - v${s.version} */`, `export interface ${s.name} {`]
  s.fields.forEach(f => {
    const opt = f.required ? '' : '?'
    lines.push(`  /** ${f.desc} */`)
    lines.push(`  ${f.name}${opt}: ${typeMap[f.type] || 'any'};`)
  })
  lines.push('}')
  return lines.join('\n')
}

export function generateGo (s) {
  const typeMap = { string: 'string', number: 'float64', integer: 'uint32', boolean: 'bool', array: '[]interface{}', object: 'map[string]interface{}' }
  const lines = [`// ${s.fullName} - v${s.version}`, 'package sds', '', `type ${s.name} struct {`]
  s.fields.forEach(f => {
    const tag = `\`json:"${f.name},omitempty" flatbuffer:"${f.fbId}"\``
    lines.push(`\t// ${f.desc}`)
    lines.push(`\t${f.name} ${typeMap[f.type] || 'interface{}'} ${tag}`)
  })
  lines.push('}')
  return lines.join('\n')
}

export function generatePython (s) {
  const typeMap = { string: 'str', number: 'float', integer: 'int', boolean: 'bool', array: 'list', object: 'dict' }
  const lines = [`"""${s.fullName} - v${s.version}"""`, 'from dataclasses import dataclass', 'from typing import Optional', '', '', '@dataclass', `class ${s.name}:`]
  lines.push(`    """${s.description.split('.')[0]}"""`)
  s.fields.forEach(f => {
    const t = typeMap[f.type] || 'object'
    if (f.required) {
      lines.push(`    ${f.name}: ${t}  # ${f.desc}`)
    } else {
      lines.push(`    ${f.name}: Optional[${t}] = None  # ${f.desc}`)
    }
  })
  return lines.join('\n')
}

export function generateRust (s) {
  const typeMap = { string: 'String', number: 'f64', integer: 'u32', boolean: 'bool', array: 'Vec<serde_json::Value>', object: 'std::collections::HashMap<String, serde_json::Value>' }
  const lines = [`/// ${s.fullName} - v${s.version}`, '#[derive(Debug, Clone, Serialize, Deserialize)]', `pub struct ${s.name} {`]
  s.fields.forEach(f => {
    const t = typeMap[f.type] || 'serde_json::Value'
    lines.push(`    /// ${f.desc}`)
    if (f.required) {
      lines.push(`    pub ${f.name.toLowerCase()}: ${t},`)
    } else {
      lines.push('    #[serde(skip_serializing_if = "Option::is_none")]')
      lines.push(`    pub ${f.name.toLowerCase()}: Option<${t}>,`)
    }
  })
  lines.push('}')
  return lines.join('\n')
}

export function generateCode (schema, format) {
  switch (format) {
    case 'flatbuffers': return generateFbs(schema)
    case 'typescript': return generateTS(schema)
    case 'go': return generateGo(schema)
    case 'python': return generatePython(schema)
    case 'rust': return generateRust(schema)
    default: return ''
  }
}

export function generateJsonSchema (schema) {
  const properties = {}
  const required = []

  schema.fields.forEach(f => {
    const prop = {
      description: f.desc,
      'x-flatbuffer-type': f.fbType,
      'x-flatbuffer-field-id': f.fbId
    }

    switch (f.type) {
      case 'string': prop.type = 'string'; break
      case 'number': prop.type = 'number'; break
      case 'integer': prop.type = 'integer'; break
      case 'boolean': prop.type = 'boolean'; break
      case 'array': prop.type = 'array'; prop.items = {}; break
      case 'object': prop.type = 'object'; break
      default: break
    }

    if (f.default) prop['x-flatbuffer-default'] = f.default
    if (f.enum) prop['x-flatbuffer-enum'] = f.enum
    if (f.required) {
      prop['x-flatbuffer-required'] = true
      required.push(f.name)
    }

    properties[f.name] = prop
  })

  return {
    $schema: 'https://json-schema.org/draft/2020-12/schema',
    $id: `https://spacedatastandards.org/schemas/${schema.name}.json`,
    title: `${schema.name} - ${schema.fullName}`,
    description: schema.description,
    type: 'object',
    'x-flatbuffer-root': true,
    'x-flatbuffer-file-identifier': schema.fileIdentifier || undefined,
    'x-flatbuffer-version': schema.version,
    properties,
    ...(required.length ? { required } : {})
  }
}

export function downloadContent (content, filename, mime) {
  const blob = new Blob([content], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
