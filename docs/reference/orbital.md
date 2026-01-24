# Orbital Data Schemas

Detailed reference for orbital data schemas in the Space Data Standards.

## OMM - Orbit Mean-Elements Message

The most widely used schema for exchanging orbital data. Compatible with Two-Line Element (TLE) format.

### Schema Definition

```flatbuffers
table OMM {
  // CCSDS Header
  CCSDS_OMM_VERS: string;          // Version (e.g., "3.0")
  CREATION_DATE: string;           // ISO 8601 timestamp
  ORIGINATOR: string;              // Data originator

  // Object Identification
  OBJECT_NAME: string;             // Satellite name
  OBJECT_ID: string;               // International designator
  CENTER_NAME: string;             // Central body (EARTH)
  REF_FRAME: string;               // Reference frame (TEME, GCRF)
  TIME_SYSTEM: string;             // Time system (UTC)

  // Mean Keplerian Elements
  MEAN_ELEMENT_THEORY: string;     // Propagation theory (SGP4)
  EPOCH: string;                   // Epoch time (ISO 8601)
  SEMI_MAJOR_AXIS: double;         // Semi-major axis (km)
  MEAN_MOTION: double;             // Mean motion (rev/day)
  ECCENTRICITY: double;            // Orbital eccentricity
  INCLINATION: double;             // Inclination (degrees)
  RA_OF_ASC_NODE: double;          // RAAN (degrees)
  ARG_OF_PERICENTER: double;       // Argument of perigee (degrees)
  MEAN_ANOMALY: double;            // Mean anomaly (degrees)

  // Physical Properties
  GM: double;                      // Gravitational parameter
  MASS: double;                    // Spacecraft mass (kg)
  DRAG_AREA: double;               // Drag cross-section (m^2)
  DRAG_COEFF: double;              // Drag coefficient
  SOLAR_RAD_AREA: double;          // Solar radiation area (m^2)
  SOLAR_RAD_COEFF: double;         // Solar radiation coefficient

  // TLE-Specific Parameters
  EPHEMERIS_TYPE: uint8;           // Ephemeris type (0=SGP, 2=SGP4)
  CLASSIFICATION_TYPE: string;     // Classification (U/C/S)
  NORAD_CAT_ID: uint32;            // NORAD catalog number
  ELEMENT_SET_NO: uint16;          // Element set number
  REV_AT_EPOCH: uint32;            // Revolution number at epoch
  BSTAR: double;                   // B* drag term
  MEAN_MOTION_DOT: double;         // First derivative of mean motion
  MEAN_MOTION_DDOT: double;        // Second derivative of mean motion
}
```

### Field Descriptions

| Field | Type | Unit | Description |
|-------|------|------|-------------|
| EPOCH | string | ISO 8601 | Reference time for orbital elements |
| SEMI_MAJOR_AXIS | double | km | Distance from center to orbit |
| MEAN_MOTION | double | rev/day | Orbital revolutions per day |
| ECCENTRICITY | double | - | Shape of orbit (0=circular, <1=elliptical) |
| INCLINATION | double | degrees | Tilt of orbit plane (0-180) |
| RA_OF_ASC_NODE | double | degrees | Right Ascension of ascending node (0-360) |
| ARG_OF_PERICENTER | double | degrees | Angle to perigee from ascending node |
| MEAN_ANOMALY | double | degrees | Position along orbit at epoch |
| BSTAR | double | 1/Earth radii | SGP4 drag term |

### Example

```typescript
const omm = {
  CCSDS_OMM_VERS: "3.0",
  CREATION_DATE: "2024-01-15T12:00:00.000Z",
  ORIGINATOR: "NASA",
  OBJECT_NAME: "ISS (ZARYA)",
  OBJECT_ID: "1998-067A",
  CENTER_NAME: "EARTH",
  REF_FRAME: "TEME",
  TIME_SYSTEM: "UTC",
  MEAN_ELEMENT_THEORY: "SGP4",
  EPOCH: "2024-01-15T12:00:00.000Z",
  MEAN_MOTION: 15.49241843,
  ECCENTRICITY: 0.0001235,
  INCLINATION: 51.6416,
  RA_OF_ASC_NODE: 123.4567,
  ARG_OF_PERICENTER: 67.8901,
  MEAN_ANOMALY: 234.5678,
  NORAD_CAT_ID: 25544,
  BSTAR: 0.00010270,
  MEAN_MOTION_DOT: 0.00016717,
  MEAN_MOTION_DDOT: 0.0
};
```

### Conversion from TLE

```typescript
import { TLEParser } from '@spacedatanetwork/sdn-js';

const tle = `ISS (ZARYA)
1 25544U 98067A   24015.50000000  .00016717  00000-0  10270-3 0  9993
2 25544  51.6416 123.4567 0001235  67.8901 234.5678 15.49241843456789`;

const omm = TLEParser.toOMM(tle);
```

---

## OEM - Orbit Ephemeris Message

Contains state vectors (position and velocity) over a time span.

### Schema Definition

```flatbuffers
table OEM {
  // Header
  CCSDS_OEM_VERS: string;
  CREATION_DATE: string;
  ORIGINATOR: string;

  // Metadata
  OBJECT_NAME: string;
  OBJECT_ID: string;
  CENTER_NAME: string;
  REF_FRAME: string;
  TIME_SYSTEM: string;

  // Ephemeris data
  START_TIME: string;
  STOP_TIME: string;
  EPHEMERIS_DATA: [EphemerisDataPoint];
}

table EphemerisDataPoint {
  EPOCH: string;           // Time stamp
  X: double;               // Position X (km)
  Y: double;               // Position Y (km)
  Z: double;               // Position Z (km)
  X_DOT: double;           // Velocity X (km/s)
  Y_DOT: double;           // Velocity Y (km/s)
  Z_DOT: double;           // Velocity Z (km/s)
}
```

### Example

```typescript
const oem = {
  CCSDS_OEM_VERS: "3.0",
  OBJECT_NAME: "ISS (ZARYA)",
  START_TIME: "2024-01-15T00:00:00.000Z",
  STOP_TIME: "2024-01-15T12:00:00.000Z",
  EPHEMERIS_DATA: [
    {
      EPOCH: "2024-01-15T00:00:00.000Z",
      X: 6678.137,
      Y: 0.0,
      Z: 0.0,
      X_DOT: 0.0,
      Y_DOT: 7.725,
      Z_DOT: 0.0
    },
    // ... more points
  ]
};
```

---

## OSM - Orbit State Message

Single instantaneous state vector.

### Schema Definition

```flatbuffers
table OSM {
  // Header
  CCSDS_OSM_VERS: string;
  CREATION_DATE: string;
  ORIGINATOR: string;

  // Object
  OBJECT_NAME: string;
  OBJECT_ID: string;
  CENTER_NAME: string;
  REF_FRAME: string;
  TIME_SYSTEM: string;

  // State
  EPOCH: string;
  X: double;               // km
  Y: double;               // km
  Z: double;               // km
  X_DOT: double;           // km/s
  Y_DOT: double;           // km/s
  Z_DOT: double;           // km/s
}
```

---

## OCM - Orbit Comprehensive Message

Complete orbit characterization including covariance and maneuvers.

### Schema Definition

```flatbuffers
table OCM {
  // Header
  CCSDS_OCM_VERS: string;
  CREATION_DATE: string;
  ORIGINATOR: string;

  // Metadata
  OBJECT_NAME: string;
  OBJECT_ID: string;

  // Multiple representations
  ORBIT_STATE: OrbitState;
  ORBIT_MEAN_ELEMENTS: OrbitMeanElements;
  COVARIANCE: Covariance;
  MANEUVERS: [Maneuver];
  PERTURBATIONS: Perturbations;
}
```

---

## Usage Patterns

### Subscribe to Orbital Data

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Mean elements (most common)
node.subscribe('OMM', (omm, peerId) => {
  console.log('OMM:', omm.OBJECT_NAME, omm.EPOCH);
});

// Ephemeris data
node.subscribe('OEM', (oem, peerId) => {
  console.log('OEM:', oem.OBJECT_NAME,
    oem.EPHEMERIS_DATA.length, 'points');
});

// State vectors
node.subscribe('OSM', (osm, peerId) => {
  console.log('OSM:', osm.OBJECT_NAME,
    `[${osm.X}, ${osm.Y}, ${osm.Z}]`);
});
```

### Propagate Orbits

```typescript
import { propagate } from '@spacedatanetwork/sdn-js';

const omm = await node.query('OMM', {
  where: { NORAD_CAT_ID: 25544 },
  limit: 1
});

// Propagate to future time
const future = new Date(Date.now() + 3600000); // +1 hour
const state = propagate(omm[0], future);

console.log('Position:', state.position);
console.log('Velocity:', state.velocity);
```

### Compare Elements

```typescript
// Get historical data for same object
const history = await node.query('OMM', {
  where: { NORAD_CAT_ID: 25544 },
  orderBy: 'EPOCH',
  order: 'desc',
  limit: 10
});

// Analyze element drift
for (let i = 1; i < history.length; i++) {
  const delta = {
    inclination: history[i-1].INCLINATION - history[i].INCLINATION,
    raan: history[i-1].RA_OF_ASC_NODE - history[i].RA_OF_ASC_NODE,
    eccentricity: history[i-1].ECCENTRICITY - history[i].ECCENTRICITY
  };
  console.log(`Change from ${history[i].EPOCH}:`, delta);
}
```

## See Also

- [Schema Overview](/reference/schemas)
- [Conjunction Data](/reference/conjunction)
- [Data Operations](/guide/js-data)
