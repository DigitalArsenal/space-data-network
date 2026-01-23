# Schema Reference

Space Data Network uses [FlatBuffers](https://google.github.io/flatbuffers/) schemas from the [Space Data Standards](https://spacedatastandards.org) project. This page provides an overview of all 32 supported schemas.

## Schema Categories

### Orbital Data

| Schema | Name | Description |
|--------|------|-------------|
| **OMM** | Orbit Mean-Elements Message | Mean orbital elements (TLE-like data) |
| **OEM** | Orbit Ephemeris Message | State vectors over time |
| **OCM** | Orbit Comprehensive Message | Complete orbit characterization |
| **OSM** | Orbit State Message | Instantaneous state vector |

### Conjunction Data

| Schema | Name | Description |
|--------|------|-------------|
| **CDM** | Conjunction Data Message | Close approach predictions |
| **CSM** | Conjunction Summary Message | Conjunction event summaries |

### Tracking Data

| Schema | Name | Description |
|--------|------|-------------|
| **TDM** | Tracking Data Message | Radar/optical measurements |
| **RFM** | RF Metrics Message | Radio frequency measurements |

### Entity Information

| Schema | Name | Description |
|--------|------|-------------|
| **EPM** | Entity Profile Message | Organization/operator profiles |
| **PNM** | Peer Node Message | Network node identification |
| **CAT** | Catalog Entry | Space object catalog entry |
| **SIT** | Site Message | Ground station/facility info |

### Maneuver Data

| Schema | Name | Description |
|--------|------|-------------|
| **MET** | Maneuver Execution Tracking | Executed maneuver data |
| **MPE** | Maneuver Plan Entry | Planned maneuver details |

### Propagation Data

| Schema | Name | Description |
|--------|------|-------------|
| **HYP** | Hyperbolic Orbit | Hyperbolic trajectory data |
| **EME** | Earth-Moon Ephemeris | Earth-Moon system data |
| **EOO** | Earth Orientation | Earth orientation parameters |
| **EOP** | Earth Orientation Parameters | Pole/UT1 corrections |

### Reference Data

| Schema | Name | Description |
|--------|------|-------------|
| **LCC** | Launch Conjunction | Launch window constraints |
| **LDM** | Launch Data Message | Launch vehicle data |
| **CRM** | Collision Risk Message | Risk assessment data |
| **CTR** | Contact Report | Communication contact data |

### Other Standards

| Schema | Name | Description |
|--------|------|-------------|
| **ATM** | Attitude Message | Spacecraft attitude data |
| **BOV** | Burn-Out Velocity | Rocket burn-out state |
| **IDM** | Identification Message | Object identification |
| **PLD** | Payload Message | Payload information |
| **PRG** | Program Message | Program/mission data |
| **REC** | Record Message | Generic record container |
| **ROC** | Reentry Operations | Reentry predictions |
| **SCM** | Spacecraft Message | Spacecraft configuration |
| **TIM** | Time Message | Time synchronization |
| **VCM** | Vector Covariance Message | State with covariance |

## Schema Details

### OMM - Orbit Mean-Elements Message

The most commonly used schema for sharing orbital data. Compatible with TLE/3LE data.

```flatbuffers
table OMM {
  CCSDS_OMM_VERS: string;
  CREATION_DATE: string;
  ORIGINATOR: string;

  // Object identification
  OBJECT_NAME: string;
  OBJECT_ID: string;
  CENTER_NAME: string;
  REF_FRAME: string;
  TIME_SYSTEM: string;

  // Mean elements
  EPOCH: string;
  MEAN_MOTION: double;
  ECCENTRICITY: double;
  INCLINATION: double;
  RA_OF_ASC_NODE: double;
  ARG_OF_PERICENTER: double;
  MEAN_ANOMALY: double;

  // Additional parameters
  GM: double;
  MASS: double;
  DRAG_AREA: double;
  DRAG_COEFF: double;
  SOLAR_RAD_AREA: double;
  SOLAR_RAD_COEFF: double;

  // TLE-specific
  EPHEMERIS_TYPE: uint8;
  CLASSIFICATION_TYPE: string;
  NORAD_CAT_ID: uint32;
  ELEMENT_SET_NO: uint16;
  REV_AT_EPOCH: uint32;
  BSTAR: double;
  MEAN_MOTION_DOT: double;
  MEAN_MOTION_DDOT: double;
}
```

**Example Usage:**

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Subscribe to OMM data
node.subscribe('OMM', (omm, peerId) => {
  console.log(`Object: ${omm.OBJECT_NAME}`);
  console.log(`NORAD ID: ${omm.NORAD_CAT_ID}`);
  console.log(`Epoch: ${omm.EPOCH}`);
  console.log(`Inclination: ${omm.INCLINATION}°`);
});
```

### CDM - Conjunction Data Message

Critical for space safety operations. Contains close approach predictions.

```flatbuffers
table CDM {
  CCSDS_CDM_VERS: string;
  CREATION_DATE: string;
  ORIGINATOR: string;
  MESSAGE_FOR: string;
  MESSAGE_ID: string;

  // Time of closest approach
  TCA: string;
  MISS_DISTANCE: double;

  // Object 1 (typically the protected asset)
  OBJECT1_DESIGNATOR: string;
  OBJECT1_CATALOG_NAME: string;
  OBJECT1_OBJECT_NAME: string;
  OBJECT1_INTERNATIONAL_DESIGNATOR: string;
  OBJECT1_EPHEMERIS_NAME: string;
  // ... position, velocity, covariance

  // Object 2 (the threat object)
  OBJECT2_DESIGNATOR: string;
  OBJECT2_CATALOG_NAME: string;
  OBJECT2_OBJECT_NAME: string;
  OBJECT2_INTERNATIONAL_DESIGNATOR: string;
  // ... position, velocity, covariance

  // Collision probability
  COLLISION_PROBABILITY: double;
  COLLISION_PROBABILITY_METHOD: string;
}
```

### EPM - Entity Profile Message

Identifies organizations and operators on the network.

```flatbuffers
table EPM {
  // Identification
  ENTITY_ID: string;
  ENTITY_NAME: string;
  ENTITY_TYPE: string;

  // Contact information
  EMAIL: string;
  PHONE: string;
  ADDRESS: string;

  // Network identity
  PUBLIC_KEY: [uint8];
  PEER_ID: string;

  // Capabilities
  SUPPORTED_SCHEMAS: [string];
  REGIONS: [string];

  // Metadata
  CREATED: string;
  UPDATED: string;
}
```

## Using Schemas

### JavaScript

```typescript
import { SchemaRegistry, validateSchema } from '@spacedatanetwork/sdn-js';

// Get schema information
const schemas = SchemaRegistry.list();
console.log(`Supported schemas: ${schemas.join(', ')}`);

// Validate data against schema
const isValid = await validateSchema('OMM', ommData);
```

### Go

```go
import "github.com/spacedatanetwork/sdn-server/internal/sds"

// List supported schemas
schemas := sds.SupportedSchemas
for _, schema := range schemas {
    fmt.Println(schema)
}

// Validate data
validator, _ := sds.NewValidator(wasmModule)
err := validator.Validate(ctx, "OMM", ommBytes)
```

## PubSub Topics

Each schema has a dedicated PubSub topic:

```
/spacedatanetwork/{discovery_hash}-{SCHEMA}
```

Examples:
- `/spacedatanetwork/abc123-OMM`
- `/spacedatanetwork/abc123-CDM`
- `/spacedatanetwork/abc123-EPM`

Subscribe to receive real-time updates for specific data types.

## Schema Evolution

Space Data Standards are versioned. SDN supports:

- **Backward compatibility** - New fields are optional
- **Schema versioning** - CCSDS_*_VERS field indicates version
- **Migration support** - Automatic conversion between compatible versions

## External Resources

- [Space Data Standards](https://spacedatastandards.org) - Official schema specifications
- [FlatBuffers Documentation](https://google.github.io/flatbuffers/) - Serialization format
- [CCSDS Standards](https://public.ccsds.org/) - Source standards
