// Isomorphism parity gate: the JS/V8 host (flatsql/standalone) must produce
// exactly the outputs recorded in shared-test-vectors/flatsql-parity.json —
// the same file the Go/WasmEdge host asserts against
// (sdn-server/internal/flatsqlrt/parity_test.go). Regenerate expectations
// with scripts/generate-flatsql-parity-vectors.mjs.
//
// Loop D.2 extends the gate to the STORE level: the sdn-js engine record
// store's PRIMARY public query API (queryEpochRawStream /
// queryRawFlatBufferStream) must reproduce the shared epoch-profile streams
// byte-for-byte over the same multi-provider corpus the Go host ingests
// through its real store (sdn-server/internal/storage/
// engine_records_parity_test.go), and the engine epoch SQL builder must
// emit the server's engine_records.go strings VERBATIM.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { describe, expect, test } from 'vitest';

// @ts-expect-error — plain .mjs helper shared with the generator script.
import { runParityCases } from '../scripts/generate-flatsql-parity-vectors.mjs';
import { FlatSQLEngineRecordStore } from './engine-record-store';
import {
  buildEngineEpochProfileSql,
  type EngineEpochProfileSpec,
} from './epoch-query-sql';
import type { LocalFlatSqlSchema } from './local-flatsql';

const vectorsPath = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../shared-test-vectors/flatsql-parity.json',
);

interface EpochStoreExpectation {
  sha256: string;
  byteLength: number;
  frameCount: number;
}

interface ParityVectors {
  schema: string;
  fileId: string;
  table: string;
  engineEpochSql: Record<string, string>;
  epochStoreCorpus: {
    schemaName: string;
    standardId: string;
    sources: Array<{ name: string; tagged: boolean; recordCount: number; streamBase64: string }>;
  };
  epochStoreCases: Array<{ name: string; profile: string; source: string; epoch: number; limit: number }>;
  epochStoreDirectCases: Array<{
    name: string;
    sql: string;
    params: Array<{ t: string; v: unknown }>;
    equalsCase: string;
  }>;
  expected: {
    epochStoreStreams: Record<string, EpochStoreExpectation>;
  };
}

const OMM_ENGINE_SPEC: EngineEpochProfileSpec = {
  tableName: 'OMM',
  partitionColumn: 'NORAD_CAT_ID',
  epochColumn: 'USER_DEFINED_EPOCH_TIMESTAMP',
};

function loadVectors(): ParityVectors {
  return JSON.parse(readFileSync(vectorsPath, 'utf8')) as ParityVectors;
}

function digestAlignedStream(stream: Uint8Array): EpochStoreExpectation {
  let frameCount = 0;
  const view = new DataView(stream.buffer, stream.byteOffset, stream.byteLength);
  let offset = 0;
  while (offset < stream.byteLength) {
    const length = view.getUint32(offset, true);
    offset += 4 + length;
    frameCount += 1;
  }
  expect(offset).toBe(stream.byteLength); // aligned framing intact
  return {
    sha256: createHash('sha256').update(stream).digest('hex'),
    byteLength: stream.byteLength,
    frameCount,
  };
}

function decodeDirectParams(params: Array<{ t: string; v: unknown }>): Array<null | boolean | number | string | Uint8Array> {
  return params.map((p) => {
    switch (p.t) {
      case 'null': return null;
      case 'bool': return p.v as boolean;
      case 'i64': return p.v as number;
      case 'f64': return p.v as number;
      case 'str': return p.v as string;
      case 'bytes': return Uint8Array.from(Buffer.from(p.v as string, 'base64'));
      default: throw new Error(`unknown param tag: ${p.t}`);
    }
  });
}

async function openCorpusStore(vectors: ParityVectors, options: {
  queryProfiles?: Record<string, { profile?: string; source?: string; epoch?: number; limit?: number }>;
  nowSeconds?: () => number;
} = {}): Promise<FlatSQLEngineRecordStore> {
  const schema: LocalFlatSqlSchema = {
    standardId: vectors.epochStoreCorpus.standardId,
    tableName: vectors.table,
    fileId: vectors.fileId,
    schema: vectors.schema,
  };
  const store = await FlatSQLEngineRecordStore.open({
    schemas: [schema],
    queryProfiles: options.queryProfiles,
    nowSeconds: options.nowSeconds,
  });
  const standards = store.standardsStore!;
  for (const source of vectors.epochStoreCorpus.sources) {
    const stream = Uint8Array.from(Buffer.from(source.streamBase64, 'base64'));
    const ingested = await standards.ingestFlatBufferStream(
      vectors.epochStoreCorpus.standardId,
      stream,
      {
        // Untagged records land in the server-default `local` partition.
        ...(source.tagged ? { source: source.name } : {}),
        persist: false,
      },
    );
    expect(ingested).toBe(source.recordCount);
  }
  return store;
}

describe('FlatSQL Go⇄JS isomorphism parity', () => {
  test('JS host reproduces the shared expected outputs byte-for-byte', async () => {
    const vectors = JSON.parse(readFileSync(vectorsPath, 'utf8'));
    expect(vectors.expected).toBeTruthy();

    const actual = await runParityCases(vectors);
    expect(actual).toEqual({ ...vectors.expected, epochStoreStreams: undefined });
  });

  test('engine epoch SQL builder emits the server engine_records.go strings VERBATIM', () => {
    const vectors = loadVectors();
    expect(buildEngineEpochProfileSql('nearest', OMM_ENGINE_SPEC)).toBe(vectors.engineEpochSql.nearest);
    expect(buildEngineEpochProfileSql('as_of', OMM_ENGINE_SPEC)).toBe(vectors.engineEpochSql.as_of);
    expect(buildEngineEpochProfileSql('forward', OMM_ENGINE_SPEC)).toBe(vectors.engineEpochSql.forward);
    // `epoch.`-prefixed profile ids resolve to the same SQL (server TrimPrefix mirror).
    expect(buildEngineEpochProfileSql('epoch.nearest', OMM_ENGINE_SPEC)).toBe(vectors.engineEpochSql.nearest);
  });

  test('sdn-js store public API reproduces the shared epoch-profile streams byte-for-byte', async () => {
    const vectors = loadVectors();
    expect(vectors.expected.epochStoreStreams).toBeTruthy();
    const store = await openCorpusStore(vectors);
    try {
      for (const c of vectors.epochStoreCases) {
        const stream = store.queryEpochRawStream(vectors.epochStoreCorpus.standardId, {
          profile: c.profile,
          source: c.source,
          epoch: c.epoch,
          limit: c.limit,
        });
        expect({ name: c.name, ...digestAlignedStream(stream) })
          .toEqual({ name: c.name, ...vectors.expected.epochStoreStreams[c.name] });
      }

      // D.1 re-verify: the server's OR-form `_source` predicate and the
      // direct-equality pushdown form return IDENTICAL bytes through the
      // generic raw-stream API — and match the profile-API result.
      for (const c of vectors.epochStoreDirectCases) {
        const stream = store.queryRawFlatBufferStream(
          vectors.epochStoreCorpus.standardId,
          c.sql,
          decodeDirectParams(c.params),
        );
        const digest = digestAlignedStream(stream);
        expect({ name: c.name, ...digest })
          .toEqual({ name: c.name, ...vectors.expected.epochStoreStreams[c.name] });
        expect(digest).toEqual(vectors.expected.epochStoreStreams[c.equalsCase]);
      }

      // Zero-copy decoded-iterator convenience: frames are subarray views
      // into the aligned stream (no copies).
      const nearestAll = vectors.epochStoreCases.find((c) => c.name === 'nearest_all')!;
      const raw = store.queryEpochRawStream('OMM', nearestAll);
      let frames = 0;
      for (const frame of store.queryEpochFrames('OMM', nearestAll)) {
        expect(frame.byteLength).toBeGreaterThan(0);
        frames += 1;
      }
      expect(frames).toBe(vectors.expected.epochStoreStreams.nearest_all.frameCount);
      expect(raw.byteLength).toBe(vectors.expected.epochStoreStreams.nearest_all.byteLength);
    } finally {
      await store.close();
    }
  });

  test('per-standard query profile config resolves request > config > fallback', async () => {
    const vectors = loadVectors();
    const asOfCase = vectors.epochStoreCases.find((c) => c.name === 'as_of_all')!;
    const nearestCase = vectors.epochStoreCases.find((c) => c.name === 'nearest_all')!;

    // Config-driven default (retrieval-module config shape, keyed by schema
    // name): a bare queryEpochRawStream('OMM') runs the configured profile.
    const configured = await openCorpusStore(vectors, {
      queryProfiles: {
        'OMM.fbs': { profile: asOfCase.profile, epoch: asOfCase.epoch, limit: asOfCase.limit },
      },
    });
    try {
      expect(digestAlignedStream(configured.queryEpochRawStream('OMM')))
        .toEqual(vectors.expected.epochStoreStreams.as_of_all);
      // Request fields override config; unset request fields keep config values.
      expect(digestAlignedStream(configured.queryEpochRawStream('OMM', { profile: 'nearest' })))
        .toEqual(vectors.expected.epochStoreStreams.nearest_all);
    } finally {
      await configured.close();
    }

    // Compiled fallback (no config): profile `nearest`, epoch = now (injected
    // clock), limit 50000 — same records as the unlimited nearest_all case.
    const fallback = await openCorpusStore(vectors, {
      nowSeconds: () => nearestCase.epoch,
    });
    try {
      expect(digestAlignedStream(fallback.queryEpochRawStream('OMM')))
        .toEqual(vectors.expected.epochStoreStreams.nearest_all);
    } finally {
      await fallback.close();
    }
  });
});
