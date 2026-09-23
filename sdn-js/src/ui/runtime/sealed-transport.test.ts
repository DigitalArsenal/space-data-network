import { describe, it, expect, beforeAll } from 'vitest';
import {
  activate, beginSession, checkPin, delegationDigest, installSealedFetch, isRemoteDashboard, pinFingerprint, revoke, sealedActive, toHex,
} from './sealed-transport';
import { decrypt, open, seal } from '../../sealed-rpc';
import { EciesKeyExchange } from '../../ecies';
import { ed25519PublicKey, initHDWallet, secp256k1PublicKey, sign } from '../../crypto/hd-wallet';

function memoryStorage() {
  const m = new Map<string, string>();
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) };
}

describe('sealed admin session', () => {
  beforeAll(async () => {
    await initHDWallet();
  });

  it('computes the same delegation digest as the node', async () => {
    const d = await delegationDigest(new Uint8Array(32).fill(1), new Uint8Array(32).fill(2), 1_800_000_000_000);
    expect(toHex(d)).toBe('e99fd44556161b92b8a45f28428a516e960d4ec955c51405414e5da0f7eb47bf');
  });

  it('knows a remote dashboard from a local or desktop one', () => {
    expect(isRemoteDashboard({ hostname: 'sdn.example.org' }, {})).toBe(true);
    expect(isRemoteDashboard({ hostname: '10.0.0.5' }, {})).toBe(true);
    expect(isRemoteDashboard({ hostname: 'localhost' }, {})).toBe(false);
    expect(isRemoteDashboard({ hostname: '127.0.0.1' }, {})).toBe(false);
    expect(isRemoteDashboard({ hostname: '[::1]' }, {})).toBe(false);
    expect(isRemoteDashboard({ hostname: 'sdn.example.org' }, { sdn: { isDesktop: true } })).toBe(false);
  });

  it('pins a node fingerprint per origin', () => {
    const s = memoryStorage();
    expect(checkPin('http://a', 'aaaa:bbbb', s)).toBe('new');
    pinFingerprint('http://a', 'aaaa:bbbb', s);
    expect(checkPin('http://a', 'aaaa:bbbb', s)).toBe('match');
    expect(checkPin('http://a', 'cccc:dddd', s)).toBe('changed');
    expect(checkPin('http://b', 'aaaa:bbbb', s)).toBe('new');
  });

  it('seals same-origin /api/ calls and leaves everything else alone', async () => {
    // A stand-in node: opens the sealed command, answers sealed and signed.
    const nodeEncPriv = new Uint8Array(32).fill(0x11);
    const nodeSignSeed = new Uint8Array(32).fill(0x22);
    const node = {
      encryptionKey: await secp256k1PublicKey(nodeEncPriv),
      signingKey: await ed25519PublicKey(nodeSignSeed),
      fingerprint: 'x',
    };
    const seen: string[] = [];
    const win: any = {
      location: { origin: 'http://node.test', href: 'http://node.test/' },
      async fetch(input: any, init?: RequestInit) {
        const url = String(input);
        seen.push(url);
        if (!url.endsWith('/api/rpc')) return new Response('plain', { status: 200 });
        const env = await open(new Uint8Array(init!.body as ArrayBuffer));
        const body = await decrypt(env, nodeEncPriv);
        const reply = await seal(
          { status: 200, body: new TextEncoder().encode(`${body.method} ${body.route} ${new TextDecoder().decode(body.body ?? new Uint8Array())}`), bodyFileId: 'text/plain', requestNonce: env.nonce, requestDigest: new Uint8Array(await crypto.subtle.digest('SHA-256', env.raw)) },
          { response: true, recipientPublicKey: body.replyKey!, keyExchange: EciesKeyExchange.X25519, signer: { publicKey: node.signingKey, sign: (m) => sign(nodeSignSeed, m) } },
        );
        return new Response(reply, { status: 200 });
      },
    };
    const uninstall = installSealedFetch(win);
    const pending = await beginSession();
    activate('http://node.test', node, pending);
    expect(sealedActive()).toBe(true);

    const sealedRes = await win.fetch('/api/node/epm?x=1', { method: 'PUT', body: 'renamed' });
    expect(await sealedRes.text()).toBe('PUT /api/node/epm?x=1 renamed');
    expect(seen.at(-1)).toBe('http://node.test/api/rpc');

    for (const path of ['/api/auth/challenge', '/api/node/info', '/ipfs/bafy', 'http://elsewhere.test/api/x']) {
      await win.fetch(path);
      expect(seen.at(-1)).not.toBe('http://node.test/api/rpc');
    }

    await revoke(win.fetch);
    expect(sealedActive()).toBe(false);
    uninstall();
  });
});
