/**
 * SpaceAware local wallet storage (loop task U0.3 — D1 groundwork).
 *
 * Decision D1 (design/SPACEAWARE_UI_WIRING_ANALYSIS.md §5): sdn-server auth
 * is passwordless Ed25519 challenge/response keyed on an xpub identity —
 * there is no server-side password. The login screen's OPERATOR ID /
 * PASSPHRASE fields (pixel-ported in U1.1/U1.2) map to a LOCAL wallet
 * unlock UX instead: "OPERATOR ID" selects/labels a wallet whose BIP-39
 * mnemonic is stored browser-side ENCRYPTED; "PASSPHRASE" decrypts it
 * locally, after which challenge/verify runs invisibly against the derived
 * Ed25519 signing key (see auth-store.ts). The decrypted mnemonic/seed/
 * private key NEVER leave this module or touch the network — only the
 * derived Ed25519 PUBLIC key and per-challenge signatures do.
 *
 * Crypto primitives are the hd-wallet-wasm native backend already wrapped by
 * `sdn-js/src/crypto` (PBKDF2-SHA256 key derivation, AES-GCM encryption,
 * BIP-39/SLIP-10 identity derivation) — no WebCrypto dependency, consistent
 * with the rest of the SDN identity stack (see `LocalDataScreen.svelte`'s
 * `verify` import for the existing precedent of reaching into
 * `sdn-js/src/crypto` from `sdn-js/ui/`).
 *
 * Storage: `localStorage['sdn_spaceaware_wallets_v1']`, a label-keyed map of
 * wallet METADATA + ciphertext only — plaintext mnemonic/seed/private key
 * material is never persisted, matching the stack-wide "never commit/persist
 * wallet secrets" rule.
 */

// Reaches directly into `crypto/hd-wallet.ts` rather than the `crypto/`
// barrel (`../../../../src/crypto`): the barrel's curated export list omits
// `pbkdf2Sha256`/`aesGcmEncryptWithIv`/`aesGcmDecryptWithIv` (native-crypto
// utilities). Same precedent as `LocalDataScreen.svelte`'s `verify` import.
import {
  generateMnemonic,
  validateMnemonic,
  identityFromMnemonic,
  pbkdf2Sha256,
  aesGcmEncryptWithIv,
  aesGcmDecryptWithIv,
  randomBytes,
  initHDWallet,
  sign as ed25519Sign,
} from '../../../../src/crypto/hd-wallet';
import { vendorHdWalletWasmLoader } from './hd-wallet-wasm-vendor';

export const LOCAL_WALLET_STORAGE_KEY = 'sdn_spaceaware_wallets_v1';

/** PBKDF2-HMAC-SHA256 iteration count (OWASP 2023 minimum floor). */
const PBKDF2_ITERATIONS = 210_000;
const AES_KEY_LENGTH = 32;
const SALT_LENGTH = 16;
const IV_LENGTH = 12;

export class LocalWalletError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'LocalWalletError';
  }
}

/** Public wallet identity — safe to list for an "OPERATOR ID" picker (no secrets). */
export interface LocalWalletSummary {
  label: string;
  xpub: string;
  peerId: string;
  createdAt: string;
}

interface StoredWalletRecord extends LocalWalletSummary {
  kdfIterations: number;
  saltHex: string;
  ivHex: string;
  ciphertextHex: string;
}

export interface CreateLocalWalletOptions {
  /** Recovery path: import an existing mnemonic instead of generating one. */
  mnemonic?: string;
  /** BIP-44 account index. Default 0. */
  account?: number;
}

export interface CreateLocalWalletResult extends LocalWalletSummary {
  /** Returned ONLY at creation time for a one-time backup prompt. Never stored. */
  mnemonic: string;
}

/** An unlocked wallet's Ed25519 signing capability. Holds the private key in a closure only. */
export interface UnlockedWallet {
  label: string;
  xpub: string;
  peerId: string;
  signingPublicKeyHex: string;
  /** Signs with the in-memory Ed25519 private key. Never exports it. */
  sign(message: Uint8Array): Promise<Uint8Array>;
  /** Forgets the in-memory private key; subsequent `sign()` calls throw. */
  lock(): void;
}

/** Minimal storage shape this module needs — same DI pattern as `globe/land-dots.ts`'s `loadLandDots`. */
export type LocalWalletStorage = Pick<Storage, 'getItem' | 'setItem'>;

function resolveStorage(storage?: LocalWalletStorage | null): LocalWalletStorage {
  const resolved = storage !== undefined ? storage : typeof localStorage !== 'undefined' ? localStorage : null;
  if (!resolved) {
    throw new LocalWalletError('localStorage is unavailable in this context');
  }
  return resolved;
}

function readStore(storage?: LocalWalletStorage | null): Record<string, StoredWalletRecord> {
  const raw = resolveStorage(storage).getItem(LOCAL_WALLET_STORAGE_KEY);
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, StoredWalletRecord>) : {};
  } catch {
    return {};
  }
}

function writeStore(store: Record<string, StoredWalletRecord>, storage?: LocalWalletStorage | null): void {
  resolveStorage(storage).setItem(LOCAL_WALLET_STORAGE_KEY, JSON.stringify(store));
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

function hexToBytes(hex: string): Uint8Array {
  const clean = hex.trim();
  if (clean.length % 2 !== 0) throw new LocalWalletError('invalid hex length');
  const out = new Uint8Array(clean.length / 2);
  for (let i = 0; i < out.length; i += 1) {
    out[i] = Number.parseInt(clean.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

async function ensureHDWalletReady(): Promise<void> {
  vendorHdWalletWasmLoader();
  const ready = await initHDWallet();
  if (!ready) {
    throw new LocalWalletError('HD Wallet WASM module failed to initialize');
  }
}

/** Wallet labels + public identity only. */
export function listLocalWallets(storage?: LocalWalletStorage | null): LocalWalletSummary[] {
  const store = readStore(storage);
  return Object.values(store)
    .map(({ label, xpub, peerId, createdAt }) => ({ label, xpub, peerId, createdAt }))
    .sort((a, b) => a.label.localeCompare(b.label));
}

export function hasLocalWallet(label: string, storage?: LocalWalletStorage | null): boolean {
  return label.trim() in readStore(storage);
}

export function removeLocalWallet(label: string, storage?: LocalWalletStorage | null): void {
  const store = readStore(storage);
  delete store[label.trim()];
  writeStore(store, storage);
}

/**
 * Create (or overwrite) a locally-stored encrypted wallet. Generates a fresh
 * 24-word BIP-39 mnemonic unless one is supplied (recovery path). The
 * mnemonic is returned once for a backup prompt; only its AES-GCM ciphertext
 * is persisted.
 */
export async function createLocalWallet(
  label: string,
  passphrase: string,
  options: CreateLocalWalletOptions = {},
  storage?: LocalWalletStorage | null,
): Promise<CreateLocalWalletResult> {
  const trimmedLabel = label.trim();
  if (!trimmedLabel) throw new LocalWalletError('wallet label is required');
  if (!passphrase) throw new LocalWalletError('passphrase is required');

  await ensureHDWalletReady();

  const mnemonic = options.mnemonic?.trim() || (await generateMnemonic({ wordCount: 24 }));
  if (!(await validateMnemonic(mnemonic))) {
    throw new LocalWalletError('invalid mnemonic phrase');
  }

  const identity = await identityFromMnemonic(mnemonic, '', options.account ?? 0);

  const salt = randomBytes(SALT_LENGTH);
  const iv = randomBytes(IV_LENGTH);
  const key = await pbkdf2Sha256(new TextEncoder().encode(passphrase), salt, PBKDF2_ITERATIONS, AES_KEY_LENGTH);
  const ciphertext = await aesGcmEncryptWithIv(key, new TextEncoder().encode(mnemonic), iv);

  const record: StoredWalletRecord = {
    label: trimmedLabel,
    xpub: identity.xpub,
    peerId: identity.peerId,
    createdAt: new Date().toISOString(),
    kdfIterations: PBKDF2_ITERATIONS,
    saltHex: bytesToHex(salt),
    ivHex: bytesToHex(iv),
    ciphertextHex: bytesToHex(ciphertext),
  };

  const store = readStore(storage);
  store[trimmedLabel] = record;
  writeStore(store, storage);

  return {
    label: trimmedLabel,
    xpub: identity.xpub,
    peerId: identity.peerId,
    createdAt: record.createdAt,
    mnemonic,
  };
}

/**
 * Decrypt a stored wallet with its passphrase and derive its signing
 * identity. The returned `sign` closure holds the Ed25519 private key for
 * the lifetime of the returned object only — callers MUST NOT persist it;
 * call `lock()` (or simply drop the reference) to forget it.
 */
export async function unlockLocalWallet(
  label: string,
  passphrase: string,
  storage?: LocalWalletStorage | null,
): Promise<UnlockedWallet> {
  const trimmedLabel = label.trim();
  const store = readStore(storage);
  const record = store[trimmedLabel];
  if (!record) throw new LocalWalletError(`no local wallet labeled ${JSON.stringify(trimmedLabel)}`);

  await ensureHDWalletReady();

  const salt = hexToBytes(record.saltHex);
  const iv = hexToBytes(record.ivHex);
  const ciphertext = hexToBytes(record.ciphertextHex);
  const key = await pbkdf2Sha256(new TextEncoder().encode(passphrase), salt, record.kdfIterations, AES_KEY_LENGTH);

  let mnemonic: string;
  try {
    const plaintext = await aesGcmDecryptWithIv(key, ciphertext, iv);
    mnemonic = new TextDecoder().decode(plaintext);
  } catch {
    throw new LocalWalletError('incorrect passphrase');
  }

  const identity = await identityFromMnemonic(mnemonic, '', 0);
  if (identity.xpub !== record.xpub) {
    // Defense in depth: AES-GCM already authenticates the ciphertext, so
    // this should be unreachable — a mismatch here means a corrupted/
    // tampered record rather than a wrong passphrase (which fails above).
    throw new LocalWalletError('decrypted wallet identity does not match stored record');
  }

  let privateKey: Uint8Array | null = identity.signingKey.privateKey;
  const signingPublicKeyHex = bytesToHex(identity.signingKey.publicKey);

  return {
    label: trimmedLabel,
    xpub: identity.xpub,
    peerId: identity.peerId,
    signingPublicKeyHex,
    async sign(message: Uint8Array): Promise<Uint8Array> {
      if (!privateKey) throw new LocalWalletError('wallet has been locked');
      return ed25519Sign(privateKey, message);
    },
    lock(): void {
      privateKey = null;
    },
  };
}
