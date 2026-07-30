/**
 * SDN Crypto Module
 *
 * Unified cryptographic operations using hd-wallet-wasm.
 * Provides HD wallet functionality plus backward-compatible crypto APIs.
 */

// Re-export types
export type {
  HDWalletOptions,
  MnemonicOptions,
  DerivedKey,
  KeyPair,
  IdentityKeyPair,
  EncryptionKeyPair,
  DerivedIdentity,
} from './types';

export type { XpubDerivedPublicIdentityKeys } from './hd-wallet';

export {
  LanguageCode,
  SDNDerivation,
  buildIdentityPath,
  buildSigningPath,
  buildEncryptionPath,
} from './types';

// Re-export HD wallet functions
export {
  // Initialization
  initHDWallet,
  isHDWalletAvailable,
  injectEntropy,
  hasEntropy,

  // Mnemonic
  generateMnemonic,
  validateMnemonic,
  mnemonicToSeed,

  // Key derivation
  deriveEd25519Key,
  deriveEd25519KeyPair,
  ed25519PublicKey,
  x25519PublicKey,
  deriveSecp256k1Key,

  // PeerID
  derivePeerIdFromPublicKey,
  derivePeerIdFromXpub,
  deriveIpnsHashFromXpub,

  // SDN identity
  deriveIdentity,
  identityFromMnemonic,
  deriveXPub,
  derivePublicIdentityKeysFromXpub,

  // Signing
  sign,
  verify,

  // Encryption
  encrypt,
  decrypt,
  encryptBytes,
  decryptBytes,

  // ECDH
  x25519ECDH,

  // Utilities
  randomBytes,
  generateKey,
  sha256,
  hkdfSha256,
} from './hd-wallet';

// Path-scoped deterministic module-delivery identity
// (spec: sdn/sandcastle-module-identity/v1 — see ./path-scoped-identity.ts)
export {
  PATH_SCOPED_IDENTITY_INFO_V1,
  PATH_SCOPED_IDENTITY_SALT_V1,
  PATH_SCOPED_IDENTITY_SEED_BYTES,
  canonicalizePathScopeUuid,
  extractPathScopeUuid,
  derivePathScopedSeed,
  derivePathScopedIdentity,
  derivePathScopedIdentityForPath,
} from './path-scoped-identity';

// Default export for convenience
import * as hdWallet from './hd-wallet';
export default hdWallet;
