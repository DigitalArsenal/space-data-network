# SDN Plugin Demo — Agent Reference Guide

This directory is the definitive reference for building, testing, and integrating
plugins with the Space Data Network. It is designed to be read by both humans and
AI agents working on SDN plugin development.

---

## Table of Contents

1. [Core Concepts](#core-concepts)
2. [FlatBuffers Binary Format](#flatbuffers-binary-format)
3. [WASM Plugin API](#wasm-plugin-api)
4. [Data Flow: Publish → PNM → Subscribe](#data-flow-publish--pnm--subscribe)
5. [Publication Log (PLOG/PLHD)](#publication-log-plogplhd)
6. [FlatSQL Storage](#flatsql-storage)
7. [Custom Protocol Registration](#custom-protocol-registration)
8. [Identity & HD Wallet Keys](#identity--hd-wallet-keys)
9. [sdn-js: Browser/Node.js as a Network Node](#sdn-js-browsernode-as-a-network-node)
10. [Periodic Tasks (Cron)](#periodic-tasks-cron)
11. [flatc-wasm: JSON ↔ FlatBuffer Streaming](#flatc-wasm-json--flatbuffer-streaming)
12. [Integration Tests](#integration-tests)
13. [File Map](#file-map)

---

## Core Concepts

SDN is a peer-to-peer network for space data that uses:

- **FlatBuffers** for zero-copy binary serialization (not JSON, not Protobuf)
- **libp2p** for peer discovery, transport, and pub/sub
- **SQLite** ("FlatSQL") for local structured storage of FlatBuffer blobs
- **GossipSub** for topic-based message broadcasting
- **Ed25519** signatures on all data for integrity verification
- **HD Wallet** (BIP-39/SLIP-10) for deterministic identity derivation

Every piece of data on SDN is a **FlatBuffer binary blob** with a 4-byte file
identifier prefix. This is not optional — JSON is only used for HTTP API
responses, never for on-wire data exchange.

---

## FlatBuffers Binary Format

### What Agents Must Understand

FlatBuffers is a binary serialization library from Google. Unlike JSON or
Protobuf, FlatBuffers are **zero-copy** — you can read fields directly from the
binary without parsing/unpacking the entire message.

### Binary Layout

```
┌─────────────────────────────────────────────────────┐
│ Offset 0-3:  root table offset (uint32 LE)          │
│ Offset 4-7:  file_identifier (4 ASCII bytes)        │  ← "$PNM", "PLOG", etc.
│              ... vtable + field data ...             │
│              ... string/vector payloads ...          │
└─────────────────────────────────────────────────────┘
```

Key properties:
- **File identifier** at bytes 4-7 tells you the message type without parsing
- All integers are **little-endian**
- Strings are UTF-8, length-prefixed
- Vectors are count-prefixed arrays
- Tables have vtables for forward/backward compatibility

### Schema Definition (.fbs files)

Schemas are defined in `.fbs` files. Example for PNM (Publish Notification Message):

```flatbuffers
// file: schemas/PNM.fbs
table PNM {
  MULTIFORMAT_ADDRESS: string;    // libp2p multiaddr of publisher
  PUBLISH_TIMESTAMP: string;      // RFC3339 timestamp
  CID: string;                    // Content ID of published data
  FILE_NAME: string;              // Human-readable name
  FILE_ID: string;                // Schema type ("OMM", "CDM", etc.)
  SIGNATURE: string;              // Ed25519 signature of CID
  TIMESTAMP_SIGNATURE: string;    // Ed25519 signature of timestamp
  SIGNATURE_TYPE: string;         // "Ed25519"
  TIMESTAMP_SIGNATURE_TYPE: string;
}
root_type PNM;
file_identifier "$PNM";
```

### Building FlatBuffers in Code

**Go (server side):**
```go
import flatbuffers "github.com/google/flatbuffers/go"
import "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"

builder := flatbuffers.NewBuilder(512)
addr := builder.CreateString("/ip4/127.0.0.1/tcp/4001")
cidStr := builder.CreateString("bafy...")
fileID := builder.CreateString("OMM")
// ... create all string fields first ...

PNM.PNMStart(builder)
PNM.PNMAddMultiformatAddress(builder, addr)
PNM.PNMAddCid(builder, cidStr)
PNM.PNMAddFileId(builder, fileID)
// ... add remaining fields ...
offset := PNM.PNMEnd(builder)
builder.FinishWithFileIdentifier(offset, []byte("$PNM"))
bytes := builder.FinishedBytes()  // This is the wire-format binary
```

**JavaScript (sdn-js):**
```javascript
import { PNM } from '@spacedatastandards/PNM';
import { Builder } from 'flatbuffers';

const builder = new Builder(512);
const addr = builder.createString('/ip4/127.0.0.1/tcp/4001');
const cid = builder.createString('bafy...');
// ...
PNM.startPNM(builder);
PNM.addMultiformatAddress(builder, addr);
PNM.addCid(builder, cid);
// ...
const offset = PNM.endPNM(builder);
PNM.finishPNMBuffer(builder, offset);
const bytes = builder.asUint8Array();  // Wire-format binary
```

### Reading FlatBuffers

**Go:**
```go
pnm := PNM.GetRootAsPNM(bytes, 0)
fmt.Println(string(pnm.Cid()))           // "bafy..."
fmt.Println(string(pnm.FileId()))         // "OMM"
```

**JavaScript:**
```javascript
import { ByteBuffer } from 'flatbuffers';
const buf = new ByteBuffer(bytes);
const pnm = PNM.getRootAsPNM(buf);
console.log(pnm.CID());       // "bafy..."
console.log(pnm.FILE_ID());   // "OMM"
```

### Identifying Message Type from Raw Bytes

```javascript
// Read file identifier from bytes 4-7
const fileId = String.fromCharCode(bytes[4], bytes[5], bytes[6], bytes[7]);
// "$PNM" → PNM, "PLOG" → Publication Log, "PLHD" → Publication Log Head
```

---

## WASM Plugin API

SDN loads standalone WASI-compatible WebAssembly modules through **WasmEdge**.
The guest ABI is the canonical `space-data-module-sdk` invoke surface.

### Required Exports

Your WASM module **must** export these functions:

| Export | Signature | Purpose |
|--------|-----------|---------|
| `plugin_alloc` | `(size: i32) → ptr: i32` | Allocate guest memory for host-written request bytes |
| `plugin_free` | `(ptr: i32, size: i32)` | Free guest allocations returned through the invoke ABI |
| `plugin_invoke_stream` | `(req_ptr: i32, req_len: i32, out_len_ptr: i32) → ptr: i32` | Invoke one manifest method using a FlatBuffer request envelope |
| `plugin_get_manifest_flatbuffer` | `() → ptr: i32` | Return the embedded FlatBuffer manifest bytes |
| `plugin_get_manifest_flatbuffer_size` | `() → size: i32` | Return the embedded manifest size |
| `_initialize` *(optional)* | `() -> void` | WASI C/C++ runtime initialization hook |
| `_start` *(optional)* | `() -> void` | Standalone command entrypoint when the guest exposes one |

### Host Functions Provided

The SDN runtime provides these imports under the `sdn_host` namespace:

| Import | Signature | Purpose |
|--------|-----------|---------|
| `call_json` | `(op_ptr: i32, op_len: i32, payload_ptr: i32, payload_len: i32) → status: i32` | Execute one sync hostcall |
| `response_len` | `() → len: i32` | Length of the last hostcall JSON response envelope |
| `read_response` | `(dst_ptr: i32, dst_len: i32) → copied: i32` | Copy the last hostcall response envelope into guest memory |
| `clear_response` | `() → status: i32` | Clear the cached hostcall response |
| `last_status_code` | `() → status: i32` | Return the previous hostcall status code |

The currently supported hostcall operations are:

- `host.runtimeTarget`
- `host.listCapabilities`
- `host.listSupportedCapabilities`
- `host.listOperations`
- `host.hasCapability`
- `clock.now`
- `clock.monotonicNow`
- `clock.nowIso`
- `random.bytes`

Binary hostcall results use the canonical JSON wrapper
`{ "__type": "bytes", "base64": "..." }`.

Plus standard `wasi_snapshot_preview1` imports.

### Memory Contract

1. Host calls `plugin_alloc(N)` to allocate N bytes in guest memory
2. Host writes input data at the returned pointer
3. Host encodes a `PluginInvokeRequest` FlatBuffer and passes it to `plugin_invoke_stream`
4. Guest returns a pointer to a `PluginInvokeResponse` FlatBuffer and writes its size to `out_len_ptr`
5. Host copies the response bytes, then calls `plugin_free(ptr, size)` on guest-owned allocations

### Plugin Lifecycle

```
1. Load standalone WASM bytes into WasmEdge
2. Call _initialize (if present) — WASI C++ runtime setup
3. Read the embedded manifest and validate the canonical exports
4. Invoke manifest methods through `plugin_invoke_stream`
5. Register libp2p protocol handlers (stream bridge)
6. Register HTTP handlers (HTTP bridge)
7. Plugin is now "running" and handles requests
```

### Example Plugin (C)

See [example-plugin/plugin.c](./example-plugin/plugin.c) for a complete
annotated example showing:
- Memory allocation/deallocation
- Initialization with identity seed
- Handling FlatBuffer requests
- Returning FlatBuffer responses
- Using host functions (time, random, logging)

---

## Data Flow: Publish → PNM → Subscribe

This is the core data exchange pattern in SDN:

```
┌──────────────┐                        ┌──────────────┐
│  Publisher    │                        │  Subscriber  │
│  (any node)  │                        │  (any node)  │
└──────┬───────┘                        └──────┬───────┘
       │                                       │
  1. Build FlatBuffer (OMM, CDM, etc.)         │
       │                                       │
  2. POST /api/v1/data/publish/{schema}        │
       │  → validates FlatBuffer               │
       │  → stores in FlatSQL (SQLite)         │
       │  → computes SHA-256 CID               │
       │  → appends PLOG entry                 │
       │                                       │
  3. Broadcast PNM via GossipSub ──────────────┤
       │  topic: /spacedatanetwork/sds/PNM.fbs │
       │                                       │
       │                                  4. Receive PNM
       │                                     │
       │                                  5. Check tip/queue config:
       │                                     - autoFetch per schema/source?
       │                                     - autoPin with TTL?
       │                                     - trusted source?
       │                                     │
       │                                  6. If autoFetch:
       │                                     fetch content by CID
       │                                     from publisher or any peer
       │                                     │
       │                                  7. Store locally in FlatSQL
       │                                     │
       │                                  8. Fire subscription handler:
       │                                     onMessage(schema, data, peerId)
```

### PNM Fields Explained

| Field | Example | Purpose |
|-------|---------|---------|
| `MULTIFORMAT_ADDRESS` | `/ip4/1.2.3.4/tcp/4001/p2p/12D3K...` | How to reach publisher |
| `CID` | `bafybeig...` | Content hash of the published data |
| `FILE_ID` | `OMM` | Which schema type was published |
| `FILE_NAME` | `ISS-2026-03-09.omm` | Human-readable filename |
| `SIGNATURE` | `(base64)` | Ed25519 signature of CID bytes |
| `PUBLISH_TIMESTAMP` | `2026-03-09T12:00:00Z` | When published |

### Tip/Queue Configuration

Subscribers configure how they handle incoming PNMs:

```yaml
# Priority resolution order (highest first):
# 1. SourceOverrides[peerID].SchemaOverrides[schema]
# 2. SourceOverrides[peerID]  (per-peer defaults)
# 3. SchemaDefaults[schema]   (per-schema defaults)
# 4. System defaults           (autoFetch=false, autoPin=false, TTL=24h)
```

---

## Publication Log (PLOG/PLHD)

PLOG/PLHD is a hash-chained log system for efficient catalog synchronization.
Instead of re-downloading entire catalogs, subscribers sync incrementally.

### How It Works

```
Publisher maintains per-schema append-only log:

  PLOG seq=1 ──→ PLOG seq=2 ──→ PLOG seq=3 ──→ ...
  (hash chain)    (hash chain)    (hash chain)

Each PLOG entry:
  - SEQUENCE: monotonic counter (starts at 1)
  - SCHEMA_TYPE: "OMM", "CDM", etc.
  - RECORD_CID: CID of the actual data record
  - PREVIOUS_ENTRY_HASH: SHA-256 chain link
  - ENTRY_HASH: SHA-256 of this entry's fields
  - SIGNATURE: Ed25519 signature of ENTRY_HASH
  - ENTITY_IDS: ["25544"] (NORAD IDs for filtering)
  - EPOCH_DAY: "2026-03-09" (time window filtering)

PLHD (Publication Log Head) — lightweight announcement:
  - HEAD_SEQUENCE: latest sequence number
  - HEAD_ENTRY_HASH: hash of latest entry
  - RECORD_COUNT: total records in log
  - Broadcast via GossipSub when log advances
```

### Sync Protocol

```
Subscriber                          Publisher
    │                                   │
    │── Compare HEAD_SEQUENCE ──────────│
    │   (subscriber's last_synced       │
    │    vs. publisher's HEAD)          │
    │                                   │
    │── MsgSyncLog (0x07) ─────────────│
    │   since_sequence=last_synced      │
    │   max_entries=500                 │
    │                                   │
    │──────── MsgSyncReply (0x08) ──────│
    │   [PLOG entries...]               │
    │                                   │
    │── Verify hash chain ──────────────│
    │── Fetch missing CIDs ─────────────│
    │── Update last_synced ─────────────│
```

### Hash Computation

```
ENTRY_HASH = SHA-256(
  SEQUENCE as 8-byte LE  ||
  SCHEMA_TYPE as UTF-8   ||
  PUBLISHER_PEER_ID as UTF-8 ||
  RECORD_CID as UTF-8   ||
  PREVIOUS_ENTRY_HASH as UTF-8 ||
  TIMESTAMP as 8-byte LE
)
```

---

## FlatSQL Storage

FlatSQL is SDN's SQLite-based storage layer that stores raw FlatBuffer blobs
alongside indexed metadata.

### Database Schema

```sql
-- Per-schema data table (one per registered schema)
CREATE TABLE sds_{schema_name} (
  cid        TEXT PRIMARY KEY,   -- SHA-256 content ID
  peer_id    TEXT,               -- Publisher's peer ID
  timestamp  INTEGER,            -- Unix timestamp
  data       BLOB,               -- Raw FlatBuffer bytes
  signature  BLOB                -- Ed25519 signature
);

-- Fast lookup index for API queries
CREATE TABLE sdn_record_index (
  schema_name      TEXT NOT NULL,
  cid              TEXT NOT NULL,
  norad_cat_id     INTEGER,      -- For orbital data filtering
  entity_id        TEXT,
  epoch_unix       INTEGER,
  epoch_day        TEXT,          -- "YYYY-MM-DD"
  source_timestamp INTEGER NOT NULL,
  PRIMARY KEY (schema_name, cid)
);

-- Publication log index
CREATE TABLE sdn_log_index (
  publisher_peer_id TEXT NOT NULL,
  schema_type       TEXT NOT NULL,
  sequence          INTEGER NOT NULL,
  entry_hash        TEXT NOT NULL,
  record_cid        TEXT NOT NULL,
  plg_cid           TEXT NOT NULL,
  epoch_day         TEXT,
  timestamp         INTEGER NOT NULL,
  PRIMARY KEY (publisher_peer_id, schema_type, sequence)
);
```

### Data Lifecycle

```
Incoming FlatBuffer bytes
    │
    ▼
Validate file_identifier (bytes 4-7) against schema registry
    │
    ▼
Compute CID = SHA-256(bytes) → hex string
    │
    ▼
INSERT into sds_{schema} (cid, peer_id, timestamp, data, signature)
    │
    ▼
Extract index fields (NORAD_CAT_ID, epoch, entity_id) from FlatBuffer
    │
    ▼
INSERT into sdn_record_index (schema_name, cid, ...)
    │
    ▼
Append PLOG entry via logservice
```

---

## Custom Protocol Registration

Plugins can register custom libp2p protocol handlers for direct peer-to-peer
communication beyond the standard SDS exchange protocol.

### Built-in Protocols

| Protocol ID | Purpose |
|-------------|---------|
| `/spacedatanetwork/sds-exchange/1.0.0` | Standard SDS data push/pull/query |
| `/space-data-network/id-exchange/1.0.0` | Legacy identity exchange |
| `/spacedatanetwork/epm-exchange/1.0.0` | Entity Profile Manifest exchange |
| `/space-data-network/module-delivery/1.0.0` | SDN module delivery |

### SDS Exchange Message Types

| Byte | Type | Purpose |
|------|------|---------|
| `0x01` | MsgRequestData | Request data by CID |
| `0x02` | MsgPushData | Push data to peer |
| `0x03` | MsgQuery | Query for data |
| `0x04` | MsgResponse | Response to query |
| `0x05` | MsgAck | Acknowledgment |
| `0x06` | MsgNack | Negative acknowledgment |
| `0x07` | MsgSyncLog | Request PLOG entries since sequence |
| `0x08` | MsgSyncReply | Response with PLOG entries |

### Registering a Custom Protocol (Go Plugin)

```go
// In your plugin's Start() method:
func (p *MyPlugin) Start(ctx context.Context, rc plugins.RuntimeContext) error {
    // Register a custom stream handler
    rc.Host.SetStreamHandler(
        protocol.ID("/my-plugin/data-feed/1.0.0"),
        p.handleStream,
    )
    return nil
}

func (p *MyPlugin) handleStream(s network.Stream) {
    defer s.Close()
    // Read request (FlatBuffer binary)
    // Process and write response (FlatBuffer binary)
}
```

### Registering a Custom Protocol (WASM Plugin via Stream Bridge)

WASM plugins use the stream bridge adapter. The host runtime translates
libp2p stream reads/writes into WASM memory operations:

```
libp2p stream ←→ streambridge ←→ WASM memory (malloc/free)
                                    ↕
                              plugin_handle_request()
```

---

## Identity & HD Wallet Keys

SDN uses BIP-39 mnemonics with SLIP-10 HD key derivation. Every node and
every user has the **same type of identity** — there is no distinction between
"node keys" and "user keys" at the cryptographic level.

### Derivation Paths

```
BIP-39 Mnemonic → PBKDF2 → 512-bit Seed → SLIP-10 Master Key
    │
    ├── m/44'/0'/account'           → secp256k1 key (xpub identity)
    │                                  xpub = BIP-32 extended public key
    │                                  Used as network identity (TOFU binding)
    │
    ├── m/44'/0'/account'/0'/0'     → Ed25519 Signing Key
    │                                  Also used as libp2p PeerID
    │                                  Signs all data, challenges, PLOG entries
    │
    └── m/44'/0'/account'/1'/0'     → X25519 Encryption Key
                                       ECIES: X25519 + ChaCha20-Poly1305
                                       Per-message or session key encryption
```

### Key Properties

- **xpub** is the master identity. Anyone with the xpub can derive your public
  signing and encryption keys.
- **PeerID** is derived from the Ed25519 public key (libp2p standard).
- **Multi-account**: One mnemonic can manage multiple SDN identities by varying
  the `account'` segment. Useful for: operator, sensor, analytics, etc.
- **TOFU binding**: The Ed25519 signing key is bound to the xpub on first login.
  Subsequent logins verify the binding.

### "User Accounts" = Keys

There is no separate concept of "user accounts" in SDN. A user IS their HD
wallet key. When you log in to the SDN web UI with a mnemonic, you derive the
same Ed25519/X25519 keys that a server node would derive. The browser becomes a
network node with those keys via sdn-js.

---

## sdn-js: Browser/Node.js as a Network Node

The `sdn-js` SDK turns any browser or Node.js process into a full SDN peer:

```javascript
import { SDNNode, identityFromMnemonic } from '@spacedatanetwork/sdn-js';

// Derive identity from mnemonic (same as server nodes)
const identity = await identityFromMnemonic('abandon abandon ...');

// Create a P2P node (libp2p under the hood)
const node = await SDNNode.create({
  identity,
  edgeRelays: ['wss://relay.spaceaware.io/...'],
  enableStorage: true,  // IndexedDB local storage
});

// Subscribe to orbital data via GossipSub
await node.subscribe('OMM.fbs', (data, peerId) => {
  console.log('Received OMM from', peerId);
});

// Publish data (broadcasts PNM to network)
await node.publish('OMM.fbs', flatBufferBytes);

// Also available: REST client for querying remote servers
import { SDNClient } from '@spacedatanetwork/sdn-js';
const client = await SDNClient.resolve('spaceaware.io');
const records = await client.query({ schema: 'OMM.fbs', day: '2026-03-09' });
```

### Browser Node Architecture

```
┌──────────────────────────────────────────────────┐
│                    Browser Tab                    │
│  ┌──────────────────────────────────────────┐    │
│  │  sdn-js (SDNNode)                        │    │
│  │  ├── libp2p (WebSocket + WebTransport)   │    │
│  │  ├── GossipSub (PubSub)                  │    │
│  │  ├── Kademlia DHT (client mode)          │    │
│  │  ├── HD Wallet (WASM — hd-wallet-wasm)   │    │
│  │  ├── IndexedDB (FlatBuffer blob storage) │    │
│  │  └── SubscriptionManager (filter/route)  │    │
│  └──────────────────────────────────────────┘    │
│       │                                          │
│       │ WebSocket / WebTransport                 │
│       ▼                                          │
│  ┌──────────────┐                                │
│  │ Edge Relay   │ (Circuit Relay v2)             │
│  │ (Go server)  │                                │
│  └──────┬───────┘                                │
│         │                                        │
│         ▼                                        │
│  ┌──────────────┐                                │
│  │ Full Nodes   │ (Go servers, public IP)        │
│  │ DHT + PubSub │                                │
│  └──────────────┘                                │
└──────────────────────────────────────────────────┘
```

### Encryption Between Nodes

SDN supports multiple encryption modes for data delivery:

| Mode | ID | Description |
|------|----|-------------|
| None | 0 | Plaintext (public data) |
| ECIES | 1 | Per-message X25519 + ChaCha20-Poly1305 |
| SessionKey | 2 | AES-256-GCM with pre-shared key |
| Hybrid | 3 | Header unencrypted, payload encrypted |

Encrypted delivery uses the recipient's X25519 public key (derived from their
xpub) to encrypt data. Only the intended recipient can decrypt.

### Streaming Support

Subscriptions support three delivery modes:

| Mode | ID | Behavior |
|------|----|----------|
| Single | 0 | One message per request |
| Streaming | 1 | Continuous real-time delivery |
| Batch | 2 | Periodic batched delivery |

---

## Periodic Tasks (Cron)

WASM plugins can declare **cron-eligible methods** in their metadata JSON. The
SDN host controls *whether* and *how often* each method runs — via the server
config file or the web UI. The plugin never starts its own goroutines.

### Metadata Declaration

In the JSON returned by `plugin_get_metadata`, include a `"cron"` array:

```json
{
  "id": "example-sensor-plugin",
  "version": "1.0.0",
  "cron": [
    {
      "method": "collect-telemetry",
      "description": "Sample sensor readings and publish as FlatBuffer",
      "default_interval": "30s",
      "input": "none",
      "output": "json"
    },
    {
      "method": "cleanup-cache",
      "description": "Prune expired entries from the local data cache",
      "default_interval": "5m",
      "input": "none",
      "output": "none"
    },
    {
      "method": "sync-catalog",
      "description": "Fetch latest catalog delta from upstream peer",
      "default_interval": "1h",
      "input": "json",
      "output": "json"
    }
  ]
}
```

### CronMethodSpec Fields

| Field | Type | Description |
|-------|------|-------------|
| `method` | string | Identifier passed to `plugin_cron` (e.g. `"collect-telemetry"`) |
| `description` | string | Human-readable label for the web UI |
| `default_interval` | string | Suggested schedule (`"30s"`, `"5m"`, `"1h"`) — overridable by server config |
| `input` | string | `"none"`, `"json"`, or `"flatbuffer"` — what the host passes in |
| `output` | string | `"none"`, `"json"`, or `"flatbuffer"` — what the plugin returns |

### WASM Export

```c
// Optional WASM export — only needed if the plugin declares cron methods
__attribute__((export_name("plugin_cron")))
int32_t plugin_cron(
    const char* method_ptr, int32_t method_len,  // method name
    uint8_t* in_ptr, int32_t in_len,             // input (type per spec)
    uint8_t* out_ptr, int32_t out_cap            // output buffer
);
// Returns: bytes written to out_ptr, 0 for no output, negative on error
```

### Server-Side Configuration

The server config file (or web UI) controls which methods are enabled and
their actual intervals. The plugin's `default_interval` is only a suggestion:

```yaml
plugins:
  cron:
    example-sensor-plugin:
      collect-telemetry:
        enabled: true
        interval: "1m"       # Override plugin's default "30s"
      cleanup-cache:
        enabled: true
        interval: ""         # Use plugin default ("5m")
      sync-catalog:
        enabled: false       # Disabled by operator
```

### Lifecycle

```text
1. Host loads WASM module, calls plugin_init()
2. Host calls plugin_get_metadata() → parses "cron" array
3. Host merges cron specs with server config/UI settings
4. For each enabled method: start a ticker goroutine
5. On each tick: host calls plugin_cron(method, input, out)
6. On shutdown: cancel all ticker goroutines, then plugin.Close()
```

---

## flatc-wasm: JSON ↔ FlatBuffer Streaming

The `flatc-wasm` module provides WASM-powered conversion between JSON and
FlatBuffer binary format. This enables plugins and API handlers to accept
JSON input, convert to FlatBuffers for storage, and convert back for queries.

### Core API

```go
// Register a schema once at startup
schemaID, err := flatc.AddSchema(ctx, "OMM", schemaBytes)

// Convert JSON → FlatBuffer binary
binary, err := flatc.JSONToBinary(ctx, schemaID, jsonData)

// Convert FlatBuffer binary → JSON
json, err := flatc.BinaryToJSON(ctx, schemaID, binaryData)
```

### StreamConverter

The `StreamConverter` wraps `flatc-wasm` for streaming conversion of
newline-delimited JSON records:

```go
import "github.com/spacedatanetwork/sdn-server/internal/wasm"

converter := wasm.NewStreamConverter(flatcModule, schemaID)

// JSON stream → FlatBuffer records
records, errs := converter.JSONStreamToFlatBuffers(ctx, reader)
for _, rec := range records {
    store.Put(rec.Binary)     // Store FlatBuffer binary
    log.Debug(rec.SourceJSON) // Original JSON kept for debugging
}

// FlatBuffer records → JSON stream
binaryRecords := [][]byte{rec1, rec2, rec3}
written, errs := converter.FlatBuffersToJSONStream(ctx, binaryRecords, writer)
```

### Batch Wire Format

For sending multiple FlatBuffer records over a libp2p stream, use the
length-prefixed batch format:

```go
// Serialize batch for wire transfer
wire := wasm.FlatBufferBatchToWire(records)
stream.Write(wire)

// Deserialize on receiving end
records, err := wasm.WireToFlatBufferBatch(wireData)
```

Wire format:
```
[record_count: uint32 LE]
[record_0_len: uint32 LE][record_0_data: N bytes]
[record_1_len: uint32 LE][record_1_data: N bytes]
...
```

### Data Flow with flatc-wasm

```
                    ┌──────────────┐
  JSON records ───→ │ JSONToBinary │ ───→ FlatBuffer records ───→ Store/Publish
  (from API/stream) │  (per record)│      (binary, zero-copy)    (FlatSQL/PNM)
                    └──────────────┘

                    ┌──────────────┐
  FlatBuffer data ─→│ BinaryToJSON │ ───→ JSON records ───→ API Response/Stream
  (from store/p2p)  │  (per record)│      (human-readable)
                    └──────────────┘
```

### Key Points

- **Thread-safe**: `FlatcModule` is mutex-protected; call from any goroutine
- **Memory managed**: WASM malloc/free handled internally — pass `[]byte` in/out
- **Schema registration**: Call `AddSchema()` once; use the `schemaID` handle after
- **Best-effort streaming**: `JSONStreamToFlatBuffers` continues past per-record
  errors, returning both successful records and error list

See `sdn-server/internal/wasm/stream_converter.go` for the full implementation.

---

## Integration Tests

The `tests/` directory contains a full integration test suite. See
[tests/README.md](./tests/README.md) for details.

### Quick Run

```bash
# From plugin-demo/tests/
npm install
npm test

# Or from repo root via CI script:
./scripts/ci-local.sh plugin-demo
```

### What the Tests Cover

1. **Server startup** — Start a local SDN node on ephemeral ports
2. **Authentication** — Ed25519 challenge-response auth flow
3. **Publish** — POST FlatBuffer data, verify CID and storage
4. **PNM notification** — Verify PNM broadcast via GossipSub
5. **PLOG/PLHD** — Verify publication log chain integrity
6. **Custom protocol** — Register and communicate via custom libp2p protocol
7. **sdn-js client** — Node.js sdn-js client subscribes and receives data

---

## File Map

```
plugin-demo/
├── README.md                      ← You are here
├── ARCHITECTURE.md                ← Detailed data flow diagrams
├── example-plugin/
│   ├── README.md                  ← Plugin development guide
│   ├── plugin.c                   ← Annotated C plugin source
│   └── Makefile                   ← Build with wasi-sdk
├── tests/
│   ├── README.md                  ← Test documentation
│   ├── package.json               ← Test dependencies
│   ├── helpers/
│   │   └── test-server.mjs        ← SDN test server launcher
│   ├── integration.test.mjs       ← Full integration test suite
│   └── run.sh                     ← Test runner script
└── schemas/
    └── ExampleSensorData.fbs      ← Example custom schema
```

### Related Directories

| Path | Description |
|------|-------------|
| `space-data-module-sdk` | External SDK package/repo with the canonical host ABI, runtime surfaces, and manifest helpers |
| `sdn-server/plugins/manager.go` | Plugin manager (CronProvider, scheduling) |
| `sdn-server/internal/wasiplugin/` | WASI runtime (Wazero) |
| `sdn-server/internal/wasm/flatc.go` | flatc-wasm module (JSONToBinary, BinaryToJSON) |
| `sdn-server/internal/wasm/stream_converter.go` | Streaming JSON↔FlatBuffer converter |
| `sdn-js/src/` | JavaScript SDK source |
| `schemas/sds/` | Space Data Standards schemas |
| `sdn-server/internal/sds/schemas/` | Server schema copies (PNM, PLOG, PLHD) |
