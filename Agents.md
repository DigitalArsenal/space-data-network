# SDS - Custom IPFS Fork for Space Data Network

## Overview

**SDS** is a specialized fork of IPFS (Kubo server + IPFS Desktop) tailored for the Space Data Network. It replaces generic content-addressed storage with FlatBuffer-native data handling and SQLite-based structured storage, optimized for space data standards.

### Why "SDS"

A stripped-down, specialized version of IPFS that knows exactly what it's looking for - Space Data Standards - and nothing else.

---

## Project Goals

| Goal | Description |
|------|-------------|
| **Lean Runtime** | Minimal memory/CPU footprint for edge relay deployment |
| **FlatBuffer Native** | All data transmission uses FlatBuffers, not arbitrary bytes |
| **SQLite Storage** | Structured queryable storage via flatbuffers-sqlite |
| **Standards Enforced** | Only accepts data conforming to spacedatastandards.org schemas |
| **Browser Compatible** | JavaScript library works behind firewalls via edge relays |

---

## Source Repositories

### Upstream Forks

| Repository | Purpose | Fork Target |
|------------|---------|-------------|
| [ipfs/kubo](https://github.com/ipfs/kubo) | Go IPFS server daemon | `spacedatanetwork-server` |
| [ipfs/ipfs-desktop](https://github.com/ipfs/ipfs-desktop) | Electron desktop app | `spacedatanetwork-desktop` |

### Local Dependencies

| Path | Purpose |
|------|---------|
| `../flatbuffers/wasm` | FlatBuffer compiler + encryption WASM module |
| `../flatbuffers-sqlite` | SQLite storage with FlatBuffer virtual tables |
| [spacedatastandards.org](https://github.com/DigitalArsenal/spacedatastandards.org) | Canonical schema definitions |

---

## Architecture

### Component Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           SDS ARCHITECTURE                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐       │
│  │  sdn-server     │     │  sdn-desktop    │     │  sdn-js         │       │
│  │  (Go daemon)    │     │ (Electron app)  │     │  (Browser lib)  │       │
│  └────────┬────────┘     └────────┬────────┘     └────────┬────────┘       │
│           │                       │                       │                 │
│           └───────────────────────┼───────────────────────┘                 │
│                                   │                                         │
│                    ┌──────────────▼──────────────┐                          │
│                    │     LibP2P Transport        │                          │
│                    │  (TCP, WS, QUIC, WebRTC)    │                          │
│                    └──────────────┬──────────────┘                          │
│                                   │                                         │
│           ┌───────────────────────┼───────────────────────┐                 │
│           │                       │                       │                 │
│  ┌────────▼────────┐   ┌─────────▼─────────┐   ┌────────▼────────┐         │
│  │  flatc-wasm     │   │  flatsql.wasm     │   │  SDS Schemas    │         │
│  │  (encryption +  │   │  (SQLite storage) │   │  (validation)   │         │
│  │   serialization)│   │                   │   │                 │         │
│  └─────────────────┘   └───────────────────┘   └─────────────────┘         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Data Flow

```
┌────────────────────────────────────────────────────────────────────────────┐
│                           DATA FLOW                                        │
├────────────────────────────────────────────────────────────────────────────┤
│                                                                            │
│  INGEST                    VALIDATE                   STORE                │
│  ──────                    ────────                   ─────                │
│                                                                            │
│  JSON/Binary  ──►  flatc-wasm  ──►  SDS Schema  ──►  flatsql  ──►  SQLite │
│  (any format)      (normalize)      (validate)       (index)     (persist)│
│                                                                            │
│  TRANSMIT                  ENCRYPT                    PUBLISH              │
│  ────────                  ───────                    ───────              │
│                                                                            │
│  FlatBuffer  ──►  flatc-wasm  ──►  LibP2P  ──►  PubSub/DHT  ──►  Peers    │
│  (binary)        (AES+ECDH)       (transport)  (announce)       (network) │
│                                                                            │
└────────────────────────────────────────────────────────────────────────────┘
```

---

## Implementation Plan

### Phase 1: Fork and Strip

**Branch:** `sdn-base`

#### 1.1 Fork Kubo

```bash
git clone https://github.com/ipfs/kubo.git spacedatanetwork-server
cd spacedatanetwork-server
git checkout -b sdn-base
```

**Remove:**
- [x] Generic content pinning (we only store validated SDS data)
- [x] Bitswap (replaced by SDS-specific protocol) - *optional, may keep for compatibility*
- [x] Gateway HTTP server (not needed for SDN)

**Keep (CRITICAL - currently in use by SDN):**
- [x] LibP2P core (host, DHT, PubSub)
- [x] **IPNS name system** - actively used for publishing folder hierarchies (30-second publish cycle)
- [x] **IPNS over PubSub** - enabled via `cfg.Ipns.UsePubsub = config.True`
- [x] **Kademlia DHT** - used for peer routing AND content discovery
- [x] **Custom DHT Discovery Namespace** - Argon2 hash of version string as rendezvous point:

  ```go
  versionHex := []byte(serverconfig.Conf.Info.Version)
  discoveryHex := hex.EncodeToString(argon2.IDKey(versionHex, versionHex, 1, 64*1024, 4, 32))
  ```

- [x] **GossipSub PubSub** - per-standard topics (e.g., `{discoveryHex}-PNM`)
- [x] **FlatFS blockstore** - v1/next-to-last/2 sharding for `/blocks`
- [x] **LevelDB datastore** - for general key-value storage at `/`
- [x] Circuit relay v2 + AutoRelay feeder (fed from DHT peers)
- [x] Hole punching / AutoNAT
- [x] Connection manager
- [x] Peer discovery (DHT routing discovery + mDNS with service name `"space-data-network-mdns"`)
- [x] All transports: TCP, WebSocket, QUIC, WebTransport

**Custom SDN Protocols (already implemented):**
- [x] `/space-data-network/id-exchange/1.0.0` - PNM/EPM exchange between peers
- [x] `/space-data-network/chat/1.0.0` - peer chat

#### 1.2 Fork IPFS Desktop

```bash
git clone https://github.com/ipfs/ipfs-desktop.git spacedatanetwork-desktop
cd spacedatanetwork-desktop
git checkout -b sdn-base
```

**Remove:**
- [x] File browser UI (replaced by SDS data explorer)
- [x] IPFS config UI (simplified for SDS)
- [x] Experiments/beta features
- [x] Gateway controls

**Keep:**
- [x] System tray integration
- [x] Auto-start functionality
- [x] Electron shell
- [x] Update mechanism

---

### Phase 2: Integrate FlatBuffers

**Branch:** `sdn-flatbuffers`

#### 2.1 Add flatc-wasm to Server

**Go Integration:**

```go
// internal/wasm/flatc.go
package wasm

import (
    "context"
    "github.com/tetratelabs/wazero"
    "github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

type FlatcModule struct {
    runtime wazero.Runtime
    module  wazero.CompiledModule

    // Exported functions
    malloc          api.Function
    free            api.Function
    jsonToBinary    api.Function
    binaryToJson    api.Function
    validateSchema  api.Function
    encrypt         api.Function
    decrypt         api.Function
    sign            api.Function
    verify          api.Function
}

func NewFlatcModule(ctx context.Context, wasmPath string) (*FlatcModule, error) {
    r := wazero.NewRuntime(ctx)
    wasi_snapshot_preview1.MustInstantiate(ctx, r)

    wasmBytes, err := os.ReadFile(wasmPath)
    if err != nil {
        return nil, err
    }

    module, err := r.Instantiate(ctx, wasmBytes)
    if err != nil {
        return nil, err
    }

    return &FlatcModule{
        runtime:        r,
        malloc:         module.ExportedFunction("malloc"),
        free:           module.ExportedFunction("free"),
        jsonToBinary:   module.ExportedFunction("wasi_json_to_binary"),
        binaryToJson:   module.ExportedFunction("wasi_binary_to_json"),
        encrypt:        module.ExportedFunction("wasi_encrypt_bytes"),
        decrypt:        module.ExportedFunction("wasi_decrypt_bytes"),
        sign:           module.ExportedFunction("wasi_ed25519_sign"),
        verify:         module.ExportedFunction("wasi_ed25519_verify"),
    }, nil
}
```

#### 2.2 Add SDS Schema Validation

```go
// internal/sds/validator.go
package sds

import (
    "embed"
    "fmt"
)

//go:embed schemas/*.fbs
var schemasFS embed.FS

type Validator struct {
    flatc   *wasm.FlatcModule
    schemas map[string]int  // schema name -> schema ID
}

func NewValidator(flatc *wasm.FlatcModule) (*Validator, error) {
    v := &Validator{
        flatc:   flatc,
        schemas: make(map[string]int),
    }

    // Load all SDS schemas
    entries, _ := schemasFS.ReadDir("schemas")
    for _, entry := range entries {
        content, _ := schemasFS.ReadFile("schemas/" + entry.Name())
        id, err := flatc.AddSchema(entry.Name(), content)
        if err != nil {
            return nil, fmt.Errorf("failed to load schema %s: %w", entry.Name(), err)
        }
        v.schemas[entry.Name()] = id
    }

    return v, nil
}

func (v *Validator) Validate(schemaName string, data []byte) error {
    schemaID, ok := v.schemas[schemaName]
    if !ok {
        return fmt.Errorf("unknown schema: %s", schemaName)
    }

    // Try to parse as FlatBuffer - if it fails, data is invalid
    _, err := v.flatc.BinaryToJSON(schemaID, data)
    return err
}
```

#### 2.3 Desktop Integration

Update `spacedatanetwork-desktop` to use flatc-wasm for:

```typescript
// src/flatbuffers/index.ts
import { FlatcRunner } from 'flatc-wasm';
import { loadEncryptionWasm } from 'flatc-wasm/encryption';

let flatc: FlatcRunner;

export async function initFlatBuffers() {
    flatc = await FlatcRunner.init();
    await loadEncryptionWasm();

    // Load all SDS schemas
    for (const [name, content] of Object.entries(SDS_SCHEMAS)) {
        flatc.mountFile(`/schemas/${name}`, content);
    }
}

export function validateAndConvert(schemaName: string, data: string | Uint8Array) {
    const schema = { entry: `/schemas/${schemaName}`, files: SDS_SCHEMAS };

    if (typeof data === 'string') {
        return flatc.generateBinary(schema, data);
    } else {
        return flatc.generateJSON(schema, { path: '/data.bin', data });
    }
}
```

---

### Phase 3: Integrate SQLite Storage

**Branch:** `sdn-storage`

#### 3.1 Replace Blockstore with flatsql

```go
// internal/storage/flatsql.go
package storage

/*
#cgo LDFLAGS: -L${SRCDIR}/../../flatbuffers-sqlite/wasm -lflatsql
*/
import "C"

import (
    "database/sql"
    _ "github.com/AltAlpha/flatsql-go"  // Custom SQLite driver with FlatBuffer VTables
)

type FlatSQLStore struct {
    db        *sql.DB
    validator *sds.Validator
}

func NewFlatSQLStore(dbPath string, validator *sds.Validator) (*FlatSQLStore, error) {
    db, err := sql.Open("flatsql", dbPath)
    if err != nil {
        return nil, err
    }

    // Create tables for each SDS type
    for schemaName := range validator.Schemas() {
        tableName := schemaNameToTable(schemaName)
        _, err := db.Exec(fmt.Sprintf(`
            CREATE TABLE IF NOT EXISTS %s (
                cid TEXT PRIMARY KEY,
                peer_id TEXT,
                timestamp INTEGER,
                data BLOB,
                signature BLOB
            )
        `, tableName))
        if err != nil {
            return nil, err
        }

        // Create FlatBuffer virtual table for querying
        _, err = db.Exec(fmt.Sprintf(`
            CREATE VIRTUAL TABLE IF NOT EXISTS %s_fb USING flatbuffer(
                schema_file='%s',
                source_table='%s',
                data_column='data'
            )
        `, tableName, schemaName, tableName))
        if err != nil {
            return nil, err
        }
    }

    return &FlatSQLStore{db: db, validator: validator}, nil
}

func (s *FlatSQLStore) Store(schemaName string, data []byte, peerId string, signature []byte) (string, error) {
    // Validate data against schema
    if err := s.validator.Validate(schemaName, data); err != nil {
        return "", fmt.Errorf("validation failed: %w", err)
    }

    // Compute CID
    cid := computeCID(data)

    // Store
    tableName := schemaNameToTable(schemaName)
    _, err := s.db.Exec(
        fmt.Sprintf("INSERT OR REPLACE INTO %s (cid, peer_id, timestamp, data, signature) VALUES (?, ?, ?, ?, ?)", tableName),
        cid, peerId, time.Now().Unix(), data, signature,
    )

    return cid, err
}

func (s *FlatSQLStore) Query(schemaName string, query string, args ...interface{}) ([][]byte, error) {
    tableName := schemaNameToTable(schemaName)

    // Query against the FlatBuffer virtual table
    rows, err := s.db.Query(
        fmt.Sprintf("SELECT data FROM %s WHERE %s", tableName, query),
        args...,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var results [][]byte
    for rows.Next() {
        var data []byte
        rows.Scan(&data)
        results = append(results, data)
    }

    return results, nil
}
```

#### 3.2 Query Examples

```go
// Query OMM records for a specific satellite
omms, err := store.Query("OMM.fbs",
    "OBJECT_ID = ? AND EPOCH > ?",
    "25544",  // ISS NORAD ID
    time.Now().Add(-24*time.Hour).Unix(),
)

// Query CDM records for conjunction events
cdms, err := store.Query("CDM.fbs",
    "TCA > ? AND MISS_DISTANCE < ?",
    time.Now().Unix(),
    1000.0,  // meters
)

// Query EPM records by organization
epms, err := store.Query("EPM.fbs",
    "LEGAL_NAME LIKE ?",
    "%Space Agency%",
)
```

---

### Phase 4: SDS Protocol Implementation

**Branch:** `sdn-protocol`

#### 4.1 Custom Protocol Handlers

```go
// internal/protocol/sds_exchange.go
package protocol

const SDSProtocolID = "/spacedatanetwork/sds-exchange/1.0.0"

type SDSExchangeHandler struct {
    store     *storage.FlatSQLStore
    validator *sds.Validator
    flatc     *wasm.FlatcModule
}

func (h *SDSExchangeHandler) HandleStream(s network.Stream) {
    defer s.Close()

    // Read message type (1 byte)
    msgType := make([]byte, 1)
    s.Read(msgType)

    switch msgType[0] {
    case 0x01: // REQUEST_DATA
        h.handleDataRequest(s)
    case 0x02: // PUSH_DATA
        h.handleDataPush(s)
    case 0x03: // QUERY
        h.handleQuery(s)
    }
}

func (h *SDSExchangeHandler) handleDataPush(s network.Stream) {
    // Read schema name length (2 bytes) + schema name
    schemaNameLen := make([]byte, 2)
    s.Read(schemaNameLen)
    schemaName := make([]byte, binary.BigEndian.Uint16(schemaNameLen))
    s.Read(schemaName)

    // Read data length (4 bytes) + data
    dataLen := make([]byte, 4)
    s.Read(dataLen)
    data := make([]byte, binary.BigEndian.Uint32(dataLen))
    s.Read(data)

    // Read signature (64 bytes for Ed25519)
    signature := make([]byte, 64)
    s.Read(signature)

    // Verify signature
    peerPubKey := extractPubKeyFromPeerID(s.Conn().RemotePeer())
    if !h.flatc.Verify(peerPubKey, data, signature) {
        s.Write([]byte{0x00}) // REJECT
        return
    }

    // Store
    cid, err := h.store.Store(string(schemaName), data, s.Conn().RemotePeer().String(), signature)
    if err != nil {
        s.Write([]byte{0x00}) // REJECT
        return
    }

    // ACK with CID
    s.Write([]byte{0x01}) // ACCEPT
    s.Write([]byte(cid))
}
```

#### 4.2 PubSub Topics per Schema

```go
// internal/pubsub/sds_topics.go
package pubsub

func SetupSDSTopics(ps *pubsub.PubSub, validator *sds.Validator) map[string]*pubsub.Topic {
    topics := make(map[string]*pubsub.Topic)

    for schemaName := range validator.Schemas() {
        topicName := fmt.Sprintf("/spacedatanetwork/sds/%s", schemaName)
        topic, _ := ps.Join(topicName)
        topics[schemaName] = topic
    }

    return topics
}
```

---

### Phase 5: Edge Relay Mode

**Branch:** `sdn-edge`

#### 5.1 Minimal Edge Configuration

```go
// cmd/spacedatanetwork/edge.go
package main

type EdgeConfig struct {
    ListenAddrs     []string  // /ip4/0.0.0.0/tcp/8080/ws
    BootstrapPeers  []string
    RelayEnabled    bool      // Always true for edge
    MaxConnections  int       // Low (100-500)

    // Disabled features
    StorageEnabled  bool      // false
    QueryEnabled    bool      // false
    PinningEnabled  bool      // false
}

func NewEdgeNode(cfg EdgeConfig) (*EdgeNode, error) {
    // Minimal LibP2P host
    h, err := libp2p.New(
        libp2p.ListenAddrStrings(cfg.ListenAddrs...),
        libp2p.EnableRelay(),
        libp2p.EnableRelayService(),  // BE a relay for others
        libp2p.EnableHolePunching(),
        libp2p.ConnectionManager(connmgr.NewConnManager(
            10,               // low water
            cfg.MaxConnections,  // high water
            connmgr.WithGracePeriod(time.Minute),
        )),
        // WebSocket + QUIC only
        libp2p.Transport(ws.New),
        libp2p.Transport(quic.NewTransport),
    )
    if err != nil {
        return nil, err
    }

    // DHT for peer discovery only (no content routing)
    dht, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
    if err != nil {
        return nil, err
    }

    // PubSub for message relay only
    ps, err := pubsub.NewGossipSub(ctx, h)
    if err != nil {
        return nil, err
    }

    return &EdgeNode{
        Host:   h,
        DHT:    dht,
        PubSub: ps,
    }, nil
}
```

#### 5.2 Edge CLI

```bash
# Start edge relay
spacedatanetwork edge --listen /ip4/0.0.0.0/tcp/8080/ws --max-conns 500

# With specific bootstrap peers
spacedatanetwork edge \
    --listen /ip4/0.0.0.0/tcp/8080/ws \
    --bootstrap /ip4/1.2.3.4/tcp/8080/ws/p2p/QmPeer1 \
    --bootstrap /ip4/5.6.7.8/tcp/8080/ws/p2p/QmPeer2

# Health check endpoint
spacedatanetwork edge --listen /ip4/0.0.0.0/tcp/8080/ws --health-port 8081
```

---

### Phase 6: JavaScript Library

**Branch:** `sdn-js`

#### 6.1 Browser Library Structure

```
spacedatanetwork-js/
├── src/
│   ├── index.ts           # Main exports
│   ├── node.ts            # SDN P2P node
│   ├── storage.ts         # IndexedDB + flatsql.wasm
│   ├── crypto.ts          # Encryption wrapper
│   ├── schemas.ts         # Bundled SDS schemas
│   └── edge-discovery.ts  # Edge relay discovery
├── wasm/
│   ├── flatc-wasm.js      # From ../flatbuffers/wasm
│   ├── flatc-encryption.wasm
│   └── flatsql.wasm       # From ../flatbuffers-sqlite
└── package.json
```

#### 6.2 Core API

```typescript
// src/node.ts
import { createLibp2p } from 'libp2p';
import { webSockets } from '@libp2p/websockets';
import { circuitRelayTransport } from '@libp2p/circuit-relay-v2';
import { FlatcRunner } from 'flatc-wasm';
import { FlatSQL } from './storage';
import { SDS_SCHEMAS, EDGE_RELAYS } from './schemas';

export class SDNNode {
    private libp2p: Libp2p;
    private flatc: FlatcRunner;
    private storage: FlatSQL;

    static async create(config: SDNConfig): Promise<SDNNode> {
        const flatc = await FlatcRunner.init();
        const storage = await FlatSQL.open('sdn-store');

        // Load all SDS schemas
        for (const [name, content] of Object.entries(SDS_SCHEMAS)) {
            flatc.mountFile(`/schemas/${name}`, content);
        }

        // Seed edge relays from config or defaults
        const bootstrapList = config.edgeRelays || EDGE_RELAYS;

        const libp2p = await createLibp2p({
            transports: [
                webSockets({ filter: filters.all }),
                circuitRelayTransport({
                    discoverRelays: 100,
                    reservationConcurrency: 10,
                }),
            ],
            peerDiscovery: [
                bootstrap({ list: bootstrapList }),
            ],
            // ... rest of config
        });

        return new SDNNode(libp2p, flatc, storage);
    }

    async publish(schemaName: string, data: object): Promise<string> {
        // Convert to FlatBuffer
        const schema = { entry: `/schemas/${schemaName}`, files: SDS_SCHEMAS };
        const binary = this.flatc.generateBinary(schema, JSON.stringify(data));

        // Sign
        const signature = await this.sign(binary);

        // Publish to topic
        const topic = `/spacedatanetwork/sds/${schemaName}`;
        await this.libp2p.services.pubsub.publish(topic,
            this.encodeMessage(binary, signature)
        );

        // Store locally
        const cid = await this.storage.store(schemaName, binary, signature);

        return cid;
    }

    async query(schemaName: string, filter: QueryFilter): Promise<object[]> {
        return this.storage.query(schemaName, filter);
    }

    async subscribe(schemaName: string, handler: (data: object) => void): Promise<void> {
        const topic = `/spacedatanetwork/sds/${schemaName}`;
        await this.libp2p.services.pubsub.subscribe(topic);

        this.libp2p.services.pubsub.addEventListener('message', (evt) => {
            if (evt.detail.topic === topic) {
                const { binary, signature } = this.decodeMessage(evt.detail.data);

                // Verify signature
                if (!this.verify(evt.detail.from, binary, signature)) {
                    console.warn('Invalid signature from', evt.detail.from);
                    return;
                }

                // Decode and deliver
                const schema = { entry: `/schemas/${schemaName}`, files: SDS_SCHEMAS };
                const json = this.flatc.generateJSON(schema, { path: '/d.bin', data: binary });
                handler(JSON.parse(json));
            }
        });
    }
}
```

#### 6.3 Edge Relay Discovery

```typescript
// src/edge-discovery.ts

// Default edge relays (seeded from configuration)
export const DEFAULT_EDGE_RELAYS = [
    '/ip4/209.182.234.97/tcp/8080/ws/p2p/16Uiu2HAkxKtJncDGfgtFpx4mNqtrzbBBrCZ8iaKKyKuEqEHuEz5J',
    // Add more edge relay addresses here
];

export class EdgeDiscovery {
    private knownRelays: Set<string> = new Set(DEFAULT_EDGE_RELAYS);
    private libp2p: Libp2p;

    constructor(libp2p: Libp2p) {
        this.libp2p = libp2p;

        // Subscribe to edge relay announcements
        this.libp2p.services.pubsub.subscribe('/spacedatanetwork/edge-relays');
        this.libp2p.services.pubsub.addEventListener('message', (evt) => {
            if (evt.detail.topic === '/spacedatanetwork/edge-relays') {
                const relay = new TextDecoder().decode(evt.detail.data);
                this.knownRelays.add(relay);
            }
        });
    }

    getRelays(): string[] {
        return Array.from(this.knownRelays);
    }

    async dialThroughRelay(targetPeerId: string): Promise<Connection> {
        for (const relay of this.knownRelays) {
            try {
                const relayAddr = multiaddr(relay);
                const circuitAddr = relayAddr.encapsulate(`/p2p-circuit/p2p/${targetPeerId}`);
                return await this.libp2p.dial(circuitAddr);
            } catch (err) {
                continue;
            }
        }
        throw new Error('No relay could reach target');
    }
}
```

---

### Phase 7: Schema Integration

**Branch:** `sdn-schemas`

#### 7.1 Clone and Embed Schemas

```bash
# Add as submodule
git submodule add https://github.com/DigitalArsenal/spacedatastandards.org schemas/sds

# Or vendor directly
curl -L https://github.com/DigitalArsenal/spacedatastandards.org/archive/main.tar.gz | tar xz
mv spacedatastandards.org-main/schemas internal/schemas/sds
```

#### 7.2 Schema Registry

```go
// internal/sds/registry.go
package sds

//go:embed schemas/sds/*.fbs
var sdsSchemasFS embed.FS

var SupportedSchemas = []string{
    "EPM.fbs",   // Entity Profile Manifest
    "PNM.fbs",   // Peer Network Manifest
    "OMM.fbs",   // Orbit Mean-Elements Message
    "OEM.fbs",   // Orbit Ephemeris Message
    "CDM.fbs",   // Conjunction Data Message
    "CAT.fbs",   // Catalog
    "CSM.fbs",   // Conjunction Summary Message
    "LDM.fbs",   // Launch Data Message
    "IDM.fbs",   // Initial Data Message
    "PLD.fbs",   // Payload
    "BOV.fbs",   // Body Orientation and Velocity
    "EOO.fbs",   // Earth Orientation
    "RFM.fbs",   // Reference Frame Message
    // ... all 30+ standards
}
```

---

### Phase 8: Encrypted Edge Relay Distribution (CDN + WASM)

**Branch:** `sdn-edge-discovery`

#### 8.1 Problem Statement

Browser clients need to bootstrap into the network but:
- Static IP lists become stale as edge relays change
- Plaintext IP lists can be easily blocked by censors
- CDN-hosted JS files need automatic updates when relays change

#### 8.2 Solution: WASM-Encrypted Relay Registry

The JavaScript library contains a WASM module that:
1. Holds an **encrypted list** of edge relay multiaddrs
2. Contains the **embedded decryption key**
3. Decrypts and returns the relay list at runtime
4. Gets **rebuilt and redeployed** when DHT detects new edge relays

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    EDGE RELAY DISTRIBUTION FLOW                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────────┐      DHT Discovery      ┌──────────────────────┐         │
│  │ Edge Relays  │ ◄────────────────────── │  Full Nodes          │         │
│  │ (announce)   │                         │  (monitor DHT)       │         │
│  └──────────────┘                         └──────────┬───────────┘         │
│                                                      │                      │
│                                           Detect new/removed relays         │
│                                                      │                      │
│                                                      ▼                      │
│                                           ┌──────────────────────┐         │
│                                           │  Registry Builder    │         │
│                                           │  - Collect relay IPs │         │
│                                           │  - Encrypt with key  │         │
│                                           │  - Compile to WASM   │         │
│                                           └──────────┬───────────┘         │
│                                                      │                      │
│                                                      ▼                      │
│  ┌──────────────┐      CDN Push           ┌──────────────────────┐         │
│  │ CDN Servers  │ ◄────────────────────── │  edge-relays.wasm    │         │
│  │ (global)     │                         │  (encrypted blob)    │         │
│  └──────┬───────┘                         └──────────────────────┘         │
│         │                                                                   │
│         │  JS Download                                                      │
│         ▼                                                                   │
│  ┌──────────────┐      Load WASM          ┌──────────────────────┐         │
│  │ Browser      │ ────────────────────► │  Decrypt at runtime  │         │
│  │ Client       │ ◄──────────────────── │  Return relay list   │         │
│  └──────────────┘      Relay IPs          └──────────────────────┘         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### 8.3 WASM Relay Registry Module

```cpp
// edge-relays.cpp - Compiled to WASM
#include <cstdint>
#include <cstring>
#include <vector>
#include <string>

// Embedded encrypted relay data (rebuilt on each update)
static const uint8_t ENCRYPTED_RELAYS[] = {
    // ... encrypted multiaddr list (replaced at build time)
};
static const size_t ENCRYPTED_RELAYS_LEN = sizeof(ENCRYPTED_RELAYS);

// Embedded key (XOR-obfuscated, unique per build)
static const uint8_t KEY_MATERIAL[] = {
    // ... obfuscated key bytes (replaced at build time)
};

// Simple XChaCha20 decryption (key embedded)
extern "C" {
    // Returns pointer to null-terminated JSON array of relay multiaddrs
    const char* get_edge_relays() {
        static std::string result;
        if (result.empty()) {
            // Deobfuscate key
            uint8_t key[32];
            for (int i = 0; i < 32; i++) {
                key[i] = KEY_MATERIAL[i] ^ KEY_MATERIAL[32 + (i % 32)];
            }

            // Decrypt relay list
            std::vector<uint8_t> decrypted = xchacha20_decrypt(
                ENCRYPTED_RELAYS, ENCRYPTED_RELAYS_LEN, key
            );

            result = std::string(decrypted.begin(), decrypted.end());

            // Clear key from memory
            memset(key, 0, 32);
        }
        return result.c_str();
    }

    // Returns number of relays
    int get_relay_count() {
        // Parse JSON and count
        const char* json = get_edge_relays();
        int count = 0;
        for (const char* p = json; *p; p++) {
            if (*p == '/') count++; // Count multiaddr starts
        }
        return count / 4; // Approximate (each multiaddr has ~4 slashes)
    }
}
```

#### 8.4 Build Pipeline for WASM Registry

```typescript
// scripts/build-edge-registry.ts
import { execSync } from 'child_process';
import { writeFileSync, readFileSync } from 'fs';
import { xchacha20Encrypt } from 'flatc-wasm/encryption';
import { randomBytes } from 'crypto';

interface RelayInfo {
    peerId: string;
    multiaddr: string;
    lastSeen: number;
    region: string;
}

async function buildEdgeRegistry(relays: RelayInfo[]) {
    // 1. Generate unique key for this build
    const key = randomBytes(32);
    const nonce = randomBytes(24);

    // 2. Create relay list JSON
    const relayList = JSON.stringify(
        relays.map(r => r.multiaddr)
    );

    // 3. Encrypt the relay list
    const plaintext = Buffer.from(relayList, 'utf8');
    const ciphertext = xchacha20Encrypt(plaintext, key, nonce);

    // 4. Obfuscate the key (XOR with random mask)
    const keyMask = randomBytes(32);
    const obfuscatedKey = Buffer.alloc(64);
    for (let i = 0; i < 32; i++) {
        obfuscatedKey[i] = key[i] ^ keyMask[i];
        obfuscatedKey[32 + i] = keyMask[i];
    }

    // 5. Generate C++ source with embedded data
    const cppSource = `
// AUTO-GENERATED - DO NOT EDIT
// Built: ${new Date().toISOString()}
// Relay count: ${relays.length}

static const uint8_t ENCRYPTED_RELAYS[] = {${
    Array.from(Buffer.concat([nonce, ciphertext]))
        .map(b => `0x${b.toString(16).padStart(2, '0')}`)
        .join(', ')
}};
static const size_t ENCRYPTED_RELAYS_LEN = ${nonce.length + ciphertext.length};

static const uint8_t KEY_MATERIAL[] = {${
    Array.from(obfuscatedKey)
        .map(b => `0x${b.toString(16).padStart(2, '0')}`)
        .join(', ')
}};
`;

    // 6. Write and compile to WASM
    writeFileSync('src/edge-relays-data.h', cppSource);

    execSync('emcc src/edge-relays.cpp -o dist/edge-relays.wasm \
        -s EXPORTED_FUNCTIONS="[_get_edge_relays, _get_relay_count, _malloc, _free]" \
        -s EXPORTED_RUNTIME_METHODS="[UTF8ToString]" \
        -O3 --closure 1');

    console.log(`Built edge-relays.wasm with ${relays.length} relays`);
}
```

#### 8.5 DHT Monitor and Auto-Rebuild

```go
// cmd/registry-builder/main.go
package main

// Monitors DHT for edge relay announcements and rebuilds WASM

type RegistryBuilder struct {
    dht          *dht.IpfsDHT
    knownRelays  map[string]*RelayInfo
    cdnEndpoints []string
    buildScript  string
    mu           sync.RWMutex
}

func (rb *RegistryBuilder) MonitorDHT(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            rb.discoverRelays(ctx)
        }
    }
}

func (rb *RegistryBuilder) discoverRelays(ctx context.Context) {
    // Find peers advertising as edge relays
    peers, err := rb.dht.FindProvidersAsync(ctx,
        cid.NewCidV1(cid.Raw, []byte("spacedatanetwork-edge-relay")), 100)

    updated := false
    for peer := range peers {
        // Verify peer is actually an edge relay
        if rb.verifyEdgeRelay(ctx, peer) {
            rb.mu.Lock()
            if _, exists := rb.knownRelays[peer.ID.String()]; !exists {
                rb.knownRelays[peer.ID.String()] = &RelayInfo{
                    PeerID:    peer.ID.String(),
                    Multiaddr: peer.Addrs[0].String(),
                    LastSeen:  time.Now().Unix(),
                }
                updated = true
                log.Info().Str("peer", peer.ID.String()).Msg("New edge relay discovered")
            }
            rb.mu.Unlock()
        }
    }

    // Prune stale relays
    rb.mu.Lock()
    for id, relay := range rb.knownRelays {
        if time.Now().Unix()-relay.LastSeen > 3600 { // 1 hour stale
            delete(rb.knownRelays, id)
            updated = true
            log.Info().Str("peer", id).Msg("Edge relay pruned (stale)")
        }
    }
    rb.mu.Unlock()

    if updated {
        rb.rebuildAndDeploy()
    }
}

func (rb *RegistryBuilder) rebuildAndDeploy() {
    rb.mu.RLock()
    relays := make([]RelayInfo, 0, len(rb.knownRelays))
    for _, r := range rb.knownRelays {
        relays = append(relays, *r)
    }
    rb.mu.RUnlock()

    // Write relay list for build script
    data, _ := json.Marshal(relays)
    os.WriteFile("/tmp/edge-relays.json", data, 0644)

    // Run build script
    cmd := exec.Command("npx", "ts-node", rb.buildScript, "/tmp/edge-relays.json")
    if err := cmd.Run(); err != nil {
        log.Error().Err(err).Msg("Failed to rebuild WASM")
        return
    }

    // Deploy to CDN endpoints
    for _, endpoint := range rb.cdnEndpoints {
        rb.deployToCDN(endpoint, "dist/edge-relays.wasm")
        rb.deployToCDN(endpoint, "dist/spacedatanetwork.js")
    }

    log.Info().Int("relays", len(relays)).Msg("Deployed updated edge registry")
}
```

#### 8.6 JavaScript Integration

```typescript
// src/edge-discovery.ts
let edgeRelaysModule: any = null;
let cachedRelays: string[] | null = null;

export async function loadEdgeRelays(): Promise<string[]> {
    if (cachedRelays) return cachedRelays;

    // Load WASM module
    if (!edgeRelaysModule) {
        edgeRelaysModule = await import('./edge-relays.wasm');
        await edgeRelaysModule.ready;
    }

    // Get decrypted relay list from WASM
    const relaysPtr = edgeRelaysModule._get_edge_relays();
    const relaysJson = edgeRelaysModule.UTF8ToString(relaysPtr);

    cachedRelays = JSON.parse(relaysJson);
    return cachedRelays;
}

// Used by SDNNode during initialization
export async function getBootstrapRelays(): Promise<string[]> {
    try {
        return await loadEdgeRelays();
    } catch (err) {
        console.warn('Failed to load encrypted relays, using fallback');
        return DEFAULT_FALLBACK_RELAYS;
    }
}
```

#### 8.7 CDN In-Place Update

The CDN servers host the JavaScript library and WASM files. When updates occur:

```bash
# CDN update script (run by registry-builder)
#!/bin/bash

WASM_FILE=$1
JS_FILE=$2
CDN_BUCKET=$3

# Upload with cache-busting headers
aws s3 cp $WASM_FILE s3://$CDN_BUCKET/edge-relays.wasm \
    --cache-control "max-age=300" \
    --content-type "application/wasm"

aws s3 cp $JS_FILE s3://$CDN_BUCKET/spacedatanetwork.js \
    --cache-control "max-age=300" \
    --content-type "application/javascript"

# Invalidate CDN cache
aws cloudfront create-invalidation \
    --distribution-id $CLOUDFRONT_ID \
    --paths "/edge-relays.wasm" "/spacedatanetwork.js"
```

#### 8.8 Security Considerations

| Threat | Mitigation |
|--------|------------|
| Key extraction from WASM | Key is XOR-obfuscated; extraction requires reverse engineering |
| Replay of old WASM | Short cache TTL (5 min); clients periodically refresh |
| CDN compromise | Multiple CDN endpoints; integrity checks via subresource integrity |
| IP blocking | Relays use diverse providers; encrypted list harder to enumerate |
| WASM tampering | Sign WASM with Ed25519; verify signature before execution |

#### 8.9 TODO: Implementation Requirements

- [x] Create `edge-relays.cpp` WASM module with embedded encryption
- [x] Build script to generate encrypted relay data
- [x] DHT monitor service (`registry-builder`)
- [x] CDN deployment automation
- [x] Subresource Integrity (SRI) hash generation
- [x] WASM signature verification in JS loader (SRI hash verification)
- [x] Fallback relay list for offline/failure scenarios
- [x] Metrics for relay discovery and WASM load success rates

---

## Build System

### Server Build

```bash
# Build spacedatanetwork server
cd spacedatanetwork-server
go build -o spacedatanetwork ./cmd/spacedatanetwork

# Build edge-only binary (smaller)
go build -tags edge -o spacedatanetwork-edge ./cmd/spacedatanetwork

# Cross-compile for edge deployment
GOOS=linux GOARCH=amd64 go build -tags edge -o spacedatanetwork-edge-linux-amd64 ./cmd/spacedatanetwork
GOOS=linux GOARCH=arm64 go build -tags edge -o spacedatanetwork-edge-linux-arm64 ./cmd/spacedatanetwork
```

### Desktop Build

```bash
# Build spacedatanetwork-desktop
cd spacedatanetwork-desktop
npm install
npm run build

# Package for distribution
npm run package:mac
npm run package:win
npm run package:linux
```

### JavaScript Library Build

```bash
# Build spacedatanetwork-js
cd spacedatanetwork-js
npm install
npm run build

# Output: dist/spacedatanetwork.js, dist/spacedatanetwork.esm.js, dist/spacedatanetwork.d.ts
```

---

## Configuration

### Server Config

```yaml
# ~/.spacedatanetwork/config.yaml
mode: full  # or "edge"

network:
  listen:
    - /ip4/0.0.0.0/tcp/4001
    - /ip4/0.0.0.0/tcp/8080/ws
    - /ip4/0.0.0.0/udp/4001/quic

  bootstrap:
    - /dnsaddr/bootstrap.digitalarsenal.io/p2p/QmBootstrap1

  edge_relays:
    - /ip4/203.0.113.1/tcp/8080/ws/p2p/QmEdge1
    - /ip4/203.0.113.2/tcp/8080/ws/p2p/QmEdge2

storage:
  path: ~/.spacedatanetwork/data
  max_size: 10GB
  gc_interval: 1h

schemas:
  validate: true
  strict: true  # Reject unknown schemas
```

### Edge Config

```yaml
# ~/.spacedatanetwork/edge-config.yaml
mode: edge

network:
  listen:
    - /ip4/0.0.0.0/tcp/8080/ws

  max_connections: 500

  bootstrap:
    - /dnsaddr/bootstrap.digitalarsenal.io/p2p/QmBootstrap1

# No storage in edge mode
```

---

## Migration from Current SDN

### Step 1: Extract Reusable Components

From current `go-space-data-network`:
- [x] Copy `internal/node/protocols/` → `spacedatanetwork-server/internal/protocol/` (reimplemented in sdn-server)
- [x] Copy `internal/node/sds_utils/` → `spacedatanetwork-server/internal/sds/` (reimplemented in sdn-server)
- [x] Copy `internal/spacedatastandards/` → `spacedatanetwork-server/internal/schemas/` (copied to sdn-server/internal/sds/schemas/)
- [x] Copy `javascript/sdn.libp2p.ts` → `spacedatanetwork-js/src/legacy/` (reimplemented in sdn-js)

### Step 2: Replace IPFS Dependencies

In `spacedatanetwork-server`:
```go
// Before (kubo)
import "github.com/ipfs/kubo/core"

// After (spacedatanetwork)
import "github.com/spacedatanetwork/spacedatanetwork-server/internal/storage"
```

### Step 3: Update JavaScript Library

```typescript
// Before (sdn.libp2p.ts)
const tokyo2WS = "/ip4/209.182.234.97/tcp/8080/ws/p2p/...";

// After (spacedatanetwork-js)
import { SDNNode, EDGE_RELAYS } from 'spacedatanetwork-js';

const node = await SDNNode.create({
    edgeRelays: EDGE_RELAYS,  // Configurable
});
```

---

## Testing Strategy

### Unit Tests

```bash
# Server
cd spacedatanetwork-server && go test ./...

# JavaScript
cd spacedatanetwork-js && npm test
```

### Integration Tests

```bash
# Start test network
docker-compose -f test/docker-compose.yml up -d

# Run integration tests
go test -tags integration ./test/...
```

### Edge Relay Tests

```bash
# Test firewall traversal
./test/firewall-test.sh

# Test browser connectivity
npx playwright test test/browser/*.spec.ts
```

---

## Deployment

### Edge Relay Deployment

```bash
# Deploy to edge server
ssh edge1.example.com
curl -L https://github.com/spacedatanetwork/spacedatanetwork-server/releases/latest/download/spacedatanetwork-edge-linux-amd64 -o /usr/local/bin/spacedatanetwork-edge
chmod +x /usr/local/bin/spacedatanetwork-edge

# Create systemd service
cat > /etc/systemd/system/spacedatanetwork-edge.service << EOF
[Unit]
Description=Space Data Network Edge Relay
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/spacedatanetwork-edge --listen /ip4/0.0.0.0/tcp/8080/ws --max-conns 500
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl enable spacedatanetwork-edge
systemctl start spacedatanetwork-edge
```

---

## Implementation Checklist

### Phase 0: Repository Restructuring

- [x] Create `legacy/` directory
- [x] Move existing code to legacy: `mv cmd internal javascript serverconfig backup docs retrievers scripts test go.mod go.sum *.sh dist tmp .air.toml .env README.md TODO.md legacy/`
- [x] Add Kubo upstream remote: `git remote add kubo-upstream https://github.com/ipfs/kubo.git`
- [x] Fetch Kubo: `git fetch kubo-upstream`
- [x] Add Kubo subtree: `git subtree add --prefix=kubo kubo-upstream/master --squash -m "Add Kubo as subtree"`
- [x] Add Desktop upstream remote: `git remote add desktop-upstream https://github.com/ipfs/ipfs-desktop.git`
- [x] Fetch Desktop: `git fetch desktop-upstream`
- [x] Add Desktop subtree: `git subtree add --prefix=desktop desktop-upstream/main --squash -m "Add IPFS Desktop as subtree"`
- [x] Add schemas submodule: `git submodule add https://github.com/DigitalArsenal/spacedatastandards.org.git schemas/sds`
- [x] Create `scripts/subtree-update.sh` for upstream merging

### Phase 1: Fork and Strip Kubo

- [x] Create `sdn-server/` directory structure
- [x] Create `sdn-server/go.mod` with replace directive for kubo
- [x] Create `sdn-server/cmd/spacedatanetwork/main.go`
- [x] Add `github.com/tetratelabs/wazero` dependency
- [x] Configure LibP2P with all transports (TCP, WS, QUIC, WebTransport)
- [x] Keep IPNS, DHT, PubSub, Circuit Relay v2
- [x] Remove Gateway HTTP server references
- [x] Verify build: `cd sdn-server && go build ./...`

### P2: FlatBuffers WASM Integration

- [x] Create `sdn-server/internal/wasm/flatc.go`
- [x] Implement FlatcModule struct with wazero runtime
- [x] Load flatc-wasm from `../flatbuffers/wasm/`
- [x] Expose functions: malloc, free, jsonToBinary, binaryToJson
- [x] Expose crypto functions: encrypt, decrypt, sign, verify
- [x] Create `sdn-server/internal/sds/validator.go`
- [x] Write unit tests for WASM integration

### Phase 3: SQLite Storage

- [x] Create `sdn-server/internal/storage/flatsql.go`
- [x] Implement FlatSQLStore struct
- [x] Load flatsql.wasm from `../flatbuffers-sqlite/`
- [x] Create tables per SDS schema (cid, peer_id, timestamp, data, signature)
- [x] Create FlatBuffer virtual tables for querying
- [x] Implement Store() and Query() methods
- [x] Write unit tests for storage layer

### Phase 4: SDS Protocol

- [x] Create `sdn-server/internal/protocol/sds_exchange.go`
- [x] Define SDSProtocolID: `/spacedatanetwork/sds-exchange/1.0.0`
- [x] Implement message types: REQUEST_DATA (0x01), PUSH_DATA (0x02), QUERY (0x03)
- [x] Implement HandleStream() for incoming connections
- [x] Implement handleDataPush() with signature verification
- [x] Create `sdn-server/internal/pubsub/topics.go`
- [x] Setup per-schema PubSub topics: `/spacedatanetwork/sds/{schemaName}`
- [x] Migrate patterns from `legacy/internal/node/protocols/`

### P5: Edge Relay Binary

- [x] Create `sdn-server/cmd/spacedatanetwork-edge/main.go`
- [x] Implement EdgeConfig struct (ListenAddrs, MaxConnections, etc.)
- [x] Create minimal LibP2P host (WS + QUIC only)
- [x] Enable relay service but disable storage
- [x] Add build tag: `// +build edge`
- [x] Verify smaller binary: `go build -tags edge`
- [x] Create systemd service file template

### P6: Browser SDK (sdn-js)

- [x] Create `sdn-js/` directory structure
- [x] Create `sdn-js/package.json`
- [x] Migrate `legacy/javascript/sdn.libp2p.ts` to `sdn-js/src/node.ts`
- [x] Create `sdn-js/src/storage.ts` (IndexedDB + flatsql.wasm)
- [x] Create `sdn-js/src/crypto.ts` (flatc-encryption.wasm wrapper)
- [x] Create `sdn-js/src/schemas.ts` (bundled SDS schemas)
- [x] Create `sdn-js/src/edge-discovery.ts`
- [x] Copy WASM files to `sdn-js/wasm/`
- [x] Verify build: `cd sdn-js && npm install && npm run build`

### P7: Embedded SDS Schemas

- [x] Create `sdn-server/internal/sds/registry.go`
- [x] Add `//go:embed schemas/sds/*.fbs` directive
- [x] Define SupportedSchemas list (EPM, PNM, OMM, CDM, etc.)
- [x] Implement schema loading from embedded FS
- [x] Wire validator to use embedded schemas
- [x] Write schema validation tests

### Phase 8: Encrypted Edge Relay Distribution

- [x] Create `scripts/build-edge-registry.ts`
- [x] Implement XChaCha20 encryption for relay list
- [x] Generate obfuscated key embedding
- [x] Compile to `sdn-js/wasm/edge-relays.wasm` (requires Emscripten)
- [x] Create `sdn-server/cmd/registry-builder/main.go`
- [x] Implement DHT monitoring for edge relay announcements
- [x] Implement auto-rebuild on relay changes
- [x] Create `scripts/cdn-deploy.sh` for CDN updates
- [x] Implement SRI hash generation
- [x] Create `sdn-js/src/edge-discovery.ts` WASM loader

### Final Verification

- [x] `cd sdn-server && go build ./cmd/spacedatanetwork` compiles
- [x] `cd sdn-server && go build -tags edge ./cmd/spacedatanetwork-edge` produces smaller binary
- [x] `cd sdn-js && npm run build` succeeds
- [x] Schema validation tests pass
- [x] Edge relay WASM implementation complete (`sdn-js/wasm/edge-relays.wasm` + SRI hash exists)
- [x] Full node can discover peers via DHT (verified with Docker testnet)
- [x] PubSub topics work for SDS message exchange (verified - GossipSub initialized, nodes connected)

---

## Timeline

| Phase | Duration | Deliverable |
|-------|----------|-------------|
| Phase 1: Fork and Strip | 4 hours | Minimal Kubo fork building |
| Phase 2: FlatBuffers | 4 hours | WASM integration complete |
| Phase 3: SQLite Storage | 8 hours | flatsql storage layer |
| Phase 4: SDS Protocol | 4 hours | Custom protocol handlers |
| Phase 5: Edge Relay | 4 hours | Edge mode binary |
| Phase 6: JavaScript | 8 hours | Browser library |
| Phase 7: Schemas | 2 hours | Full SDS support |
| Phase 8: Encrypted CDN | 8 hours | WASM-encrypted relay registry + auto-deploy |
| **Total** | **4 days** | Production ready (Claude-assisted) |

---

## Open Questions (Design Decisions - Not Implementation Tasks)

These are architectural decisions that require stakeholder input:

| Question | Status | Notes |
|----------|--------|-------|
| Should we maintain compatibility with standard IPFS nodes? | **Pending Decision** | Trade-off: compatibility vs. optimization |
| Do we need to support schema evolution/versioning? | **Pending Decision** | FlatBuffers supports forward/backward compat |
| What's the retention policy for historical data? | **Pending Decision** | Depends on storage constraints |
| Should edge relays participate in PubSub message relay? | **Pending Decision** | Currently they do relay PubSub |
| Do we need a DHT for content routing or just peer routing? | **Pending Decision** | Currently using both via Kubo |

## All tasks TODO are in .claude/tasks.md, not here
