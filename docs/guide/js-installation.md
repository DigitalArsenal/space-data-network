# JavaScript SDK Installation

Install and configure the Space Data Network JavaScript SDK for browser and Node.js applications.

## Requirements

- Node.js 18 or later
- npm, yarn, or pnpm

## Installation

::: code-group

```bash [npm]
npm install @spacedatanetwork/sdn-js
```

```bash [yarn]
yarn add @spacedatanetwork/sdn-js
```

```bash [pnpm]
pnpm add @spacedatanetwork/sdn-js
```

:::

## Bundle Size

The SDK is optimized for both browser and Node.js environments:

| Bundle | Size (gzipped) |
|--------|----------------|
| Browser (ESM) | ~85 KB |
| Node.js (CJS) | ~120 KB |
| Types | ~15 KB |

## TypeScript Support

The SDK is written in TypeScript and includes full type definitions:

```typescript
import {
  SDNNode,
  SDNNodeConfig,
  SchemaType,
  OMM,
  CDM
} from '@spacedatanetwork/sdn-js';

// Full type safety
const config: SDNNodeConfig = {
  relays: ['/dns4/relay.spacedatanetwork.org/tcp/443/wss'],
  storage: { type: 'memory' }
};

const node = new SDNNode(config);
```

## Quick Start

### Browser

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

console.log('Connected! Peer ID:', node.peerId);

// Subscribe to orbital data
node.subscribe('OMM', (data, peerId) => {
  console.log('Received:', data);
});
```

### Node.js

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

async function main() {
  const node = new SDNNode({
    storage: { type: 'sqlite', path: './sdn.db' }
  });

  await node.start();
  console.log('Node started:', node.peerId);

  // Keep running
  process.on('SIGINT', async () => {
    await node.stop();
    process.exit(0);
  });
}

main();
```

## Framework Integration

### React

```tsx
import { useEffect, useState } from 'react';
import { SDNNode } from '@spacedatanetwork/sdn-js';

function useSDN() {
  const [node, setNode] = useState<SDNNode | null>(null);
  const [isConnected, setIsConnected] = useState(false);

  useEffect(() => {
    const sdn = new SDNNode();

    sdn.start().then(() => {
      setNode(sdn);
      setIsConnected(true);
    });

    return () => {
      sdn.stop();
    };
  }, []);

  return { node, isConnected };
}

function App() {
  const { node, isConnected } = useSDN();

  if (!isConnected) return <div>Connecting...</div>;

  return <div>Connected: {node?.peerId}</div>;
}
```

### Vue

```vue
<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue';
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = ref<SDNNode | null>(null);
const isConnected = ref(false);

onMounted(async () => {
  node.value = new SDNNode();
  await node.value.start();
  isConnected.value = true;
});

onUnmounted(() => {
  node.value?.stop();
});
</script>

<template>
  <div v-if="!isConnected">Connecting...</div>
  <div v-else>Connected: {{ node?.peerId }}</div>
</template>
```

### Next.js

```typescript
// lib/sdn.ts
import { SDNNode } from '@spacedatanetwork/sdn-js';

let node: SDNNode | null = null;

export async function getSDNNode(): Promise<SDNNode> {
  if (!node) {
    node = new SDNNode();
    await node.start();
  }
  return node;
}

// pages/api/space-data.ts
import { getSDNNode } from '@/lib/sdn';

export default async function handler(req, res) {
  const node = await getSDNNode();
  const data = await node.query('OMM', { limit: 10 });
  res.json(data);
}
```

## CDN Usage

For quick prototyping, use the CDN build:

```html
<script type="module">
  import { SDNNode } from 'https://unpkg.com/@spacedatanetwork/sdn-js/dist/browser.esm.js';

  const node = new SDNNode();
  await node.start();
  console.log('Connected:', node.peerId);
</script>
```

## Bundler Configuration

### Vite

```typescript
// vite.config.ts
import { defineConfig } from 'vite';

export default defineConfig({
  optimizeDeps: {
    include: ['@spacedatanetwork/sdn-js']
  },
  build: {
    commonjsOptions: {
      include: [/@spacedatanetwork\/sdn-js/, /node_modules/]
    }
  }
});
```

### Webpack

```javascript
// webpack.config.js
module.exports = {
  resolve: {
    fallback: {
      crypto: require.resolve('crypto-browserify'),
      stream: require.resolve('stream-browserify'),
      buffer: require.resolve('buffer/')
    }
  },
  plugins: [
    new webpack.ProvidePlugin({
      Buffer: ['buffer', 'Buffer']
    })
  ]
};
```

### esbuild

```javascript
// esbuild.config.js
require('esbuild').build({
  entryPoints: ['src/index.ts'],
  bundle: true,
  platform: 'browser',
  define: {
    'process.env.NODE_ENV': '"production"'
  }
});
```

## Peer Dependencies

The SDK has minimal peer dependencies:

```json
{
  "peerDependencies": {
    "typescript": ">=4.7.0"
  },
  "peerDependenciesMeta": {
    "typescript": {
      "optional": true
    }
  }
}
```

## Troubleshooting

### Module Resolution Errors

If you encounter module resolution issues:

```bash
# Clear cache and reinstall
rm -rf node_modules package-lock.json
npm install
```

### WebSocket Connection Failed

Ensure you're using the correct relay address:

```typescript
const node = new SDNNode({
  relays: [
    // Use WSS (secure) in production
    '/dns4/relay.spacedatanetwork.org/tcp/443/wss/p2p/12D3KooW...'
  ]
});
```

### CORS Issues

If connecting from a browser, ensure the relay allows your origin:

```typescript
// The public relays allow all origins
// For private relays, configure CORS on the server
```

### Memory Issues in Browser

For large datasets, use streaming:

```typescript
// Instead of loading all data
const allData = await node.query('OMM');

// Stream results
for await (const item of node.stream('OMM')) {
  processItem(item);
}
```

## Version Compatibility

| SDK Version | Node.js | Browser | TypeScript |
|-------------|---------|---------|------------|
| 1.x | 18+ | ES2020+ | 4.7+ |
| 0.x | 16+ | ES2019+ | 4.5+ |

## Next Steps

- [Browser Usage](/guide/js-browser) - Browser-specific features
- [Node.js Usage](/guide/js-node) - Server-side usage
- [Data Operations](/guide/js-data) - Working with space data
- [API Reference](/api/js-node) - Complete API documentation
