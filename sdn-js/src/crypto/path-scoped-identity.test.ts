import { describe, expect, it } from 'vitest';

import { initHDWallet } from './hd-wallet';
import {
  PATH_SCOPED_IDENTITY_INFO_V1,
  PATH_SCOPED_IDENTITY_SALT_V1,
  PATH_SCOPED_IDENTITY_SEED_BYTES,
  canonicalizePathScopeUuid,
  derivePathScopedIdentity,
  derivePathScopedIdentityForPath,
  derivePathScopedSeed,
  extractPathScopeUuid,
} from './path-scoped-identity';

const UUID_A = '5168998c-c1da-4048-b3c1-d7b1df1450c5';
const UUID_B = '00000000-0000-4000-8000-000000000000';

function hex(bytes: Uint8Array): string {
  return Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}

describe('path-scoped module identity — spec constants', () => {
  // These strings are the registration contract. If one of them changes, every
  // registered xpub in every PLG.ALLOWED_XPUBS becomes wrong, so pin them.
  it('pins the versioned domain separation strings', () => {
    expect(PATH_SCOPED_IDENTITY_INFO_V1).toBe('sdn/sandcastle-module-identity/v1');
    expect(PATH_SCOPED_IDENTITY_SALT_V1).toBe(
      'sdn/sandcastle-module-identity/salt/v1',
    );
    expect(PATH_SCOPED_IDENTITY_SEED_BYTES).toBe(64);
  });
});

describe('canonicalizePathScopeUuid', () => {
  it('accepts the canonical form and lowercases', () => {
    expect(canonicalizePathScopeUuid(UUID_A)).toBe(UUID_A);
    expect(canonicalizePathScopeUuid(UUID_A.toUpperCase())).toBe(UUID_A);
    expect(canonicalizePathScopeUuid(`  ${UUID_A}  `)).toBe(UUID_A);
  });

  it('refuses non-canonical spellings rather than guessing', () => {
    expect(canonicalizePathScopeUuid(UUID_A.replace(/-/g, ''))).toBeNull();
    expect(canonicalizePathScopeUuid(`{${UUID_A}}`)).toBeNull();
    expect(canonicalizePathScopeUuid(`urn:uuid:${UUID_A}`)).toBeNull();
    expect(canonicalizePathScopeUuid('not-a-uuid')).toBeNull();
    expect(canonicalizePathScopeUuid(undefined)).toBeNull();
    expect(canonicalizePathScopeUuid(12345)).toBeNull();
  });
});

describe('extractPathScopeUuid', () => {
  it('finds the uuid in a private deployment path', () => {
    expect(extractPathScopeUuid(`/OrbPro/private/${UUID_A}/sandcastle/`)).toBe(
      UUID_A,
    );
    expect(extractPathScopeUuid(`/private/${UUID_A}`)).toBe(UUID_A);
    expect(
      extractPathScopeUuid(
        `https://digitalarsenal.github.io/OrbPro/private/${UUID_A}/sandcastle/index.html?id=hpop-propagation`,
      ),
    ).toBe(UUID_A);
    expect(
      extractPathScopeUuid(`/OrbPro/private/${UUID_A.toUpperCase()}/sandcastle/`),
    ).toBe(UUID_A);
  });

  it('fails closed everywhere else', () => {
    // Public gallery, local dev, and near-misses must all keep the existing
    // random-per-load identity.
    expect(extractPathScopeUuid('/OrbPro/sandcastle/')).toBeNull();
    expect(extractPathScopeUuid('/')).toBeNull();
    expect(extractPathScopeUuid(`/public/${UUID_A}/sandcastle/`)).toBeNull();
    expect(extractPathScopeUuid(`/privateer/${UUID_A}/`)).toBeNull();
    expect(extractPathScopeUuid('/private/not-a-uuid/sandcastle/')).toBeNull();
    expect(extractPathScopeUuid('')).toBeNull();
    expect(extractPathScopeUuid(null)).toBeNull();
    expect(extractPathScopeUuid('http://[::1')).toBeNull();
  });
});

describe('derivePathScopedSeed', () => {
  it('is deterministic, 64 bytes, and distinct per uuid', async () => {
    expect(await initHDWallet()).toBe(true);
    const a1 = await derivePathScopedSeed(UUID_A);
    const a2 = await derivePathScopedSeed(UUID_A.toUpperCase());
    const b1 = await derivePathScopedSeed(UUID_B);

    expect(a1.length).toBe(PATH_SCOPED_IDENTITY_SEED_BYTES);
    expect(hex(a2)).toBe(hex(a1));
    expect(hex(b1)).not.toBe(hex(a1));
  });

  it('throws on a non-canonical uuid instead of deriving a stranger', async () => {
    await expect(derivePathScopedSeed('nope')).rejects.toThrow(/canonical/i);
  });

  it('matches the locked HKDF-SHA256 vector for the spec strings', async () => {
    expect(await initHDWallet()).toBe(true);
    // Independent RFC 5869 check: HKDF-SHA256(ikm=utf8(uuid),
    // salt=utf8(SALT_V1), info=utf8(INFO_V1), L=64) computed with WebCrypto,
    // proving the WASM backend and a second implementation agree. Two
    // implementations agreeing is what makes this spec portable.
    const enc = new TextEncoder();
    const key = await crypto.subtle.importKey(
      'raw',
      enc.encode(UUID_A),
      'HKDF',
      false,
      ['deriveBits'],
    );
    const bits = await crypto.subtle.deriveBits(
      {
        name: 'HKDF',
        hash: 'SHA-256',
        salt: enc.encode(PATH_SCOPED_IDENTITY_SALT_V1),
        info: enc.encode(PATH_SCOPED_IDENTITY_INFO_V1),
      },
      key,
      PATH_SCOPED_IDENTITY_SEED_BYTES * 8,
    );
    const expected = new Uint8Array(bits);
    const actual = await derivePathScopedSeed(UUID_A);
    expect(hex(actual)).toBe(hex(expected));
  });
});

describe('derivePathScopedIdentity', () => {
  it('yields the same peerId/xpub/keys for the same uuid', async () => {
    expect(await initHDWallet()).toBe(true);
    const first = await derivePathScopedIdentity(UUID_A);
    const second = await derivePathScopedIdentity(UUID_A);

    expect(first.xpub).toBeTruthy();
    expect(second.xpub).toBe(first.xpub);
    expect(second.peerId).toBe(first.peerId);
    expect(hex(second.signingKey.publicKey)).toBe(hex(first.signingKey.publicKey));
    expect(hex(second.encryptionKey.publicKey)).toBe(
      hex(first.encryptionKey.publicKey),
    );
  });

  it('is a rotation boundary: a different uuid is a different identity', async () => {
    expect(await initHDWallet()).toBe(true);
    const a = await derivePathScopedIdentity(UUID_A);
    const b = await derivePathScopedIdentity(UUID_B);
    // This is the revocation property: rotating the UUID rotates the identity,
    // so the old registration stops working.
    expect(b.xpub).not.toBe(a.xpub);
    expect(b.peerId).not.toBe(a.peerId);
  });

  it('separates accounts', async () => {
    expect(await initHDWallet()).toBe(true);
    const account0 = await derivePathScopedIdentity(UUID_A, 0);
    const account1 = await derivePathScopedIdentity(UUID_A, 1);
    expect(account1.xpub).not.toBe(account0.xpub);
  });
});

describe('derivePathScopedIdentityForPath', () => {
  it('derives under /private/<uuid>/ and returns null elsewhere', async () => {
    expect(await initHDWallet()).toBe(true);
    const fromPath = await derivePathScopedIdentityForPath(
      `/OrbPro/private/${UUID_A}/sandcastle/`,
    );
    const direct = await derivePathScopedIdentity(UUID_A);
    expect(fromPath?.xpub).toBe(direct.xpub);

    // null means "keep the random per-load identity" — the public gallery must
    // not silently share one identity across every visitor.
    expect(await derivePathScopedIdentityForPath('/OrbPro/sandcastle/')).toBeNull();
  });
});
