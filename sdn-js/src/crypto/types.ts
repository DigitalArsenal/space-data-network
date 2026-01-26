/**
 * Type definitions for HD wallet cryptographic operations
 */

/**
 * WASM module exports for hd-wallet-wasm
 */
export interface HDWalletWasmExports {
  // Memory management
  hd_alloc: (size: number) => number;
  hd_dealloc: (ptr: number) => void;
  memory: WebAssembly.Memory;

  // Entropy
  hd_inject_entropy: (ptr: number, len: number) => void;
  hd_get_entropy_status: () => number;

  // Mnemonic functions
  hd_mnemonic_generate: (
    outputPtr: number,
    outputSize: number,
    wordCount: number,
    language: number
  ) => number;
  hd_mnemonic_validate: (mnemonicPtr: number, language: number) => number;
  hd_mnemonic_to_seed: (
    mnemonicPtr: number,
    passphrasePtr: number,
    seedOutPtr: number,
    seedSize: number
  ) => number;

  // Key derivation
  hd_slip10_ed25519_derive_path: (
    seedPtr: number,
    seedLen: number,
    pathPtr: number,
    keyOutPtr: number,
    chainCodeOutPtr: number
  ) => number;
  hd_ed25519_pubkey_from_seed: (
    seedPtr: number,
    publicKeyOutPtr: number,
    publicKeySize: number
  ) => number;

  // Signing
  hd_ed25519_sign: (
    seedPtr: number,
    seedLen: number,
    messagePtr: number,
    messageLen: number,
    signatureOutPtr: number,
    signatureSize: number
  ) => number;
  hd_ed25519_verify: (
    publicKeyPtr: number,
    publicKeyLen: number,
    messagePtr: number,
    messageLen: number,
    signaturePtr: number,
    signatureLen: number
  ) => number;

  // X25519 ECDH
  hd_x25519_pubkey: (
    privateKeyPtr: number,
    publicKeyOutPtr: number,
    publicKeySize: number
  ) => number;
  hd_ecdh_x25519: (
    privateKeyPtr: number,
    publicKeyPtr: number,
    sharedSecretOutPtr: number,
    sharedSecretSize: number
  ) => number;

  // AES-GCM encryption
  hd_aes_gcm_encrypt?: (
    keyPtr: number,
    keyLen: number,
    plaintextPtr: number,
    plaintextLen: number,
    outputPtr: number,
    outputSize: number
  ) => number;
  hd_aes_gcm_decrypt?: (
    keyPtr: number,
    keyLen: number,
    ciphertextPtr: number,
    ciphertextLen: number,
    outputPtr: number,
    outputSize: number
  ) => number;

  // Version
  hd_get_version: () => number;
}

/**
 * HD wallet module wrapper
 */
export interface HDWalletModule {
  ready: Promise<void>;
  exports: HDWalletWasmExports;
  heap: Uint8Array;
  alloc: (size: number) => number;
  dealloc: (ptr: number) => void;
  writeBytes: (ptr: number, data: Uint8Array) => void;
  readBytes: (ptr: number, length: number) => Uint8Array;
  writeString: (str: string) => number;
  readString: (ptr: number, length: number) => string;
}

/**
 * Derived key result from HD derivation
 */
export interface DerivedKey {
  /** 32-byte private key (seed for Ed25519) */
  privateKey: Uint8Array;
  /** 32-byte chain code for further derivation */
  chainCode: Uint8Array;
}

/**
 * Full key pair with public key
 */
export interface KeyPair {
  /** 32-byte private key */
  privateKey: Uint8Array;
  /** 32-byte public key */
  publicKey: Uint8Array;
}

/**
 * Encryption key pair (X25519)
 */
export interface EncryptionKeyPair {
  /** 32-byte X25519 private key */
  privateKey: Uint8Array;
  /** 32-byte X25519 public key */
  publicKey: Uint8Array;
}

/**
 * Derived SDN identity
 */
export interface DerivedIdentity {
  /** BIP-44 account index */
  account: number;
  /** Ed25519 signing key pair */
  signingKey: KeyPair;
  /** X25519 encryption key pair */
  encryptionKey: EncryptionKeyPair;
  /** Derivation path for signing key */
  signingKeyPath: string;
  /** Derivation path for encryption key */
  encryptionKeyPath: string;
}

/**
 * HD wallet initialization options
 */
export interface HDWalletOptions {
  /** Path to WASM file (optional, uses default paths if not provided) */
  wasmPath?: string;
  /** Expected SRI hash for integrity verification */
  expectedSri?: string;
  /** Skip integrity verification (not recommended for production) */
  skipIntegrityCheck?: boolean;
}

/**
 * Mnemonic generation options
 */
export interface MnemonicOptions {
  /** Number of words (12, 15, 18, 21, or 24). Default: 24 */
  wordCount?: 12 | 15 | 18 | 21 | 24;
  /** Language for wordlist. Default: 'english' */
  language?: 'english' | 'japanese' | 'korean' | 'spanish' | 'chinese_simplified' | 'chinese_traditional' | 'french' | 'italian' | 'czech' | 'portuguese';
}

/**
 * Language codes for BIP-39 wordlists
 */
export const LanguageCode = {
  english: 0,
  japanese: 1,
  korean: 2,
  spanish: 3,
  chinese_simplified: 4,
  chinese_traditional: 5,
  french: 6,
  italian: 7,
  czech: 8,
  portuguese: 9,
} as const;

/**
 * Error codes from WASM module
 */
export const ErrorCode = {
  OK: 0,
  NO_ENTROPY: -1,
  INVALID_MNEMONIC: -2,
  INVALID_PATH: -3,
  INVALID_KEY: -4,
  SIGNING_FAILED: -5,
  VERIFICATION_FAILED: -6,
  ENCRYPTION_FAILED: -7,
  DECRYPTION_FAILED: -8,
} as const;

/**
 * SDN derivation constants
 */
export const SDNDerivation = {
  /** SLIP-44 coin type for SDN */
  COIN_TYPE: 9999,
  /** Signing key purpose (change index 0) */
  SIGNING_PURPOSE: 0,
  /** Encryption key purpose (change index 1) */
  ENCRYPTION_PURPOSE: 1,
  /** Default BIP-44 purpose */
  BIP44_PURPOSE: 44,
} as const;

/**
 * Build SDN derivation path for signing key
 */
export function buildSigningPath(account: number): string {
  return `m/${SDNDerivation.BIP44_PURPOSE}'/${SDNDerivation.COIN_TYPE}'/${account}'/${SDNDerivation.SIGNING_PURPOSE}'/0'`;
}

/**
 * Build SDN derivation path for encryption key
 */
export function buildEncryptionPath(account: number): string {
  return `m/${SDNDerivation.BIP44_PURPOSE}'/${SDNDerivation.COIN_TYPE}'/${account}'/${SDNDerivation.ENCRYPTION_PURPOSE}'/0'`;
}
