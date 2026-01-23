# Space Data Network (SDN)

**A decentralized peer-to-peer network for exchanging standardized space data using [Space Data Standards](https://spacedatastandards.org), built on [IPFS](https://ipfs.tech) and [libp2p](https://libp2p.io).**

[![Go Version](https://img.shields.io/badge/go-1.21+-blue.svg)](https://golang.org)
[![TypeScript](https://img.shields.io/badge/typescript-5.0+-blue.svg)](https://www.typescriptlang.org)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![IPFS](https://img.shields.io/badge/built%20on-IPFS-65c2cb.svg)](https://ipfs.tech)

---

## Mission

**Enable decentralized, global collaboration on space situational awareness and space traffic management.**

As space becomes increasingly congested with satellites, debris, and new actors, the need for transparent, real-time data sharing has never been greater. Space Data Network removes barriers to collaboration by:

- **Eliminating single points of failure** - No central server that can go down or be blocked
- **Enabling permissionless participation** - Anyone can join and contribute data
- **Ensuring data integrity** - Cryptographic verification of all shared data
- **Reducing latency** - Direct peer-to-peer data exchange without intermediaries
- **Promoting interoperability** - Standardized formats everyone can use

Whether you're a space agency sharing conjunction warnings, a satellite operator publishing ephemeris data, or a researcher analyzing space traffic patterns, SDN provides the infrastructure for open collaboration.

---

## Overview

Space Data Network enables real-time sharing of space situational awareness data between organizations, satellites, and ground stations. Built on [IPFS](https://ipfs.tech)/[libp2p](https://libp2p.io) with [FlatBuffers](https://google.github.io/flatbuffers/) serialization, SDN provides:

- **Standardized Data Exchange** - All 32 Space Data Standards schemas supported
- **Decentralized Architecture** - No central server required
- **Real-time PubSub** - Subscribe to data streams by type (OMM, CDM, EPM, etc.)
- **Cryptographic Verification** - Ed25519 signatures on all data
- **Cross-Platform** - Server (Go), Browser (TypeScript), Edge Relay support

## Quick Start

### Install the Server

```bash
# Download latest release
curl -sSL https://spacedatanetwork.org/install.sh | bash

# Or build from source
git clone https://github.com/DigitalArsenal/go-space-data-network.git
cd go-space-data-network/sdn-server
go build -o spacedatanetwork ./cmd/spacedatanetwork
```

### Install the JavaScript SDK

```bash
npm install @spacedatanetwork/sdn-js
# or
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

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Space Data Network                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐        │
│  │  Full Node  │◄──►│  Full Node  │◄──►│  Full Node  │        │
│  │   (Go)      │    │   (Go)      │    │   (Go)      │        │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘        │
│         │                  │                  │                │
│         │    DHT + PubSub  │                  │                │
│         │                  │                  │                │
│  ┌──────┴──────┐    ┌──────┴──────┐    ┌──────┴──────┐        │
│  │ Edge Relay  │    │ Edge Relay  │    │ Edge Relay  │        │
│  │   (Go)      │    │   (Go)      │    │   (Go)      │        │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘        │
│         │                  │                  │                │
│         │   Circuit Relay  │                  │                │
│         │                  │                  │                │
│  ┌──────┴──────┐    ┌──────┴──────┐    ┌──────┴──────┐        │
│  │  Browser    │    │  Browser    │    │  Browser    │        │
│  │   (JS)      │    │   (JS)      │    │   (JS)      │        │
│  └─────────────┘    └─────────────┘    └─────────────┘        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## Built on IPFS

Space Data Network is built on the **[InterPlanetary File System (IPFS)](https://ipfs.tech)** stack, leveraging battle-tested decentralized networking technology:

| Technology | Purpose | Link |
|------------|---------|------|
| **[libp2p](https://libp2p.io)** | Modular P2P networking (transports, discovery, encryption) | [docs.libp2p.io](https://docs.libp2p.io) |
| **[Kademlia DHT](https://en.wikipedia.org/wiki/Kademlia)** | Distributed peer and content discovery | [libp2p DHT](https://github.com/libp2p/go-libp2p-kad-dht) |
| **[GossipSub](https://docs.libp2p.io/concepts/pubsub/overview/)** | Scalable publish/subscribe messaging | [libp2p PubSub](https://github.com/libp2p/go-libp2p-pubsub) |
| **[Circuit Relay](https://docs.libp2p.io/concepts/nat/circuit-relay/)** | NAT traversal for browser clients | [libp2p Relay](https://github.com/libp2p/go-libp2p/tree/master/p2p/protocol/circuitv2) |
| **[Kubo](https://github.com/ipfs/kubo)** | Reference IPFS implementation (basis for SDN server) | [github.com/ipfs/kubo](https://github.com/ipfs/kubo) |

### Why IPFS/libp2p?

- **Proven at scale** - Powers millions of nodes worldwide
- **Transport agnostic** - TCP, QUIC, WebSocket, WebTransport, WebRTC
- **NAT-friendly** - Hole punching, relay, and AutoNAT
- **Cryptographic identity** - Every peer has a verifiable identity
- **Content addressing** - Data integrity through content-based identifiers

SDN extends IPFS with **space-specific optimizations**:
- FlatBuffers instead of IPLD for zero-copy performance
- Schema-validated data (only valid Space Data Standards accepted)
- Topic-per-schema PubSub for efficient subscription
- SQLite storage with FlatBuffer virtual tables for queryable data

## Components

| Component | Description | Language |
|-----------|-------------|----------|
| [`sdn-server`](./sdn-server) | Full node and edge relay server | Go |
| [`sdn-js`](./sdn-js) | Browser/Node.js SDK | TypeScript |
| [`schemas`](./schemas) | FlatBuffer schema definitions | FlatBuffers |
| [`desktop`](./desktop) | Desktop application (Electron) | TypeScript |
| [`kubo`](./kubo) | IPFS reference implementation (subtree) | Go |

## Supported Standards

SDN supports all 32 Space Data Standards:

| Category | Standards |
|----------|-----------|
| **Orbit** | OMM, OEM, OCM, OSM |
| **Conjunction** | CDM, CSM |
| **Tracking** | TDM, RFM |
| **Catalog** | CAT, SIT |
| **Entity** | EPM, PNM |
| **Maneuver** | MET, MPE |
| **Propagation** | HYP, EME, EOO, EOP |
| **Reference** | LCC, LDM, CRM, CTR |
| **Other** | ATM, BOV, IDM, PLD, PRG, REC, ROC, SCM, TIM, VCM |

## Space Traffic Management Use Cases

SDN is designed to support the growing need for **Space Traffic Management (STM)** and **Space Situational Awareness (SSA)**:

### Conjunction Assessment
Share and receive collision warnings in real-time:
```typescript
node.subscribe('CDM', (cdm, peerId) => {
  if (cdm.COLLISION_PROBABILITY > 1e-4) {
    alertOperator(cdm);
  }
});
```

### Orbital Data Exchange
Publish and subscribe to satellite positions:
- **OMM** - Mean orbital elements (TLE-equivalent)
- **OEM** - Precise ephemeris state vectors
- **OCM** - Comprehensive orbit characterization

### Coordination & Transparency
- **Maneuver notifications** - Share planned maneuvers (MPE) to prevent surprises
- **Launch coordination** - Distribute launch windows and trajectories (LDM, LCC)
- **Reentry predictions** - Broadcast reentry forecasts (ROC)

### Entity Discovery
Find and verify other participants:
- **EPM** - Organization profiles with public keys
- **PNM** - Peer node identification and capabilities
- **SIT** - Ground station and facility information

---

## Data Ingestion Pipelines

> **Coming Soon**: WASM-based data ingestion plugins

SDN will support modular data ingestion pipelines that convert raw data formats into standardized FlatBuffer schemas. These plugins run in WebAssembly for cross-platform compatibility:

```
┌─────────────┐     ┌─────────────────┐     ┌─────────────┐
│  Raw Data   │────►│  WASM Plugin    │────►│ FlatBuffer  │
│  (TLE, XML) │     │  (Converter)    │     │  (OMM.fbs)  │
└─────────────┘     └─────────────────┘     └─────────────┘
```

**Planned plugins:**
- TLE to OMM converter
- CCSDS XML to native FlatBuffer
- SP3 ephemeris to OEM
- Custom format adapters

See [Data Ingestion Pipeline Architecture](./docs/ingestion-pipelines.md) for details.

## Documentation

- [Getting Started Guide](./docs/getting-started.md)
- [Full Node Setup](./docs/full-node.md)
- [Edge Relay Deployment](./docs/edge-relay.md)
- [JavaScript SDK Reference](./docs/js-sdk.md)
- [Schema Reference](./docs/schemas.md)
- [API Documentation](./docs/api.md)

## Downloads

### Server Binaries

| Platform | Architecture | Download |
|----------|--------------|----------|
| Linux | x86_64 | [spacedatanetwork-linux-amd64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest) |
| Linux | ARM64 | [spacedatanetwork-linux-arm64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest) |
| macOS | x86_64 | [spacedatanetwork-darwin-amd64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest) |
| macOS | ARM64 | [spacedatanetwork-darwin-arm64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest) |
| Windows | x86_64 | [spacedatanetwork-windows-amd64.exe](https://github.com/DigitalArsenal/go-space-data-network/releases/latest) |

### Edge Relay (Lightweight)

| Platform | Architecture | Download |
|----------|--------------|----------|
| Linux | x86_64 | [spacedatanetwork-edge-linux-amd64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest) |
| Linux | ARM64 | [spacedatanetwork-edge-linux-arm64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest) |

### JavaScript SDK

```bash
npm install @spacedatanetwork/sdn-js
```

## Development

### Prerequisites

- Go 1.21+
- Node.js 18+
- Emscripten (for WASM compilation)

### Building from Source

```bash
# Clone repository
git clone https://github.com/DigitalArsenal/go-space-data-network.git
cd go-space-data-network

# Build server
cd sdn-server
go build -o spacedatanetwork ./cmd/spacedatanetwork
go build -tags edge -o spacedatanetwork-edge ./cmd/spacedatanetwork-edge

# Build JavaScript SDK
cd ../sdn-js
npm install
npm run build

# Setup Emscripten (for WASM)
./scripts/setup-emsdk.sh
```

### Running Tests

```bash
# Go tests
cd sdn-server
go test ./...

# JavaScript tests
cd sdn-js
npm test
```

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](./CONTRIBUTING.md) for guidelines.

## License

MIT License - see [LICENSE](./LICENSE) for details.

## Links

- **Website**: [spacedatanetwork.org](https://spacedatanetwork.org)
- **Documentation**: [docs.spacedatanetwork.org](https://docs.spacedatanetwork.org)
- **GitHub**: [github.com/DigitalArsenal/go-space-data-network](https://github.com/DigitalArsenal/go-space-data-network)
- **Space Data Standards**: [spacedatastandards.org](https://spacedatastandards.org)
- **npm Package**: [@spacedatanetwork/sdn-js](https://www.npmjs.com/package/@spacedatanetwork/sdn-js)

---

<p align="center">
  <strong>Built for the space community</strong><br>
  Enabling secure, standardized data exchange across the cosmos
</p>
