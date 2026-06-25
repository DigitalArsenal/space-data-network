# Space Data Network (SDN)

**A decentralized peer-to-peer network for exchanging standardized space data using [Space Data Standards](https://spacedatastandards.org), built on [IPFS](https://ipfs.tech) and [libp2p](https://libp2p.io).**

[![CI](https://img.shields.io/github/actions/workflow/status/DigitalArsenal/space-data-network/ci.yml?branch=main&style=flat-square&logo=githubactions&logoColor=white&label=CI)](https://github.com/DigitalArsenal/space-data-network/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/DigitalArsenal/space-data-network?filename=sdn-server%2Fgo.mod&style=flat-square&logo=go)](https://github.com/DigitalArsenal/space-data-network/blob/main/sdn-server/go.mod)
[![TypeScript](https://img.shields.io/badge/TypeScript-5.x-3178C6?style=flat-square&logo=typescript&logoColor=white)](https://www.typescriptlang.org/)
[![License](https://img.shields.io/github/license/DigitalArsenal/space-data-network?style=flat-square)](https://github.com/DigitalArsenal/space-data-network/blob/main/LICENSE)
[![Built on IPFS](https://img.shields.io/badge/project-IPFS-65C2CB?style=flat-square&logo=ipfs&logoColor=white)](https://ipfs.tech/)

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

## Current UI Surfaces

- `/` is the browser-first SDN UI.
- `/webui` is the upstream-style IPFS WebUI.
- `/admin` is reserved for admin and auth flows.

The SDN browser path uses `sdn-js` plus the generic async capability surfaces from `space-data-module-sdk` and the existing `hd-wallet-wasm` and `hd-wallet-ui` identity stack. It uses direct SDN APIs and browser-safe package exports without a helper service.

## Marketplace Direction

Marketplace discovery is driven by the canonical `PLG` manifest from `spacedatastandards.org`. There is exactly one signed listing per `PLUGIN_ID + VERSION`, and schema changes must land upstream before they are consumed here.

---

## Quick Start

### Install the Server

```bash
# macOS/Linux
curl -fsSL https://spacedatanetwork.org/install.sh | bash

# Windows PowerShell
irm https://spacedatanetwork.org/install.ps1 | iex

# Or build from source
git clone https://github.com/DigitalArsenal/space-data-network.git
cd space-data-network
npm run install:wasmedge
npm run server:build
```

The one-line installers are user-scoped by default. They install the bundle
under `~/.spacedatanetwork/bundles` and command launchers under
`~/.spacedatanetwork/bin`; set `SDN_INSTALL_DIR` or `SDN_BUNDLE_DIR` only when
you need a custom location.

Source builds of the Go server host standalone WASM artifacts through WasmEdge,
so `space-data-network` installs and wires the native WasmEdge SDK as part of
its own build and test entrypoints.

### Build the JavaScript SDK (Source)

```bash
cd space-data-network/sdn-js
npm install
npm run build
```

### Run a Full Node

```bash
# Initialize configuration and node identity
spacedatanetwork init

# Start the node as a persistent background service
spacedatanetwork start

# Check the local daemon
spacedatanetwork status
```

`spacedatanetwork start` installs and starts a user-scoped background service
that persists across login/restart. It uses launchd on macOS, `systemd --user`
on Linux, and a per-user Scheduled Task on Windows, so the daemon uses the same
config, mnemonic, and data directory as the interactive CLI.

Foreground/manual mode is still available:

```bash
spacedatanetwork daemon
```

Service controls:

```bash
spacedatanetwork stop
spacedatanetwork restart
spacedatanetwork service status
spacedatanetwork service install
spacedatanetwork service uninstall
```

Remove the current self-contained install and aliases:

```bash
spacedatanetwork remove --dry-run
spacedatanetwork remove
```

`remove` preserves `~/.spacedatanetwork` by default so node identity and data
survive reinstall. Use `spacedatanetwork remove --purge-data` only when you
want to delete the local config, mnemonic, and data as well.

Search local SDN providers, standards, and data-source metadata:

```bash
spacedatanetwork search providers celestrak --schema OMM
spacedatanetwork search standards OMM --format json
spacedatanetwork search data --schema CAT --provider-id space-data-network-02 --format csv
```

Search output defaults to aligned table rows. Use `--format json` for scripts or
`--format csv` for spreadsheets.

Create or update your public EPM/vCard contact record:

```bash
spacedatanetwork identity wizard
spacedatanetwork identity wizard --set dn="CelesTrak Provider" --set legal_name="CelesTrak" --format json --yes
spacedatanetwork identity export --format flatbuffer --output epm.fbs
spacedatanetwork identity export --format qrcode
```

Identity export supports text, JSON, CSV, FlatBuffer, and QR code output. Text
is the vCard, JSON is the EPM projection, CSV is a single-row contact export,
FlatBuffer writes the signed EPM bytes, and QR code prints a terminal QR for
the vCard.

The wizard stores only public EPM contact fields. It never prints mnemonic,
xpriv, private signing key, or private encryption key material. If the daemon is
running with admin auth enabled, pass `--session-token` or set
`SDN_SESSION_TOKEN` so the wizard can update the live EPM without dropping
runtime-owned fields.

Update the CLI bundle through the SDN-owned signed update provider:

```bash
spacedatanetwork update check
spacedatanetwork update stage
spacedatanetwork update apply
```

The update feed is rooted at `updates.spacedatanetwork.org`. The daemon update
path stages the replacement, swaps the running bundle in place, restarts the
daemon, checks health, and rolls back if the updated daemon does not come back
healthy.

### Browser Usage

```typescript
import { SDNNode, SchemaRegistry } from './path/to/sdn-js/dist/esm/index.js';

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

## CI and Local Checks

- Local CI (same checks as GitHub CI):

```bash
./scripts/ci-local.sh quick
```

- Full local CI (includes encryption tests):

```bash
./scripts/ci-local.sh full
```

- Pushes run local CI automatically via `.husky/pre-push`. To bypass intentionally:

```bash
SKIP_LOCAL_CI=1 git push
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

Builds are published as GitHub releases with release numbers like
`v1.0.3-beta.1`: [Space Data Network v1.0.3-beta.1 release](https://github.com/DigitalArsenal/space-data-network/releases/tag/v1.0.3-beta.1).
Use the newest release number as `<beta-version>` when downloading assets.

| Artifact | Use |
|----------|-----|
| `spacedatanetwork-full_<native-package-version>_amd64.deb` / `.rpm` | Linux full-node packages |
| `spacedatanetwork-linux-vm-<native-package-version>.tar.gz` | Linux VM/full-node bundle |
| `spacedatanetwork-<beta-version>-<os>-<arch>.tar.gz` / `.zip` | Self-contained native CLI bundle with SDN, Kubo, UI assets, updater module |
| `space-data-network-desktop-<desktop-version>-<platform artifact>` | Desktop app installers for macOS, Windows, and Linux |
| `spacedatanetwork-container-<native-package-version>-linux-amd64.tar.gz` | Docker image tarball |
| `spacedatanetwork-darwin-arm64.tar.gz` | macOS Apple Silicon full-node bundle |
| `spacedatanetwork-sdn-js-<sdk-version>.tgz` | Browser and Node JavaScript SDK package tarball |
| `spacedatanetwork-sbom.cdx.json` | CycloneDX SBOM |
| `spacedatanetwork-checksums.txt` | SHA-256 checksums for release files |

Native Linux download names use `<native-package-version>` such as `1.0.3.beta.1`
because GitHub release asset names avoid the release tag's SemVer hyphen.

Container images are published to Docker Hub and tagged by release number and by
the moving `beta` tag:

- `dockerdigitalarsenal/space-data-network:v1.0.3-beta.1`

The single image defaults to a full node. Operators who need edge-relay mode can
override the container command.

### Native releases

Native artifacts live under versioned SDN release tags, not SDK-only tags such
as `sdn-js-v...`. The portable CLI archive is the primary cross-platform native
download and includes `spacedatanetwork`, the `sdn` alias, bundled Kubo, SDN UI,
IPFS WebUI, and the updater module. Existing Linux packages, VM bundles, Docker
tarballs, and the macOS ARM64 full-node bundle remain available for operators.

After installing a portable CLI archive, use `spacedatanetwork start` for the
persistent background node, `spacedatanetwork daemon` for foreground/manual
mode, and `spacedatanetwork remove` to remove the current installed bundle.

macOS ARM64 direct download:
[spacedatanetwork-darwin-arm64.tar.gz](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-darwin-arm64.tar.gz)

Portable CLI archive names:

- `spacedatanetwork-<beta-version>-darwin-arm64.tar.gz`
- `spacedatanetwork-<beta-version>-darwin-amd64.tar.gz`
- `spacedatanetwork-<beta-version>-linux-amd64.tar.gz`
- `spacedatanetwork-<beta-version>-linux-arm64.tar.gz`
- `spacedatanetwork-<beta-version>-windows-amd64.zip`

Beta `v1.0.3-beta.1` direct downloads:

- [macOS Apple Silicon self-contained CLI](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-1.0.3-beta.1-darwin-arm64.tar.gz)
- [Linux 64-bit Intel/AMD self-contained CLI](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-1.0.3-beta.1-linux-amd64.tar.gz)
- [Windows 64-bit Intel/AMD self-contained CLI](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-1.0.3-beta.1-windows-amd64.zip)
- [macOS Desktop app (DMG)](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/space-data-network-desktop-0.47.0-mac.dmg)
- [Windows Desktop installer](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/space-data-network-desktop-setup-0.47.0-windows-x64.exe)
- [Linux Desktop AppImage](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/space-data-network-desktop-0.47.0-linux-x86_64.AppImage)
- [Linux 64-bit Intel/AMD full node DEB](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-full_1.0.3.beta.1_amd64.deb)
- [Linux 64-bit Intel/AMD full node RPM](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-full-1.0.3.beta.1-1.x86_64.rpm)
- [Linux 64-bit Intel/AMD VM bundle](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-linux-vm-1.0.3.beta.1.tar.gz)
- [Docker 64-bit Intel/AMD image](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-container-1.0.3.beta.1-linux-amd64.tar.gz)
- [JavaScript SDK package tarball](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-sdn-js-2.0.12.tgz)
- [CycloneDX SBOM](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-sbom.cdx.json)
- [SHA-256 checksums](https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-checksums.txt)

### JavaScript SDK

```bash
cd sdn-js
npm install
npm run build
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

### Managed Node Identity

- The node mnemonic under `data/<node>/keys/mnemonic` is the single root secret for a managed node.
- The SDN node identity and the managed IPFS identity are both derived from that root instead of being stored as separate long-lived key sources.
- `/api/directory/nodes` and `/api/directory/users` expose the local FlatSQL-backed EPM directory index used by the SDN dashboard and shared runtime adapters.

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
| [plugin-demo](./plugin-demo) | Plugin development guide, WASM API reference, integration tests | C / JS |
| [kubo](./kubo) | IPFS reference implementation | Go |

Canonical module-delivery records live in `spacedatastandards.org`, and SDN consumes the shared runtime helpers from `space-data-module-sdk`. SDN does not ship a separate module-spec tree anymore.

The live licensing/module-delivery flow is carried by the SDS families `LCH`, `LPF`, `LGR`, `LWK`, `LMR`, `PLG`, and `REC`.

Browser and Node apps use public `sdn-js` package surfaces for marketplace
purchase and encrypted module delivery:

- `@spacedatanetwork/sdn-js` for `SDNNode`, `requestModuleGrant`,
  `fetchEncryptedModuleBundle`, `requestEncryptedModuleBundle`, and
  `MODULE_DELIVERY_PROTOCOL_ID`
- `@spacedatanetwork/sdn-js/ui` for PLG listing discovery, grant content-key
  unwrap, encrypted bundle decrypt, SDK browser harness load, and module invoke
- `@spacedatanetwork/sdn-js/storefront` for `createStorefrontClient`, purchase
  request types, payment enums, grant status, and purchase status

The documented third-party flow is in
[`sdn-js/examples/purchase-encrypted-wasm-delivery.ts`](./sdn-js/examples/purchase-encrypted-wasm-delivery.ts):
discover a PLG listing, purchase it through the storefront client, request the
encrypted WASM bundle through `SDNNode`, unwrap and decrypt locally, load with
the SDK browser harness, and invoke the module.

Run the focused module-delivery compatibility checks:

```bash
npm run test:module-delivery
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

## Data Flow

SDN uses a layered data flow architecture: **FlatBuffers** → **FlatSQL** → **PNM** → **PLOG/PLHD** → **Subscriptions**

```
Publisher                                      Subscriber
    │                                               │
    │  1. Build FlatBuffer (OMM, CDM, etc.)         │
    │  2. POST /api/v1/data/publish/{schema}        │
    │     → Validate + append to FlatSQL stream     │
    │     → Compute SHA-256 CID                     │
    │     → Append PLOG entry (hash-chained log)    │
    │                                               │
    │  3. Broadcast PNM via GossipSub ──────────────│──→ Receive PNM
    │     (lightweight notification: CID + schema)  │
    │                                               │  4. Verify signature
    │  5. Publish PLHD (log head) ──────────────────│──→ Check tip/queue config
    │                                               │
    │                                               │  6. If autoFetch: fetch CID
    │  ◄────────────────────────────────────────────│     from publisher or any peer
    │     (SDS exchange protocol)                   │
    │                                               │  7. Store in local FlatSQL
    │                                               │  8. Fire onMessage(schema, data, peerId)
```

### FlatSQL Storage

FlatBuffer bytes are stored in append-only FlatSQL stream files under the node data directory. SQLite schema tables are canonical SDS names such as `OMM`, `MPE`, `CAT`, and `SPW`, and store only metadata needed to locate the bytes: `cid`, `peer_id`, `timestamp`, `stream_path`, `stream_offset`, `record_length`, and `signature_hex`. Source/provenance rows are keyed by provider, source, batch, producer peer ID, and producer public key. Content is addressed by SHA-256 CID — the same FlatBuffer bytes always produce the same hash.

### Publication Logs (PLOG/PLHD)

Each publisher maintains a per-schema **hash-chained log** for efficient incremental sync:

- **PLOG** — Append-only log entries with sequence numbers, chain links, and Ed25519 signatures
- **PLHD** — Lightweight log head announcements broadcast when the log advances
- Subscribers compare HEAD_SEQUENCE against last_synced to determine the delta
- Full chain verification: recompute hashes + verify signatures

### PNM Tip/Queue System

**Publish Notification Messages (PNM)** decouple content storage from notification. Nodes configure auto-fetch, auto-pin, and TTL per-source AND per-schema:

| Setting | Description |
|---------|-------------|
| **Per-schema defaults** | E.g., always fetch CDM (conjunction data) |
| **Per-source overrides** | E.g., trust data from partner organizations |
| **Per-source+schema** | E.g., special handling for OMM from trusted source |

### Encrypted Messages

SDN supports end-to-end encryption for private data:

Encrypted conjunction assessment can screen private maneuver ephemeris without broadcasting
planned maneuvers to competitors. Operators can submit protected MPE/EPM inputs
through grant-scoped channels, receive threshold CA results, and preserve
provenance without exposing maneuver intent to the public network.

| Mode | Algorithm | Use Case |
|------|-----------|----------|
| **ECIES** | X25519 + ChaCha20-Poly1305 | Per-message encryption |
| **SessionKey** | AES-256-GCM | Bulk streaming |
| **Hybrid** | Plaintext header + encrypted payload | Routable encrypted data |

### Streaming Subscriptions

Three delivery modes: **Single** (on-demand), **Streaming** (real-time), **Batch** (periodic). Subscriptions support schema/peer filtering, rate limiting, and priority routing.

### Browser Nodes (sdn-js)

The sdn-js SDK turns any browser or Node.js process into a full SDN peer. A browser with the same mnemonic as a server node has the **same cryptographic identity** — users ARE their HD wallet keys.

See [docs/docs.html](./docs/docs.html) and [plugin-demo/](./plugin-demo/) for complete architecture documentation.

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

### Marketplace CLI

The CLI can search local daemon storefront listings with the same row, JSON, and
CSV output conventions used by provider/data search commands:

```bash
spacedatanetwork marketplace list --format table
spacedatanetwork marketplace search maneuver --standard MPE --kind data_stream --tag encrypted --access-type streaming
spacedatanetwork marketplace search ca --kind wasm_module --provider-id 12D3KooW... --format json
spacedatanetwork marketplace show listing-mpe-alpha --format csv
```

Supported filters include provider peer ID, SDS/data type (`--standard` or
`--data-type`), listing kind (`data_stream` or `wasm_module`), tags, access type
(`one-time`, `subscription`, `streaming`, `query`), limit, and offset.

### Field-Encrypted Stream Inspection

Protected marketplace streams can carry SDS `FSM` field-stream messages where
each field is explicitly marked `Public`, `Encrypted`, `Redacted`, or
`Unavailable`. The CLI can inspect an `FSM` message from a file or stdin and
prints only field visibility metadata, lengths, policy IDs, key epochs, and
decisions. It does not print plaintext values, ciphertext bytes, nonces, tags,
AAD hashes, or provider signatures.

```bash
spacedatanetwork channels field-stream message.fsm
spacedatanetwork channels field-stream message.fsm --format json
spacedatanetwork channels field-stream message.fsm --format csv
cat message.fsm | spacedatanetwork channels field-stream - --format table
```

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
- For `--skip-build`, it requires `PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX` and `--artifact-private-key-file <path>`.

The command uses the standardized plugin task:

```bash
npm run build:key-server
```

It then copies/decrypts the generated encrypted artifact, boots SDN with a temporary plugin catalog, and verifies:

- `/api/v1/plugins/manifest` reports the module-delivery plugin id as `running`
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
   - For `--skip-build`, pass `--artifact-private-key-file <path>` pointing to the matching private key file
4. Run:
   ```bash
npm run plugin-harness -- /path/to/private-plugin-repo
   ```
5. The harness validates the plugin lifecycle and plugin API endpoints in SDN.

Use `--skip-build` when reusing staged artifacts already in CI:

```bash
export PLUGIN_KEY_SERVER_ARTIFACT_PUBLIC_KEY_HEX=<public_hex>
npm run plugin-harness -- /path/to/private-plugin-repo --skip-build --artifact-dir /path/to/Build/plugin/licensing-server --artifact-private-key-file /secure/artifact-private-key.hex
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

### Local Admin Dev Wallet

`npm run admin:dev`, [`config/dev.yaml`](./config/dev.yaml), and [`config/dev-docker.yaml`](./config/dev-docker.yaml) all use the tracked local-only wallet in [`config/dev-wallet.env`](./config/dev-wallet.env).

That file contains the mnemonic, xpub, and derivation path for the local dev admin identity. It is meant only for local development, and the production deploy script refuses to deploy if that xpub appears in a production config.

### Suite Version Pinning

The suite-wide version contract lives in [`suite.versions.json`](./suite.versions.json).

That manifest is the canonical source for:

- the suite release version
- the pinned `spacedatastandards.org` version
- the pinned `hd-wallet-wasm` and `hd-wallet-ui` versions
- the pinned IPFS WebUI version
- the current advertised SDN protocol flag

Generated readers are checked into:

- [`sdn-js/src/version-info.generated.ts`](./sdn-js/src/version-info.generated.ts)
- [`sdn-server/internal/versioninfo/generated.go`](./sdn-server/internal/versioninfo/generated.go)

When the manifest changes, regenerate those files with:

```bash
npm run generate:versions
```

And verify repo-wide consistency with:

```bash
npm run check:versions
```

---

## Documentation

Full documentation is available at [spacedatanetwork.org](https://spacedatanetwork.org/) or locally at [docs/docs.html](./docs/docs.html).

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
- **Data Flow Architecture** — FlatBuffers, FlatSQL, PNM, PLOG/PLHD, streaming, encryption
- **Wallet Identity** — HD key derivation, TOFU binding, multi-account
- **Browser Nodes** — sdn-js as a full network peer
- **Plugin System** — WASM API, host functions, lifecycle

---

## Links

- [spacedatanetwork.org](https://spacedatanetwork.org/)
- [GitHub](https://github.com/DigitalArsenal/space-data-network)
- [Space Data Standards](https://spacedatastandards.org)
- [SDN JS Source](https://github.com/DigitalArsenal/space-data-network/tree/main/sdn-js)

---

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for guidelines.

## License

MIT License - see [LICENSE](./LICENSE) for details.

---

<p align="center">
  <strong>Built for the space community</strong>
</p>
