import { describe, it, expect, beforeAll } from 'vitest';

import { ChannelKeys } from './channel-keys';
import { EciesKeyExchange } from './ecies';
import {
  initHDWallet,
  x25519PublicKey,
  secp256k1PublicKey,
} from './crypto/hd-wallet';

const toHex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
function hex(s: string): Uint8Array {
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(i * 2, i * 2 + 2), 16);
  return out;
}

interface Party {
  id: string;
  priv: Uint8Array;
  pub: Uint8Array;
  kx: EciesKeyExchange;
}

async function party(id: string, seed: string, kx: EciesKeyExchange): Promise<Party> {
  const priv = hex(seed.repeat(32).slice(0, 64));
  const pub = kx === EciesKeyExchange.Secp256k1
    ? await secp256k1PublicKey(priv)
    : await x25519PublicKey(priv);
  return { id, priv, pub, kx };
}

describe('ChannelKeys — group-chat key management', () => {
  beforeAll(async () => {
    await initHDWallet();
  });

  it('wraps the channel content key one-to-many to a mixed-curve member set', async () => {
    const ch = await ChannelKeys.create('chat-room-1');
    expect(ch.epoch).toBe(1);
    expect(ch.getContentKey().length).toBe(32);

    const alice = await party('alice', '11', EciesKeyExchange.X25519);
    const bob = await party('bob', '22', EciesKeyExchange.Secp256k1); // mixed curve
    const carol = await party('carol', '33', EciesKeyExchange.X25519);
    for (const p of [alice, bob, carol]) {
      ch.addMember({ id: p.id, publicKey: p.pub, keyExchange: p.kx });
    }

    const envs = await ch.wrapForMembers();
    expect(envs.length).toBe(3);
    const want = toHex(ch.getContentKey());
    const byId = new Map(envs.map((e) => [e.memberId, e]));

    for (const p of [alice, bob, carol]) {
      const e = byId.get(p.id)!;
      expect(e.epoch).toBe(1);
      const got = await ChannelKeys.unwrapForMember(p.priv, e.encBytes, e.kmfBytes, ch.context);
      expect(toHex(got)).toBe(want);
    }

    // Non-member can't recover the key from someone else's envelope.
    const mallory = await party('mallory', '44', EciesKeyExchange.X25519);
    const aliceEnv = byId.get('alice')!;
    const bad = await ChannelKeys.unwrapForMember(mallory.priv, aliceEnv.encBytes, aliceEnv.kmfBytes, ch.context);
    expect(toHex(bad)).not.toBe(want);
  });

  it('rekeys on member removal for forward secrecy', async () => {
    const ch = await ChannelKeys.create('chat-room-2');
    const alice = await party('alice', '55', EciesKeyExchange.X25519);
    const bob = await party('bob', '66', EciesKeyExchange.X25519);
    ch.addMember({ id: alice.id, publicKey: alice.pub, keyExchange: alice.kx });
    ch.addMember({ id: bob.id, publicKey: bob.pub, keyExchange: bob.kx });

    const before = toHex(ch.getContentKey());
    const env1 = await ch.wrapForMembers();

    ch.removeMember('bob');
    expect(ch.epoch).toBe(2);
    const after = toHex(ch.getContentKey());
    expect(after).not.toBe(before);

    const env2 = await ch.wrapForMembers();
    expect(env2.length).toBe(1);
    expect(env2[0].memberId).toBe('alice');
    const aliceNew = await ChannelKeys.unwrapForMember(alice.priv, env2[0].encBytes, env2[0].kmfBytes, ch.context);
    expect(toHex(aliceNew)).toBe(after);

    // Bob's OLD envelope still only yields the OLD key, never the rotated one.
    const bobOldEnv = env1.find((e) => e.memberId === 'bob')!;
    const bobOld = await ChannelKeys.unwrapForMember(bob.priv, bobOldEnv.encBytes, bobOldEnv.kmfBytes, ch.context);
    expect(toHex(bobOld)).toBe(before);
    expect(toHex(bobOld)).not.toBe(after);

    expect(() => ch.removeMember('bob')).toThrow();
  });

  it('guards empty/invalid inputs', async () => {
    const ch = await ChannelKeys.create('empty');
    await expect(ch.wrapForMembers()).rejects.toThrow();
    expect(() => ch.addMember({ id: '', publicKey: new Uint8Array([1]), keyExchange: EciesKeyExchange.X25519 })).toThrow();
    expect(() => ch.addMember({ id: 'x', publicKey: new Uint8Array(0), keyExchange: EciesKeyExchange.X25519 })).toThrow();
    await expect(ChannelKeys.create('')).rejects.toThrow();
  });
});
