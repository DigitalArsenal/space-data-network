/**
 * Type definitions for HD wallet cryptographic operations
 */

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
 * SDN derivation constants
 */
export const SDNDerivation = {
  /** SLIP-44 coin type for SDN (1957 = Sputnik launch year) */
  COIN_TYPE: 1957,
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
