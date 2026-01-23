# Security & Encryption

This guide covers the security architecture of Space Data Network, including transport encryption, peer authentication, data integrity, and best practices for secure operation.

## Overview of SDN Security Model

SDN implements a comprehensive security model designed for a hostile network environment where trust must be established cryptographically, not assumed.

### Defense in Depth

SDN employs multiple layers of security that work together:

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Application Layer                             │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │           Schema Validation (FlatBuffer Schemas)             │   │
│  │        Ensures data conforms to expected structure           │   │
│  └─────────────────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────────────────┤
│                        Message Layer                                 │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │              Ed25519 Digital Signatures                      │   │
│  │     Verifies authenticity and integrity of every message     │   │
│  └─────────────────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────────────────┤
│                        Transport Layer                               │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │           Noise Protocol (Noise_XX Handshake)                │   │
│  │      Encrypted, authenticated peer-to-peer connections       │   │
│  └─────────────────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────────────────┤
│                        Identity Layer                                │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │           Ed25519 Key Pairs (Peer Identity)                  │   │
│  │        Unique, verifiable identity for every node            │   │
│  └─────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

### Zero Trust Architecture

SDN follows zero trust principles:

| Principle | Implementation |
|-----------|----------------|
| **Never trust, always verify** | All messages require valid Ed25519 signatures |
| **Assume breach** | Transport encryption protects against eavesdropping |
| **Verify explicitly** | Peer IDs cryptographically derived from public keys |
| **Least privilege** | Nodes only access data from subscribed topics |

### Cryptographic Foundations

SDN relies on well-established cryptographic primitives:

| Primitive | Algorithm | Purpose |
|-----------|-----------|---------|
| **Asymmetric Signing** | Ed25519 | Message signatures, peer identity |
| **Key Exchange** | X25519 (Curve25519) | Noise protocol handshake |
| **Symmetric Encryption** | ChaCha20-Poly1305 | Transport encryption |
| **Hashing** | SHA-256 | Content addressing, peer ID derivation |
| **Optional Encryption** | AES-256-GCM | Application-layer data encryption |

## Transport Encryption: Noise Protocol

All peer-to-peer connections in SDN are encrypted using the [Noise Protocol Framework](https://noiseprotocol.org/), a modern, flexible framework for building cryptographic protocols.

### What is Noise Protocol Framework?

Noise is a framework for constructing cryptographic protocols. Unlike monolithic protocols like TLS, Noise provides a set of building blocks that can be combined to create protocols with specific security properties.

Key characteristics:

- **Simple specification**: The core spec is about 35 pages (vs. TLS at 100+)
- **Formal verification**: Noise handshakes have been formally analyzed
- **No negotiation complexity**: Pattern chosen at design time, not runtime
- **Modern cryptography**: Uses Curve25519, ChaCha20, SHA-256 by default

### Noise_XX Handshake Pattern

libp2p (and therefore SDN) uses the `Noise_XX` handshake pattern:

```
Noise_XX:
  -> e                     Initiator sends ephemeral public key
  <- e, ee, s, es          Responder sends ephemeral + static keys
  -> s, se                 Initiator sends static key
```

Breaking this down:

1. **Initiator sends `e`**: Creates ephemeral Curve25519 keypair, sends public key
2. **Responder sends `e, ee, s, es`**:
   - Creates ephemeral keypair, sends public key
   - Performs `ee` (ephemeral-ephemeral) Diffie-Hellman
   - Encrypts and sends static public key
   - Performs `es` (ephemeral-static) Diffie-Hellman
3. **Initiator sends `s, se`**:
   - Encrypts and sends static public key
   - Performs `se` (static-ephemeral) Diffie-Hellman

After the handshake, both parties have established:
- Mutual authentication (verified each other's static keys)
- A shared symmetric key for encrypting all subsequent traffic

### Implementation in SDN

::: code-group

```typescript [JavaScript/Browser]
import { createLibp2p } from 'libp2p';
import { noise } from '@chainsafe/libp2p-noise';

const node = await createLibp2p({
  // ... other config
  connectionEncryption: [noise()],
});
```

```go [Go/Server]
import (
    "github.com/libp2p/go-libp2p"
    "github.com/libp2p/go-libp2p/p2p/security/noise"
)

host, err := libp2p.New(
    libp2p.Security(noise.ID, noise.New),
)
```

:::

### Forward Secrecy

The `XX` pattern provides **forward secrecy**: even if a node's long-term private key is compromised in the future, past communications remain secure.

This works because:

1. **Ephemeral keys**: Each connection uses fresh ephemeral keypairs
2. **Key derivation**: Session keys derive from both ephemeral and static keys
3. **Key deletion**: Ephemeral keys are discarded after handshake completion

```
Session Key = HKDF(ee || es || se)
              ─────────────────────
              │      │      │
              │      │      └── Static-Ephemeral DH
              │      └────────── Ephemeral-Static DH
              └─────────────────  Ephemeral-Ephemeral DH
```

### Comparison to TLS

| Feature | Noise (libp2p) | TLS 1.3 |
|---------|---------------|---------|
| **Handshake Latency** | 1.5 RTT (XX pattern) | 1 RTT |
| **Forward Secrecy** | Always | Always (1.3+) |
| **Mutual Auth** | Built-in | Optional |
| **Cipher Negotiation** | None (fixed) | Yes |
| **Certificate Infrastructure** | None needed | PKI required |
| **Implementation Complexity** | Lower | Higher |
| **Key Rotation** | Per-connection | Session resumption |

SDN uses Noise because:
- No need for centralized certificate authorities
- Simpler implementation with fewer attack surfaces
- Native support for mutual authentication
- Better suited for decentralized networks

## Peer Authentication

Every node in SDN has a cryptographic identity that enables authentication without central authorities.

### Ed25519 Key Pairs for Identity

Each SDN node generates an Ed25519 keypair on first run:

```
┌──────────────────────────────────────────────────┐
│               Ed25519 Key Pair                    │
├──────────────────────────────────────────────────┤
│                                                   │
│  Private Key (32 bytes)                          │
│  ├── Must be kept secret                         │
│  ├── Used for signing messages                   │
│  └── Used in Noise handshake                     │
│                                                   │
│  Public Key (32 bytes)                           │
│  ├── Shared with all peers                       │
│  ├── Used to derive Peer ID                      │
│  └── Used to verify signatures                   │
│                                                   │
└──────────────────────────────────────────────────┘
```

### libp2p Peer IDs

Peer IDs are derived from public keys using multihash encoding:

```
Public Key (Ed25519, 32 bytes)
        │
        ▼
┌───────────────────┐
│  Protobuf Encode  │  → Add key type prefix
└───────────────────┘
        │
        ▼
┌───────────────────┐
│   SHA-256 Hash    │  → If encoded key > 42 bytes
└───────────────────┘
        │
        ▼
┌───────────────────┐
│ Multihash Encode  │  → Add hash function + length prefix
└───────────────────┘
        │
        ▼
┌───────────────────┐
│  Base58 Encode    │  → Human-readable representation
└───────────────────┘
        │
        ▼
   12D3KooWExample...
```

For Ed25519 keys (32 bytes), the public key is embedded directly in the Peer ID, allowing instant verification without additional lookups.

### How Peers Verify Each Other

During the Noise_XX handshake:

```
┌─────────────┐                              ┌─────────────┐
│   Node A    │                              │   Node B    │
│  (Dialer)   │                              │ (Listener)  │
└──────┬──────┘                              └──────┬──────┘
       │                                            │
       │  1. Initiate connection to Peer ID         │
       │─────────────────────────────────────────►  │
       │                                            │
       │  2. Noise handshake exchanges keys         │
       │◄────────────────────────────────────────► │
       │                                            │
       │  3. Extract public key from Noise          │
       │                                            │
       │  4. Derive Peer ID from public key         │
       │                                            │
       │  5. Compare with expected Peer ID          │
       │     ✓ Match = Connection authenticated     │
       │     ✗ Mismatch = Connection rejected       │
       │                                            │
```

### Mutual Authentication Guarantees

After a successful connection:

1. **Both parties authenticated**: Each node verified the other's identity
2. **No impersonation possible**: Private key required to complete handshake
3. **Peer ID binding**: Communication channel bound to specific identities
4. **Man-in-the-middle protection**: Attacker cannot insert themselves

```typescript
// In SDN JavaScript client
node.addEventListener('peer:connect', (evt) => {
  const peerId = evt.detail.toString();
  // This peer ID is cryptographically verified
  console.log(`Authenticated connection with ${peerId}`);
});
```

## Data Integrity

Beyond transport encryption, SDN ensures data integrity at the message level through digital signatures.

### Message Signing with Ed25519

Every message published to SDN includes an Ed25519 signature:

```
┌────────────────────────────────────────────────────────────────┐
│                       SDN Message                               │
├────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Schema Name (variable)    "OMM"                               │
│  ─────────────────────────────────────────────────────────     │
│  Data (FlatBuffer binary)  [Serialized orbital elements]       │
│  ─────────────────────────────────────────────────────────     │
│  Signature (64 bytes)      [Ed25519 signature over data]       │
│  ─────────────────────────────────────────────────────────     │
│  Publisher Peer ID         12D3KooW...                         │
│                                                                 │
└────────────────────────────────────────────────────────────────┘
```

The signature covers the entire data payload, ensuring:
- Data has not been modified in transit
- Data was created by the claimed publisher
- Publisher possessed the private key at signing time

### Signing Process

```typescript
import { sign } from '@spacedatanetwork/sdn-js/crypto';

// Create the message data
const ommData = serializeToFlatBuffer(orbitalElements);

// Sign with node's private key
const signature = await sign(privateKey, ommData);

// Combine for transmission
const message = new Uint8Array(ommData.length + 64);
message.set(ommData, 0);
message.set(signature, ommData.length);
```

### Signature Verification on Receipt

When a node receives a message:

```typescript
import { verify } from '@spacedatanetwork/sdn-js/crypto';

// Extract signature from message
const data = message.slice(0, message.length - 64);
const signature = message.slice(message.length - 64);

// Get publisher's public key from Peer ID
const publicKey = extractPublicKey(publisherPeerId);

// Verify signature
const isValid = await verify(publicKey, data, signature);

if (!isValid) {
  // Reject message - signature invalid
  throw new Error('Invalid signature');
}

// Process verified message
processData(data);
```

### FlatBuffers and Data Validity

[FlatBuffers](https://google.github.io/flatbuffers/) provide additional security benefits:

1. **Schema enforcement**: Data must match defined schema structure
2. **No parsing vulnerabilities**: Zero-copy access prevents buffer overflow attacks
3. **Type safety**: Strong typing prevents type confusion attacks
4. **Size limits**: Schema-defined bounds prevent resource exhaustion

```
┌─────────────────────────────────────────────────────────────┐
│              FlatBuffer Validation Pipeline                  │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  1. Size Check        → Reject if too large/small           │
│  2. Root Verification → Verify buffer root is valid         │
│  3. Field Access      → Type-safe field extraction          │
│  4. Range Validation  → Verify values within bounds         │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### Schema Validation as Security Boundary

SDN uses Space Data Standards schemas as a security boundary:

```go
// Server-side validation
func ValidateMessage(schema string, data []byte) error {
    // Get schema definition
    schemaDef, ok := schemas[schema]
    if !ok {
        return ErrUnknownSchema
    }

    // Verify FlatBuffer structure
    if err := schemaDef.Verify(data); err != nil {
        return ErrInvalidSchema
    }

    // Validate business rules
    if err := schemaDef.ValidateRules(data); err != nil {
        return ErrValidationFailed
    }

    return nil
}
```

## Key Exchange & Management

Understanding key management is essential for secure SDN operations.

### Ephemeral Keys in Noise

Each connection uses fresh ephemeral keys:

```
┌──────────────────────────────────────────────────────────────────┐
│                    Connection Key Lifecycle                       │
├──────────────────────────────────────────────────────────────────┤
│                                                                   │
│  Connection Start                                                │
│  ├── Generate ephemeral X25519 keypair                          │
│  ├── Use in Noise handshake                                     │
│  └── Derive session keys                                         │
│                                                                   │
│  Connection Active                                                │
│  ├── All traffic encrypted with session keys                    │
│  └── Ephemeral private key can be deleted                       │
│                                                                   │
│  Connection End                                                   │
│  ├── Session keys destroyed                                      │
│  └── No key material remains                                     │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

Benefits:
- **Forward secrecy**: Past sessions secure even if static key compromised
- **Replay protection**: Each session has unique keys
- **Minimal key exposure**: Keys exist only for connection duration

### Static Keys for Peer Identity

Static keys (Ed25519) persist across sessions:

| Key Type | Lifetime | Purpose | Storage |
|----------|----------|---------|---------|
| **Static Private** | Permanent | Signing, identity proof | Secure file, keychain |
| **Static Public** | Permanent | Embedded in Peer ID | Shared with network |
| **Ephemeral Private** | Per-connection | Key exchange | Memory only |
| **Ephemeral Public** | Per-connection | Key exchange | Transmitted in handshake |

### Key Derivation Process

After Noise handshake completes:

```
┌─────────────────────────────────────────────────────────────┐
│                    Key Derivation                            │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  DH Results:                                                 │
│  ├── ee = X25519(ephemeral_a, ephemeral_b)                  │
│  ├── es = X25519(ephemeral_a, static_b)                     │
│  └── se = X25519(static_a, ephemeral_b)                     │
│                                                              │
│  Chaining Key (CK):                                          │
│  └── Updated after each DH: CK = HKDF(CK, DH_result)        │
│                                                              │
│  Session Keys:                                               │
│  ├── encrypt_key = HKDF(CK, "encrypt")                      │
│  └── decrypt_key = HKDF(CK, "decrypt")                      │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### Best Practices for Key Storage

::: warning Key Security
Your node's private key is its identity. If compromised, an attacker can impersonate your node.
:::

**Server Nodes (Go)**:

```bash
# Keys stored in config directory
~/.spacedatanetwork/
├── config.json        # Node configuration
└── identity.key       # Ed25519 private key (chmod 600)
```

Recommendations:
- Set restrictive permissions: `chmod 600 identity.key`
- Use encrypted filesystem where possible
- Consider hardware security modules (HSM) for high-value nodes
- Back up keys securely and offline

**Browser Nodes (JavaScript)**:

```typescript
// Keys stored in IndexedDB (browser) or keychain (Node.js)
// For sensitive applications, consider:
const node = await SDNNode.create({
  // Use Web Crypto for key storage
  keyStorage: 'webcrypto',
  // Or external key management
  keyProvider: async () => {
    return await fetchFromSecureVault();
  }
});
```

## Security Best Practices

### Verifying Peer Identities

Before trusting data from a peer, verify their identity:

```typescript
// Maintain allowlist of trusted peers
const trustedPeers = new Set([
  '12D3KooWSpaceAgencyNode...',
  '12D3KooWOperatorNode...',
]);

node.subscribe('CDM', (data, peerId) => {
  if (!trustedPeers.has(peerId)) {
    console.warn(`Received CDM from untrusted peer: ${peerId}`);
    // Handle accordingly - log, quarantine, reject
    return;
  }

  // Process trusted data
  processConjunctionData(data);
});
```

### Handling Untrusted Data

Always validate data even after signature verification:

```typescript
function processOrbitalData(data: OMMData, peerId: string): void {
  // 1. Schema already validated by FlatBuffers

  // 2. Validate business rules
  if (data.MEAN_MOTION <= 0 || data.MEAN_MOTION > 20) {
    throw new Error('Invalid mean motion value');
  }

  // 3. Check temporal validity
  const epoch = new Date(data.EPOCH);
  const now = new Date();
  const ageHours = (now.getTime() - epoch.getTime()) / 3600000;

  if (ageHours > 24) {
    console.warn('Stale orbital data received');
  }

  // 4. Cross-reference with known data
  if (trustedSources.has(peerId)) {
    storeWithHighConfidence(data);
  } else {
    storeWithLowConfidence(data);
  }
}
```

### Network Isolation Options

For sensitive deployments, consider network isolation:

**Private Network Setup**:

```bash
# Generate network secret (all nodes must share this)
spacedatanetwork swarm key gen > swarm.key

# Configure nodes to use private network
spacedatanetwork init
cp swarm.key ~/.spacedatanetwork/swarm.key

# Nodes will only connect to peers with matching swarm key
spacedatanetwork daemon
```

**Topic-Based Isolation**:

```typescript
// Use topic prefixes for isolation
const PRIVATE_PREFIX = '/private/org-name/';

node.subscribe(`${PRIVATE_PREFIX}CDM`, (data, peerId) => {
  // Only peers subscribing to this specific topic receive data
});
```

### Monitoring and Alerting

Implement security monitoring:

```typescript
// Track connection patterns
const connectionMetrics = {
  totalConnections: 0,
  failedHandshakes: 0,
  rejectedMessages: 0,
  unknownPeers: new Set(),
};

node.addEventListener('peer:connect', (evt) => {
  connectionMetrics.totalConnections++;

  const peerId = evt.detail.toString();
  if (!knownPeers.has(peerId)) {
    connectionMetrics.unknownPeers.add(peerId);

    // Alert on unusual connection patterns
    if (connectionMetrics.unknownPeers.size > threshold) {
      alertSecurityTeam('Unusual number of unknown peer connections');
    }
  }
});

// Log failed signature verifications
function onSignatureFailure(peerId: string, schema: string): void {
  connectionMetrics.rejectedMessages++;
  console.error(`Signature verification failed: peer=${peerId} schema=${schema}`);

  // Consider temporary ban for repeated failures
  trackFailure(peerId);
  if (getFailureCount(peerId) > 5) {
    blockPeer(peerId);
    alertSecurityTeam(`Blocked peer ${peerId} for repeated signature failures`);
  }
}
```

### Security Checklist

Before deploying an SDN node:

- [ ] **Key Protection**: Private key stored securely with appropriate permissions
- [ ] **Key Backup**: Secure offline backup of identity key
- [ ] **Peer Verification**: Implement peer allowlist for critical data
- [ ] **Data Validation**: Validate all received data beyond schema checks
- [ ] **Monitoring**: Set up logging and alerting for security events
- [ ] **Updates**: Plan for regular updates to address security patches
- [ ] **Network Segmentation**: Consider private network for sensitive operations
- [ ] **Access Control**: Limit who can access node configuration

## Optional Application-Layer Encryption

For sensitive data requiring confidentiality, SDN supports application-layer encryption:

```typescript
import { encrypt, decrypt, generateKey } from '@spacedatanetwork/sdn-js/crypto';

// Generate or retrieve shared secret
const sharedKey = await getSharedKey(recipientPeerId);

// Encrypt before publishing
const sensitiveData = serializeToFlatBuffer(classifiedOrbit);
const encryptedData = await encrypt(sharedKey, sensitiveData);

await node.publish('OMM', {
  encrypted: true,
  data: encryptedData,
});

// Recipients decrypt with shared key
node.subscribe('OMM', async (message, peerId) => {
  if (message.encrypted) {
    const decrypted = await decrypt(sharedKey, message.data);
    const orbit = deserializeFromFlatBuffer(decrypted);
    processOrbit(orbit);
  }
});
```

This uses AES-256-GCM encryption via WASM or Web Crypto API for confidential data exchange.

## Related Documentation

- [Architecture Overview](/guide/architecture) - System architecture and data flow
- [Full Node Setup](/guide/full-node) - Deploy and configure a server node
- [Schema Reference](/reference/schemas) - Space Data Standards schemas
