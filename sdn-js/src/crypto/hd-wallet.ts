/**
 * HD Wallet WASM Wrapper for sdn-js
 *
 * Re-exports from hd-wallet-wasm npm package with SDN-specific helpers.
 * Provides BIP-39 mnemonic generation, SLIP-10 key derivation,
 * Ed25519 signing, and X25519 encryption.
 */

import initHDWalletWasm, {
  type HDWalletModule,
  Curve,
  Language,
} from 'hd-wallet-wasm';

import {
  HDWalletOptions,
  MnemonicOptions,
  DerivedKey,
  KeyPair,
  IdentityKeyPair,
  DerivedIdentity,
  LanguageCode,
  SDNDerivation,
  buildIdentityPath,
  buildSigningPath,
  buildEncryptionPath,
} from './types';

// Module state
let hdWalletModule: HDWalletModule | null = null;
let moduleReady: Promise<void> | null = null;

/**
 * Initialize the HD wallet WASM module
 */
export async function initHDWallet(_options: HDWalletOptions = {}): Promise<boolean> {
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

  try {
    // The npm package handles WASM loading internally
    hdWalletModule = await initHDWalletWasm();
    resolveReady!();
    return true;
  } catch (err) {
    console.error('Failed to load HD Wallet WASM:', err);
    resolveReady!();
    return false;
  }
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
 */
export function injectEntropy(entropy: Uint8Array): void {
  getModule().injectEntropy(entropy);
}

/**
 * Check if entropy is available
 */
export function hasEntropy(): boolean {
  if (!hdWalletModule) return false;
  return hdWalletModule.getEntropyStatus() >= 2;
}

// =============================================================================
// Mnemonic Functions
// =============================================================================

/**
 * Map language option to Language enum
 */
function mapLanguage(language: MnemonicOptions['language']): Language {
  const langCode = LanguageCode[language ?? 'english'];
  return langCode as Language;
}

/**
 * Generate a new BIP-39 mnemonic phrase
 */
export async function generateMnemonic(options: MnemonicOptions = {}): Promise<string> {
  const module = getModule();
  const wordCount = options.wordCount ?? 24;
  const language = mapLanguage(options.language);
  return module.mnemonic.generate(wordCount, language);
}

/**
 * Validate a BIP-39 mnemonic phrase
 */
export async function validateMnemonic(
  mnemonic: string,
  language: MnemonicOptions['language'] = 'english'
): Promise<boolean> {
  const module = getModule();
  return module.mnemonic.validate(mnemonic, mapLanguage(language));
}

/**
 * Convert a mnemonic to a 64-byte seed using PBKDF2
 */
export async function mnemonicToSeed(
  mnemonic: string,
  passphrase: string = ''
): Promise<Uint8Array> {
  const module = getModule();
  return module.mnemonic.toSeed(mnemonic, passphrase);
}

// =============================================================================
// Key Derivation Functions
// =============================================================================

/**
 * Derive an Ed25519 key at the given path using SLIP-10
 */
export async function deriveEd25519Key(
  seed: Uint8Array,
  path: string
): Promise<DerivedKey> {
  const module = getModule();

  if (seed.length !== 64) {
    throw new Error('Seed must be 64 bytes');
  }

  const masterKey = module.hdkey.fromSeed(seed, Curve.ED25519);
  const derived = masterKey.derivePath(path);

  const result: DerivedKey = {
    privateKey: derived.privateKey(),
    chainCode: derived.chainCode(),
  };

  // Clean up
  derived.wipe();
  masterKey.wipe();

  return result;
}

/**
 * Derive Ed25519 public key from a 32-byte seed
 */
export async function ed25519PublicKey(seed: Uint8Array): Promise<Uint8Array> {
  const module = getModule();

  if (seed.length !== 32) {
    throw new Error('Seed must be 32 bytes');
  }

  return module.curves.publicKeyFromPrivate(seed, Curve.ED25519);
}

/**
 * Derive a full Ed25519 key pair
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
 */
export async function x25519PublicKey(privateKey: Uint8Array): Promise<Uint8Array> {
  const module = getModule();

  if (privateKey.length !== 32) {
    throw new Error('Private key must be 32 bytes');
  }

  return module.curves.publicKeyFromPrivate(privateKey, Curve.X25519);
}

// =============================================================================
// Secp256k1 Key Derivation
// =============================================================================

/**
 * Derive a secp256k1 key pair at the given BIP-32 path
 */
export async function deriveSecp256k1Key(
  seed: Uint8Array,
  path: string
): Promise<IdentityKeyPair> {
  const module = getModule();

  if (seed.length !== 64) {
    throw new Error('Seed must be 64 bytes');
  }

  const masterKey = module.hdkey.fromSeed(seed, Curve.SECP256K1);
  const derived = masterKey.derivePath(path);

  try {
    return {
      privateKey: derived.privateKey(),
      publicKey: derived.publicKey(), // 33-byte compressed
    };
  } finally {
    derived.wipe();
    masterKey.wipe();
  }
}

/**
 * Derive a PeerID string from a secp256k1 compressed public key
 */
export function derivePeerIdFromPublicKey(publicKey: Uint8Array): string {
  const module = getModule();
  const peerIdBytes = module.libp2p.peerIdFromPublicKey(publicKey, Curve.SECP256K1);
  return module.libp2p.peerIdToString(peerIdBytes);
}

/**
 * Derive a PeerID string from an xpub
 */
export function derivePeerIdFromXpub(xpub: string): string {
  const module = getModule();
  return module.libp2p.peerIdFromXpub(xpub);
}

/**
 * Derive an IPNS hash from an xpub
 */
export function deriveIpnsHashFromXpub(xpub: string): string {
  const module = getModule();
  return module.libp2p.ipnsHashFromXpub(xpub);
}

// =============================================================================
// SDN Identity Functions
// =============================================================================

/**
 * Derive a complete SDN identity from a seed
 */
export async function deriveIdentity(
  seed: Uint8Array,
  account: number = 0
): Promise<DerivedIdentity> {
  const identityPath = buildIdentityPath(account);
  const signingPath = buildSigningPath(account);
  const encryptionPath = buildEncryptionPath(account);

  // Secp256k1 identity key (for PeerID)
  const identityKey = await deriveSecp256k1Key(seed, identityPath);
  const peerId = derivePeerIdFromPublicKey(identityKey.publicKey);
  const xpub = await deriveXPub(seed, account);

  // Ed25519 signing key (for auth)
  const signingKey = await deriveEd25519KeyPair(seed, signingPath);

  // X25519 encryption key
  const encryptionDerived = await deriveEd25519Key(seed, encryptionPath);
  const encryptionPubKey = await x25519PublicKey(encryptionDerived.privateKey);

  return {
    account,
    identityKey,
    peerId,
    xpub,
    signingKey,
    encryptionKey: {
      privateKey: encryptionDerived.privateKey,
      publicKey: encryptionPubKey,
    },
    identityKeyPath: identityPath,
    signingKeyPath: signingPath,
    encryptionKeyPath: encryptionPath,
  };
}

/**
 * Derive a deterministic account xpub from the seed.
 *
 * Path: m/44'/0'/{account}'
 */
export async function deriveXPub(
  seed: Uint8Array,
  account: number = 0,
): Promise<string> {
  const module = getModule();

  if (seed.length !== 64) {
    throw new Error('Seed must be 64 bytes');
  }

  const accountPath = `m/${SDNDerivation.BIP44_PURPOSE}'/${SDNDerivation.COIN_TYPE}'/${account}'`;

  let masterKey: ReturnType<typeof module.hdkey.fromSeed> | null = null;
  let accountKey: ReturnType<typeof module.hdkey.fromSeed> | null = null;
  let neuteredKey: ReturnType<typeof module.hdkey.fromSeed> | null = null;

  try {
    masterKey = module.hdkey.fromSeed(seed, Curve.SECP256K1);
    accountKey = masterKey.derivePath(accountPath);
    neuteredKey = accountKey.neutered();
    return neuteredKey.toXpub();
  } finally {
    neuteredKey?.wipe();
    accountKey?.wipe();
    masterKey?.wipe();
  }
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

  return module.curves.ed25519.sign(message, seed);
}

/**
 * Verify an Ed25519 signature
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

  return module.curves.ed25519.verify(message, signature, publicKey);
}

// =============================================================================
// ECDH / Encryption Functions (Backward Compatible)
// =============================================================================

/**
 * Perform X25519 key exchange
 */
export async function x25519ECDH(
  privateKey: Uint8Array,
  publicKey: Uint8Array
): Promise<Uint8Array> {
  const module = getModule();

  if (privateKey.length !== 32 || publicKey.length !== 32) {
    throw new Error('Keys must be 32 bytes');
  }

  return module.curves.x25519.ecdh(privateKey, publicKey);
}

/**
 * Encrypt data using AES-GCM (uses Web Crypto API)
 */
export async function encrypt(key: Uint8Array, plaintext: Uint8Array): Promise<Uint8Array> {
  const iv = crypto.getRandomValues(new Uint8Array(12));

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
 * Decrypt data using AES-GCM (uses Web Crypto API)
 */
export async function decrypt(key: Uint8Array, ciphertext: Uint8Array): Promise<Uint8Array> {
  const iv = ciphertext.slice(0, 12);
  const encrypted = ciphertext.slice(12);

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
  const dataBuffer = new ArrayBuffer(data.length);
  new Uint8Array(dataBuffer).set(data);

  const hashBuffer = await crypto.subtle.digest('SHA-256', dataBuffer);
  return new Uint8Array(hashBuffer);
}
