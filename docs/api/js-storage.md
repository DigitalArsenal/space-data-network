# Storage API

The Storage API provides persistent data storage with encryption capabilities.

## Overview

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Access storage
const storage = node.storage;
```

## Methods

### store(schema, data)

Store data by schema type.

```typescript
await storage.store('OMM', ommData);
await storage.store('CDM', cdmData);
```

**Parameters:**
- `schema` - Schema type (e.g., 'OMM', 'CDM')
- `data` - Data object matching the schema

**Returns:** `Promise<string>` - Content ID (CID)

### get(schema, cid)

Retrieve data by CID.

```typescript
const omm = await storage.get('OMM', cid);
```

### query(schema, options)

Query stored data.

```typescript
const results = await storage.query('OMM', {
  where: { NORAD_CAT_ID: 25544 },
  limit: 10,
  orderBy: 'EPOCH',
  order: 'desc'
});
```

### delete(schema, cid)

Delete data by CID.

```typescript
await storage.delete('OMM', cid);
```

### clear(schema)

Clear all data for a schema.

```typescript
await storage.clear('OMM');
```

## Encryption

### Enable Encryption

```typescript
const node = new SDNNode({
  storage: {
    encrypted: true,
    password: 'your-secure-password'
  }
});
```

### Encryption Methods

```typescript
// Encrypt specific data
const encrypted = await storage.encrypt(data, password);

// Decrypt data
const decrypted = await storage.decrypt(encrypted, password);
```

## Export/Import

### Export Data

```typescript
// Export as JSON
const json = await storage.export('OMM', { format: 'json' });

// Export as binary (FlatBuffers)
const binary = await storage.export('OMM', { format: 'binary' });
```

### Import Data

```typescript
// Import JSON
await storage.import('OMM', jsonData, { format: 'json' });

// Import binary
await storage.import('OMM', binaryData, { format: 'binary' });
```

## Statistics

```typescript
const stats = await storage.stats();

console.log('Total records:', stats.totalRecords);
console.log('Storage size:', stats.sizeBytes);
console.log('Schemas:', stats.schemas);
```

## See Also

- [SDNNode API](/api/js-node)
- [Schemas API](/api/js-schemas)
- [Data Operations](/guide/js-data)
