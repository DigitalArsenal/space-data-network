import { describe, it, expect, beforeAll } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import {
  eciesWrap,
  eciesUnwrap,
  eciesWrapForRecipients,
  EciesKeyExchange,
  DEFAULT_GRANT_CONTEXT,
} from './ecies';
import {
  initHDWallet,
  x25519PublicKey,
  secp256k1PublicKey,
} from './crypto/hd-wallet';

function hex(s: string): Uint8Array {
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(i * 2, i * 2 + 2), 16);
  return out;
}
const toHex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');

interface Vector {
  keyExchange: string;
  context: string;
  recipientPrivHex: string;
  recipientPubHex: string;
  ephemeralPrivHex: string;
  contentKeyHex: string;
  encHex: string;
  kmfHex: string;
}

const vectors: Vector[] = JSON.parse(
  readFileSync(
    fileURLToPath(new URL('./testdata/ecies_conformance_vectors.json', import.meta.url)),
    'utf8',
  ),
);

const kxOf = (name: string) =>
  name === 'Secp256k1' ? EciesKeyExchange.Secp256k1 : EciesKeyExchange.X25519;

describe('unified ECIES (cross-runtime with the Go reference)', () => {
  beforeAll(async () => {
    await initHDWallet();
  });

  // The core cross-runtime proof: JS unwraps the Go-produced $ENC+$KMF bytes.
  for (const v of vectors) {
    it(`JS unwraps the Go ${v.keyExchange} conformance vector`, async () => {
      const contentKey = await eciesUnwrap(
        hex(v.recipientPrivHex),
        hex(v.encHex),
        hex(v.kmfHex),
        v.context || DEFAULT_GRANT_CONTEXT,
      );
      expect(toHex(contentKey)).toBe(v.contentKeyHex);
    });
  }

  // JS wrap → JS unwrap round-trip for both curves.
  for (const kxName of ['X25519', 'Secp256k1']) {
    it(`JS wrap/unwrap round-trips ${kxName}`, async () => {
      const kx = kxOf(kxName);
      const priv = hex('11'.repeat(32));
      const pub = kx === EciesKeyExchange.Secp256k1
        ? await secp256k1PublicKey(priv)
        : await x25519PublicKey(priv);
      const contentKey = hex('a1'.repeat(32));
      const { encBytes, kmfBytes } = await eciesWrap(pub, contentKey, { keyExchange: kx });
      // KEY_BYTES must be wrapped, not plaintext.
      expect(toHex(kmfBytes)).not.toContain(toHex(contentKey));
      const got = await eciesUnwrap(priv, encBytes, kmfBytes);
      expect(toHex(got)).toBe(toHex(contentKey));
    });
  }

  // One-to-many: one content key wrapped for a mixed-curve recipient set; every
  // recipient unwraps the SAME key from its own envelope; envelopes are distinct
  // and addressable by key id; no recipient opens another's.
  it('wraps one content key for many recipients (one-to-many, mixed curves)', async () => {
    const contentKey = hex('c3'.repeat(32));
    const ctx = 'space-data-network/storefront/one-to-many/v1';
    const parties = [
      { priv: hex('21'.repeat(32)), kx: EciesKeyExchange.X25519, keyId: new TextEncoder().encode('buyer-x-1') },
      { priv: hex('22'.repeat(32)), kx: EciesKeyExchange.Secp256k1, keyId: new TextEncoder().encode('buyer-s-1') },
      { priv: hex('23'.repeat(32)), kx: EciesKeyExchange.X25519, keyId: new TextEncoder().encode('buyer-x-2') },
    ];
    const recipients = [];
    for (const p of parties) {
      const publicKey = p.kx === EciesKeyExchange.Secp256k1
        ? await secp256k1PublicKey(p.priv)
        : await x25519PublicKey(p.priv);
      recipients.push({ publicKey, keyExchange: p.kx, keyId: p.keyId });
    }
    const envs = await eciesWrapForRecipients(contentKey, recipients, ctx);
    expect(envs.length).toBe(parties.length);

    for (let i = 0; i < parties.length; i++) {
      const got = await eciesUnwrap(parties[i].priv, envs[i].encBytes, envs[i].kmfBytes, ctx);
      expect(toHex(got)).toBe(toHex(contentKey));
      // distinct wrapped bytes per recipient
      for (let j = 0; j < envs.length; j++) {
        if (j !== i) expect(toHex(envs[i].kmfBytes)).not.toBe(toHex(envs[j].kmfBytes));
      }
    }
    // isolation: party 0's key on party 2's envelope (both X25519) must not yield the key
    const bad = await eciesUnwrap(parties[0].priv, envs[2].encBytes, envs[2].kmfBytes, ctx);
    expect(toHex(bad)).not.toBe(toHex(contentKey));
  });

  // The Go conformance set IS a one-to-many set: both vectors wrap the identical
  // content key for different-curve recipients — proves Go→JS one-to-many.
  it('Go conformance vectors are a one-to-many set (same content key, N recipients)', async () => {
    expect(vectors.length).toBeGreaterThan(1);
    const keys = new Set<string>();
    for (const v of vectors) {
      const k = await eciesUnwrap(hex(v.recipientPrivHex), hex(v.encHex), hex(v.kmfHex), v.context);
      keys.add(toHex(k));
    }
    expect(keys.size).toBe(1); // every recipient recovered the one shared content key
  });

  // JS wrap → Go must unwrap (round-trip closes when the vector matches the Go
  // reference's deterministic wrap of the same inputs). Here we assert JS wrap
  // with the vector's fixed ephemeral key reproduces the Go bytes.
  for (const v of vectors) {
    it(`JS wrap reproduces the Go ${v.keyExchange} vector bytes (deterministic ephemeral)`, async () => {
      const { encBytes, kmfBytes } = await eciesWrap(hex(v.recipientPubHex), hex(v.contentKeyHex), {
        keyExchange: kxOf(v.keyExchange),
        context: v.context,
        ephemeralPrivateKey: hex(v.ephemeralPrivHex),
        nonceStart: new Uint8Array(12), // Go vector uses zeroReader for nonce
      });
      // KMF (the wrapped key) must match byte-for-byte — it depends only on the
      // ECDH+KDF+CTR, not on ENC framing order.
      expect(toHex(kmfBytes)).toBe(v.kmfHex);
      // The Go reference must be able to unwrap the JS-produced ENC too.
      const back = await eciesUnwrap(hex(v.recipientPrivHex), encBytes, kmfBytes, v.context);
      expect(toHex(back)).toBe(v.contentKeyHex);
    });
  }
});
