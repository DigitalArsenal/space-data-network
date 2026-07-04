import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';

import { CAT } from 'spacedatastandards.org/lib/js/CAT/CAT.js';
import { operationalState } from 'spacedatastandards.org/lib/js/CAT/operationalState.js';
import { spaceObjectClass } from 'spacedatastandards.org/lib/js/CAT/spaceObjectClass.js';

import { buildEpochProfileSql } from './epoch-query-sql';
import { clearLocalFlatSqlStore, createLocalFlatSqlStore, decodeFlatSqlSizePrefixedStream, flatSqlSizePrefixedStreamInfo, isReadOnlyFlatSqlQuery, stripSdnFlatBufferSizePrefix } from './local-flatsql';

const require = createRequire(import.meta.url);

function readSdsSchema(specifier: string): string {
  return readFileSync(require.resolve(specifier), 'utf8');
}

const CAT_SCHEMA = readSdsSchema('spacedatastandards.org/schema/CAT/main.fbs');
const OMM_SCHEMA = readSdsSchema('spacedatastandards.org/schema/OMM/main.fbs');
const PNM_SCHEMA = readSdsSchema('spacedatastandards.org/schema/PNM/main.fbs');
const STARLINK_6292_OMM_BYTES = Buffer.from('HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA', 'base64');

describe('local FlatSQL datastore', () => {
  it('strips the SDN size prefix before ingesting standard FlatBuffers', () => {
    const raw = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);

    expect(String.fromCharCode(...raw.slice(4, 8))).toBe('$OMM');
    expect(raw.byteLength).toBe(STARLINK_6292_OMM_BYTES.byteLength - 4);
  });

  it('ingests downloaded OMM FlatBuffers and answers SQL over the local store', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-1',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], 'space-data-network-02');
    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-1',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], 'space-data-network-02');

    const result = await store.query('SELECT OBJECT_NAME, NORAD_CAT_ID FROM OMM WHERE NORAD_CAT_ID = 56775 LIMIT 10');

    expect(result.columns).toEqual(['OBJECT_NAME', 'NORAD_CAT_ID']);
    expect(result.records).toEqual([{ OBJECT_NAME: 'STARLINK-6292', NORAD_CAT_ID: 56775 }]);
    expect(store.getStats()).toEqual([expect.objectContaining({
      standardId: 'OMM',
      tableName: 'OMM',
      recordCount: 1,
      ingestedRecordCount: 1,
    })]);
    expect(store.getStats()[0]?.cachedBytes).toBeGreaterThan(0);
  });

  it('runs generated OMM epoch profile SQL against the local FlatSQL store', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-epoch-sql',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], { persist: false });

    const daySql = buildEpochProfileSql({
      standardId: 'OMM',
      profile: 'epoch.day',
      day: '2026-05-10',
      limit: 10,
    });
    const nearestSql = buildEpochProfileSql({
      standardId: 'OMM',
      profile: 'epoch.nearest',
      at: '2026-05-11T00:00',
      maxDeltaSeconds: 86400,
      limit: 10,
    });

    expect(store.query(daySql, 'OMM').records).toEqual([expect.objectContaining({ NORAD_CAT_ID: 56775 })]);
    expect(store.query(nearestSql, 'OMM').records).toEqual([expect.objectContaining({ OBJECT_NAME: 'STARLINK-6292' })]);
  });

  it('supports deferred persistence for bulk ingest batches', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-deferred',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], { source: 'space-data-network-02', persist: false });

    expect(store.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 1', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
    expect(store.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
      recordCount: 1,
      cachedBytes: 0,
    }));

    await store.flush('OMM');

    expect(store.getStats({ includeCachedBytes: false })[0]?.cachedBytes).toBeGreaterThan(0);
  });

  it('ingests native FlatSQL size-prefixed streams without using JSON row objects as the sync boundary', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    const stream = flatSqlSizePrefixedStream([stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES)]);
    const ingested = await store.ingestFlatBufferStream('OMM', stream, {
      recordKeys: ['celestrak-omm-stream'],
      persist: false,
    });

    expect(ingested).toBe(1);
    expect(store.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 1', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
    expect(store.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
      recordCount: 1,
      ingestedRecordCount: 1,
    }));
  });

  it('ingests content-keyed streams in one engine call when nothing dedupes, and still filters overlaps (loop D.6)', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });
    const frameA = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    const frameB = withReplacedAscii(frameA, 'STARLINK-6292', 'STARLINK-6293');
    const frameC = withReplacedAscii(frameA, 'STARLINK-6292', 'STARLINK-6294');

    // No recordKeys / no recordKeyPrefix → content-hash dedupe keys (the
    // RemoteEpochStreamClient ingest path). A fresh full stream takes the
    // D.6 single-engine-call route (original aligned bytes, no rebuild).
    const first = await store.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([frameA, frameB]), { persist: false });
    expect(first).toBe(2);

    // Overlapping re-delivery: content keys must still dedupe A/B and only
    // materialize C (the filtered per-record route).
    const second = await store.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([frameA, frameB, frameC]), { persist: false });
    expect(second).toBe(1);

    // Full replay is a no-op.
    const replay = await store.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([frameA, frameB, frameC]), { persist: false });
    expect(replay).toBe(0);

    const rows = await store.query('SELECT OBJECT_NAME FROM OMM ORDER BY OBJECT_NAME', 'OMM');
    expect(rows.records).toEqual([
      { OBJECT_NAME: 'STARLINK-6292' },
      { OBJECT_NAME: 'STARLINK-6293' },
      { OBJECT_NAME: 'STARLINK-6294' },
    ]);
    store.destroy();
  });

  it('does not drop distinct records that collide on the legacy 32-bit content key (loop D.6)', async () => {
    // REAL BUG caught at catalog scale: dedupe keys carried only
    // (byteLength, fnv1a32) — a 250K bulk stream lost ~13 DISTINCT records
    // to birthday collisions. Craft a genuine same-length fnv1a32 collision
    // between two distinct OMM records and require BOTH to materialize.
    const fnv1a32 = (bytes: Uint8Array): number => {
      let hash = 0x811c9dc5;
      for (let i = 0; i < bytes.length; i += 1) {
        hash ^= bytes[i];
        hash = Math.imul(hash, 0x01000193);
      }
      return hash >>> 0;
    };
    const base = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    const nameAt = (() => {
      const needle = 'STARLINK-6292';
      outer: for (let i = 0; i <= base.byteLength - needle.length; i += 1) {
        for (let j = 0; j < needle.length; j += 1) {
          if (base[i + j] !== needle.charCodeAt(j)) continue outer;
        }
        return i;
      }
      throw new Error('fixture name not found');
    })();
    // Mutate 6 OBJECT_NAME characters (24 variant bits — 'STARLINK-6292'
    // positions 4..9 become printable ASCII), keeping byteLength identical.
    const mutateAt = nameAt + 4;
    const variant = (suffix: number): Uint8Array => {
      const out = new Uint8Array(base);
      for (let n = 0; n < 6; n += 1) {
        out[mutateAt + n] = 0x41 + ((suffix >> (4 * n)) & 0x0f);
      }
      return out;
    };
    // Birthday-search for an fnv1a32 collision; the hash state over the
    // unchanged prefix is cached so each candidate only hashes its tail
    // (expected first collision after ~82K candidates).
    let prefixHash = 0x811c9dc5;
    for (let i = 0; i < mutateAt; i += 1) {
      prefixHash ^= base[i];
      prefixHash = Math.imul(prefixHash, 0x01000193);
    }
    const tailHash = (candidate: Uint8Array): number => {
      let hash = prefixHash;
      for (let i = mutateAt; i < candidate.byteLength; i += 1) {
        hash ^= candidate[i];
        hash = Math.imul(hash, 0x01000193);
      }
      return hash >>> 0;
    };
    const seen = new Map<number, number>();
    let pair: [Uint8Array, Uint8Array] | null = null;
    for (let suffix = 0; suffix < 0x1000000; suffix += 1) {
      const candidate = variant(suffix);
      const hash = tailHash(candidate);
      const previous = seen.get(hash);
      if (previous !== undefined) {
        pair = [variant(previous), candidate];
        break;
      }
      seen.set(hash, suffix);
    }
    if (!pair) throw new Error('no fnv1a32 collision found in the search budget');
    expect(fnv1a32(pair[0])).toBe(fnv1a32(pair[1]));
    expect(pair[0]).not.toEqual(pair[1]);

    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });
    const ingested = await store.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream(pair), { persist: false });
    expect(ingested).toBe(2);
    const replay = await store.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream(pair), { persist: false });
    expect(replay).toBe(0);
    store.destroy();
  });

  it('honors legacy 32-bit content keys persisted by older builds (loop D.6 migration)', async () => {
    const fnv1a32 = (bytes: Uint8Array): number => {
      let hash = 0x811c9dc5;
      for (let i = 0; i < bytes.length; i += 1) {
        hash ^= bytes[i];
        hash = Math.imul(hash, 0x01000193);
      }
      return hash >>> 0;
    };
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });
    const frame = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    // Seed the ledger with the OLD key format (what a pre-D.6 build
    // persisted): `<standard>|stream|<len>|<fnv1a32 hex8>`.
    const legacyKey = `OMM|stream|${frame.byteLength}|${fnv1a32(frame).toString(16).padStart(8, '0')}`;
    const seeded = await store.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([frame]), {
      recordKeys: [legacyKey],
      persist: false,
    });
    expect(seeded).toBe(1);
    // A content-keyed re-delivery of the same bytes must STILL dedupe.
    const replay = await store.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([frame]), { persist: false });
    expect(replay).toBe(0);
    store.destroy();
  });

  it('preserves CAT replacement stream rows exactly as published', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'CAT',
        tableName: 'CAT',
        fileId: '$CAT',
        schema: CAT_SCHEMA,
      }],
    });

    const firstIss = stripSdnFlatBufferSizePrefix(buildCatFixture({
      objectName: 'ISS (ZARYA)',
      objectId: '1998-067A',
      noradCatId: 25544,
    }));
    const duplicateIss = stripSdnFlatBufferSizePrefix(buildCatFixture({
      objectName: 'ISS (ZARYA) DUPLICATE',
      objectId: '1998-067A',
      noradCatId: 25544,
    }));
    const hubble = stripSdnFlatBufferSizePrefix(buildCatFixture({
      objectName: 'HST',
      objectId: '1990-037B',
      noradCatId: 20580,
    }));

    const ingested = await store.ingestFlatBufferStream('CAT', flatSqlSizePrefixedStream([
      firstIss,
      duplicateIss,
      hubble,
    ]), {
      recordKeyPrefix: 'published:cat-snapshot',
      persist: false,
    });

    expect(ingested).toBe(3);
    expect(store.query('SELECT OBJECT_NAME, NORAD_CAT_ID FROM CAT ORDER BY NORAD_CAT_ID DESC LIMIT 10', 'CAT').records).toEqual([
      { OBJECT_NAME: 'ISS (ZARYA)', NORAD_CAT_ID: 25544 },
      { OBJECT_NAME: 'ISS (ZARYA) DUPLICATE', NORAD_CAT_ID: 25544 },
      { OBJECT_NAME: 'HST', NORAD_CAT_ID: 20580 },
    ]);
    expect(store.query(
      'SELECT NORAD_CAT_ID, COUNT(*) AS c FROM CAT WHERE NORAD_CAT_ID = 25544 GROUP BY NORAD_CAT_ID',
      'CAT',
    ).records).toEqual([{ NORAD_CAT_ID: 25544, c: 2 }]);
    store.destroy();
  });

  it('resumes native FlatSQL stream ingest from a skipped frame with a zero-offset buffer', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    const frame = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    const stream = flatSqlSizePrefixedStream([frame, frame]);
    const ingested = await store.ingestFlatBufferStream('OMM', stream, {
      persist: false,
      skipRecords: 1,
      recordKeyPrefix: 'published:bafkshard',
      recordKeyOffset: 1,
    });

    expect(ingested).toBe(1);
    expect(store.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 1', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
    expect(store.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
      recordCount: 1,
      ingestedRecordCount: 1,
    }));
  });

  it('trusts ordered published shard offsets when stale record keys claim missing rows', async () => {
    const restoreIndexedDb = installMemoryIndexedDb();
    try {
      await clearLocalFlatSqlStore({
        persistenceKey: 'stale-published-keys',
        standardIds: ['OMM'],
      });

      const first = await createLocalFlatSqlStore({
        persistenceKey: 'stale-published-keys',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      const frame = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
      await first.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([frame]), {
        persist: true,
        recordKeyPrefix: 'published:already-materialized',
      });
      await first.flush('OMM');
      first.destroy();

      await putLocalFlatSqlTestValue('stale-published-keys:OMM:record-keys', [
        'OMM|published:stale-shard|1',
      ]);

      const resumed = await createLocalFlatSqlStore({
        persistenceKey: 'stale-published-keys',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      const ingested = await resumed.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([frame, frame]), {
        persist: false,
        skipRecords: 1,
        recordKeyPrefix: 'published:stale-shard',
        recordKeyOffset: 1,
      });

      expect(ingested).toBe(1);
      expect(resumed.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 2,
        ingestedRecordCount: 1,
      }));
      resumed.destroy();
    } finally {
      restoreIndexedDb();
    }
  });

  it('records pin ledger entries for verified published FlatSQL shards', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    const stream = flatSqlSizePrefixedStream([stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES)]);
    await store.ingestFlatBufferStream('OMM', stream, {
      persist: false,
      recordKeyPrefix: 'published:bafkshard',
      pinLedgerEntries: [{
        cid: 'bafkshard',
        standardId: 'OMM',
        schemaName: 'OMM.fbs',
        providerPeerId: '16Uiu2HCelesTrak',
        providerPublicKey: 'provider-public-key',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'source-sha-001',
        queryProfile: 'dataset-publication-offset-v1',
        snapshotId: 'head-1',
        head: 'head-1',
        highWaterMark: 'head-1:1',
        byteHash: 'shard-sha',
        role: 'shard',
        rowCount: 1,
        byteCount: stream.byteLength,
        verificationState: 'verified',
        materializedAt: '2026-05-12T00:00:00.000Z',
        verifiedAt: '2026-05-12T00:00:00.000Z',
      }],
    });

    await expect(store.listPinLedgerEntries({
      standardId: 'OMM',
      providerPeerId: '16Uiu2HCelesTrak',
      queryProfile: 'dataset-publication-offset-v1',
      role: 'shard',
    })).resolves.toEqual([
      expect.objectContaining({
        cid: 'bafkshard',
        schemaName: 'OMM.fbs',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'source-sha-001',
        head: 'head-1',
        highWaterMark: 'head-1:1',
        byteHash: 'shard-sha',
        verificationState: 'verified',
      }),
    ]);
    await expect(Promise.resolve(store.getStats({ includeCachedBytes: false }))).resolves.toEqual([
      expect.objectContaining({
        standardId: 'OMM',
        pinnedRows: 1,
        pinnedBytes: stream.byteLength,
        snapshotId: 'head-1',
        head: 'head-1',
        highWaterMark: 'head-1:1',
        lastSyncedAt: '2026-05-12T00:00:00.000Z',
      }),
    ]);
  });

  it('clears an active standard store and pin ledger before replacing a full snapshot', async () => {
    const restoreIndexedDb = installMemoryIndexedDb();
    try {
      await clearLocalFlatSqlStore({
        persistenceKey: 'replace-active-store',
        standardIds: ['OMM'],
      });

      const first = await createLocalFlatSqlStore({
        persistenceKey: 'replace-active-store',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      const stream = flatSqlSizePrefixedStream([stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES)]);
      await first.ingestFlatBufferStream('OMM', stream, {
        persist: false,
        recordKeyPrefix: 'published:bafyold',
      });
      await first.recordPinLedgerEntries([{
        cid: 'bafyold',
        standardId: 'OMM',
        schemaName: 'OMM.fbs',
        providerPeerId: '16Uiu2HCelesTrak',
        providerPublicKey: 'provider-public-key',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'old-source-sha',
        queryProfile: 'dataset-publication-offset-v1',
        snapshotId: 'old-head',
        head: 'old-head',
        highWaterMark: 'old-head:1',
        role: 'shard',
        rowCount: 1,
        byteCount: stream.byteLength,
        verificationState: 'verified',
        materializedAt: '2026-05-12T00:00:00.000Z',
        verifiedAt: '2026-05-12T00:00:00.000Z',
        updatedAt: '2026-05-12T00:00:00.000Z',
      }], { persist: false });

      await first.clearStandard('OMM');

      expect(first.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([]);
      expect(first.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 0,
        ingestedRecordCount: 0,
        pinnedRows: 0,
        pinnedBytes: 0,
        cachedBytes: 0,
      }));
      await expect(first.listPinLedgerEntries({ standardId: 'OMM' })).resolves.toEqual([]);

      await first.ingestFlatBufferStream('OMM', stream, {
        recordKeyPrefix: 'published:bafynew',
      });
      await first.flush('OMM');
      first.destroy();

      const loaded = await createLocalFlatSqlStore({
        persistenceKey: 'replace-active-store',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      expect(loaded.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
      await expect(loaded.listPinLedgerEntries({ standardId: 'OMM' })).resolves.toEqual([]);
      loaded.destroy();
    } finally {
      restoreIndexedDb();
    }
  });

  it('stages a full snapshot replacement and swaps it into the active store on commit', async () => {
    const restoreIndexedDb = installMemoryIndexedDb();
    try {
      await clearLocalFlatSqlStore({
        persistenceKey: 'staged-replacement-store',
        standardIds: ['OMM'],
      });

      const active = await createLocalFlatSqlStore({
        persistenceKey: 'staged-replacement-store',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      const frame = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
      const oldStream = flatSqlSizePrefixedStream([frame, frame]);
      const newStream = flatSqlSizePrefixedStream([frame]);
      await active.ingestFlatBufferStream('OMM', oldStream, {
        recordKeyPrefix: 'published:old',
      });
      await active.recordPinLedgerEntries([{
        cid: 'old',
        standardId: 'OMM',
        schemaName: 'OMM.fbs',
        batchId: 'old-batch',
        queryProfile: 'dataset-publication-offset-v1',
        role: 'shard',
        rowCount: 2,
        byteCount: oldStream.byteLength,
        verificationState: 'verified',
        materializedAt: '2026-05-12T00:00:00.000Z',
        verifiedAt: '2026-05-12T00:00:00.000Z',
        updatedAt: '2026-05-12T00:00:00.000Z',
      }]);
      expect(active.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 2,
        pinnedRows: 2,
      }));

      const replacement = await active.createStandardReplacementStore('OMM');
      await replacement.ingestFlatBufferStream('OMM', newStream, {
        persist: false,
        recordKeyPrefix: 'published:new',
      });
      expect(active.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 2,
        pinnedRows: 2,
      }));

      await active.replaceStandardFrom('OMM', replacement, [{
        cid: 'new',
        standardId: 'OMM',
        schemaName: 'OMM.fbs',
        batchId: 'new-batch',
        queryProfile: 'dataset-publication-offset-v1',
        role: 'shard',
        rowCount: 1,
        byteCount: newStream.byteLength,
        verificationState: 'verified',
        materializedAt: '2026-05-13T00:00:00.000Z',
        verifiedAt: '2026-05-13T00:00:00.000Z',
        updatedAt: '2026-05-13T00:00:00.000Z',
      }]);

      expect(active.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 1,
        ingestedRecordCount: 1,
        pinnedRows: 1,
      }));
      await expect(active.listPinLedgerEntries({ standardId: 'OMM' })).resolves.toEqual([
        expect.objectContaining({ cid: 'new', batchId: 'new-batch' }),
      ]);
      active.destroy();

      const loaded = await createLocalFlatSqlStore({
        persistenceKey: 'staged-replacement-store',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      expect(loaded.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 1,
        pinnedRows: 1,
      }));
      await expect(loaded.listPinLedgerEntries({ standardId: 'OMM' })).resolves.toEqual([
        expect.objectContaining({ cid: 'new', batchId: 'new-batch' }),
      ]);
      loaded.destroy();
    } finally {
      restoreIndexedDb();
    }
  });

  it('decodes FlatSQL sync streams with zero-copy record views', () => {
    const first = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    const stream = flatSqlSizePrefixedStream([first]);

    const records = decodeFlatSqlSizePrefixedStream(stream);

    expect(records).toHaveLength(1);
    expect(records[0]?.buffer).toBe(stream.buffer);
  });

  it('scans native FlatSQL shard streams without materializing record views', () => {
    const direct = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    const stream = flatSqlSizePrefixedStream([direct, direct]);
    const firstFrameLength = 4 + direct.byteLength;

    expect(flatSqlSizePrefixedStreamInfo(stream)).toEqual({
      totalRecordCount: 2,
      ingestRecordCount: 2,
      ingestStartOffset: 0,
      allFramesHaveDirectFileIdentifier: true,
    });
    expect(flatSqlSizePrefixedStreamInfo(stream, 1)).toEqual({
      totalRecordCount: 2,
      ingestRecordCount: 1,
      ingestStartOffset: firstFrameLength,
      allFramesHaveDirectFileIdentifier: true,
    });
    expect(flatSqlSizePrefixedStreamInfo(flatSqlSizePrefixedStream([STARLINK_6292_OMM_BYTES]))).toEqual({
      totalRecordCount: 1,
      ingestRecordCount: 1,
      ingestStartOffset: 0,
      allFramesHaveDirectFileIdentifier: false,
    });
  });

  it('tracks downloaded FlatBuffer frames separately from materialized SQL rows', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [
      {
        cid: 'celestrak-omm-history',
        schemaName: 'OMM.fbs',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'fixture-batch',
        timestamp: '2026-05-11T04:02:25Z',
        dataBytes: STARLINK_6292_OMM_BYTES,
      },
      {
        cid: 'celestrak-omm-history',
        schemaName: 'OMM.fbs',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'fixture-batch',
        timestamp: '2026-05-11T07:02:25Z',
        dataBytes: STARLINK_6292_OMM_BYTES,
      },
    ], { source: 'space-data-network-02', persist: false });

    expect(store.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
      recordCount: 2,
      ingestedRecordCount: 2,
    }));
  });

  it('keeps query results fresh across ingest, persisted load, query template changes, and namespace changes', async () => {
    const restoreIndexedDb = installMemoryIndexedDb();
    try {
      await clearLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-a',
        standardIds: ['OMM', 'PNM'],
      });
      await clearLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-b',
        standardIds: ['OMM'],
      });

      const first = await createLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-a',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });

      expect(first.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([]);
      await first.ingestRecords('OMM', [{
        cid: 'cache-omm-1',
        schemaName: 'OMM.fbs',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'fixture-batch',
        timestamp: '2026-05-11T04:02:25Z',
        dataBytes: STARLINK_6292_OMM_BYTES,
      }]);

      expect(first.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
      expect(first.query('SELECT OBJECT_NAME FROM OMM WHERE NORAD_CAT_ID = 56775 LIMIT 10', 'OMM').records).toEqual([{ OBJECT_NAME: 'STARLINK-6292' }]);

      await first.ingestRecords('OMM', [{
        cid: 'cache-omm-2',
        schemaName: 'OMM.fbs',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'fixture-batch',
        timestamp: '2026-05-11T07:02:25Z',
        dataBytes: STARLINK_6292_OMM_BYTES,
      }]);
      expect(first.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }, { NORAD_CAT_ID: 56775 }]);
      await first.flush('OMM');
      first.destroy();

      const loaded = await createLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-a',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      expect(loaded.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }, { NORAD_CAT_ID: 56775 }]);
      expect(loaded.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 2,
        ingestedRecordCount: 2,
      }));

      const otherNamespace = await createLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-b',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      expect(otherNamespace.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([]);

      const otherTemplate = await createLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-a',
        schemas: [{
          standardId: 'PNM',
          tableName: 'PNM',
          fileId: '$PNM',
          schema: PNM_SCHEMA,
        }],
      });
      expect(otherTemplate.query('SELECT * FROM PNM LIMIT 0', 'PNM').columns).not.toContain('NORAD_CAT_ID');
      expect(otherTemplate.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        standardId: 'PNM',
        recordCount: 0,
      }));
    } finally {
      restoreIndexedDb();
    }
  });

  it('persists local FlatSQL bytes through the desktop FlatBuffer storage API when configured', async () => {
    const persisted = new Map<string, Uint8Array>();
    const calls: Array<{ method: string; url: string }> = [];
    const fetchMock = async (url: string | URL | Request, init?: RequestInit): Promise<Response> => {
      const requestUrl = String(url);
      const method = String(init?.method ?? 'GET').toUpperCase();
      calls.push({ method, url: requestUrl });
      const key = decodeURIComponent(new URL(requestUrl).pathname.split('/').pop() ?? '');
      if (method === 'GET') {
        const bytes = persisted.get(key);
        return bytes
          ? new Response(bytes.slice(), { status: 200 })
          : new Response('missing', { status: 404 });
      }
      if (method === 'PUT') {
        const body = init?.body;
        const bytes = body instanceof Uint8Array
          ? body
          : body instanceof ArrayBuffer
            ? new Uint8Array(body)
            : new Uint8Array(await new Response(body as BodyInit).arrayBuffer());
        persisted.set(key, bytes.slice());
        return new Response(null, { status: 204 });
      }
      if (method === 'DELETE') {
        persisted.delete(key);
        return new Response(null, { status: 204 });
      }
      return new Response('bad method', { status: 405 });
    };

    const first = await createLocalFlatSqlStore({
      persistenceKey: 'desktop-flatbuffer-cache',
      desktopPersistenceBaseUrl: 'http://desktop.local',
      fetch: fetchMock,
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });
    await first.ingestRecords('OMM', [{
      cid: 'desktop-cache-omm',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }]);
    await first.flush('OMM');
    first.destroy();

    const loaded = await createLocalFlatSqlStore({
      persistenceKey: 'desktop-flatbuffer-cache',
      desktopPersistenceBaseUrl: 'http://desktop.local',
      fetch: fetchMock,
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    expect(loaded.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
    // D.1 layout: per-(standard, source) aligned streams + a source list —
    // records without an explicit source live in the `local` partition.
    expect(persisted.has('desktop-flatbuffer-cache:OMM:sources')).toBe(true);
    expect(persisted.has('desktop-flatbuffer-cache:OMM:src:local')).toBe(true);
    expect(persisted.has('desktop-flatbuffer-cache:OMM:record-keys')).toBe(true);
    expect(calls).toEqual(expect.arrayContaining([
      expect.objectContaining({ method: 'PUT', url: 'http://desktop.local/api/flatsql/persistence/desktop-flatbuffer-cache%3AOMM%3Asrc%3Alocal' }),
      expect.objectContaining({ method: 'GET', url: 'http://desktop.local/api/flatsql/persistence/desktop-flatbuffer-cache%3AOMM%3Asrc%3Alocal' }),
    ]));
    loaded.destroy();
  });

  it('can defer published-shard pin ledger persistence until the FlatSQL checkpoint flush', async () => {
    const persisted = new Map<string, Uint8Array>();
    const calls: Array<{ method: string; key: string }> = [];
    const fetchMock = async (url: string | URL | Request, init?: RequestInit): Promise<Response> => {
      const method = String(init?.method ?? 'GET').toUpperCase();
      const key = decodeURIComponent(new URL(String(url)).pathname.split('/').pop() ?? '');
      calls.push({ method, key });
      if (method === 'GET') {
        const bytes = persisted.get(key);
        return bytes
          ? new Response(bytes.slice(), { status: 200 })
          : new Response('missing', { status: 404 });
      }
      if (method === 'PUT') {
        const bytes = init?.body instanceof Uint8Array
          ? init.body
          : new Uint8Array(await new Response(init?.body as BodyInit).arrayBuffer());
        persisted.set(key, bytes.slice());
        return new Response(null, { status: 204 });
      }
      return new Response(null, { status: method === 'DELETE' ? 204 : 405 });
    };

    const store = await createLocalFlatSqlStore({
      persistenceKey: 'deferred-pin-ledger',
      desktopPersistenceBaseUrl: 'http://desktop.local',
      fetch: fetchMock,
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });
    await store.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES)]), {
      persist: false,
      recordKeyPrefix: 'published:bafycheckpoint',
    });
    await store.recordPinLedgerEntries([{
      cid: 'bafycheckpoint',
      standardId: 'OMM',
      schemaName: 'OMM.fbs',
      role: 'shard',
      rowCount: 1,
      byteCount: STARLINK_6292_OMM_BYTES.byteLength,
      verificationState: 'verified',
      materializedAt: '2026-05-15T00:00:00.000Z',
      verifiedAt: '2026-05-15T00:00:00.000Z',
      updatedAt: '2026-05-15T00:00:00.000Z',
    }], { persist: false });

    await expect(store.listPinLedgerEntries({ standardId: 'OMM' })).resolves.toHaveLength(1);
    expect(calls.some((call) => call.method === 'PUT' && call.key === 'deferred-pin-ledger:pin-ledger')).toBe(false);

    await store.flush('OMM');

    expect(calls.some((call) => call.method === 'PUT' && call.key === 'deferred-pin-ledger:pin-ledger')).toBe(true);
    store.destroy();
  });

  it('allows callers to clear persisted local FlatSQL data without IndexedDB support', async () => {
    await expect(clearLocalFlatSqlStore({
      persistenceKey: 'sdn-data:configured:space-data-network-02',
      standardIds: ['OMM', 'PNM'],
    })).resolves.toBeUndefined();
  });

  it('repairs persisted record-key counts that outrun rebuilt FlatSQL rows', async () => {
    const restoreIndexedDb = installMemoryIndexedDb();
    try {
      await clearLocalFlatSqlStore({
        persistenceKey: 'repair-record-keys',
        standardIds: ['OMM'],
      });

      const first = await createLocalFlatSqlStore({
        persistenceKey: 'repair-record-keys',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      const stream = flatSqlSizePrefixedStream([stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES)]);
      await first.ingestFlatBufferStream('OMM', stream, {
        persist: true,
        recordKeyPrefix: 'published:bafkshard',
      });
      await first.flush('OMM');
      first.destroy();

      await putLocalFlatSqlTestValue('repair-record-keys:OMM:record-keys', [
        'OMM|published:bafkshard|0',
        'OMM|published:bafkshard|1',
      ]);

      const repaired = await createLocalFlatSqlStore({
        persistenceKey: 'repair-record-keys',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });

      expect(repaired.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 1,
        ingestedRecordCount: 0,
      }));
      repaired.destroy();
    } finally {
      restoreIndexedDb();
    }
  });

  it('rejects non-read-only SQL before it reaches FlatSQL', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    expect(isReadOnlyFlatSqlQuery('SELECT * FROM OMM LIMIT 1')).toBe(true);
    expect(isReadOnlyFlatSqlQuery('WITH latest AS (SELECT * FROM OMM) SELECT * FROM latest')).toBe(true);
    expect(isReadOnlyFlatSqlQuery('DELETE FROM OMM')).toBe(false);
    expect(isReadOnlyFlatSqlQuery('SELECT * FROM OMM; DELETE FROM OMM')).toBe(false);
    expect(isReadOnlyFlatSqlQuery('PRAGMA table_info(OMM)')).toBe(false);

    expect(() => store.query('DELETE FROM OMM')).toThrow(/read-only SELECT/);
  });

  it('enforces local SQL byte limits after FlatSQL execution', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-byte-limit',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], { persist: false });

    expect(() => store.query('SELECT OBJECT_NAME FROM OMM LIMIT 10', 'OMM', {
      maxBytes: 4,
      maxLimit: 10,
      timeoutMs: 5000,
    })).toThrow(/byte limit/i);
  });

  it('registers SDS schemas whose comments contain URLs without exposing comment tokens as columns', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'PNM',
        tableName: 'PNM',
        fileId: '$PNM',
        schema: PNM_SCHEMA,
      }],
    });

    expect(store.getStats()).toEqual([expect.objectContaining({
      standardId: 'PNM',
      tableName: 'PNM',
      recordCount: 0,
    })]);
    expect(store.query('SELECT * FROM PNM LIMIT 0', 'PNM').columns).not.toContain('https');
  });
});

/** Same-length ASCII substitution inside a FlatBuffer fixture (distinct records, identical layout). */
function withReplacedAscii(bytes: Uint8Array, search: string, replacement: string): Uint8Array {
  if (search.length !== replacement.length) throw new Error('replacement must preserve length');
  const out = new Uint8Array(bytes);
  const needle = Array.from(search, (char) => char.charCodeAt(0));
  outer: for (let index = 0; index <= out.byteLength - needle.length; index += 1) {
    for (let j = 0; j < needle.length; j += 1) {
      if (out[index + j] !== needle[j]) continue outer;
    }
    for (let j = 0; j < needle.length; j += 1) {
      out[index + j] = replacement.charCodeAt(j);
    }
    return out;
  }
  throw new Error(`ASCII sequence ${search} not found`);
}

function flatSqlSizePrefixedStream(records: Uint8Array[]): Uint8Array {
  const totalLength = records.reduce((sum, frame) => sum + 4 + frame.byteLength, 0);
  const out = new Uint8Array(totalLength);
  const view = new DataView(out.buffer);
  let offset = 0;
  for (const frame of records) {
    view.setUint32(offset, frame.byteLength, true);
    offset += 4;
    out.set(frame, offset);
    offset += frame.byteLength;
  }
  return out;
}

function buildCatFixture(options: { objectName: string; objectId: string; noradCatId: number }): Uint8Array {
  const builder = new flatbuffers.Builder(256);
  const objectName = builder.createString(options.objectName);
  const objectId = builder.createString(options.objectId);
  CAT.startCAT(builder);
  CAT.addObjectName(builder, objectName);
  CAT.addObjectId(builder, objectId);
  CAT.addNoradCatId(builder, options.noradCatId);
  CAT.addObjectType(builder, spaceObjectClass.PAYLOAD);
  CAT.addOpsStatusCode(builder, operationalState.OPERATIONAL);
  const cat = CAT.endCAT(builder);
  CAT.finishSizePrefixedCATBuffer(builder, cat);
  return builder.asUint8Array();
}

function putLocalFlatSqlTestValue(key: string, value: unknown): Promise<void> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open('sdn-local-flatsql', 1);
    request.onerror = () => reject(request.error ?? new Error('IndexedDB open failed'));
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains('datastores')) db.createObjectStore('datastores');
    };
    request.onsuccess = () => {
      const db = request.result;
      const transaction = db.transaction('datastores', 'readwrite');
      transaction.objectStore('datastores').put(value, key);
      transaction.oncomplete = () => {
        db.close();
        resolve();
      };
      transaction.onerror = () => {
        db.close();
        reject(transaction.error ?? new Error('IndexedDB write failed'));
      };
    };
  });
}

function installMemoryIndexedDb(): () => void {
  const hadIndexedDb = 'indexedDB' in globalThis;
  const previous = globalThis.indexedDB;
  const databases = new Map<string, Map<string, unknown>>();
  const factory = {
    open(name: string): MemoryIDBRequest {
      const request = new MemoryIDBRequest();
      setTimeout(() => {
        let stores = databases.get(name);
        const needsUpgrade = !stores;
        if (!stores) {
          stores = new Map<string, unknown>();
          databases.set(name, stores);
        }
        request.result = new MemoryIDBDatabase(stores);
        if (needsUpgrade) request.onupgradeneeded?.({ target: request });
        request.onsuccess?.({ target: request });
      }, 0);
      return request;
    },
    deleteDatabase(name: string): MemoryIDBRequest {
      const request = new MemoryIDBRequest();
      setTimeout(() => {
        databases.delete(name);
        request.onsuccess?.({ target: request });
      }, 0);
      return request;
    },
  };
  Object.defineProperty(globalThis, 'indexedDB', {
    configurable: true,
    writable: true,
    value: factory,
  });
  return () => {
    if (hadIndexedDb) {
      Object.defineProperty(globalThis, 'indexedDB', {
        configurable: true,
        writable: true,
        value: previous,
      });
      return;
    }
    delete (globalThis as { indexedDB?: IDBFactory }).indexedDB;
  };
}

class MemoryIDBRequest {
  result: unknown;
  error: Error | null = null;
  onsuccess: ((event: { target: MemoryIDBRequest }) => void) | null = null;
  onerror: ((event: { target: MemoryIDBRequest }) => void) | null = null;
  onupgradeneeded: ((event: { target: MemoryIDBRequest }) => void) | null = null;
}

class MemoryIDBDatabase {
  readonly objectStoreNames: { contains: (name: string) => boolean };

  constructor(private readonly stores: Map<string, unknown>) {
    this.objectStoreNames = {
      contains: (name: string) => this.stores.has(name),
    };
  }

  createObjectStore(name: string): void {
    if (!this.stores.has(name)) this.stores.set(name, new Map<string, unknown>());
  }

  transaction(_storeName: string, _mode: IDBTransactionMode): MemoryIDBTransaction {
    return new MemoryIDBTransaction(this.stores);
  }

  close(): void {}
}

class MemoryIDBTransaction {
  oncomplete: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(private readonly stores: Map<string, unknown>) {}

  objectStore(name: string): MemoryIDBObjectStore {
    let store = this.stores.get(name);
    if (!(store instanceof Map)) {
      store = new Map<string, unknown>();
      this.stores.set(name, store);
    }
    return new MemoryIDBObjectStore(store, this);
  }

  complete(): void {
    setTimeout(() => this.oncomplete?.(), 0);
  }
}

class MemoryIDBObjectStore {
  constructor(
    private readonly values: Map<string, unknown>,
    private readonly transaction: MemoryIDBTransaction,
  ) {}

  get(key: string): MemoryIDBRequest {
    const request = new MemoryIDBRequest();
    setTimeout(() => {
      request.result = this.values.get(key);
      request.onsuccess?.({ target: request });
      this.transaction.complete();
    }, 0);
    return request;
  }

  put(value: unknown, key: string): MemoryIDBRequest {
    const request = new MemoryIDBRequest();
    setTimeout(() => {
      this.values.set(key, value);
      request.result = key;
      request.onsuccess?.({ target: request });
      this.transaction.complete();
    }, 0);
    return request;
  }

  delete(key: string): MemoryIDBRequest {
    const request = new MemoryIDBRequest();
    setTimeout(() => {
      this.values.delete(key);
      request.onsuccess?.({ target: request });
      this.transaction.complete();
    }, 0);
    return request;
  }
}
