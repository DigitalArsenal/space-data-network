# Schemas API

The Schemas API provides access to Space Data Standards schema definitions and validation.

## Overview

```typescript
import { SDNNode, SchemaRegistry } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Access schemas through node
const schemas = node.schemas;

// Or use SchemaRegistry directly
const registry = SchemaRegistry;
```

## SchemaRegistry

### list()

List all supported schema types.

```typescript
const schemas = SchemaRegistry.list();
// ['OMM', 'CDM', 'EPM', 'TDM', 'OEM', ...]
```

### get(name)

Get schema definition.

```typescript
const ommSchema = SchemaRegistry.get('OMM');

console.log(ommSchema.name);        // 'OMM'
console.log(ommSchema.description); // 'Orbital Mean-Elements Message'
console.log(ommSchema.fields);      // Array of field definitions
```

### getFields(name)

Get schema field definitions.

```typescript
const fields = SchemaRegistry.getFields('OMM');

for (const field of fields) {
  console.log(field.name);     // 'EPOCH'
  console.log(field.type);     // 'string'
  console.log(field.required); // true
}
```

## Validation

### validate(schema, data)

Validate data against a schema.

```typescript
import { validateSchema } from '@spacedatanetwork/sdn-js';

const result = validateSchema('OMM', ommData);

if (result.valid) {
  console.log('Data is valid');
} else {
  console.log('Errors:', result.errors);
}
```

### Validation Result

```typescript
interface ValidationResult {
  valid: boolean;
  errors: ValidationError[];
}

interface ValidationError {
  field: string;
  message: string;
  value?: any;
}
```

### Example

```typescript
const omm = {
  OBJECT_NAME: 'ISS',
  EPOCH: 'invalid-date' // Invalid
};

const result = validateSchema('OMM', omm);

// result.valid === false
// result.errors[0] === {
//   field: 'EPOCH',
//   message: 'Invalid ISO 8601 date format',
//   value: 'invalid-date'
// }
```

## Type Definitions

### TypeScript Types

```typescript
import type { OMM, CDM, EPM, TDM } from '@spacedatanetwork/sdn-js';

const omm: OMM = {
  OBJECT_NAME: 'ISS (ZARYA)',
  NORAD_CAT_ID: 25544,
  EPOCH: '2024-01-15T12:00:00.000Z',
  MEAN_MOTION: 15.5,
  ECCENTRICITY: 0.0001,
  INCLINATION: 51.64,
  RA_OF_ASC_NODE: 123.45,
  ARG_OF_PERICENTER: 67.89,
  MEAN_ANOMALY: 234.56
};
```

### Schema Categories

```typescript
// Orbital schemas
import type { OMM, OEM, OCM, OSM } from '@spacedatanetwork/sdn-js';

// Conjunction schemas
import type { CDM, CSM, CRM } from '@spacedatanetwork/sdn-js';

// Tracking schemas
import type { TDM, RFM, CTR } from '@spacedatanetwork/sdn-js';

// Entity schemas
import type { EPM, PNM, CAT, SIT } from '@spacedatanetwork/sdn-js';
```

## Schema Conversion

### TLE to OMM

```typescript
import { TLEParser } from '@spacedatanetwork/sdn-js';

const tle = `ISS (ZARYA)
1 25544U 98067A   24015.50000000  .00016717  00000-0  10270-3 0  9993
2 25544  51.6400 100.0000 0001234  90.0000 270.0000 15.50000000123456`;

const omm = TLEParser.toOMM(tle);
```

### OMM to TLE

```typescript
import { TLEFormatter } from '@spacedatanetwork/sdn-js';

const tle = TLEFormatter.fromOMM(omm);
```

## FlatBuffers

### Serialize

```typescript
import { serialize } from '@spacedatanetwork/sdn-js';

const bytes = serialize('OMM', ommData);
```

### Deserialize

```typescript
import { deserialize } from '@spacedatanetwork/sdn-js';

const omm = deserialize('OMM', bytes);
```

## See Also

- [Schema Reference](/reference/schemas)
- [SDNNode API](/api/js-node)
- [Data Operations](/guide/js-data)
