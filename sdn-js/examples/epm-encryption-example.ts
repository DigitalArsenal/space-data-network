/**
 * EPM-based ECIES Encryption Example
 *
 * This example demonstrates the full encryption flow:
 * 1. EPM lookup to get recipient's encryption public key
 * 2. ECIES encryption using EncryptionContext
 * 3. Transmitting encrypted data with header
 * 4. Decryption on recipient side
 *
 * Supports multiple key exchange algorithms:
 * - X25519 (default, recommended for new systems)
 * - secp256k1 (Bitcoin/Ethereum compatible)
 * - P-256/secp256r1 (NIST, government/enterprise)
 */

import { EPMResolver, KeyType, createEPMResolver } from '../src/epm-resolver';

// In production, import from the flatbuffers package:
// import { EncryptionContext, initEncryption } from 'flatbuffers-encryption';

// Mock types for this example (replace with actual imports)
interface EncryptionHeader {
  version: number;
  algorithm: string;
  senderPublicKey: Uint8Array;
  recipientKeyId: Uint8Array;
  iv: Uint8Array;
}

// =============================================================================
// Sender Side - Encrypting data for a recipient
// =============================================================================

async function encryptForRecipient(
  resolver: EPMResolver,
  recipientPeerID: string,
  plaintext: Uint8Array
): Promise<{ header: EncryptionHeader; ciphertext: Uint8Array }> {
  // Step 1: Look up recipient's encryption public key from their EPM
  const encryptionKey = await resolver.getEncryptionKey(recipientPeerID);

  if (!encryptionKey) {
    throw new Error(`No encryption key found for peer: ${recipientPeerID}`);
  }

  console.log(`Found ${encryptionKey.algorithm} encryption key for ${recipientPeerID}`);

  // Step 2: Create encryption context using ECIES
  // The EncryptionContext will:
  // - Generate an ephemeral key pair
  // - Perform ECDH with recipient's public key
  // - Derive AES-256 key using HKDF
  // - Generate random IV

  // In production:
  // const ctx = EncryptionContext.forEncryption(encryptionKey.publicKey, {
  //   algorithm: encryptionKey.algorithm,
  //   context: 'sdn-v1',
  // });

  // For this example, simulate the encryption:
  const header: EncryptionHeader = {
    version: 1,
    algorithm: encryptionKey.algorithm || 'x25519',
    senderPublicKey: new Uint8Array(32), // Ephemeral public key
    recipientKeyId: new Uint8Array(8),
    iv: crypto.getRandomValues(new Uint8Array(16)),
  };

  // const ciphertext = ctx.encryptBuffer(plaintext);
  const ciphertext = plaintext; // Simulated

  // Step 3: Get the header containing ephemeral public key
  // const header = ctx.getHeader();

  // Step 4: Clean up sensitive material
  // ctx.destroy();

  return { header, ciphertext };
}

// =============================================================================
// Recipient Side - Decrypting received data
// =============================================================================

async function decryptFromSender(
  recipientPrivateKey: Uint8Array,
  header: EncryptionHeader,
  ciphertext: Uint8Array
): Promise<Uint8Array> {
  // Step 1: Parse the header to get ephemeral public key and algorithm

  console.log(`Decrypting with algorithm: ${header.algorithm}`);

  // Step 2: Create decryption context
  // The EncryptionContext will:
  // - Perform ECDH with sender's ephemeral public key
  // - Derive the same AES-256 key using HKDF
  // - Use the IV from header

  // In production:
  // const ctx = EncryptionContext.forDecryption(recipientPrivateKey, header);
  // const plaintext = ctx.decryptBuffer(ciphertext);
  // ctx.destroy();

  // For this example, simulate decryption:
  const plaintext = ciphertext;

  return plaintext;
}

// =============================================================================
// Full Example Flow
// =============================================================================

async function main() {
  // Create EPM resolver
  const resolver = createEPMResolver({
    cacheTTL: 5 * 60 * 1000, // 5 minutes
  });

  // Simulate receiving an EPM announcement (normally via PubSub PNM)
  // In production, EPMs are discovered via:
  // 1. PubSub PNM announcements
  // 2. IPNS resolution
  // 3. Direct peer exchange

  // Add Alice's EPM (she supports X25519)
  resolver.addParsedEPM('alice-peer-id', {
    peerID: 'alice-peer-id',
    dn: 'CN=Alice, O=Space Corp',
    legalName: 'Space Corp',
    email: 'alice@spacecorp.com',
    keys: [
      {
        publicKey: '0x' + 'a'.repeat(64), // 32-byte X25519 public key (hex)
        keyType: KeyType.Signing,
        algorithm: 'x25519', // Ed25519 for signing (derived from same seed)
      },
      {
        publicKey: '0x' + 'b'.repeat(64), // 32-byte X25519 public key
        keyType: KeyType.Encryption,
        algorithm: 'x25519',
      },
    ],
    multiformatAddresses: [
      '/ip4/192.168.1.100/tcp/4001/p2p/alice-peer-id',
    ],
  });

  // Add Bob's EPM (he uses secp256k1 for Ethereum compatibility)
  resolver.addParsedEPM('bob-peer-id', {
    peerID: 'bob-peer-id',
    dn: 'CN=Bob, O=DeFi Labs',
    legalName: 'DeFi Labs',
    email: 'bob@defilabs.io',
    keys: [
      {
        publicKey: '0x' + 'c'.repeat(66), // 33-byte compressed secp256k1
        keyType: KeyType.Signing,
        algorithm: 'secp256k1',
        addressType: 'ethereum',
      },
      {
        publicKey: '0x' + 'd'.repeat(66), // 33-byte compressed secp256k1
        keyType: KeyType.Encryption,
        algorithm: 'secp256k1',
        addressType: 'ethereum',
      },
    ],
    multiformatAddresses: [
      '/ip4/10.0.0.50/tcp/4001/p2p/bob-peer-id',
    ],
  });

  // Add Carol's EPM (she uses P-256 for government compliance)
  resolver.addParsedEPM('carol-peer-id', {
    peerID: 'carol-peer-id',
    dn: 'CN=Carol, O=Gov Agency',
    legalName: 'Government Agency',
    email: 'carol@gov.agency',
    keys: [
      {
        publicKey: '0x' + 'e'.repeat(66), // 33-byte compressed P-256
        keyType: KeyType.Signing,
        algorithm: 'p256',
        addressType: 'p256-nist',
      },
      {
        publicKey: '0x' + 'f'.repeat(66), // 33-byte compressed P-256
        keyType: KeyType.Encryption,
        algorithm: 'p256',
        addressType: 'p256-nist',
      },
    ],
    multiformatAddresses: [
      '/ip4/172.16.0.1/tcp/4001/p2p/carol-peer-id',
    ],
  });

  // Example: Encrypt a FlatBuffer message for Alice
  console.log('\n=== Encrypting for Alice (X25519) ===');
  const messageForAlice = new TextEncoder().encode('Hello Alice! This is encrypted.');
  const { header: aliceHeader, ciphertext: aliceCiphertext } = await encryptForRecipient(
    resolver,
    'alice-peer-id',
    messageForAlice
  );
  console.log('Header algorithm:', aliceHeader.algorithm);
  console.log('Ciphertext length:', aliceCiphertext.length);

  // Example: Encrypt for Bob (secp256k1)
  console.log('\n=== Encrypting for Bob (secp256k1) ===');
  const messageForBob = new TextEncoder().encode('Hello Bob! Ethereum-compatible encryption.');
  const { header: bobHeader } = await encryptForRecipient(
    resolver,
    'bob-peer-id',
    messageForBob
  );
  console.log('Header algorithm:', bobHeader.algorithm);

  // Example: Encrypt for Carol (P-256)
  console.log('\n=== Encrypting for Carol (P-256) ===');
  const messageForCarol = new TextEncoder().encode('Hello Carol! NIST-compliant encryption.');
  const { header: carolHeader } = await encryptForRecipient(
    resolver,
    'carol-peer-id',
    messageForCarol
  );
  console.log('Header algorithm:', carolHeader.algorithm);

  // Get cache stats
  console.log('\n=== Cache Stats ===');
  console.log(resolver.getCacheStats());

  // Example: Extract specific key types
  console.log('\n=== Key Extraction Examples ===');

  const aliceSigningKey = await resolver.getSigningKey('alice-peer-id');
  console.log('Alice signing key algorithm:', aliceSigningKey?.algorithm);

  const bobEncryptionKey = await resolver.getEncryptionKey('bob-peer-id', 'secp256k1');
  console.log('Bob encryption key algorithm:', bobEncryptionKey?.algorithm);

  // Prefer X25519 but fall back to whatever is available
  const carolKey = await resolver.getEncryptionKey('carol-peer-id');
  console.log('Carol encryption key algorithm:', carolKey?.algorithm);
}

// Run the example
main().catch(console.error);
