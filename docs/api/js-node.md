# JavaScript SDK - SDNNode

The `SDNNode` class is the main entry point for the Space Data Network JavaScript SDK.

## Installation

```bash
npm install @spacedatanetwork/sdn-js
```

## Import

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

// Or CommonJS
const { SDNNode } = require('@spacedatanetwork/sdn-js');
```

## Constructor

```typescript
new SDNNode(options?: SDNNodeOptions)
```

### Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `bootstrapPeers` | `string[]` | Built-in list | Bootstrap peer multiaddrs |
| `enableRelay` | `boolean` | `true` | Enable circuit relay |
| `enablePubSub` | `boolean` | `true` | Enable GossipSub |
| `storageBackend` | `'indexeddb' \| 'memory'` | `'indexeddb'` | Storage backend |
| `schemas` | `string[]` | All schemas | Schemas to subscribe to |

### Example

```typescript
const node = new SDNNode({
  bootstrapPeers: [
    '/ip4/209.182.234.97/tcp/8080/ws/p2p/12D3KooW...',
  ],
  enableRelay: true,
  schemas: ['OMM', 'CDM', 'EPM'],
});
```

## Methods

### start()

Start the node and connect to the network.

```typescript
async start(): Promise<void>
```

**Example:**

```typescript
const node = new SDNNode();
await node.start();
console.log('Node started:', node.peerId);
```

### stop()

Stop the node and disconnect from the network.

```typescript
async stop(): Promise<void>
```

**Example:**

```typescript
await node.stop();
console.log('Node stopped');
```

### subscribe()

Subscribe to a data stream.

```typescript
subscribe(
  schema: string,
  handler: (data: any, peerId: string) => void,
  options?: SubscribeOptions
): () => void
```

**Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `schema` | `string` | Schema name (e.g., 'OMM') |
| `handler` | `function` | Callback for received data |
| `options` | `SubscribeOptions` | Optional subscription options |

**SubscribeOptions:**

| Option | Type | Description |
|--------|------|-------------|
| `filter` | `(data: any) => boolean` | Filter function |
| `transform` | `(data: any) => any` | Transform function |

**Returns:** Unsubscribe function

**Example:**

```typescript
// Subscribe to all OMM messages
const unsubscribe = node.subscribe('OMM', (omm, peerId) => {
  console.log(`Received OMM from ${peerId}:`, omm.OBJECT_NAME);
});

// With filter
const unsubscribe = node.subscribe('OMM', handler, {
  filter: (omm) => omm.INCLINATION > 50,
});

// Unsubscribe later
unsubscribe();
```

### publish()

Publish data to the network.

```typescript
async publish(schema: string, data: any): Promise<string>
```

**Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `schema` | `string` | Schema name |
| `data` | `any` | Data object (will be serialized to FlatBuffer) |

**Returns:** CID of published data

**Example:**

```typescript
const ommData = {
  OBJECT_NAME: 'MY-SAT-1',
  OBJECT_ID: '2024-001A',
  EPOCH: '2024-01-15T12:00:00.000Z',
  MEAN_MOTION: 15.0,
  ECCENTRICITY: 0.001,
  INCLINATION: 98.0,
  RA_OF_ASC_NODE: 45.0,
  ARG_OF_PERICENTER: 90.0,
  MEAN_ANOMALY: 180.0,
};

const cid = await node.publish('OMM', ommData);
console.log('Published:', cid);
```

### get()

Retrieve data by CID.

```typescript
async get(schema: string, cid: string): Promise<any>
```

**Example:**

```typescript
const omm = await node.get('OMM', 'abc123...');
console.log('Retrieved:', omm.OBJECT_NAME);
```

### query()

Query local storage.

```typescript
async query(schema: string, options?: QueryOptions): Promise<any[]>
```

**QueryOptions:**

| Option | Type | Description |
|--------|------|-------------|
| `filter` | `(data: any) => boolean` | Filter function |
| `limit` | `number` | Max results |
| `offset` | `number` | Skip first N results |
| `orderBy` | `string` | Field to sort by |
| `order` | `'asc' \| 'desc'` | Sort order |

**Example:**

```typescript
// Get all OMM records
const allOMM = await node.query('OMM');

// Filter by inclination
const highInc = await node.query('OMM', {
  filter: (omm) => omm.INCLINATION > 80,
  limit: 100,
});

// Sort by epoch
const recent = await node.query('CDM', {
  orderBy: 'TCA',
  order: 'desc',
  limit: 10,
});
```

### request()

Request data from a specific peer.

```typescript
async request(peerId: string, schema: string, cid: string): Promise<any>
```

**Example:**

```typescript
const omm = await node.request('12D3KooW...', 'OMM', 'abc123...');
```

### getPeers()

Get list of connected peers.

```typescript
getPeers(): string[]
```

**Example:**

```typescript
const peers = node.getPeers();
console.log(`Connected to ${peers.length} peers`);
```

### on()

Subscribe to node events.

```typescript
on(event: string, handler: (...args: any[]) => void): void
```

**Events:**

| Event | Arguments | Description |
|-------|-----------|-------------|
| `peer:connect` | `peerId: string` | Peer connected |
| `peer:disconnect` | `peerId: string` | Peer disconnected |
| `data:received` | `schema: string, cid: string` | Data received |
| `error` | `error: Error` | Error occurred |

**Example:**

```typescript
node.on('peer:connect', (peerId) => {
  console.log('Connected to:', peerId);
});

node.on('error', (error) => {
  console.error('Node error:', error);
});
```

## Properties

### peerId

The node's peer ID.

```typescript
readonly peerId: string
```

### isRunning

Whether the node is running.

```typescript
readonly isRunning: boolean
```

### connectedPeers

Number of connected peers.

```typescript
readonly connectedPeers: number
```

## Complete Example

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

async function main() {
  // Create node
  const node = new SDNNode({
    schemas: ['OMM', 'CDM'],
  });

  // Subscribe to events
  node.on('peer:connect', (peerId) => {
    console.log(`Connected to ${peerId}`);
  });

  // Start the node
  await node.start();
  console.log(`Node started: ${node.peerId}`);

  // Subscribe to OMM messages
  const unsubscribe = node.subscribe('OMM', (omm, peerId) => {
    console.log(`Received OMM from ${peerId}:`);
    console.log(`  Object: ${omm.OBJECT_NAME}`);
    console.log(`  Epoch: ${omm.EPOCH}`);
  });

  // Publish some data
  const myOMM = {
    OBJECT_NAME: 'MY-SAT',
    OBJECT_ID: '2024-001A',
    EPOCH: new Date().toISOString(),
    MEAN_MOTION: 15.0,
    ECCENTRICITY: 0.001,
    INCLINATION: 98.0,
    RA_OF_ASC_NODE: 45.0,
    ARG_OF_PERICENTER: 90.0,
    MEAN_ANOMALY: 180.0,
  };

  const cid = await node.publish('OMM', myOMM);
  console.log(`Published OMM: ${cid}`);

  // Query local data
  const results = await node.query('OMM', { limit: 10 });
  console.log(`Found ${results.length} OMM records`);

  // Keep running...
  // To stop: await node.stop();
}

main().catch(console.error);
```

## TypeScript Types

```typescript
interface SDNNodeOptions {
  bootstrapPeers?: string[];
  enableRelay?: boolean;
  enablePubSub?: boolean;
  storageBackend?: 'indexeddb' | 'memory';
  schemas?: string[];
}

interface SubscribeOptions {
  filter?: (data: any) => boolean;
  transform?: (data: any) => any;
}

interface QueryOptions {
  filter?: (data: any) => boolean;
  limit?: number;
  offset?: number;
  orderBy?: string;
  order?: 'asc' | 'desc';
}
```

## See Also

- [Storage API](/api/js-storage) - Low-level storage operations
- [Schemas API](/api/js-schemas) - Schema validation and serialization
- [Crypto API](/api/js-crypto) - Cryptographic operations
