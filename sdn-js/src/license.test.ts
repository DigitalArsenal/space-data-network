import { describe, expect, it } from 'vitest';

import {
  LicenseProtocolError,
  derivePeerIdFromEd25519Seed,
  parseLicenseResponse,
} from './license';

describe('license.parseLicenseResponse', () => {
  it('parses grant responses', () => {
    const payload = {
      type: 'grant_response',
      req_id: 'req-1',
      entitlement: {
        xpub: 'xpub-test',
        plan: 'free',
        status: 'active',
        updated_at: 1700000000,
      },
      capability_token: 'token',
      expires_at: 1700000900,
    };

    const parsed = parseLicenseResponse(new TextEncoder().encode(`${JSON.stringify(payload)}\n`));
    expect(parsed.type).toBe('grant_response');
    expect((parsed as { req_id: string }).req_id).toBe('req-1');
  });

  it('returns error responses without throwing', () => {
    const payload = {
      type: 'error_response',
      code: 'entitlement_inactive',
      message: 'subscription inactive',
    };

    const parsed = parseLicenseResponse(new TextEncoder().encode(JSON.stringify(payload)));
    expect(parsed).toEqual(payload);
  });

  it('throws for malformed json', () => {
    expect(() => parseLicenseResponse(new TextEncoder().encode('{not-json'))).toThrow(LicenseProtocolError);
  });

  it('throws for missing response type', () => {
    expect(() => parseLicenseResponse(new TextEncoder().encode('{"ok":true}'))).toThrow(LicenseProtocolError);
  });
});

describe('license.derivePeerIdFromEd25519Seed', () => {
  it('is deterministic for the same seed', async () => {
    const seed = new Uint8Array(32);
    for (let i = 0; i < seed.length; i++) {
      seed[i] = i;
    }

    const peerIdA = await derivePeerIdFromEd25519Seed(seed);
    const peerIdB = await derivePeerIdFromEd25519Seed(seed);

    expect(peerIdA).toBe(peerIdB);
    expect(peerIdA.length).toBeGreaterThan(20);
  });
});
