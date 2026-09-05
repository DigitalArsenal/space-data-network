# @spacedatanetwork/sdn-js

Browser and Node.js client for the [Space Data Network](https://github.com/DigitalArsenal/space-data-network). SDN adds standardized FlatBuffer records, identity, trust and signed WASM modules to IPFS and libp2p. The Go node wraps Kubo; this client integrates Helia.

## Install

```bash
npm install @spacedatanetwork/sdn-js
```

The HTTP-only `@spacedatanetwork/sdn-js/http` entry uses fetch and FlatBuffer
framing without loading a wallet, peer node, compiler or storage engine.
The package root exports the core SDN SDK. Browser UI/runtime helpers are
published at `@spacedatanetwork/sdn-js/ui`, and marketplace purchase helpers are
published at `@spacedatanetwork/sdn-js/storefront`.

## Quick Start

Start with a read from a running SDN node that serves OMM records. Use its HTTP
address below; `http://127.0.0.1:5001` is the local example. The node's homepage
lets you inspect its datasets and providers before writing client code.

```typescript
import { HttpTransport } from '@spacedatanetwork/sdn-js/http';

const transport = new HttpTransport('http://127.0.0.1:5001');
const result = await transport.queryData({ schema: 'OMM', profile: 'nearest', limit: 10 });
for (const record of result.frames()) {
  // Each record is a canonical FlatBuffer, ready for its SDS decoder.
  console.log('Record bytes:', record.byteLength);
}
```

A node with no matching records returns an empty stream. A not-ready or denied
request rejects with an HTTP error; it is not an empty dataset. Reading via HTTP
does not require starting a browser peer or generating an identity.

For named publication streams, discover channels by `standardCode` and inspect
their synchronization state. Handle a node with no advertised channel explicitly:

```typescript
import { SDNClient } from '@spacedatanetwork/sdn-js';

const client = SDNClient.fromUrl('http://127.0.0.1:5001');
const [channel] = await client.channels.list({ standardCode: 'OMM' });
if (channel) console.log(await client.channels.monitor(channel.channelId));
else console.log('This node has not advertised an OMM channel.');
```

Publishing requires an authorized provider session and actual encoded records.
This example function targets the `spaceaware-OMM` channel; choose a channel your
provider controls before calling it:

```typescript
async function publishRecords(flatbufferStreamBytes: Uint8Array) {
  if (flatbufferStreamBytes.byteLength === 0) throw new Error('No records to publish');
  await client.channels.publish('spaceaware-OMM', flatbufferStreamBytes);
}
```

To participate directly as a peer:

```typescript
import { SDNNode, initHDWallet, identityFromMnemonic, generateMnemonic } from '@spacedatanetwork/sdn-js';

await initHDWallet();
const mnemonic = await generateMnemonic();
const identity = await identityFromMnemonic(mnemonic);

// create() starts the node. Keep the mnemonic private and back it up securely.
const node = await SDNNode.create({
  identity,
  enableRelayProbing: true,
});
console.log('Peer ID:', node.peerId);
console.log('Connected peers:', node.peers);
await node.stop();
```

### Bulk records and conditional requests

`RemoteEpochStreamClient` feeds original response bytes into a FlatSQL engine
store. Supply an already opened `FlatSQLEngineRecordStore` with the required SDS
schemas. Its optional third constructor argument sets `maxEntries` (default 16)
and `maxBytes` (default 32 MiB of retained payloads); either zero disables caching.

Every request still contacts the server. A 304 returns the exact retained
representation, even when local records or source partitions have changed.
Eviction removes the validator with the bytes, and `no-store` is respected.
`fromCache` reports this replay; `fromLocalStore` is its deprecated compatibility
alias. Call `clearCache()` when changing sessions or releasing the client.

ETags are HTTP validators. They do not replace CID, signature or scientific
validation. CDN delivery must preserve those checks and must not share private
or authenticated responses between users.

### Stream large IPFS objects

```typescript
import { createHeliaSDNNode, streamCIDFromHelia } from '@spacedatanetwork/sdn-js';

const node = await createHeliaSDNNode();
const controller = new AbortController();
try {
  for await (const chunk of streamCIDFromHelia(node.helia, cid, { signal: controller.signal })) {
    await consumeChunk(chunk); // your storage or record-stream consumer
  }
} finally {
  await node.stop();
}
```

The iterator retrieves data on demand and closes its provider session on early
return, cancellation or failure. Copy a chunk if retaining it beyond the next
iteration. An optional `maxBytes` sets a total transfer limit.
`fetchCIDBytesFromHelia` buffers a whole object with a default 128 MiB payload
limit; concatenation also needs memory for the final result. Use the iterator
for datasets, or pass an explicit larger limit for a known artifact.

Helia's legacy write adapter asks producers to await `onDrain()` when `send()`
returns false. It refuses further writes at its 8 MiB/1,024-chunk hard limit,
propagates sink errors, and releases accepted queued bytes on abort.

## Features

- **Peer-to-peer networking** -- libp2p with WebSocket, WebTransport, and circuit relay transports
- **HD wallet identity** -- BIP-39 mnemonic, SLIP-10 key derivation (Ed25519 signing + X25519 encryption)
- **Edge relay load balancing** -- automatic relay probing with weighted scoring (load, latency, reliability)
- **Space data standards** -- FlatBuffer-native CCSDS and SDS message types
- **End-to-end encryption** -- X25519 ECDH key agreement + ChaCha20-Poly1305
- **Module delivery** -- provider-identity grant exchange over `/space-data-network/module-delivery/1.0.0`
- **Local storage** -- FlatSQL WASM over canonical FlatBuffer records, with browser persistence adapters
- **Data marketplace** -- storefront client for listing, purchasing, and reviewing space data
- **EPM resolution** -- Entity Profile Manifest discovery and key exchange

## Browser UI Runtime

Use the explicit UI subpath when embedding the SDN browser UI/runtime helpers in
your own app.

```typescript
import {
  loadMarketplaceListingsFromServer,
  unwrapGrantContentKey,
  decryptEncryptedModuleBundle,
  loadDecryptedModule,
  invokeLoadedModule,
  ObservedPeerIndex,
  SDNUIEventBus,
  mountWalletUI,
} from '@spacedatanetwork/sdn-js/ui';
```

## Configuration

```typescript
interface SDNConfig {
  edgeRelays?: string[];          // Custom relay multiaddrs
  bootstrapPeers?: string[];      // Additional bootstrap peers
  includeIPFSBootstrap?: boolean; // Include public IPFS bootstrap nodes
  identity?: DerivedIdentity;     // HD wallet identity (secp256k1 PeerID + Ed25519 signing)
  privateKey?: Uint8Array;        // Ed25519 signing key (32-byte seed)
  enableStorage?: boolean;        // Enable local IndexedDB storage (default: true)
  storeName?: string;             // IndexedDB store name (default: 'sdn-store')
  enableRelayProbing?: boolean;   // Enable relay load probing (default: true)
  relayProbeIntervalMs?: number;  // Probe interval in ms (default: 30000)
  skipSignatureVerification?: boolean; // Skip signature checks (not recommended)
}
```

## API

### SDNNode

The main P2P node class. Create with `SDNNode.create()`.

```typescript
const node = await SDNNode.create(config?, events?);

// Properties
node.peerId         // Peer ID string
node.peers          // Connected peer IDs
node.canSign        // Whether signing is available

// Pub/Sub
await node.publish(schema, data)        // Publish to a schema topic
await node.subscribe(schema, handler?)  // Subscribe to a schema topic
await node.unsubscribe(schema)          // Unsubscribe

// Storage
await node.query(schema, filter?)       // Query local records

// Dialing
await node.dial(multiaddr)             // Dial a peer directly
await node.dialProtocolThroughRelay(relayAddr, peerId, protocol, payload)

// Relay discovery
node.getDiscovery()                    // Get EdgeDiscovery instance

// Lifecycle
await node.stop()                      // Stop the node
```

### HD Wallet & Crypto

BIP-39 mnemonic generation, SLIP-10 key derivation, Ed25519 signing, and X25519 encryption powered by [hd-wallet-wasm](https://www.npmjs.com/package/hd-wallet-wasm).

```typescript
import {
  initHDWallet,
  generateMnemonic,
  validateMnemonic,
  identityFromMnemonic,
  deriveIdentity,
  sign,
  verify,
  encrypt,
  decrypt,
  x25519ECDH,
} from '@spacedatanetwork/sdn-js';

// Initialize the WASM module (required before any crypto ops)
await initHDWallet();

// Generate and validate mnemonics
const mnemonic = await generateMnemonic();       // 24-word BIP-39
const valid = await validateMnemonic(mnemonic);

// Derive a full SDN identity (signing + encryption + PeerID keys)
const identity = await identityFromMnemonic(mnemonic);
// identity.signingKey     — Ed25519 (m/44'/0'/0'/0'/0')
// identity.encryptionKey  — X25519 (m/44'/0'/0'/1'/0')
// identity.identityKey    — secp256k1 for PeerID

// Sign and verify messages
const message = new TextEncoder().encode('hello');
const sig = await sign(identity.signingKey.privateKey, message);
const ok = await verify(identity.signingKey.publicKey, message, sig);

// Encrypt and decrypt
const ciphertext = await encrypt(recipientPubKey, message);
const plaintext = await decrypt(myPrivateKey, ciphertext);
```

### Edge Discovery & Load Balancing

Automatic relay discovery with load-aware selection. Clients probe relay `/api/relay/status` endpoints and score by connection load (50%), latency (30%), and failure history (20%).

```typescript
import { EdgeDiscovery, multiaddrToStatusURL } from '@spacedatanetwork/sdn-js';

// Create discovery instance
const discovery = new EdgeDiscovery([
  '/dns4/relay1.example.com/tcp/443/wss/p2p/12D3KooW...',
  '/dns4/relay2.example.com/tcp/443/wss/p2p/12D3KooW...',
]);

// Probe all relays
const results = await discovery.probeAllRelays();
for (const [addr, result] of results) {
  console.log(addr, result.status?.load, result.latencyMs);
}

// Get best relays (sorted by composite score)
const best = discovery.getBestRelays(3);

// Start background probing (every 30s)
discovery.startProbing(30_000);

// Get circuit relay address for a target peer
const circuitAddr = discovery.getCircuitAddress('target-peer-id');

// Convert multiaddr to HTTP URL
multiaddrToStatusURL('/dns4/example.com/tcp/443/wss/p2p/...');
// → 'https://example.com/api/relay/status'
```

### EPM Resolution

Resolve Entity Profile Manifests for key exchange and identity verification.

```typescript
import { createEPMResolver, KeyType } from '@spacedatanetwork/sdn-js';

const resolver = createEPMResolver({ gateway: 'https://ipfs.io' });
const epm = await resolver.resolve(xpub);

// Extract keys
const signingKey = epm.getKey(KeyType.SIGNING);
const encryptionKey = epm.getKey(KeyType.ENCRYPTION);
```

### Module Delivery

Requester-side module delivery stays on libp2p plus Helia. The public path
does not bootstrap through legacy discovery or browser broker endpoints.

```typescript
import {
  MODULE_DELIVERY_PROTOCOL_ID,
  SDNNode,
  fetchEncryptedModuleBundle,
  requestEncryptedModuleBundle,
  requestModuleGrant,
} from '@spacedatanetwork/sdn-js';

const result = await node.requestEncryptedModuleBundle({
  serverDescriptor: {
    publicKey: '02...provider-compressed-secp256k1-key...',
    cid: 'bafy...provider-epm',
  },
  moduleId: 'com.example.analytics',
  moduleVersion: '1.2.3',
  requesterDomain: globalThis.location.origin,
  requestedTimeoutMs: 300_000,
});

console.log(result.grant.bundleDescriptor.cid);
console.log(result.encryptedBundleBytes.length);
```

`SDNNode.requestEncryptedModuleBundle(...)` is the high-level requester path.
The lower-level `requestModuleGrant(...)`, `fetchEncryptedModuleBundle(...)`,
`requestEncryptedModuleBundle(...)`, and `MODULE_DELIVERY_PROTOCOL_ID` exports
are also public for apps that provide their own transport implementation.

### Storefront

Client for the SDN data marketplace.

```typescript
import {
  AccessType,
  GrantStatus,
  PaymentMethod,
  PurchaseStatus,
  createStorefrontClient,
} from '@spacedatanetwork/sdn-js/storefront';

const client = createStorefrontClient({
  apiBaseUrl: 'https://spaceaware.io',
  peerId: '12D3KooWRequester...',
});

// Browse listings
const results = await client.searchListings({
  searchText: 'conjunction',
  dataTypes: ['CDM'],
  accessTypes: [AccessType.Subscription],
});

// Purchase data access
const purchase = await client.createPurchase({
  listingId: results.listings[0].listingId,
  tierName: 'Pro',
  paymentMethod: PaymentMethod.SDNCredits,
});
```

### Purchase, Deliver, Decrypt, Run

The browser end-to-end path uses only public package exports:

```typescript
import { SDNNode, identityFromMnemonic, initHDWallet } from '@spacedatanetwork/sdn-js';
import {
  decryptEncryptedModuleBundle,
  invokeLoadedModule,
  loadDecryptedModule,
  loadMarketplaceListingsFromServer,
  unwrapGrantContentKey,
} from '@spacedatanetwork/sdn-js/ui';
import { PaymentMethod, createStorefrontClient } from '@spacedatanetwork/sdn-js/storefront';

await initHDWallet();
const identity = await identityFromMnemonic(mnemonic);
const node = await SDNNode.create({ identity, enableRelayProbing: true });

const [listing] = await loadMarketplaceListingsFromServer('https://spaceaware.io');
const storefront = createStorefrontClient({
  apiBaseUrl: 'https://spaceaware.io',
  peerId: identity.peerId,
  encryptionPubkey: identity.encryptionKey.publicKey,
  keyAlgorithm: 'x25519',
});

const purchase = await storefront.createPurchase({
  listingId: listing.pluginId,
  tierName: 'default',
  paymentMethod: PaymentMethod.SDNCredits,
  encryptionPubkey: identity.encryptionKey.publicKey,
  keyAlgorithm: 'x25519',
  preferredDeliveryMethod: 'IPFSPin',
});
await storefront.payWithCredits(purchase.requestId);

const delivery = await node.requestEncryptedModuleBundle({
  serverDescriptor: {
    publicKey: providerPublicKey,
    relayAddresses: providerRelayAddresses,
  },
  moduleId: listing.pluginId,
  moduleVersion: listing.version,
  requesterDomain: globalThis.location.origin,
  requestedTimeoutMs: 300_000,
});

const contentKey = await unwrapGrantContentKey(
  delivery.grant.wrappedContentKey,
  identity.encryptionKey.privateKey,
);
const wasmBytes = await decryptEncryptedModuleBundle(
  delivery.encryptedBundleBytes,
  contentKey,
);
const harness = await loadDecryptedModule(wasmBytes);
const result = await invokeLoadedModule(harness, {
  methodId: 'invoke',
  inputs: [],
});
```

A fuller documented example lives at
[`examples/purchase-encrypted-wasm-delivery.ts`](./examples/purchase-encrypted-wasm-delivery.ts).

### Subscriptions

Advanced subscription management with filtering and routing.

```typescript
import { SubscriptionManager, StreamingMode } from '@spacedatanetwork/sdn-js';

const manager = new SubscriptionManager();

manager.subscribe({
  schema: 'CDM.fbs',
  mode: StreamingMode.REALTIME,
  filters: [{ field: 'MISS_DISTANCE', op: 'lt', value: 1000 }],
}, (event) => {
  console.log('Close approach:', event.data);
});
```

### Schemas

40+ FlatBuffer-based space data schemas following CCSDS standards.

```typescript
import { SUPPORTED_SCHEMAS, SDS_SCHEMAS } from '@spacedatanetwork/sdn-js';

// All supported schema names
console.log(SUPPORTED_SCHEMAS);
// ['ACL.fbs', 'ATM.fbs', 'BOV.fbs', 'CAT.fbs', 'CDM.fbs', ...]
```

Key schemas include:
- **OMM** -- Orbit Mean-Elements Message (TLE-equivalent)
- **OEM** -- Orbit Ephemeris Message (time-series position/velocity)
- **CDM** -- Conjunction Data Message (collision warnings)
- **EPM** -- Entity Profile Manifest (identity/contact)
- **STF** -- Storefront Listing (marketplace)

## Browser vs Node.js

The SDK is designed for browsers but works in Node.js 18+ with the following considerations:

| Feature | Browser | Node.js |
|---------|---------|---------|
| WebSocket transport | Yes | Yes |
| WebTransport | Yes | No |
| Circuit relay | Yes | Yes |
| IndexedDB storage | Yes | Requires polyfill |
| HD wallet WASM | Yes (auto-loaded) | Yes (auto-loaded) |
| Relay probing (fetch) | Yes | Yes (Node 18+) |

## Environment Variables

```bash
# Override default edge relays (comma-separated multiaddrs)
SDN_EDGE_RELAYS=/dns4/relay1.example.com/tcp/443/wss/p2p/...,/dns4/relay2.example.com/tcp/443/wss/p2p/...
```

In the browser, set `window.__SDN_EDGE_RELAYS__` as a string array before importing.

## Testing

### Stress tests

Stress tests live in `src/stress/*.stress.test.ts` and are excluded from the normal `npm test` run. They are fully offline and deterministic: streaming and backpressure are exercised against the real FlatSQL sync chunk codec and `SubscriptionManager` over an in-memory loopback transport.

```bash
# Build first (the throughput harness tests import dist/ui/index.mjs)
npm run build

npx vitest run --config vitest.stress.config.mts

# Optionally scale the in-memory streaming volume (default: 64 MB)
STRESS_TARGET_MB=256 npx vitest run --config vitest.stress.config.mts
```

For live-network throughput measurement against a running SDN node, use `npm run measure:flatsql-sync` or `npm run chaos:local` instead.

### Live relay integration test

`src/spaceaware-relay.integration.test.ts` dials a real relay and is skipped unless `SDN_RUN_RELAY_TEST=1` **and** both fixture variables are set:

```bash
SDN_RUN_RELAY_TEST=1 \
SDN_SPACEAWARE_PROVIDER_PUBLIC_KEY=<hex-encoded provider public key> \
SDN_SPACEAWARE_RELAY_CANDIDATES=/dns4/relay.example.com/tcp/443/wss,/dns4/relay2.example.com/tcp/443/wss \
npm run test:relay
```

- `SDN_SPACEAWARE_PROVIDER_PUBLIC_KEY`: hex-encoded provider public key used to derive the relay's PeerID.
- `SDN_SPACEAWARE_RELAY_CANDIDATES`: comma-separated relay multiaddrs (a `/p2p/<peerId>` suffix is appended automatically when missing).

If either fixture is missing the suite stays skipped even with `SDN_RUN_RELAY_TEST=1`, so it never runs accidentally in offline CI.

## License

MIT
