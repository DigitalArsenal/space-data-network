/**
 * SDN Crypto Module
 *
 * Unified cryptographic operations using hd-wallet-wasm.
 * Provides backward-compatible APIs from flatc-crypto.wasm
 * plus new HD wallet functionality.
 */

// Re-export types
export type {
  HDWalletWasmExports,
  HDWalletModule,
  HDWalletOptions,
  MnemonicOptions,
  DerivedKey,
  KeyPair,
  EncryptionKeyPair,
  DerivedIdentity,
} from './types';

export {
  LanguageCode,
  ErrorCode,
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

  // Signing (backward compatible)
  sign,
  verify,

  // Encryption (backward compatible)
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

// Default export for convenience
import * as hdWallet from './hd-wallet';
export default hdWallet;
