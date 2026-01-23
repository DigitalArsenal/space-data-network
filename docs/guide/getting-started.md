# Getting Started

This guide will help you get up and running with Space Data Network in minutes.

## Choose Your Path

<div class="path-cards">

### Run a Server Node

Best for: Organizations hosting data, running infrastructure

[Full Node Setup →](/guide/full-node)

### Browser Application

Best for: Web applications, dashboards, end-user tools

[JavaScript SDK →](/guide/js-installation)

### Edge Relay

Best for: Enabling browser connectivity, embedded systems

[Edge Relay Setup →](/guide/edge-relay)

</div>

## Quick Start: JavaScript SDK

The fastest way to start using SDN is with the JavaScript SDK in a browser or Node.js application.

### Installation

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

### Basic Usage

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

async function main() {
  // Create and start a node
  const node = new SDNNode();
  await node.start();

  console.log('Node started! Peer ID:', node.peerId);

  // Subscribe to Orbital Mean-Elements Messages
  node.subscribe('OMM', (data, peerId) => {
    console.log(`Received OMM from ${peerId}:`);
    console.log(data);
  });

  // Keep the node running
  console.log('Listening for OMM messages...');
}

main().catch(console.error);
```

### Publishing Data

```typescript
import { SDNNode, SchemaRegistry } from '@spacedatanetwork/sdn-js';

async function publishOMM() {
  const node = new SDNNode();
  await node.start();

  // Create OMM data
  const ommData = {
    OBJECT_NAME: 'ISS (ZARYA)',
    OBJECT_ID: '1998-067A',
    EPOCH: '2024-01-15T12:00:00.000Z',
    MEAN_MOTION: 15.49,
    ECCENTRICITY: 0.0001,
    INCLINATION: 51.64,
    RA_OF_ASC_NODE: 123.45,
    ARG_OF_PERICENTER: 67.89,
    MEAN_ANOMALY: 234.56,
  };

  // Publish to the network
  await node.publish('OMM', ommData);
  console.log('OMM published successfully!');
}
```

## Quick Start: Server Node

### Download

::: code-group

```bash [Linux (x64)]
curl -Lo spacedatanetwork https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-linux-amd64
chmod +x spacedatanetwork
```

```bash [Linux (ARM64)]
curl -Lo spacedatanetwork https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-linux-arm64
chmod +x spacedatanetwork
```

```bash [macOS (Apple Silicon)]
curl -Lo spacedatanetwork https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-darwin-arm64
chmod +x spacedatanetwork
```

```bash [macOS (Intel)]
curl -Lo spacedatanetwork https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-darwin-amd64
chmod +x spacedatanetwork
```

:::

### Initialize and Run

```bash
# Initialize configuration
./spacedatanetwork init

# Start the daemon
./spacedatanetwork daemon
```

You should see output like:

```
SDN Node starting...
Peer ID: 12D3KooWExample123...
Listening on:
  /ip4/0.0.0.0/tcp/4001
  /ip4/0.0.0.0/udp/4001/quic-v1
  /ip6/::/tcp/4001
DHT bootstrapping...
Connected to 3 peers
Ready to exchange space data!
```

## Build from Source

### Prerequisites

- Go 1.21 or later
- Node.js 18 or later (for JavaScript SDK)
- Git

### Clone and Build

```bash
# Clone the repository
git clone https://github.com/DigitalArsenal/go-space-data-network.git
cd go-space-data-network

# Build the server
cd sdn-server
go build -o spacedatanetwork ./cmd/spacedatanetwork

# Optionally build the edge relay (smaller binary)
go build -tags edge -o spacedatanetwork-edge ./cmd/spacedatanetwork-edge
```

### Build JavaScript SDK

```bash
cd sdn-js
npm install
npm run build
```

## Verify Installation

### Server

```bash
./spacedatanetwork version
# Output: spacedatanetwork v1.0.0
```

### JavaScript

```javascript
import { version } from '@spacedatanetwork/sdn-js';
console.log(version);
// Output: 1.0.0
```

## Next Steps

Now that you have SDN installed, explore these guides:

- **[Full Node Setup](/guide/full-node)** - Configure and run a full node
- **[Edge Relay](/guide/edge-relay)** - Enable browser connectivity
- **[JavaScript SDK](/guide/js-browser)** - Build browser applications
- **[Schema Reference](/reference/schemas)** - Understand the data formats
- **[Data Ingestion](/guide/ingestion-overview)** - Convert your data to SDN formats

## Getting Help

- **GitHub Issues**: [Report bugs or request features](https://github.com/DigitalArsenal/go-space-data-network/issues)
- **Discussions**: [Ask questions and share ideas](https://github.com/DigitalArsenal/go-space-data-network/discussions)
- **Space Data Standards**: [Schema documentation](https://spacedatastandards.org)
