/**
 * Loop D.4 — wire-compat verification against a RECORDED peer stream.
 *
 * The fixtures under src/testdata/flatsql-sync are GENUINE byte streams
 * emitted by the deployed server implementation
 * (sdn-server/internal/protocol.FlatSQLSyncHandler — recorded by
 * TestRecordFlatSQLSyncWireFixtures over a real FlatSQLStore with
 * source-tagged records and a registered dataset publication). This suite
 * proves the datasync-fed engine store consumes a deployed peer's wire
 * format unchanged:
 *
 *  - byte-stability: every recorded response hashes to its pinned sha256;
 *  - cursor semantics: the sync cursor is the server's rowid-snapshot
 *    cursor over the sdn_record_index rowid space (v1, base64url JSON),
 *    parsed and resumed without reinterpretation;
 *  - shard/PNM-adjacent formats: chunk header + LE size-prefixed frames,
 *    manifest segments, and published-shard headers all parse byte-stable;
 *  - provenance: records land in the engine store under their true
 *    provider/source partitions with pin-ledger provenance intact;
 *  - computeCID identity: sha256(frame) equals the server-assigned cid;
 *  - idempotence: replaying the recorded stream is a no-op.
 */
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import { sha256 } from './crypto/hd-wallet';
import { syncFlatSqlSchemaIntoStore } from './datasync-session';
import { FlatSQLEngineRecordStore } from './engine-record-store';
import {
  decodeFlatSqlPublishedShard,
  decodeFlatSqlSyncChunk,
  decodeFlatSqlSyncManifest,
  FLATSQL_SYNC_PROTOCOL_ID,
  type FlatSqlSyncQuery,
} from './flatsql-sync';

const require = createRequire(import.meta.url);
const FIXTURE_DIR = join(dirname(fileURLToPath(import.meta.url)), 'testdata', 'flatsql-sync');

const OMM_SCHEMA = readFileSync(require.resolve('spacedatastandards.org/schema/OMM/main.fbs'), 'utf8');
const OMM_STANDARD = {
  standardId: 'OMM',
  tableName: 'OMM',
  fileId: '$OMM',
  schema: OMM_SCHEMA,
};

interface FixtureCursorMeta {
  encoded: string;
  v: number;
  mode: string;
  afterRowId: number;
  maxRowId: number;
  snapshotId: string;
}

interface FixtureMeta {
  protocol: string;
  records: Array<{
    cid: string;
    norad: number;
    objectName: string;
    providerId: string;
    sourceName: string;
    batchId: string;
    sizeBytes: number;
  }>;
  page1: FixturePageMeta;
  page2: FixturePageMeta;
  manifest: { file: string; sha256: string };
  publishedShard: {
    file: string;
    sha256: string;
    shardCid: string;
    shardSha256: string;
    rowCount: number;
    byteCount: number;
    providerId: string;
    sourceName: string;
    batchId: string;
  };
}

interface FixturePageMeta {
  file: string;
  sha256: string;
  totalCount: number;
  count: number;
  snapshotId: string;
  head: string;
  cursor?: FixtureCursorMeta | null;
  nextCursor?: FixtureCursorMeta | null;
  resultCids: string[];
}

const meta = JSON.parse(readFileSync(join(FIXTURE_DIR, 'meta.json'), 'utf8')) as FixtureMeta;

function fixtureBytes(name: string): Uint8Array {
  return new Uint8Array(readFileSync(join(FIXTURE_DIR, name)));
}

async function sha256Hex(bytes: Uint8Array): Promise<string> {
  return Array.from(await sha256(bytes)).map((b) => b.toString(16).padStart(2, '0')).join('');
}

function decodeRowidCursor(cursor: string): { v: number; mode: string; after_row_id: number; max_row_id: number; snapshot_id?: string } {
  return JSON.parse(Buffer.from(cursor, 'base64url').toString('utf8'));
}

describe('D.4 wire-compat: recorded peer stream (sdn-server FlatSQLSyncHandler output)', () => {
  it('recorded responses are byte-stable (sha256-pinned)', async () => {
    for (const fixture of [meta.page1, meta.page2, meta.manifest, meta.publishedShard]) {
      expect(await sha256Hex(fixtureBytes(fixture.file))).toBe(fixture.sha256);
    }
    expect(meta.protocol).toBe(FLATSQL_SYNC_PROTOCOL_ID);
  });

  it('parses the chunk wire format and the rowid-snapshot cursor space unchanged', async () => {
    const chunk = decodeFlatSqlSyncChunk(fixtureBytes(meta.page1.file));
    expect(chunk.header.syncProtocol).toBe(FLATSQL_SYNC_PROTOCOL_ID);
    expect(chunk.header.schema).toBe('OMM.fbs');
    expect(chunk.header.totalCount).toBe(meta.page1.totalCount);
    expect(chunk.header.count).toBe(meta.page1.count);
    expect(chunk.header.snapshotId).toBe(meta.page1.snapshotId);
    expect(chunk.header.head).toBe(meta.page1.head);
    expect(chunk.records).toHaveLength(meta.page1.count);

    // GROUND TRUTH: the datasync cursor is the server's rowid-snapshot
    // cursor over sdn_record_index.rowid — v1 base64url JSON. Parse it the
    // way a deployed peer produced it and assert the rowid boundaries.
    const cursor = decodeRowidCursor(chunk.header.cursor);
    expect(cursor).toMatchObject({
      v: 1,
      mode: 'rowid-snapshot',
      after_row_id: meta.page1.cursor!.afterRowId,
      max_row_id: meta.page1.cursor!.maxRowId,
    });
    const nextCursor = decodeRowidCursor(chunk.header.nextCursor);
    expect(nextCursor).toMatchObject({
      v: 1,
      mode: 'rowid-snapshot',
      after_row_id: meta.page1.nextCursor!.afterRowId,
      max_row_id: meta.page1.nextCursor!.maxRowId,
      snapshot_id: meta.page1.snapshotId,
    });

    // Refs carry the server-assigned cids and per-record provenance.
    expect(chunk.header.results.map((ref) => ref.cid)).toEqual(meta.page1.resultCids);
    for (const ref of chunk.header.results) {
      const record = meta.records.find((entry) => entry.cid === ref.cid)!;
      expect(record).toBeDefined();
      expect(ref.providerId).toBe(record.providerId);
      expect(ref.sourceName).toBe(record.sourceName);
      expect(ref.batchId).toBe(record.batchId);
      expect(ref.sizeBytes).toBe(record.sizeBytes);
    }

    // computeCID identity: the server cid is sha256 over the exact frame
    // bytes it streams — byte-identical on this side of the wire.
    for (let index = 0; index < chunk.records.length; index += 1) {
      expect(await sha256Hex(chunk.records[index])).toBe(meta.page1.resultCids[index]);
    }
  });

  it('materializes the recorded cursor walk into the engine store with true provenance, idempotently', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    const page1 = decodeFlatSqlSyncChunk(fixtureBytes(meta.page1.file));
    const page2 = decodeFlatSqlSyncChunk(fixtureBytes(meta.page2.file));

    // Page 2 is the server's continuation for page 1's next_cursor.
    expect(page2.header.snapshotId).toBe(page1.header.snapshotId);
    expect(page2.header.nextCursor).toBe('');

    const first = await store.ingestSyncChunk(page1);
    const second = await store.ingestSyncChunk(page2);
    expect(first.ingestedRecords).toBe(2);
    expect(second.ingestedRecords).toBe(1);
    expect([...new Set([...first.sources, ...second.sources])].sort()).toEqual(['celestrak-gp', 'spacetrack-gp']);

    // True provider/source partitions — the server layout, not `local`.
    const rows = await store.standardsStore!.query(
      'SELECT NORAD_CAT_ID, OBJECT_NAME, _source FROM OMM ORDER BY NORAD_CAT_ID LIMIT 10',
      'OMM',
    );
    expect(rows.records).toEqual([
      { NORAD_CAT_ID: 25544, OBJECT_NAME: 'ISS (ZARYA)', _source: 'OMM@celestrak-gp' },
      { NORAD_CAT_ID: 43013, OBJECT_NAME: 'NOAA-20', _source: 'OMM@spacetrack-gp' },
      { NORAD_CAT_ID: 56775, OBJECT_NAME: 'STARLINK-6292', _source: 'OMM@celestrak-gp' },
    ]);

    // Envelope index rows carry the server-assigned cids.
    const envelopes = store.sql("SELECT cid FROM sdn_record_index WHERE schema_name = 'OMM.fbs' ORDER BY cid");
    expect(envelopes.rows.map((row) => row[0])).toEqual(meta.records.map((record) => record.cid).sort());

    // Second replay of the SAME recorded peer stream is a no-op.
    const replay1 = await store.ingestSyncChunk(decodeFlatSqlSyncChunk(fixtureBytes(meta.page1.file)));
    const replay2 = await store.ingestSyncChunk(decodeFlatSqlSyncChunk(fixtureBytes(meta.page2.file)));
    expect(replay1).toMatchObject({ ingestedRecords: 0, indexedEnvelopes: 0, replayed: true });
    expect(replay2).toMatchObject({ ingestedRecords: 0, indexedEnvelopes: 0, replayed: true });
    const count = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
    expect(count.records).toEqual([{ n: 3 }]);
    await store.close();
  });

  it('drives a full recorded sync session (the SDNNode syncFlatSqlSchema walk) end to end, idempotently', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    // Replay the RECORDED peer: the transport answers exactly like the live
    // handler did — first page for the cold request, second page for page 1's
    // next_cursor (asserted verbatim against the recorded request).
    const transport = {
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery) {
        if (!query.cursor) return decodeFlatSqlSyncChunk(fixtureBytes(meta.page1.file));
        expect(query.cursor).toBe(meta.page1.nextCursor!.encoded);
        expect(query.snapshotId).toBe(meta.page1.snapshotId);
        expect(query.head).toBe(meta.page1.head);
        return decodeFlatSqlSyncChunk(fixtureBytes(meta.page2.file));
      },
    };

    const summary = await syncFlatSqlSchemaIntoStore(transport, store, { targetPeerId: 'recorded-peer', schema: 'OMM.fbs' });
    expect(summary).toMatchObject({
      schema: 'OMM.fbs',
      standardId: 'OMM',
      chunks: 2,
      totalRecords: 3,
      ingestedRecords: 3,
      indexedEnvelopes: 3,
      snapshotId: meta.page1.snapshotId,
      head: meta.page1.head,
      nextCursor: '',
      complete: true,
    });
    expect(summary.sources.sort()).toEqual(['celestrak-gp', 'spacetrack-gp']);

    // A second session over the same recorded stream is a no-op.
    const replay = await syncFlatSqlSchemaIntoStore(transport, store, { targetPeerId: 'recorded-peer', schema: 'OMM.fbs' });
    expect(replay).toMatchObject({ chunks: 2, totalRecords: 3, ingestedRecords: 0, indexedEnvelopes: 0, complete: true });
    const count = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
    expect(count.records).toEqual([{ n: 3 }]);
    await store.close();
  });

  it('parses the recorded manifest with segments in the same cursor space', () => {
    const manifest = decodeFlatSqlSyncManifest(fixtureBytes(meta.manifest.file));
    expect(manifest.syncProtocol).toBe(FLATSQL_SYNC_PROTOCOL_ID);
    expect(manifest.schema).toBe('OMM.fbs');
    expect(manifest.totalCount).toBe(3);
    expect(manifest.segments.map((segment) => segment.rowCount)).toEqual([2, 1]);
    // Segment fan-out cursors live in the SAME rowid-snapshot space.
    const segmentCursor = decodeRowidCursor(manifest.segments[0].nextCursor);
    expect(segmentCursor.mode).toBe('rowid-snapshot');
    expect(segmentCursor.v).toBe(1);
    expect(manifest.segments[1].nextCursor).toBe('');
  });

  it('materializes the recorded published shard with pin-ledger provenance intact, idempotently', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    const shard = decodeFlatSqlPublishedShard(fixtureBytes(meta.publishedShard.file));
    expect(shard.header.syncProtocol).toBe(FLATSQL_SYNC_PROTOCOL_ID);
    expect(shard.header.cid).toBe(meta.publishedShard.shardCid);
    expect(shard.header.providerId).toBe(meta.publishedShard.providerId);
    expect(shard.header.sourceName).toBe(meta.publishedShard.sourceName);
    expect(shard.header.batchId).toBe(meta.publishedShard.batchId);
    expect(shard.header.rowCount).toBe(meta.publishedShard.rowCount);
    expect(shard.streamBytes.byteLength).toBe(meta.publishedShard.byteCount);
    expect(await sha256Hex(shard.streamBytes)).toBe(meta.publishedShard.shardSha256);

    const result = await store.ingestPublishedShard(shard, { providerPeerId: '12D3KooWRecordedPeer' });
    expect(result).toMatchObject({
      standardId: 'OMM',
      ingestedRecords: 2,
      indexedEnvelopes: 2,
      sources: ['celestrak-gp'],
      replayed: false,
    });

    const rows = await store.standardsStore!.query(
      'SELECT NORAD_CAT_ID, _source FROM OMM ORDER BY NORAD_CAT_ID LIMIT 10',
      'OMM',
    );
    expect(rows.records).toEqual([
      { NORAD_CAT_ID: 25544, _source: 'OMM@celestrak-gp' },
      { NORAD_CAT_ID: 56775, _source: 'OMM@celestrak-gp' },
    ]);

    // Pin-ledger provenance from the recorded header.
    const ledger = await store.listPinLedgerEntries({ cid: meta.publishedShard.shardCid });
    expect(ledger).toHaveLength(1);
    expect(ledger[0]).toMatchObject({
      standardId: 'OMM',
      providerId: meta.publishedShard.providerId,
      sourceName: meta.publishedShard.sourceName,
      batchId: meta.publishedShard.batchId,
      byteHash: meta.publishedShard.shardSha256,
      role: 'shard',
      rowCount: meta.publishedShard.rowCount,
      byteCount: meta.publishedShard.byteCount,
      verificationState: 'verified',
    });

    // Envelope cids of the shard frames are the server-assigned record cids.
    const celestrakCids = meta.records
      .filter((record) => record.sourceName === 'celestrak-gp')
      .map((record) => record.cid)
      .sort();
    const envelopes = store.sql("SELECT cid FROM sdn_record_index WHERE schema_name = 'OMM.fbs' ORDER BY cid");
    expect(envelopes.rows.map((row) => row[0])).toEqual(celestrakCids);

    // Re-delivery of the recorded shard is a no-op.
    const replay = await store.ingestPublishedShard(
      decodeFlatSqlPublishedShard(fixtureBytes(meta.publishedShard.file)),
      { providerPeerId: '12D3KooWRecordedPeer' },
    );
    expect(replay).toMatchObject({ ingestedRecords: 0, replayed: true });
    const count = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
    expect(count.records).toEqual([{ n: 2 }]);
    await store.close();
  });
});
