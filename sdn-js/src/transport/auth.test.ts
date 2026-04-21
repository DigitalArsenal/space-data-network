import { afterEach, describe, expect, it, vi } from 'vitest';

const { sign } = vi.hoisted(() => ({
  sign: vi.fn(async () => new Uint8Array(64).fill(7)),
}));

vi.mock('../crypto/index', () => ({
  sign,
}));

import { SessionAuth } from './auth';

describe('SessionAuth', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('authenticates without ever sending xpub on the wire', async () => {
    const fetch = vi.fn(async (_input: string, init?: RequestInit) => {
      const url = String(_input);
      if (url.endsWith('/api/auth/challenge')) {
        return jsonResponse(200, {
          challenge_id: 'challenge-1',
          challenge: 'AQIDBA',
          expires_at: Math.floor(Date.now() / 1000) + 60,
        });
      }
      if (url.endsWith('/api/auth/verify')) {
        return jsonResponse(200, {
          user: {
            name: 'Redacted Admin',
            trust_level: 'admin',
          },
          expires_at: Math.floor(Date.now() / 1000) + 3600,
        });
      }
      throw new Error(`unexpected fetch ${url} ${JSON.stringify(init ?? {})}`);
    });

    vi.stubGlobal('fetch', fetch);

    const identity = {
      xpub: 'xpub-should-not-leak',
      signingKey: {
        privateKey: new Uint8Array(32).fill(9),
        publicKey: new Uint8Array(32).fill(5),
      },
    } as any;

    const auth = new SessionAuth('https://node.example', identity);
    await auth.authenticate();

    expect(fetch).toHaveBeenCalledTimes(2);
    expect(sign).toHaveBeenCalledWith(identity.signingKey.privateKey, new Uint8Array([1, 2, 3, 4]));

    const challengeBody = parseBody(fetch.mock.calls[0]?.[1]);
    expect(challengeBody).not.toHaveProperty('xpub');
    expect(challengeBody).toMatchObject({
      client_pubkey_hex: '0505050505050505050505050505050505050505050505050505050505050505',
    });

    const verifyBody = parseBody(fetch.mock.calls[1]?.[1]);
    expect(verifyBody).not.toHaveProperty('xpub');
    expect(verifyBody).toMatchObject({
      challenge_id: 'challenge-1',
      client_pubkey_hex: '0505050505050505050505050505050505050505050505050505050505050505',
      challenge: 'AQIDBA',
      signature_hex: '07070707070707070707070707070707070707070707070707070707070707070707070707070707070707070707070707070707070707070707070707070707',
    });
  });
});

function jsonResponse(status: number, payload: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async json() {
      return payload;
    },
    async text() {
      return JSON.stringify(payload);
    },
  };
}

function parseBody(init?: RequestInit) {
  return JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
}
