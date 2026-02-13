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
  EncryptionKeyPair,
  DerivedIdentity,
} from './types';

export {
  LanguageCode,
  SDNDerivation,
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

  // SDN identity
  deriveIdentity,
  identityFromMnemonic,
  deriveXPub,

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
} from './hd-wallet';

// vCard utilities
export type {
  VCardPersonInfo,
  VCardOptions,
  ParsedVCard,
} from './vcard';

export {
  generateVCard,
  parseVCard,
  createVCardBlob,
  createVCardDataURL,
} from './vcard';

// Default export for convenience
import * as hdWallet from './hd-wallet';
export default hdWallet;
