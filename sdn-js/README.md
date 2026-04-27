# @spacedatanetwork/sdn-js

Browser and Node.js SDK for the [Space Data Network](https://github.com/DigitalArsenal/space-data-network) -- a peer-to-peer network for space data standards built on libp2p.

## Install

```bash
npm install @spacedatanetwork/sdn-js
```

The package root exports the core SDN SDK. Browser UI/runtime helpers are
published at `@spacedatanetwork/sdn-js/ui`, and marketplace purchase helpers are
published at `@spacedatanetwork/sdn-js/storefront`.

## Quick Start

```typescript
import { SDNNode, identityFromMnemonic, generateMnemonic } from '@spacedatanetwork/sdn-js';

// Generate an HD wallet identity
const mnemonic = await generateMnemonic();
const identity = await identityFromMnemonic(mnemonic);

// Create and start a P2P node
const node = await SDNNode.create({
  identity,
  enableRelayProbing: true, // load-balance across edge relays
});

console.log('Peer ID:', node.peerId);
console.log('Connected peers:', node.peers);

// Subscribe to orbit data
await node.subscribe('OMM.fbs', (data, from) => {
  console.log(`Received OMM from ${from}:`, data);
});

// Publish orbit data
await node.publish('OMM.fbs', {
  OBJECT_NAME: 'ISS (ZARYA)',
  NORAD_CAT_ID: 25544,
  EPOCH: '2024-01-15T12:00:00Z',
  MEAN_MOTION: 15.5,
  ECCENTRICITY: 0.0001,
  INCLINATION: 51.6,
});

// Clean up
await node.stop();
```

## Features

- **Peer-to-peer networking** -- libp2p with WebSocket, WebTransport, and circuit relay transports
- **HD wallet identity** -- BIP-39 mnemonic, SLIP-10 key derivation (Ed25519 signing + X25519 encryption)
- **Edge relay load balancing** -- automatic relay probing with weighted scoring (load, latency, reliability)
- **40+ space data schemas** -- FlatBuffer-native CCSDS and SDS message types
- **End-to-end encryption** -- X25519 ECDH key agreement + ChaCha20-Poly1305
- **Module delivery** -- provider-identity grant exchange over `/space-data-network/module-delivery/1.0.0`
- **Local storage** -- IndexedDB-backed record store with schema-based queries
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

## License

MIT
