/**
 * Loop D.3 — ONE conditional stream request into the REAL engine store.
 *
 * A mocked flow endpoint (exact server shapes from
 * sdn-server/internal/flowrt/http_mount_integration_test.go: stream
 * content-type, u32 LE aligned framing, `ETag: W/"fnv1a64-…"`,
 * `X-SDN-Record-Count`, 304 on matching `If-None-Match`) serves engine
 * streams recorded in shared-test-vectors/flatsql-parity.json — the same
 * byte-exact corpus the Go host's store produces. The client feeds the HTTP
 * body STRAIGHT into a real FlatSQL-WASM engine store
 * (ingestFlatBufferStream, zero re-encode) and serves 304s from the local
 * store via queryEpochRawStream.
 */
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

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
  it('ingests the 200 stream zero-copy and serves the 304 from the local engine store', async () => {
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

      // Second call: conditional, 304, byte-identical stream from the LOCAL store.
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
