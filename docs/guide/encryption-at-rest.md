# Encryption at Rest

Space Data Network provides comprehensive encryption at rest capabilities through its WASM-based encryption module, ensuring that sensitive space data remains protected when stored on disk or in databases.

## Why Encryption at Rest Matters

### Protecting Sensitive Space Data

Space operations data often contains sensitive information:

- **Orbital parameters** that could reveal satellite capabilities
- **Conjunction data** for collision avoidance decisions
- **Entity profiles** with organizational contact information
- **Proprietary observation data** from commercial operators

Encryption at rest ensures this data remains confidential even if storage media is compromised.

### Regulatory Compliance

Many space operations must comply with data protection regulations:

- **ITAR** (International Traffic in Arms Regulations)
- **EAR** (Export Administration Regulations)
- **GDPR** for European operator data
- **National space agency requirements**

Encryption at rest is often a baseline requirement for compliance.

### Defense Against Physical Attacks

Encryption protects against:

- **Stolen devices** - Laptops or servers with local SDN data
- **Decommissioned hardware** - Old drives with residual data
- **Physical access** - Unauthorized access to data centers
- **Forensic recovery** - Attempts to recover deleted data

## SDN Encryption Architecture

SDN's encryption architecture integrates at multiple layers:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        DATA ENCRYPTION FLOW                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   Application Data                                                          │
│         │                                                                   │
│         ▼                                                                   │
│   ┌─────────────┐                                                           │
│   │ FlatBuffers │  Serialize to binary format                              │
│   │ (Schema)    │                                                           │
│   └──────┬──────┘                                                           │
│          │                                                                   │
│          ▼                                                                   │
│   ┌─────────────┐     ┌─────────────┐                                       │
│   │ flatc-wasm  │◄────│ Argon2id    │  Derive key from password            │
│   │ (Encrypt)   │     │ (KDF)       │                                       │
│   └──────┬──────┘     └─────────────┘                                       │
│          │                                                                   │
│          ▼                                                                   │
│   ┌─────────────┐                                                           │
│   │ AES-256-GCM │  Authenticated encryption                                │
│   │ Ciphertext  │                                                           │
│   └──────┬──────┘                                                           │
│          │                                                                   │
│          ▼                                                                   │
│   ┌─────────────┐     ┌─────────────┐                                       │
│   │  FlatSQL    │  or │  IndexedDB  │  Encrypted storage                   │
│   │  (SQLite)   │     │  (Browser)  │                                       │
│   └─────────────┘     └─────────────┘                                       │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### FlatBuffers as the Data Layer

All SDN data uses [FlatBuffers](https://flatbuffers.dev/) for serialization:

- **Zero-copy access** - Efficient data reading without parsing overhead
- **Schema validation** - Data integrity before encryption
- **Compact binary format** - Smaller ciphertext size
- **Cross-platform** - Works identically in Go, TypeScript, and browsers

### WASM Encryption Module

The `flatc-wasm` module provides cryptographic operations in a sandboxed WebAssembly environment:

- **Portable** - Same code runs on servers, desktops, and browsers
- **Secure** - Memory isolation prevents key leakage
- **Fast** - Near-native performance for crypto operations
- **Auditable** - Single codebase for all platforms

## WASM Encryption Module

### flatc-wasm Capabilities

The `flatc-wasm` module exports the following encryption functions:

| Function | Purpose |
|----------|---------|
| `wasi_encrypt_bytes` | Encrypt arbitrary byte data |
| `wasi_decrypt_bytes` | Decrypt encrypted data |
| `wasi_ed25519_sign` | Sign data with Ed25519 |
| `wasi_ed25519_verify` | Verify Ed25519 signatures |

### Core Functions

#### wasi_encrypt_bytes

Encrypts plaintext using AES-256-GCM:

```
wasi_encrypt_bytes(
    key_ptr: u32,      // Pointer to 32-byte key
    key_len: u32,      // Key length (32)
    data_ptr: u32,     // Pointer to plaintext
    data_len: u32,     // Plaintext length
    out_ptr: u32,      // Pointer to output buffer
    out_len: u32       // Output buffer size
) -> u32              // Returns ciphertext length
```

**Output format:**
```
[12 bytes nonce][N bytes ciphertext][16 bytes auth tag]
```

#### wasi_decrypt_bytes

Decrypts ciphertext and verifies authentication tag:

```
wasi_decrypt_bytes(
    key_ptr: u32,      // Pointer to 32-byte key
    key_len: u32,      // Key length (32)
    data_ptr: u32,     // Pointer to ciphertext
    data_len: u32,     // Ciphertext length
    out_ptr: u32,      // Pointer to output buffer
    out_len: u32       // Output buffer size
) -> u32              // Returns plaintext length (0 on failure)
```

### AES-256-GCM Encryption

SDN uses AES-256-GCM as the primary encryption algorithm:

- **256-bit key** - Quantum-resistant key size
- **Galois/Counter Mode** - Authenticated encryption
- **12-byte nonce** - Randomly generated per encryption
- **16-byte auth tag** - Integrity verification

::: info Why AES-256-GCM?
AES-256-GCM is the industry standard for authenticated encryption, recommended by NIST and used in TLS 1.3. It provides both confidentiality and integrity in a single operation.
:::

### XChaCha20-Poly1305 Option

For scenarios requiring extended nonces or additional security margins:

```
XChaCha20-Poly1305:
- 256-bit key
- 24-byte nonce (vs 12 for AES-GCM)
- 16-byte auth tag
- Resistant to nonce misuse
```

XChaCha20-Poly1305 is used internally for the encrypted edge relay registry.

## Key Derivation

### Argon2id for Password-Based Key Derivation

SDN uses Argon2id, the winner of the Password Hashing Competition, for deriving encryption keys from passwords:

::: tip Why Argon2id?
Argon2id combines the side-channel resistance of Argon2i with the GPU-resistance of Argon2d, making it the recommended variant for most use cases.
:::

### Parameters

| Parameter | Default Value | Description |
|-----------|---------------|-------------|
| `memory` | 64 MB (65536 KiB) | Memory cost in KiB |
| `iterations` | 1 | Time cost (passes) |
| `parallelism` | 4 | Degree of parallelism |
| `keyLength` | 32 bytes | Output key length |

These parameters balance security and performance for typical deployments. For high-security scenarios, increase memory and iterations.

### Salt Handling

Salts must be:

- **Unique** - Different for each password/key derivation
- **Random** - Generated using cryptographically secure RNG
- **Stored** - Saved alongside encrypted data for decryption
- **16+ bytes** - Minimum 128 bits for security

::: warning Salt Storage
Never derive the salt from the password. Always generate it randomly and store it with the ciphertext.
:::

### Example: Deriving Keys from Passwords

::: code-group

```typescript [TypeScript]
import { argon2id } from '@noble/hashes/argon2';
import { randomBytes } from '@noble/hashes/utils';

interface DerivedKey {
  key: Uint8Array;
  salt: Uint8Array;
}

/**
 * Derive an encryption key from a password using Argon2id
 */
function deriveKey(password: string, existingSalt?: Uint8Array): DerivedKey {
  // Generate or use existing salt
  const salt = existingSalt ?? randomBytes(16);

  // Derive 32-byte key using Argon2id
  const key = argon2id(password, salt, {
    t: 1,           // iterations
    m: 65536,       // memory in KiB (64 MB)
    p: 4,           // parallelism
    dkLen: 32,      // key length
  });

  return { key, salt };
}

// Usage: Creating a new encrypted record
const { key, salt } = deriveKey('user-password');
const ciphertext = await encryptData(key, plaintextData);
// Store: salt + ciphertext

// Usage: Decrypting an existing record
const storedSalt = readSaltFromStorage();
const { key: decryptKey } = deriveKey('user-password', storedSalt);
const plaintext = await decryptData(decryptKey, ciphertext);
```

```go [Go]
package main

import (
    "crypto/rand"
    "golang.org/x/crypto/argon2"
)

// DerivedKey contains the key and salt
type DerivedKey struct {
    Key  []byte
    Salt []byte
}

// DeriveKey derives an encryption key from a password using Argon2id
func DeriveKey(password string, existingSalt []byte) (*DerivedKey, error) {
    var salt []byte

    if existingSalt != nil {
        salt = existingSalt
    } else {
        // Generate new random salt
        salt = make([]byte, 16)
        if _, err := rand.Read(salt); err != nil {
            return nil, err
        }
    }

    // Derive 32-byte key using Argon2id
    // Parameters: password, salt, iterations, memory (KB), parallelism, key length
    key := argon2.IDKey(
        []byte(password),
        salt,
        1,           // iterations
        64*1024,     // memory in KiB (64 MB)
        4,           // parallelism
        32,          // key length
    )

    return &DerivedKey{Key: key, Salt: salt}, nil
}
```

:::

## SQLite Storage Encryption

### Integration with FlatSQL

FlatSQL extends SQLite with FlatBuffer-aware storage. When combined with encryption:

```
┌────────────────────────────────────────────────────────────┐
│                    ENCRYPTED FLATSQL                        │
├────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐ │
│  │  FlatBuffer  │───►│  Encrypt     │───►│   SQLite     │ │
│  │  Binary Data │    │  (AES-GCM)   │    │   BLOB       │ │
│  └──────────────┘    └──────────────┘    └──────────────┘ │
│                                                             │
│  Table Schema:                                              │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ CREATE TABLE encrypted_data (                        │   │
│  │     cid          TEXT PRIMARY KEY,                   │   │
│  │     peer_id      TEXT,                               │   │
│  │     timestamp    INTEGER,                            │   │
│  │     salt         BLOB,        -- 16 bytes           │   │
│  │     data         BLOB,        -- encrypted          │   │
│  │     signature    BLOB         -- Ed25519 sig        │   │
│  │ );                                                   │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
└────────────────────────────────────────────────────────────┘
```

### Encrypted Database Files

For full database encryption, SDN supports SQLCipher-compatible encryption:

::: code-group

```go [Go - Server]
import (
    "database/sql"
    _ "github.com/mutecomm/go-sqlcipher/v4"
)

func OpenEncryptedDB(path, passphrase string) (*sql.DB, error) {
    // Connection string with encryption key
    dsn := fmt.Sprintf("%s?_pragma_key=%s&_pragma_cipher_page_size=4096",
        path,
        url.QueryEscape(passphrase),
    )

    db, err := sql.Open("sqlite3", dsn)
    if err != nil {
        return nil, err
    }

    // Verify encryption is working
    var result string
    err = db.QueryRow("PRAGMA cipher_version").Scan(&result)
    if err != nil {
        db.Close()
        return nil, fmt.Errorf("database not encrypted: %w", err)
    }

    return db, nil
}
```

```typescript [TypeScript - Browser]
import { openDB, DBSchema } from 'idb';

interface EncryptedStore extends DBSchema {
  data: {
    key: string;           // CID
    value: {
      cid: string;
      peerId: string;
      timestamp: number;
      salt: Uint8Array;
      encryptedData: Uint8Array;
      signature: Uint8Array;
    };
  };
}

async function openEncryptedStore() {
  return openDB<EncryptedStore>('sdn-encrypted', 1, {
    upgrade(db) {
      const store = db.createObjectStore('data', { keyPath: 'cid' });
      store.createIndex('peerId', 'peerId');
      store.createIndex('timestamp', 'timestamp');
    },
  });
}
```

:::

### Key Management for Databases

Database encryption keys should be managed separately from application data:

```
┌─────────────────────────────────────────────────────────────┐
│                   KEY MANAGEMENT HIERARCHY                   │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  Master Key (User Password)                                  │
│         │                                                    │
│         ▼  Argon2id                                          │
│  ┌─────────────────┐                                         │
│  │ Key Encryption  │ ─── Stored in secure enclave           │
│  │ Key (KEK)       │     or hardware security module         │
│  └────────┬────────┘                                         │
│           │                                                  │
│           ▼  AES-256-GCM                                     │
│  ┌─────────────────┐                                         │
│  │ Data Encryption │ ─── Encrypted, stored in database       │
│  │ Key (DEK)       │     header or separate key file         │
│  └────────┬────────┘                                         │
│           │                                                  │
│           ▼  AES-256-GCM                                     │
│  ┌─────────────────┐                                         │
│  │ Encrypted Data  │ ─── Stored in database tables           │
│  └─────────────────┘                                         │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

## Implementation Examples

### Encrypting FlatBuffer Data

::: code-group

```typescript [TypeScript]
import { SDNNode } from '@spacedatanetwork/sdn-js';
import { argon2id } from '@noble/hashes/argon2';
import { randomBytes, bytesToHex } from '@noble/hashes/utils';

interface EncryptedRecord {
  cid: string;
  salt: Uint8Array;
  nonce: Uint8Array;
  ciphertext: Uint8Array;
  tag: Uint8Array;
}

class EncryptedStorage {
  private node: SDNNode;
  private kek: Uint8Array | null = null;

  constructor(node: SDNNode) {
    this.node = node;
  }

  /**
   * Initialize encryption with a password
   */
  async unlock(password: string, salt?: Uint8Array): Promise<Uint8Array> {
    const keySalt = salt ?? randomBytes(16);

    this.kek = argon2id(password, keySalt, {
      t: 1,
      m: 65536,
      p: 4,
      dkLen: 32,
    });

    return keySalt;
  }

  /**
   * Encrypt and store FlatBuffer data
   */
  async storeEncrypted(
    schemaName: string,
    data: object
  ): Promise<EncryptedRecord> {
    if (!this.kek) {
      throw new Error('Storage not unlocked');
    }

    // Serialize to FlatBuffer binary
    const flatbufferBinary = await this.node.serialize(schemaName, data);

    // Encrypt using WASM module
    const encrypted = await this.node.encrypt(this.kek, flatbufferBinary);

    // Compute CID for content addressing
    const cid = await this.node.computeCID(encrypted.ciphertext);

    return {
      cid,
      salt: encrypted.salt,
      nonce: encrypted.nonce,
      ciphertext: encrypted.ciphertext,
      tag: encrypted.tag,
    };
  }

  /**
   * Lock storage (clear key from memory)
   */
  lock(): void {
    if (this.kek) {
      // Overwrite key material before clearing
      this.kek.fill(0);
      this.kek = null;
    }
  }
}

// Usage example
async function example() {
  const node = await SDNNode.create();
  const storage = new EncryptedStorage(node);

  // Unlock with password
  const salt = await storage.unlock('my-secure-password');

  // Store encrypted OMM data
  const ommData = {
    OBJECT_NAME: 'ISS (ZARYA)',
    OBJECT_ID: '1998-067A',
    EPOCH: '2024-01-15T12:00:00.000Z',
    MEAN_MOTION: 15.49,
    ECCENTRICITY: 0.0001,
    INCLINATION: 51.64,
    // ... additional orbital elements
  };

  const record = await storage.storeEncrypted('OMM', ommData);
  console.log('Stored encrypted record:', record.cid);

  // Lock when done
  storage.lock();
}
```

```go [Go]
package main

import (
    "context"
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "fmt"

    "golang.org/x/crypto/argon2"
    "github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// EncryptedRecord represents stored encrypted data
type EncryptedRecord struct {
    CID        string
    Salt       []byte
    Ciphertext []byte
}

// EncryptedStorage handles encrypted data operations
type EncryptedStorage struct {
    flatc *wasm.FlatcModule
    kek   []byte
}

// NewEncryptedStorage creates a new encrypted storage handler
func NewEncryptedStorage(flatc *wasm.FlatcModule) *EncryptedStorage {
    return &EncryptedStorage{flatc: flatc}
}

// Unlock initializes encryption with a password
func (s *EncryptedStorage) Unlock(password string, existingSalt []byte) ([]byte, error) {
    var salt []byte
    if existingSalt != nil {
        salt = existingSalt
    } else {
        salt = make([]byte, 16)
        if _, err := rand.Read(salt); err != nil {
            return nil, err
        }
    }

    // Derive key using Argon2id
    s.kek = argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)

    return salt, nil
}

// EncryptData encrypts FlatBuffer binary data
func (s *EncryptedStorage) EncryptData(ctx context.Context, data []byte) (*EncryptedRecord, error) {
    if s.kek == nil {
        return nil, fmt.Errorf("storage not unlocked")
    }

    // Use WASM module for encryption
    ciphertext, err := s.flatc.Encrypt(ctx, s.kek, data)
    if err != nil {
        return nil, fmt.Errorf("encryption failed: %w", err)
    }

    // Compute CID
    cid := computeCID(ciphertext)

    return &EncryptedRecord{
        CID:        cid,
        Ciphertext: ciphertext,
    }, nil
}

// DecryptData decrypts stored data
func (s *EncryptedStorage) DecryptData(ctx context.Context, record *EncryptedRecord) ([]byte, error) {
    if s.kek == nil {
        return nil, fmt.Errorf("storage not unlocked")
    }

    return s.flatc.Decrypt(ctx, s.kek, record.Ciphertext)
}

// Lock clears the key from memory
func (s *EncryptedStorage) Lock() {
    if s.kek != nil {
        // Overwrite key material
        for i := range s.kek {
            s.kek[i] = 0
        }
        s.kek = nil
    }
}
```

:::

### Decrypting on Retrieval

::: code-group

```typescript [TypeScript]
async function retrieveDecrypted(
  storage: EncryptedStorage,
  db: IDBDatabase,
  cid: string
): Promise<object | null> {
  // Retrieve encrypted record from IndexedDB
  const tx = db.transaction('data', 'readonly');
  const store = tx.objectStore('data');
  const record = await store.get(cid);

  if (!record) {
    return null;
  }

  // Decrypt using the unlocked key
  const plaintext = await storage.decrypt(record.ciphertext);

  // Deserialize FlatBuffer to object
  return storage.node.deserialize(record.schemaName, plaintext);
}
```

```go [Go]
func (s *EncryptedStorage) RetrieveDecrypted(
    ctx context.Context,
    store *storage.FlatSQLStore,
    cid string,
) ([]byte, error) {
    // Retrieve encrypted record from database
    record, err := store.GetByCID(ctx, cid)
    if err != nil {
        return nil, err
    }

    // Decrypt
    plaintext, err := s.DecryptData(ctx, &EncryptedRecord{
        CID:        record.CID,
        Ciphertext: record.Data,
    })
    if err != nil {
        return nil, fmt.Errorf("decryption failed: %w", err)
    }

    return plaintext, nil
}
```

:::

### Key Rotation Strategies

Key rotation is essential for long-term security:

```typescript
interface KeyRotationConfig {
  rotationIntervalDays: number;
  keepOldVersions: number;
  notifyBeforeDays: number;
}

class KeyRotationManager {
  private config: KeyRotationConfig;
  private storage: EncryptedStorage;

  constructor(storage: EncryptedStorage, config: KeyRotationConfig) {
    this.storage = storage;
    this.config = config;
  }

  /**
   * Rotate encryption keys
   * Re-encrypts all data with a new key derived from the same password
   */
  async rotateKeys(password: string): Promise<void> {
    // Generate new salt for new key version
    const newSalt = randomBytes(16);
    const newVersion = Date.now();

    // Derive new key
    const newKey = argon2id(password, newSalt, {
      t: 1,
      m: 65536,
      p: 4,
      dkLen: 32,
    });

    // Re-encrypt all records
    const records = await this.storage.getAllRecords();

    for (const record of records) {
      // Decrypt with old key
      const plaintext = await this.storage.decrypt(record.ciphertext);

      // Re-encrypt with new key
      const newCiphertext = await encrypt(newKey, plaintext);

      // Update record
      await this.storage.updateRecord(record.cid, {
        ciphertext: newCiphertext,
        keyVersion: newVersion,
        salt: newSalt,
      });
    }

    // Archive old key version (for recovery)
    await this.archiveKeyVersion(record.keyVersion);

    // Clear old key from memory
    this.storage.lock();

    // Unlock with new key
    await this.storage.unlock(password, newSalt);
  }
}
```

## Best Practices

### Key Storage Recommendations

::: danger Never Store Keys in Code
Never embed encryption keys directly in source code, configuration files committed to version control, or client-side JavaScript.
:::

**Recommended key storage approaches:**

| Environment | Recommendation |
|-------------|----------------|
| **Server** | Hardware Security Module (HSM) or encrypted key file with restricted permissions |
| **Desktop** | OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service) |
| **Browser** | Derive from user password; never persist raw keys to localStorage |
| **Mobile** | Secure Enclave (iOS) or Android Keystore |

### Memory Protection

Protect keys in memory during operation:

```typescript
class SecureKey {
  private key: Uint8Array | null = null;

  set(keyBytes: Uint8Array): void {
    // Copy to new buffer (avoid external references)
    this.key = new Uint8Array(keyBytes);
  }

  use<T>(operation: (key: Uint8Array) => T): T {
    if (!this.key) {
      throw new Error('Key not set');
    }
    return operation(this.key);
  }

  clear(): void {
    if (this.key) {
      // Overwrite with zeros
      this.key.fill(0);
      // Overwrite with random data
      crypto.getRandomValues(this.key);
      // Clear reference
      this.key = null;
    }
  }
}
```

### Secure Key Destruction

Always securely destroy keys when no longer needed:

::: code-group

```typescript [TypeScript]
function secureZero(buffer: Uint8Array): void {
  // Multiple overwrites for defense in depth
  buffer.fill(0);
  crypto.getRandomValues(buffer);
  buffer.fill(0);
}

// Usage
const key = deriveKey(password, salt);
try {
  await encryptData(key, data);
} finally {
  secureZero(key);
}
```

```go [Go]
import "crypto/subtle"

func secureZero(buf []byte) {
    // Use constant-time operation to prevent optimization removal
    for i := range buf {
        buf[i] = 0
    }
    // Additional pass with subtle to prevent compiler optimization
    subtle.ConstantTimeCopy(1, buf, make([]byte, len(buf)))
}

// Usage
key, _ := DeriveKey(password, salt)
defer secureZero(key.Key)

ciphertext, err := Encrypt(key.Key, plaintext)
```

:::

### Additional Security Measures

1. **Use authenticated encryption** - Always use AES-GCM or ChaCha20-Poly1305, never unauthenticated modes

2. **Validate before decryption** - Verify signatures and checksums before attempting decryption

3. **Rate limit key attempts** - Protect against brute-force attacks on password-derived keys

4. **Log encryption events** - Maintain audit logs of encryption/decryption operations (without logging keys)

5. **Regular key rotation** - Rotate encryption keys periodically, especially after suspected compromise

## Next Steps

- [Security & Transport Encryption](/guide/security-encryption) - Noise protocol and TLS
- [Digital Identity](/guide/digital-identity) - Ed25519 keys and EPM
- [Architecture Overview](/guide/architecture) - Full system design
