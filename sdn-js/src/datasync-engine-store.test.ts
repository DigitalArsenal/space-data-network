/**
 * Loop D.4 — datasync-fed store: published-shard / flatsql-sync ingest
 * materializes into THE engine store (FlatSQLEngineRecordStore) with
 * pin-ledger provenance (provider/source/batch) intact.
 *
 * Covers: sync-chunk → engine materialization with TRUE provenance
 * (per-provider source partitions, not `local`), envelope index rows,
 * pin-ledger provenance for published shards, idempotent replay under sync
 * re-delivery (record-key ledger + pin ledger), and durability of the
 * datasync-fed state across reload.
 */
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { describe, expect, it } from 'vitest';

import { sha256 } from './crypto/hd-wallet';
import { FlatSQLEngineRecordStore } from './engine-record-store';
import {
  decodeFlatSqlPublishedShard,
  decodeFlatSqlSyncChunk,
  FLATSQL_SYNC_PROTOCOL_ID,
  type FlatSqlSyncChunk,
} from './flatsql-sync';
import { MemoryFlatSqlPersistenceStore } from './local-flatsql';

const require = createRequire(import.meta.url);

const OMM_SCHEMA = readFileSync(require.resolve('spacedatastandards.org/schema/OMM/main.fbs'), 'utf8');
const STARLINK_6292_OMM_BYTES = new Uint8Array(Buffer.from('HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA', 'base64'));

const OMM_STANDARD = {
  standardId: 'OMM',
  tableName: 'OMM',
  fileId: '$OMM',
  schema: OMM_SCHEMA,
};

const textEncoder = new TextEncoder();

/** Wire framing: [u32 BE length][JSON] header, then LE size-prefixed frames. */
function encodeSyncResponseBytes(header: Record<string, unknown>, frames: Uint8Array[]): Uint8Array {
  const json = textEncoder.encode(JSON.stringify(header));
  const streamLength = frames.reduce((sum, frame) => sum + 4 + frame.byteLength, 0);
  const bytes = new Uint8Array(4 + json.byteLength + streamLength);
  const view = new DataView(bytes.buffer);
  view.setUint32(0, json.byteLength, false);
  bytes.set(json, 4);
  let offset = 4 + json.byteLength;
  for (const frame of frames) {
    view.setUint32(offset, frame.byteLength, true);
    bytes.set(frame, offset + 4);
    offset += 4 + frame.byteLength;
  }
  return bytes;
}

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

async function sha256Hex(bytes: Uint8Array): Promise<string> {
  return Array.from(await sha256(bytes)).map((b) => b.toString(16).padStart(2, '0')).join('');
}

const OMM_A = STARLINK_6292_OMM_BYTES;
const OMM_B = withReplacedAscii(STARLINK_6292_OMM_BYTES, 'STARLINK-6292', 'STARLINK-6293');
const OMM_C = withReplacedAscii(STARLINK_6292_OMM_BYTES, 'STARLINK-6292', 'STARLINK-6294');

function mixedProvenanceChunk(): FlatSqlSyncChunk {
  const header = {
    schema: 'OMM.fbs',
    total_count: 3,
    count: 3,
    limit: 3,
    offset: 0,
    cursor: 'cursor-1',
    next_cursor: '',
    snapshot_id: 'snapshot-1',
    head: 'snapshot-1',
    high_water_mark: '1:2:3:3',
    scan_hash: 'scan-1',
    chunk_hash: 'scan-1',
    query_profile: 'dataset-publication-offset-v1',
    sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
    max_chunk_size: 50_000,
    transports: ['wss'],
    results: [
      {
        schema_name: 'OMM.fbs',
        cid: 'cid-omm-a',
        peer_id: 'peer-provider',
        provider_id: 'space-data-network-02',
        source_name: 'celestrak-gp',
        batch_id: 'batch-a',
        timestamp: '2026-05-10T12:00:00Z',
        size_bytes: OMM_A.byteLength,
      },
      {
        schema_name: 'OMM.fbs',
        cid: 'cid-omm-b',
        peer_id: 'peer-provider',
        provider_id: 'space-data-network-02',
        source_name: 'celestrak-gp',
        batch_id: 'batch-a',
        timestamp: '2026-05-10T12:00:01Z',
        size_bytes: OMM_B.byteLength,
      },
      {
        schema_name: 'OMM.fbs',
        cid: 'cid-omm-c',
        peer_id: 'peer-provider',
        provider_id: 'demo-provider',
        source_name: 'spacetrack-gp',
        batch_id: 'batch-b',
        timestamp: '2026-05-10T12:00:02Z',
        size_bytes: OMM_C.byteLength,
      },
    ],
  };
  return decodeFlatSqlSyncChunk(encodeSyncResponseBytes(header, [OMM_A, OMM_B, OMM_C]));
}

describe('datasync-fed engine store (loop D.4): flatsql-sync chunks', () => {
  it('materializes sync records into their TRUE per-provider source partitions with envelope index rows', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    const chunk = mixedProvenanceChunk();

    const result = await store.ingestSyncChunk(chunk);
    expect(result).toMatchObject({
      standardId: 'OMM',
      totalRecords: 3,
      ingestedRecords: 3,
      indexedEnvelopes: 3,
      replayed: false,
    });
    expect(result.sources.sort()).toEqual(['celestrak-gp', 'spacetrack-gp']);

    // Server layout: registerSource per provider — `_source` carries the
    // FULL shadow-table name; nothing lands in the `local` partition.
    const partitions = await store.standardsStore!.query(
      'SELECT OBJECT_NAME, _source FROM OMM ORDER BY OBJECT_NAME LIMIT 10',
      'OMM',
    );
    expect(partitions.records).toEqual([
      { OBJECT_NAME: 'STARLINK-6292', _source: 'OMM@celestrak-gp' },
      { OBJECT_NAME: 'STARLINK-6293', _source: 'OMM@celestrak-gp' },
      { OBJECT_NAME: 'STARLINK-6294', _source: 'OMM@spacetrack-gp' },
    ]);
    expect(store.standardsStore!.listSources!('OMM')).toEqual(['celestrak-gp', 'spacetrack-gp']);

    // Envelope index rows mirror sdn_record_index: cid/peer/source_timestamp.
    const envelopes = store.sql(
      "SELECT cid, peer_id, source_timestamp FROM sdn_record_index WHERE schema_name = 'OMM.fbs' ORDER BY cid",
    );
    expect(envelopes.rows).toEqual([
      ['cid-omm-a', 'peer-provider', Date.parse('2026-05-10T12:00:00Z')],
      ['cid-omm-b', 'peer-provider', Date.parse('2026-05-10T12:00:01Z')],
      ['cid-omm-c', 'peer-provider', Date.parse('2026-05-10T12:00:02Z')],
    ]);
    await store.close();
  });

  it('is idempotent under sync re-delivery (record-key ledger holds)', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    await store.ingestSyncChunk(mixedProvenanceChunk());

    const replay = await store.ingestSyncChunk(mixedProvenanceChunk());
    expect(replay).toMatchObject({
      totalRecords: 3,
      ingestedRecords: 0,
      indexedEnvelopes: 0,
      replayed: true,
    });
    const rows = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
    expect(rows.records).toEqual([{ n: 3 }]);
    expect(store.sql("SELECT cid FROM sdn_record_index WHERE schema_name = 'OMM.fbs'").rowCount).toBe(3);
    await store.close();
  });

  it('falls back to query-level provenance for untagged refs, and to `local` only as a last resort', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    const untagged = (cid: string) => ({
      schema_name: 'OMM.fbs',
      cid,
      peer_id: 'peer-x',
      size_bytes: OMM_A.byteLength,
    });
    const chunkFor = (cid: string) => decodeFlatSqlSyncChunk(encodeSyncResponseBytes({
      schema: 'OMM.fbs',
      count: 1,
      sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
      results: [untagged(cid)],
    }, [OMM_A]));

    const viaQueryProvenance = await store.ingestSyncChunk(chunkFor('cid-1'), {
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
    });
    expect(viaQueryProvenance.sources).toEqual(['celestrak-gp']);

    const viaProviderFallback = await store.ingestSyncChunk(chunkFor('cid-2'), {
      providerId: 'space-data-network-02',
    });
    expect(viaProviderFallback.sources).toEqual(['space-data-network-02']);

    const lastResort = await store.ingestSyncChunk(chunkFor('cid-3'));
    expect(lastResort.sources).toEqual(['local']);
    await store.close();
  });

  it('persists the datasync-fed state: partitions, envelope rows, and dedupe survive reload', async () => {
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    const options = {
      schemas: [OMM_STANDARD],
      persistenceKey: 'datasync-durability',
      persistenceStore,
    };
    const first = await FlatSQLEngineRecordStore.open(options);
    await first.ingestSyncChunk(mixedProvenanceChunk());
    await first.close();

    const second = await FlatSQLEngineRecordStore.open(options);
    const partitions = await second.standardsStore!.query(
      'SELECT _source, COUNT(*) AS n FROM OMM GROUP BY _source ORDER BY _source',
      'OMM',
    );
    expect(partitions.records).toEqual([
      { _source: 'OMM@celestrak-gp', n: 2 },
      { _source: 'OMM@spacetrack-gp', n: 1 },
    ]);
    // Envelope index rows restored from the compact durable sync index.
    expect(second.sql("SELECT cid FROM sdn_record_index WHERE schema_name = 'OMM.fbs'").rowCount).toBe(3);
    // Replay after reload stays a no-op (persisted record keys).
    const replay = await second.ingestSyncChunk(mixedProvenanceChunk());
    expect(replay.ingestedRecords).toBe(0);
    expect(replay.indexedEnvelopes).toBe(0);
    await second.close();
  });
});

describe('datasync-fed engine store (loop D.4): published shards', () => {
  async function publishedShardBytes(): Promise<{ bytes: Uint8Array; streamSha: string; frames: Uint8Array[] }> {
    const frames = [OMM_A, OMM_B];
    const streamLength = frames.reduce((sum, frame) => sum + 4 + frame.byteLength, 0);
    const stream = new Uint8Array(streamLength);
    const view = new DataView(stream.buffer);
    let offset = 0;
    for (const frame of frames) {
      view.setUint32(offset, frame.byteLength, true);
      stream.set(frame, offset + 4);
      offset += 4 + frame.byteLength;
    }
    const streamSha = await sha256Hex(stream);
    const header = {
      op: 'read_published_shard',
      status: 'ok',
      schema: 'OMM.fbs',
      provider_id: 'space-data-network-02',
      source_name: 'celestrak-gp',
      batch_id: 'batch-a',
      query_profile: 'dataset-publication-offset-v1',
      cid: 'bafkteststarlinkshard',
      row_count: 2,
      byte_count: stream.byteLength,
      shard_sha256: streamSha,
      head: 'feed-head-1',
      sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
      transports: ['wss'],
      payload_format: 'flatsql-size-prefixed-flatbuffers',
    };
    const json = textEncoder.encode(JSON.stringify(header));
    const bytes = new Uint8Array(4 + json.byteLength + stream.byteLength);
    new DataView(bytes.buffer).setUint32(0, json.byteLength, false);
    bytes.set(json, 4);
    bytes.set(stream, 4 + json.byteLength);
    return { bytes, streamSha, frames };
  }

  it('materializes a published shard with pin-ledger provenance intact', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    const { bytes, streamSha, frames } = await publishedShardBytes();
    const shard = decodeFlatSqlPublishedShard(bytes);

    const result = await store.ingestPublishedShard(shard, {
      providerPeerId: '12D3KooWProviderPeer',
      providerPublicKey: 'provider-public-key',
    });
    expect(result).toMatchObject({
      standardId: 'OMM',
      totalRecords: 2,
      ingestedRecords: 2,
      indexedEnvelopes: 2,
      sources: ['celestrak-gp'],
      replayed: false,
    });

    // Records live in the provider partition.
    const rows = await store.standardsStore!.query(
      "SELECT COUNT(*) AS n FROM \"OMM@celestrak-gp\"",
      'OMM',
    );
    expect(rows.records).toEqual([{ n: 2 }]);

    // Pin-ledger provenance: provider/source/batch, byte hash, feed head.
    const ledger = await store.listPinLedgerEntries({ cid: 'bafkteststarlinkshard' });
    expect(ledger).toHaveLength(1);
    expect(ledger[0]).toMatchObject({
      standardId: 'OMM',
      schemaName: 'OMM.fbs',
      providerPeerId: '12D3KooWProviderPeer',
      providerPublicKey: 'provider-public-key',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'batch-a',
      queryProfile: 'dataset-publication-offset-v1',
      head: 'feed-head-1',
      byteHash: streamSha,
      role: 'shard',
      rowCount: 2,
      byteCount: shard.streamBytes.byteLength,
      verificationState: 'verified',
    });
    // Provenance queries by provider/source/batch still work.
    expect(await store.listPinLedgerEntries({
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'batch-a',
      role: 'shard',
      verificationState: 'verified',
    })).toHaveLength(1);

    // Envelope index rows: cid = sha256 of the frame — the server computeCID.
    const expectedCids = await Promise.all(frames.map((frame) => sha256Hex(frame)));
    const envelopes = store.sql("SELECT cid FROM sdn_record_index WHERE schema_name = 'OMM.fbs' ORDER BY cid");
    expect(envelopes.rows.map((row) => row[0])).toEqual([...expectedCids].sort());
    await store.close();
  });

  it('makes shard re-delivery a no-op (pin ledger + record-key ledger)', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    const { bytes } = await publishedShardBytes();
    await store.ingestPublishedShard(decodeFlatSqlPublishedShard(bytes));

    const replay = await store.ingestPublishedShard(decodeFlatSqlPublishedShard(bytes));
    expect(replay).toMatchObject({ ingestedRecords: 0, indexedEnvelopes: 0, replayed: true });
    const rows = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
    expect(rows.records).toEqual([{ n: 2 }]);
    expect(await store.listPinLedgerEntries({ role: 'shard' })).toHaveLength(1);
    await store.close();
  });

  it('rejects shards whose bytes do not match the declared SHA-256 or row count', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    const { bytes } = await publishedShardBytes();
    const shard = decodeFlatSqlPublishedShard(bytes);

    const corrupted = {
      header: shard.header,
      streamBytes: withReplacedAscii(shard.streamBytes, 'STARLINK-6292', 'STARLINK-9999'),
    };
    await expect(store.ingestPublishedShard(corrupted)).rejects.toThrow(/SHA-256 mismatch/);

    const wrongRows = {
      header: { ...shard.header, shardSha256: undefined, rowCount: 5 },
      streamBytes: shard.streamBytes,
    };
    await expect(store.ingestPublishedShard(wrongRows)).rejects.toThrow(/2\/5 record frames/);

    // Nothing was materialized by the failed attempts.
    const rows = await store.standardsStore!.query('SELECT COUNT(*) AS n FROM OMM', 'OMM');
    expect(rows.records).toEqual([{ n: 0 }]);
    await store.close();
  });
});
