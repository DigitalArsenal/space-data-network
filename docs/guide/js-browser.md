# Browser Usage

Build browser applications with the Space Data Network JavaScript SDK.

## Overview

Browser nodes connect to the SDN network through edge relays using WebSocket or WebRTC. They can:

- Subscribe to real-time space data
- Publish data to the network
- Query historical data
- Verify data signatures

## Getting Started

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

console.log('Browser node started!');
console.log('Peer ID:', node.peerId);
```

## Configuration

### Relay Selection

Browsers connect through edge relays. By default, public relays are used:

```typescript
const node = new SDNNode({
  relays: [
    '/dns4/relay1.spacedatanetwork.org/tcp/443/wss/p2p/12D3KooW...',
    '/dns4/relay2.spacedatanetwork.org/tcp/443/wss/p2p/12D3KooW...',
  ]
});
```

### Storage Options

Choose how data is stored in the browser:

```typescript
// In-memory (default) - data lost on page refresh
const node = new SDNNode({
  storage: { type: 'memory' }
});

// IndexedDB - persistent storage
const node = new SDNNode({
  storage: {
    type: 'indexeddb',
    name: 'sdn-data'
  }
});

// LocalStorage - for small datasets
const node = new SDNNode({
  storage: {
    type: 'localstorage',
    prefix: 'sdn:'
  }
});
```

### Identity

Generate or restore cryptographic identity:

```typescript
// Auto-generate new identity
const node = new SDNNode();

// Restore from seed phrase
const node = new SDNNode({
  identity: {
    mnemonic: 'abandon abandon abandon...' // 12 or 24 words
  }
});

// Restore from private key
const node = new SDNNode({
  identity: {
    privateKey: 'base64-encoded-key'
  }
});
```

## Subscribing to Data

### Basic Subscription

```typescript
// Subscribe to Orbital Mean-Elements Messages
node.subscribe('OMM', (data, peerId) => {
  console.log('New OMM from', peerId);
  console.log('Object:', data.OBJECT_NAME);
  console.log('Epoch:', data.EPOCH);
});

// Subscribe to Conjunction Data Messages
node.subscribe('CDM', (data, peerId) => {
  console.log('Conjunction warning!');
  console.log('Object 1:', data.OBJECT1_OBJECT_DESIGNATOR);
  console.log('Object 2:', data.OBJECT2_OBJECT_DESIGNATOR);
  console.log('TCA:', data.TCA);
  console.log('Miss distance:', data.MISS_DISTANCE, 'km');
});
```

### Filtered Subscriptions

```typescript
// Only receive data for specific satellites
node.subscribe('OMM', (data, peerId) => {
  console.log('ISS update:', data);
}, {
  filter: (data) => data.OBJECT_NAME?.includes('ISS')
});

// Only high-probability conjunctions
node.subscribe('CDM', (data, peerId) => {
  console.log('Critical conjunction:', data);
}, {
  filter: (data) => data.COLLISION_PROBABILITY > 0.001
});
```

### Multiple Subscriptions

```typescript
// Subscribe to multiple schemas
const schemas = ['OMM', 'CDM', 'EPM', 'TDM'];

schemas.forEach(schema => {
  node.subscribe(schema, (data, peerId) => {
    console.log(`Received ${schema}:`, data);
  });
});
```

## Publishing Data

### Publish Space Data

```typescript
// Publish OMM
await node.publish('OMM', {
  OBJECT_NAME: 'MY-SAT-1',
  OBJECT_ID: '2024-001A',
  EPOCH: new Date().toISOString(),
  MEAN_MOTION: 15.0,
  ECCENTRICITY: 0.0001,
  INCLINATION: 51.6,
  RA_OF_ASC_NODE: 123.45,
  ARG_OF_PERICENTER: 67.89,
  MEAN_ANOMALY: 234.56,
});
```

### Signed Publishing

Data is automatically signed with your node's identity:

```typescript
const result = await node.publish('OMM', ommData);

console.log('Published with signature:', result.signature);
console.log('Message ID:', result.messageId);
```

### Batch Publishing

```typescript
const ommRecords = [...]; // Array of OMM data

// Publish all at once
await node.publishBatch('OMM', ommRecords);

// Or with progress
for (let i = 0; i < ommRecords.length; i++) {
  await node.publish('OMM', ommRecords[i]);
  updateProgress((i + 1) / ommRecords.length * 100);
}
```

## Querying Data

### Query Local Storage

```typescript
// Get recent OMMs
const recentOMMs = await node.query('OMM', {
  limit: 100,
  orderBy: 'EPOCH',
  order: 'desc'
});

// Search by object
const issData = await node.query('OMM', {
  where: { OBJECT_NAME: 'ISS (ZARYA)' }
});

// Date range query
const lastWeek = await node.query('OMM', {
  where: {
    EPOCH: {
      $gte: new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString()
    }
  }
});
```

### Stream Results

For large result sets, stream data to avoid memory issues:

```typescript
for await (const omm of node.stream('OMM', { limit: 10000 })) {
  processRecord(omm);
}
```

## Signature Verification

### Verify Data Origin

```typescript
node.subscribe('OMM', async (data, peerId, metadata) => {
  // Verify signature
  const isValid = await node.verify(data, metadata.signature, peerId);

  if (isValid) {
    console.log('Verified data from', peerId);
  } else {
    console.warn('Invalid signature!');
  }
});
```

### Trust Management

```typescript
// Add trusted publishers
node.trust.add('12D3KooWTrustedPeer...');

// Check if publisher is trusted
const isTrusted = node.trust.has(peerId);

// Only process data from trusted sources
node.subscribe('CDM', (data, peerId) => {
  if (node.trust.has(peerId)) {
    processConjunction(data);
  }
});
```

## UI Components

### Connection Status

```typescript
// React example
function ConnectionStatus() {
  const [status, setStatus] = useState('connecting');
  const [peers, setPeers] = useState(0);

  useEffect(() => {
    node.on('connected', () => setStatus('connected'));
    node.on('disconnected', () => setStatus('disconnected'));
    node.on('peer:connect', () => setPeers(p => p + 1));
    node.on('peer:disconnect', () => setPeers(p => p - 1));
  }, []);

  return (
    <div className={`status ${status}`}>
      {status === 'connected'
        ? `Connected (${peers} peers)`
        : 'Connecting...'}
    </div>
  );
}
```

### Data Feed

```typescript
function SpaceDataFeed() {
  const [items, setItems] = useState([]);

  useEffect(() => {
    const unsub = node.subscribe('OMM', (data, peerId) => {
      setItems(prev => [
        { data, peerId, time: Date.now() },
        ...prev.slice(0, 99) // Keep last 100
      ]);
    });

    return () => unsub();
  }, []);

  return (
    <ul>
      {items.map((item, i) => (
        <li key={i}>
          {item.data.OBJECT_NAME} - {item.data.EPOCH}
        </li>
      ))}
    </ul>
  );
}
```

## Performance Tips

### Lazy Loading

```typescript
// Load SDK only when needed
const startSDN = async () => {
  const { SDNNode } = await import('@spacedatanetwork/sdn-js');
  const node = new SDNNode();
  await node.start();
  return node;
};
```

### Web Workers

Offload processing to a Web Worker:

```typescript
// worker.ts
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();

self.onmessage = async (e) => {
  if (e.data.type === 'start') {
    await node.start();
    self.postMessage({ type: 'started', peerId: node.peerId });
  }
};

node.subscribe('OMM', (data) => {
  self.postMessage({ type: 'data', schema: 'OMM', data });
});
```

### Connection Pooling

```typescript
// Reuse connection across page navigations
if (!window.sdnNode) {
  window.sdnNode = new SDNNode();
  await window.sdnNode.start();
}

const node = window.sdnNode;
```

## Offline Support

### Service Worker

```typescript
// sw.js
self.addEventListener('fetch', (event) => {
  if (event.request.url.includes('/api/space-data')) {
    event.respondWith(
      caches.match(event.request).then((cached) => {
        return cached || fetch(event.request);
      })
    );
  }
});
```

### Background Sync

```typescript
// Queue messages when offline
if (!navigator.onLine) {
  await saveToQueue(data);
} else {
  await node.publish('OMM', data);
}

// Sync when back online
window.addEventListener('online', async () => {
  const queue = await getQueue();
  for (const item of queue) {
    await node.publish(item.schema, item.data);
  }
  clearQueue();
});
```

## Security Considerations

### Content Security Policy

```html
<meta http-equiv="Content-Security-Policy" content="
  connect-src 'self' wss://*.spacedatanetwork.org;
  worker-src 'self' blob:;
">
```

### Subresource Integrity

```html
<script
  src="https://unpkg.com/@spacedatanetwork/sdn-js"
  integrity="sha384-..."
  crossorigin="anonymous">
</script>
```

## Next Steps

- [Node.js Usage](/guide/js-node) - Server-side features
- [Data Operations](/guide/js-data) - Advanced data handling
- [Digital Identity](/guide/digital-identity) - Identity management
- [API Reference](/api/js-node) - Complete API docs
