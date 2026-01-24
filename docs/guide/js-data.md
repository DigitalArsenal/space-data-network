# Data Operations

Work with space data using the SDN JavaScript SDK - querying, transforming, and analyzing orbital data.

## Space Data Standards

SDN uses the Space Data Standards (SDS) schemas. The most common schemas are:

| Schema | Full Name | Description |
|--------|-----------|-------------|
| OMM | Orbital Mean-Elements Message | Keplerian orbital elements |
| CDM | Conjunction Data Message | Close approach warnings |
| EPM | Entity Profile Manifest | Organization/operator info |
| TDM | Tracking Data Message | Ground station observations |
| OEM | Orbital Ephemeris Message | State vectors over time |
| ROC | Re-entry Object Collection | Debris and decay data |

## Subscribing to Data

### Basic Subscription

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Subscribe to all OMM messages
const unsub = node.subscribe('OMM', (omm, peerId) => {
  console.log('Object:', omm.OBJECT_NAME);
  console.log('NORAD ID:', omm.NORAD_CAT_ID);
  console.log('Epoch:', omm.EPOCH);
  console.log('From peer:', peerId);
});

// Later, unsubscribe
unsub();
```

### Typed Subscriptions

```typescript
import { SDNNode, OMM, CDM } from '@spacedatanetwork/sdn-js';

node.subscribe<OMM>('OMM', (omm) => {
  // Full TypeScript support
  const meanMotion: number = omm.MEAN_MOTION;
  const eccentricity: number = omm.ECCENTRICITY;
});

node.subscribe<CDM>('CDM', (cdm) => {
  const tca: string = cdm.TCA; // Time of Closest Approach
  const missDistance: number = cdm.MISS_DISTANCE;
  const probability: number = cdm.COLLISION_PROBABILITY;
});
```

### Filtered Subscriptions

```typescript
// Only LEO satellites (period < 128 minutes)
node.subscribe('OMM', (omm) => {
  console.log('LEO satellite:', omm.OBJECT_NAME);
}, {
  filter: (omm) => omm.MEAN_MOTION > 11.25
});

// Only high-probability conjunctions
node.subscribe('CDM', (cdm) => {
  console.log('CRITICAL:', cdm.OBJECT1_OBJECT_DESIGNATOR);
}, {
  filter: (cdm) => cdm.COLLISION_PROBABILITY > 0.001
});

// Only from trusted publishers
const trustedPeers = new Set(['12D3KooW...', '12D3KooW...']);

node.subscribe('OMM', (omm, peerId) => {
  console.log('Trusted data:', omm);
}, {
  filter: (_, peerId) => trustedPeers.has(peerId)
});
```

## Querying Data

### Basic Queries

```typescript
// Get recent OMMs
const recent = await node.query('OMM', {
  limit: 100,
  orderBy: 'EPOCH',
  order: 'desc'
});

// Search by object name
const iss = await node.query('OMM', {
  where: { OBJECT_NAME: 'ISS (ZARYA)' }
});

// Search by NORAD ID
const sat = await node.query('OMM', {
  where: { NORAD_CAT_ID: 25544 }
});
```

### Advanced Queries

```typescript
// Multiple conditions (AND)
const leoSats = await node.query('OMM', {
  where: {
    MEAN_MOTION: { $gte: 11.25 },          // LEO
    ECCENTRICITY: { $lt: 0.1 },             // Near-circular
    INCLINATION: { $gte: 50, $lte: 60 }     // Mid-inclination
  },
  limit: 1000
});

// OR conditions
const importantSats = await node.query('OMM', {
  where: {
    $or: [
      { OBJECT_NAME: { $contains: 'ISS' } },
      { OBJECT_NAME: { $contains: 'STARLINK' } },
      { OBJECT_NAME: { $contains: 'ONEWEB' } }
    ]
  }
});

// Date range
const lastWeek = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);

const recentData = await node.query('OMM', {
  where: {
    EPOCH: { $gte: lastWeek.toISOString() }
  },
  orderBy: 'EPOCH',
  order: 'desc'
});
```

### Pagination

```typescript
// Page through results
async function* paginateOMM(pageSize: number = 100) {
  let offset = 0;

  while (true) {
    const page = await node.query('OMM', {
      limit: pageSize,
      offset,
      orderBy: 'EPOCH',
      order: 'desc'
    });

    if (page.length === 0) break;

    yield page;
    offset += pageSize;
  }
}

// Usage
for await (const page of paginateOMM(100)) {
  for (const omm of page) {
    processOMM(omm);
  }
}
```

### Streaming

```typescript
// Stream large result sets
for await (const omm of node.stream('OMM', { limit: 100000 })) {
  // Process one at a time - memory efficient
  await processOMM(omm);
}
```

## Publishing Data

### Publish Single Record

```typescript
const result = await node.publish('OMM', {
  OBJECT_NAME: 'MY-SAT-1',
  OBJECT_ID: '2024-001A',
  CENTER_NAME: 'EARTH',
  REF_FRAME: 'TEME',
  TIME_SYSTEM: 'UTC',
  MEAN_ELEMENT_THEORY: 'SGP4',
  EPOCH: new Date().toISOString(),
  MEAN_MOTION: 15.0,
  ECCENTRICITY: 0.0001,
  INCLINATION: 51.6,
  RA_OF_ASC_NODE: 123.45,
  ARG_OF_PERICENTER: 67.89,
  MEAN_ANOMALY: 234.56,
  BSTAR: 0.0001,
  MEAN_MOTION_DOT: 0.00000001,
  MEAN_MOTION_DDOT: 0.0
});

console.log('Message ID:', result.messageId);
console.log('Signature:', result.signature);
```

### Batch Publishing

```typescript
// Publish multiple records efficiently
const ommRecords = [...]; // Array of OMM data

const results = await node.publishBatch('OMM', ommRecords);

console.log(`Published ${results.length} records`);
```

### With Validation

```typescript
import { validateOMM } from '@spacedatanetwork/sdn-js';

const omm = {
  OBJECT_NAME: 'TEST-SAT',
  EPOCH: 'invalid-date' // This will fail validation
};

const validation = validateOMM(omm);

if (validation.valid) {
  await node.publish('OMM', omm);
} else {
  console.error('Validation errors:', validation.errors);
}
```

## Data Transformation

### TLE to OMM

```typescript
import { TLEParser } from '@spacedatanetwork/sdn-js';

const tle = `ISS (ZARYA)
1 25544U 98067A   24015.50000000  .00016717  00000-0  10270-3 0  9993
2 25544  51.6400 100.0000 0001234  90.0000 270.0000 15.50000000123456`;

const omm = TLEParser.toOMM(tle);

console.log(omm);
// {
//   OBJECT_NAME: 'ISS (ZARYA)',
//   NORAD_CAT_ID: 25544,
//   EPOCH: '2024-01-15T12:00:00.000Z',
//   MEAN_MOTION: 15.5,
//   ...
// }
```

### Parse TLE File

```typescript
import { TLEParser } from '@spacedatanetwork/sdn-js';
import fs from 'fs/promises';

const content = await fs.readFile('active.txt', 'utf-8');
const records = TLEParser.parseFile(content);

for (const omm of records) {
  await node.publish('OMM', omm);
}
```

### OMM to TLE

```typescript
import { TLEFormatter } from '@spacedatanetwork/sdn-js';

const omm = await node.query('OMM', {
  where: { NORAD_CAT_ID: 25544 },
  limit: 1
});

const tle = TLEFormatter.fromOMM(omm[0]);
console.log(tle);
// ISS (ZARYA)
// 1 25544U 98067A   ...
// 2 25544  51.6400 ...
```

## Orbital Computations

### SGP4 Propagation

```typescript
import { propagate } from '@spacedatanetwork/sdn-js';

const omm = await node.query('OMM', {
  where: { NORAD_CAT_ID: 25544 },
  limit: 1
});

// Propagate to current time
const now = new Date();
const state = propagate(omm[0], now);

console.log('Position (km):', state.position);
console.log('Velocity (km/s):', state.velocity);
console.log('Latitude:', state.geodetic.latitude);
console.log('Longitude:', state.geodetic.longitude);
console.log('Altitude:', state.geodetic.altitude);
```

### Ground Track

```typescript
import { propagate } from '@spacedatanetwork/sdn-js';

// Generate ground track for next 90 minutes
const points = [];
const start = new Date();

for (let i = 0; i <= 90; i++) {
  const time = new Date(start.getTime() + i * 60000);
  const state = propagate(omm, time);

  points.push({
    time: time.toISOString(),
    lat: state.geodetic.latitude,
    lon: state.geodetic.longitude,
    alt: state.geodetic.altitude
  });
}
```

### Visibility Windows

```typescript
import { findPasses } from '@spacedatanetwork/sdn-js';

const groundStation = {
  latitude: 40.7128,   // New York
  longitude: -74.0060,
  altitude: 0
};

const passes = findPasses(omm, groundStation, {
  start: new Date(),
  end: new Date(Date.now() + 24 * 60 * 60 * 1000), // Next 24 hours
  minElevation: 10  // Minimum 10 degrees above horizon
});

for (const pass of passes) {
  console.log('AOS:', pass.aos);
  console.log('TCA:', pass.tca);
  console.log('LOS:', pass.los);
  console.log('Max Elevation:', pass.maxElevation);
}
```

## Conjunction Analysis

### Find Close Approaches

```typescript
// Get recent conjunction warnings
const cdms = await node.query('CDM', {
  where: {
    COLLISION_PROBABILITY: { $gt: 0.0001 }
  },
  orderBy: 'TCA',
  order: 'asc'
});

for (const cdm of cdms) {
  console.log('---');
  console.log('Object 1:', cdm.OBJECT1_OBJECT_DESIGNATOR);
  console.log('Object 2:', cdm.OBJECT2_OBJECT_DESIGNATOR);
  console.log('TCA:', cdm.TCA);
  console.log('Miss Distance:', cdm.MISS_DISTANCE, 'km');
  console.log('Probability:', cdm.COLLISION_PROBABILITY);
}
```

### Compute Conjunction

```typescript
import { findConjunctions } from '@spacedatanetwork/sdn-js';

const primary = await node.query('OMM', {
  where: { NORAD_CAT_ID: 25544 }
});

const catalog = await node.query('OMM', { limit: -1 });

const conjunctions = findConjunctions(primary[0], catalog, {
  timeSpan: 7 * 24 * 60 * 60 * 1000, // 7 days
  threshold: 10 // km
});

for (const conj of conjunctions) {
  console.log('Secondary:', conj.secondary.OBJECT_NAME);
  console.log('Time:', conj.tca);
  console.log('Distance:', conj.missDistance, 'km');
}
```

## Data Export

### JSON Export

```typescript
const data = await node.query('OMM', { limit: -1 });

// Pretty print
await fs.writeFile('omm.json', JSON.stringify(data, null, 2));

// Compact
await fs.writeFile('omm.min.json', JSON.stringify(data));
```

### CSV Export

```typescript
import { stringify } from 'csv-stringify/sync';

const data = await node.query('OMM', { limit: -1 });

const csv = stringify(data, {
  header: true,
  columns: [
    'OBJECT_NAME',
    'NORAD_CAT_ID',
    'EPOCH',
    'MEAN_MOTION',
    'ECCENTRICITY',
    'INCLINATION',
    'RA_OF_ASC_NODE',
    'ARG_OF_PERICENTER',
    'MEAN_ANOMALY'
  ]
});

await fs.writeFile('omm.csv', csv);
```

### FlatBuffers Binary

```typescript
// Export as FlatBuffers binary (native format)
const binary = await node.storage.exportBinary('OMM');
await fs.writeFile('omm.sds', binary);

// Import binary
const data = await fs.readFile('omm.sds');
await node.storage.importBinary('OMM', data);
```

## Next Steps

- [Schema Reference](/reference/schemas) - All SDS schemas
- [Data Ingestion](/guide/ingestion-overview) - Build pipelines
- [API Reference](/api/js-node) - Complete API docs
