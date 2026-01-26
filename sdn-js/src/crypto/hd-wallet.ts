/**
 * HD Wallet WASM Wrapper for sdn-js
 *
 * Provides BIP-39 mnemonic generation, SLIP-10 key derivation,
 * Ed25519 signing, and X25519 encryption using hd-wallet-wasm.
 *
 * This module replaces flatc-crypto.wasm with backward-compatible APIs
 * plus new HD wallet functionality.
 */

import {
  HDWalletWasmExports,
  HDWalletModule,
  HDWalletOptions,
  MnemonicOptions,
  DerivedKey,
  KeyPair,
  DerivedIdentity,
  LanguageCode,
  ErrorCode,
  buildSigningPath,
  buildEncryptionPath,
} from './types';

// Module state
let hdWalletModule: HDWalletModule | null = null;
let moduleReady: Promise<void> | null = null;
let entropyInjected = false;

// WASM paths to try
const WASM_PATHS = [
  './hd-wallet.wasm',
  '/hd-wallet.wasm',
  '../wasm/hd-wallet.wasm',
  'https://cdn.spacedatanetwork.org/hd-wallet.wasm',
];

/**
 * Verify WASM integrity using SRI hash
 */
async function verifySri(data: ArrayBuffer, expectedSri: string): Promise<boolean> {
  try {
    const match = expectedSri.match(/^sha(256|384|512)-(.+)$/);
    if (!match) {
      console.warn('Invalid SRI format:', expectedSri);
      return false;
    }

    const algorithm = `SHA-${match[1]}`;
    const expectedHash = match[2];

    const hashBuffer = await crypto.subtle.digest(algorithm, data);
    const hashArray = new Uint8Array(hashBuffer);
    const actualHash = btoa(String.fromCharCode(...hashArray));

    if (actualHash !== expectedHash) {
      console.error('HD Wallet WASM integrity check failed: hash mismatch');
      return false;
    }

    return true;
  } catch (err) {
    console.error('SRI verification error:', err);
    return false;
  }
}

/**
 * Fetch SRI hash for WASM file
 */
async function fetchSriHash(wasmPath: string): Promise<string | null> {
  try {
    const sriPath = wasmPath + '.sri';
    const response = await fetch(sriPath);
    if (!response.ok) return null;
    return (await response.text()).trim();
  } catch {
    return null;
  }
}

/**
 * Create module wrapper from WASM exports
 */
function createModuleWrapper(exports: HDWalletWasmExports): HDWalletModule {
  return {
    ready: Promise.resolve(),
    exports,
    get heap() {
      return new Uint8Array(exports.memory.buffer);
    },
    alloc(size: number): number {
      return exports.hd_alloc(size);
    },
    dealloc(ptr: number): void {
      exports.hd_dealloc(ptr);
    },
    writeBytes(ptr: number, data: Uint8Array): void {
      new Uint8Array(exports.memory.buffer).set(data, ptr);
    },
    readBytes(ptr: number, length: number): Uint8Array {
      const result = new Uint8Array(length);
      result.set(new Uint8Array(exports.memory.buffer, ptr, length));
      return result;
    },
    writeString(str: string): number {
      const encoder = new TextEncoder();
      const bytes = encoder.encode(str + '\0');
      const ptr = exports.hd_alloc(bytes.length);
      new Uint8Array(exports.memory.buffer).set(bytes, ptr);
      return ptr;
    },
    readString(ptr: number, length: number): string {
      const decoder = new TextDecoder();
      const bytes = new Uint8Array(exports.memory.buffer, ptr, length);
      return decoder.decode(bytes);
    },
  };
}

/**
 * Initialize the HD wallet WASM module
 */
export async function initHDWallet(options: HDWalletOptions = {}): Promise<boolean> {
  if (hdWalletModule) {
    return true;
  }

  if (moduleReady) {
    await moduleReady;
    return hdWalletModule !== null;
  }

  let resolveReady: () => void;
  moduleReady = new Promise<void>((resolve) => {
    resolveReady = resolve;
  });

  const paths = options.wasmPath ? [options.wasmPath, ...WASM_PATHS] : WASM_PATHS;

  for (const path of paths) {
    try {
      const response = await fetch(path);
      if (!response.ok) continue;

      const wasmBytes = await response.arrayBuffer();

      // Verify integrity if not skipped
      if (!options.skipIntegrityCheck) {
        let expectedSri = options.expectedSri;

        if (!expectedSri) {
          expectedSri = (await fetchSriHash(path)) ?? undefined;
        }

        if (expectedSri) {
          const isValid = await verifySri(wasmBytes, expectedSri);
          if (!isValid) {
            console.error(`HD Wallet WASM from ${path} failed integrity check`);
            continue;
          }
        } else {
          console.warn(`No SRI hash available for ${path}, loading without verification`);
        }
      }

      // WASI imports
      const importObject = {
        wasi_snapshot_preview1: {
          fd_write: () => 0,
          fd_seek: () => 0,
          fd_close: () => 0,
          proc_exit: () => {},
          environ_sizes_get: () => 0,
          environ_get: () => 0,
          clock_time_get: () => 0,
          random_get: (bufPtr: number, bufLen: number) => {
            // Provide entropy from Web Crypto API
            const buf = new Uint8Array(bufLen);
            crypto.getRandomValues(buf);
            const memory = new Uint8Array(
              (hdWalletModule?.exports.memory ?? new WebAssembly.Memory({ initial: 1 })).buffer
            );
            memory.set(buf, bufPtr);
            return 0;
          },
        },
      };

      const wasmModule = await WebAssembly.instantiate(wasmBytes, importObject);
      const exports = wasmModule.instance.exports as unknown as HDWalletWasmExports;

      hdWalletModule = createModuleWrapper(exports);
      resolveReady!();
      return true;
    } catch (err) {
      console.warn(`Failed to load HD Wallet WASM from ${path}:`, err);
      continue;
    }
  }

  resolveReady!();
  return false;
}

/**
 * Check if HD wallet module is loaded
 */
export function isHDWalletAvailable(): boolean {
  return hdWalletModule !== null;
}

/**
 * Get the loaded module (throws if not loaded)
 */
function getModule(): HDWalletModule {
  if (!hdWalletModule) {
    throw new Error('HD Wallet WASM module not loaded - call initHDWallet() first');
  }
  return hdWalletModule;
}

/**
 * Inject entropy for WASI environments
 * In browsers, this is typically not needed as we provide random_get
 */
export function injectEntropy(entropy: Uint8Array): void {
  const module = getModule();
  const ptr = module.alloc(entropy.length);
  try {
    module.writeBytes(ptr, entropy);
    module.exports.hd_inject_entropy(ptr, entropy.length);
    entropyInjected = true;
  } finally {
    module.dealloc(ptr);
  }
}

/**
 * Check if entropy is available
 */
export function hasEntropy(): boolean {
  if (!hdWalletModule) return false;
  const status = hdWalletModule.exports.hd_get_entropy_status();
  return status >= 2 || entropyInjected;
}

// =============================================================================
// Mnemonic Functions
// =============================================================================

/**
 * Generate a new BIP-39 mnemonic phrase
 *
 * @param options - Generation options (wordCount, language)
 * @returns The mnemonic phrase as a space-separated string
 *
 * @example
 * ```ts
 * const mnemonic = await generateMnemonic({ wordCount: 24 });
 * console.log(mnemonic); // "word1 word2 ... word24"
 * ```
 */
export async function generateMnemonic(options: MnemonicOptions = {}): Promise<string> {
  const module = getModule();
  const wordCount = options.wordCount ?? 24;
  const language = LanguageCode[options.language ?? 'english'];

  const outputSize = 512;
  const outputPtr = module.alloc(outputSize);

  try {
    const result = module.exports.hd_mnemonic_generate(
      outputPtr,
      outputSize,
      wordCount,
      language
    );

    if (result < 0) {
      switch (result) {
        case ErrorCode.NO_ENTROPY:
          throw new Error('No entropy available - ensure crypto.getRandomValues is available');
        default:
          throw new Error(`Mnemonic generation failed with error: ${result}`);
      }
    }

    return module.readString(outputPtr, result);
  } finally {
    module.dealloc(outputPtr);
  }
}

/**
 * Validate a BIP-39 mnemonic phrase
 *
 * @param mnemonic - The mnemonic phrase to validate
 * @param language - Optional language (default: english)
 * @returns True if the mnemonic is valid
 */
export async function validateMnemonic(
  mnemonic: string,
  language: MnemonicOptions['language'] = 'english'
): Promise<boolean> {
  const module = getModule();
  const mnemonicPtr = module.writeString(mnemonic);

  try {
    const result = module.exports.hd_mnemonic_validate(
      mnemonicPtr,
      LanguageCode[language]
    );
    return result === 0; // 0 = OK
  } finally {
    module.dealloc(mnemonicPtr);
  }
}

/**
 * Convert a mnemonic to a 64-byte seed using PBKDF2
 *
 * @param mnemonic - The mnemonic phrase
 * @param passphrase - Optional passphrase (default: empty)
 * @returns 64-byte seed
 */
export async function mnemonicToSeed(
  mnemonic: string,
  passphrase: string = ''
): Promise<Uint8Array> {
  const module = getModule();
  const mnemonicPtr = module.writeString(mnemonic);
  const passphrasePtr = module.writeString(passphrase);
  const seedSize = 64;
  const seedPtr = module.alloc(seedSize);

  try {
    const result = module.exports.hd_mnemonic_to_seed(
      mnemonicPtr,
      passphrasePtr,
      seedPtr,
      seedSize
    );

    if (result !== 0) {
      throw new Error(`Seed derivation failed with error: ${result}`);
    }

    return module.readBytes(seedPtr, seedSize);
  } finally {
    module.dealloc(mnemonicPtr);
    module.dealloc(passphrasePtr);
    module.dealloc(seedPtr);
  }
}

// =============================================================================
// Key Derivation Functions
// =============================================================================

/**
 * Derive an Ed25519 key at the given path using SLIP-10
 *
 * @param seed - 64-byte seed from mnemonicToSeed
 * @param path - Derivation path (e.g., "m/44'/9999'/0'/0'/0'")
 * @returns Derived private key and chain code
 *
 * @example
 * ```ts
 * const seed = await mnemonicToSeed(mnemonic);
 * const key = await deriveEd25519Key(seed, "m/44'/9999'/0'/0'/0'");
 * ```
 */
export async function deriveEd25519Key(
  seed: Uint8Array,
  path: string
): Promise<DerivedKey> {
  const module = getModule();

  if (seed.length !== 64) {
    throw new Error('Seed must be 64 bytes');
  }

  const seedPtr = module.alloc(seed.length);
  const pathPtr = module.writeString(path);
  const keySize = 32;
  const keyPtr = module.alloc(keySize);
  const chainCodePtr = module.alloc(keySize);

  try {
    module.writeBytes(seedPtr, seed);

    const result = module.exports.hd_slip10_ed25519_derive_path(
      seedPtr,
      seed.length,
      pathPtr,
      keyPtr,
      chainCodePtr
    );

    if (result !== 0) {
      throw new Error(`Key derivation failed with error: ${result}`);
    }

    return {
      privateKey: module.readBytes(keyPtr, keySize),
      chainCode: module.readBytes(chainCodePtr, keySize),
    };
  } finally {
    module.dealloc(seedPtr);
    module.dealloc(pathPtr);
    module.dealloc(keyPtr);
    module.dealloc(chainCodePtr);
  }
}

/**
 * Derive Ed25519 public key from a 32-byte seed
 *
 * @param seed - 32-byte Ed25519 seed (private key)
 * @returns 32-byte public key
 */
export async function ed25519PublicKey(seed: Uint8Array): Promise<Uint8Array> {
  const module = getModule();

  if (seed.length !== 32) {
    throw new Error('Seed must be 32 bytes');
  }

  const seedPtr = module.alloc(seed.length);
  const pubKeySize = 32;
  const pubKeyPtr = module.alloc(pubKeySize);

  try {
    module.writeBytes(seedPtr, seed);

    const result = module.exports.hd_ed25519_pubkey_from_seed(
      seedPtr,
      pubKeyPtr,
      pubKeySize
    );

    if (result !== 0) {
      throw new Error(`Public key derivation failed with error: ${result}`);
    }

    return module.readBytes(pubKeyPtr, pubKeySize);
  } finally {
    module.dealloc(seedPtr);
    module.dealloc(pubKeyPtr);
  }
}

/**
 * Derive a full Ed25519 key pair
 *
 * @param seed - 64-byte seed from mnemonicToSeed
 * @param path - Derivation path
 * @returns Key pair with private and public keys
 */
export async function deriveEd25519KeyPair(
  seed: Uint8Array,
  path: string
): Promise<KeyPair> {
  const derived = await deriveEd25519Key(seed, path);
  const publicKey = await ed25519PublicKey(derived.privateKey);

  return {
    privateKey: derived.privateKey,
    publicKey,
  };
}

/**
 * Derive X25519 public key from private key
 *
 * @param privateKey - 32-byte X25519 private key
 * @returns 32-byte X25519 public key
 */
export async function x25519PublicKey(privateKey: Uint8Array): Promise<Uint8Array> {
  const module = getModule();

  if (privateKey.length !== 32) {
    throw new Error('Private key must be 32 bytes');
  }

  const privKeyPtr = module.alloc(privateKey.length);
  const pubKeySize = 32;
  const pubKeyPtr = module.alloc(pubKeySize);

  try {
    module.writeBytes(privKeyPtr, privateKey);

    const result = module.exports.hd_x25519_pubkey(
      privKeyPtr,
      pubKeyPtr,
      pubKeySize
    );

    if (result !== 0) {
      throw new Error(`X25519 public key derivation failed with error: ${result}`);
    }

    return module.readBytes(pubKeyPtr, pubKeySize);
  } finally {
    module.dealloc(privKeyPtr);
    module.dealloc(pubKeyPtr);
  }
}

// =============================================================================
// SDN Identity Functions
// =============================================================================

/**
 * Derive a complete SDN identity from a seed
 *
 * @param seed - 64-byte seed from mnemonicToSeed
 * @param account - BIP-44 account index (default: 0)
 * @returns Derived identity with signing and encryption keys
 */
export async function deriveIdentity(
  seed: Uint8Array,
  account: number = 0
): Promise<DerivedIdentity> {
  const signingPath = buildSigningPath(account);
  const encryptionPath = buildEncryptionPath(account);

  const signingKey = await deriveEd25519KeyPair(seed, signingPath);
  const encryptionDerived = await deriveEd25519Key(seed, encryptionPath);
  const encryptionPubKey = await x25519PublicKey(encryptionDerived.privateKey);

  return {
    account,
    signingKey,
    encryptionKey: {
      privateKey: encryptionDerived.privateKey,
      publicKey: encryptionPubKey,
    },
    signingKeyPath: signingPath,
    encryptionKeyPath: encryptionPath,
  };
}

/**
 * Create identity from mnemonic (convenience function)
 */
export async function identityFromMnemonic(
  mnemonic: string,
  passphrase: string = '',
  account: number = 0
): Promise<DerivedIdentity> {
  const isValid = await validateMnemonic(mnemonic);
  if (!isValid) {
    throw new Error('Invalid mnemonic phrase');
  }

  const seed = await mnemonicToSeed(mnemonic, passphrase);
  return deriveIdentity(seed, account);
}

// =============================================================================
// Signing Functions (Backward Compatible)
// =============================================================================

/**
 * Sign a message using Ed25519
 *
 * @param privateKey - 32-byte or 64-byte private key (backward compatible)
 * @param message - Message to sign
 * @returns 64-byte signature
 *
 * @remarks
 * For backward compatibility with flatc-crypto.wasm, this function accepts
 * both 32-byte seeds and 64-byte full keys (seed + public key).
 */
export async function sign(
  privateKey: Uint8Array,
  message: Uint8Array
): Promise<Uint8Array> {
  const module = getModule();

  // Handle backward compatibility: 64-byte key = seed + pubkey
  const seed = privateKey.length === 64 ? privateKey.slice(0, 32) : privateKey;

  if (seed.length !== 32) {
    throw new Error('Private key must be 32 or 64 bytes');
  }

  const seedPtr = module.alloc(seed.length);
  const msgPtr = module.alloc(message.length);
  const sigSize = 64;
  const sigPtr = module.alloc(sigSize);

  try {
    module.writeBytes(seedPtr, seed);
    module.writeBytes(msgPtr, message);

    const result = module.exports.hd_ed25519_sign(
      seedPtr,
      seed.length,
      msgPtr,
      message.length,
      sigPtr,
      sigSize
    );

    // Returns signature length (64) on success, negative on error
    if (result < 0) {
      throw new Error('Signing failed');
    }

    return module.readBytes(sigPtr, sigSize);
  } finally {
    module.dealloc(seedPtr);
    module.dealloc(msgPtr);
    module.dealloc(sigPtr);
  }
}

/**
 * Verify an Ed25519 signature
 *
 * @param publicKey - 32-byte public key
 * @param message - Original message
 * @param signature - 64-byte signature
 * @returns True if signature is valid
 */
export async function verify(
  publicKey: Uint8Array,
  message: Uint8Array,
  signature: Uint8Array
): Promise<boolean> {
  const module = getModule();

  if (publicKey.length !== 32) {
    throw new Error('Public key must be 32 bytes');
  }
  if (signature.length !== 64) {
    throw new Error('Signature must be 64 bytes');
  }

  const pubKeyPtr = module.alloc(publicKey.length);
  const msgPtr = module.alloc(message.length);
  const sigPtr = module.alloc(signature.length);

  try {
    module.writeBytes(pubKeyPtr, publicKey);
    module.writeBytes(msgPtr, message);
    module.writeBytes(sigPtr, signature);

    const result = module.exports.hd_ed25519_verify(
      pubKeyPtr,
      publicKey.length,
      msgPtr,
      message.length,
      sigPtr,
      signature.length
    );

    return result === 1;
  } finally {
    module.dealloc(pubKeyPtr);
    module.dealloc(msgPtr);
    module.dealloc(sigPtr);
  }
}

// =============================================================================
// ECDH / Encryption Functions (Backward Compatible)
// =============================================================================

/**
 * Perform X25519 key exchange
 *
 * @param privateKey - 32-byte X25519 private key
 * @param publicKey - 32-byte X25519 public key
 * @returns 32-byte shared secret
 */
export async function x25519ECDH(
  privateKey: Uint8Array,
  publicKey: Uint8Array
): Promise<Uint8Array> {
  const module = getModule();

  if (privateKey.length !== 32 || publicKey.length !== 32) {
    throw new Error('Keys must be 32 bytes');
  }

  const privKeyPtr = module.alloc(privateKey.length);
  const pubKeyPtr = module.alloc(publicKey.length);
  const sharedSize = 32;
  const sharedPtr = module.alloc(sharedSize);

  try {
    module.writeBytes(privKeyPtr, privateKey);
    module.writeBytes(pubKeyPtr, publicKey);

    const result = module.exports.hd_ecdh_x25519(
      privKeyPtr,
      pubKeyPtr,
      sharedPtr,
      sharedSize
    );

    if (result !== 0) {
      throw new Error(`X25519 ECDH failed with error: ${result}`);
    }

    return module.readBytes(sharedPtr, sharedSize);
  } finally {
    module.dealloc(privKeyPtr);
    module.dealloc(pubKeyPtr);
    module.dealloc(sharedPtr);
  }
}

/**
 * Encrypt data using AES-GCM (falls back to Web Crypto if WASM not available)
 *
 * @param key - 32-byte encryption key
 * @param plaintext - Data to encrypt
 * @returns Encrypted data with IV prepended
 */
export async function encrypt(key: Uint8Array, plaintext: Uint8Array): Promise<Uint8Array> {
  const module = hdWalletModule;

  // Try WASM first if available
  if (module?.exports.hd_aes_gcm_encrypt) {
    try {
      const keyPtr = module.alloc(key.length);
      const dataPtr = module.alloc(plaintext.length);
      const outputSize = plaintext.length + 28; // IV (12) + tag (16)
      const outputPtr = module.alloc(outputSize);

      try {
        module.writeBytes(keyPtr, key);
        module.writeBytes(dataPtr, plaintext);

        const resultSize = module.exports.hd_aes_gcm_encrypt!(
          keyPtr,
          key.length,
          dataPtr,
          plaintext.length,
          outputPtr,
          outputSize
        );

        if (resultSize > 0) {
          return module.readBytes(outputPtr, resultSize);
        }
      } finally {
        module.dealloc(keyPtr);
        module.dealloc(dataPtr);
        module.dealloc(outputPtr);
      }
    } catch {
      // Fall through to Web Crypto
    }
  }

  // Fallback to Web Crypto API
  const iv = crypto.getRandomValues(new Uint8Array(12));

  // Create ArrayBuffer copies for Web Crypto API compatibility
  const keyBuffer = new ArrayBuffer(key.length);
  new Uint8Array(keyBuffer).set(key);

  const plaintextBuffer = new ArrayBuffer(plaintext.length);
  new Uint8Array(plaintextBuffer).set(plaintext);

  const cryptoKey = await crypto.subtle.importKey(
    'raw',
    keyBuffer,
    { name: 'AES-GCM' },
    false,
    ['encrypt']
  );

  const encrypted = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv },
    cryptoKey,
    plaintextBuffer
  );

  // Prepend IV to ciphertext
  const result = new Uint8Array(iv.length + encrypted.byteLength);
  result.set(iv, 0);
  result.set(new Uint8Array(encrypted), iv.length);

  return result;
}

/**
 * Decrypt data using AES-GCM (falls back to Web Crypto if WASM not available)
 *
 * @param key - 32-byte encryption key
 * @param ciphertext - Encrypted data with IV prepended
 * @returns Decrypted data
 */
export async function decrypt(key: Uint8Array, ciphertext: Uint8Array): Promise<Uint8Array> {
  const module = hdWalletModule;

  // Try WASM first if available
  if (module?.exports.hd_aes_gcm_decrypt) {
    try {
      const keyPtr = module.alloc(key.length);
      const dataPtr = module.alloc(ciphertext.length);
      const outputSize = ciphertext.length;
      const outputPtr = module.alloc(outputSize);

      try {
        module.writeBytes(keyPtr, key);
        module.writeBytes(dataPtr, ciphertext);

        const resultSize = module.exports.hd_aes_gcm_decrypt!(
          keyPtr,
          key.length,
          dataPtr,
          ciphertext.length,
          outputPtr,
          outputSize
        );

        if (resultSize > 0) {
          return module.readBytes(outputPtr, resultSize);
        }
      } finally {
        module.dealloc(keyPtr);
        module.dealloc(dataPtr);
        module.dealloc(outputPtr);
      }
    } catch {
      // Fall through to Web Crypto
    }
  }

  // Fallback to Web Crypto API
  const iv = ciphertext.slice(0, 12);
  const encrypted = ciphertext.slice(12);

  // Create ArrayBuffer copies for Web Crypto API compatibility
  const keyBuffer = new ArrayBuffer(key.length);
  new Uint8Array(keyBuffer).set(key);

  const encryptedBuffer = new ArrayBuffer(encrypted.length);
  new Uint8Array(encryptedBuffer).set(encrypted);

  const cryptoKey = await crypto.subtle.importKey(
    'raw',
    keyBuffer,
    { name: 'AES-GCM' },
    false,
    ['decrypt']
  );

  const decrypted = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv },
    cryptoKey,
    encryptedBuffer
  );

  return new Uint8Array(decrypted);
}

/**
 * Encrypt bytes using ECDH-derived shared secret (backward compatible)
 *
 * @param message - Data to encrypt
 * @param recipientPubKey - Recipient's X25519 public key
 * @param senderPrivKey - Sender's X25519 private key
 * @returns Encrypted data
 */
export async function encryptBytes(
  message: Uint8Array,
  recipientPubKey: Uint8Array,
  senderPrivKey: Uint8Array
): Promise<Uint8Array> {
  const sharedSecret = await x25519ECDH(senderPrivKey, recipientPubKey);
  return encrypt(sharedSecret, message);
}

/**
 * Decrypt bytes using ECDH-derived shared secret (backward compatible)
 *
 * @param encrypted - Encrypted data
 * @param senderPubKey - Sender's X25519 public key
 * @param recipientPrivKey - Recipient's X25519 private key
 * @returns Decrypted data
 */
export async function decryptBytes(
  encrypted: Uint8Array,
  senderPubKey: Uint8Array,
  recipientPrivKey: Uint8Array
): Promise<Uint8Array> {
  const sharedSecret = await x25519ECDH(recipientPrivKey, senderPubKey);
  return decrypt(sharedSecret, encrypted);
}

// =============================================================================
// Utility Functions
// =============================================================================

/**
 * Generate random bytes
 */
export function randomBytes(length: number): Uint8Array {
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  return bytes;
}

/**
 * Generate a random 32-byte encryption key
 */
export function generateKey(): Uint8Array {
  return randomBytes(32);
}

/**
 * Compute SHA-256 hash
 */
export async function sha256(data: Uint8Array): Promise<Uint8Array> {
  // Create ArrayBuffer copy for Web Crypto API compatibility
  const dataBuffer = new ArrayBuffer(data.length);
  new Uint8Array(dataBuffer).set(data);

  const hashBuffer = await crypto.subtle.digest('SHA-256', dataBuffer);
  return new Uint8Array(hashBuffer);
}
