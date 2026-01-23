# What is Space Data Network?

Space Data Network (SDN) is a **decentralized peer-to-peer network** for exchanging standardized space data, built on [IPFS](https://ipfs.tech) and [libp2p](https://libp2p.io). It enables organizations, satellites, and ground stations to share space situational awareness data in real-time without relying on a central server.

## Mission

**Enable decentralized, global collaboration on space situational awareness and space traffic management.**

Space is getting crowded. With over 10,000 active satellites, millions of debris fragments, and dozens of new launches every month, the need for coordination has never been greater. Yet today's space data infrastructure is fragmented—proprietary systems, incompatible formats, and centralized choke points that create single points of failure.

SDN changes this by providing **open infrastructure** that anyone can use:

- **Space agencies** can share tracking data and conjunction warnings globally
- **Satellite operators** can coordinate maneuvers without intermediaries
- **Researchers** can access real-time data for analysis
- **New entrants** can participate without expensive integrations

This isn't about replacing existing systems—it's about providing a common layer that connects them all.

## The Problem

Space data exchange today faces several challenges:

- **Fragmented Standards** - Different organizations use different formats
- **Centralized Dependencies** - Data flows through single points of failure
- **Delayed Propagation** - Critical data takes time to reach all parties
- **Verification Challenges** - Difficult to verify data authenticity
- **Access Barriers** - High costs and bureaucracy limit participation

## The Solution

SDN addresses these challenges through:

### Standardized Data Formats

All data in SDN uses [Space Data Standards](https://spacedatastandards.org) - a comprehensive set of 32 schemas covering all aspects of space operations:

| Category | Standards |
|----------|-----------|
| Orbital Elements | OMM, OEM, OCM, OSM |
| Conjunction Assessment | CDM, CSM |
| Tracking Data | TDM, RFM |
| Entity Information | EPM, PNM |
| And more... | 32 total schemas |

### Decentralized Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│     ┌─────┐         ┌─────┐         ┌─────┐               │
│     │Node │◄───────►│Node │◄───────►│Node │               │
│     └──┬──┘         └──┬──┘         └──┬──┘               │
│        │               │               │                   │
│        │      DHT + GossipSub          │                   │
│        │               │               │                   │
│     ┌──┴──┐         ┌──┴──┐         ┌──┴──┐               │
│     │Edge │         │Edge │         │Edge │               │
│     └──┬──┘         └──┬──┘         └──┬──┘               │
│        │               │               │                   │
│     ┌──┴──┐         ┌──┴──┐         ┌──┴──┐               │
│     │ App │         │ App │         │ App │               │
│     └─────┘         └─────┘         └─────┘               │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

- **Full Nodes** - Store data, participate in DHT, relay messages
- **Edge Relays** - Enable browser connectivity via Circuit Relay
- **Applications** - Connect from browsers or any environment

### Real-time Data Exchange

SDN uses GossipSub for real-time publish/subscribe:

```typescript
// Subscribe to conjunction data messages
node.subscribe('CDM', (data, peerId) => {
  // Receive CDM updates in real-time
  evaluateThreat(data);
});
```

Each data type has its own topic, allowing selective subscription.

### Cryptographic Verification

All data in SDN is signed with Ed25519:

```
┌────────────────────────────────────────┐
│            SDN Message                 │
├────────────────────────────────────────┤
│  Schema: OMM.fbs                       │
│  Data: [FlatBuffer binary]             │
│  Signature: [64-byte Ed25519]          │
│  PeerID: 12D3KooW...                   │
└────────────────────────────────────────┘
```

Recipients can verify:
- Data hasn't been tampered with
- Data came from the claimed source
- Source has a valid network identity

## Space Traffic Management

SDN is purpose-built for **Space Traffic Management (STM)** and **Space Situational Awareness (SSA)**—the critical infrastructure needed as space becomes increasingly congested.

### The Challenge

- **10,000+** active satellites in orbit
- **Millions** of tracked debris fragments
- **100+** launches per year
- **Multiple** organizations need to coordinate

### How SDN Helps

| Challenge | SDN Solution |
|-----------|--------------|
| **Collision avoidance** | Real-time CDM sharing via PubSub |
| **Maneuver coordination** | Publish maneuver plans (MPE) network-wide |
| **Launch deconfliction** | Share launch windows (LCC, LDM) |
| **Tracking data** | Distribute measurements (TDM, RFM) |
| **Reentry warnings** | Broadcast predictions (ROC) |

### Example: Conjunction Warning Flow

```
1. Operator A detects close approach
2. Publishes CDM to SDN network
3. All subscribers receive within seconds
4. Operator B (the other party) is automatically notified
5. Both operators coordinate response
```

No central coordinator needed. No delays. No access restrictions.

## Use Cases

### Space Agencies

Share tracking data and conjunction warnings across international partners in real-time. Enable transparent STM operations.

### Satellite Operators

- Publish orbital elements for coordination
- Receive conjunction warnings automatically
- Exchange maneuver plans with neighbors
- Coordinate station-keeping activities

### Ground Station Networks

- Distribute tracking measurements
- Coordinate observation schedules
- Share calibration data
- Pool resources for coverage

### Space Weather Services

- Publish environmental data
- Distribute alerts and forecasts
- Enable downstream applications

### Researchers & Academia

- Access real-time orbital data
- Analyze space traffic patterns
- Develop STM algorithms
- Contribute to open science

## Built on IPFS

SDN is built on the [InterPlanetary File System (IPFS)](https://ipfs.tech) stack—the same decentralized technology that powers web3, distributed storage, and censorship-resistant applications worldwide.

### What is IPFS?

[IPFS](https://ipfs.tech) is a peer-to-peer network protocol for storing and sharing data in a distributed file system. Unlike traditional client-server architectures, IPFS creates a resilient network where:

- **No single point of failure** - Data is distributed across many peers
- **Content addressing** - Data is identified by what it is, not where it is
- **Verifiable** - Cryptographic hashes ensure data integrity
- **Efficient** - Data is deduplicated and served from the nearest peer

### Why IPFS for Space Data?

| Benefit | Description |
|---------|-------------|
| **Resilience** | Network continues operating even if nodes go offline |
| **Global reach** | Any peer worldwide can participate |
| **Proven scale** | Powers millions of nodes in production |
| **Open source** | Transparent, auditable, community-driven |

### IPFS Technologies in SDN

| Component | IPFS Technology | Purpose |
|-----------|-----------------|---------|
| **Peer Discovery** | [Kademlia DHT](https://docs.libp2p.io/concepts/fundamentals/protocols/#kad-dht) | Find peers and content across the network |
| **Real-time Messaging** | [GossipSub](https://docs.libp2p.io/concepts/pubsub/overview/) | Publish/subscribe for data streams |
| **NAT Traversal** | [Circuit Relay](https://docs.libp2p.io/concepts/nat/circuit-relay/) | Enable browser and firewalled clients |
| **Transports** | [libp2p](https://libp2p.io) | TCP, QUIC, WebSocket, WebRTC |
| **Identity** | [Peer IDs](https://docs.libp2p.io/concepts/fundamentals/peers/) | Cryptographic identity for every node |

### SDN Extensions to IPFS

While SDN uses IPFS networking, it adds **space-specific optimizations**:

- **FlatBuffers** instead of IPLD for zero-copy, high-performance serialization
- **Schema validation** - Only valid Space Data Standards messages accepted
- **Topic-per-schema** PubSub for efficient subscription filtering
- **SQLite storage** with queryable FlatBuffer data

### Learn More About IPFS

- [IPFS Documentation](https://docs.ipfs.tech/)
- [libp2p Documentation](https://docs.libp2p.io/)
- [IPFS GitHub](https://github.com/ipfs)
- [Kubo (Go implementation)](https://github.com/ipfs/kubo)

## Technology Stack

| Layer | Technology |
|-------|------------|
| Network | [libp2p](https://libp2p.io) (DHT, PubSub, Circuit Relay) |
| Serialization | [FlatBuffers](https://google.github.io/flatbuffers/) (zero-copy binary) |
| Storage | SQLite with FlatBuffer virtual tables |
| Cryptography | Ed25519 (signing), AES-GCM (encryption) |
| WASM | [wazero](https://wazero.io/) (Go), Web APIs (browser) |

## Next Steps

- [Getting Started](/guide/getting-started) - Install and run your first node
- [Architecture](/guide/architecture) - Deep dive into how SDN works
- [Schema Reference](/reference/schemas) - Explore all 32 data standards
