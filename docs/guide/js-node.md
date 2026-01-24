# Node.js Usage

Run Space Data Network nodes in Node.js for server-side applications, data pipelines, and background services.

## Overview

Node.js SDN nodes have full capabilities:

- Direct P2P connections (no relay required)
- Persistent SQLite storage
- File-based data ingestion
- Background processing

## Getting Started

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode({
  storage: {
    type: 'sqlite',
    path: './sdn.db'
  }
});

await node.start();

console.log('Node started:', node.peerId);

// Keep running
process.on('SIGINT', async () => {
  console.log('Shutting down...');
  await node.stop();
  process.exit(0);
});
```

## Configuration

### Full Configuration

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode({
  // Listen addresses
  addresses: [
    '/ip4/0.0.0.0/tcp/4001',
    '/ip4/0.0.0.0/udp/4001/quic-v1'
  ],

  // Bootstrap peers
  bootstrap: [
    '/dnsaddr/bootstrap.spacedatanetwork.org/p2p/12D3KooW...'
  ],

  // Storage
  storage: {
    type: 'sqlite',
    path: './data/sdn.db',
    gc: {
      enabled: true,
      interval: 3600000, // 1 hour
      maxAge: 2592000000 // 30 days
    }
  },

  // PubSub
  pubsub: {
    enabled: true,
    topics: ['OMM', 'CDM', 'EPM']
  },

  // Identity
  identity: {
    privateKey: process.env.SDN_PRIVATE_KEY
  }
});
```

### Environment Variables

```bash
# .env
SDN_PRIVATE_KEY=base64-encoded-key
SDN_STORAGE_PATH=/var/lib/sdn/sdn.db
SDN_BOOTSTRAP_PEERS=/dnsaddr/bootstrap.spacedatanetwork.org/p2p/12D3KooW...
```

```typescript
import 'dotenv/config';
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode({
  identity: { privateKey: process.env.SDN_PRIVATE_KEY },
  storage: { type: 'sqlite', path: process.env.SDN_STORAGE_PATH },
  bootstrap: process.env.SDN_BOOTSTRAP_PEERS?.split(',')
});
```

## Data Pipeline

### Ingesting External Data

```typescript
import { SDNNode, TLEParser } from '@spacedatanetwork/sdn-js';
import fs from 'fs/promises';

const node = new SDNNode();
await node.start();

// Parse TLE file
const tleContent = await fs.readFile('tle-data.txt', 'utf-8');
const ommRecords = TLEParser.toOMM(tleContent);

// Publish to network
for (const omm of ommRecords) {
  await node.publish('OMM', omm);
}

console.log(`Published ${ommRecords.length} OMM records`);
```

### Watch Directory

```typescript
import { watch } from 'fs/promises';

async function watchDirectory(node: SDNNode, dir: string) {
  const watcher = watch(dir);

  for await (const event of watcher) {
    if (event.eventType === 'change' && event.filename?.endsWith('.tle')) {
      const content = await fs.readFile(`${dir}/${event.filename}`, 'utf-8');
      const records = TLEParser.toOMM(content);

      for (const record of records) {
        await node.publish('OMM', record);
      }

      console.log(`Ingested ${records.length} records from ${event.filename}`);
    }
  }
}
```

### Scheduled Ingestion

```typescript
import cron from 'node-cron';

// Fetch and publish every hour
cron.schedule('0 * * * *', async () => {
  const response = await fetch('https://celestrak.org/NORAD/elements/active.txt');
  const tle = await response.text();
  const records = TLEParser.toOMM(tle);

  await node.publishBatch('OMM', records);
  console.log(`Scheduled ingest: ${records.length} records`);
});
```

## Express Integration

### REST API

```typescript
import express from 'express';
import { SDNNode } from '@spacedatanetwork/sdn-js';

const app = express();
const node = new SDNNode();

app.use(express.json());

// Get recent OMM data
app.get('/api/omm', async (req, res) => {
  const { limit = 100, object } = req.query;

  const data = await node.query('OMM', {
    where: object ? { OBJECT_NAME: object } : undefined,
    limit: Number(limit),
    orderBy: 'EPOCH',
    order: 'desc'
  });

  res.json(data);
});

// Publish new data
app.post('/api/omm', async (req, res) => {
  const result = await node.publish('OMM', req.body);
  res.json({ success: true, messageId: result.messageId });
});

// Get conjunction warnings
app.get('/api/cdm', async (req, res) => {
  const data = await node.query('CDM', {
    where: {
      COLLISION_PROBABILITY: { $gt: 0.0001 }
    },
    orderBy: 'TCA',
    order: 'asc'
  });

  res.json(data);
});

// Start server
async function main() {
  await node.start();
  app.listen(3000, () => {
    console.log('API server running on port 3000');
  });
}

main();
```

### WebSocket Real-time

```typescript
import { WebSocketServer } from 'ws';

const wss = new WebSocketServer({ port: 8080 });
const clients = new Set<WebSocket>();

wss.on('connection', (ws) => {
  clients.add(ws);
  ws.on('close', () => clients.delete(ws));
});

// Broadcast space data to all clients
node.subscribe('OMM', (data, peerId) => {
  const message = JSON.stringify({ type: 'OMM', data, peerId });

  for (const client of clients) {
    if (client.readyState === WebSocket.OPEN) {
      client.send(message);
    }
  }
});

node.subscribe('CDM', (data, peerId) => {
  const message = JSON.stringify({ type: 'CDM', data, peerId });

  for (const client of clients) {
    if (client.readyState === WebSocket.OPEN) {
      client.send(message);
    }
  }
});
```

## Database Operations

### Direct Database Access

```typescript
// Export data
const allOMM = await node.query('OMM', { limit: -1 });
await fs.writeFile('omm-export.json', JSON.stringify(allOMM, null, 2));

// Import data
const importData = JSON.parse(await fs.readFile('omm-import.json', 'utf-8'));
for (const record of importData) {
  await node.storage.put('OMM', record);
}

// Get statistics
const stats = await node.storage.stats();
console.log('Records by schema:', stats.counts);
console.log('Database size:', stats.size);
```

### Custom Queries

```typescript
// Complex query
const results = await node.query('OMM', {
  where: {
    $and: [
      { INCLINATION: { $gte: 50, $lte: 55 } },
      { ECCENTRICITY: { $lt: 0.01 } },
      { MEAN_MOTION: { $gte: 14, $lte: 16 } }
    ]
  },
  orderBy: 'EPOCH',
  order: 'desc',
  limit: 100
});

// Aggregation
const countByInclination = await node.storage.aggregate('OMM', {
  groupBy: 'INCLINATION',
  aggregates: [
    { field: '*', function: 'count', alias: 'count' }
  ]
});
```

## Clustering

### Worker Threads

```typescript
import { Worker, isMainThread, parentPort } from 'worker_threads';

if (isMainThread) {
  // Main thread - manage workers
  const workers = [];

  for (let i = 0; i < 4; i++) {
    const worker = new Worker(__filename);
    workers.push(worker);

    worker.on('message', (msg) => {
      console.log(`Worker ${i}:`, msg);
    });
  }
} else {
  // Worker thread - process data
  const node = new SDNNode({
    storage: { type: 'memory' }
  });

  await node.start();

  node.subscribe('OMM', (data) => {
    // Process in parallel
    const result = processOMM(data);
    parentPort?.postMessage(result);
  });
}
```

### Cluster Mode

```typescript
import cluster from 'cluster';
import os from 'os';

if (cluster.isPrimary) {
  console.log(`Primary ${process.pid} is running`);

  // Fork workers
  const numCPUs = os.cpus().length;
  for (let i = 0; i < numCPUs; i++) {
    cluster.fork();
  }

  cluster.on('exit', (worker) => {
    console.log(`Worker ${worker.process.pid} died`);
    cluster.fork(); // Replace dead worker
  });
} else {
  // Workers run SDN node
  const node = new SDNNode();
  await node.start();

  console.log(`Worker ${process.pid} started`);
}
```

## Monitoring

### Health Checks

```typescript
import express from 'express';

const healthApp = express();

healthApp.get('/health', async (req, res) => {
  const peers = node.peers.length;
  const isConnected = peers > 0;

  res.status(isConnected ? 200 : 503).json({
    status: isConnected ? 'healthy' : 'degraded',
    peers,
    uptime: process.uptime(),
    peerId: node.peerId
  });
});

healthApp.get('/ready', async (req, res) => {
  const isReady = node.isStarted && node.peers.length > 0;
  res.status(isReady ? 200 : 503).json({ ready: isReady });
});

healthApp.listen(9090);
```

### Metrics

```typescript
import promClient from 'prom-client';

// Create metrics
const messageCounter = new promClient.Counter({
  name: 'sdn_messages_received_total',
  help: 'Total messages received',
  labelNames: ['schema']
});

const peerGauge = new promClient.Gauge({
  name: 'sdn_peers_connected',
  help: 'Number of connected peers'
});

// Track metrics
node.subscribe('OMM', () => messageCounter.inc({ schema: 'OMM' }));
node.subscribe('CDM', () => messageCounter.inc({ schema: 'CDM' }));

setInterval(() => {
  peerGauge.set(node.peers.length);
}, 5000);

// Expose metrics endpoint
app.get('/metrics', async (req, res) => {
  res.set('Content-Type', promClient.register.contentType);
  res.send(await promClient.register.metrics());
});
```

### Logging

```typescript
import pino from 'pino';

const logger = pino({
  level: process.env.LOG_LEVEL || 'info'
});

node.on('peer:connect', (peerId) => {
  logger.info({ peerId }, 'Peer connected');
});

node.on('peer:disconnect', (peerId) => {
  logger.info({ peerId }, 'Peer disconnected');
});

node.subscribe('CDM', (data, peerId) => {
  logger.warn({
    object1: data.OBJECT1_OBJECT_DESIGNATOR,
    object2: data.OBJECT2_OBJECT_DESIGNATOR,
    probability: data.COLLISION_PROBABILITY,
    peerId
  }, 'Conjunction warning received');
});
```

## Error Handling

```typescript
import { SDNNode, SDNError } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();

node.on('error', (error) => {
  if (error instanceof SDNError) {
    logger.error({ code: error.code, message: error.message }, 'SDN error');
  } else {
    logger.error({ error }, 'Unknown error');
  }
});

// Graceful shutdown
async function shutdown() {
  logger.info('Shutting down...');

  try {
    await node.stop();
    logger.info('Node stopped cleanly');
    process.exit(0);
  } catch (error) {
    logger.error({ error }, 'Error during shutdown');
    process.exit(1);
  }
}

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
```

## Next Steps

- [Data Operations](/guide/js-data) - Advanced data handling
- [Data Ingestion](/guide/ingestion-overview) - Build data pipelines
- [Deployment Guide](/guide/deployment) - Production deployment
- [API Reference](/api/js-node) - Complete API docs
