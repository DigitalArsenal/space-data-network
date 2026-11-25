/**
 * Persisted SDN node identity helpers (HERMES ruling, stack #113).
 *
 * `SDNNode` derives its libp2p PeerID from `config.identity.identityKey`
 * (secp256k1 at m/44'/0'/{account}'), whereas `config.privateKey` only signs
 * auth challenges. `loadOrCreateIdentity(store)` loads a persisted 64-byte HD
 * seed or generates and persists a new one on first boot so a Node or browser
 * consumer keeps a stable PeerID across restarts.
 *
 * Browser-safe: this module has zero Node-only imports. For Node.js file
 * persistence, pair with `fileIdentityStore` from `@spacedatanetwork/sdn-js/node`.
 */

import {
  deriveIdentity,
  generateMnemonic,
  initHDWallet,
  mnemonicToSeed,
} from './crypto/hd-wallet';
import type { DerivedIdentity } from './crypto/types';

/** Required byte length of an HD wallet BIP-39 master seed. */
export const IDENTITY_SEED_LENGTH = 64;

/**
 * Storage contract for a node's 64-byte HD master seed.
 *
 * Implementations MUST return `null` from `load()` ONLY when no identity has
 * been persisted yet. An existing record that cannot be read or fails
 * integrity checks MUST throw and MUST remain untouched so a node never
 * silently mints a replacement PeerID.
 */
export interface IdentityStore {
  load(): Promise<Uint8Array | null>;
  save(seed: Uint8Array): Promise<void>;
}

export interface LoadOrCreateIdentityOptions {
  /** BIP-44 account index (default: 0). */
  account?: number;
  /** Optional BIP-39 passphrase used only when minting a new identity (default: ''). */
  passphrase?: string;
}

/**
 * Load a persisted SDN identity from `store`, or generate and persist a fresh
 * one if `store.load()` returns `null`.
 *
 * Never prints or logs seed or mnemonic material. If `store.load()` returns
 * malformed bytes or throws, this function throws without calling `store.save`.
 */
export async function loadOrCreateIdentity(
  store: IdentityStore,
  options: LoadOrCreateIdentityOptions = {},
): Promise<DerivedIdentity> {
  if (!store || typeof store.load !== 'function' || typeof store.save !== 'function') {
    throw new TypeError('IdentityStore with load() and save() methods is required');
  }
  const account = options.account ?? 0;
  if (!Number.isInteger(account) || account < 0) {
    throw new RangeError('account must be a non-negative integer');
  }

  const ready = await initHDWallet();
  if (!ready) {
    throw new Error('HD Wallet WASM module failed to initialize');
  }

  const existing = await store.load();
  if (existing !== null) {
    if (!(existing instanceof Uint8Array) || existing.byteLength !== IDENTITY_SEED_LENGTH) {
      throw new Error(
        `Persisted identity seed must be ${IDENTITY_SEED_LENGTH} bytes; refusing to replace existing identity`,
      );
    }
    return deriveIdentity(existing, account);
  }

  const mnemonic = await generateMnemonic({ wordCount: 24 });
  const seed = await mnemonicToSeed(mnemonic, options.passphrase ?? '');
  if (seed.byteLength !== IDENTITY_SEED_LENGTH) {
    throw new Error(`Derived identity seed length ${seed.byteLength} !== ${IDENTITY_SEED_LENGTH}`);
  }
  await store.save(seed);
  return deriveIdentity(seed, account);
}
