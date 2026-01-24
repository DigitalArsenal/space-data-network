# PubSub Topics Reference

SDN uses libp2p GossipSub for real-time data distribution. This reference documents the topic structure and subscription patterns.

## Topic Structure

### Topic Format

```
/spacedatanetwork/{network_id}/{schema}
```

Components:
- **network_id**: Network identifier hash (mainnet, testnet, or custom)
- **schema**: Space Data Standards schema name

### Standard Topics

| Topic | Description |
|-------|-------------|
| `/spacedatanetwork/mainnet/OMM` | Orbital Mean-Elements Messages |
| `/spacedatanetwork/mainnet/CDM` | Conjunction Data Messages |
| `/spacedatanetwork/mainnet/EPM` | Entity Profile Manifests |
| `/spacedatanetwork/mainnet/TDM` | Tracking Data Messages |
| `/spacedatanetwork/mainnet/OEM` | Orbital Ephemeris Messages |
| `/spacedatanetwork/mainnet/OSM` | Orbital State Messages |
| `/spacedatanetwork/mainnet/CAT` | Catalog Entries |
| `/spacedatanetwork/mainnet/SIT` | Site Messages |
| `/spacedatanetwork/mainnet/PNM` | Peer Node Messages |
| `/spacedatanetwork/mainnet/CTR` | Contact Reports |
| `/spacedatanetwork/mainnet/RFM` | RF Metrics Messages |

### All 32 Schema Topics

```
OMM, OEM, OCM, OSM          # Orbital data
CDM, CSM, CRM               # Conjunction data
TDM, RFM, CTR               # Tracking data
EPM, PNM, CAT, SIT          # Entity data
MET, MPE                    # Maneuver data
HYP, EME, EOO, EOP          # Propagation data
LCC, LDM                    # Launch data
ATM, BOV, IDM, PLD          # Other standards
PRG, REC, ROC, SCM, TIM, VCM
```

## Subscribing

### Subscribe to Single Schema

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Subscribe to OMM
node.subscribe('OMM', (omm, peerId) => {
  console.log('OMM from', peerId);
  console.log('Object:', omm.OBJECT_NAME);
});
```

### Subscribe to Multiple Schemas

```typescript
// Subscribe to multiple schemas
const schemas = ['OMM', 'CDM', 'EPM'];

for (const schema of schemas) {
  node.subscribe(schema, (data, peerId) => {
    console.log(`${schema} from ${peerId}:`, data);
  });
}
```

### Subscribe to All Schemas

```typescript
// Subscribe to all available schemas
const allSchemas = node.schemas.list();

for (const schema of allSchemas) {
  node.subscribe(schema, (data, peerId) => {
    handleData(schema, data, peerId);
  });
}
```

### Filtered Subscriptions

```typescript
// Filter at application level
node.subscribe('OMM', (omm, peerId) => {
  // Only process LEO satellites
  if (omm.MEAN_MOTION > 11.25) {
    processLEO(omm);
  }
}, {
  filter: (omm) => omm.MEAN_MOTION > 11.25
});

// Filter by source
node.subscribe('CDM', (cdm, peerId) => {
  processCDM(cdm);
}, {
  filter: (cdm, peerId) => trustedPeers.has(peerId)
});
```

## Publishing

### Publish to Topic

```typescript
// Publish OMM data
await node.publish('OMM', {
  OBJECT_NAME: 'MY-SAT-1',
  OBJECT_ID: '2024-001A',
  EPOCH: new Date().toISOString(),
  // ... other fields
});

// Publish CDM
await node.publish('CDM', cdmData);
```

### Batch Publishing

```typescript
// Publish multiple records
const records = [...];

for (const record of records) {
  await node.publish('OMM', record);
}

// Or use batch method
await node.publishBatch('OMM', records);
```

## GossipSub Configuration

### Default Parameters

| Parameter | Value | Description |
|-----------|-------|-------------|
| Heartbeat interval | 1s | Mesh maintenance frequency |
| History length | 5 | Messages kept for gossip |
| History gossip | 3 | Peers to gossip history |
| D (mesh degree) | 6 | Target mesh peers |
| D_lo | 4 | Minimum mesh peers |
| D_hi | 12 | Maximum mesh peers |
| D_lazy | 6 | Lazy push peers |

### Custom Configuration

```typescript
const node = new SDNNode({
  pubsub: {
    heartbeatInterval: 500,
    historyLength: 10,
    historyGossip: 5,
    floodPublish: false
  }
});
```

## Message Format

### Published Message Structure

```flatbuffers
table PubSubMessage {
  // Header
  topic: string;                   // Topic name
  message_id: string;              // Unique message ID
  timestamp: string;               // ISO 8601 timestamp

  // Sender
  from: string;                    // Peer ID
  seqno: [uint8];                  // Sequence number

  // Payload
  schema: string;                  // Data schema
  data: [uint8];                   // FlatBuffers payload

  // Signature
  signature: [uint8];              // Ed25519 signature
  key: [uint8];                    // Public key
}
```

### Message Validation

Messages are validated before delivery:

1. **Signature verification** - Valid Ed25519 signature
2. **Schema validation** - Payload matches declared schema
3. **Timestamp check** - Not too old or future-dated
4. **Deduplication** - Not previously received

## Topic Discovery

### Discover Active Topics

```typescript
// Get topics with active publishers
const activeTopics = await node.pubsub.getTopics();

console.log('Active topics:', activeTopics);
// ['/spacedatanetwork/mainnet/OMM', '/spacedatanetwork/mainnet/CDM', ...]
```

### Discover Peers on Topic

```typescript
// Get peers subscribed to OMM
const ommPeers = await node.pubsub.getPeers('OMM');

console.log('OMM subscribers:', ommPeers.length);
```

## Network Isolation

### Testnet Topics

```typescript
// Connect to testnet
const node = new SDNNode({
  network: 'testnet'
});

await node.start();

// Subscribes to /spacedatanetwork/testnet/OMM
node.subscribe('OMM', handler);
```

### Custom Network

```typescript
// Create isolated network
const node = new SDNNode({
  network: 'my-private-network'
});

// Uses /spacedatanetwork/my-private-network/OMM
```

## Message Priority

### Priority Levels

Some schemas have higher priority:

| Priority | Schemas | Rationale |
|----------|---------|-----------|
| Critical | CDM, CRM | Safety-critical data |
| High | OMM, TDM | Operational data |
| Normal | EPM, CAT, SIT | Metadata |
| Low | PNM | Node announcements |

### Priority Handling

```typescript
// High-priority publish
await node.publish('CDM', cdmData, {
  priority: 'critical'
});
```

## Metrics

### Topic Metrics

```typescript
// Get topic statistics
const stats = await node.pubsub.stats();

console.log('Messages published:', stats.messagesPublished);
console.log('Messages received:', stats.messagesReceived);
console.log('Bytes sent:', stats.bytesSent);
console.log('Bytes received:', stats.bytesReceived);
```

### Per-Topic Metrics

```typescript
// Get OMM-specific stats
const ommStats = await node.pubsub.topicStats('OMM');

console.log('OMM messages:', ommStats.messages);
console.log('OMM peers:', ommStats.peers);
console.log('OMM rate:', ommStats.messagesPerSecond);
```

## Best Practices

### Subscription Management

```typescript
// Store unsubscribe functions
const subscriptions = new Map();

function subscribe(schema: string, handler: Function) {
  const unsub = node.subscribe(schema, handler);
  subscriptions.set(schema, unsub);
}

function unsubscribe(schema: string) {
  const unsub = subscriptions.get(schema);
  if (unsub) {
    unsub();
    subscriptions.delete(schema);
  }
}

// Cleanup on shutdown
async function shutdown() {
  for (const unsub of subscriptions.values()) {
    unsub();
  }
  await node.stop();
}
```

### Error Handling

```typescript
node.subscribe('OMM', (omm, peerId) => {
  try {
    processOMM(omm);
  } catch (error) {
    console.error('Error processing OMM:', error);
    // Don't rethrow - continue receiving
  }
});

// Handle connection errors
node.on('pubsub:error', (error) => {
  console.error('PubSub error:', error);
});
```

### Rate Limiting

```typescript
import { RateLimiter } from '@spacedatanetwork/sdn-js';

const limiter = new RateLimiter({
  maxPerSecond: 10
});

node.subscribe('OMM', async (omm, peerId) => {
  await limiter.wait();
  processOMM(omm);
});
```

## See Also

- [SDS Exchange Protocol](/reference/protocol-sds)
- [Schema Reference](/reference/schemas)
- [Data Operations](/guide/js-data)
