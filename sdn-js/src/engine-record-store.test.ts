/**
 * Loop D.1 — ONE store: the FlatSQL-WASM engine store is THE SDNNode store.
 *
 * Covers: the promoted store's NodeRecordStorage surface, per-provider
 * source registration + unified views mirroring the sdn-server layout
 * (shadow tables `OMM@<source>`, `_source` = FULL shadow-table name),
 * ModuleHostRecordStore conformance (module hosting drops in unchanged),
 * and reload durability (envelope journal + per-source engine streams).
 */
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { describe, expect, it } from 'vitest';

import {
  FlatSQLEngineRecordStore,
  MemorySnapshotPersistence,
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
  schema: OMM_SCHEMA,
};

const enc = (text: string) => new TextEncoder().encode(text);

describe('FlatSQLEngineRecordStore (THE SDNNode store)', () => {
  it('serves the NodeRecordStorage surface from the engine control table', async () => {
    const store = await FlatSQLEngineRecordStore.open();
    const cid = await store.store('OMM.fbs', enc('record-1'), 'peer-a', enc('sig'));
    expect(cid).toMatch(/^[0-9a-f]{64}$/);
    // content-addressed dedupe
    expect(await store.store('OMM.fbs', enc('record-1'), 'peer-a', enc('sig'))).toBe(cid);
    expect(await store.count()).toBe(1);

    await store.store('OMM.fbs', enc('record-2'), 'peer-b', enc('sig'));
    await store.store('CDM.fbs', enc('record-3'), 'peer-a', enc('sig'));
    expect(await store.count('OMM.fbs')).toBe(2);
    expect((await store.query('OMM.fbs')).length).toBe(2);
    expect((await store.query('OMM.fbs', { peerId: 'peer-b' })).length).toBe(1);
    expect((await store.query('OMM.fbs', { limit: 1 })).length).toBe(1);

    // The row index is a plain SQL control table inside the engine, named
    // after the server layout (sdn_record_index — flatsql.go).
    const provenance = store.sql(
      "SELECT schema_name, peer_id FROM sdn_record_index WHERE schema_name = 'OMM.fbs' ORDER BY rowid",
    );
    expect(provenance.rowCount).toBe(2);
    expect(provenance.columns).toEqual(['schema_name', 'peer_id']);

    await store.delete(cid);
    expect(await store.get('OMM.fbs', cid)).toBeNull();
    expect(await store.count('OMM.fbs')).toBe(1);
    await store.close();
  });

  it('mirrors SDS FlatBuffer records into the per-standard engine database under the server default source', async () => {
    const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    await store.store('OMM.fbs', new Uint8Array(STARLINK_6292_OMM_BYTES), 'peer-a', new Uint8Array(0));
    // Non-FlatBuffer payloads must not reach the record vtab.
    await store.store('OMM.fbs', enc('{"json":"payload"}'), 'peer-a', new Uint8Array(0));

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
    expect(await store.count('OMM.fbs')).toBe(2); // both envelopes indexed
    await store.close();
  });

  it('satisfies the ModuleHostRecordStore contract so module hosting drops in unchanged', async () => {
    const store = await FlatSQLEngineRecordStore.open();
    const adapters = createModuleHostCapabilityAdapters({
      storage: store,
      peerId: 'module-host-peer',
    });
    expect(Object.keys(adapters)).toEqual(['storage']);

    // storage.write {schema, data:base64} -> {cid}
    const writeResult = (await adapters.storage!.write({
      schema: 'OMM',
      data: Buffer.from('module-record').toString('base64'),
    })) as { cid: string };
    expect(writeResult.cid).toMatch(/^[0-9a-f]{64}$/);

    // storage.query {schema, limit} -> records
    const queried = (await adapters.storage!.query({ schema: 'OMM', limit: 5 })) as Array<{
      cid: string;
      peerId: string;
    }>;
    expect(queried.length).toBe(1);
    expect(queried[0].cid).toBe(writeResult.cid);
    expect(queried[0].peerId).toBe('module-host-peer');

    // storage.delete {cid} -> true
    expect(await adapters.storage!.delete({ cid: writeResult.cid })).toBe(true);
    expect(((await adapters.storage!.query({ schema: 'OMM' })) as unknown[]).length).toBe(0);
    await store.close();
  });

  it('survives reload: envelope journal + per-source engine streams rebuild the same store', async () => {
    const persistence = new MemorySnapshotPersistence();
    const persistenceStore = new MemoryFlatSqlPersistenceStore();

    const first = await FlatSQLEngineRecordStore.open({
      persistence,
      persistenceStore,
      persistenceKey: 'reload-durability',
      schemas: [OMM_STANDARD],
    });
    const cid1 = await first.store('OMM.fbs', new Uint8Array(STARLINK_6292_OMM_BYTES), 'peer-a', enc('sig-a'));
    const cid2 = await first.store('CDM.fbs', enc('beta'), 'peer-b', enc('sig-b'));
    await first.close();

    const second = await FlatSQLEngineRecordStore.open({
      persistence,
      persistenceStore,
      persistenceKey: 'reload-durability',
      schemas: [OMM_STANDARD],
    });
    expect(await second.count()).toBe(2);
    const alpha = await second.get('OMM.fbs', cid1);
    expect([...(alpha?.data ?? [])]).toEqual([...STARLINK_6292_OMM_BYTES]);
    expect(alpha?.peerId).toBe('peer-a');
    expect((await second.get('CDM.fbs', cid2))?.peerId).toBe('peer-b');
    // Control-table index rebuilt from the journal.
    expect(second.sql('SELECT cid FROM sdn_record_index').rowCount).toBe(2);
    // SDS record vtab rebuilt into the same source partition — exactly one
    // copy (ingested-keys ledger makes journal replay idempotent).
    const rows = await second.standardsStore!.query('SELECT NORAD_CAT_ID, _source FROM OMM LIMIT 10', 'OMM');
    expect(rows.records).toEqual([{ NORAD_CAT_ID: 56775, _source: 'OMM@local' }]);
    await second.close();
  });

  it('detects SDS FlatBuffer payloads by file identifier (bare and size-prefixed)', () => {
    const sizePrefixed = new Uint8Array(STARLINK_6292_OMM_BYTES);
    const bare = stripSdnFlatBufferSizePrefix(sizePrefixed);
    expect(flatBufferMatchesFileId(sizePrefixed, '$OMM')).toBe(true);
    expect(flatBufferMatchesFileId(bare, '$OMM')).toBe(true);
    expect(flatBufferMatchesFileId(sizePrefixed, '$CAT')).toBe(false);
    expect(flatBufferMatchesFileId(enc('not a flatbuffer'), '$OMM')).toBe(false);
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
      const stream = store.queryEpochRawStream('OMM', {
        profile: 'nearest',
        epoch: BENCH_EPOCH_BASE + 2 * 86400,
        limit: -1,
        source: 'celestrak-gp',
      });
      let frames = 0;
      for (const frame of store.queryEpochFrames('OMM', {
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

    expect(store.listSources!('OMM')).toEqual(['celestrak-gp', 'spacetrack-gp']);

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

    expect(store.getStats()[0]).toEqual(expect.objectContaining({
      standardId: 'OMM',
      recordCount: 2,
    }));
    store.destroy();
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
    first.destroy();

    const second = await createLocalFlatSqlStore(options);
    expect(second.listSources!('OMM')).toEqual(['celestrak-gp', LOCAL_FLATSQL_DEFAULT_SOURCE]);
    const unified = await second.query('SELECT _source FROM OMM ORDER BY _source LIMIT 10', 'OMM');
    expect(unified.records).toEqual([
      { _source: 'OMM@celestrak-gp' },
      { _source: 'OMM@local' },
    ]);
    // Re-ingesting the same records after reload stays deduped.
    expect(await second.ingestRecords('OMM', [record], { source: 'celestrak-gp' })).toBe(0);
    second.destroy();
  });

  it('migrates a legacy pre-D.1 exportData snapshot into the local source partition', async () => {
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    // Legacy layout: one exportData blob (a size-prefixed record stream)
    // under `<key>:<STD>` with no source attribution.
    const bare = stripSdnFlatBufferSizePrefix(new Uint8Array(STARLINK_6292_OMM_BYTES));
    const legacyBlob = new Uint8Array(4 + bare.byteLength);
    new DataView(legacyBlob.buffer).setUint32(0, bare.byteLength, true);
    legacyBlob.set(bare, 4);
    await persistenceStore.writeBytes('legacy-migration:OMM', legacyBlob);

    const store = await createLocalFlatSqlStore({
      schemas: [OMM_STANDARD],
      persistenceKey: 'legacy-migration',
      persistenceStore,
    });
    const rows = await store.query('SELECT NORAD_CAT_ID, _source FROM OMM LIMIT 10', 'OMM');
    expect(rows.records).toEqual([{ NORAD_CAT_ID: 56775, _source: 'OMM@local' }]);

    // The legacy blob is folded into the ENGINE'S OWN disk state and deleted.
    // Nothing rewrites the retired snapshot-export layout in its place: the
    // durable representation is now the index + arena the engine writes.
    await store.flush('OMM');
    expect(await persistenceStore.readBytes('legacy-migration:OMM')).toBeNull();
    expect(await persistenceStore.readJson('legacy-migration:OMM:sources')).toBeNull();
    expect(await persistenceStore.readBytes('legacy-migration:OMM:src:local')).toBeNull();
    store.destroy();

    const reopened = await createLocalFlatSqlStore({
      schemas: [OMM_STANDARD],
      persistenceKey: 'legacy-migration',
      persistenceStore,
    });
    const reloaded = await reopened.query('SELECT NORAD_CAT_ID, _source FROM OMM LIMIT 10', 'OMM');
    expect(reloaded.records).toEqual([{ NORAD_CAT_ID: 56775, _source: 'OMM@local' }]);
    reopened.destroy();
  });
});
