# Space Data Network (SDN)

**A decentralized peer-to-peer network for exchanging standardized space data using [Space Data Standards](https://spacedatastandards.org), built on [IPFS](https://ipfs.tech) and [libp2p](https://libp2p.io).**

[![Go](https://img.shields.io/badge/go-1.21+-white?style=flat&labelColor=black)](https://golang.org)
[![TypeScript](https://img.shields.io/badge/typescript-5.0+-white?style=flat&labelColor=black)](https://www.typescriptlang.org)
[![License](https://img.shields.io/badge/license-MIT-white?style=flat&labelColor=black)](LICENSE)
[![IPFS](https://img.shields.io/badge/built_on-IPFS-white?style=flat&labelColor=black)](https://ipfs.tech)

---

## Mission

**Enable decentralized, global collaboration on space situational awareness and space traffic management.**

As space becomes increasingly congested with satellites, debris, and new actors, the need for transparent, real-time data sharing has never been greater. Space Data Network removes barriers to collaboration by:

- **Eliminating single points of failure** - No central server that can go down or be blocked
- **Enabling permissionless participation** - Anyone can join and contribute data
- **Ensuring data integrity** - Cryptographic verification of all shared data
- **Reducing latency** - Direct peer-to-peer data exchange without intermediaries
- **Promoting interoperability** - Standardized formats everyone can use

---

## Overview

Space Data Network enables real-time sharing of space situational awareness data between organizations, satellites, and ground stations. Built on [IPFS](https://ipfs.tech)/[libp2p](https://libp2p.io) with [FlatBuffers](https://google.github.io/flatbuffers/) serialization, SDN provides:

- **Standardized Data Exchange** - All 32 Space Data Standards schemas supported
- **Decentralized Architecture** - No central server required
- **Real-time PubSub** - Subscribe to data streams by type (OMM, CDM, EPM, etc.)
- **Cryptographic Verification** - Ed25519 signatures on all data
- **Cross-Platform** - Server (Go), Browser (TypeScript), Desktop, Edge Relay support

---

## Quick Start

### Install the Server

```bash
# Download latest release
curl -sSL https://spacedatanetwork.org/install.sh | bash

# Or build from source
git clone https://github.com/DigitalArsenal/space-data-network.git
cd space-data-network/sdn-server
go build -o spacedatanetwork ./cmd/spacedatanetwork
```

### Install the JavaScript SDK

```bash
npm install @spacedatanetwork/sdn-js
```

```bash
yarn add @spacedatanetwork/sdn-js
```

### Run a Full Node

```bash
# Initialize configuration
./spacedatanetwork init

# Start the node
./spacedatanetwork daemon
```

### Browser Usage

```typescript
import { SDNNode, SchemaRegistry } from '@spacedatanetwork/sdn-js';

// Create and start a node
const node = new SDNNode();
await node.start();

// Subscribe to Orbital Mean-Elements Messages
node.subscribe('OMM', (data, peerId) => {
  console.log(`Received OMM from ${peerId}:`, data);
});

// Publish data
const ommData = { /* your OMM data */ };
await node.publish('OMM', ommData);
```

---

## Architecture

```
+-------------------------------------------------------------------+
|                      Space Data Network                           |
+-------------------------------------------------------------------+
|                                                                   |
|   +-----------+      +-----------+      +-----------+             |
|   | Full Node |<---->| Full Node |<---->| Full Node |             |
|   |   (Go)    |      |   (Go)    |      |   (Go)    |             |
|   +-----+-----+      +-----+-----+      +-----+-----+             |
|         |                  |                  |                   |
|         |     DHT + PubSub |                  |                   |
|         |                  |                  |                   |
|   +-----+-----+      +-----+-----+      +-----+-----+             |
|   |Edge Relay |      |Edge Relay |      |Edge Relay |             |
|   |   (Go)    |      |   (Go)    |      |   (Go)    |             |
|   +-----+-----+      +-----+-----+      +-----+-----+             |
|         |                  |                  |                   |
|         |  Circuit Relay   |                  |                   |
|         |                  |                  |                   |
|   +-----+-----+      +-----+-----+      +-----+-----+             |
|   |  Browser  |      |  Desktop  |      |  Browser  |             |
|   |   (JS)    |      |   (App)   |      |   (JS)    |             |
|   +-----------+      +-----------+      +-----------+             |
|                                                                   |
+-------------------------------------------------------------------+
```

---

## Downloads

### Full Node

| Platform | Architecture | Download |
|----------|--------------|----------|
| Linux | amd64 | [spacedatanetwork-linux-amd64](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/spacedatanetwork-linux-amd64) |
| Linux | arm64 | [spacedatanetwork-linux-arm64](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/spacedatanetwork-linux-arm64) |
| macOS | amd64 | [spacedatanetwork-darwin-amd64](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/spacedatanetwork-darwin-amd64) |
| macOS | arm64 | [spacedatanetwork-darwin-arm64](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/spacedatanetwork-darwin-arm64) |
| Windows | amd64 | [spacedatanetwork-windows-amd64.exe](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/spacedatanetwork-windows-amd64.exe) |

### Edge Relay

| Platform | Architecture | Download |
|----------|--------------|----------|
| Linux | amd64 | [spacedatanetwork-edge-linux-amd64](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/spacedatanetwork-edge-linux-amd64) |
| Linux | arm64 | [spacedatanetwork-edge-linux-arm64](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/spacedatanetwork-edge-linux-arm64) |

### Desktop Application

| Platform | Download |
|----------|----------|
| macOS | [SpaceDataNetwork.dmg](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/SpaceDataNetwork.dmg) |
| Windows | [SpaceDataNetwork-Setup.exe](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/SpaceDataNetwork-Setup.exe) |
| Linux | [SpaceDataNetwork.AppImage](https://github.com/DigitalArsenal/space-data-network/releases/latest/download/SpaceDataNetwork.AppImage) |

### JavaScript SDK

```bash
npm install @spacedatanetwork/sdn-js
```

---

## Built on IPFS

Space Data Network is built on the **[InterPlanetary File System (IPFS)](https://ipfs.tech)** stack:

| Technology | Purpose |
|------------|---------|
| [libp2p](https://libp2p.io) | Modular P2P networking |
| [Kademlia DHT](https://docs.libp2p.io) | Distributed peer discovery |
| [GossipSub](https://docs.libp2p.io/concepts/pubsub/overview/) | Publish/subscribe messaging |
| [Circuit Relay](https://docs.libp2p.io/concepts/nat/circuit-relay/) | NAT traversal |
| [Kubo](https://github.com/ipfs/kubo) | IPFS reference implementation |

SDN extends IPFS with space-specific optimizations:
- FlatBuffers for zero-copy performance
- Schema-validated data (Space Data Standards only)
- Topic-per-schema PubSub
- SQLite storage with FlatBuffer virtual tables

---

## Components

| Component | Description | Language |
|-----------|-------------|----------|
| [sdn-server](./sdn-server) | Full node and edge relay server | Go |
| [sdn-js](./sdn-js) | Browser/Node.js SDK | TypeScript |
| [desktop](./desktop) | Desktop application | TypeScript |
| [schemas](./schemas) | FlatBuffer schema definitions | FlatBuffers |
| [kubo](./kubo) | IPFS reference implementation | Go |

---

## Supported Standards

SDN supports all 32 Space Data Standards:

| Category | Standards |
|----------|-----------|
| Orbit | OMM, OEM, OCM, OSM |
| Conjunction | CDM, CSM |
| Tracking | TDM, RFM |
| Catalog | CAT, SIT |
| Entity | EPM, PNM |
| Maneuver | MET, MPE |
| Propagation | HYP, EME, EOO, EOP |
| Reference | LCC, LDM, CRM, CTR |
| Other | ATM, BOV, IDM, PLD, PRG, REC, ROC, SCM, TIM, VCM |

---

## Use Cases

### Conjunction Assessment

```typescript
node.subscribe('CDM', (cdm, peerId) => {
  if (cdm.COLLISION_PROBABILITY > 1e-4) {
    alertOperator(cdm);
  }
});
```

### Orbital Data Exchange

- **OMM** - Mean orbital elements (TLE-equivalent)
- **OEM** - Precise ephemeris state vectors
- **OCM** - Comprehensive orbit characterization

### Coordination

- **MPE** - Maneuver notifications
- **LDM/LCC** - Launch coordination
- **ROC** - Reentry predictions

---

## Development

### Prerequisites

- Go 1.21+
- Node.js 18+
- Emscripten (for WASM)

### Build from Source

```bash
git clone https://github.com/DigitalArsenal/space-data-network.git
cd space-data-network

# Build server
cd sdn-server
go build -o spacedatanetwork ./cmd/spacedatanetwork
go build -tags edge -o spacedatanetwork-edge ./cmd/spacedatanetwork-edge

# Build JavaScript SDK
cd ../sdn-js
npm install
npm run build
```

### Run Tests

```bash
# Go tests
cd sdn-server && go test ./...

# JavaScript tests
cd sdn-js && npm test
```

---

## Documentation

- [Getting Started](./docs/getting-started.md)
- [Full Node Setup](./docs/full-node.md)
- [Edge Relay Deployment](./docs/edge-relay.md)
- [JavaScript SDK Reference](./docs/js-sdk.md)
- [Schema Reference](./docs/schemas.md)
- [API Documentation](./docs/api.md)

---

## Links

- [spacedatanetwork.org](https://spacedatanetwork.org)
- [docs.spacedatanetwork.org](https://docs.spacedatanetwork.org)
- [GitHub](https://github.com/DigitalArsenal/space-data-network)
- [Space Data Standards](https://spacedatastandards.org)
- [npm: @spacedatanetwork/sdn-js](https://www.npmjs.com/package/@spacedatanetwork/sdn-js)

---

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for guidelines.

## License

MIT License - see [LICENSE](./LICENSE) for details.

---

<p align="center">
  <strong>Built for the space community</strong>
</p>
