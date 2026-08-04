/**
 * Loop D.3 — HTTP transport flatbuffers-first.
 *
 * `queryData` defaults to ONE request against the flow-served bulk endpoint
 * (`GET /api/v1/data/<standard>/bulk`) and returns the aligned size-prefixed
 * FlatBuffer record stream verbatim for zero-copy consumption. JSON is the
 * opt-in edge adapter. Conditional requests ride `If-None-Match`/304.
 *
 * The mocked responses reproduce the REAL flow-served shapes asserted by the
 * Go integration test (sdn-server internal/flowrt/http_mount_integration_test.go):
 * Content-Type `application/vnd.sdn.flatbuffers.stream`, u32 LE size-prefixed
 * frames, `ETag: W/"fnv1a64-<hex>"`, `X-SDN-Record-Count`, 304 on a matching
 * `If-None-Match`, and the json branch's `{count, records}` envelope.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  FLATBUFFER_STREAM_CONTENT_TYPE,
  HttpTransport,
  SDNTransportError,
  iterateSizePrefixedFrames,
} from './http';

const BASE = 'https://sdn.example.test';

function alignedStream(frames: Uint8Array[]): Uint8Array {
  const total = frames.reduce((sum, frame) => sum + 4 + frame.byteLength, 0);
  const bytes = new Uint8Array(total);
  const view = new DataView(bytes.buffer);
  let offset = 0;
  for (const frame of frames) {
    view.setUint32(offset, frame.byteLength, true); // LITTLE-ENDIAN — the engine/server framing
    offset += 4;
    bytes.set(frame, offset);
    offset += frame.byteLength;
  }
  return bytes;
}

function streamResponse(frames: Uint8Array[], etag: string): Response {
  return new Response(alignedStream(frames).slice().buffer as ArrayBuffer, {
    status: 200,
    headers: {
      'Content-Type': FLATBUFFER_STREAM_CONTENT_TYPE,
      'X-SDN-Record-Count': String(frames.length),
      'X-SDN-Stream-Format': 'flatsql-size-prefixed-le-u32',
      ETag: etag,
    },
  });
}

function notModifiedResponse(etag: string): Response {
  return new Response(null, { status: 304, headers: { ETag: etag } });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('HttpTransport.queryData (flatbuffers-first, loop D.3)', () => {
  it('defaults to ONE flow-endpoint stream request and returns the aligned stream verbatim', async () => {
    const frames = [new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8]), new Uint8Array([9, 10, 11, 12])];
    const etag = 'W/"fnv1a64-0123456789abcdef"';
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      return streamResponse(frames, etag);
    }));

    const transport = new HttpTransport(BASE);
    const result = await transport.queryData({
      schema: 'OMM.fbs',
      profile: 'nearest',
      epoch: 1778500800.5,
      limit: 100,
      source: 'celestrak-gp',
    });

    // ONE request, to the flow-served bulk endpoint, asking for the stream.
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(
      `${BASE}/api/v1/data/omm/bulk?source=celestrak-gp&profile=nearest&epoch=1778500800.5&limit=100`,
    );
    expect((calls[0].init?.headers as Record<string, string>).Accept).toBe(FLATBUFFER_STREAM_CONTENT_TYPE);
    expect((calls[0].init?.headers as Record<string, string>)['If-None-Match']).toBeUndefined();

    expect(result.format).toBe('flatbuffers');
    expect(result.status).toBe(200);
    expect(result.notModified).toBe(false);
    expect(result.etag).toBe(etag);
    expect(result.recordCount).toBe(2);
    expect(result.stream).toEqual(alignedStream(frames));

    // frames() yields zero-copy subarray views into the stream.
    const decoded = [...result.frames()];
    expect(decoded.map((frame) => [...frame])).toEqual(frames.map((frame) => [...frame]));
    expect(decoded[0].buffer).toBe(result.stream.buffer);
  });

  it('sends If-None-Match and reports 304 as notModified with an empty stream', async () => {
    const etag = 'W/"fnv1a64-feedfacecafebeef"';
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      return notModifiedResponse(etag);
    }));

    const transport = new HttpTransport(BASE);
    const result = await transport.queryData({ schema: 'OMM', ifNoneMatch: etag });

    expect(calls).toHaveLength(1);
    expect((calls[0].init?.headers as Record<string, string>)['If-None-Match']).toBe(etag);
    expect(result.notModified).toBe(true);
    expect(result.status).toBe(304);
    expect(result.etag).toBe(etag);
    expect(result.stream.byteLength).toBe(0);
    expect([...result.frames()]).toEqual([]);
  });

  it('treats json as the opt-in edge adapter (bare top-level array, count from header)', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      // The wire contract: a BARE top-level array of records; the record
      // count travels in x-sdn-record-count exactly like the fb path.
      // Property names are SCHEMA-EXACT (hard rule
      // json-schema-capitalization-rule): the IDL capitalization, never
      // lowercased.
      return new Response(JSON.stringify([
        { NORAD_CAT_ID: 1001, OBJECT_NAME: 'SAT-1001' },
        { NORAD_CAT_ID: 1002, OBJECT_NAME: 'SAT-1002' },
      ]), {
        status: 200,
        headers: { 'Content-Type': 'application/json', 'X-SDN-Record-Count': '2' },
      });
    }));

    const transport = new HttpTransport(BASE);
    const result = await transport.queryData({ schema: 'omm', format: 'json', limit: 2 });

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(`${BASE}/api/v1/data/omm/bulk?limit=2&format=json`);
    expect((calls[0].init?.headers as Record<string, string>).Accept).toBe('application/json');
    expect(result.format).toBe('json');
    expect(result.count).toBe(2);
    expect(result.records[1]).toEqual({ NORAD_CAT_ID: 1002, OBJECT_NAME: 'SAT-1002' });
  });

  it('json adapter falls back to array length when the count header is absent', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify([{ NORAD_CAT_ID: 1001 }]),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    )));

    const transport = new HttpTransport(BASE);
    const result = await transport.queryData({ schema: 'omm', format: 'json' });
    expect(result.count).toBe(1);
    expect(result.records).toHaveLength(1);
  });

  it('surfaces flow 404s as SDNTransportError', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ error: 'no route for GET /api/v1/data/nope/bulk' }),
      { status: 404, headers: { 'Content-Type': 'application/json' } },
    )));

    const transport = new HttpTransport(BASE);
    await expect(transport.queryData({ schema: 'nope' })).rejects.toBeInstanceOf(SDNTransportError);
  });

  it("defaults to credentials 'include' (session-cookie auth, historical behavior)", async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      return streamResponse([new Uint8Array([1, 2, 3, 4])], 'W/"fnv1a64-1"');
    }));

    await new HttpTransport(BASE).queryData({ schema: 'OMM' });

    expect(calls).toHaveLength(1);
    expect(calls[0].init?.credentials).toBe('include');
  });

  it("honors credentials 'omit' for anonymous cross-origin data queries", async () => {
    // Browsers block credentialed CORS responses unless the server sends
    // Access-Control-Allow-Credentials: true — SDN nodes do not on the
    // public data endpoints, so cross-origin consumers (e.g. the OrbPro
    // Sandcastle catalog loader) must be able to opt out of cookies.
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      return streamResponse([new Uint8Array([1, 2, 3, 4])], 'W/"fnv1a64-1"');
    }));

    await new HttpTransport(BASE, undefined, { credentials: 'omit' }).queryData({ schema: 'OMM' });

    expect(calls).toHaveLength(1);
    expect(calls[0].init?.credentials).toBe('omit');
  });
});

describe('HttpTransport.getRecord (raw bytes — the base64 envelope is dead)', () => {
  it('reads raw FlatBuffer bytes from the records endpoint', async () => {
    const payload = new Uint8Array([0xaa, 0xbb, 0xcc]);
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      return new Response(payload.slice().buffer as ArrayBuffer, {
        status: 200,
        headers: {
          'Content-Type': 'application/x-flatbuffers',
          'X-SDN-Schema': 'OMM.fbs',
          'X-SDN-Record-ID': 'cid-1',
        },
      });
    }));

    const transport = new HttpTransport(BASE);
    const bytes = await transport.getRecord('OMM.fbs', 'cid-1');
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(`${BASE}/api/v1/data/records/OMM.fbs/cid-1`);
    expect(bytes).toEqual(payload);
  });

  it('returns null for a missing record', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ error: { message: 'not found' } }),
      { status: 404, headers: { 'Content-Type': 'application/json' } },
    )));

    const transport = new HttpTransport(BASE);
    await expect(transport.getRecord('OMM.fbs', 'missing')).resolves.toBeNull();
  });
});

describe('iterateSizePrefixedFrames', () => {
  it('rejects truncated streams', () => {
    const stream = alignedStream([new Uint8Array([1, 2, 3])]).subarray(0, 5);
    expect(() => [...iterateSizePrefixedFrames(stream)]).toThrow(/truncated frame/);
  });
});

/**
 * Default-$APP discovery (owner ruling 2026-08-04). Every SDN runtime has a
 * default app — the Dashboard on a server node, the Orbital Console in this
 * browser client — both loaded and linking to each other. The client finds
 * them ANONYMOUSLY, before it has any identity.
 *
 * The mocked bodies reproduce the node's real shapes (sdn-server
 * internal/api/appsdefault.go + internal/apps): $APP record fields keep the
 * IDL's UPPER_SNAKE spelling, node-synthesized fields are lowercase.
 */
describe('HttpTransport default-$APP discovery', () => {
  const defaultsDocument = {
    generated_at: '2026-08-04T14:00:00Z',
    node_peer_id: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
    runtime_classes: ['server', 'browser'],
    defaults: {
      server: {
        runtime_class: 'server',
        state: 'installed',
        default: true,
        url: '/',
        record_url: '/api/v1/apps/records/sdn-dashboard',
        record_bytes: 553_952,
        cross_link: {
          runtime_class: 'browser',
          app_id: 'spaceaware-orbital-console',
          url: 'https://spaceaware.io/beta/',
        },
        ID: 'sdn-dashboard',
        NAME: 'SDN Node Dashboard',
        VERSION: '1.0.4',
        UI: [{ ID: 'dashboard', ENTRY: true, MEDIA_TYPE: 'text/html; charset=utf-8', ENCODING: 'UTF8' }],
      },
      browser: {
        runtime_class: 'browser',
        state: 'declared',
        default: true,
        url: 'https://spaceaware.io/beta/',
        cross_link: { runtime_class: 'server', app_id: 'sdn-dashboard', url: '/' },
        ID: 'spaceaware-orbital-console',
        NAME: 'Orbital Console',
      },
    },
  };

  it('reads both faces and their cross-links from the anonymous route', async () => {
    const calls: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      calls.push(url);
      return new Response(JSON.stringify(defaultsDocument), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }));

    const transport = new HttpTransport(BASE, undefined, { credentials: 'omit' });
    const doc = await transport.getDefaultApps();

    expect(calls).toEqual([`${BASE}/api/v1/apps/default`]);
    // The app THIS client opens, and the link back to the node's own face.
    expect(doc.defaults.browser?.ID).toBe('spaceaware-orbital-console');
    expect(doc.defaults.browser?.url).toBe('https://spaceaware.io/beta/');
    expect(doc.defaults.browser?.cross_link?.app_id).toBe('sdn-dashboard');
    expect(doc.defaults.server?.cross_link?.url).toBe('https://spaceaware.io/beta/');
    // Record fields keep IDL capitalization; synthesized fields are lowercase.
    expect(doc.defaults.server?.NAME).toBe('SDN Node Dashboard');
    expect(doc.defaults.server?.UI?.[0].CONTENT_SHA256).toBeUndefined();
    expect(doc.defaults.server?.state).toBe('installed');
  });

  it('fetches an installed app record as raw $APP bytes', async () => {
    const record = new Uint8Array([0xa4, 0xa9, 0x00, 0x00, 0x28, 0x00, 0x00, 0x00, 0x24, 0x41, 0x50, 0x50]);
    const calls: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      calls.push(url);
      return new Response(record.slice().buffer as ArrayBuffer, {
        status: 200,
        headers: { 'Content-Type': 'application/x-flatbuffers; schema=APP' },
      });
    }));

    const transport = new HttpTransport(BASE);
    const bytes = await transport.getAppRecord('sdn-dashboard');
    expect(calls).toEqual([`${BASE}/api/v1/apps/records/sdn-dashboard`]);
    expect(bytes).toEqual(record);
  });

  it('returns null for a DECLARED app — a pointer is not a record', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('404 page not found', { status: 404 })));
    const transport = new HttpTransport(BASE);
    await expect(transport.getAppRecord('spaceaware-orbital-console')).resolves.toBeNull();
  });
});
