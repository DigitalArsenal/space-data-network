# ID Exchange Protocol

The ID Exchange Protocol enables identity verification and trust establishment between SDN nodes.

## Overview

The ID Exchange Protocol provides:

- Peer identity verification
- Entity profile exchange
- Trust chain establishment
- Key attestation

## Protocol Identifier

```
/spacedatanetwork/id-exchange/1.0.0
```

## Message Types

### ID_REQUEST

Request identity information from a peer.

```flatbuffers
table IDRequest {
  // Request metadata
  request_id: string;              // Unique request ID
  timestamp: string;               // ISO 8601 timestamp

  // Requested information
  include_profile: bool;           // Include entity profile
  include_attestations: bool;      // Include attestations
  include_capabilities: bool;      // Include capabilities

  // Challenge for proof of identity
  challenge: [uint8];              // Random challenge bytes
}
```

### ID_RESPONSE

Response to identity request.

```flatbuffers
table IDResponse {
  // Response metadata
  request_id: string;              // Original request ID
  timestamp: string;               // Response timestamp

  // Identity
  peer_id: string;                 // libp2p Peer ID
  public_key: [uint8];             // Ed25519 public key
  public_key_algorithm: string;    // Key algorithm

  // Challenge response
  challenge_response: [uint8];     // Signed challenge

  // Entity Profile (optional)
  entity_profile: EPM;             // Full entity profile

  // Attestations (optional)
  attestations: [Attestation];     // Identity attestations

  // Capabilities (optional)
  supported_schemas: [string];     // Supported data schemas
  services: [string];              // Provided services
}

table Attestation {
  attester_id: string;             // Attesting peer ID
  attester_name: string;           // Attester name
  statement: string;               // Attestation statement
  timestamp: string;               // When attested
  signature: [uint8];              // Attester signature
  expires: string;                 // Expiration date
}
```

### ID_VERIFY

Request to verify a third-party identity.

```flatbuffers
table IDVerify {
  // Request
  request_id: string;
  target_peer_id: string;          // Peer to verify

  // Evidence
  claimed_public_key: [uint8];     // Claimed public key
  claimed_profile: EPM;            // Claimed profile
}
```

### ID_VERIFY_RESPONSE

Response to verification request.

```flatbuffers
table IDVerifyResponse {
  request_id: string;
  target_peer_id: string;

  // Verification result
  verified: bool;                  // Verification passed
  confidence: double;              // Confidence level (0-1)

  // Details
  key_verified: bool;              // Public key verified
  profile_verified: bool;          // Profile verified
  attestations_verified: uint32;   // Number verified

  // Issues
  issues: [string];                // Any verification issues
}
```

### TRUST_ANNOUNCE

Announce trust relationship.

```flatbuffers
table TrustAnnounce {
  // Trust relationship
  truster_id: string;              // Trusting peer
  trustee_id: string;              // Trusted peer
  trust_level: string;             // FULL, PARTIAL, REVOKED

  // Scope
  schemas: [string];               // Schemas trust applies to
  services: [string];              // Services trust applies to

  // Metadata
  reason: string;                  // Reason for trust
  timestamp: string;               // When established
  expires: string;                 // Expiration
  signature: [uint8];              // Truster signature
}
```

## Identity Verification Flow

### Initial Connection

```
Node A                           Node B
    |                                 |
    |  ID_REQUEST(challenge)         |
    |-------------------------------->|
    |                                 |
    |       ID_RESPONSE(signed)      |
    |<--------------------------------|
    |                                 |
    |  (verify signature)             |
    |                                 |
```

### Profile Exchange

```
Node A                           Node B
    |                                 |
    |  ID_REQUEST(include_profile)   |
    |-------------------------------->|
    |                                 |
    |  ID_RESPONSE(entity_profile)   |
    |<--------------------------------|
    |                                 |
    |  (validate and store)           |
    |                                 |
```

### Trust Chain Verification

```
Node A              Node B              Node C (Known Trusted)
    |                   |                      |
    |  (connect)        |                      |
    |<----------------->|                      |
    |                   |                      |
    |  ID_VERIFY(B's key)                      |
    |----------------------------------------->|
    |                                          |
    |        ID_VERIFY_RESPONSE(verified)      |
    |<-----------------------------------------|
    |                   |                      |
    |  (trust B via C)  |                      |
    |                   |                      |
```

## Cryptographic Identity

### Key Generation

SDN uses Ed25519 for identity:

```typescript
import { generateIdentity } from '@spacedatanetwork/sdn-js';

// Generate new identity
const identity = await generateIdentity();

console.log('Peer ID:', identity.peerId);
console.log('Public Key:', identity.publicKey);
// Private key stored securely
```

### Peer ID Derivation

Peer IDs are derived from public keys:

```
PeerID = Base58(Multihash(SHA-256(PublicKey)))
```

Example: `12D3KooWExample123456789...`

### Challenge-Response

```typescript
// Verifier creates challenge
const challenge = crypto.randomBytes(32);

// Responder signs challenge
const response = ed25519.sign(challenge, privateKey);

// Verifier validates
const isValid = ed25519.verify(response, challenge, publicKey);
```

## Entity Profiles

### Publishing Profile

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Create and publish entity profile
const profile = {
  ENTITY_ID: node.peerId,
  ENTITY_NAME: 'Example Organization',
  ENTITY_TYPE: 'OPERATOR',
  PUBLIC_KEY: node.publicKey,
  PEER_ID: node.peerId,
  SUPPORTED_SCHEMAS: ['OMM', 'CDM'],
  CREATED: new Date().toISOString()
};

await node.publishProfile(profile);
```

### Retrieving Profile

```typescript
// Get peer's profile
const profile = await node.getProfile(peerId);

if (profile) {
  console.log('Entity:', profile.ENTITY_NAME);
  console.log('Type:', profile.ENTITY_TYPE);
  console.log('Schemas:', profile.SUPPORTED_SCHEMAS);
}
```

## Trust Management

### Trust Levels

| Level | Description |
|-------|-------------|
| FULL | Complete trust for all operations |
| PARTIAL | Trust for specific schemas/services |
| NONE | No trust (default) |
| REVOKED | Previously trusted, now revoked |

### Establishing Trust

```typescript
// Trust a peer
await node.trust.add(peerId, {
  level: 'FULL',
  reason: 'Verified operator',
  expires: '2025-01-01T00:00:00Z'
});

// Partial trust
await node.trust.add(peerId, {
  level: 'PARTIAL',
  schemas: ['OMM'],
  reason: 'OMM data provider'
});
```

### Checking Trust

```typescript
// Check trust status
const isTrusted = node.trust.has(peerId);
const trustLevel = node.trust.level(peerId);

// Check for specific schema
const trustsOMM = node.trust.hasForSchema(peerId, 'OMM');
```

### Trust Announcements

```typescript
// Subscribe to trust announcements
node.on('trust:announce', (announcement) => {
  console.log(`${announcement.truster_id} trusts ${announcement.trustee_id}`);
  console.log(`Level: ${announcement.trust_level}`);
});
```

## Attestations

### Creating Attestation

```typescript
// Attest to another peer's identity
const attestation = await node.attest(peerId, {
  statement: 'Verified operator of satellite constellation',
  expires: '2025-01-01T00:00:00Z'
});
```

### Verifying Attestations

```typescript
// Get attestations for a peer
const attestations = await node.getAttestations(peerId);

for (const att of attestations) {
  // Verify attestation signature
  const isValid = await node.verifyAttestation(att);

  if (isValid) {
    console.log(`Attested by ${att.attester_name}: ${att.statement}`);
  }
}
```

## Security Considerations

### Key Storage

- Private keys should never be transmitted
- Use secure key storage (hardware tokens, secure enclaves)
- Support key rotation

### Verification Best Practices

```typescript
// Always verify before trusting
const verified = await node.verifyPeer(peerId, {
  checkProfile: true,
  checkAttestations: true,
  minimumAttestations: 2
});

if (verified.confidence >= 0.8) {
  await node.trust.add(peerId, { level: 'PARTIAL' });
}
```

### Revocation

```typescript
// Revoke trust
await node.trust.revoke(peerId, {
  reason: 'Key compromise suspected'
});

// Announce revocation
await node.trust.announceRevocation(peerId);
```

## Implementation

### JavaScript

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Request identity
const identity = await node.requestIdentity(peerId);

// Verify challenge
const challenge = crypto.randomBytes(32);
const verified = await node.verifyChallenge(peerId, challenge);
```

### Go

```go
import (
    "github.com/spacedatanetwork/sdn-server/internal/protocol"
)

// Create ID exchange handler
idExchange := protocol.NewIDExchange(host)

// Request identity
identity, err := idExchange.RequestIdentity(ctx, peerId)

// Verify
verified := idExchange.Verify(identity, challenge)
```

## See Also

- [SDS Exchange Protocol](/reference/protocol-sds)
- [Digital Identity Guide](/guide/digital-identity)
- [Entity Data Schemas](/reference/entity)
