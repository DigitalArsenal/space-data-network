import { publishedSchema } from './test-schema-helper';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
/**
 * THE STREAM SURFACE (2.0.18) — a size-prefixed SDS shard is not a record.
 *
 * Filed as `sdn-js-flatsql-storage-stream-surface` out of measurements taken
 * in a real browser, over real IndexedDB, on the deployed 2.0.17 bytes: a
 * 32,141-frame $OMM shard (10,544,168 B) handed to `store()` produced ZERO
 * queryable rows, cost 342.8 ms, and left 21,104,976 B in IndexedDB — one
 * copy in the envelope journal and a second in the standards lane.
 *
 * Root cause, and the first thing proved below: `flatBufferMatchesFileId`
 * accepted the SHARD as a single size-prefixed RECORD, because the first
 * frame's u32 length prefix puts `$OMM` at bytes 8..11 and the helper checked
 * offsets 4 AND 8 with no length constraint. The Go host it mirrors never had
 * that hole — `engineRecordPayload` (sdn-server/internal/storage/
 * engine_records.go:137) requires `uint32(data[:4])+4 == len(data)`. sdn-js
 * was the permissive one; this closes the divergence.
 *
 * The surface that replaces the broken path is `storeStream` / `readStream`,
 * and the property that makes it safe for a content-addressed consumer is the
 * BYTE-IDENTICAL round trip at catalog scale, asserted here across teardown
 * and reopen.
 */

import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { readFileSync, mkdtempSync, readdirSync, statSync, rmSync } from 'node:fs';
import { afterEach, describe, expect, it } from 'vitest';

import {
  FlatSQLEngineRecordStore,
  countFlatBufferStreamFramesWithFileId,
  flatBufferMatchesFileId,
  flatBufferStreamMatchesFileId,
} from './engine-record-store';
import {
  MemoryFlatSqlPersistenceStore,
  stripSdnFlatBufferSizePrefix,
} from './local-flatsql';
// @ts-expect-error — plain .mjs helper shared with the D.6 benchmark scripts.
import { buildBenchCorpus } from '../scripts/lib/d6-bench-common.mjs';

const require = createRequire(import.meta.url);
const OMM_SCHEMA = readFileSync(require.resolve('spacedatastandards.org/schema/OMM/main.fbs'), 'utf8');

const OMM_STANDARD = {
  standardId: 'OMM',
  tableName: 'OMM',
  fileId: '$OMM',
  ...publishedSchema('OMM'),
};

/** One real $OMM record, size-prefixed: prefix 284, buffer 288 → ONE frame. */
const STARLINK_6292_OMM_BYTES = new Uint8Array(Buffer.from('HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA', 'base64'));

const digest = (bytes: Uint8Array) => createHash('sha256').update(Buffer.from(bytes)).digest('hex');
const enc = (text: string) => new TextEncoder().encode(text);
const roots = new Map<string, string>();
function engineFor(key: string) {
  if (!roots.has(key)) roots.set(key, mkdtempSync(join(tmpdir(), 'stream-records-')));
  return { root: roots.get(key)! };
}
afterEach(() => {
  for (const root of roots.values()) rmSync(root, { recursive: true, force: true });
  roots.clear();
});

/**
 * The live catalog's shape: 32,141 aligned size-prefixed $OMM frames in ONE
 * buffer. Built once — every scale assertion below shares it.
 */
const CATALOG_FRAME_COUNT = 32_141;
let catalogPromise: { streamBytes: Uint8Array; recordCount: number } | null = null;
function catalogShard(): { streamBytes: Uint8Array; recordCount: number } {
  if (!catalogPromise) catalogPromise = buildBenchCorpus(CATALOG_FRAME_COUNT);
  return catalogPromise!;
}

/** Total bytes a persistence store is holding, and per key — the IndexedDB total. */
function persistedTotals(store: MemoryFlatSqlPersistenceStore): { total: number; keys: string[] } {
  const entries = (store as unknown as { entries: Map<string, unknown> }).entries;
  let total = 0;
  const keys: string[] = [];
  for (const [key, value] of entries) {
    keys.push(key);
    if (value instanceof Uint8Array) total += value.byteLength;
    else total += new TextEncoder().encode(JSON.stringify(value)).byteLength;
  }
  return { total, keys };
}

describe('a size-prefixed stream is not a record (the 2.0.17 false positive)', () => {
  it('refuses the shard as a single record, exactly where the Go host refuses it', () => {
    const { streamBytes } = catalogShard();
    // Catalog scale, so the classifier is exercised on the real shape.
    expect(countFlatBufferStreamFramesWithFileId(streamBytes, '$OMM')).toBe(CATALOG_FRAME_COUNT);
    expect(streamBytes.byteLength).toBeGreaterThan(10_000_000);

    // THE REGRESSION. In 2.0.17 this was `true` — the whole 10.5 MB shard
    // looked like one $OMM record because bytes 8..11 are `$OMM`.
    expect(flatBufferMatchesFileId(streamBytes, '$OMM')).toBe(false);
    // The identifier IS there at byte 8; it is the LENGTH rule that rejects it.
    expect(String.fromCharCode(...streamBytes.subarray(8, 12))).toBe('$OMM');

    // The Go host's rule verbatim: prefix + 4 must account for the whole buffer.
    const view = new DataView(streamBytes.buffer, streamBytes.byteOffset, streamBytes.byteLength);
    expect(view.getUint32(0, true) + 4).not.toBe(streamBytes.byteLength);
  });

  it('still accepts every genuine single record, bare or size-prefixed', () => {
    const bare = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    expect(flatBufferMatchesFileId(STARLINK_6292_OMM_BYTES, '$OMM')).toBe(true);
    expect(flatBufferMatchesFileId(bare, '$OMM')).toBe(true);
    expect(flatBufferMatchesFileId(STARLINK_6292_OMM_BYTES, '$CAT')).toBe(false);
    expect(flatBufferMatchesFileId(enc('not a flatbuffer'), '$OMM')).toBe(false);
    // A one-frame stream is BOTH a record and a stream — deliberate overlap.
    expect(flatBufferStreamMatchesFileId(STARLINK_6292_OMM_BYTES, '$OMM')).toBe(true);
  });

  it('classifies the shard positively, and never mistakes opaque bytes for one', () => {
    const { streamBytes } = catalogShard();
    expect(countFlatBufferStreamFramesWithFileId(streamBytes, '$OMM')).toBe(CATALOG_FRAME_COUNT);
    expect(countFlatBufferStreamFramesWithFileId(streamBytes, '$CAT')).toBe(0);

    // Opaque payloads, including ones whose leading u32 is enormous or whose
    // frame lengths do not tile the buffer, are not streams.
    expect(countFlatBufferStreamFramesWithFileId(enc('beta'), '$OMM')).toBe(0);
    expect(countFlatBufferStreamFramesWithFileId(enc('a much longer opaque payload'), '$OMM')).toBe(0);
    const truncated = streamBytes.subarray(0, streamBytes.byteLength - 3);
    expect(countFlatBufferStreamFramesWithFileId(truncated, '$OMM')).toBe(0);
    // A stream with ONE foreign frame in the middle is not a $OMM stream.
    const poisoned = new Uint8Array(streamBytes.subarray(0, 4096));
    poisoned[8] = 'X'.charCodeAt(0);
    expect(countFlatBufferStreamFramesWithFileId(poisoned, '$OMM')).toBe(0);
  });
});

describe('registered binary persistence', () => {




  it('opens the disk-backed lane when schemas are given — the gate that failed', async () => {
    const store = await FlatSQLEngineRecordStore.open({
      persistenceKey: 'lane-gate',
      engine: engineFor('lane-gate'),
      persistenceStore: new MemoryFlatSqlPersistenceStore(),
      schemas: [OMM_STANDARD],
    });
    expect(store.standardsStore).not.toBeNull();
    expect(store.registeredStandards()).toEqual([{ standardId: 'OMM', fileId: '$OMM', tableName: 'OMM' }]);
    await store.close();
  });
});

describe('storeStream — the shard-shaped admit point', () => {
  it('routes by FILE IDENTIFIER, not by a caller-supplied name', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      const { streamBytes } = buildBenchCorpus(8);
      // No standardId passed at all: the frames say what they are.
      const result = await store.storeStream(streamBytes, { source: 'celestrak-gp' });
      expect(result.standardId).toBe('OMM');
      expect(result.frames).toBe(8);
      expect(result.ingested).toBe(8);

      // A caller ASSERTION that disagrees with the header is refused, never
      // overridden — the routing law: header decides, strings do not.
      await expect(store.storeStream(buildBenchCorpus(2).streamBytes, { standardId: 'CDM' }))
        .rejects.toThrow(/routed to|asserted|not registered|file identifier/);

      // Bytes that are not a registered standard's stream are refused.
      await expect(store.storeStream(enc('definitely not a shard')))
        .rejects.toThrow(/not an aligned size-prefixed stream/);
    } finally {
      await store.close();
    }
  });

  it('verifies a declared sha256 before ingesting anything', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      const { streamBytes } = buildBenchCorpus(16);
      await expect(store.storeStream(streamBytes, { sha256: '0'.repeat(64) }))
        .rejects.toThrow(/sha256 mismatch/);
      expect((await store.readStream('OMM')).byteLength).toBe(0);

      const result = await store.storeStream(streamBytes, { sha256: digest(streamBytes) });
      expect(result.ingested).toBe(16);
    } finally {
      await store.close();
    }
  });

  it('is idempotent under shard replay (the shardCid keys the frames)', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      const { streamBytes } = buildBenchCorpus(32);
      const cid = digest(streamBytes);
      const first = await store.storeStream(streamBytes, { source: 'celestrak-gp', shardCid: cid });
      expect(first.ingested).toBe(32);
      expect(first.replayed).toBe(false);

      const replay = await store.storeStream(streamBytes, { source: 'celestrak-gp', shardCid: cid });
      expect(replay.ingested).toBe(0);
      expect(replay.replayed).toBe(true);
      const rows = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
      expect(Number((rows.records[0] as Record<string, unknown>).n)).toBe(32);
    } finally {
      await store.close();
    }
  });
});

describe('stream records and optional envelope metadata', () => {
  it('indexes stream records without optional envelope metadata', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      const { streamBytes } = buildBenchCorpus(64);
      const result = await store.storeStream(streamBytes, { source: 'celestrak-gp' });
      expect(result.indexedEnvelopes).toBe(0);
      // Binary records are queryable without an additional envelope journal.
      expect(await store.count()).toBe(64);
      expect((await store.sql('SELECT _cid FROM OMM')).rowCount).toBe(64);
      // The lane holds every frame.
      const rows = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
      expect(Number((rows.records[0] as Record<string, unknown>).n)).toBe(64);
    } finally {
      await store.close();
    }
  });

  it('still indexes envelopes when a caller explicitly opts in', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      const { streamBytes } = buildBenchCorpus(8);
      const result = await store.storeStream(streamBytes, { indexEnvelopes: true });
      expect(result.indexedEnvelopes).toBe(8);
    } finally {
      await store.close();
    }
  });

  it('leaves store()\'s record contract byte-for-byte unchanged', async () => {
    // The envelope is the durable substrate for a RECORD and its provenance;
    // that is the design (`engine vtab is a query cache over the durable
    // envelope`), not a legacy fallback, and it is not being retired.
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    const first = await FlatSQLEngineRecordStore.open({
      persistenceKey: 'record-contract',
      engine: engineFor('record-contract'),
      persistenceStore,
      schemas: [OMM_STANDARD],
    });
    const cid = await first.store('OMM.fbs', new Uint8Array(STARLINK_6292_OMM_BYTES), 'peer-a', enc('sig-a'));
    await first.close();

    const second = await FlatSQLEngineRecordStore.open({
      persistenceKey: 'record-contract',
      engine: engineFor('record-contract'),
      persistenceStore,
      schemas: [OMM_STANDARD],
    });
    try {
      expect(await second.count()).toBe(1);
      const restored = await second.get('OMM.fbs', cid);
      expect(restored?.data).toEqual(stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES));
      expect(restored?.peerId).toBe('peer-a');
      // ...and it still mirrors into the lane as a query cache.
      const rows = await second.standardsStore!.query('SELECT NORAD_CAT_ID, _source FROM OMM', 'OMM');
      expect(rows.records).toEqual([{ NORAD_CAT_ID: 56775, _source: 'OMM@local' }]);
    } finally {
      await second.close();
    }
  });

  it('refuses a shard through store() and names the surface that takes it', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      const { streamBytes } = buildBenchCorpus(4);
      await expect(store.store('OMM.fbs', streamBytes, 'peer-a', enc('sig')))
        .rejects.toThrow(/store\(\) admits ONE record/);
      await expect(store.store('OMM.fbs', streamBytes, 'peer-a', enc('sig')))
        .rejects.toThrow(/storeStream/);
      // Nothing was admitted anywhere.
      expect(await store.count()).toBe(0);
      expect((await store.readStream('OMM')).byteLength).toBe(0);
    } finally {
      await store.close();
    }
  });
});

describe('catalog scale: the gate from the task, and byte identity', () => {
  it('opens the lane, materializes every frame, and costs ONE shard on disk', async () => {
    const { streamBytes } = catalogShard();
    const persistenceStore = new MemoryFlatSqlPersistenceStore();

    // Opened the way the Orbital Console opens it — plus the schemas that
    // 2.0.18 makes both declared and mandatory.
    const store = await FlatSQLEngineRecordStore.open({
      persistenceKey: 'spaceaware-catalog',
      engine: engineFor('spaceaware-catalog'),
      persistenceStore,
      schemas: [OMM_STANDARD],
    });
    try {
      // GATE 1 — failed on 2.0.17 for the console's exact call.
      expect(store.standardsStore).not.toBeNull();

      const result = await store.storeStream(streamBytes, {
        source: 'celestrak-gp',
        shardCid: digest(streamBytes),
      });
      expect(result.standardId).toBe('OMM');
      expect(result.frames).toBe(CATALOG_FRAME_COUNT);

      // GATE 2 — rows == frame count. On 2.0.17 with schemas passed: 0.
      const rows = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
      expect(Number((rows.records[0] as Record<string, unknown>).n)).toBe(CATALOG_FRAME_COUNT);

      await store.flush();

      // Keep the original storage-amplification bound on actual filesystem bytes.
      await store.close();
      const files = (directory: string): string[] => readdirSync(directory).flatMap(name => {
        const entry = join(directory, name);
        return statSync(entry).isDirectory() ? files(entry) : [entry];
      });
      const paths = files(engineFor('spaceaware-catalog').root);
      const total = paths.reduce((sum, file) => sum + statSync(file).size, 0);
      expect(paths.length).toBeGreaterThan(0);
      const { keys } = persistedTotals(persistenceStore);
      expect(total).toBeGreaterThan(streamBytes.byteLength);
      expect(total / streamBytes.byteLength).toBeLessThan(1.35);
      // And no whole-blob envelope snapshot was written for the shard.
      const envelopeKey = keys.find((key) => key.endsWith(':record-envelopes'));
      if (envelopeKey) {
        const blob = await persistenceStore.readBytes(envelopeKey);
        expect(blob === null || blob.byteLength < 1024).toBe(true);
      }
    } finally {
      await store.close();
    }
  }, 240_000);

  it('round-trips 32,141 frames BYTE-IDENTICALLY, across teardown and reopen', async () => {
    const { streamBytes } = catalogShard();
    const shardDigest = digest(streamBytes);
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    const open = () => FlatSQLEngineRecordStore.open({
      persistenceKey: 'catalog-round-trip',
      engine: engineFor('catalog-round-trip'),
      persistenceStore,
      schemas: [OMM_STANDARD],
    });

    const first = await open();
    await first.storeStream(streamBytes, { source: 'celestrak-gp', shardCid: shardDigest });

    // Read back from the LIVE store: same bytes the caller delivered. This is
    // what makes the surface safe for a content-addressed consumer — the
    // console labels these bytes with the publication's shardCid, so
    // reassembled-but-different bytes would break content addressing.
    const live = await first.readStream('OMM', { source: 'celestrak-gp' });
    expect(live.byteLength).toBe(streamBytes.byteLength);
    expect(digest(live)).toBe(shardDigest);

    await first.close();

    // ...and from DISK ALONE, after the engine was torn down.
    const second = await open();
    try {
      const restored = await second.readStream('OMM', { source: 'celestrak-gp' });
      expect(restored.byteLength).toBe(streamBytes.byteLength);
      expect(digest(restored)).toBe(shardDigest);
    } finally {
      await second.close();
    }
  }, 240_000);



  it('reads an unknown source as an empty stream, and an unknown standard loudly', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      await store.storeStream(buildBenchCorpus(4).streamBytes, { source: 'celestrak-gp' });
      expect((await store.readStream('OMM', { source: 'no-such-provider' })).byteLength).toBe(0);
      expect((await store.readStream('OMM.fbs', { source: 'celestrak-gp' })).byteLength).toBeGreaterThan(0);
      await expect(store.readStream('CDM')).rejects.toThrow(/not registered/);
    } finally {
      await store.close();
    }
  });
});

it('reports received frames separately from newly materialized records', async () => {
  const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
  try {
    const { streamBytes: frame } = buildBenchCorpus(1);
    const stream = new Uint8Array(frame.length * 2);
    stream.set(frame);
    stream.set(frame, frame.length);
    const first = await store.storeStream(stream);
    expect(first.frames).toBe(2);
    expect(first.ingested).toBe(1);
    expect(first.bytes).toBe(stream.length);
    expect(await store.count()).toBe(1);
    const replay = await store.storeStream(stream);
    expect(replay.frames).toBe(2);
    expect(replay.ingested).toBe(0);
    expect(replay.replayed).toBe(true);
    expect(await store.count()).toBe(1);
  } finally { await store.close(); }
});
