# Architecture

Space Data Network is built on a layered architecture that provides flexibility, security, and performance.

## System Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Application Layer                               │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐        │
│  │   Web Apps      │  │   CLI Tools     │  │  Integrations   │        │
│  │   (Browser)     │  │   (Terminal)    │  │  (APIs/Plugins) │        │
│  └────────┬────────┘  └────────┬────────┘  └────────┬────────┘        │
└───────────┼────────────────────┼────────────────────┼──────────────────┘
            │                    │                    │
┌───────────┼────────────────────┼────────────────────┼──────────────────┐
│           │            SDK Layer                    │                   │
│  ┌────────┴────────┐  ┌────────┴────────┐  ┌───────┴─────────┐        │
│  │    sdn-js       │  │   Go Client     │  │  Data Ingestion │        │
│  │  (TypeScript)   │  │   Libraries     │  │  WASM Plugins   │        │
│  └────────┬────────┘  └────────┬────────┘  └───────┬─────────┘        │
└───────────┼────────────────────┼────────────────────┼──────────────────┘
            │                    │                    │
┌───────────┼────────────────────┼────────────────────┼──────────────────┐
│           │           Network Layer                 │                   │
│  ┌────────┴──────────────────────────────────────────────────┐        │
│  │                      libp2p                                │        │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐     │        │
│  │  │   DHT    │ │  PubSub  │ │  Relay   │ │Transport │     │        │
│  │  │(Kademlia)│ │(GossipSub│ │(Circuit) │ │(TCP/QUIC)│     │        │
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘     │        │
│  └────────────────────────────────────────────────────────────┘        │
└─────────────────────────────────────────────────────────────────────────┘
            │                    │                    │
┌───────────┼────────────────────┼────────────────────┼──────────────────┐
│           │           Storage Layer                 │                   │
│  ┌────────┴────────┐  ┌────────┴────────┐  ┌───────┴─────────┐        │
│  │   FlatSQL       │  │   IndexedDB     │  │   FlatBuffer    │        │
│  │   (SQLite)      │  │   (Browser)     │  │   Validation    │        │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘        │
└─────────────────────────────────────────────────────────────────────────┘
```

## Node Types

### Full Node

Full nodes are the backbone of the network:

- **Store Data** - Persist all received data to local storage
- **Participate in DHT** - Help other nodes find peers and content
- **Relay Messages** - Forward PubSub messages to subscribers
- **Serve Queries** - Respond to data requests from other nodes

```go
// Full node configuration
type FullNode struct {
    Host      host.Host        // libp2p host
    DHT       *dht.IpfsDHT     // Kademlia DHT
    PubSub    *pubsub.PubSub   // GossipSub
    Store     *FlatSQLStore    // SQLite storage
    Validator *sds.Validator   // Schema validation
}
```

### Edge Relay

Edge relays enable browser connectivity:

- **Circuit Relay** - Bridge connections for NAT-traversal
- **WebSocket/WebTransport** - Browser-compatible transports
- **Lightweight** - Minimal resource requirements

```bash
# Run as edge relay only
./spacedatanetwork-edge --relay-only
```

### Browser Node

Browser nodes connect via edge relays:

- **WebRTC** - Direct peer connections when possible
- **Circuit Relay** - Fallback through edge relays
- **IndexedDB** - Local browser storage

## Data Flow

### Publishing Data

```
┌──────────┐     ┌───────────┐     ┌──────────┐     ┌──────────┐
│Publisher │────►│ Validate  │────►│   Sign   │────►│ Broadcast│
│          │     │ (Schema)  │     │ (Ed25519)│     │ (PubSub) │
└──────────┘     └───────────┘     └──────────┘     └──────────┘
                                                          │
                      ┌───────────────────────────────────┤
                      ▼                                   ▼
               ┌──────────┐                        ┌──────────┐
               │Subscriber│                        │Subscriber│
               │   Node   │                        │   Node   │
               └──────────┘                        └──────────┘
```

1. **Validate** - Data is validated against the FlatBuffer schema
2. **Sign** - Publisher signs the data with Ed25519 private key
3. **Broadcast** - Message is broadcast via GossipSub topic
4. **Receive** - Subscribers receive and verify the message
5. **Store** - Valid messages are stored locally

### Requesting Data

```
┌──────────┐     ┌───────────┐     ┌──────────┐     ┌──────────┐
│Requester │────►│   DHT     │────►│  Peer    │────►│ Response │
│          │     │  Lookup   │     │ Connect  │     │          │
└──────────┘     └───────────┘     └──────────┘     └──────────┘
```

1. **DHT Lookup** - Find peers that have the requested data
2. **Peer Connect** - Establish direct connection
3. **Request** - Send SDS exchange protocol request
4. **Response** - Receive and verify data

## Protocol Stack

### Transport Layer

SDN supports multiple transport protocols:

| Transport | Use Case | Browser Support |
|-----------|----------|-----------------|
| TCP | Primary server transport | No |
| QUIC | Low-latency, multiplexed | No |
| WebSocket | Browser connectivity | Yes |
| WebTransport | Modern browser transport | Yes |
| WebRTC | Direct browser-to-browser | Yes |

### Discovery

Peer discovery uses multiple mechanisms:

1. **DHT Bootstrap** - Connect to known bootstrap nodes
2. **DHT Routing** - Find peers via Kademlia routing table
3. **mDNS** - Local network discovery
4. **PubSub Peers** - Discover peers via topic subscription

### SDS Exchange Protocol

Custom protocol for space data exchange:

```
Protocol ID: /spacedatanetwork/sds-exchange/1.0.0

Message Types:
  0x01 - Request Data
  0x02 - Push Data
  0x03 - Query
  0x04 - Response
  0x05 - ACK
  0x06 - NACK

Message Format:
  [1 byte]  Message Type
  [2 bytes] Schema Name Length
  [N bytes] Schema Name
  [4 bytes] Data Length
  [N bytes] Data (FlatBuffer)
  [64 bytes] Signature (Ed25519)
```

## Storage Architecture

### Server Storage (FlatSQL)

SQLite-based storage with FlatBuffer support:

```sql
-- One table per schema type
CREATE TABLE omm (
    cid TEXT PRIMARY KEY,
    peer_id TEXT,
    timestamp INTEGER,
    data BLOB,
    signature BLOB
);

-- Indexes for common queries
CREATE INDEX idx_omm_peer ON omm(peer_id);
CREATE INDEX idx_omm_timestamp ON omm(timestamp);
```

### Browser Storage

IndexedDB with schema-based object stores:

```typescript
const db = await openDB('sdn-store', 1, {
  upgrade(db) {
    // Create store for each schema
    SUPPORTED_SCHEMAS.forEach(schema => {
      db.createObjectStore(schema, { keyPath: 'cid' });
    });
  }
});
```

## Security Model

### Identity

Each node has an Ed25519 keypair:

- **Peer ID** - Derived from public key (multihash)
- **Private Key** - Used for signing messages
- **Public Key** - Embedded in Peer ID, used for verification

### Data Integrity

All published data includes:

1. **Schema Validation** - Data must conform to FlatBuffer schema
2. **Signature** - Ed25519 signature over data bytes
3. **Peer ID** - Publisher identity for accountability

### Encryption (Optional)

For sensitive data:

- **AES-GCM** - Symmetric encryption with WASM or Web Crypto
- **Key Exchange** - Out-of-band or via secure channels

## Scalability

### Horizontal Scaling

- Add more full nodes to increase storage capacity
- Add more edge relays for browser connectivity
- DHT automatically balances peer discovery

### Topic Sharding

For high-volume schemas, topics can be sharded:

```
/sdn/OMM           - All OMM data
/sdn/OMM/region/us - US region only
/sdn/OMM/sat/25544 - Specific satellite (ISS)
```

## Next Steps

- [Full Node Setup](/guide/full-node) - Deploy your own node
- [Protocol Reference](/reference/protocol-sds) - Protocol details
- [Schema Reference](/reference/schemas) - Data format specifications
