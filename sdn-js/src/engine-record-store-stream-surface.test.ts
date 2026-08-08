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
 * BYTE-IDENTICAL round trip at catalogue scale, asserted here across teardown
 * and reopen.
 */

import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import {
  FlatSQLEngineRecordStore,
  countFlatBufferStreamFramesWithFileId,
  flatBufferMatchesFileId,
  flatBufferStreamMatchesFileId,
} from './engine-record-store';
import { FlatSQLStorage } from './flatsql-storage';
import {
  MemoryFlatSqlPersistenceStore,
  getSharedFlatSqlIoRouter,
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
  schema: OMM_SCHEMA,
};

/** One real $OMM record, size-prefixed: prefix 284, buffer 288 → ONE frame. */
const STARLINK_6292_OMM_BYTES = new Uint8Array(Buffer.from('HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA', 'base64'));

const digest = (bytes: Uint8Array) => createHash('sha256').update(Buffer.from(bytes)).digest('hex');
const enc = (text: string) => new TextEncoder().encode(text);

/**
 * The live catalogue's shape: 32,141 aligned size-prefixed $OMM frames in ONE
 * buffer. Built once — every scale assertion below shares it.
 */
const CATALOGUE_FRAME_COUNT = 32_141;
let cataloguePromise: { streamBytes: Uint8Array; recordCount: number } | null = null;
function catalogueShard(): { streamBytes: Uint8Array; recordCount: number } {
  if (!cataloguePromise) cataloguePromise = buildBenchCorpus(CATALOGUE_FRAME_COUNT);
  return cataloguePromise!;
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
    const { streamBytes } = catalogueShard();
    // Catalogue scale, so the classifier is exercised on the real shape.
    expect(countFlatBufferStreamFramesWithFileId(streamBytes, '$OMM')).toBe(CATALOGUE_FRAME_COUNT);
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
    const { streamBytes } = catalogueShard();
    expect(countFlatBufferStreamFramesWithFileId(streamBytes, '$OMM')).toBe(CATALOGUE_FRAME_COUNT);
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

describe('the option that decides durability is no longer silent', () => {
  it('refuses a persistenceKey with no schemas instead of downgrading to whole-blob', async () => {
    // The Orbital Console's exact call on 2.0.17: no error, no lane, one
    // 10,544,950 B `<key>:record-envelopes` blob for 32,139 live browsers.
    await expect(FlatSQLStorage.open({
      persistenceKey: 'spaceaware-catalog',
      persistenceStore: new MemoryFlatSqlPersistenceStore(),
    })).rejects.toThrow(/without `schemas`/);
    await expect(FlatSQLStorage.open({
      persistenceKey: 'spaceaware-catalog',
      persistenceStore: new MemoryFlatSqlPersistenceStore(),
    })).rejects.toThrow(/storeStream/);
  });

  it('opens journal-only when the caller DECLARES that is what they meant', async () => {
    const store = await FlatSQLStorage.open({
      persistenceKey: 'declared-journal',
      persistenceStore: new MemoryFlatSqlPersistenceStore(),
      envelopeJournalOnly: true,
    });
    expect(store.standardsStore).toBeNull();
    await store.close();
  });

  it('opens the disk-backed lane when schemas are given — the gate that failed', async () => {
    const store = await FlatSQLStorage.open({
      persistenceKey: 'lane-gate',
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
      expect(store.readStream('OMM').byteLength).toBe(0);

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

describe('THE ENVELOPE RULING — a shard lives in the lane, not in both', () => {
  it('writes no envelope row for a shard, so one shard costs ONE copy', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      const { streamBytes } = buildBenchCorpus(64);
      const result = await store.storeStream(streamBytes, { source: 'celestrak-gp' });
      expect(result.indexedEnvelopes).toBe(0);
      // The record-envelope surface is untouched: no rows, nothing to journal.
      expect(await store.count()).toBe(0);
      expect(store.sql('SELECT cid FROM sdn_record_index').rowCount).toBe(0);
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
      persistenceStore,
      schemas: [OMM_STANDARD],
    });
    const cid = await first.store('OMM.fbs', new Uint8Array(STARLINK_6292_OMM_BYTES), 'peer-a', enc('sig-a'));
    await first.close();

    const second = await FlatSQLEngineRecordStore.open({
      persistenceKey: 'record-contract',
      persistenceStore,
      schemas: [OMM_STANDARD],
    });
    try {
      expect(await second.count()).toBe(1);
      const restored = await second.get('OMM.fbs', cid);
      expect([...(restored?.data ?? [])]).toEqual([...STARLINK_6292_OMM_BYTES]);
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
      expect(store.readStream('OMM').byteLength).toBe(0);
    } finally {
      await store.close();
    }
  });
});

describe('catalogue scale: the gate from the task, and byte identity', () => {
  it('opens the lane, materializes every frame, and costs ONE shard on disk', async () => {
    const { streamBytes } = catalogueShard();
    const persistenceStore = new MemoryFlatSqlPersistenceStore();

    // Opened the way the Orbital Console opens it — plus the schemas that
    // 2.0.18 makes both declared and mandatory.
    const store = await FlatSQLStorage.open({
      persistenceKey: 'spaceaware-catalog',
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
      expect(result.frames).toBe(CATALOGUE_FRAME_COUNT);

      // GATE 2 — rows == frame count. On 2.0.17 with schemas passed: 0.
      const rows = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
      expect(Number((rows.records[0] as Record<string, unknown>).n)).toBe(CATALOGUE_FRAME_COUNT);

      await getSharedFlatSqlIoRouter().flush();

      // GATE 3 — the persisted total is about ONE shard, not two. On 2.0.17
      // the same work left 21,104,976 B for a 10,544,168 B shard (2.00x).
      //
      // Measured here: 12,698,914 B for a 10,025,832 B shard = 1.267x. The
      // arena holds the shard verbatim ONCE (10,025,832 B); the remainder is
      // the lane's per-frame ingested-keys ledger
      // (`<key>:OMM:record-keys` = 2,656,594 B — `OMM|shard:<cid>|<index>`
      // per frame), which is what makes shard replay a no-op. That ledger is
      // a pre-existing lane cost shared with the published-shard path, not
      // something this surface introduces; `sdn-js-flatsql-shard-level-ledger`
      // tracks collapsing it to one entry per shard.
      const { total, keys } = persistedTotals(persistenceStore);
      expect(total).toBeGreaterThan(streamBytes.byteLength);
      expect(total / streamBytes.byteLength).toBeLessThan(1.35);
      // The arena is the shard, ONCE — no second durable copy anywhere.
      const arenaKeys = keys.filter((key) => key.includes('.fsdata#') && !key.endsWith('#meta'));
      expect(arenaKeys.length).toBeGreaterThan(0);
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
    const { streamBytes } = catalogueShard();
    const shardDigest = digest(streamBytes);
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    const open = () => FlatSQLStorage.open({
      persistenceKey: 'catalog-round-trip',
      persistenceStore,
      schemas: [OMM_STANDARD],
    });

    const first = await open();
    await first.storeStream(streamBytes, { source: 'celestrak-gp', shardCid: shardDigest });

    // Read back from the LIVE store: same bytes the caller delivered. This is
    // what makes the surface safe for a content-addressed consumer — the
    // console labels these bytes with the publication's shardCid, so
    // reassembled-but-different bytes would break content addressing.
    const live = first.readStream('OMM', { source: 'celestrak-gp' });
    expect(live.byteLength).toBe(streamBytes.byteLength);
    expect(digest(live)).toBe(shardDigest);

    await first.close();
    await getSharedFlatSqlIoRouter().flush();

    // ...and from DISK ALONE, after the engine was torn down.
    const second = await open();
    try {
      const restored = second.readStream('OMM', { source: 'celestrak-gp' });
      expect(restored.byteLength).toBe(streamBytes.byteLength);
      expect(digest(restored)).toBe(shardDigest);
    } finally {
      await second.close();
    }
  }, 240_000);

  it('opens a live 2.0.17 whole-blob journal without crashing — the change is ADDITIVE', async () => {
    // 32,139 browsers hold `<key>:record-envelopes` containing a shard that
    // 2.0.17 accepted through `store()`. 2.0.18 must open that cache, not
    // throw on it: the refusal is on the WRITE path (`store()`), while
    // journal replay goes straight to `insertEnvelope` + `mirrorIntoStandard`,
    // and the mirror now correctly declines a stream instead of feeding it to
    // a one-record ingest. No layout changed, so there is nothing to migrate.
    const { streamBytes } = catalogueShard();
    const persistenceStore = new MemoryFlatSqlPersistenceStore();

    // Build the legacy state the way 2.0.17 did: shard through `store()`.
    const legacy = await FlatSQLStorage.open({
      persistenceKey: 'legacy-2017-cache',
      persistenceStore,
      envelopeJournalOnly: true, // 2.0.17's console passed no schemas at all
    });
    // Reach past the 2.0.18 write-path refusal to reproduce the stored state.
    await (legacy as unknown as {
      insertEnvelope(record: {
        cid: string; schema: string; peerId: string; timestamp: number;
        data: Uint8Array; signature: Uint8Array;
      }): void;
    }).insertEnvelope({
      cid: digest(streamBytes),
      schema: 'OMM.fbs',
      peerId: 'legacy',
      timestamp: Date.now(),
      data: streamBytes,
      signature: new Uint8Array(0),
    });
    await legacy.flush();
    await legacy.close();

    const legacyBlob = await persistenceStore.readBytes('legacy-2017-cache:record-envelopes');
    expect(legacyBlob).not.toBeNull();
    expect(legacyBlob!.byteLength).toBeGreaterThan(10_000_000);

    // 2.0.18 opens it — WITH schemas this time — and survives.
    const upgraded = await FlatSQLStorage.open({
      persistenceKey: 'legacy-2017-cache',
      persistenceStore,
      schemas: [OMM_STANDARD],
    });
    try {
      expect(upgraded.standardsStore).not.toBeNull();
      // The legacy envelope still replays (nothing is lost)...
      expect(await upgraded.count()).toBe(1);
      // ...and is NOT fed to the one-record mirror, which is what produced
      // the empty table and the doubled storage on 2.0.17.
      const rows = await upgraded.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
      expect(Number((rows.records[0] as Record<string, unknown>).n)).toBe(0);

      // The caller re-caches through the new surface and gets real rows.
      const result = await upgraded.storeStream(streamBytes, { source: 'celestrak-gp' });
      expect(result.ingested).toBe(CATALOGUE_FRAME_COUNT);
      const after = await upgraded.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
      expect(Number((after.records[0] as Record<string, unknown>).n)).toBe(CATALOGUE_FRAME_COUNT);
    } finally {
      await upgraded.close();
    }
  }, 240_000);

  it('reads an unknown source as an empty stream, and an unknown standard loudly', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      await store.storeStream(buildBenchCorpus(4).streamBytes, { source: 'celestrak-gp' });
      expect(store.readStream('OMM', { source: 'no-such-provider' }).byteLength).toBe(0);
      expect(store.readStream('OMM.fbs', { source: 'celestrak-gp' }).byteLength).toBeGreaterThan(0);
      expect(() => store.readStream('CDM')).toThrow(/not registered/);
    } finally {
      await store.close();
    }
  });
});
