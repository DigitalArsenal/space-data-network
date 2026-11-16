import { CID } from 'multiformats/cid';
import { sha256 } from 'multiformats/hashes/sha2';
import { publishedSchema, recordTestSchema, recordTestBytes } from './test-schema-helper';
/**
 * Binary record-store contracts: CRUD, provenance, module-host capabilities,
 * filesystem reload, source partitions, and catalog-scale raw queries.
 */
import { readFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { describe, expect, it } from 'vitest';

import {
  FlatSQLEngineRecordStore,
  flatBufferMatchesFileId,
} from './engine-record-store';
import {
  createLocalFlatSqlStore,
  LOCAL_FLATSQL_DEFAULT_SOURCE,
  MemoryFlatSqlPersistenceStore,
  stripSdnFlatBufferSizePrefix,
} from './local-flatsql';
import { createModuleHostCapabilityAdapters } from './module-host-adapters';
// @ts-expect-error — plain .mjs helper shared with the D.6 benchmark scripts.
import { BENCH_EPOCH_BASE, buildBenchCorpus } from '../scripts/lib/d6-bench-common.mjs';

const require = createRequire(import.meta.url);

const OMM_SCHEMA = readFileSync(require.resolve('spacedatastandards.org/schema/OMM/main.fbs'), 'utf8');
const STARLINK_6292_OMM_BYTES = Buffer.from('HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA', 'base64');

const OMM_STANDARD = {
  standardId: 'OMM',
  tableName: 'OMM',
  fileId: '$OMM',
  ...publishedSchema('OMM'),
};

const enc = (text: string) => new TextEncoder().encode(text);

describe('FlatSQLEngineRecordStore (THE SDNNode store)', () => {
  it('serves NodeRecordStorage CRUD and provenance from registered binary records', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [recordTestSchema(), recordTestSchema('ALT')] });
    const first = await recordTestBytes(1);
    const cid = await store.store('TST.fbs', first, 'peer-a', enc('sig'));
    expect(cid).toBe(CID.createV1(0x55, await sha256.digest(first)).toString());
    expect(await store.store('TST.fbs', first, 'peer-a', enc('sig'))).toBe(cid);
    expect(await store.count()).toBe(1);
    await store.store('TST.fbs', await recordTestBytes(2), 'peer-b', enc('sig'));
    await store.store('ALT.fbs', await recordTestBytes(3, 'ALT'), 'peer-a', enc('sig'));
    expect(await store.count('TST.fbs')).toBe(2);
    expect(await store.query('TST.fbs')).toHaveLength(2);
    expect(await store.query('TST.fbs', { peerId: 'peer-b' })).toHaveLength(1);
    expect(await store.query('TST.fbs', { limit: 1 })).toHaveLength(1);
    const provenance = await store.sql('SELECT VALUE, _peer FROM TST ORDER BY VALUE');
    expect(provenance.rowCount).toBe(2);
    expect(provenance.columns).toEqual(['VALUE', '_peer']);
    expect(provenance.rows).toEqual([[1, 'peer-a'], [2, 'peer-b']]);
    expect((await store.get('TST.fbs', cid))?.data).toEqual(first);
    expect(await store.get('ALT.fbs', cid)).toBeNull();
    await store.delete(cid);
    expect(await store.get('TST.fbs', cid)).toBeNull();
    expect(await store.count('TST.fbs')).toBe(1);
    await store.close();
  });

  it('mirrors SDS FlatBuffer records into the per-standard engine database under the server default source', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    await store.store('OMM.fbs', new Uint8Array(STARLINK_6292_OMM_BYTES), 'peer-a', new Uint8Array(0));
    // Non-FlatBuffer payloads must not reach the record vtab.
    await expect(store.store('OMM.fbs', enc('{"json":"payload"}'), 'peer-a', new Uint8Array(0)))
      .rejects.toThrow(/file identifier/);

    const standards = store.standardsStore!;
    const result = await standards.query(
      'SELECT NORAD_CAT_ID, _source FROM OMM LIMIT 10',
      'OMM',
    );
    expect(result.records).toEqual([{
      NORAD_CAT_ID: 56775,
      // Server semantics: `_source` carries the FULL shadow-table name.
      _source: `OMM@${LOCAL_FLATSQL_DEFAULT_SOURCE}`,
    }]);
    expect(await store.count('OMM.fbs')).toBe(1);
    await store.close();
  });

  it('satisfies the ModuleHostRecordStore contract so module hosting drops in unchanged', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [recordTestSchema()] });
    const adapters = createModuleHostCapabilityAdapters({
      storage: store,
      peerId: 'module-host-peer',
    });
    expect(Object.keys(adapters)).toEqual(['storage']);

    // storage.write {schema, data:base64} -> {cid}
    const writeResult = (await adapters.storage!.write({
      schema: 'TST',
      data: Buffer.from(await recordTestBytes(1)).toString('base64'),
    })) as { cid: string };
    expect(writeResult.cid).toBe(CID.createV1(0x55, await sha256.digest(await recordTestBytes(1))).toString());

    // storage.query {schema, limit} -> records
    const queried = (await adapters.storage!.query({ schema: 'TST', limit: 5 })) as Array<{
      cid: string;
      peerId: string;
    }>;
    expect(queried.length).toBe(1);
    expect(queried[0].cid).toBe(writeResult.cid);
    expect(queried[0].peerId).toBe('module-host-peer');

    // storage.delete {cid} -> true
    expect(await adapters.storage!.delete({ cid: writeResult.cid })).toBe(true);
    expect(((await adapters.storage!.query({ schema: 'TST' })) as unknown[]).length).toBe(0);
    await store.close();
  });

  it('restores binary records, provenance, and deletions from the filesystem', async () => {
    const root = mkdtempSync(join(tmpdir(), 'record-reload-'));
    const options = { engine: { root }, schemas: [recordTestSchema(), recordTestSchema('ALT')] };
    try {
      const first = await FlatSQLEngineRecordStore.open(options);
      const alpha = await recordTestBytes(1);
      const cid1 = await first.store('TST.fbs', alpha, 'peer-a', enc('sig-a'));
      const cid2 = await first.store('ALT.fbs', await recordTestBytes(2, 'ALT'), 'peer-b', enc('sig-b'));
      const removed = await first.store('TST.fbs', await recordTestBytes(3), 'peer-c', enc('sig-c'));
      await first.delete(removed);
      await first.close();
      const second = await FlatSQLEngineRecordStore.open(options);
      expect(await second.count()).toBe(2);
      expect((await second.get('TST.fbs', cid1))?.data).toEqual(alpha);
      expect((await second.get('TST.fbs', cid1))?.peerId).toBe('peer-a');
      expect((await second.get('ALT.fbs', cid2))?.peerId).toBe('peer-b');
      expect((await second.get('ALT.fbs', cid2))?.signature).toEqual(enc('sig-b'));
      expect(await second.get('TST.fbs', removed)).toBeNull();
      expect((await second.sql('SELECT _cid FROM TST')).rowCount).toBe(1);
      expect((await second.sql('SELECT _cid FROM ALT')).rowCount).toBe(1);
      await second.close();
    } finally { rmSync(root, { recursive: true, force: true }); }
  });

  it('detects SDS FlatBuffer payloads by file identifier (bare and size-prefixed)', () => {
    const sizePrefixed = new Uint8Array(STARLINK_6292_OMM_BYTES);
    const bare = stripSdnFlatBufferSizePrefix(sizePrefixed);
    expect(flatBufferMatchesFileId(sizePrefixed, '$OMM')).toBe(true);
    expect(flatBufferMatchesFileId(bare, '$OMM')).toBe(true);
    expect(flatBufferMatchesFileId(sizePrefixed, '$CAT')).toBe(false);
    expect(flatBufferMatchesFileId(enc('not a flatbuffer'), '$OMM')).toBe(false);

    // ...and a multi-frame STREAM is NOT a record, however much its first
    // frame's size prefix makes bytes 8..11 look like one. Full coverage of
    // the rule and the surface that takes a stream:
    // engine-record-store-stream-surface.test.ts.
    const shard = buildBenchCorpus(3).streamBytes as Uint8Array;
    expect(flatBufferMatchesFileId(shard, '$OMM')).toBe(false);
    expect(String.fromCharCode(...shard.subarray(8, 12))).toBe('$OMM');
  });
});

describe('catalog-scale engine queries (loop D.6 regression)', () => {
  it('answers a catalog-scale epoch-nearest raw stream (29K partitions) without temp-store spill failures', async () => {
    // REAL BUG caught by the D.6 REST-stream benchmark: the browser/Node
    // wasm build has no filesystem, and the epoch-nearest window over a
    // catalog-scale partition set spilled a temp b-tree to "disk" →
    // `SQL execution error: disk I/O error`. Fixed by pinning
    // temp_store=MEMORY at database creation (configureEngineDatabaseSession).
    const corpus = buildBenchCorpus(29000);
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    try {
      const ingested = await store.ingestFlatBufferStream('OMM', corpus.streamBytes, {
        source: 'celestrak-gp',
        recordKeyPrefix: 'shard:d6-regression',
        persist: false,
      });
      expect(ingested).toBe(29000);
      const stream = (await store.queryEpochRawStream('OMM', {
        profile: 'nearest',
        epoch: BENCH_EPOCH_BASE + 2 * 86400,
        limit: -1,
        source: 'celestrak-gp',
      }));
      let frames = 0;
      for (const frame of await store.queryEpochFrames('OMM', {
        profile: 'nearest',
        epoch: BENCH_EPOCH_BASE + 2 * 86400,
        limit: -1,
        source: 'celestrak-gp',
      })) {
        if (frame.byteLength === 0) throw new Error('empty frame');
        frames += 1;
      }
      expect(frames).toBe(29000);
      expect(stream.byteLength).toBeGreaterThan(corpus.streamBytes.byteLength * 0.9);
    } finally {
      await store.close();
    }
  }, 120_000);
});

describe('per-provider source partitioning (server layout mirror)', () => {
  it('registers a shadow table per source and reports the full shadow-table name through _source', async () => {
    const store = await createLocalFlatSqlStore({ schemas: [OMM_STANDARD] });
    const record = {
      cid: 'celestrak-omm-1',
      schemaName: 'OMM.fbs',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'batch-1',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: new Uint8Array(STARLINK_6292_OMM_BYTES),
    };
    await store.ingestRecords('OMM', [record], { source: 'celestrak-gp' });
    await store.ingestRecords('OMM', [{ ...record, cid: 'other-provider-omm-1', batchId: 'batch-2' }], { source: 'spacetrack-gp' });

    expect(await store.listSources!('OMM')).toEqual(['celestrak-gp', 'spacetrack-gp']);

    // Unified view unions the per-source shadow tables; `_source` carries
    // the FULL shadow-table name (`OMM@celestrak-gp`), matching the server.
    const unified = await store.query('SELECT NORAD_CAT_ID, _source FROM OMM ORDER BY _source LIMIT 10', 'OMM');
    expect(unified.records).toEqual([
      { NORAD_CAT_ID: 56775, _source: 'OMM@celestrak-gp' },
      { NORAD_CAT_ID: 56775, _source: 'OMM@spacetrack-gp' },
    ]);

    // Per-source shadow tables are directly addressable, like the server's.
    const shadow = await store.query('SELECT NORAD_CAT_ID FROM "OMM@celestrak-gp" LIMIT 10', 'OMM');
    expect(shadow.records).toEqual([{ NORAD_CAT_ID: 56775 }]);

    // Source-filtered unified query — the exact predicate shape the server's
    // engine epoch SQL uses (`_source = ?` with the full shadow name).
    const filtered = await store.query(
      "SELECT NORAD_CAT_ID FROM OMM WHERE _source = 'OMM@spacetrack-gp' LIMIT 10",
      'OMM',
    );
    expect(filtered.records).toEqual([{ NORAD_CAT_ID: 56775 }]);

    expect((await store.getStats())[0]).toEqual(expect.objectContaining({
      standardId: 'OMM',
      recordCount: 2,
    }));
    (await store.destroy());
  });

  it('persists per-(standard, source) aligned streams and restores every partition on reopen', async () => {
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    const options = {
      schemas: [OMM_STANDARD],
      persistenceKey: 'source-durability',
      persistenceStore,
    };
    const record = {
      cid: 'celestrak-omm-1',
      schemaName: 'OMM.fbs',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: new Uint8Array(STARLINK_6292_OMM_BYTES),
    };

    const first = await createLocalFlatSqlStore(options);
    await first.ingestRecords('OMM', [record], { source: 'celestrak-gp' });
    await first.ingestRecords('OMM', [{ ...record, cid: 'local-omm-1' }]); // default `local`
    (await first.destroy());

    const second = await createLocalFlatSqlStore(options);
    expect(await second.listSources!('OMM')).toEqual(['celestrak-gp', LOCAL_FLATSQL_DEFAULT_SOURCE]);
    const unified = await second.query('SELECT _source FROM OMM ORDER BY _source LIMIT 10', 'OMM');
    expect(unified.records).toEqual([
      { _source: 'OMM@celestrak-gp' },
      { _source: 'OMM@local' },
    ]);
    // Re-ingesting the same records after reload stays deduped.
    expect(await second.ingestRecords('OMM', [record], { source: 'celestrak-gp' })).toBe(0);
    (await second.destroy());
  });


});
