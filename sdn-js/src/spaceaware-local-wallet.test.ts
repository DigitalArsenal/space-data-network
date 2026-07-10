/**
 * Unit tests for the SpaceAware local wallet storage/unlock module
 * (loop task U0.3 — D1 groundwork).
 *
 * Uses the REAL hd-wallet-wasm crypto backend (same precedent as
 * `crypto.test.ts`'s `initHDWallet()` usage under vitest's node
 * environment) rather than mocking it, so the PBKDF2/AES-GCM/SLIP-10 round
 * trip is genuinely exercised. All fixtures below are throwaway test-only
 * values — never real wallet material.
 */
import { beforeAll, describe, expect, it } from 'vitest';
import {
  createLocalWallet,
  hasLocalWallet,
  listLocalWallets,
  LocalWalletError,
  removeLocalWallet,
  unlockLocalWallet,
  type LocalWalletStorage,
  type UnlockedWallet,
} from '../ui/src/lib/auth/local-wallet';
import { verify as verifyEd25519 } from './crypto/hd-wallet';

function memoryStorage(): LocalWalletStorage & { raw: () => string | null } {
  const map = new Map<string, string>();
  return {
    getItem: (key: string) => (map.has(key) ? map.get(key)! : null),
    setItem: (key: string, value: string) => {
      map.set(key, value);
    },
    raw: () => map.get('sdn_spaceaware_wallets_v1') ?? null,
  };
}

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i += 1) out[i] = Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}

describe('local wallet create/unlock round trip (D1 groundwork)', () => {
  const storage = memoryStorage();
  const label = 'test-operator';
  const passphrase = 'correct horse battery staple TEST FIXTURE';
  let created: Awaited<ReturnType<typeof createLocalWallet>>;
  let unlocked: UnlockedWallet;

  beforeAll(async () => {
    created = await createLocalWallet(label, passphrase, {}, storage);
    unlocked = await unlockLocalWallet(label, passphrase, storage);
  }, 20_000);

  it('creates a wallet with a generated 24-word mnemonic and derives a real identity', () => {
    expect(created.label).toBe(label);
    expect(created.mnemonic.trim().split(/\s+/)).toHaveLength(24);
    expect(created.xpub).toMatch(/^xpub/);
    expect(created.peerId.length).toBeGreaterThan(0);
  });

  it('never persists the plaintext mnemonic — only ciphertext hits storage', () => {
    const raw = storage.raw();
    expect(raw).toBeTruthy();
    expect(raw).not.toContain(created.mnemonic);
    // Per-word check as a serialized JSON string token (quoted) rather than
    // a bare substring: a bare-substring check false-positives whenever a
    // mnemonic word happens to be a substring of one of the record's own
    // field names (e.g. the BIP-39 word "text" inside "ciphertextHex").
    for (const word of created.mnemonic.split(/\s+/).slice(0, 5)) {
      expect(raw).not.toContain(`"${word}"`);
      expect(raw).not.toContain(`"${word} `);
      expect(raw).not.toContain(` ${word}"`);
    }
  });

  it('lists the wallet by label with public identity only (no secrets)', () => {
    const wallets = listLocalWallets(storage);
    expect(wallets).toEqual([
      { label, xpub: created.xpub, peerId: created.peerId, createdAt: created.createdAt },
    ]);
    expect(hasLocalWallet(label, storage)).toBe(true);
  });

  it('unlocks with the correct passphrase and re-derives the same public identity', () => {
    expect(unlocked.label).toBe(label);
    expect(unlocked.xpub).toBe(created.xpub);
    expect(unlocked.peerId).toBe(created.peerId);
    expect(unlocked.signingPublicKeyHex).toMatch(/^[0-9a-f]{64}$/);
  });

  it('signs a challenge with the in-memory private key, verifiable against the public key only', async () => {
    const message = new TextEncoder().encode('spaceaware-auth-challenge-fixture');
    const signature = await unlocked.sign(message);
    expect(signature).toBeInstanceOf(Uint8Array);
    expect(signature.length).toBe(64);

    await import('./crypto/hd-wallet').then((m) => m.initHDWallet());
    const valid = await verifyEd25519(hexToBytes(unlocked.signingPublicKeyHex), message, signature);
    expect(valid).toBe(true);
  });

  it('forgets the private key after lock() — subsequent sign() throws', async () => {
    const relocked = await unlockLocalWallet(label, passphrase, storage);
    relocked.lock();
    await expect(relocked.sign(new Uint8Array([1, 2, 3]))).rejects.toThrow(LocalWalletError);
  });

  it('rejects the wrong passphrase without corrupting the stored record', async () => {
    await expect(unlockLocalWallet(label, 'definitely the wrong passphrase', storage)).rejects.toThrow(
      /incorrect passphrase/,
    );
    // The record must still be intact and unlockable with the right passphrase.
    const retry = await unlockLocalWallet(label, passphrase, storage);
    expect(retry.xpub).toBe(created.xpub);
  });

  it('removes the wallet from the label-keyed store', () => {
    removeLocalWallet(label, storage);
    expect(hasLocalWallet(label, storage)).toBe(false);
    expect(listLocalWallets(storage)).toEqual([]);
  });
});

describe('local wallet recovery path (import an existing mnemonic)', () => {
  it('re-derives the same xpub/peerId when the same mnemonic is imported twice', async () => {
    const storage = memoryStorage();
    const { generateMnemonic, initHDWallet } = await import('./crypto/hd-wallet');
    await initHDWallet();
    const mnemonic = await generateMnemonic({ wordCount: 24 });

    const first = await createLocalWallet('recovered', 'fixture-passphrase-one', { mnemonic }, storage);
    removeLocalWallet('recovered', storage);
    const second = await createLocalWallet('recovered', 'fixture-passphrase-two', { mnemonic }, storage);

    expect(second.xpub).toBe(first.xpub);
    expect(second.peerId).toBe(first.peerId);
  }, 20_000);
});
