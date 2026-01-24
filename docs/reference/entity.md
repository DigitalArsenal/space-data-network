# Entity Data Schemas

Reference for organization, operator, and node identification schemas.

## EPM - Entity Profile Manifest

Identifies organizations and operators on the network. Used for verified data attribution.

### Schema Definition

```flatbuffers
table EPM {
  // Identification
  ENTITY_ID: string;               // Unique entity identifier
  ENTITY_NAME: string;             // Full legal name
  ENTITY_TYPE: string;             // OPERATOR, AGENCY, PROVIDER, etc.

  // Organization Details
  PARENT_ENTITY_ID: string;        // Parent organization (if subsidiary)
  COUNTRY: string;                 // ISO 3166-1 country code
  REGISTRATION_NUMBER: string;     // Legal registration number

  // Contact Information
  PRIMARY_CONTACT_NAME: string;
  PRIMARY_CONTACT_TITLE: string;
  EMAIL: string;
  PHONE: string;
  ADDRESS_LINE_1: string;
  ADDRESS_LINE_2: string;
  CITY: string;
  STATE_PROVINCE: string;
  POSTAL_CODE: string;
  COUNTRY_CODE: string;

  // Emergency Contact
  EMERGENCY_CONTACT_NAME: string;
  EMERGENCY_CONTACT_EMAIL: string;
  EMERGENCY_CONTACT_PHONE: string;

  // Network Identity
  PUBLIC_KEY: [uint8];             // Ed25519 public key
  PUBLIC_KEY_ALGORITHM: string;    // Key algorithm (Ed25519)
  PEER_ID: string;                 // libp2p Peer ID
  PEER_ADDRESSES: [string];        // Multiaddresses

  // Capabilities
  SUPPORTED_SCHEMAS: [string];     // OMM, CDM, etc.
  REGIONS: [string];               // Geographic regions served
  SERVICES: [string];              // DATA_PROVIDER, RELAY, etc.

  // Assets
  OPERATED_OBJECTS: [string];      // NORAD IDs of operated satellites
  GROUND_STATIONS: [string];       // Station identifiers

  // Metadata
  CREATED: string;                 // ISO 8601 creation timestamp
  UPDATED: string;                 // Last update timestamp
  VALID_UNTIL: string;             // Expiration date
  SIGNATURE: [uint8];              // Entity self-signature
}
```

### Entity Types

| Type | Description |
|------|-------------|
| OPERATOR | Satellite operator |
| AGENCY | Government space agency |
| PROVIDER | Data/service provider |
| RESEARCHER | Research institution |
| COMMERCIAL | Commercial entity |
| MILITARY | Military organization |

### Example

```typescript
const epm = {
  ENTITY_ID: "ORG-12345",
  ENTITY_NAME: "Example Space Corporation",
  ENTITY_TYPE: "OPERATOR",
  COUNTRY: "US",

  PRIMARY_CONTACT_NAME: "Jane Smith",
  PRIMARY_CONTACT_TITLE: "Space Traffic Coordinator",
  EMAIL: "stc@example-space.com",
  PHONE: "+1-555-0123",
  CITY: "Houston",
  STATE_PROVINCE: "TX",
  COUNTRY_CODE: "US",

  EMERGENCY_CONTACT_NAME: "Operations Center",
  EMERGENCY_CONTACT_EMAIL: "ops@example-space.com",
  EMERGENCY_CONTACT_PHONE: "+1-555-0911",

  PUBLIC_KEY_ALGORITHM: "Ed25519",
  PEER_ID: "12D3KooWExample...",

  SUPPORTED_SCHEMAS: ["OMM", "CDM", "EPM"],
  REGIONS: ["NORTH_AMERICA", "EUROPE"],
  SERVICES: ["DATA_PROVIDER", "OPERATOR"],

  OPERATED_OBJECTS: ["12345", "12346", "12347"],

  CREATED: "2024-01-01T00:00:00.000Z",
  UPDATED: "2024-01-15T12:00:00.000Z",
  VALID_UNTIL: "2025-01-01T00:00:00.000Z"
};
```

---

## PNM - Peer Node Message

Describes network node capabilities and status.

### Schema Definition

```flatbuffers
table PNM {
  // Node Identification
  PEER_ID: string;                 // libp2p Peer ID
  NODE_NAME: string;               // Human-readable name
  NODE_TYPE: string;               // FULL, EDGE, BROWSER
  NODE_VERSION: string;            // Software version

  // Network Addresses
  LISTEN_ADDRESSES: [string];      // Listening multiaddresses
  ANNOUNCE_ADDRESSES: [string];    // Announced multiaddresses
  OBSERVED_ADDRESSES: [string];    // Addresses observed by peers

  // Capabilities
  PROTOCOLS: [string];             // Supported protocols
  SUPPORTED_SCHEMAS: [string];     // Data schemas supported

  // Relay information
  IS_RELAY: bool;                  // Acts as circuit relay
  RELAY_RESERVATIONS: uint32;      // Current relay reservations
  MAX_RELAY_RESERVATIONS: uint32;  // Maximum reservations

  // Status
  UPTIME_SECONDS: uint64;          // Node uptime
  CONNECTED_PEERS: uint32;         // Current peer count
  BANDWIDTH_IN: uint64;            // Bytes received
  BANDWIDTH_OUT: uint64;           // Bytes sent

  // Storage
  STORAGE_USED: uint64;            // Bytes stored
  STORAGE_CAPACITY: uint64;        // Maximum storage
  RECORD_COUNT: uint64;            // Total records stored

  // Metadata
  CREATED: string;
  UPDATED: string;
}
```

### Node Types

| Type | Description |
|------|-------------|
| FULL | Full node with persistent storage |
| EDGE | Edge relay for browser connectivity |
| BROWSER | Browser-based node |
| EMBEDDED | Embedded/IoT device |

---

## CAT - Catalog Entry

Space object catalog entry with metadata.

### Schema Definition

```flatbuffers
table CAT {
  // Object Identification
  NORAD_CAT_ID: uint32;            // NORAD catalog number
  OBJECT_ID: string;               // International designator
  OBJECT_NAME: string;             // Common name
  OBJECT_TYPE: string;             // PAYLOAD, ROCKET BODY, DEBRIS

  // Origin
  LAUNCH_DATE: string;             // Launch date
  LAUNCH_SITE: string;             // Launch site code
  LAUNCH_VEHICLE: string;          // Launch vehicle type
  LAUNCH_MISSION: string;          // Mission name

  // Operator
  OWNER: string;                   // Owning country/organization
  OPERATOR: string;                // Operating entity
  OPERATOR_ENTITY_ID: string;      // Reference to EPM

  // Physical Properties
  RCS: double;                     // Radar cross-section (m^2)
  MASS: double;                    // Mass (kg)
  SHAPE: string;                   // Object shape
  SIZE: string;                    // Size category

  // Status
  OPERATIONAL_STATUS: string;      // ACTIVE, INACTIVE, DECAYED
  DECAY_DATE: string;              // Decay date (if applicable)

  // Orbital Regime
  ORBIT_TYPE: string;              // LEO, MEO, GEO, HEO
  APOGEE: double;                  // Apogee altitude (km)
  PERIGEE: double;                 // Perigee altitude (km)
  INCLINATION: double;             // Inclination (degrees)
  PERIOD: double;                  // Orbital period (minutes)

  // Metadata
  CREATED: string;
  UPDATED: string;
}
```

### Object Types

| Type | Description |
|------|-------------|
| PAYLOAD | Operational satellite |
| ROCKET BODY | Spent rocket stage |
| DEBRIS | Space debris fragment |
| TBA | To be assigned |
| UNKNOWN | Unknown object |

---

## SIT - Site Message

Ground station or facility information.

### Schema Definition

```flatbuffers
table SIT {
  // Site Identification
  SITE_ID: string;                 // Unique identifier
  SITE_NAME: string;               // Facility name
  SITE_TYPE: string;               // GROUND_STATION, OPERATIONS_CENTER

  // Location
  LATITUDE: double;                // Latitude (degrees)
  LONGITUDE: double;               // Longitude (degrees)
  ALTITUDE: double;                // Altitude (meters)
  GEODETIC_DATUM: string;          // Reference datum (WGS84)

  // Operator
  OPERATOR: string;                // Operating organization
  OPERATOR_ENTITY_ID: string;      // Reference to EPM

  // Capabilities
  ANTENNA_TYPES: [string];         // Antenna types available
  FREQUENCY_BANDS: [string];       // S, X, Ka, etc.
  SERVICES: [string];              // TT&C, TRACKING, etc.

  // Coverage
  MIN_ELEVATION: double;           // Minimum elevation (degrees)
  MAX_RANGE: double;               // Maximum range (km)

  // Contact
  CONTACT_EMAIL: string;
  CONTACT_PHONE: string;

  // Metadata
  CREATED: string;
  UPDATED: string;
}
```

---

## Usage Patterns

### Publish Organization Profile

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Publish organization profile
const epm = {
  ENTITY_ID: `ORG-${node.peerId}`,
  ENTITY_NAME: 'My Organization',
  ENTITY_TYPE: 'OPERATOR',
  // ... other fields
  PEER_ID: node.peerId,
  SUPPORTED_SCHEMAS: ['OMM', 'CDM'],
  CREATED: new Date().toISOString(),
  UPDATED: new Date().toISOString()
};

await node.publish('EPM', epm);
```

### Discover Organizations

```typescript
// Find all operators
const operators = await node.query('EPM', {
  where: { ENTITY_TYPE: 'OPERATOR' }
});

// Find organizations in a region
const euOperators = await node.query('EPM', {
  where: {
    ENTITY_TYPE: 'OPERATOR',
    REGIONS: { $contains: 'EUROPE' }
  }
});

// Find by capability
const cdmProviders = await node.query('EPM', {
  where: {
    SUPPORTED_SCHEMAS: { $contains: 'CDM' }
  }
});
```

### Verify Data Source

```typescript
import { verifySignature } from '@spacedatanetwork/sdn-js';

node.subscribe('OMM', async (omm, peerId, metadata) => {
  // Find entity profile for peer
  const entities = await node.query('EPM', {
    where: { PEER_ID: peerId }
  });

  if (entities.length > 0) {
    const entity = entities[0];

    // Verify signature
    const isValid = await verifySignature(
      omm,
      metadata.signature,
      entity.PUBLIC_KEY
    );

    if (isValid) {
      console.log(`Verified OMM from ${entity.ENTITY_NAME}`);
    }
  }
});
```

### Monitor Network Nodes

```typescript
// Subscribe to node announcements
node.subscribe('PNM', (pnm, peerId) => {
  console.log(`Node: ${pnm.NODE_NAME}`);
  console.log(`  Type: ${pnm.NODE_TYPE}`);
  console.log(`  Peers: ${pnm.CONNECTED_PEERS}`);
  console.log(`  Uptime: ${pnm.UPTIME_SECONDS}s`);
});

// Query network statistics
const relays = await node.query('PNM', {
  where: { IS_RELAY: true }
});

console.log(`Active relays: ${relays.length}`);
```

## See Also

- [Schema Overview](/reference/schemas)
- [Digital Identity](/guide/digital-identity)
- [SDS Exchange Protocol](/reference/protocol-sds)
