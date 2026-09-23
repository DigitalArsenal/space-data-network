import { describe, it, expect, beforeAll } from 'vitest';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { canonicalJson, decrypt, open, seal, type RpcSigner } from './sealed-rpc';
import { EciesKeyExchange } from './ecies';
import { ed25519PublicKey, initHDWallet, sign, x25519PublicKey } from './crypto/hd-wallet';

function hex(s: string): Uint8Array {
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(i * 2, i * 2 + 2), 16);
  return out;
}
const toHex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
const text = (b?: Uint8Array) => new TextDecoder().decode(b ?? new Uint8Array());

// Produced by sdn-server internal/sealed TestGoVector (fixed, test-only keys).
const goVector = JSON.parse(
  readFileSync(fileURLToPath(new URL('./testdata/sealed-go-vector.json', import.meta.url)), 'utf8'),
);

async function signerFrom(seed: Uint8Array): Promise<RpcSigner> {
  return { publicKey: await ed25519PublicKey(seed), sign: (m) => sign(seed, m) };
}

describe('$RPC sealed transport', () => {
  beforeAll(async () => {
    await initHDWallet();
  });

  it('opens and verifies a Go-sealed request byte-for-byte', async () => {
    const env = await open(hex(goVector.requestHex));
    expect(text(canonicalJson(env))).toBe(goVector.requestCanonicalJson);
    const body = await decrypt(env, hex(goVector.nodeEncPrivHex));
    expect(body.method).toBe(goVector.method);
    expect(body.route).toBe(goVector.route);
    expect(text(body.body)).toBe(goVector.body);
  });

  it('opens a Go-sealed reply signed by the node', async () => {
    const env = await open(hex(goVector.responseHex));
    expect(env.response).toBe(true);
    expect(toHex(env.signer)).toBe(goVector.nodeSignPubHex);
    const body = await decrypt(env, hex(goVector.replyPrivHex));
    expect(body.status).toBe(200);
    expect(text(body.body)).toBe('ok');
  });

  it('refuses a tampered envelope', async () => {
    const raw = hex(goVector.requestHex);
    raw[raw.length - 200] ^= 1;
    await expect(open(raw)).rejects.toThrow();
  });

  it('round-trips its own envelopes and writes the vector Go opens', async () => {
    const sessionSeed = new Uint8Array(32).fill(0x55);
    const signer = await signerFrom(sessionSeed);
    const replyPub = await x25519PublicKey(hex(goVector.replyPrivHex));
    const envelope = await seal(
      { method: 'POST', route: '/api/auth/users', body: new TextEncoder().encode('{"name":"ops"}'), replyKey: replyPub },
      { recipientPublicKey: hex(goVector.nodeEncPubHex), keyExchange: EciesKeyExchange.Secp256k1, signer, sessionId: 's-js' },
    );
    const env = await open(envelope);
    const body = await decrypt(env, hex(goVector.nodeEncPrivHex));
    expect(body.route).toBe('/api/auth/users');
    // Opened with the wrong key, the plaintext is not recovered.
    await expect(decrypt(env, new Uint8Array(32).fill(0x66))).rejects.toThrow();

    const out = process.env.SEALED_JS_VECTOR_OUT;
    if (out) {
      writeFileSync(out, JSON.stringify({
        nodeEncPrivHex: goVector.nodeEncPrivHex,
        sessionPubHex: toHex(signer.publicKey),
        requestHex: toHex(envelope),
        Method: 'POST', Route: '/api/auth/users', Body: '{"name":"ops"}',
      }, null, 2));
    }
  });
});
