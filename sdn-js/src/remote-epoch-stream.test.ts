/**
 * Loop D.3 — ONE conditional stream request into the REAL engine store.
 *
 * A mocked flow endpoint (exact server shapes from
 * sdn-server/internal/flowrt/http_mount_integration_test.go: stream
 * content-type, u32 LE aligned framing, `ETag: W/"fnv1a64-…"`,
 * `X-SDN-Record-Count`, 304 on matching `If-None-Match`) serves engine
 * streams recorded in shared-test-vectors/flatsql-parity.json — the same
 * byte-exact corpus the Go host's store produces. The client feeds the HTTP
 * body directly into a real FlatSQL-WASM engine store without re-encoding.
 * Conditional replay uses a bounded cache of those exact response bytes.
 */
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { describe, expect, it, vi } from 'vitest';

import { FlatSQLEngineRecordStore } from './engine-record-store';
import { RemoteEpochStreamClient } from './remote-epoch-stream';
import { FLATBUFFER_STREAM_CONTENT_TYPE, HttpTransport } from './transport/http';
import type { LocalFlatSqlSchema } from './local-flatsql';

const vectorsPath = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../shared-test-vectors/flatsql-parity.json',
);

interface ParityVectors {
  schema: string;
  fileId: string;
  table: string;
  epochStoreCorpus: {
    standardId: string;
    sources: Array<{ name: string; tagged: boolean; recordCount: number; streamBase64: string }>;
  };
  expected: {
    epochStoreStreams: Record<string, { sha256: string; byteLength: number; frameCount: number }>;
  };
}

const vectors = JSON.parse(readFileSync(vectorsPath, 'utf8')) as ParityVectors;
const EPOCH = 1778500800.5;

function sha256Hex(bytes: Uint8Array): string {
  return createHash('sha256').update(bytes).digest('hex');
}

async function openEngineStore(): Promise<FlatSQLEngineRecordStore> {
  const schema: LocalFlatSqlSchema = {
    standardId: vectors.epochStoreCorpus.standardId,
    tableName: vectors.table,
    fileId: vectors.fileId,
    schema: vectors.schema,
  };
  return FlatSQLEngineRecordStore.open({ schemas: [schema] });
}

/**
 * Produce the byte-exact server response body for
 * `omm/bulk?profile=nearest&source=celestrak-gp` by running the SAME engine
 * query the flow runs, over the shared corpus — sha256-pinned to the
 * expectations the Go host asserts (recorded known-good response body).
 */
async function recordedNearestCelestrakBody(): Promise<Uint8Array> {
  const corpusStore = await openEngineStore();
  try {
    for (const source of vectors.epochStoreCorpus.sources) {
      const stream = Uint8Array.from(Buffer.from(source.streamBase64, 'base64'));
      await corpusStore.ingestFlatBufferStream(vectors.epochStoreCorpus.standardId, stream, {
        ...(source.tagged ? { source: source.name } : {}),
        persist: false,
      });
    }
    const body = corpusStore.queryEpochRawStream('OMM', {
      profile: 'nearest',
      source: 'celestrak-gp',
      epoch: EPOCH,
      limit: -1,
    });
    // Recorded-fixture guard: identical to the shared Go⇄JS expectation.
    expect(sha256Hex(body)).toBe(vectors.expected.epochStoreStreams.nearest_celestrak.sha256);
    return body;
  } finally {
    await corpusStore.close();
  }
}

describe('RemoteEpochStreamClient (loop D.3 — conditional stream requests)', () => {
  it('replays exact bytes after mapped ingestion, local teardown and caller mutation', async () => {
    const body = await recordedNearestCelestrakBody();
    const store = await openEngineStore();
    const queryData = vi.fn(async (options) => ({
      format: 'flatbuffers' as const, status: options.ifNoneMatch ? 304 : 200,
      notModified: Boolean(options.ifNoneMatch), etag: '"snapshot"',
      stream: options.ifNoneMatch ? new Uint8Array() : body.slice(),
      recordCount: vectors.expected.epochStoreStreams.nearest_celestrak.frameCount,
      frames: function* () {},
    }));
    const client = new RemoteEpochStreamClient({ queryData }, store);
    const request = { schema: 'OMM', profile: 'nearest', source: 'celestrak-gp', ingestSource: 'local-mirror' };
    try {
      const first = await client.fetchEpochStream(request);
      expect(first.ingested).toBeGreaterThan(0);
      first.stream.fill(0);
      // The response is still valid even when the local query corpus is gone.
      await store.close();
      const second = await client.fetchEpochStream(request);
      expect(second.stream).toEqual(body);
      expect(second.fromCache).toBe(true);
      second.stream.fill(0);
      expect((await client.fetchEpochStream(request)).stream).toEqual(body);
    } finally { await store.close(); }
  });

  it('bounds cache entries and bytes, evicts validators, and isolates ingest partitions', async () => {
    const body = await recordedNearestCelestrakBody();
    const seen: Array<{ source?: string; ifNoneMatch?: string | null }> = [];
    const queryData = vi.fn(async (options) => {
      seen.push(options);
      return {
        format: 'flatbuffers' as const, status: options.ifNoneMatch ? 304 : 200,
        notModified: Boolean(options.ifNoneMatch), etag: '"snapshot"',
        stream: options.ifNoneMatch ? new Uint8Array() : body.slice(),
        recordCount: 999, frames: function* () {},
      };
    });
    const store = { ingestFlatBufferStream: vi.fn(async () => 1), queryEpochRawStream: () => new Uint8Array() };
    const client = new RemoteEpochStreamClient({ queryData }, store, { maxEntries: 2, maxBytes: body.length * 2 });
    const request = { schema: 'OMM', source: 'one' };
    for (const source of ['one', 'two', 'one', 'three', 'two']) await client.fetchEpochStream({ ...request, source });
    expect(seen.map((item) => item.ifNoneMatch ?? null)).toEqual([null, null, '"snapshot"', null, null]);
    await client.fetchEpochStream({ ...request, ingestSource: 'different' });
    expect(seen.at(-1)?.ifNoneMatch).toBeUndefined();
    client.clearCache();
    expect(client.cachedEtag(request)).toBeNull();
    const tiny = new RemoteEpochStreamClient({ queryData }, store, { maxBytes: body.length - 1 });
    await tiny.fetchEpochStream(request);
    await tiny.fetchEpochStream(request);
    expect(seen.at(-1)?.ifNoneMatch).toBeUndefined();
  });

  it('retries an unexpected 304 once and refuses a second bodyless response', async () => {
    const queryData = vi.fn(async () => ({
      format: 'flatbuffers' as const, status: 304, notModified: true,
      etag: '"missing"', stream: new Uint8Array(), recordCount: 0, frames: function* () {},
    }));
    const store = { ingestFlatBufferStream: vi.fn(async () => 0), queryEpochRawStream: () => new Uint8Array() };
    const client = new RemoteEpochStreamClient({ queryData }, store);
    await expect(client.fetchEpochStream({ schema: 'OMM' })).rejects.toThrow(/304.*retained/i);
    expect(queryData).toHaveBeenCalledTimes(2);
    expect(store.ingestFlatBufferStream).not.toHaveBeenCalled();
  });

  it('does not retain no-store responses or failed ingestions', async () => {
    const body = await recordedNearestCelestrakBody();
    const queryData = vi.fn(async () => ({
      format: 'flatbuffers' as const, status: 200, notModified: false,
      etag: '"private"', cacheControl: 'private, no-store', stream: body.slice(),
      recordCount: 0, frames: function* () {},
    }));
    const store = { ingestFlatBufferStream: vi.fn(async () => 1), queryEpochRawStream: () => new Uint8Array() };
    const client = new RemoteEpochStreamClient({ queryData }, store);
    const request = { schema: 'OMM' };
    expect((await client.fetchEpochStream(request)).recordCount).toBe(vectors.expected.epochStoreStreams.nearest_celestrak.frameCount);
    expect(client.cachedEtag(request)).toBeNull();
    store.ingestFlatBufferStream.mockRejectedValueOnce(new Error('disk unavailable'));
    await expect(client.fetchEpochStream(request)).rejects.toThrow('disk unavailable');
    expect(client.cachedEtag(request)).toBeNull();
  });

  it('rejects truncated framing before ingestion or caching', async () => {
    const queryData = vi.fn(async () => ({
      format: 'flatbuffers' as const, status: 200, notModified: false,
      etag: '"broken"', stream: new Uint8Array([20, 0, 0, 0, 1]),
      recordCount: 1, frames: function* () {},
    }));
    const store = { ingestFlatBufferStream: vi.fn(async () => 1), queryEpochRawStream: () => new Uint8Array() };
    const client = new RemoteEpochStreamClient({ queryData }, store);
    await expect(client.fetchEpochStream({ schema: 'OMM' })).rejects.toThrow(/truncated frame/);
    expect(store.ingestFlatBufferStream).not.toHaveBeenCalled();
    expect(client.cachedEtag({ schema: 'OMM' })).toBeNull();
  });

  it('applies no-store on a 304 and recovers from a bodyless response after eviction', async () => {
    const body = await recordedNearestCelestrakBody();
    const seen: Array<string | null> = [];
    let call = 0;
    const queryData = vi.fn(async (options) => {
      seen.push(options.ifNoneMatch ?? null);
      call++;
      const notModified = call === 2 || call === 3;
      return {
        format: 'flatbuffers' as const, status: notModified ? 304 : 200, notModified,
        etag: '"snapshot"', cacheControl: call === 2 ? 'no-store' : null,
        stream: notModified ? new Uint8Array() : body.slice(), recordCount: 0, frames: function* () {},
      };
    });
    const store = { ingestFlatBufferStream: vi.fn(async () => 1), queryEpochRawStream: () => new Uint8Array() };
    const client = new RemoteEpochStreamClient({ queryData }, store);
    const request = { schema: 'OMM' };
    await client.fetchEpochStream(request);
    expect((await client.fetchEpochStream(request)).stream).toEqual(body);
    expect(client.cachedEtag(request)).toBeNull();
    expect((await client.fetchEpochStream(request)).stream).toEqual(body);
    expect(seen).toEqual([null, '"snapshot"', null, null]);
    expect(store.ingestFlatBufferStream).toHaveBeenCalledTimes(2);
  });

  it('ingests the original 200 stream and replays the validated response on 304', async () => {
    const body = await recordedNearestCelestrakBody();
    const etag = `W/"fnv1a64-${sha256Hex(body).slice(0, 16)}"`;
    const expected = vectors.expected.epochStoreStreams.nearest_celestrak;

    const requests: Array<{ url: string; ifNoneMatch: string | null }> = [];
    const fetchMock = async (url: string | URL | Request, init?: RequestInit): Promise<Response> => {
      const headers = (init?.headers ?? {}) as Record<string, string>;
      const ifNoneMatch = headers['If-None-Match'] ?? null;
      requests.push({ url: String(url), ifNoneMatch });
      if (ifNoneMatch === etag) {
        return new Response(null, { status: 304, headers: { ETag: etag } });
      }
      return new Response(body.slice().buffer as ArrayBuffer, {
        status: 200,
        headers: {
          'Content-Type': FLATBUFFER_STREAM_CONTENT_TYPE,
          'X-SDN-Record-Count': String(expected.frameCount),
          ETag: etag,
        },
      });
    };

    const previousFetch = globalThis.fetch;
    globalThis.fetch = fetchMock as typeof globalThis.fetch;
    const localStore = await openEngineStore();
    try {
      const transport = new HttpTransport('https://sdn.example.test');
      const client = new RemoteEpochStreamClient(transport, localStore);
      const request = {
        schema: 'OMM.fbs',
        profile: 'nearest',
        epoch: EPOCH,
        source: 'celestrak-gp',
      };

      // First call: unconditional, 200, stream fed straight into the store.
      const first = await client.fetchEpochStream(request);
      expect(requests).toHaveLength(1);
      expect(requests[0].url).toBe(
        'https://sdn.example.test/api/v1/data/omm/bulk?source=celestrak-gp&profile=nearest&epoch=1778500800.5',
      );
      expect(requests[0].ifNoneMatch).toBeNull();
      expect(first.fromLocalStore).toBe(false);
      expect(first.etag).toBe(etag);
      expect(first.recordCount).toBe(expected.frameCount);
      expect(first.ingested).toBe(expected.frameCount);
      expect(sha256Hex(first.stream)).toBe(expected.sha256);
      expect(client.cachedEtag(request)).toBe(etag);

      // Second call: conditional, 304, byte-identical retained response.
      const second = await client.fetchEpochStream(request);
      expect(requests).toHaveLength(2);
      expect(requests[1].ifNoneMatch).toBe(etag);
      expect(second.fromLocalStore).toBe(true);
      expect(second.ingested).toBe(0);
      expect(second.etag).toBe(etag);
      expect(second.recordCount).toBe(expected.frameCount);
      expect(second.stream.byteLength).toBe(expected.byteLength);
      expect(sha256Hex(second.stream)).toBe(expected.sha256); // 304 replay ≡ server body
      expect([...second.frames()]).toHaveLength(expected.frameCount);
    } finally {
      globalThis.fetch = previousFetch;
      await localStore.close();
    }
  });

  it('revalidates to 200 when the server data changed (stale validator)', async () => {
    const body = await recordedNearestCelestrakBody();
    const expected = vectors.expected.epochStoreStreams.nearest_celestrak;
    let etag = 'W/"fnv1a64-generation-one0"';

    const requests: Array<string | null> = [];
    const fetchMock = async (_url: string | URL | Request, init?: RequestInit): Promise<Response> => {
      const headers = (init?.headers ?? {}) as Record<string, string>;
      const ifNoneMatch = headers['If-None-Match'] ?? null;
      requests.push(ifNoneMatch);
      if (ifNoneMatch === etag) {
        return new Response(null, { status: 304, headers: { ETag: etag } });
      }
      return new Response(body.slice().buffer as ArrayBuffer, {
        status: 200,
        headers: {
          'Content-Type': FLATBUFFER_STREAM_CONTENT_TYPE,
          'X-SDN-Record-Count': String(expected.frameCount),
          ETag: etag,
        },
      });
    };

    const previousFetch = globalThis.fetch;
    globalThis.fetch = fetchMock as typeof globalThis.fetch;
    const localStore = await openEngineStore();
    try {
      const transport = new HttpTransport('https://sdn.example.test');
      const client = new RemoteEpochStreamClient(transport, localStore);
      const request = { schema: 'OMM', profile: 'nearest', epoch: EPOCH, source: 'celestrak-gp' };

      const first = await client.fetchEpochStream(request);
      expect(first.fromLocalStore).toBe(false);
      expect(first.ingested).toBe(expected.frameCount);

      // Server-side data rotates: new etag → the cached validator is stale.
      etag = 'W/"fnv1a64-generation-two0"';
      const second = await client.fetchEpochStream(request);
      expect(requests).toEqual([null, 'W/"fnv1a64-generation-one0"']);
      expect(second.fromLocalStore).toBe(false); // 200, not 304
      expect(second.ingested).toBe(0); // same bytes → ingested-keys ledger dedupes
      expect(client.cachedEtag(request)).toBe(etag);
    } finally {
      globalThis.fetch = previousFetch;
      await localStore.close();
    }
  });
});
