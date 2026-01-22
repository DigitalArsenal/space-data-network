# Space Data Network - Repository Analysis

## Overview

The **Space Data Network** is a peer-to-peer (P2P) distributed network system for collaborative space data exchange. It enables nodes to securely share, discover, and exchange standardized space-related data (orbital data, satellite information, etc.) using cryptographic verification and distributed consensus mechanisms.

### Core Technologies

| Technology | Purpose |
|------------|---------|
| **LibP2P** | P2P networking foundation |
| **Google Flatbuffers** | Efficient binary serialization via Space Data Standards schemas |
| **IPFS/Kubo** | Distributed file storage and content addressing |
| **Secp256k1 (Ethereum)** | Digital signatures and BIP32/BIP44 key derivation |
| **ChaCha20Poly1305** | Private key encryption at rest |

---

## Project Structure

```
go-space-data-network/
├── cmd/                           # Application entry points
│   ├── node/main.go              # Main daemon entry point
│   ├── http/                     # HTTP/HTTPS server handlers
│   └── socket/                   # Unix socket IPC server
├── internal/                      # Core implementation
│   ├── node/                     # LibP2P node & core logic
│   │   ├── node.go              # Node struct & lifecycle
│   │   ├── ipfs_node.go         # IPFS operations
│   │   ├── peerdiscovery.go     # DHT + mDNS discovery
│   │   ├── watch_folder.go      # File monitoring
│   │   ├── create_server_epm.go # EPM creation
│   │   ├── generate_wallet_config.go # Wallet & key management
│   │   ├── keys.go              # Key extraction utilities
│   │   ├── protocols/           # P2P protocol handlers
│   │   ├── pubsub/              # GossipSub messaging
│   │   ├── crypto_utils/        # Cryptography utilities
│   │   └── sds_utils/           # Space Data Standards utilities
│   ├── spacedatastandards/      # 30+ Flatbuffer schema implementations
│   └── web/                     # Web server utilities
├── serverconfig/                 # Global configuration management
├── retrievers/                   # External data fetchers (Celestrak)
├── test/                         # Test utilities
├── build.sh                      # Platform-specific build script
└── build_dist.sh                 # Distribution builds
```

---

## Key Components

### 1. Node Structure (`internal/node/node.go`)

The central `Node` struct manages all P2P functionality:

```go
type Node struct {
    Host              host.Host           // LibP2P host
    DHT               *dht.IpfsDHT        // Kademlia DHT
    Wallet            *hdwallet.Wallet    // Ethereum HD wallet
    signingAccount    accounts.Account    // For digital signatures
    encryptionAccount accounts.Account    // For encryption
    IPFS              *core.IpfsNode      // IPFS node instance
    peerChan          chan peer.AddrInfo  // Peer discovery channel
    FileWatcher       Watcher             // Folder monitoring
    SDSTopics         map[string]*pubsub.Topic
    SDSSubscriptions  map[string]*pubsub.Subscription
    EPM               *EPM.EPM            // Node's Entity Profile
}
```

**Network Listeners:**
- `/ip4/0.0.0.0/tcp/8080/ws` - WebSocket
- `/ip4/0.0.0.0/tcp/0` - TCP
- `/ip6/::/tcp/0` - IPv6
- QUIC and WebTransport enabled

### 2. CLI Commands (`cmd/node/main.go`)

| Flag | Description |
|------|-------------|
| `--list-peers` | List connected peers with their EPM info |
| `--get-epm <id>` | Fetch Entity Profile Manifest by PeerID or email |
| `--create-server-epm` | Create server's EPM |
| `--output-server-epm` | Output EPM to file or QR code |
| `--export-private-key-mnemonic` | Export private key as BIP39 mnemonic |
| `--export-private-key-hex` | Export private key as hex |
| `--import-private-key-mnemonic` | Import from BIP39 mnemonic |
| `--import-private-key-hex` | Import from hex |

### 3. Socket IPC Server (`cmd/socket/main.go`)

Unix socket server for inter-process communication with binary protocol:

```
[8 bytes: message length]
[32 bytes: command (padded)]
[N bytes: data]
[1 byte: EOT 0x04]
```

**Commands:**
- `ADD_PEER` - Add peer to network
- `REMOVE_PEER` - Remove peer
- `LIST_PEERS` - Display network peers
- `PUBLIC_KEY` - Get node's public key
- `CREATE_SERVER_EPM` - Create EPM structure
- `GET_EPM` - Retrieve EPM by PeerID/email

### 4. HTTP/HTTPS Server (`cmd/http/`)

| File | Purpose |
|------|---------|
| `index.handler.go` | Static file serving from configured root folder |
| `verify_domain.handler.go` | Domain verification for Let's Encrypt |
| `certificate_manager.go` | Let's Encrypt autocert management |
| `rate_limiter.go` | Rate limiting (1000 req/hour per IP) |
| `cors.middleware.go` | CORS headers (wildcard) |
| `self_signed_cert.go` | Self-signed certificate generation |

**Endpoints:**
- `/` - Static file serving
- `/verify-domain?domain=X` - Domain verification for HTTPS upgrade

---

## Data Standards

### Entity Profile Manifest (EPM)

Describes a node/organization in the network:

| Field | Description |
|-------|-------------|
| `LEGAL_NAME` | Organization name |
| `EMAIL` | Contact email |
| `GIVEN_NAME`, `FAMILY_NAME` | Contact person info |
| `KEYS` | Array of CryptoKey (signing, encryption) |
| `MULTIFORMAT_ADDRESS` | IPNS addresses (base32 & base36) |
| `ADDRESS` | Full postal address |
| `ALTERNATE_NAMES` | Alternative identifiers |

### Peer Network Manifest (PNM)

Published when sharing files:

| Field | Description |
|-------|-------------|
| `MULTIFORMAT_ADDRESS` | Publisher's IPNS address |
| `CID` | Content ID of published data |
| `PUBLISH_TIMESTAMP` | RFC3339 timestamp |
| `SIGNATURE` | Ethereum digital signature |
| `SIGNATURE_TYPE` | "ETH" |
| `FILE_ID` | Identifier for the file |

### Supported Space Data Standards (30+)

Located in `internal/spacedatastandards/`:

- **EPM** - Entity Profile Manifest
- **PNM** - Peer Network Manifest
- **OMM** - Orbital Mean Message
- **OEM** - Orbital Ephemeris Message
- **CDM** - Conjunction Data Message
- **CAT** - Catalog
- **CSM** - Conjunction Summary Message
- **LDM** - Launch Data Message
- **IDM** - Initial Data Message
- **PLD** - Payload
- **BOV** - Body Orientation and Velocity
- **EOO** - Earth Orientation
- **RFM** - Reference Frame Message
- And 17+ more domain-specific standards

---

## Workflows

### Node Startup

```
main()
├── Load .env file
├── Parse CLI flags
├── Initialize config
├── GenerateWalletAndIPFSRepo()
│   ├── Load/create BIP39 mnemonic
│   ├── Derive signing & encryption accounts
│   └── Create IPFS repo
├── Create LibP2P host (TCP, WS, QUIC, WebTransport)
├── Enable NAT/relay/hole punching
├── Create IPFS node
├── Start HTTP/HTTPS servers
├── RegisterPlugins() [PNM exchange protocol]
├── Setup file watcher
├── Start peer discovery
├── Setup PubSub topics
├── Publish IPNS record
└── Start socket IPC server
```

### File Publishing

```
File added to outgoing folder
├── File watcher detects change
├── 1-second debounce
├── AddFileFromBytes() → IPFS CID
├── CreatePNM() with CID + digital signature
├── SerializePNM() → Flatbuffer
├── Publish to PubSub topic
└── AddFolderToIPNS() → Updates IPNS record
```

### Peer Discovery

```
Node startup
├── Initialize DHT
├── Start mDNS service
├── Advertise version hash to DHT
├── Auto-relay feeder (exponential backoff)
│   ├── Get closest DHT peers
│   └── Feed to relay system
└── Respond to peer requests
```

---

## Protocols

### PNM Exchange Protocol

- **Protocol ID:** `/space-data-network/id-exchange/1.0.0`
- Stream-based PNM exchange between peers
- Signature verification using Secp256k1 public keys
- EPM fetched from IPFS after PNM verification

### PubSub System

- **Router:** GossipSub
- **Topic naming:** `{discoveryHash}-{standard}`
- **Discovery hash:** Argon2id(version, version) with parameters (t=1, m=64KB, p=4)
- One topic per Space Data Standard

---

## Configuration

### Environment Variables

| Variable | Description |
|----------|-------------|
| `SPACE_DATA_NETWORK_DATASTORE_PASSWORD` | Encryption password for datastore |
| `SPACE_DATA_NETWORK_DATASTORE_DIRECTORY` | Data directory path |
| `SPACE_DATA_NETWORK_WEBSERVER_PORT` | HTTPS server port |
| `SPACE_DATA_NETWORK_ETHEREUM_DERIVATION_PATH` | BIP32/44 derivation path |

### Default Paths

- **Data directory:** `~/.spacedatanetwork/`
- **IPFS repository:** `~/.spacedatanetwork/ipfs/`
- **Self-signed certs:** `~/.spacedatanetwork/certificates/selfsigned/`
- **Autocert certs:** `~/.spacedatanetwork/certificates/autocert/`

### IPFS Configuration

- Mount-based spec with LevelDB & FlatFS
- 10GB storage maximum
- GC period: 1 hour

---

## Security

### Cryptography

| Purpose | Algorithm |
|---------|-----------|
| Key derivation | BIP39 mnemonic → BIP32/44 HD wallet |
| Digital signatures | Secp256k1 (Ethereum standard) |
| Private key encryption | ChaCha20Poly1305 (XChaCha20 variant) |
| Content hashing | SHA2-256 (IPFS default) |
| Discovery hash | Argon2id |

### Network Security

- Noise & TLS protocols for transport encryption
- Public key pinning via LibP2P PeerID
- Rate limiting (1000 requests/hour per IP)
- Exponential backoff for repeated requests
- NAT traversal with relay support

### Key Management

- Two independent derivation paths: signing & encryption
- Private keys encrypted at rest with user password
- Mnemonic-based backup and recovery

---

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                    CLI Application                          │
│  (cmd/node/main.go - flags, key mgmt, node lifecycle)      │
└────────────────┬────────────────┬────────────────┬──────────┘
                 │                │                │
         ┌───────▼──────┐ ┌──────▼────────┐ ┌────▼────────┐
         │ Socket Server│ │ HTTP/HTTPS    │ │ LibP2P Node │
         │ (cmd/socket) │ │ Server        │ │ (internal)  │
         └───────┬──────┘ └──────┬────────┘ └────┬────────┘
                 │                │               │
         ┌───────▼────────────────▼───────────────▼────────┐
         │       Node Struct (internal/node/node.go)       │
         │   LibP2P Host, DHT, IPFS, Wallet, EPM           │
         └───────┬──────────────┬────────────────┬─────────┘
                 │              │                │
         ┌───────▼──────┐ ┌─────▼─────┐ ┌──────▼──────┐
         │ File Watcher │ │ Peer      │ │ PubSub      │
         │              │ │ Discovery │ │             │
         └───────┬──────┘ └─────┬─────┘ └──────┬──────┘
                 │              │               │
         ┌───────▼──────────────▼───────────────▼────────┐
         │        SDS Utilities (sds_utils/)             │
         │    EPM/PNM creation & Flatbuffer handling     │
         └───────┬──────────────────────────────┬────────┘
                 │                              │
         ┌───────▼──────────┐         ┌────────▼──────────┐
         │  IPFS/Kubo Node  │         │   Server Config   │
         └──────────────────┘         └───────────────────┘
```

---

## Deployment

### Network Topology

```
                     Internet
                        │
        ┌───────────────┼───────────────┐
        │               │               │
    [Node 1]        [Node 2]        [Node N]
    (:443)          (:443)          (:443)
        │               │               │
        └───────────────┼───────────────┘
                        │
                   LibP2P DHT
                   mDNS Discovery
                   PubSub Mesh
                        │
        ┌───────────────┼───────────────┐
        │               │               │
    [IPFS DHT]    [Bootstrap]       [Relays]
```

### Building

```bash
# Standard build
./build.sh

# Distribution builds (multi-platform)
./build_dist.sh
```

### Running

```bash
# Start node
./spacedatanetwork

# With custom datastore
SPACE_DATA_NETWORK_DATASTORE_DIRECTORY=/path/to/data ./spacedatanetwork

# List peers
./spacedatanetwork --list-peers
```

---

## Edge Relay Servers

### Purpose

Edge relay servers are lightweight, stripped-down nodes deployed on low-spec machines at the network edge. Their sole purpose is to help JavaScript/browser clients connect through firewalls and NAT. They do **NOT** participate in content storage or pinning.

### Why They're Needed

**Browser Limitations:**
- Browsers can only use WebSockets, WebTransport, and WebRTC
- Cannot open raw TCP/UDP listening ports
- Cannot participate directly in Kademlia DHT
- Cannot do traditional hole punching like native apps

**The Problem:**
When both a browser client and a target Go node are behind NAT/firewalls, they cannot establish direct connections. Edge relays solve this by providing publicly-routable bridge points.

### Characteristics

| Property | Edge Relay | Full Node |
|----------|------------|-----------|
| IPFS Pinning | **Disabled** | Enabled |
| Content Storage | None | 10GB max |
| DHT Participation | Yes | Yes |
| Circuit Relay v2 | **Primary function** | Enabled |
| WebSocket Endpoint | Required (`:8080/ws`) | Optional |
| PubSub | Discovery topics only | All SDS topics |
| Hardware Requirements | Very low (1 CPU, 512MB RAM) | Standard |
| File Watcher | Disabled | Enabled |
| EPM/Identity | Minimal | Full |

### Connection Flow

```
┌──────────────────┐     WebSocket      ┌──────────────────┐
│  Browser Client  │◄──────────────────►│  Edge Relay      │
│  (behind NAT)    │                    │  (public IP)     │
└──────────────────┘                    └────────┬─────────┘
                                                 │
                                          p2p-circuit
                                                 │
                                        ┌────────▼─────────┐
                                        │  Target Node     │
                                        │  (behind NAT)    │
                                        └──────────────────┘
```

**Relay Address Format:**
```
/ip4/<edge-relay-ip>/tcp/8080/ws/p2p/<relay-peer-id>/p2p-circuit/p2p/<target-peer-id>
```

### JavaScript Client Configuration

The JavaScript library ([javascript/sdn.libp2p.ts](javascript/sdn.libp2p.ts)) needs to be seeded with edge relay IPs:

```typescript
// Bootstrap with known edge relay servers
bootstrap({
    list: [
        '/ip4/<EDGE_RELAY_1_IP>/tcp/8080/ws/p2p/<PEER_ID_1>',
        '/ip4/<EDGE_RELAY_2_IP>/tcp/8080/ws/p2p/<PEER_ID_2>',
        // ... more edge relays for redundancy
    ]
})
```

```typescript
// Circuit relay transport discovers and uses edge relays
circuitRelayTransport({
    discoverRelays: 100,
    reservationConcurrency: 100
})
```

### Edge Relay Server Configuration

Edge relays should be started with a minimal configuration:

| Setting | Value | Reason |
|---------|-------|--------|
| `--edge-relay-mode` | `true` | Enables relay-only mode |
| IPFS storage | `0` or disabled | No content pinning |
| GC | Aggressive | Minimize memory |
| File watcher | Disabled | No content publishing |
| PubSub topics | Discovery only | Reduce bandwidth |

### Deployment Strategy

```
                        Global Edge Relay Network
                                  │
        ┌─────────────────────────┼─────────────────────────┐
        │                         │                         │
   [US-West Edge]           [EU Edge]              [Asia Edge]
   Low-spec VPS             Low-spec VPS           Low-spec VPS
   Public IP                Public IP              Public IP
   WebSocket :8080          WebSocket :8080        WebSocket :8080
        │                         │                         │
        └─────────────────────────┼─────────────────────────┘
                                  │
                           LibP2P DHT Mesh
                                  │
        ┌─────────────────────────┼─────────────────────────┐
        │                         │                         │
   [Full Node A]           [Full Node B]           [Full Node C]
   Behind NAT              Behind NAT              Public/NAT
   Content Storage         Content Storage         Content Storage
```

**Recommended Deployment:**
- Minimum 3 edge relays for geographic redundancy
- Use cheap VPS providers ($5/month tier sufficient)
- Public IPv4 required (IPv6 optional)
- No domain required (IP-based addressing)
- Systemd service with auto-restart

### Bridge Responsibilities

Edge relays must bridge between DHT and PubSub for browser discovery:

1. **DHT → PubSub Bridge**: Announce peers discovered via DHT to PubSub topics so browsers can find them
2. **Relay Announcements**: Publish their own relay addresses to PubSub so browsers can discover available relays
3. **Connection Mediation**: Accept relay reservations from both browsers and full nodes

### TODO: Implementation Requirements

- [ ] Add `--edge-relay-mode` CLI flag
- [ ] Disable IPFS pinning in edge mode
- [ ] Disable file watcher in edge mode
- [ ] Create DHT → PubSub peer discovery bridge
- [ ] Publish relay availability to discovery PubSub topic
- [ ] Configurable list of seed edge relay IPs for JS library
- [ ] Minimal memory footprint configuration
- [ ] Health check endpoint for monitoring

---

## Design Patterns

| Pattern | Implementation |
|---------|----------------|
| Daemon | Long-running background service |
| Producer-Consumer | File watcher → IPFS upload queue |
| Publish-Subscribe | GossipSub for standard channels |
| Service Discovery | DHT + mDNS + Relay |
| IPC | Unix socket for CLI commands |
| Middleware Stack | CORS → Rate Limit → Handler |

---

## Key Files Reference

| File | Purpose |
|------|---------|
| `cmd/node/main.go` | Entry point, CLI flags, key management |
| `cmd/socket/main.go` | IPC socket server |
| `cmd/http/*.go` | HTTPS server, domain verification, TLS |
| `internal/node/node.go` | Node struct & lifecycle management |
| `internal/node/ipfs_node.go` | IPFS add, pin, IPNS operations |
| `internal/node/peerdiscovery.go` | DHT + mDNS peer discovery |
| `internal/node/protocols/id.protocol.go` | PNM exchange protocol |
| `internal/node/pubsub/main.go` | GossipSub setup |
| `internal/node/watch_folder.go` | File monitoring & debouncing |
| `internal/node/sds_utils/epm.go` | EPM Flatbuffer utilities |
| `internal/node/sds_utils/pnm.go` | PNM Flatbuffer utilities |
| `internal/spacedatastandards/` | 30+ schema implementations |
| `serverconfig/serverconfig.go` | Global configuration singleton |
| `retrievers/celestrak.go` | External data fetcher |

---

## Future Enhancements (TODOs in codebase)

- File manifest with digital signatures
- Storage limits (1GB/1000 files per node)
- DNS detection for network peers
- CORS restriction to network-only peers
- Binary update channel
- JSON manifest of active PNMs
- Program manifest schema
- Direct dial protocols for encrypted file retrieval
