# Digital Identity

Digital identity is foundational to the Space Data Network. Every participant - whether a space agency, satellite operator, ground station, or individual researcher - is identified by a cryptographic key pair. This enables trustless verification of data authenticity without relying on central authorities.

## Identity in the Space Data Network

### Why Identity Matters for Space Data

Space situational awareness depends on trust. When an operator receives a conjunction warning, they need to know:

- **Who sent it?** Is this from a legitimate tracking source?
- **Is it authentic?** Has the data been modified in transit?
- **Can I verify it?** Do I have the means to check these claims?

In traditional centralized systems, trust flows from a central authority. In SDN's decentralized network, trust is established through cryptography. Each piece of data carries a digital signature that proves its origin and integrity.

```
Traditional:    Data --> Central Authority --> Recipients (trust the authority)
SDN:            Data + Signature --> Recipients (verify the signature)
```

### Cryptographic vs Organizational Identity

SDN distinguishes between two layers of identity:

| Layer | Description | Example |
|-------|-------------|---------|
| **Cryptographic Identity** | Ed25519 key pair, Peer ID derived from public key | `12D3KooWA1b2C3d4E5f6G7h8I9j0...` |
| **Organizational Identity** | Human-readable information in Entity Profile Manifest | "NASA Goddard Space Flight Center" |

The cryptographic identity is always present and verifiable. The organizational identity is optional metadata that can be signed and published to the network.

### Trust Models in Decentralized Networks

SDN supports multiple trust models:

**1. Direct Trust**
- You personally verify an entity's public key
- Highest assurance, requires out-of-band verification
- Suitable for high-value relationships

**2. Web of Trust**
- Entities sign each other's EPMs (Entity Profile Manifests)
- Trust is transitive through verified paths
- Suitable for community-based trust

**3. Reputation-Based**
- Track data quality and reliability over time
- Build trust through consistent, accurate contributions
- Suitable for open participation

::: tip
Most SDN deployments use a combination of these models. Start with direct trust for critical partners, then expand through web of trust for broader collaboration.
:::

## Ed25519 Key Pairs

SDN uses Ed25519 for all cryptographic operations. Ed25519 provides:

- **Fast signature generation and verification**
- **Small key sizes** (32 bytes public, 64 bytes private)
- **Strong security** (128-bit security level)
- **Deterministic signatures** (same input always produces same output)

### Public/Private Key Fundamentals

```
┌─────────────────────────────────────────────────────────────┐
│                      Key Pair                                │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│   Private Key (32 bytes)          Public Key (32 bytes)     │
│   ┌────────────────────┐         ┌────────────────────┐     │
│   │ Keep this SECRET!  │ ──────► │ Share freely       │     │
│   │                    │ derives │                    │     │
│   │ Signs data         │         │ Verifies signatures│     │
│   │ Proves ownership   │         │ Derives Peer ID    │     │
│   └────────────────────┘         └────────────────────┘     │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

- **Private Key**: Must be kept secret. Used to sign data and prove identity.
- **Public Key**: Shared openly. Used to verify signatures and derive Peer ID.

### Generating Key Pairs

::: code-group

```typescript [TypeScript]
import { generateKeyPair } from '@libp2p/crypto/keys';
import { peerIdFromKeys } from '@libp2p/peer-id';

async function createIdentity() {
  // Generate Ed25519 key pair
  const keyPair = await generateKeyPair('Ed25519');

  // Derive Peer ID from public key
  const peerId = await peerIdFromKeys(keyPair.public.bytes, keyPair.bytes);

  console.log('Peer ID:', peerId.toString());
  console.log('Public Key (hex):', Buffer.from(keyPair.public.bytes).toString('hex'));

  // Export private key for storage (keep this secret!)
  const privateKeyBytes = keyPair.bytes;

  return {
    peerId,
    publicKey: keyPair.public,
    privateKey: keyPair,
  };
}
```

```go [Go]
package main

import (
    "encoding/hex"
    "fmt"

    "github.com/libp2p/go-libp2p/core/crypto"
    "github.com/libp2p/go-libp2p/core/peer"
)

func createIdentity() (peer.ID, crypto.PrivKey, error) {
    // Generate Ed25519 key pair
    privKey, pubKey, err := crypto.GenerateEd25519Key(nil)
    if err != nil {
        return "", nil, err
    }

    // Derive Peer ID from public key
    peerID, err := peer.IDFromPublicKey(pubKey)
    if err != nil {
        return "", nil, err
    }

    fmt.Println("Peer ID:", peerID.String())

    // Get raw public key bytes
    pubKeyBytes, _ := pubKey.Raw()
    fmt.Println("Public Key (hex):", hex.EncodeToString(pubKeyBytes))

    return peerID, privKey, nil
}
```

:::

### Key Storage and Security

::: danger SECURITY WARNING
Your private key is your identity. If it's compromised, an attacker can impersonate you on the network. Treat it with the same care as a password or SSH key.
:::

**Storage Best Practices:**

| Environment | Recommended Storage |
|-------------|---------------------|
| Server | Encrypted file with restricted permissions (0600) |
| Browser | IndexedDB with Web Crypto API |
| Desktop App | OS keychain (Keychain on macOS, Credential Manager on Windows) |
| Mobile | Secure enclave or keystore |

::: code-group

```typescript [Browser Storage]
import { openDB } from 'idb';

async function storePrivateKey(privateKeyBytes: Uint8Array) {
  // Use Web Crypto API to derive an encryption key from a password
  const password = await promptUserForPassword();
  const encoder = new TextEncoder();
  const keyMaterial = await crypto.subtle.importKey(
    'raw',
    encoder.encode(password),
    'PBKDF2',
    false,
    ['deriveBits', 'deriveKey']
  );

  const salt = crypto.getRandomValues(new Uint8Array(16));
  const encryptionKey = await crypto.subtle.deriveKey(
    {
      name: 'PBKDF2',
      salt,
      iterations: 100000,
      hash: 'SHA-256',
    },
    keyMaterial,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt']
  );

  const iv = crypto.getRandomValues(new Uint8Array(12));
  const encryptedKey = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv },
    encryptionKey,
    privateKeyBytes
  );

  // Store in IndexedDB
  const db = await openDB('sdn-identity', 1, {
    upgrade(db) {
      db.createObjectStore('keys');
    },
  });

  await db.put('keys', { salt, iv, encryptedKey }, 'privateKey');
}
```

```go [Server Storage]
package main

import (
    "encoding/base64"
    "os"

    "github.com/libp2p/go-libp2p/core/crypto"
)

func savePrivateKey(privKey crypto.PrivKey, path string) error {
    // Marshal the private key
    keyBytes, err := crypto.MarshalPrivateKey(privKey)
    if err != nil {
        return err
    }

    // Encode as base64 for storage
    encoded := base64.StdEncoding.EncodeToString(keyBytes)

    // Write with restricted permissions
    return os.WriteFile(path, []byte(encoded), 0600)
}

func loadPrivateKey(path string) (crypto.PrivKey, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }

    keyBytes, err := base64.StdEncoding.DecodeString(string(data))
    if err != nil {
        return nil, err
    }

    return crypto.UnmarshalPrivateKey(keyBytes)
}
```

:::

### libp2p Peer IDs Derived from Public Keys

The Peer ID is a unique identifier derived from the public key using multihash encoding:

```
Public Key (32 bytes)
        │
        ▼
┌───────────────────┐
│   Multihash       │
│  ┌─────────────┐  │
│  │ 0x00 (identity)│  Length prefix
│  │ 0x24 (36)   │  │
│  │ 0xed01      │  │  Ed25519 multicodec
│  │ [32 bytes]  │  │  Public key bytes
│  └─────────────┘  │
└───────────────────┘
        │
        ▼
   Base58 Encode
        │
        ▼
12D3KooWA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0...
```

The Peer ID:
- Uniquely identifies a node on the network
- Can be used to look up the node's public key
- Enables signature verification without prior key exchange

## Entity Profile Manifest (EPM)

The Entity Profile Manifest provides human-readable identity information that can be cryptographically linked to a Peer ID. Think of it as a **digital business card for space entities**.

### EPM Schema Overview

The EPM schema is defined in FlatBuffers format at `schemas/sds/schema/EPM/main.fbs`:

```flatbuffers
table EPM {
  DN: string;                    // Distinguished Name
  LEGAL_NAME: string;            // Organization or person name
  FAMILY_NAME: string;           // Surname (for individuals)
  GIVEN_NAME: string;            // First name (for individuals)
  ADDITIONAL_NAME: string;       // Middle name
  HONORIFIC_PREFIX: string;      // e.g., "Dr.", "Mr."
  HONORIFIC_SUFFIX: string;      // e.g., "Jr.", "PhD"
  JOB_TITLE: string;             // Role or position
  OCCUPATION: string;            // Field of work
  ADDRESS: Address;              // Physical location
  ALTERNATE_NAMES: [string];     // Other known names
  EMAIL: string;                 // Contact email
  TELEPHONE: string;             // Contact phone
  KEYS: [CryptoKey];             // Cryptographic keys
  MULTIFORMAT_ADDRESS: [string]; // Network addresses
}
```

### EPM Fields Reference

| Field | Purpose | Example |
|-------|---------|---------|
| `LEGAL_NAME` | Full legal name of entity | "National Aeronautics and Space Administration" |
| `DN` | Distinguished Name (X.500 format) | "CN=NASA,O=US Government,C=US" |
| `GIVEN_NAME` / `FAMILY_NAME` | Individual name components | "John" / "Smith" |
| `JOB_TITLE` | Role within organization | "Flight Dynamics Officer" |
| `EMAIL` | Primary contact email | "contact@example.space" |
| `KEYS` | Associated cryptographic keys | Ed25519 public keys |
| `MULTIFORMAT_ADDRESS` | Network endpoints | "/ip4/192.168.1.1/tcp/4001" |
| `ADDRESS` | Physical address | Country, region, locality, street |

### How EPM Functions Like a Digital vCard

Just as a vCard contains contact information that can be shared, an EPM contains:

| vCard Field | EPM Equivalent |
|-------------|----------------|
| `FN` (Full Name) | `LEGAL_NAME` |
| `N` (Name components) | `GIVEN_NAME`, `FAMILY_NAME`, etc. |
| `ORG` | Implicit from `LEGAL_NAME` |
| `TITLE` | `JOB_TITLE` |
| `EMAIL` | `EMAIL` |
| `TEL` | `TELEPHONE` |
| `ADR` | `ADDRESS` |
| `KEY` | `KEYS` (cryptographic keys) |

The key difference: EPMs are **cryptographically signed**, making them verifiable.

### Publishing Your EPM to the Network

Once created and signed, your EPM can be published to the network:

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

async function publishEntityProfile(node: SDNNode) {
  // Your EPM will be signed with the node's private key
  await node.publish('EPM', {
    LEGAL_NAME: 'Orbital Dynamics Corp',
    EMAIL: 'operations@orbitaldynamics.space',
    TELEPHONE: '+1-555-ORBIT',
    KEYS: [{
      PUBLIC_KEY: node.publicKeyHex,
      KEY_TYPE: 'Signing',
    }],
    MULTIFORMAT_ADDRESS: node.multiaddrs.map(ma => ma.toString()),
  });

  console.log('EPM published to network');
}
```

## Identity Card Builder

Creating a complete digital identity involves generating keys, constructing an EPM, and signing it.

### Creating Your Digital Identity

::: code-group

```typescript [TypeScript]
import * as flatbuffers from 'flatbuffers';
import { EPM, EPMT, CryptoKeyT, AddressT } from '@spacedatastandards/sds/EPM';
import { KeyType } from '@spacedatastandards/sds/EPM/KeyType';
import { generateKeyPair } from '@libp2p/crypto/keys';
import { peerIdFromKeys } from '@libp2p/peer-id';

interface IdentityCardOptions {
  legalName: string;
  email?: string;
  telephone?: string;
  jobTitle?: string;
  address?: {
    country: string;
    region?: string;
    locality?: string;
    street?: string;
    postalCode?: string;
  };
}

async function buildIdentityCard(options: IdentityCardOptions) {
  // Step 1: Generate cryptographic identity
  const keyPair = await generateKeyPair('Ed25519');
  const peerId = await peerIdFromKeys(keyPair.public.bytes, keyPair.bytes);
  const publicKeyHex = Buffer.from(keyPair.public.bytes).toString('hex');

  // Step 2: Build the EPM using the object API
  const epm = new EPMT();
  epm.LEGAL_NAME = options.legalName;
  epm.EMAIL = options.email || null;
  epm.TELEPHONE = options.telephone || null;
  epm.JOB_TITLE = options.jobTitle || null;

  // Add cryptographic key
  const cryptoKey = new CryptoKeyT();
  cryptoKey.PUBLIC_KEY = publicKeyHex;
  cryptoKey.KEY_TYPE = KeyType.Signing;
  epm.KEYS = [cryptoKey];

  // Add address if provided
  if (options.address) {
    const address = new AddressT();
    address.COUNTRY = options.address.country;
    address.REGION = options.address.region || null;
    address.LOCALITY = options.address.locality || null;
    address.STREET = options.address.street || null;
    address.POSTAL_CODE = options.address.postalCode || null;
    epm.ADDRESS = address;
  }

  // Step 3: Serialize to FlatBuffer
  const builder = new flatbuffers.Builder(1024);
  const offset = epm.pack(builder);
  EPM.finishEPMBuffer(builder, offset);
  const epmBytes = builder.asUint8Array();

  // Step 4: Sign the EPM
  const signature = await keyPair.sign(epmBytes);

  return {
    peerId: peerId.toString(),
    publicKey: publicKeyHex,
    privateKey: keyPair,
    epmBytes,
    signature,
    epm,
  };
}

// Usage example
async function main() {
  const identity = await buildIdentityCard({
    legalName: 'Orbital Dynamics Corporation',
    email: 'ops@orbitaldynamics.space',
    telephone: '+1-555-ORBIT',
    jobTitle: 'Satellite Operations',
    address: {
      country: 'USA',
      region: 'California',
      locality: 'Los Angeles',
    },
  });

  console.log('Identity created!');
  console.log('Peer ID:', identity.peerId);
  console.log('Public Key:', identity.publicKey);
}
```

```go [Go]
package main

import (
    "encoding/hex"
    "fmt"

    flatbuffers "github.com/google/flatbuffers/go"
    "github.com/libp2p/go-libp2p/core/crypto"
    "github.com/libp2p/go-libp2p/core/peer"

    "spacedatastandards/EPM"
)

type IdentityCardOptions struct {
    LegalName string
    Email     string
    Telephone string
    JobTitle  string
    Country   string
    Region    string
    Locality  string
}

type IdentityCard struct {
    PeerID     peer.ID
    PublicKey  []byte
    PrivateKey crypto.PrivKey
    EPMBytes   []byte
    Signature  []byte
}

func BuildIdentityCard(opts IdentityCardOptions) (*IdentityCard, error) {
    // Step 1: Generate cryptographic identity
    privKey, pubKey, err := crypto.GenerateEd25519Key(nil)
    if err != nil {
        return nil, err
    }

    peerID, err := peer.IDFromPublicKey(pubKey)
    if err != nil {
        return nil, err
    }

    pubKeyBytes, _ := pubKey.Raw()
    publicKeyHex := hex.EncodeToString(pubKeyBytes)

    // Step 2: Build the EPM
    builder := flatbuffers.NewBuilder(1024)

    // Create strings
    legalName := builder.CreateString(opts.LegalName)
    email := builder.CreateString(opts.Email)
    telephone := builder.CreateString(opts.Telephone)
    jobTitle := builder.CreateString(opts.JobTitle)
    pubKeyStr := builder.CreateString(publicKeyHex)

    // Create CryptoKey
    EPM.CryptoKeyStart(builder)
    EPM.CryptoKeyAddPUBLIC_KEY(builder, pubKeyStr)
    EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
    cryptoKey := EPM.CryptoKeyEnd(builder)

    // Create keys vector
    EPM.EPMStartKEYSVector(builder, 1)
    builder.PrependUOffsetT(cryptoKey)
    keys := builder.EndVector(1)

    // Create Address if provided
    var address flatbuffers.UOffsetT
    if opts.Country != "" {
        country := builder.CreateString(opts.Country)
        region := builder.CreateString(opts.Region)
        locality := builder.CreateString(opts.Locality)

        EPM.AddressStart(builder)
        EPM.AddressAddCOUNTRY(builder, country)
        EPM.AddressAddREGION(builder, region)
        EPM.AddressAddLOCALITY(builder, locality)
        address = EPM.AddressEnd(builder)
    }

    // Create EPM
    EPM.EPMStart(builder)
    EPM.EPMAddLEGAL_NAME(builder, legalName)
    EPM.EPMAddEMAIL(builder, email)
    EPM.EPMAddTELEPHONE(builder, telephone)
    EPM.EPMAddJOB_TITLE(builder, jobTitle)
    EPM.EPMAddKEYS(builder, keys)
    if address != 0 {
        EPM.EPMAddADDRESS(builder, address)
    }
    epmOffset := EPM.EPMEnd(builder)

    EPM.FinishEPMBuffer(builder, epmOffset)
    epmBytes := builder.FinishedBytes()

    // Step 3: Sign the EPM
    signature, err := privKey.Sign(epmBytes)
    if err != nil {
        return nil, err
    }

    return &IdentityCard{
        PeerID:     peerID,
        PublicKey:  pubKeyBytes,
        PrivateKey: privKey,
        EPMBytes:   epmBytes,
        Signature:  signature,
    }, nil
}

func main() {
    card, err := BuildIdentityCard(IdentityCardOptions{
        LegalName: "Orbital Dynamics Corporation",
        Email:     "ops@orbitaldynamics.space",
        Telephone: "+1-555-ORBIT",
        JobTitle:  "Satellite Operations",
        Country:   "USA",
        Region:    "California",
        Locality:  "Los Angeles",
    })
    if err != nil {
        panic(err)
    }

    fmt.Println("Identity created!")
    fmt.Println("Peer ID:", card.PeerID.String())
    fmt.Println("Public Key:", hex.EncodeToString(card.PublicKey))
}
```

:::

### Required vs Optional Fields

| Field | Required | Purpose |
|-------|----------|---------|
| `LEGAL_NAME` | Yes | Primary identifier for the entity |
| `KEYS` | Yes | At least one cryptographic key for verification |
| `EMAIL` | Recommended | Contact information |
| `DN` | Optional | Distinguished Name for directory services |
| `ADDRESS` | Optional | Physical location |
| `TELEPHONE` | Optional | Phone contact |
| `JOB_TITLE` | Optional | Role description |
| `MULTIFORMAT_ADDRESS` | Optional | Network addresses |

### Signing Your Identity Card

Every EPM should be signed to prove authenticity:

```typescript
import { sha256 } from '@noble/hashes/sha256';

async function signAndVerifyEPM(epmBytes: Uint8Array, keyPair: any) {
  // Sign the EPM bytes
  const signature = await keyPair.sign(epmBytes);

  // The signature can be stored alongside or embedded in the EPM
  const signedEPM = {
    epm: epmBytes,
    signature: signature,
    signatureType: 'Ed25519',
    publicKey: keyPair.public.bytes,
  };

  // Verification (by recipient)
  const isValid = await keyPair.public.verify(epmBytes, signature);
  console.log('Signature valid:', isValid);

  return signedEPM;
}
```

## Peer Network Manifest (PNM)

While EPM describes the entity, the Peer Network Manifest (PNM) describes **content publication** on the network.

### PNM for Node Identification

The PNM schema (`schemas/sds/schema/PNM/main.fbs`) tracks published content:

```flatbuffers
table PNM {
  MULTIFORMAT_ADDRESS: string;     // Publisher's network address
  PUBLISH_TIMESTAMP: string;       // When the content was published
  CID: string;                     // Content Identifier (hash of content)
  FILE_NAME: string;               // Human-readable name
  FILE_ID: string;                 // Schema type (e.g., "OMM", "CDM")
  SIGNATURE: string;               // Digital signature of CID
  TIMESTAMP_SIGNATURE: string;     // Signature of timestamp
  SIGNATURE_TYPE: string;          // Type of signature used
  TIMESTAMP_SIGNATURE_TYPE: string;
}
```

### Linking EPM to PNM

The connection between EPM (who you are) and PNM (what you publish):

```
┌────────────────────────────────────────────────────────────────┐
│                      Entity Identity                            │
├────────────────────────────────────────────────────────────────┤
│                                                                  │
│   ┌─────────────┐              ┌─────────────┐                 │
│   │     EPM     │──────────────│     PNM     │                 │
│   │  (Who)      │  same key    │  (What)     │                 │
│   │             │  signs both  │             │                 │
│   │ LEGAL_NAME  │              │ CID         │                 │
│   │ EMAIL       │              │ FILE_ID     │                 │
│   │ KEYS ───────┼──────────────┼─ SIGNATURE  │                 │
│   │             │              │             │                 │
│   └─────────────┘              └─────────────┘                 │
│                                                                  │
└────────────────────────────────────────────────────────────────┘
```

### Node Capabilities Declaration

A node can declare its capabilities through its published manifests:

```typescript
async function declareCapabilities(node: SDNNode) {
  // Publish EPM with capabilities in metadata
  const capabilities = {
    schemas: ['OMM', 'CDM', 'TDM'],  // Supported data types
    roles: ['publisher', 'relay'],   // Network roles
    storage: '100GB',                // Available storage
    uptime: '99.9%',                 // Availability target
  };

  await node.publish('EPM', {
    LEGAL_NAME: 'Example Ground Station',
    ALTERNATE_NAMES: [JSON.stringify({ capabilities })],
    // ... other fields
  });
}
```

## Cryptographic Verification

### Verifying Signatures on Identity Data

::: code-group

```typescript [TypeScript]
import { unmarshalPublicKey } from '@libp2p/crypto/keys';
import { peerIdFromString } from '@libp2p/peer-id';

async function verifyEPMSignature(
  epmBytes: Uint8Array,
  signature: Uint8Array,
  publisherPeerId: string
): Promise<boolean> {
  // Extract public key from Peer ID
  const peerId = peerIdFromString(publisherPeerId);
  const publicKey = peerId.publicKey;

  if (!publicKey) {
    throw new Error('Peer ID does not contain public key');
  }

  // Unmarshal and verify
  const key = unmarshalPublicKey(publicKey);
  return key.verify(epmBytes, signature);
}

// Example usage
async function handleReceivedEPM(data: any, peerId: string, signature: Uint8Array) {
  const isValid = await verifyEPMSignature(data.epmBytes, signature, peerId);

  if (isValid) {
    console.log('EPM signature verified - data is authentic');
    // Process the EPM
  } else {
    console.warn('EPM signature invalid - data may be tampered');
    // Reject the EPM
  }
}
```

```go [Go]
package main

import (
    "fmt"

    "github.com/libp2p/go-libp2p/core/crypto"
    "github.com/libp2p/go-libp2p/core/peer"
)

func VerifyEPMSignature(epmBytes, signature []byte, publisherPeerID string) (bool, error) {
    // Parse the Peer ID
    peerID, err := peer.Decode(publisherPeerID)
    if err != nil {
        return false, err
    }

    // Extract public key from Peer ID
    pubKey, err := peerID.ExtractPublicKey()
    if err != nil {
        return false, err
    }

    // Verify signature
    valid, err := pubKey.Verify(epmBytes, signature)
    if err != nil {
        return false, err
    }

    return valid, nil
}

func HandleReceivedEPM(epmBytes, signature []byte, peerID string) {
    valid, err := VerifyEPMSignature(epmBytes, signature, peerID)
    if err != nil {
        fmt.Println("Verification error:", err)
        return
    }

    if valid {
        fmt.Println("EPM signature verified - data is authentic")
    } else {
        fmt.Println("EPM signature invalid - data may be tampered")
    }
}
```

:::

### Chain of Trust

Trust can be established through signed endorsements:

```
┌─────────────────────────────────────────────────────────────────┐
│                      Trust Chain                                 │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│   Root Authority                                                 │
│   (Space Agency)                                                 │
│        │                                                         │
│        │ signs EPM of                                            │
│        ▼                                                         │
│   Regional Hub                                                   │
│   (National Operator)                                            │
│        │                                                         │
│        │ signs EPM of                                            │
│        ▼                                                         │
│   Satellite Operator                                             │
│   (Commercial Entity)                                            │
│        │                                                         │
│        │ signs data                                              │
│        ▼                                                         │
│   Published OMM/CDM/etc.                                         │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

::: info
Chain of trust is optional in SDN. Nodes can verify signatures using only the public key embedded in the Peer ID, without requiring a trust hierarchy.
:::

### Revoking Compromised Identities

If a private key is compromised:

1. **Generate a new key pair**
2. **Publish a revocation notice** signed with the old key (if still available)
3. **Publish new EPM** with the new key
4. **Notify trusted peers** out-of-band

```typescript
interface RevocationNotice {
  revokedPeerId: string;
  revokedPublicKey: string;
  reason: 'key_compromise' | 'key_rotation' | 'entity_terminated';
  timestamp: string;
  newPeerId?: string;          // If rotating to new key
  signatureWithOldKey?: string; // If old key still available
  signatureWithNewKey?: string; // Signed by replacement key
}

async function publishRevocation(
  node: SDNNode,
  oldKeyPair: any,
  reason: string
) {
  const revocation: RevocationNotice = {
    revokedPeerId: oldPeerId,
    revokedPublicKey: oldPublicKeyHex,
    reason: 'key_compromise',
    timestamp: new Date().toISOString(),
    newPeerId: node.peerId.toString(),
  };

  // Sign with old key if available
  if (oldKeyPair) {
    const oldSignature = await oldKeyPair.sign(
      Buffer.from(JSON.stringify(revocation))
    );
    revocation.signatureWithOldKey = Buffer.from(oldSignature).toString('hex');
  }

  // Sign with new key
  const newSignature = await node.privateKey.sign(
    Buffer.from(JSON.stringify(revocation))
  );
  revocation.signatureWithNewKey = Buffer.from(newSignature).toString('hex');

  // Broadcast to network
  await node.publish('revocation', revocation);
}
```

## Multi-Device Identity

### Using the Same Identity Across Devices

Organizations often need to use the same cryptographic identity across multiple systems.

::: warning
Sharing private keys between devices increases the attack surface. Consider using hierarchical key derivation instead.
:::

**Option 1: Direct Key Sharing (Simple but Less Secure)**

```typescript
// Export from primary device
const exportedKey = await exportPrivateKey(privateKey, 'strongpassword');

// Import on secondary device
const importedKey = await importPrivateKey(exportedKey, 'strongpassword');
```

**Option 2: Hierarchical Keys (Recommended)**

Each device gets a derived key that can be revoked independently:

```
                  Master Key
                      │
        ┌─────────────┼─────────────┐
        │             │             │
        ▼             ▼             ▼
   Device A       Device B      Device C
   (Server)       (Desktop)     (Mobile)
```

### Key Synchronization Approaches

| Approach | Pros | Cons |
|----------|------|------|
| Shared secret | Simple | Single point of failure |
| Key server | Centralized management | Requires trust in server |
| Hierarchical derivation | Independent revocation | More complex |
| Multi-sig | Distributed trust | Coordination overhead |

### Hierarchical Key Derivation

SDN supports BIP-32-style extended keys for hierarchical derivation:

```typescript
import { HDKey } from '@scure/bip32';

function deriveDeviceKey(masterSeed: Uint8Array, deviceIndex: number) {
  // Create HD key from seed
  const hdKey = HDKey.fromMasterSeed(masterSeed);

  // Derive path: m/44'/SDN'/account'/device'
  const path = `m/44'/2024'/0'/${deviceIndex}'`;
  const deviceKey = hdKey.derive(path);

  return {
    publicKey: deviceKey.publicKey,
    privateKey: deviceKey.privateKey,
    path,
  };
}

// Usage
const masterSeed = crypto.getRandomValues(new Uint8Array(32));
const serverKey = deriveDeviceKey(masterSeed, 0);
const desktopKey = deriveDeviceKey(masterSeed, 1);
const mobileKey = deriveDeviceKey(masterSeed, 2);
```

The EPM schema supports extended keys:

```typescript
// From EPM.fbs
const cryptoKey = {
  PUBLIC_KEY: publicKeyHex,
  XPUB: extendedPublicKey,    // BIP-32 extended public key
  KEY_TYPE: 'Signing',
};
```

## Identity Recovery

### Backup Strategies

::: danger CRITICAL
Without a backup, losing your private key means losing your identity permanently. There is no "forgot password" option in cryptographic systems.
:::

**Recommended Backup Strategy:**

1. **Encrypted file backup** - Store encrypted key in secure location
2. **Paper backup** - Print key as QR code or mnemonic phrase
3. **Split key backup** - Use Shamir's Secret Sharing

```typescript
import { split, combine } from 'shamir-secret-sharing';

async function createSplitBackup(privateKeyBytes: Uint8Array) {
  // Split into 5 shares, requiring 3 to recover
  const shares = await split(privateKeyBytes, 5, 3);

  // Distribute shares to trusted parties
  return shares.map((share, i) => ({
    shareIndex: i + 1,
    shareData: Buffer.from(share).toString('base64'),
  }));
}

async function recoverFromShares(shares: Uint8Array[]) {
  // Combine any 3 shares to recover the key
  const recovered = await combine(shares);
  return recovered;
}
```

### Recovery Procedures

**If you have a backup:**

1. Import the private key from backup
2. Verify the derived Peer ID matches expected value
3. Republish your EPM to announce presence

**If you've lost your key:**

1. Generate a new key pair
2. Publish a new EPM with the new identity
3. Request trusted peers to re-sign your new EPM
4. Update any systems that referenced your old Peer ID

### Key Rotation

Even without compromise, periodic key rotation is good security practice:

```typescript
async function rotateIdentity(
  node: SDNNode,
  rotationPeriodDays: number = 365
) {
  // Generate new key pair
  const newKeyPair = await generateKeyPair('Ed25519');
  const newPeerId = await peerIdFromKeys(
    newKeyPair.public.bytes,
    newKeyPair.bytes
  );

  // Create rotation announcement signed by old key
  const announcement = {
    type: 'key_rotation',
    oldPeerId: node.peerId.toString(),
    newPeerId: newPeerId.toString(),
    newPublicKey: Buffer.from(newKeyPair.public.bytes).toString('hex'),
    effectiveDate: new Date(
      Date.now() + 7 * 24 * 60 * 60 * 1000 // 7 days grace period
    ).toISOString(),
    rotationReason: 'scheduled_rotation',
  };

  // Sign with old key
  const signature = await node.privateKey.sign(
    Buffer.from(JSON.stringify(announcement))
  );

  // Publish rotation announcement
  await node.publish('key_rotation', {
    ...announcement,
    signature: Buffer.from(signature).toString('hex'),
  });

  console.log('Key rotation announced. New identity will be active in 7 days.');

  return { newKeyPair, newPeerId, announcement };
}
```

## Summary

Digital identity in SDN provides:

- **Cryptographic foundation** using Ed25519 key pairs
- **Human-readable profiles** through Entity Profile Manifest (EPM)
- **Content attribution** via Peer Network Manifest (PNM)
- **Verifiable authenticity** through digital signatures
- **Flexible trust models** supporting various organizational needs

::: tip Next Steps
- [Architecture](/guide/architecture) - Understand how identity fits into the network
- [Getting Started](/guide/getting-started) - Create your first SDN node
- [Schema Reference](/reference/schemas) - Explore EPM and PNM schemas in detail
:::
