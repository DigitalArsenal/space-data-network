# Crypto API

The Crypto API provides cryptographic operations including key generation, signing, verification, and encryption.

## Overview

```typescript
import { Crypto, generateKeyPair } from '@spacedatanetwork/sdn-js';
```

## Key Generation

### Ed25519 Keys

```typescript
import { generateKeyPair } from '@spacedatanetwork/sdn-js';

// Generate new key pair
const keyPair = await generateKeyPair();

console.log(keyPair.publicKey);  // Uint8Array (32 bytes)
console.log(keyPair.privateKey); // Uint8Array (64 bytes)
console.log(keyPair.peerId);     // libp2p Peer ID string
```

### From Seed Phrase (BIP-39)

```typescript
import { generateKeyPair, generateMnemonic } from '@spacedatanetwork/sdn-js';

// Generate mnemonic
const mnemonic = generateMnemonic();
// "abandon abandon abandon ... about"

// Derive keys from mnemonic
const keyPair = await generateKeyPair({ mnemonic });
```

### From Password

```typescript
import { generateKeyPair } from '@spacedatanetwork/sdn-js';

const keyPair = await generateKeyPair({
  password: 'your-secure-password',
  salt: 'optional-salt'
});
```

## Signing

### Sign Data

```typescript
import { sign } from '@spacedatanetwork/sdn-js';

const message = new TextEncoder().encode('Hello, Space!');
const signature = await sign(message, keyPair.privateKey);
```

### Sign Schema Data

```typescript
import { signData } from '@spacedatanetwork/sdn-js';

const signedOMM = await signData('OMM', ommData, keyPair.privateKey);

console.log(signedOMM.signature); // Uint8Array
console.log(signedOMM.publicKey); // Uint8Array
```

## Verification

### Verify Signature

```typescript
import { verify } from '@spacedatanetwork/sdn-js';

const isValid = await verify(message, signature, keyPair.publicKey);
```

### Verify Signed Data

```typescript
import { verifyData } from '@spacedatanetwork/sdn-js';

const result = await verifyData('OMM', signedOMM);

if (result.valid) {
  console.log('Signed by:', result.peerId);
} else {
  console.log('Invalid signature');
}
```

## Encryption

### AES-256-GCM

```typescript
import { encrypt, decrypt } from '@spacedatanetwork/sdn-js';

// Encrypt
const encrypted = await encrypt(plaintext, password);

// Decrypt
const decrypted = await decrypt(encrypted, password);
```

### X25519 Key Exchange

```typescript
import { deriveSharedSecret } from '@spacedatanetwork/sdn-js';

// Derive X25519 keys from Ed25519
const x25519KeyPair = await deriveX25519(ed25519KeyPair);

// Derive shared secret
const sharedSecret = await deriveSharedSecret(
  x25519KeyPair.privateKey,
  recipientPublicKey
);
```

## HD Wallet Derivation (BIP-32/44)

### Derive Child Keys

```typescript
import { deriveKey } from '@spacedatanetwork/sdn-js';

// BIP-44 path: m/44'/501'/0'/0/0
const childKey = await deriveKey(masterKey, "m/44'/501'/0'/0/0");
```

### Blockchain Addresses

```typescript
import { deriveAddress } from '@spacedatanetwork/sdn-js';

// Derive Ethereum address
const ethAddress = await deriveAddress(keyPair, 'ethereum');

// Derive Solana address
const solAddress = await deriveAddress(keyPair, 'solana');

// Derive Bitcoin address
const btcAddress = await deriveAddress(keyPair, 'bitcoin');
```

## Hashing

### SHA-256

```typescript
import { hash } from '@spacedatanetwork/sdn-js';

const digest = await hash(data, 'sha256');
```

### Blake2b

```typescript
const digest = await hash(data, 'blake2b-256');
```

## Utilities

### Encode/Decode

```typescript
import {
  base58Encode,
  base58Decode,
  base64Encode,
  base64Decode,
  hexEncode,
  hexDecode
} from '@spacedatanetwork/sdn-js';

// Base58
const b58 = base58Encode(bytes);
const bytes = base58Decode(b58);

// Base64
const b64 = base64Encode(bytes);
const bytes = base64Decode(b64);

// Hex
const hex = hexEncode(bytes);
const bytes = hexDecode(hex);
```

### Random Bytes

```typescript
import { randomBytes } from '@spacedatanetwork/sdn-js';

const nonce = await randomBytes(24);
const salt = await randomBytes(16);
```

## Key Storage

### Export Keys

```typescript
import { exportKey } from '@spacedatanetwork/sdn-js';

// Export as PEM
const pem = await exportKey(keyPair.privateKey, 'pem');

// Export encrypted
const encrypted = await exportKey(keyPair.privateKey, 'encrypted', password);
```

### Import Keys

```typescript
import { importKey } from '@spacedatanetwork/sdn-js';

// Import from PEM
const privateKey = await importKey(pem, 'pem');

// Import encrypted
const privateKey = await importKey(encrypted, 'encrypted', password);
```

## WASM Module

The crypto operations use a WebAssembly module for performance and security.

```typescript
import { loadCrypto } from '@spacedatanetwork/sdn-js';

// Preload WASM module
await loadCrypto();

// Check if loaded
import { isCryptoReady } from '@spacedatanetwork/sdn-js';
console.log(isCryptoReady()); // true
```

## See Also

- [Digital Identity Guide](/guide/digital-identity)
- [Encryption at Rest](/guide/encryption-at-rest)
- [SDNNode API](/api/js-node)
