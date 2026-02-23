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

- **Standardized Data Exchange** - All Space Data Standards schemas supported
- **Decentralized Architecture** - No central server required
- **Real-time PubSub** - Subscribe to data streams by type (OMM, CDM, EPM, etc.)
- **Cryptographic Verification** - Ed25519 signatures on all data
- **Cross-Platform** - Server (Go), Browser (TypeScript), Desktop, Edge Relay support

---

## Quick Start

### Install the Server

```bash
# Download latest release
curl -sSL https://digitalarsenal.github.io/space-data-network//install.sh | bash

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

## Identity & HD Key Derivation

Every SDN node derives its cryptographic identity from a **BIP-39 mnemonic** using [SLIP-10](https://github.com/satoshilabs/slips/blob/master/slip-0010.md) hierarchical deterministic key derivation with the standard BIP-44 Bitcoin derivation path (coin type **0**).

```text
BIP-39 Mnemonic → PBKDF2 → 512-bit Seed → SLIP-10 Master Key
    ├── m/44'/0'/0'/0'/0'  →  Ed25519 Signing Key (also libp2p PeerID)
    └── m/44'/0'/0'/1'/0'  →  X25519 Encryption Key
```

### Why BIP-44?

SDN reuses the standard [BIP-44](https://github.com/bitcoin/bips/blob/master/bip-0044.mediawiki) HD wallet path structure with Bitcoin's coin type `0`:

- **Wallet-native identity.** The BIP-44 path structure lets users derive SDN signing and encryption keys from the same mnemonic they use for cryptocurrency wallets. One seed, many independent key trees.
- **Multi-account.** The `account'` segment enables one mnemonic to manage multiple SDN identities (operator, sensor, analytics service), each with independent key pairs.

The **xpub** (extended public key) serves as the master network identity. Anyone with the xpub can derive the node's public signing and encryption keys without access to private key material.

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
- Schema-validated data (Space Data Standards + OrbPro control schemas)
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

OrbPro licensing/key exchange stream schemas (v1.0) are versioned in the plugin SDK:

- `packages/plugin-sdk/schemas/orbpro/key-broker/PublicKeyResponse.fbs`
- `packages/plugin-sdk/schemas/orbpro/key-broker/KeyBrokerRequest.fbs`
- `packages/plugin-sdk/schemas/orbpro/key-broker/KeyBrokerResponse.fbs`

Regenerate plugin SDK + SDN Go bindings from these schemas (via `flatc-wasm`):

```bash
npm run generate:plugin-sdk:key-broker-bindings
```

Run the plugin SDK protocol test client:

```bash
npm run test:plugin-sdk:key-broker-client -- --node-info-url http://127.0.0.1:5010/api/node/info
```

### Server Packages

| Package | Description |
|---------|-------------|
| `internal/sds` | FlatBuffer builders for all SDS schemas with fluent API |
| `internal/vcard` | EPM to vCard/QR code bidirectional conversion |
| `internal/pubsub` | PubSub topics and PNM-based tip/queue system |
| `internal/storage` | SQLite storage with FlatBuffer support |

---

## Supported Standards

SDN supports all [Space Data Standards](https://spacedatastandards.org):

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

## Network Architecture

SDN uses a **two-tier peer topology** for maximum reach and reliability:

```text
┌─────────────────────────────────────────────────────────────────┐
│                    FULL NODES (Open Internet)                    │
│                                                                  │
│    ┌──────────┐      ┌──────────┐      ┌──────────┐             │
│    │Full Node │◄────►│Full Node │◄────►│Full Node │             │
│    │  (Go)    │      │  (Go)    │      │  (Go)    │             │
│    └────┬─────┘      └────┬─────┘      └────┬─────┘             │
│         │                 │                 │                    │
│         │    DHT + GossipSub + Relay        │                    │
│         │                 │                 │                    │
├─────────┼─────────────────┼─────────────────┼────────────────────┤
│         ▼                 ▼                 ▼                    │
│                 LIGHT PEERS (Behind NAT/Firewall)                │
│                                                                  │
│    ┌──────────┐      ┌──────────┐      ┌──────────┐             │
│    │ Browser  │      │ Desktop  │      │Corporate │             │
│    │  (JS)    │      │  (App)   │      │   Node   │             │
│    └──────────┘      └──────────┘      └──────────┘             │
└─────────────────────────────────────────────────────────────────┘
```

### Full Nodes
- Run on servers with **public IP addresses**
- Participate in DHT routing and peer discovery
- Relay traffic for firewalled peers via Circuit Relay
- Pin and store content for the network
- **Requirements:** Public IP, ports 4001 (libp2p), 8080 (HTTP API)

### Light Peers
- Connect through relay nodes when behind NAT/firewalls
- Can subscribe to data, publish messages, verify signatures
- Cannot contribute to DHT routing
- Includes: browsers, mobile apps, desktop apps, corporate networks

### Run a Full Node

Help strengthen the network by running a full node:

```bash
./spacedatanetwork daemon --relay-enabled --announce-public
```

---

## Content Addressing

All data on SDN is **content-addressed** using cryptographic hashes (CIDs):

| Feature | Description |
|---------|-------------|
| **Tamper-proof** | Hash changes if data is modified - tampering is immediately detectable |
| **Permanent references** | CIDs never change - reference specific data versions forever |
| **Deduplication** | Same data = same hash - network automatically deduplicates |
| **Selective pinning** | Choose what to store locally - pin critical data for availability |

---

## PNM Tip/Queue System

SDN uses **Publish Notification Messages (PNM)** for intelligent content distribution. Instead of broadcasting all data, nodes announce content availability via PNM, allowing peers to selectively fetch based on their configuration.

### How It Works

```
Publisher                           Subscriber
    |                                   |
    |-- Pin content locally             |
    |-- Broadcast PNM (CID + schema) ---|--> Receive PNM
    |                                   |-- Check config for peer + schema
    |                                   |-- If autoFetch: fetch content
    |                                   |-- If autoPin: pin with TTL
```

### Configuration

Nodes can configure auto-fetch, auto-pin, and TTL per-source AND per-schema:

| Setting | Description |
|---------|-------------|
| **Per-schema defaults** | E.g., always fetch CDM (conjunction data) |
| **Per-source overrides** | E.g., trust data from partner organizations |
| **Per-source+schema** | E.g., special handling for OMM from trusted source |

This enables flexible policies like:
- Auto-pin all conjunction warnings from anyone
- Auto-fetch orbital data only from trusted partners
- Store data from government agencies for 1 week, commercial for 24h

See [sdn-server documentation](./sdn-server/README.md) for configuration details.

---

## Data Marketplace

SDN includes an optional **commercial layer** for monetizing space data:

### How It Works

1. **Provider publishes** premium data product (high-precision ephemeris, analysis, etc.)
2. **Per-customer encryption** - Data encrypted with each customer's public key (ECIES)
3. **Customer pays** via credit card through integrated payment gateway
4. **Access granted** - Customer receives and decrypts data with their private key

### Features

| Category | Options |
|----------|---------|
| **Data Products** | High-precision ephemeris, conjunction analysis, historical archives, real-time feeds |
| **Plugin Marketplace** | Analysis algorithms, visualization tools, format converters, custom propagators |
| **Payment Options** | Credit cards (Stripe), subscriptions, usage-based billing, enterprise invoicing |

### Technical Details

- **Encryption:** ECIES with X25519 key exchange + AES-256-GCM
- **Payment Gateway:** Stripe integration for credit card processing
- **Revenue Distribution:** Automated splits between data providers and platform
- **Metering:** Usage tracking for consumption-based billing

The marketplace operates **on top of the free, open network**. Core SSA data exchange remains free and open - the commercial layer is opt-in for premium products.

---


## Plugin harness smoke test

Run an end-to-end check that validates loading a licensing plugin from a local workspace into SDN.

```bash
npm run plugin-harness -- /path/to/private-repo
```

You can also use the command with any plugin workspace path:

```bash
npm run plugin-harness -- /path/to/repo
```

Options:
- `--repo` (or positional first arg): path to the plugin workspace
- `--admin-addr`: admin endpoint used for verification (default `127.0.0.1:5010`)
- `--artifact-dir`: path to existing encrypted artifacts when `--skip-build` is set
- `--skip-build`: use existing artifacts in the staging directory
- `--keep-workspace`: keep temporary workspace for debugging
- `--derivation-secret`: optional derivation secret override (64 hex chars)

This command is key-management agnostic on the CLI:
- It derives the keypair internally for normal runs.
- A fixed test public key is read from `PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX` when set.
- For `--skip-build`, it requires both `PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX` and `PLUGIN_KEY_SERVER_ARTIFACT_PRIVATE_KEY_HEX`.

The command uses the standardized plugin task:

```bash
npm run build:key-server
```

It then copies/decrypts the generated encrypted artifact, boots SDN with a temporary plugin catalog, and verifies:

- `/api/v1/plugins/manifest` reports the plugin id (default `plugin-key-broker`) as `running`
- `/api/v1/plugins/<plugin-id>/bundle` returns 200 and non-empty WASM payload

### Private repo setup for plugin harness tests

This harness runs against private repos as long as the repo is reachable and follows the plugin workspace contract.

1. Clone/fetch private repo using your normal auth path (SSH key or token-based HTTPS).
2. Confirm workspace layout includes:
   - `package.json`
   - `scripts/build-plugin-release.js` (or `PLUGIN_HARNESS_BUILD_HELPER_SCRIPT` override)
   - `npm run build:key-server` succeeds (or configure `PLUGIN_HARNESS_BUILD_COMMAND`)
3. Export one of the artifact public key env vars used for staging:
   - `PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX` (preferred)
   - `PLUGIN_KEY_SERVER_ARTIFACT_PRIVATE_KEY_HEX` when using `--skip-build`
4. Run:
   ```bash
npm run plugin-harness -- /path/to/private-plugin-repo
   ```
5. The harness validates the plugin lifecycle and plugin API endpoints in SDN.

Use `--skip-build` when reusing staged artifacts already in CI:

```bash
export PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX=<public_hex>
export PLUGIN_KEY_SERVER_ARTIFACT_PRIVATE_KEY_HEX=<matching_private_hex>
npm run plugin-harness -- /path/to/private-plugin-repo --skip-build --artifact-dir /path/to/Build/plugin/licensing-server
```

If your private repo has a custom auth requirement, run the harness in that authenticated shell context so Git can access dependencies and source.

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

Full documentation is available at [docs.digitalarsenal.github.io/space-data-network](https://digitalarsenal.github.io/space-data-network/) or locally at [docs/docs.html](./docs/docs.html).

To preview the docs locally, start a webserver from the `docs/` directory:

```bash
cd docs && python3 -m http.server 8080
```

Then open [http://localhost:8080](http://localhost:8080).

**Topics covered:**
- Getting Started & Quick Start
- Full Node Setup & Configuration
- Edge Relay Deployment
- JavaScript SDK Reference
- REST & WebSocket API
- Schema Reference (all Space Data Standards)

---

## Links

- [digitalarsenal.github.io/space-data-network](https://digitalarsenal.github.io/space-data-network/)
- [docs.digitalarsenal.github.io/space-data-network](https://digitalarsenal.github.io/space-data-network/)
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
