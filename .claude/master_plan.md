# Space Data Network — Master Plan

**Owner:** DigitalArsenal.io, Inc.
**Last Updated:** 2026-02-11
**Status:** Draft v1.0

---

## Table of Contents

1. [Vision & Narrative](#1-vision--narrative)
2. [Ecosystem Map](#2-ecosystem-map)
3. [Technical Architecture](#3-technical-architecture)
4. [Website Unification Strategy](#4-website-unification-strategy)
5. [Commercialization Strategy](#5-commercialization-strategy)
6. [Pitch Deck Outline](#6-pitch-deck-outline)
7. [Funding & Grant Opportunities](#7-funding--grant-opportunities)
8. [Marketing Strategy](#8-marketing-strategy)
9. [Roadmap & Milestones](#9-roadmap--milestones)
10. [Risk Analysis & Mitigations](#10-risk-analysis--mitigations)
11. [Appendix: Repository Index](#11-appendix-repository-index)
12. [24-Hour SpaceAware.io Launch Plan (Free Tier)](#12-24-hour-spaceawareio-launch-plan-free-tier)

---

## 1. Vision & Narrative

### The One-Liner

> **Space Data Network is the TCP/IP of space — an open, decentralized protocol for exchanging standardized space data, powered by an ecosystem of commercial tools that make space operations accessible to everyone.**

### The Problem

The space industry is fragmented by:

- **Proprietary silos** — Every operator, agency, and vendor uses incompatible formats and closed networks. Getting conjunction data from one entity to another requires bilateral agreements, email chains, or intermediary organizations.
- **Single points of failure** — Centralized clearinghouses (Space-Track.org, CelesTrak, 18th SDS) can go down, get defunded, or become geopolitically contested. When they do, the entire SSA ecosystem stalls.
- **Legacy data formats** — TLEs were designed for punch cards in the 1960s. VCMs and CDMs are exchanged as flat text files. There's no modern, type-safe, high-performance serialization standard.
- **No marketplace** — An operator with a high-fidelity radar wants to sell observations to a satellite operator who needs them. Today there's no trustless, automated way to do this. Every transaction requires lawyers and contracts.
- **No accessible tooling** — Orbital mechanics software costs $50K–$500K/year (STK, FreeFlyer, GMAT). Hardware-in-the-loop spacecraft simulation requires million-dollar facilities.

### The Solution

A vertically-integrated, open-core ecosystem:

| Layer | Open Source (Free) | Commercial (Revenue) |
|-------|-------------------|---------------------|
| **Standards** | Space Data Standards (127 FlatBuffers schemas) | — |
| **Serialization** | flatc-wasm (FlatBuffers compiler in WASM) | — |
| **Query** | FlatSQL (SQL over FlatBuffers) | — |
| **Identity & Crypto** | hd-wallet-wasm (HD wallets, signing) | — |
| **Network** | Space Data Network (P2P protocol) | Data Marketplace (transaction fees) |
| **Simulation** | Tudat-WASM, Basilisk-WASM | — |
| **Visualization** | CesiumJS (base) | SpaceAware.io (OrbPro2/3 embedded) |
| **AI/NLP** | — | SpaceAware.io (MCP NLP globe control) |
| **Modeling & Sim** | — | SpaceAware.io (ModSim plugins) |
| **Platform** | — | SpaceAware.io (SaaS — all commercial features) |

### Why Now

1. **Regulatory momentum** — The UN COPUOS Long-term Sustainability Guidelines, FCC orbital debris rules, and proposed Space Traffic Management frameworks all require better data sharing.
2. **Commercial SSA explosion** — LeoLabs, ExoAnalytic, Kayhan Space, Slingshot Aerospace are proving there's a market for SSA data and tools.
3. **WASM maturity** — WebAssembly now enables full astrodynamics simulation in the browser. No installs, no license servers, no vendor lock-in.
4. **Decentralization technology** — libp2p, IPFS, and FlatBuffers are production-ready for building trustless data networks.
5. **AI integration** — MCP (Model Context Protocol) enables natural-language interfaces for space operations, dramatically lowering the barrier to entry.

---

## 2. Ecosystem Map

```
┌─────────────────────────────────────────────────────────────────────┐
│                     USER-FACING PRODUCTS                            │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐   │
│  │                    SpaceAware.io (SaaS)                      │   │
│  │   $0 Free · $10 Explorer · $20 Analyst · $30 Operator ·     │   │
│  │   $40 Mission — all per-seat/month                           │   │
│  ├───────────────────────────────────────────────────────────────┤   │
│  │  OrbPro2 Engine │ OrbPro2-MCP (NLP) │ ModSim (18 plugins)   │   │
│  │  (visualization)│ (AI globe control) │ (608 entity types)    │   │
│  └──────────────────────────┬───────────────────────────────────┘   │
│                             │                                       │
├─────────────────────────────┼───────────────────────────────────────┤
│                     OPEN INFRASTRUCTURE                             │
│                             │                                       │
│  ┌──────────────────────────┴───────────────────────────────────┐   │
│  │              Space Data Network (P2P Protocol)               │   │
│  │   libp2p · GossipSub · DHT · Circuit Relay · Marketplace    │   │
│  └───┬──────────┬────────────┬──────────────┬───────────────────┘   │
│      │          │            │              │                       │
│  ┌───┴───┐  ┌──┴─────┐  ┌──┴───────┐  ┌──┴──────────────┐        │
│  │FlatSQL│  │flatc   │  │hd-wallet │  │Space Data       │        │
│  │(Query)│  │-wasm   │  │-wasm     │  │Standards (127)  │        │
│  └───────┘  │(Serial)│  │(Identity)│  └─────────────────┘        │
│             └────────┘  └──────────┘                              │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │  Simulation Engines: Tudat-WASM + Basilisk-WASM (310+ cls)  │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

### Repository-to-Product Mapping

| Repository | Role | License | Revenue Model |
|---|---|---|---|
| `space-data-network` | P2P protocol, marketplace | MIT | Transaction fees |
| `spacedatastandards.org` | Schema definitions + website | Apache 2.0 | None (drives adoption) |
| `flatbuffers/wasm` | Serialization engine | Apache 2.0 | None (drives adoption) |
| `flatsql` | Query engine | Apache 2.0 | None (drives adoption) |
| `hd-wallet-wasm` | Identity & crypto | Apache 2.0 | None (drives adoption) |
| `tudat-wasm` | Astrodynamics engine | BSD 3-Clause | None (drives adoption) |
| `basilisk` | Spacecraft simulation | ISC | None (drives adoption) |
| `OrbPro` | 3D visualization engine | Proprietary | Powers SpaceAware.io |
| `OrbPro2-MCP` | AI-powered globe control | Proprietary | SpaceAware.io $30+ tier |
| `OrbPro2-ModSim` | Modeling & simulation | Proprietary | SpaceAware.io $40 tier |
| `WEBGPU_OrbPro3` | Next-gen rendering engine | Proprietary | Future SpaceAware.io upgrade |
| `spaceaware.io` | SaaS platform | Proprietary | Per-seat subscriptions ($0-$40/mo) |

---

## 3. Technical Architecture

This section details how data flows through the Space Data Network ecosystem — from serialization with FlatBuffers, to queryable storage via FlatSQL, to real-time streaming into OrbPro and other clients.

### 3.1 FlatBuffers & Space Data Standards

All data in the ecosystem is serialized using [FlatBuffers](https://google.github.io/flatbuffers/), Google's zero-copy serialization library. The **Space Data Standards (SDS)** define 127 FlatBuffer schemas covering every domain of space operations:

| Category | Schemas | Examples |
|----------|---------|----------|
| **Orbital Data** | OMM, OEM, OCM, OSM | Orbit Mean-elements, Ephemeris, Comprehensive, State Messages |
| **Conjunction Assessment** | CDM, CSM | Conjunction Data Messages, Conjunction Summaries |
| **Tracking** | TDM, RFM | Tracking Data Messages, Reference Frame Messages |
| **Catalog** | CAT, SIT | Object Catalogs, Satellite Information |
| **Entity/Identity** | EPM, PNM, IDM | Entity Profiles, Publish Notifications, Identity Messages |
| **Environment** | MET, EOO, EOP | Meteorological, Earth Orientation, Atmospheric data |
| **Marketplace** | STF, PLG, PLK, PUR, ACL | Storefront Listings, Plugins, Purchase Records, Access Control |

**Why FlatBuffers (not Protobuf, JSON, or XML):**
- **Zero-copy deserialization** — access fields directly from the wire buffer without parsing (~5ns per record, 250M ops/sec on M3 Ultra)
- **Schema evolution** — add fields without breaking existing readers
- **13-language code generation** — C++, C#, Go, Java, JavaScript, TypeScript, Python, Rust, Swift, Kotlin, Dart, Lua, PHP via `flatc-wasm`
- **Compact binary format** — 10-100x smaller than equivalent XML/JSON
- **No runtime dependency** — generated code is self-contained

**Compilation pipeline:**
```
.fbs schema files (spacedatastandards.org)
        ↓
flatc-wasm (FlatBuffers compiler running in WASM)
        ↓
├── Go structs      → sdn-server
├── TypeScript      → sdn-js (browser/Node.js)
├── C++ headers     → OrbPro / native clients
└── C# classes      → .NET integrations
```

**Server-side fluent builders (Go):**
```go
omm := sds.NewOMMBuilder().
    WithObjectName("ISS (ZARYA)").
    WithNoradCatID(25544).
    WithEpoch("2026-02-09T12:00:00Z").
    WithMeanMotion(15.489).
    Build()  // → FlatBuffer binary, ready for network transmission
```

### 3.2 FlatSQL — SQL over FlatBuffers

FlatSQL bridges the gap between FlatBuffers' binary efficiency and SQL's query power. It implements **SQLite virtual tables** that operate directly on FlatBuffer binary data.

**How it works in the SDN server:**

Each SDS schema type gets a dedicated SQLite table with content-addressed storage:

```sql
CREATE TABLE IF NOT EXISTS <schema_type> (
    cid        TEXT PRIMARY KEY,    -- Content Identifier (SHA-256 hash)
    peer_id    TEXT NOT NULL,       -- Source peer
    timestamp  INTEGER NOT NULL,    -- When received
    data       BLOB NOT NULL,       -- Raw FlatBuffer binary
    signature  BLOB,                -- Ed25519 signature
    created_at INTEGER DEFAULT (strftime('%s', 'now'))
);
CREATE INDEX idx_<schema>_peer_ts ON <schema> (peer_id, timestamp);
```

**Query capabilities:**
- `Get(cid)` — retrieve a specific record by content hash
- `Query(schemaType, filters)` — filter by schema fields
- `QueryWithPeerID(schemaType, peerID)` — all data from a specific peer
- `QuerySince(schemaType, timestamp)` — time-windowed queries for sync

**Why this matters for OrbPro:**
- OrbPro can query historical orbital data with SQL while receiving real-time updates via streaming
- Content addressing (CID) ensures deduplication — the same observation from multiple peers is stored once
- The `peer_id` + `timestamp` index enables fast reconstruction of any peer's data timeline

### 3.3 OrbPro Data Streaming

OrbPro consumes space data through the SDN's real-time streaming infrastructure. Data flows from the P2P network into OrbPro's 3D visualization engine via a multi-layered pipeline.

#### Streaming Protocol Stack

```
┌──────────────────────────────────────────────────────────────┐
│  OrbPro Visualization (CesiumJS fork)                        │
│  Renders: satellites, orbits, sensor FOVs, conjunctions      │
├──────────────────────────────────────────────────────────────┤
│  Subscription Manager                                        │
│  Modes: Single | Real-Time Streaming | Batch                 │
│  Filters: schema, field values, rate limits, TTL             │
├──────────────────────────────────────────────────────────────┤
│  SDN Node (sdn-js in browser, or sdn-server via API)         │
│  GossipSub topics: /spacedatanetwork/sds/<SCHEMA_TYPE>       │
├──────────────────────────────────────────────────────────────┤
│  libp2p Transport                                            │
│  WebSocket | WebTransport (QUIC) | Circuit Relay v2          │
├──────────────────────────────────────────────────────────────┤
│  Noise Encryption + Ed25519 Peer Identity                    │
└──────────────────────────────────────────────────────────────┘
```

#### Publish-Subscribe Flow

**Publishing (any peer):**
1. Serialize data as FlatBuffer using SDS schema
2. Pin content locally → generates CID (content hash)
3. Create a **PNM (Publish Notification Message)** containing: CID, schema FILE_ID, digital signature, multiformat address
4. Broadcast PNM on GossipSub topic `/spacedatanetwork/sds/PNM`

**Subscribing (OrbPro or any client):**
1. Subscribe to relevant GossipSub topics (e.g., `OMM` for orbit data, `CDM` for conjunction alerts)
2. Receive PNM → check subscription config for source peer + schema type
3. If `autoFetch` enabled → retrieve full FlatBuffer payload by CID from DHT/IPFS
4. If `autoPin` enabled → store locally with configurable TTL
5. Deserialize (zero-copy) and render in OrbPro

#### Subscription Modes

| Mode | Behavior | Use Case |
|------|----------|----------|
| **Single** | One-shot request/response | Load catalog on startup |
| **Streaming** | Real-time as messages arrive | Live conjunction alerts, tracking updates |
| **Batch** | Collect N messages or wait T seconds, deliver as group | Bulk ephemeris updates, historical data sync |

#### Advanced Subscription Features

```typescript
// OrbPro subscribes to real-time conjunction alerts with filtering
await node.subscribe('CDM', {
    mode: 'streaming',
    encryption: 'hybrid',           // Accept both encrypted and plaintext
    filters: [{
        field: 'MISS_DISTANCE',
        operator: 'lt',
        value: 1000                  // Only conjunctions < 1km
    }],
    rateLimit: { messagesPerMinute: 120 },
    ttl: 86400,                      // Retain for 24 hours
    autoFetch: true,
    autoPin: true
});
```

#### PNM Tip/Queue System

The server manages incoming PNMs with a priority-based configuration hierarchy:

1. **Source+Schema override** (highest priority) — custom rules per peer per schema
2. **Source override** — trust-based rules per peer
3. **Schema default** — per-schema auto-fetch/pin/TTL settings
4. **System default** (lowest) — global fallback configuration

This allows OrbPro deployments to prioritize data from trusted sources (e.g., auto-fetch all OMMs from 18th SDS, but only manually review data from unknown peers).

#### Wire Protocol

The SDS Exchange protocol (`/spacedatanetwork/sds-exchange/1.0.0`) uses a compact binary format:

```
Byte 0:        Message Type (RequestData=0x01 | PushData=0x02 | Query=0x03 | Response=0x04 | Ack=0x05 | Nack=0x06)
Bytes 1-2:     Schema Name Length
Bytes 3-N:     Schema Name (UTF-8)
Bytes N+1-N+4: Data Length
Bytes N+5-End: FlatBuffer Binary Payload
```

**Routing headers** support directed messaging:
```go
RoutingHeader{
    SchemaType:       "CDM",
    DestinationPeers: []string{peerID1, peerID2},
    TTL:              3,          // Max hops
    Priority:         200,        // 0-255 (higher = more urgent)
    Encrypted:        true,
    SessionKeyID:     "session-abc-123",
}
```

#### Performance

| Metric | Value |
|--------|-------|
| OMM Serialization | 327ns/record |
| OMM Deserialization | 5ns/record (zero-copy) |
| EPM Serialization | 574ns/record |
| PNM Serialization | 207ns/record |
| Max message size | 10MB (configurable) |
| Batch buffer | 1000 messages (configurable) |
| Stress-tested throughput | 10GB+ sustained streaming |

### 3.4 Backend Architecture — SDN Server (Go)

The SDN server (`sdn-server/`) is the backbone full-node implementation, written in Go for performance and cross-platform deployment.

#### Component Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                      HTTP Server (REST API)                      │
│          Setup UI · Admin Dashboard · Peer Management            │
├───────┬──────────┬──────────┬──────────┬──────────┬─────────────┤
│ Admin │  Audit   │   Keys   │  Peers   │ Storage  │ Storefront  │
│ Mgmt  │ Logging  │   Mgmt   │  Mgmt    │ (FlatSQL)│ (Marketplace│
├───────┴──────────┴──────────┴──────────┴──────────┴─────────────┤
│                    SDN Node (internal/node/)                      │
│    libp2p Host · GossipSub · Kademlia DHT · Topic Registry       │
├──────────────────────────────────────────────────────────────────┤
│              Subscription & Streaming Manager                     │
│    Session mgmt · Batch/Stream/Single modes · Rate limiting       │
├──────────────────────────────────────────────────────────────────┤
│                 Protocol Handlers                                 │
│  /sds-exchange/1.0.0 · /id-exchange/1.0.0 · /chat/1.0.0         │
├──────────────────────────────────────────────────────────────────┤
│              libp2p (TCP · WebSocket · WebTransport · Relay)      │
└──────────────────────────────────────────────────────────────────┘
```

#### Core Components

| Component | Location | Purpose |
|-----------|----------|---------|
| **Node** | `internal/node/` | Manages libp2p host, GossipSub, DHT, topic registry, and trusted peer connections |
| **Storage (FlatSQL)** | `internal/storage/` | SQLite-backed FlatBuffer storage with CID-based content addressing |
| **Subscription** | `internal/subscription/` | StreamingManager with per-peer session limits, timeout handling, activity tracking |
| **Protocol** | `internal/protocol/` | SDS Exchange protocol handler — request/push/query/ack with rate limiting |
| **PubSub/PNM** | `internal/pubsub/` | PNM tip queue with priority-based configuration hierarchy |
| **Storefront** | `internal/storefront/` | Data marketplace — catalog, payments (Stripe), ECIES encryption, trust scoring |
| **Peers** | `internal/peers/` | TrustedConnectionGater, peer registry, reputation tracking, trust-based rate limiting |
| **Keys** | `internal/keys/` | Ed25519 signing keys, X25519 encryption keys, key rotation |
| **Admin** | `internal/admin/` | User management, RBAC, setup/initialization workflows |
| **Audit** | `internal/audit/` | Event tracking, action logging, compliance trails |
| **SDS** | `internal/sds/` | Fluent API builders for all schema types, schema validation |
| **vCard** | `internal/vcard/` | Bidirectional EPM ↔ vCard 4.0 conversion, QR code generation |
| **Server** | `internal/server/` | HTTP server — setup interface, admin dashboard, peer management UI |
| **Config** | `internal/config/` | Network, security, storage, PubSub, and discovery configuration |

#### Deployment Modes

| Mode | Binary | Use Case |
|------|--------|----------|
| **Full Node** | `spacedatanetwork` | Public-IP server — full DHT, GossipSub, relay, storage, marketplace |
| **Edge Relay** | `spacedatanetwork-edge` | Lightweight relay node for NAT traversal, minimal storage |

#### Key Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| libp2p-go | v0.46.0 | P2P networking foundation |
| go-libp2p-pubsub | v0.15.0 | GossipSub messaging |
| go-libp2p-kad-dht | v0.36.0 | Kademlia distributed hash table |
| FlatBuffers | v25.12.19 | Serialization runtime |
| go-sqlite3 | v1.14.22 | FlatSQL storage backend |
| wazero | v1.7.0 | WebAssembly runtime (for HD wallet crypto in Go) |

#### Network Topology

```
                    ┌───────────────┐
                    │  Bootstrap    │
                    │  Peers (DHT)  │
                    └───────┬───────┘
                            │
        ┌───────────────────┼───────────────────┐
        │                   │                   │
┌───────┴───────┐   ┌──────┴────────┐   ┌─────┴─────────┐
│  Full Node A  │◄─►│  Full Node B  │◄─►│  Full Node C  │
│  (Public IP)  │   │  (Public IP)  │   │  (Public IP)  │
│  + Relay      │   │  + Relay      │   │  + Relay      │
└───────┬───────┘   └──────┬────────┘   └───────────────┘
        │                  │
   Circuit Relay v2   Circuit Relay v2
        │                  │
┌───────┴───────┐   ┌──────┴────────┐
│  Light Peer   │   │  Light Peer   │
│  (Browser/    │   │  (Desktop,    │
│   OrbPro)     │   │   behind NAT) │
└───────────────┘   └───────────────┘
```

- **Full Nodes:** Public IP, participate in DHT, relay traffic for light peers, run storefront
- **Light Peers:** Browser-based (OrbPro, sdn-js) or desktop behind NAT, connect via Circuit Relay v2
- **Discovery:** Kademlia DHT for global peer discovery + mDNS (`space-data-network-mdns`) for local network

### 3.5 Client Applications

#### A. JavaScript SDK (`sdn-js`) — Browser & Node.js

**Package:** `@spacedatanetwork/sdn-js`

The primary client library for browser-based applications including OrbPro. Implements a full P2P node that runs in-browser via WebAssembly.

| Feature | Details |
|---------|---------|
| **P2P Node** | Full libp2p node with GossipSub, DHT, Circuit Relay |
| **Transports** | WebSocket, WebTransport (QUIC), Circuit Relay v2 |
| **Pub/Sub** | Subscribe/publish to all SDS schema topics |
| **Storage** | IndexedDB for persistent local data |
| **Crypto** | Ed25519 signing via HD wallet WASM module |
| **Streaming** | Single, real-time, and batch subscription modes |
| **Filtering** | Field-level filters (eq, ne, gt, lt, contains, startsWith, in, between) |
| **Storefront** | Full marketplace client — search, purchase, review, sell |
| **Encryption** | ECIES (X25519 + AES-256-GCM) for premium data delivery |

```typescript
import { createSDNNode } from '@spacedatanetwork/sdn-js';

const node = await createSDNNode({ relay: true });

// Subscribe to real-time orbit data
await node.subscribe('OMM', (data, peerId) => {
    // Zero-copy FlatBuffer → render in OrbPro
    updateSatellitePosition(data);
});

// Publish observation data to the network
const cid = await node.publish('OMM', ommFlatBuffer);
```

#### B. Desktop Application (`desktop/`)

An Electron-based native application forked from IPFS Desktop, providing a full SDN node with system tray integration.

| Feature | Details |
|---------|---------|
| **Platforms** | Windows, macOS, Linux |
| **Node Type** | Full IPFS/SDN node with bundled Kubo binary |
| **UI** | Embedded WebUI for node management |
| **System Tray** | Background operation with tray menu |
| **Auto-Update** | Built-in update mechanism |
| **Storage** | Local filesystem-backed IPFS datastore |

#### C. Web UI (`webui/`)

A React-based web interface forked from IPFS WebUI, providing browser-based node management.

| Feature | Details |
|---------|---------|
| **File Browser** | Upload, browse, and manage pinned data |
| **Node Status** | Connection info, bandwidth stats, peer count |
| **Peer Management** | View connected peers, add bootstrap nodes |
| **Settings** | Configuration management for the SDN node |
| **Compatibility** | Works with any SDN/IPFS node via HTTP API |

#### D. Storefront Client (`sdn-js/src/storefront/`)

A TypeScript client for the data marketplace, usable from any JavaScript environment.

| Feature | Details |
|---------|---------|
| **Search** | Filter listings by schema type, price, provider, keywords |
| **Purchase** | Buy data access with Stripe payment integration |
| **Access Grants** | Receive ECIES-encrypted data keyed to your public key |
| **Reviews** | Rate and review data providers |
| **Credits** | Balance tracking and usage management |
| **Seller Tools** | List data products, set pricing, manage subscriptions |

```typescript
const storefront = new StorefrontClient({
    apiBaseUrl: 'https://storefront.spacedatanetwork.io',
    peerId: myNode.peerId,
    sign: async (data) => myNode.sign(data),
});

// Browse premium ephemeris data
const listings = await storefront.searchListings({
    query: 'high-precision ephemeris',
    schemaTypes: ['OEM', 'OCM'],
    maxPrice: 50,
});

// Purchase access
const order = await storefront.purchaseAccess(listings[0].id, {
    paymentMethod: 'stripe',
});
```

#### E. OrbPro Engine (CesiumJS Fork)

The proprietary 3D visualization engine that is the primary consumer of SDN data for SpaceAware.io.

| Feature | Details |
|---------|---------|
| **Rendering** | CesiumJS fork with orbital-specific optimizations |
| **Data Source** | Connects to SDN via embedded sdn-js node |
| **Sensor Modeling** | Conic, rectangular, and custom sensor FOV volumes |
| **Access Analysis** | Range → body occlusion → FOV → terrain multi-tier checks |
| **Viewshed** | GPU-accelerated terrain visibility via shadow maps |
| **Propagation** | SGP4 (WASM-accelerated) + Tudat high-fidelity |
| **Simulation** | Basilisk spacecraft simulation (attitude, thrusters, sensors) |
| **AI Control** | OrbPro2-MCP enables NLP commands ("Show Starlink over Europe") |
| **ModSim** | 18 WASM plugins, 608 entity types for combat/mission simulation |

#### Client Comparison Matrix

| Capability | sdn-js (Browser) | Desktop | Web UI | Storefront | OrbPro |
|------------|:-:|:-:|:-:|:-:|:-:|
| P2P Node | Full (light) | Full | — | — | Full (light) |
| Pub/Sub | Yes | Yes | — | — | Yes |
| FlatBuffer Decode | Yes (WASM) | Yes | — | — | Yes (WASM) |
| Local Storage | IndexedDB | Filesystem | — | — | IndexedDB |
| Marketplace | Via StorefrontClient | Via API | — | Yes | Yes |
| 3D Visualization | — | — | — | — | Yes |
| Simulation | — | — | — | — | Yes (Tudat/Basilisk) |
| AI/NLP Control | — | — | — | — | Yes (MCP) |
| Offline Capable | Partial | Full | — | — | Partial |

### 3.6 Homomorphic Encryption — Encrypted Conjunction Assessment

The Space Data Network includes **homomorphic encryption (HE)** built directly into the FlatBuffers serialization layer, enabling conjunction assessment on encrypted satellite ephemeris data. This is the absolute minimum foundation for a functional, secure space traffic management system.

#### The Problem: Why Operators Don't Share Their Best Data

| Barrier | Details |
|---------|---------|
| **National Security** | Military and intelligence satellites have classified orbits. Sharing precise ephemeris reveals capabilities, coverage gaps, and mission intent. |
| **Commercial IP** | Constellation geometry represents billions in investment. Orbital slots, station-keeping strategies, and coverage patterns are proprietary trade secrets. |
| **Liability Exposure** | Sharing data creates legal obligations. If you share ephemeris and a conjunction is missed, you may bear greater liability than if you shared nothing. |
| **Trust Deficit** | No operator wants to trust a central authority with their most sensitive data. The US DoD's 18th SDS is the de facto authority today, but allies and commercial operators want alternatives. |

#### The Solution: Math on Ciphertext

Using **Microsoft SEAL v4.1.1** (BFV lattice-based scheme), the FlatBuffers fork supports homomorphic arithmetic on encrypted data fields. Operators encrypt their satellite positions with a public key and send them via **direct authenticated libp2p streams** to a mutually-agreed assessor node. The assessor computes squared Euclidean distance entirely on ciphertext; only the assessor can decrypt the result — and the result is a *distance*, not a position.

**Critical architecture decision:** Encrypted ephemeris is **never broadcast on GossipSub** or any public channel. If ciphertexts were public, any peer could grab them and perform HE computations locally against arbitrary probe positions — the HE math doesn't need the secret key. This would allow adversaries to triangulate hidden satellite orbits by binary-searching with fake ephemeris. Instead, ciphertexts flow exclusively through **point-to-point authenticated streams** to the assessor, who is the only entity that ever holds both parties' ciphertexts and performs the computation.

#### How It Works

```
Step 1: Assessor generates HE key pair
        ├── Public key → distributed to operators (via direct stream)
        └── Secret key → kept by assessor only

Step 2: Operators encrypt ephemeris (each independently)
        Operator A: enc(x_a), enc(y_a), enc(z_a) → sent to assessor via direct stream
        Operator B: enc(x_b), enc(y_b), enc(z_b) → sent to assessor via direct stream
        ⚠ Ciphertexts NEVER touch GossipSub or any public channel

Step 3: Assessor computes on encrypted data (NO secret key needed for math)
        ct_dx = Sub(enc(x_a), enc(x_b))
        ct_dy = Sub(enc(y_a), enc(y_b))
        ct_dz = Sub(enc(z_a), enc(z_b))
        ct_dx2 = Mul(ct_dx, ct_dx)
        ct_dy2 = Mul(ct_dy, ct_dy)
        ct_dz2 = Mul(ct_dz, ct_dz)
        ct_dist2 = Add(ct_dx2, Add(ct_dy2, ct_dz2))

Step 4: Assessor decrypts ONLY the distance
        dist² = Decrypt(ct_dist2)
        if dist² < threshold² → CONJUNCTION ALERT
```

**8 homomorphic operations per time step. Zero private positions revealed.**

#### Implementation Details

| Component | Details |
|-----------|---------|
| **HE Library** | Microsoft SEAL v4.1.1, compiled into WASM via Emscripten |
| **Scheme** | BFV (Brakerski/Fan-Vercauteren) — integer arithmetic on lattices |
| **Polynomial Degree** | 4096 (128-bit security) |
| **Ciphertext Size** | ~2KB per encrypted value |
| **Float Encoding** | Fixed-point with scale 2¹⁶ (65536) — ~4-5 decimal digits of precision |
| **Schema Integration** | `he_encrypted` attribute on FlatBuffer numeric fields |
| **Supported Types** | int8–int64, uint8–uint64, float, double, and vectors thereof |
| **Key Serialization** | FlatBuffer schema (`he_context.fbs`) with file identifier `HEBK` |
| **WASI Bindings** | C interface for WASM: `wasi_he_encrypt_int64()`, `wasi_he_add()`, etc. |

#### FlatBuffer Schema Example

```fbs
attribute "he_encrypted";

table EncryptedEphemeris {
  object_id: uint64;             // Public — NORAD catalog ID
  epoch: double;                 // Public — timestamp
  x_km: double (he_encrypted);  // Encrypted — position X
  y_km: double (he_encrypted);  // Encrypted — position Y
  z_km: double (he_encrypted);  // Encrypted — position Z
}
```

#### Operational Implications

1. **Classified satellites can participate** in global conjunction assessment without compromising operational security
2. **Commercial IP is protected** — constellation positioning (a multi-billion-dollar investment) never leaves the operator's control
3. **Liability is reduced** — operators contribute to safety without creating data-sharing obligations
4. **No central authority required** — any SDN full node can serve as an assessor; operators choose by mutual agreement. The role is decentralized even though the data path is point-to-point.
5. **Continuous, not periodic** — operators stream encrypted ephemeris to the assessor via persistent direct streams, enabling continuous conjunction monitoring without batch delays

#### Anti-Probing Defenses (Ephemeris Scanning Attack Mitigation)

**The Attack:** An adversary wants to locate hidden/classified satellites. If encrypted ephemeris were broadcast on the public network (GossipSub), an attacker could grab any operator's ciphertext and perform HE distance computations locally against thousands of probe positions — HE arithmetic doesn't need the secret key. By observing which probes produce "near" results, the attacker triangulates the hidden orbit.

**The Fundamental Defense: Private Data Path**

Encrypted ephemeris **never touches GossipSub or any public channel**. Ciphertexts flow exclusively through direct, authenticated libp2p streams:

```
Operator A ──[direct stream]──► Assessor Node ◄──[direct stream]── Operator B
                                      │
                                 HE computation
                                 (only entity with
                                  both ciphertexts)
                                      │
                              Decrypt → SAFE/ALERT
                                      │
                     ┌────────────────┴────────────────┐
                     ▼                                 ▼
               Operator A                        Operator B
              (binary result)                   (binary result)
```

The assessor is the **only entity that ever holds both parties' ciphertexts**. No other peer on the network can perform HE computations against your ephemeris because they never see it.

The SDN P2P network is still used for:
- **Discovery** — finding assessor nodes and potential assessment partners via DHT
- **Signaling** — handshake/negotiation for assessment sessions
- **Public data** — non-sensitive data (public TLEs, CDMs, catalog objects) still flows over GossipSub
- **Result delivery** — binary SAFE/ALERT notifications

**Defense-in-Depth Layers (beyond the private data path):**

| Layer | Mechanism | What It Prevents |
|-------|-----------|-----------------|
| **1. Assessor as Gatekeeper** | The assessor node performs ALL HE computation and ALL decryption. It enforces policy: both operators must be authenticated, the assessment must be mutually authorized, and each operator's ciphertext is received via their direct authenticated stream. | No third party can compute against your ciphertext because they never have it. The assessor controls the only copy. |
| **2. Bilateral Opt-In** | Both operators must complete a mutual handshake (signed by both HD wallet identities) before the assessor accepts any ciphertexts. Neither party can unilaterally initiate an assessment. | Prevents an attacker from creating fake identities and running assessments against unwilling targets. |
| **3. Identity Staking** | Each SDN identity must post a cryptographic bond or accumulate reputation through verified activity before participating in HE assessments. Malicious behavior triggers slashing. | Makes Sybil attacks expensive — creating many fake identities to probe from different angles requires proportional economic cost. |
| **4. Proof of Custody** | Ephemeris submissions must be corroborated by at least one independent source (radar track from a trusted sensor network, or cross-reference with publicly tracked catalog objects). | Prevents submission of fabricated ephemeris for nonexistent satellites. You can't probe with a "satellite" that doesn't exist. |
| **5. Rate Limiting** | The assessor limits each identity to N assessment sessions per epoch. Limits scale with reputation/stake. | Even if an attacker bypasses bilateral consent (e.g., by colluding with a target), the rate limit caps information extraction. |
| **6. Threshold-Only Output** | Assessments return only a binary SAFE/ALERT, not the computed distance. Precise distance requires both parties to opt in post-alert. | Each query leaks at most 1 bit. Triangulation requires O(log(orbital_volume/threshold)) queries minimum per orbital dimension. |
| **7. Differential Privacy** | The assessor adds calibrated Laplace noise to the distance before threshold comparison. | Even the 1-bit threshold output is noisy — repeated queries give inconsistent answers, defeating systematic probing. |
| **8. Anomaly Detection** | The assessor monitors for scanning signatures: rapid sequential assessments, grid-pattern positions, ephemeris violating Keplerian constraints, single identity requesting assessments against many different operators. | Catches attackers who manage to get through the other layers by detecting statistical patterns across many queries. |

**Protocol Flow:**

```
1. Discovery: Operator A finds Operator B via SDN DHT
   → Both verify each other's identity (HD wallet signatures)
   → Both select a mutually-trusted assessor node

2. Handshake: Three-way mutual authorization
   → A signs: "I authorize assessment with B via Assessor X"
   → B signs: "I authorize assessment with A via Assessor X"
   → Assessor verifies both signatures + stake + rate limits

3. Key Distribution: Assessor generates fresh HE key pair for this session
   → Public key sent to A and B via their direct streams
   → Secret key never leaves the assessor

4. Encrypted Submission: Each operator encrypts and sends via direct stream
   → A sends enc(x_a, y_a, z_a) to Assessor (never broadcast)
   → B sends enc(x_b, y_b, z_b) to Assessor (never broadcast)
   → Assessor validates: proof of custody, Keplerian sanity check

5. Computation: Assessor performs HE distance + adds noise
   → noisy_d² = HE_distance²(A, B) + Laplace(λ)
   → Result: noisy_d² < threshold² → ALERT or SAFE

6. Disclosure: Binary result sent to both operators
   → If ALERT and both opt in → precise distance revealed
   → Assessor logs session for anomaly detection
```

**Why This Is Secure:** The critical insight is that HE arithmetic is permissionless — anyone with ciphertexts can compute on them. Therefore, **ciphertexts themselves must be treated as sensitive**, even though they're encrypted. The private data path ensures that the only entity capable of computing on your ciphertext is the assessor you chose, and the only decryption results you see are for assessments you mutually authorized.

**Decentralization is preserved:** Any SDN full node can serve as an assessor. Operators aren't locked into a single authority — they can choose different assessors for different assessment partners, rotate assessors, or even run their own assessor node. The trust model is: "I trust this specific node to perform my computation honestly" — not "I trust a central authority with all my data forever."

#### Status

- **Working**: Encrypted conjunction assessment test passing (5 time steps, 15km threshold, conjunction detected at 7.3km)
- **Timeline**: Shipping in SDN by end of February 2026
- **Code**: [flatbuffers/tests/he_encryption_test.cpp](https://github.com/DigitalArsenal/flatbuffers/blob/cb41d970d2429736daa8cdadd1c63c3b1b5bf5db/tests/he_encryption_test.cpp#L342)

---

## 4. Website Unification Strategy

### Current State

Each project has its own website with independent styling, navigation, and messaging. A visitor landing on one site has no idea the others exist.

### Target State

All sites share a unified visual identity and cross-link as parts of one ecosystem, while each site remains focused on its specific audience and purpose.

### Unified Design System

**Shared Elements Across All Sites:**
- Common header/nav bar with ecosystem dropdown menu
- Consistent color palette (dark theme primary, space-inspired)
- Shared footer with links to all ecosystem projects
- "Part of the Space Data Network ecosystem" badge
- Consistent typography (monospace for technical, sans-serif for marketing)

### Per-Site Strategy

#### A. **spacedatanetwork.io** (Hub Site — Create New or Rebrand Existing)
- **Audience:** Everyone — first-time visitors, investors, developers, operators
- **Purpose:** Ecosystem landing page and routing
- **Content:**
  - 60-second animated explainer of the full ecosystem
  - "Choose your path" cards: Developer / Operator / Investor / Researcher
  - Live network stats (peer count, messages/day, schemas in use)
  - Links to all sub-sites
  - Blog/news feed

#### B. **spacedatastandards.org** (Standards Body Site)
- **Audience:** Standards committee members, data engineers, schema contributors
- **Purpose:** Schema reference and governance
- **Content Refinements:**
  - Sharpen the hero: "The FlatBuffers standard for space data — 127 schemas, 13 languages, zero-copy performance"
  - Add "Adopted by" logos section (even if it's your own projects initially)
  - Interactive schema explorer (already exists — polish it)
  - Governance page: How to propose new schemas, versioning policy
  - Migration guides: "Convert your TLEs/VCMs/CDMs to SDS"

#### C. **digitalarsenal.io** (Company Site)
- **Audience:** Potential customers, partners, investors
- **Purpose:** Company credibility and commercial offerings
- **Content Refinements:**
  - Position as "The company behind Space Data Network"
  - Products page: OrbPro2, SpaceAware.io, OrbPro2-ModSim
  - Open source portfolio page
  - Team / About / Contact
  - Case studies and testimonials

#### D. **spaceaware.io** (SaaS Platform — To Be Created)
- **Audience:** Satellite operators, SSA analysts, defense/intel users, hobbyists, researchers
- **Purpose:** The single commercial surface for the entire ecosystem
- **Content:**
  - Product features and demo video
  - Pricing page: Free / Explorer $10 / Analyst $20 / Operator $30 / Mission $40 (per-seat)
  - Feature comparison vs. STK, FreeFlyer, GMAT
  - Account creation and login
  - Dashboard screenshots / live free-tier demo
  - Sandcastle gallery of interactive demos
  - API documentation
  - Data marketplace browser
  - Integration docs (how SpaceAware uses SDN, OrbPro, Tudat, Basilisk under the hood)

#### F. **GitHub Pages for Open-Source Repos**
- Each repo (SDN, flatbuffers, flatsql, hd-wallet-wasm, tudat-wasm, basilisk) keeps its GitHub Pages docs
- Add unified header bar linking back to spacedatanetwork.io
- Consistent "Part of the SDN ecosystem" branding

### Implementation Priority

1. Add unified nav/footer component to all existing sites (1-2 weeks)
2. Create spacedatanetwork.io hub landing page (2-3 weeks)
3. Design and launch spaceaware.io MVP (4-6 weeks)
4. Polish OrbPro product pages (2-3 weeks)
5. Update digitalarsenal.io as company hub (1-2 weeks)

---

## 5. Commercialization Strategy

### Primary Revenue: SpaceAware.io (Per-Seat SaaS)

All commercial features — OrbPro visualization, MCP AI control, ModSim simulation, sensor modeling, marketplace access — are delivered through a single product: **SpaceAware.io**. No separate developer licenses. One product, five tiers, per-seat pricing.

#### Tier Overview

| Tier | Price | Theme |
|------|-------|-------|
| **Free** | $0/seat/month | Awareness |
| **Explorer** | $10/seat/month | Share & Save |
| **Analyst** | $20/seat/month | Analyze |
| **Operator** | $30/seat/month | Simulate |
| **Mission** | $40/seat/month | Command |

#### Free — Awareness ($0/seat/month)

The free tier is built on open-source algorithms — generous by design to drive network effects and SDN adoption. **No saving, no link sharing.**

| Feature | Details |
|---------|---------|
| **Conjunction Assessment** | View all public conjunction data messages (CDMs) from the SDN |
| **SGP4 / SGP4-XP Propagation** | WASM-accelerated TLE/GP propagation — standard and extended precision |
| **High-Def Orbit Propagation** | Tudat-WASM numerical propagation (gravity harmonics, drag, SRP, third-body) |
| **3D Globe Visualization** | OrbPro2-powered CesiumJS with full satellite catalog |
| **Crypto Wallet Integration** | HD-Wallet-WASM: BIP-32/39/44 key derivation, Ed25519 signing, peer identity |
| **FIPS 140-3 Encryption** | NIST-validated cryptographic module for all on-wire data |
| **vCards on SDN** | View and publish entity profile cards (EPM ↔ vCard 4.0) on the Space Data Network |
| **SDN Light Node** | Built-in P2P node — you're part of the network, contribute to data availability |
| **Object Search** | Search by NORAD ID, name, intl designator, object type |

#### Explorer — Share & Save ($10/seat/month)

For hobbyists, students, and researchers who want to save and share their work.

| Feature | Details |
|---------|---------|
| Everything in Free | + |
| **Link Sharing** | Generate shareable URLs for any view, object, or analysis |
| **10 Saved Scenarios** | Save camera position, tracked objects, time range, and overlays |
| **Data Export** | Export data as CSV, JSON, or FlatBuffers binary |
| **Custom Alerts** | Configure conjunction thresholds, miss distance filters, email + in-app notifications |
| **Embed Widget** | Embeddable 3D globe iframe for websites/blogs |
| **Bookmarks & Tags** | Organize objects into collections with custom tags |

#### Analyst — Analyze ($20/seat/month)

For SSA analysts and researchers who need simulation and maneuver planning.

| Feature | Details |
|---------|---------|
| Everything in Explorer | + |
| **100 Saved Scenarios** | Expanded scenario library |
| **Spacecraft Simulator** | Basilisk-WASM: attitude dynamics, reaction wheels, thrusters, sensors, FSW algorithms (310+ classes) |
| **Lambert Transfer Planning** | Compute minimum-energy transfer orbits between any two points |
| **Hohmann Transfer Planning** | Two-impulse coplanar orbit transfers with delta-V budgets |
| **Sensor FOV Modeling** | Define conic, rectangular, and custom-geometry sensor volumes |
| **Access & Viewshed Analysis** | Multi-tier visibility: range → body occlusion → FOV → terrain |
| **API Access** | REST + WebSocket API — 25K calls/day |
| **Marketplace Browse** | Browse and purchase data/plugins from the SDN marketplace |

#### Operator — Simulate ($30/seat/month)

For satellite operators, defense analysts, and mission planners who need advanced simulation and team coordination.

| Feature | Details |
|---------|---------|
| Everything in Analyst | + |
| **Monte Carlo Analysis** | Batch simulations with parameter variations and statistical output |
| **Missile Trajectory Simulation** | Ballistic and cruise missile flight modeling with atmospheric effects |
| **Launch & Reentry Modeling** | Ascent trajectory planning, reentry corridor analysis, debris footprint prediction |
| **500 Saved Scenarios** | Large scenario library for operational workflows |
| **Operator Chat** | Real-time team messaging with arbitrary groups created through the Space Data Network |
| **AI/NLP Globe Control** | MCP-powered natural language: "Show me all Starlink over Europe" / "Propagate ISS 72 hours" |
| **Collision Avoidance Workflow** | End-to-end CA: detect → assess → plan maneuver → simulate → share CDM |
| **API Access** | 100K calls/day + streaming WebSocket feeds |
| **CZML/KML Export** | Export scenarios for use in other tools |

#### Mission — Command ($40/seat/month)

For mission operations centers, defense teams, and organizations running multi-domain operations.

| Feature | Details |
|---------|---------|
| Everything in Operator | + |
| **RPO / Proximity Operations** | Rendezvous & proximity operations planning — relative motion, approach corridors, safety ellipsoids |
| **Combat/Mission Simulation** | OrbPro2-ModSim: 608 entity types across 35 WASM plugins — air, space, ground, naval, subsurface |
| **Unlimited Saved Scenarios** | No cap on scenario storage |
| **Electronic Warfare (EW)** | Jamming, spoofing, signal analysis, and electromagnetic spectrum modeling |
| **Multi-Domain Simulation** | Aircraft 6DOF, helicopter, naval vessels, submarines (sonar), ground vehicles, ballistics |
| **Sensor Fusion & Tracking** | Multi-sensor data fusion (radar, optical, IR), track correlation, custody management |
| **Fire Control Systems** | Weapons engagement zones, kill chains, target assignment optimization |
| **Damage & Vulnerability Assessment** | Lethality modeling, armor penetration, blast effects, structural vulnerability |
| **Threat Modeling** | Adversary capability analysis, order of battle, threat rings, engagement envelopes |
| **Ground Segment Operations** | Ground station scheduling, contact planning, pass prediction, link margin analysis |
| **Comms & Link Budget** | RF propagation, antenna patterns, interference analysis, link availability |
| **Power & Thermal Analysis** | Solar array modeling, battery state-of-charge, thermal environment simulation |
| **Team Workspaces** | Shared scenarios, annotations, and analysis across your team |
| **Operator Chat (Teams)** | Arbitrary team creation through SDN for cross-org coordination |
| **SSO/SAML** | Enterprise identity provider integration |
| **Marketplace Selling** | List and sell your own data, plugins, and analysis on the marketplace |
| **Unlimited API** | No rate limits, priority queue |
| **Audit Logging** | Full activity audit trail for compliance |
| **On-Prem Deployment** | Self-hosted option for air-gapped / classified networks |

#### Volume Pricing

All tiers are per-seat. Volume discounts for annual commitments:

| Seats | Discount |
|-------|----------|
| 1-4 | List price |
| 5-19 | 15% off |
| 20-49 | 25% off |
| 50+ | Custom agreement |

**Annual billing**: 2 months free (pay for 10, get 12)

#### Revenue Math

| Scenario | Seats | Tier Mix | MRR | ARR |
|----------|-------|----------|-----|-----|
| **Y1 Target** | 500 free, 100 paid | 50x$10 + 30x$20 + 15x$30 + 5x$40 | $1,760 | $21K |
| **Y2 Target** | 2,000 free, 500 paid | 200x$10 + 150x$20 + 100x$30 + 50x$40 | $10,000 | $120K |
| **Y3 Target** | 8,000 free, 2,000 paid | 800x$10 + 600x$20 + 400x$30 + 200x$40 | $40,000 | $480K |
| **Y4 Target** | 25,000 free, 6,000 paid | 2.4Kx$10 + 1.8Kx$20 + 1.2Kx$30 + 600x$40 | $120,000 | $1.44M |
| **Y5 Target** | 75,000 free, 18,000 paid | 7.2Kx$10 + 5.4Kx$20 + 3.6Kx$30 + 1.8Kx$40 | $360,000 | $4.32M |

### Secondary Revenue

#### Stream 2: Data Marketplace Transaction Fees ($$$)

**Built into the SDN protocol's storefront system:**

| Transaction Type | Fee Structure |
|---|---|
| **Data Sales** | 5% platform fee on each transaction |
| **Plugin Sales** | 15% platform fee (plugin marketplace) |
| **Subscription Data Feeds** | 5% of recurring revenue |
| **Free/Open Data** | $0 (always free, drives network effects) |

**Marketplace Categories:**
- Premium ephemeris data (high-precision observations)
- Conjunction analysis reports
- Historical orbital data archives
- Atmospheric density models
- Real-time RF monitoring data
- Debris tracking observations
- Custom propagation algorithms (plugins)
- Sensor tasking results

#### Stream 3: NFT-Based Asset Timeshares ($$$ — Longer Term)

**Concept:** Tokenize time slots and capabilities on on-orbit assets, ground stations, and data centers.

**Use Cases:**

| Asset Type | NFT Represents | Example |
|---|---|---|
| **Satellite observation time** | 1-hour imaging pass over a region | "10 minutes of optical tracking from LEO sat #42 on 2026-03-15" |
| **Ground station access** | Antenna time for uplink/downlink | "S-band pass from Canberra DSN, 15-min slot" |
| **Compute on edge/space** | Processing time on orbital compute nodes | "1 GPU-hour on orbital data center" |
| **Spectrum rights** | Temporary frequency allocation | "X-band 8.2 GHz, 36 MHz bandwidth, 2-hour window" |
| **Data center colocation** | Rack space in SDN-connected facility | "1U rack-month in Ashburn, SDN-peered" |

**Implementation:**
- Use hd-wallet-wasm for key management and signing
- Mint NFTs on Solana (low fees, fast finality) or Ethereum L2
- Smart contracts enforce time-slot ownership and access control
- SDN marketplace handles discovery and payment
- Atomic swaps between data credits and NFTs

**Revenue:** 2.5% minting fee + 1% secondary market royalty

#### Stream 4: Consulting & Integration Services ($$)

- SDN node deployment and configuration
- Custom plugin development for OrbPro2
- Space data pipeline architecture
- Migration from legacy formats to Space Data Standards
- Training and workshops

**Rates:** $250-400/hour, or fixed-price project engagements

### Revenue Projections (Conservative Estimates)

| Year | SpaceAware.io Subs | Marketplace Fees | NFTs | Services | Total |
|------|-------------------|-------------------|------|----------|-------|
| **Y1** | $21K | $10K | $0 | $75K | **$106K** |
| **Y2** | $120K | $75K | $25K | $100K | **$320K** |
| **Y3** | $480K | $250K | $100K | $200K | **$1.03M** |
| **Y4** | $1.44M | $750K | $300K | $350K | **$2.84M** |
| **Y5** | $4.32M | $2M | $750K | $500K | **$7.57M** |

---

## 6. Pitch Deck Outline

### Slide Structure (12 slides, 3-minute pitch)

**Slide 1 — Title**
> Space Data Network: The Open Protocol for Space Data Exchange
> DigitalArsenal.io, Inc.

**Slide 2 — The Problem**
- 15,000+ active satellites, 30,000+ tracked objects, growing rapidly
- Data exchange relies on email, FTP, and 1960s-era formats (TLE)
- $50K-$500K/year for orbital mechanics software
- No standardized marketplace for space data
- Starlink alone = half of all active satellites, yet sharing data is ad-hoc

**Slide 3 — The Solution**
- Decentralized P2P protocol (like BitTorrent for space data)
- 127 standardized schemas (like HTTP content types for space)
- Everything runs in the browser via WebAssembly
- Open infrastructure, commercial products on top

**Slide 4 — How It Works (Architecture)**
- [Ecosystem diagram from Section 2]
- Open standards layer → Open network layer → Commercial products
- "We built TCP/IP for space, and we're selling Cisco routers"

**Slide 5 — The Open Source Moat**
- Space Data Standards: 127 schemas, 13 languages, adopted as foundation
- FlatBuffers WASM: Zero-dependency serialization in browser
- FlatSQL: SQL queries over binary data at 580K ops/sec
- Tudat + Basilisk WASM: Full astrodynamics simulation in browser (world first)
- HD-Wallet-WASM: Cryptographic identity and blockchain integration
- **10 years of R&D, 100K+ lines of code, impossible to replicate quickly**

**Slide 6 — Commercial Product: SpaceAware.io**
- One product, five tiers, $0-$40/seat/month — replaces $500K/yr STK
- Free tier: full catalog + history + conjunction data + upload to SDN (drives adoption)
- Paid tiers progressively unlock: sharing → sensor analysis → simulation → mission ops
- Built on OrbPro2 engine, Tudat/Basilisk WASM, MCP AI control, ModSim plugins
- Data Marketplace baked in — 5% transaction fees on a growing $2B+ SSA market

**Slide 7 — Demo / Screenshots**
- OrbPro2 3D visualization with sensor modeling
- Natural language control via MCP ("Show me all Starlink satellites over Europe")
- Basilisk spacecraft simulation running in browser
- Data marketplace storefront

**Slide 8 — Market Size**
- Space Situational Awareness market: $1.5B (2025) → $3.6B (2030) — 19% CAGR
- Earth Observation data market: $7.5B (2025) → $14.5B (2030)
- Space simulation software: $2.1B (2025) → $4.8B (2030)
- Our SAM (Serviceable Addressable Market): $800M by 2030

**Slide 9 — Business Model**
- Revenue mix: SaaS (55%) + Marketplace (20%) + Services (15%) + NFTs (10%)
- Gross margins: 90%+ (pure SaaS, no hardware)
- Free tier drives SDN adoption → network effects → marketplace flywheel
- Per-seat pricing: predictable, scalable, low friction to start

**Slide 10 — Traction & Validation**
- [Insert current metrics: GitHub stars, npm downloads, node count]
- Space Data Standards used by [list any adopters]
- FlatBuffers WASM: [npm download count] downloads
- OrbPro: [customer/pilot count]
- Government interest: [any LOIs, pilots, or conversations]

**Slide 11 — Team**
- [Founder/team bios]
- Advisory board (if any)
- Key technical accomplishments (first WASM astrodynamics simulation, etc.)

**Slide 12 — The Ask**
- Raising: $[X]M Seed / Series A
- Use of funds: 40% Engineering, 25% GTM, 20% Operations, 15% Reserve
- Key milestones for next 18 months
- Contact info

### Supplementary Slides (Appendix)

- **Competitive Landscape**: STK vs. FreeFlyer vs. GMAT vs. SpaceAware.io feature matrix
- **Technical Deep Dive**: Architecture, performance benchmarks, security model
- **Customer Pipeline**: LOIs, pilots, conversations
- **IP Portfolio**: List of key innovations and potential patents
- **NFT Asset Tokenization**: Detailed vision for satellite timeshare marketplace

---

## 7. Funding & Grant Opportunities

### Government Grants & Programs

#### A. Space-Specific

| Program | Agency | Relevance | Amount | Deadline |
|---------|--------|-----------|--------|----------|
| **SBIR/STTR Phase I** | NASA | Space data standards, SSA tools | $150K | Rolling (multiple solicitations/year) |
| **SBIR/STTR Phase II** | NASA | Follow-on from Phase I | $750K | By invitation |
| **AFRL SBIR** | Air Force / Space Force | SSA, space domain awareness | $150K-$1.5M | Rolling |
| **SpaceWERX SBIR/STTR** | Space Force | Space technology commercialization | $50K-$1.5M | Rolling |
| **SpaceWERX Orbital Prime** | Space Force | On-orbit servicing data exchange | Varies | Periodic |
| **DARPA** | DoD | Decentralized space networks, autonomous SSA | $500K-$5M | Topic-dependent |
| **SDA (Space Development Agency)** | DoD | Proliferated LEO data mesh | Varies | BAAs |
| **NOAA** | Commerce | Space weather data standards | $100K-$500K | Annual |
| **NSF Convergence Accelerator** | NSF | Open-source infrastructure for national needs | $750K (Phase 1), $5M (Phase 2) | Annual |
| **NIST** | Commerce | Data standards development | $50K-$300K | Varies |

#### B. Technology-General

| Program | Agency | Relevance | Amount |
|---------|--------|-----------|--------|
| **NSF POSE** | NSF | Pathways to Open-Source Ecosystems | $300K-$1.5M |
| **ARPA-H / ARPA-E** | DoE/HHS | Novel use of decentralized data networks | $1M-$10M |
| **NTIA Public Wireless** | Commerce | Open-source network infrastructure | Varies |

### Venture Capital — Space-Focused

| Firm | Focus | Stage | Notable Investments |
|------|-------|-------|-------------------|
| **Space Capital** | Space infrastructure | Seed-B | Kayhan Space, LeoLabs, Privateer |
| **Seraphim Space** | Space tech | Seed-B | D-Orbit, Spire, HawkEye 360 |
| **Promus Ventures** | Space & defense | Seed-A | |
| **Type One Ventures** | Deep tech / space | Pre-Seed-A | |
| **Stellar Ventures** | Space startups | Seed-A | |
| **Lockheed Martin Ventures** | Defense/space tech | Series A+ | |
| **Boeing HorizonX** | Aerospace tech | Series A+ | |
| **Airbus Ventures** | Aerospace innovation | Series A+ | |
| **In-Q-Tel** | Intelligence community tech | Any stage | Critical for gov adoption |
| **USIT (US Innovative Technology Fund)** | National security tech | Seed-B | |

### Venture Capital — Deep Tech / Open Source

| Firm | Focus | Stage | Notable Investments |
|------|-------|-------|-------------------|
| **a16z** | Open source / crypto / infra | Seed-Growth | Protocol Labs (IPFS) |
| **Sequoia** | Infrastructure / platform | Seed-Growth | |
| **OSS Capital** | Open-source-first companies | Seed | |
| **Heavybit** | Developer tools | Seed-A | |
| **Boldstart Ventures** | Developer-first startups | Pre-Seed-Seed | |
| **Gradient Ventures** (Google) | AI + infrastructure | Seed-A | |
| **Multicoin Capital** | Crypto / decentralized protocols | Seed-B | |
| **Polychain Capital** | Decentralized infrastructure | Seed-B | |

### Strategic Partners & Accelerators

| Organization | Type | Value |
|---|---|---|
| **Techstars Allied Space** | Accelerator | $120K + mentorship + USAF/Space Force connections |
| **Starburst Aerospace** | Accelerator | Defense/space customer intros |
| **AFWERX** | DoD innovation | Direct path to Space Force contracts |
| **Plug and Play Space** | Accelerator | Corporate partner connections |
| **Creative Destruction Lab (Space)** | Accelerator | Canadian space ecosystem |
| **ESA BIC** | Incubator | European Space Agency business incubation |
| **Y Combinator** | Accelerator | General — but has funded space companies |
| **Microsoft for Startups** | Program | Azure credits, AI tools, go-to-market |
| **AWS Space** | Program | Cloud credits, ground station partnerships |
| **Google for Startups** | Program | GCP credits, AI/ML resources |

### Non-Dilutive Funding Strategies

1. **NASA SBIR Fast-Track**: Submit Phase I, immediately apply for Phase II bridge
2. **SpaceWERX Pitch Days**: Rapid contracting (days, not months)
3. **CDAO (Chief Digital & AI Office)**: AI/data standards adoption across DoD
4. **NIST Standards Grants**: Funding for standards development and adoption
5. **Open Source Security Foundation (OpenSSF)**: Grants for security-critical open-source infrastructure

---

## 8. Marketing Strategy

### Brand Positioning

**Tagline Options:**
- "The Open Protocol for Space Data"
- "Space Awareness for Everyone"
- "From Orbit to Insight — Open, Decentralized, Unstoppable"

**Key Messages:**
1. **For Developers**: "Build space applications with zero-copy data, browser-native simulation, and a global P2P network — all open source."
2. **For Operators**: "Real-time conjunction alerts, high-fidelity propagation, and a marketplace for the data you need — at 1/20th the cost of legacy tools."
3. **For Investors**: "The open-core company building the data layer for the $400B space economy."
4. **For Government**: "Resilient, decentralized SSA infrastructure that can't be denied, degraded, or destroyed."

### Content Strategy — Claude Teams Agentic Approach

#### Video Content Pipeline

Use Claude Teams to script, storyboard, and produce content at scale:

**1. Explainer Videos (4-6 per quarter)**
- "What is Space Data Network?" (2-min animated explainer)
- "How SDN Replaces Email-Based Conjunction Warnings" (3-min demo)
- "OrbPro2: STK-Level Analysis in Your Browser" (5-min product demo)
- "Building a Satellite Tracker in 10 Minutes with SDN" (tutorial)

**Production Workflow with Claude Teams:**
```
1. Claude writes script + shot list from product docs
2. Human reviews/approves script
3. Claude generates Manim/Motion Canvas animation code
4. Claude creates voiceover script optimized for ElevenLabs/TTS
5. Human records or generates voiceover
6. Claude writes YouTube description, tags, chapters, social posts
7. Human publishes, Claude drafts follow-up engagement responses
```

**2. Demo/Tutorial Videos (2 per week)**
- Screen recordings with Claude-generated narration scripts
- "Build X with SDN" series (satellite tracker, conjunction alerter, data marketplace listing)
- Use OrbPro2 Sandcastle gallery as demo content

**3. Conference Talk Prep**
- Claude drafts presentations from master plan content
- Generates speaker notes and Q&A preparation
- Creates one-pagers and leave-behinds

#### Written Content Pipeline

**LinkedIn Articles (1-2 per week)**

Claude Teams workflow:
```
1. Provide Claude with topic + key data points
2. Claude drafts 800-1200 word article with:
   - Hook (surprising stat or question)
   - Problem framing
   - Solution narrative (naturally incorporating SDN)
   - Call to action
3. Human reviews, adds personal anecdotes
4. Claude generates 5 LinkedIn post variations (different hooks)
5. Schedule across the week with different angles
```

**Article Topic Calendar:**
- Week 1: "Why TLEs Need to Die" (problem awareness)
- Week 2: "I Ran Spacecraft Simulation in My Browser" (technical wow)
- Week 3: "The $500K Software That Should Be Free" (market disruption)
- Week 4: "Decentralizing Space Data: Lessons from BitTorrent" (technical vision)
- Week 5: "127 Standards, 13 Languages, Zero Vendor Lock-in" (standards story)
- Week 6: "NFTs for Satellite Time — Crazy or Inevitable?" (future vision)
- Repeat themes with fresh angles

**Blog Posts (2-4 per month)**
- Technical deep dives for developer audience
- Product announcements and updates
- Case studies (even hypothetical initially)
- "State of the Network" monthly reports

#### Social Media Strategy

**LinkedIn (Primary Channel)**
- Target: Space industry professionals, defense procurement, VC
- Post frequency: 5x/week
- Mix: 40% thought leadership, 30% product demos, 20% industry commentary, 10% team/culture
- Use Claude to draft all posts, schedule with Buffer/Hootsuite

**Twitter/X (Secondary)**
- Target: Developers, crypto/web3 community, space enthusiasts
- Post frequency: 3-5x/day (threads + quick updates)
- Live-tweet conference attendance
- Engage with space industry accounts

**YouTube (Demos & Tutorials)**
- Post frequency: 2x/week
- Optimize thumbnails and titles with Claude
- Build playlist structure: Tutorials / Product Demos / Talks / Explainers

**GitHub (Community Building)**
- Maintain active discussions and issue engagement
- Monthly "What's New" releases with changelogs
- Contributor spotlights
- "Good First Issue" labels for onboarding

#### Paid Advertising

**Phase 1 (Months 1-6): Awareness**
- LinkedIn Ads targeting: Space industry titles (SSA analyst, satellite operator, astrodynamics engineer)
- Google Ads: Keywords around "orbital mechanics software", "SSA tools", "conjunction assessment"
- Budget: $2K-5K/month
- Goal: Drive traffic to spacedatanetwork.io, collect email leads

**Phase 2 (Months 6-12): Conversion**
- Retargeting website visitors with SpaceAware.io free tier signup
- SpaceAware.io paid tier upgrade campaigns
- Webinar promotion ads
- Budget: $5K-10K/month

**Phase 3 (Year 2+): Scale**
- Conference sponsorships (AMOS, Space Symposium, SmallSat, IAC)
- Print ads in SpaceNews, Via Satellite
- Podcast sponsorships (Space Café, T-Minus, Main Engine Cut Off)
- Budget: $15K-25K/month

#### Print & Conference Materials

**Collateral (Claude-Generated, Designer-Polished):**
- 2-page product briefs for each product
- Technical white papers (SDN protocol, SDS schemas, OrbPro2 architecture)
- Case study templates
- Business cards with QR code to spacedatanetwork.io
- Conference booth banner and tablecloth designs
- Sticker/swag designs

**Key Conferences:**

| Conference | When | Why |
|---|---|---|
| **AMOS (Advanced Maui Optical & Space Surveillance)** | Sep | Premier SSA conference — decision makers |
| **Space Symposium** | Apr | Largest US space conference |
| **SmallSat** | Aug | Small satellite operators — our target market |
| **IAC (International Astronautical Congress)** | Oct | Global reach, standards adoption |
| **GEOINT** | Jun | Intelligence community — SpaceAware customers |
| **CES / Web Summit** | Jan/Nov | Tech press, general awareness |
| **FOSS4G** | Varies | Open-source geospatial community |
| **KubeCon** | Varies | P2P/infrastructure developer community |

#### Community Building

**Developer Relations:**
- Monthly virtual "SDN Office Hours" (Zoom/Discord)
- Hackathon sponsorship (sponsor space track at HackMIT, SpaceHacks, etc.)
- "SDN Ambassador" program for university researchers
- Open-source contributor incentive program (swag, recognition, bounties)

**Strategic Partnerships:**
- CesiumJS community cross-promotion
- IPFS/Protocol Labs ecosystem collaboration
- University partnerships (CU Boulder/AVS Lab for Basilisk, TU Delft for Tudat)
- Integrate with existing SSA tools (LeoLabs API, Space-Track, CelesTrak)

---

## 9. Roadmap & Milestones

### Phase 1: Foundation (Months 1-6)

**Objective:** Unified web presence, OrbPro2 launch, initial revenue

| Milestone | Target | Status |
|---|---|---|
| Unify website nav/footer across all sites | Month 1 | Not started |
| Launch spacedatanetwork.io hub site | Month 2 | Not started |
| SpaceAware.io Free tier live (catalog + history + conjunctions) | Month 2 | Not started |
| SpaceAware.io Explorer tier ($10) live | Month 3 | Not started |
| Submit NASA SBIR Phase I proposal | Month 3 | Not started |
| First 50 SpaceAware.io free accounts | Month 4 | Not started |
| Launch data marketplace beta (on SDN testnet) | Month 5 | Not started |
| First LinkedIn article series (6 articles) | Month 2-4 | Not started |
| 10 SDN full nodes running | Month 6 | Not started |

### Phase 2: Growth (Months 6-12)

**Objective:** SpaceAware.io launch, marketplace traction, grant funding secured

| Milestone | Target |
|---|---|
| SpaceAware.io Analyst ($20) + Operator ($30) tiers live | Month 7 |
| 500 SpaceAware.io free accounts, 50 paid | Month 8 |
| First data marketplace transaction | Month 8 |
| SBIR Phase I award (or resubmit) | Month 9 |
| SpaceAware.io Mission ($40) tier live | Month 9 |
| First conference talk (AMOS or SmallSat) | Month 8-9 |
| 50 SDN full nodes | Month 10 |
| First YouTube tutorial series (5 videos) | Month 8 |
| $50K cumulative revenue | Month 12 |

### Phase 3: Scale (Months 12-24)

**Objective:** Series Seed/A raise, enterprise customers, marketplace flywheel

| Milestone | Target |
|---|---|
| Raise $1.5-3M Seed round | Month 14 |
| 2,000 free accounts, 500 paid seats | Month 15 |
| OrbPro3 (WebGPU) engine upgrade for SpaceAware.io | Month 16 |
| NFT asset tokenization pilot (ground station time) | Month 18 |
| First $40/seat Mission customer (10+ seats) | Month 15 |
| 200 SDN full nodes globally | Month 18 |
| SBIR Phase II award | Month 18 |
| $120K ARR | Month 24 |

### Phase 4: Dominance (Months 24-48)

**Objective:** Market leadership, protocol standard adoption, international expansion

| Milestone | Target |
|---|---|
| SDN protocol submitted to CCSDS or ITU for standardization | Month 30 |
| Space Data Standards adopted by 3+ government agencies | Month 30 |
| 1,000+ SDN nodes globally | Month 30 |
| SpaceAware.io competitive with STK in government evaluations | Month 36 |
| NFT marketplace for satellite time operational | Month 36 |
| $1.5M ARR | Month 36 |
| Series A raise ($8-15M) | Month 36 |
| International office (ESA/JAXA partner region) | Month 48 |
| $4.3M ARR | Month 48 |

---

## 10. Risk Analysis & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| **CesiumJS changes license** | Low | High | OrbPro2 is a deep fork; can maintain independently. WebGPU OrbPro3 reduces dependency. |
| **Government builds competing open standard** | Medium | High | Stay ahead technically. Offer to contribute SDS to CCSDS. Be the reference implementation. |
| **ITAR/export control on simulation tools** | Medium | Medium | Tudat (Dutch) and Basilisk (university) have academic exceptions. Browser-WASM distribution is software, not hardware. Legal review needed. |
| **Crypto/NFT regulatory risk** | Medium | Medium | NFT features are optional layer. Support Stripe/fiat payments as primary. |
| **Slow enterprise sales cycle** | High | Medium | Focus on self-serve (SpaceAware.io) for revenue while building enterprise pipeline. Government grants bridge the gap. |
| **Open-source competitors** | Medium | Medium | 10+ years of R&D is hard to replicate. Community + ecosystem effects compound. Stay 2 years ahead. |
| **Key person risk** | High | High | Document everything. Build team. Use Claude Teams to capture institutional knowledge. |
| **Decentralization adoption resistance** | Medium | Medium | Position SDN as optional enhancement, not replacement. Support centralized deployment too. |

---

## 11. Appendix: Repository Index

| # | Repository | Path | Description | License |
|---|-----------|------|-------------|---------|
| 1 | space-data-network | `./` | P2P protocol, SDN server (Go), JS SDK, Desktop, WebUI | MIT |
| 2 | flatbuffers/wasm | `../flatbuffers/wasm` | FlatBuffers compiler in WASM, 13-lang codegen, encryption | Apache 2.0 |
| 3 | flatsql | `../flatsql` | SQL query engine over FlatBuffers via SQLite virtual tables | Apache 2.0 |
| 4 | hd-wallet-wasm | `../hd-wallet-wasm` | HD wallet, BIP-32/39/44, 50+ chains, FIPS 140-3 | Apache 2.0 |
| 5 | spacedatastandards.org | `../spacedatastandards.org` | 127 FlatBuffers schemas for space data, Svelte website | Apache 2.0 |
| 6 | tudat-wasm | `../tudat-wasm` | TU Delft astrodynamics toolbox compiled to WASM | BSD 3-Clause |
| 7 | basilisk | `../basilisk` | Basilisk spacecraft simulator compiled to WASM (310+ classes, 1757 tests) | ISC |
| 8 | OrbPro | `../OrbPro` | CesiumJS fork with orbital viz, sensor modeling, viewshed, access analysis | Proprietary |
| 9 | OrbPro2-MCP | `../OrbPro2-MCP` | In-browser LLM + MCP for natural language globe control | Proprietary |
| 10 | OrbPro2-ModSim | `../OrbPro2-ModSim` | 18 WASM plugins, 608 entity types, combat/mission simulation | Proprietary |
| 11 | WEBGPU_OrbPro3 | `../WEBGPU_OrbPro3` | Next-gen WebGPU CesiumJS rendering engine | Proprietary |
| 12 | spaceaware.io | `../spaceaware.io` | SaaS platform for space awareness (TO BE CREATED) | Proprietary |
| 13 | DigitalArsenal.io | `../DigitalArsenal.io` | Company website, Svelte + CesiumJS + Tailwind | Proprietary |

---

## 12. 24-Hour SpaceAware.io Launch Plan (Free Tier)

**Target window:** Start now (2026-02-11), public launch within 24 hours (by 2026-02-12).

### 12.1 Free Tier Definition (Launch Scope)

Ship only these features in the first 24 hours:

1. Public catalog/ephemeris API for `OMM`, `MPE`, and `CAT` lookups by object/day.
2. Historical + current catalog updates from local archives + CelesTrak + Space-Track catch-up.
3. Public status page + landing/docs on GitHub Pages.
4. One production SDN full node + one edge relay in DigitalOcean.
5. OrbPro license server running as a sidecar service (plugin integration deferred to phase 2).
6. Stripe subscription checkout + webhook confirmation for paid tiers.

### 12.2 Deployment Topology (DigitalOcean + Cloudflare + GitHub Pages)

**DNS / Routing**
- `spaceaware.io` and `www.spaceaware.io` -> GitHub Pages (marketing/docs).
- `api.spaceaware.io` -> Cloudflare proxied -> DigitalOcean SDN node admin/API port.
- `relay.spaceaware.io` -> Cloudflare proxied (WS enabled) -> DigitalOcean edge relay.

**DigitalOcean (minimum production shape)**
- Droplet A (`spaceaware-core`, 8 vCPU / 16 GB / 200+ GB volume):
  - `spacedatanetwork daemon` (full mode)
  - FlatSQL data at `/opt/data/sdn`
  - public query API exposed via `/api/v1/data/*` on admin listener
  - OrbPro license server container from `../OrbPro` on private Docker network
- Droplet B (`spaceaware-edge`, 2 vCPU / 4 GB):
  - `spacedatanetwork-edge` relay

### 12.3 Data Pipeline in First 24 Hours

**Local history import**
- Canonical storage root: `/opt/data/`
- Keep raw upstream files in `/opt/data/raw/{source}/{yyyy-mm-dd}/`
- Keep FlatSQL DB in `/opt/data/sdn/sdn.db`
- Run `spacedatanetwork reindex` after initial import to populate fast query indexes.

**Gap-fill strategy (Space-Track, no usage spikes)**
- Pull in day-sized windows with checkpointing:
  - checkpoint table/file stores last successful date per source/schema.
  - process `N` days, sleep, continue (avoid burst limits and retry storms).
- Do not re-request completed windows unless checksum/version changed.

**Current updates (CelesTrak)**
- Hourly: `https://celestrak.org/pub/GP/catalog.csv` -> normalize to SDS `OMM` + derived `MPE`.
- Daily: `https://celestrak.org/pub/satcat.csv` -> normalize to SDS `CAT`.

### 12.4 API Contract for Cloudflare Caching

**Launch endpoints**
- `GET /api/v1/data/health`
- `GET /api/v1/data/omm?norad_cat_id=<id>&day=YYYY-MM-DD&limit=100`
- `GET /api/v1/data/mpe?entity_id=<id>&day=YYYY-MM-DD&limit=100`
- `GET /api/v1/data/cat?norad_cat_id=<id>&limit=5`

**Caching behavior**
- Response headers include `ETag`, `Last-Modified`, `Cache-Control`.
- Past-day queries cache long (`s-maxage=86400`), current-day queries short (`s-maxage=120`).
- Cloudflare cache key should include full query string.

### 12.5 24-Hour Execution Timeline

**Hour 0-2**
1. Provision two DigitalOcean droplets and attach `/opt/data` volume to core node.
2. Configure Cloudflare DNS + proxy rules (`api`, `relay`, root domain).
3. Deploy SDN full node and edge relay containers/services.

**Hour 2-6**
1. Load historical `/opt/data` records into FlatSQL.
2. Run `spacedatanetwork reindex` on core node.
3. Verify query API locally for known NORAD IDs and dates.

**Hour 6-12**
1. Start Space-Track gap-fill with checkpointed day windows.
2. Start hourly CelesTrak GP sync and daily SATCAT sync jobs.
3. Bring up OrbPro license sidecar and verify internal connectivity.

**Hour 12-18**
1. Publish GitHub Pages landing + API docs + free-tier limits.
2. Enable Cloudflare caching/WAF/rate limits for `api.spaceaware.io`.
3. Configure Stripe webhook to `POST /api/storefront/payments/stripe/webhook`.
4. Smoke-test from external network (API + relay + website + billing flow).

**Hour 18-24**
1. Announce free tier (LinkedIn/X + docs changelog).
2. Monitor latency/error/cache-hit metrics and tune limits.
3. Freeze launch scope; defer non-critical plugin integration work.

### 12.6 Launch-Day Command Checklist

```bash
# On core node
spacedatanetwork init
spacedatanetwork reindex
export STRIPE_SECRET_KEY="sk_live_..."
export STRIPE_WEBHOOK_SECRET="whsec_..."
export STRIPE_SUCCESS_URL="https://spaceaware.io/billing/success?session_id={CHECKOUT_SESSION_ID}"
export STRIPE_CANCEL_URL="https://spaceaware.io/billing/cancel"
spacedatanetwork daemon
```

```bash
# Key API checks
curl "https://api.spaceaware.io/api/v1/data/health"
curl "https://api.spaceaware.io/api/v1/data/omm?norad_cat_id=25544&day=2026-02-11&limit=5"
curl "https://api.spaceaware.io/api/v1/data/cat?norad_cat_id=25544&limit=1"
```

### 12.7 Single-File App Delivery Over IPFS/IPNS

**Goal:** Ship SpaceAware UI as one static artifact, domain-independent, while keeping paid capability enforcement server-side.

**Build/output shape**
- Produce a single `index.html` with inlined JS/CSS (no runtime chunk loading).
- Bundle `sdn-js`, `hd-wallet-wasm` bootstrap glue, and OrbPro viewer loader stubs into the single file.
- Keep large optional assets (WASM/sprites/terrain packs) as separate immutable CIDs loaded on demand.

**Publishing model**
- Publish `index.html` to IPFS -> immutable CID.
- Publish `spaceaware-app` IPNS key to latest CID.
- Client bootstrap options:
  - Native IPFS node: `ipns://<spaceaware-app-key>`
  - Gateway fallback: `https://<gateway>/ipns/<spaceaware-app-key>`

**Critical rule**
- No reusable secrets in published IPFS content.
- IPFS content can contain public keys and endpoint hints, never private/shared long-lived license keys.

### 12.8 License/Entitlement Architecture (xpub + PeerID + Stripe)

**Identity**
- Canonical user identifier: `xpub` from `hd-wallet-wasm`.
- Network identifier: `peer_id` derived from the signing public key used by libp2p.
- Store verified binding: `xpub <-> signing_pubkey <-> peer_id`.

**License service transport**
- Run OrbPro license server as a libp2p protocol service on a stable peer:
  - `/orbpro/license/1.0.0`
- Discover via:
  - pinned bootstrap peer multiaddr list
  - optional IPNS record containing current reachable multiaddrs

**Why this is lockable without domain dependence**
- App delivery is content-addressed and public.
- Authorization is live, cryptographic, and short-lived (capability grants).
- Paid APIs/features validate server-signed grants, not hostname/session cookies.

**Encrypted plugin delivery (current implementation)**
- `GET /api/v1/plugins/manifest` returns plugin metadata (id/version/scope/sha256/size).
- `GET /api/v1/plugins/{id}/bundle` serves encrypted plugin bytes with `ETag` and cache headers.
- `POST /api/v1/plugins/{id}/key-envelope` requires bearer token + scope and returns an X25519-wrapped decryption envelope.
- Browser/client sends ephemeral X25519 public key in the request; server returns wrapped key material bound to:
  - `peer_id`
  - capability token `jti`
  - plugin id/version/hash
  - short expiry
- Cloudflare can cache encrypted bundles; key-envelope responses are `private, no-store`.

### 12.9 Protocol Messages (Launch Format: JSON over libp2p stream)

For the 24-hour launch, keep protocol simple: newline-delimited JSON messages on `/orbpro/license/1.0.0`.

**1) Challenge request**

```json
{
  "type": "challenge_request",
  "req_id": "uuid-v4",
  "xpub": "xpub6CUGRU...",
  "peer_id": "12D3KooW...",
  "client_pubkey_hex": "aabbcc...",
  "ts": 1760000000
}
```

**2) Challenge response**

```json
{
  "type": "challenge_response",
  "req_id": "uuid-v4",
  "challenge": "base64-32-bytes",
  "expires_at": 1760000060,
  "server_peer_id": "12D3KooWLicense..."
}
```

**3) Proof request**

```json
{
  "type": "proof_request",
  "req_id": "uuid-v4",
  "xpub": "xpub6CUGRU...",
  "peer_id": "12D3KooW...",
  "challenge": "base64-32-bytes",
  "signature_hex": "ed25519sig...",
  "ts": 1760000020
}
```

**4) Grant response**

```json
{
  "type": "grant_response",
  "req_id": "uuid-v4",
  "entitlement": {
    "plan": "free",
    "status": "active",
    "stripe_customer_id": "cus_...",
    "stripe_subscription_id": "sub_..."
  },
  "capability_token": "base64url-jws",
  "expires_at": 1760000920
}
```

**Capability token claims (server-signed, Ed25519)**

```json
{
  "iss": "spaceaware-license",
  "sub": "xpub6CUGRU...",
  "peer_id": "12D3KooW...",
  "plan": "free",
  "scopes": ["api:data:read:free", "orbpro:base"],
  "iat": 1760000020,
  "exp": 1760000920,
  "jti": "uuid-v4"
}
```

**Enforcement**
- Free endpoints: no token required, aggressively cacheable via Cloudflare.
- Paid endpoints/features: require `Authorization: Bearer <capability_token>`.
- Reject if `exp` elapsed, `peer_id` mismatch, revoked `jti`, or subscription inactive.

### 12.10 Docker Deployment (Single Host, Launch Day)

Use one DigitalOcean host for first 24 hours (split edge relay to host #2 in phase 2).

```yaml
version: "3.9"
services:
  sdn-node:
    image: ghcr.io/spacedatanetwork/sdn-server:latest
    command: ["spacedatanetwork", "daemon", "--config", "/etc/sdn/config.yaml"]
    volumes:
      - /opt/data/sdn:/opt/data/sdn
      - /opt/data/raw:/opt/data/raw
      - /opt/data/keys:/opt/data/keys
      - ./config/sdn:/etc/sdn
    environment:
      - STRIPE_SECRET_KEY=${STRIPE_SECRET_KEY}
      - STRIPE_WEBHOOK_SECRET=${STRIPE_WEBHOOK_SECRET}
      - STRIPE_SUCCESS_URL=${STRIPE_SUCCESS_URL}
      - STRIPE_CANCEL_URL=${STRIPE_CANCEL_URL}
      - HD_WALLET_WASM_PATH=/opt/wasm/hd-wallet.wasm
    ports:
      - "8080:8080"   # ws relay/listen
      - "5001:5001"   # admin/api
    networks: [sdn]
    restart: unless-stopped

  sdn-ingest:
    image: ghcr.io/spacedatanetwork/sdn-server:latest
    command: ["spacedatanetwork", "ingest", "--config", "/etc/sdn/config.yaml", "--loop"]
    volumes:
      - /opt/data/sdn:/opt/data/sdn
      - /opt/data/raw:/opt/data/raw
      - ./config/sdn:/etc/sdn
    depends_on: [sdn-node]
    networks: [sdn]
    restart: unless-stopped

  orbpro-license:
    image: ghcr.io/digitalarsenal/orbpro-license:latest
    environment:
      - LICENSE_PROTOCOL=/orbpro/license/1.0.0
      - SDN_BOOTSTRAP=/ip4/<PUBLIC_IP>/tcp/8080/ws/p2p/<SDN_PEER_ID>
      - STRIPE_SECRET_KEY=${STRIPE_SECRET_KEY}
      - STRIPE_WEBHOOK_SECRET=${STRIPE_WEBHOOK_SECRET}
      - ENTITLEMENTS_DB=/opt/data/license/license.db
    volumes:
      - /opt/data/license:/opt/data/license
    depends_on: [sdn-node]
    networks: [sdn]
    restart: unless-stopped

networks:
  sdn:
    driver: bridge
```

**Cloudflare cache policy**
- Cache only `GET` on free/public API routes.
- Bypass cache for any request with `Authorization` header.
- Add WAF + rate limits on `/api/storefront/payments/stripe/webhook` and license endpoints.

### 12.11 Non-Goals for This 24-Hour Window

- Full OrbPro pluginization inside SDN protocol path.
- Advanced entitlement automation beyond Stripe payment confirmation + grant issuance.
- Multi-region replicated databases.
- Advanced analytics dashboards beyond core operational health.

---

## Action Items — Immediate Next Steps

### This Week
- [ ] Review and refine this master plan
- [ ] Decide on fundraising strategy (bootstrap vs. grant-first vs. VC-first)
- [ ] Define SpaceAware.io Free tier MVP feature set

### This Month
- [ ] Build unified nav component for all websites
- [ ] Create spacedatanetwork.io landing page
- [ ] Set up SpaceAware.io with Stripe billing ($10/$20/$30/$40 per-seat)
- [ ] Draft first 3 LinkedIn articles
- [ ] Identify specific SBIR topics for next submission window

### This Quarter
- [ ] Launch SpaceAware.io Free + Explorer tiers
- [ ] First 50 free accounts
- [ ] Submit first SBIR proposal
- [ ] Attend or present at first conference
- [ ] Create explainer video
- [ ] First paying SpaceAware.io customers

---

## Contact

**ALL e-mail correspondence goes to tj@digitalarsenal.io.**

---

*This is a living document. Update as strategy evolves.*
