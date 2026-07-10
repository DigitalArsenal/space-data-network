/**
 * Unit tests for the SpaceAware typed API client (loop task U0.3).
 *
 * Exercises the gateway/auth wire conventions from SDN_SPACEAWARE_UI_LOOP.md
 * GROUND TRUTH: bare top-level JSON arrays (no envelope), `X-SDN-Record-Count`
 * / `ETag` / `If-None-Match`, 401 JSON error shape, schema-exact key
 * passthrough, and the auth-endpoint request/response wire shapes matching
 * `sdn-server/internal/auth/handler.go`.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SdnApiClient, SdnApiError } from '../ui/src/lib/auth/sdn-api-client';

const SERVER_BASE_URL = 'http://127.0.0.1:9999';

function client(fetchImpl: typeof fetch): SdnApiClient {
  return new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, apiBase: '/api/v1', fetchImpl });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('SdnApiClient.requestBareArray', () => {
  it('parses a bare top-level JSON array and reads the record count from X-SDN-Record-Count', async () => {
    const fixture = [
      { NORAD_CAT_ID: 25544, OBJECT_NAME: 'ISS (ZARYA)' },
      { NORAD_CAT_ID: 43013, OBJECT_NAME: 'CELESTRAK-1' },
    ];
    const fetchImpl = vi.fn(async () =>
      new Response(JSON.stringify(fixture), {
        status: 200,
        headers: { 'content-type': 'application/json', 'x-sdn-record-count': '2', etag: 'W/"fnv1a64-abc"' },
      }),
    );

    const result = await client(fetchImpl as unknown as typeof fetch).requestBareArray('/peers');

    expect(result.status).toBe(200);
    expect(result.notModified).toBe(false);
    expect(result.recordCount).toBe(2);
    expect(result.etag).toBe('W/"fnv1a64-abc"');
    // Bare array, not {"records":[...]}
    expect(result.records).toEqual(fixture);
  });

  it('preserves SDS record keys byte-exact (schema-exact capitalization, no key transformation)', async () => {
    // Fixture uses the exact OMM/EPM casing the loop's hard rule mandates
    // (NORAD_CAT_ID, not norad_cat_id) — the client must never rename keys.
    const fixture = [
      {
        NORAD_CAT_ID: 25544,
        OBJECT_NAME: 'ISS (ZARYA)',
        MEAN_MOTION: 15.49560532,
        EPOCH: '2026-07-06T12:00:00Z',
        dn: 'operator.example',
        peer_id: '12D3KooWTest',
      },
    ];
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify(fixture), { status: 200 }));

    const result = await client(fetchImpl as unknown as typeof fetch).requestBareArray<Record<string, unknown>>(
      '/standards',
    );

    const record = result.records[0];
    expect(Object.keys(record)).toEqual(Object.keys(fixture[0]));
    expect(record.NORAD_CAT_ID).toBe(25544);
    expect(record.MEAN_MOTION).toBe(15.49560532);
    expect(record).not.toHaveProperty('norad_cat_id');
    expect(record).not.toHaveProperty('normCatId');
  });

  it('falls back to records.length when X-SDN-Record-Count is absent', async () => {
    const fixture = [{ id: 1 }, { id: 2 }, { id: 3 }];
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify(fixture), { status: 200 }));

    const result = await client(fetchImpl as unknown as typeof fetch).requestBareArray('/peers');

    expect(result.recordCount).toBe(3);
  });

  it('sends If-None-Match and treats a 304 as notModified without parsing a body', async () => {
    let capturedHeaders: Headers | undefined;
    const fetchImpl = vi.fn(async (_url: string, init?: RequestInit) => {
      capturedHeaders = new Headers(init?.headers);
      // 304 is a null-body status per the Fetch spec (the Response
      // constructor rejects a body here) — this also proves the client
      // cannot be calling .json() on it without throwing.
      return new Response(null, { status: 304, headers: { etag: 'W/"fnv1a64-abc"' } });
    });

    const result = await client(fetchImpl as unknown as typeof fetch).requestBareArray('/peers', {
      ifNoneMatch: 'W/"fnv1a64-abc"',
    });

    expect(capturedHeaders?.get('if-none-match')).toBe('W/"fnv1a64-abc"');
    expect(result.notModified).toBe(true);
    expect(result.status).toBe(304);
    expect(result.records).toEqual([]);
  });

  it('resolves gateway paths under the configured apiBase', async () => {
    let capturedUrl = '';
    const fetchImpl = vi.fn(async (url: string) => {
      capturedUrl = url;
      return new Response('[]', { status: 200 });
    });

    await client(fetchImpl as unknown as typeof fetch).requestBareArray('/peers');

    expect(capturedUrl).toBe(`${SERVER_BASE_URL}/api/v1/peers`);
  });
});

describe('SdnApiClient 401 handling', () => {
  it('throws SdnApiError with the server error-body shape ({"code","message"}) and never redirects', async () => {
    const fetchImpl = vi.fn(async () =>
      Response.json({ code: 'unauthorized', message: 'not authenticated' }, { status: 401 }),
    );

    const c = client(fetchImpl as unknown as typeof fetch);
    await expect(c.authMe()).rejects.toMatchObject({
      name: 'SdnApiError',
      status: 401,
      code: 'unauthorized',
      message: 'not authenticated',
    });
    await expect(c.authMe()).rejects.toBeInstanceOf(SdnApiError);
  });

  it('exposes isUnauthorized for 401s and not for other errors', async () => {
    const fetchImpl = vi.fn(async () =>
      Response.json({ code: 'forbidden', message: 'admin access required' }, { status: 403 }),
    );
    try {
      await client(fetchImpl as unknown as typeof fetch).authMe();
      throw new Error('expected authMe to throw');
    } catch (err) {
      expect(err).toBeInstanceOf(SdnApiError);
      expect((err as SdnApiError).isUnauthorized).toBe(false);
      expect((err as SdnApiError).status).toBe(403);
    }
  });
});

describe('SdnApiClient auth surface wire shapes (sdn-server/internal/auth/handler.go)', () => {
  it('POSTs /api/auth/challenge at the server root (not apiBase-prefixed) with the exact field names', async () => {
    let capturedUrl = '';
    let capturedBody: unknown;
    const fetchImpl = vi.fn(async (url: string, init?: RequestInit) => {
      capturedUrl = url;
      capturedBody = JSON.parse(String(init?.body ?? '{}'));
      return Response.json({ challenge_id: 'c1', challenge: 'YWJj', expires_at: 1234 });
    });

    const resp = await client(fetchImpl as unknown as typeof fetch).authChallenge({
      xpub: 'xpubTESTFIXTUREnotarealkey',
      client_pubkey_hex: 'aa'.repeat(32),
      ts: 1720000000,
    });

    expect(capturedUrl).toBe(`${SERVER_BASE_URL}/api/auth/challenge`);
    expect(capturedBody).toEqual({
      xpub: 'xpubTESTFIXTUREnotarealkey',
      client_pubkey_hex: 'aa'.repeat(32),
      ts: 1720000000,
    });
    expect(resp).toEqual({ challenge_id: 'c1', challenge: 'YWJj', expires_at: 1234 });
  });

  it('POSTs /api/auth/verify with the exact field names and returns the PGP-vocabulary trust_level', async () => {
    let capturedBody: unknown;
    const fetchImpl = vi.fn(async (_url: string, init?: RequestInit) => {
      capturedBody = JSON.parse(String(init?.body ?? '{}'));
      return Response.json({ user: { name: 'Test Operator', trust_level: 'full' }, expires_at: 999 });
    });

    const resp = await client(fetchImpl as unknown as typeof fetch).authVerify({
      challenge_id: 'c1',
      xpub: 'xpubTESTFIXTUREnotarealkey',
      client_pubkey_hex: 'aa'.repeat(32),
      challenge: 'YWJj',
      signature_hex: 'bb'.repeat(64),
    });

    expect(capturedBody).toEqual({
      challenge_id: 'c1',
      xpub: 'xpubTESTFIXTUREnotarealkey',
      client_pubkey_hex: 'aa'.repeat(32),
      challenge: 'YWJj',
      signature_hex: 'bb'.repeat(64),
    });
    expect(resp.user.trust_level).toBe('full');
  });

  it('reads apiBase/serverBaseUrl from window.__SDN_CONFIG__ when not overridden', async () => {
    vi.stubGlobal('window', {
      location: { origin: 'https://sdn.example.test' },
      __SDN_CONFIG__: { apiBase: '/api/v1', serverBaseUrl: 'https://sdn.example.test' },
    });
    let capturedUrl = '';
    const fetchImpl = vi.fn(async (url: string) => {
      capturedUrl = url;
      return new Response('[]', { status: 200 });
    });

    const c = new SdnApiClient({ fetchImpl: fetchImpl as unknown as typeof fetch });
    await c.requestBareArray('/peers');

    expect(capturedUrl).toBe('https://sdn.example.test/api/v1/peers');
  });
});
