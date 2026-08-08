/**
 * THE PRODUCTION LANE IS DISK-BACKED.
 *
 * `flatsql-io-store.test.ts` proves the shim works. This proves the PRODUCT
 * uses it: the engine every sdn-js node runs — the one `createLocalFlatSqlStore`
 * initialises, not one a test built for itself — reaches durable files through
 * the seven `flatsql_io_*` imports, backed by the node's own persistence store.
 *
 * Before this wiring `initFlatSQL` was called with no `io:` at all, so the
 * browser lane had NO filesystem: a database opened at a path was silently
 * ephemeral (FLATSQL_STATE_NO_FILESYSTEM) while the Go host reached real files.
 * That is the divergence the disk-backed law forbids, and the assertion below
 * is the one that fails if it ever returns.
 */

import { createHash } from 'node:crypto';
import { describe, expect, test } from 'vitest';

// @ts-expect-error — plain .mjs helper shared with the parity generator.
import { buildEpochStoreCorpus } from '../scripts/generate-flatsql-parity-vectors.mjs';

import {
  MemoryFlatSqlPersistenceStore,
  clearLocalFlatSqlStore,
  createLocalFlatSqlStore,
  flatSqlDatabasePathForKey,
  flatSqlIoPrefixForKey,
  getSharedFlatSql,
  getSharedFlatSqlIoRouter,
} from './local-flatsql';
import { hydrateFlatSqlDatabase, restoreFlatSqlState } from './flatsql-io-store';

const OMM_SCHEMA = `
  table OMM {
    OBJECT_NAME:string;
    OBJECT_ID:string;
    EPOCH:string;
    NORAD_CAT_ID:uint32;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    USER_DEFINED_EPOCH_TIMESTAMP:double;
  }
  root_type OMM;
  file_identifier "$OMM";
`;

const digest = (bytes: Uint8Array) =>
  createHash('sha256').update(Buffer.from(bytes)).digest('hex');

function corpusStream(): { bytes: Uint8Array; records: number } {
  const corpus = buildEpochStoreCorpus();
  return {
    bytes: new Uint8Array(Buffer.from(corpus.sources[0].streamBase64, 'base64')),
    records: corpus.sources[0].recordCount as number,
  };
}

interface CorpusSource { name: string; bytes: Uint8Array; records: number }

/**
 * The parity corpus, per SOURCE: celestrak-gp = 60 frames, provider-two = 20,
 * local = 5 — the exact partitioning the engine had to learn to restore
 * (flatsql `_flatsql_sources` / `_flatsql_source_ranges`, 1.4.5).
 */
function corpusSources(): CorpusSource[] {
  return (buildEpochStoreCorpus().sources as Array<{ name: string; streamBase64: string; recordCount: number }>)
    .map((source) => ({
      name: source.name,
      bytes: new Uint8Array(Buffer.from(source.streamBase64, 'base64')),
      records: source.recordCount,
    }));
}

const OMM_STANDARD = {
  standardId: 'OMM',
  tableName: 'OMM',
  fileId: '$OMM',
  schema: OMM_SCHEMA,
} as const;

function storeEntries(store: MemoryFlatSqlPersistenceStore): Map<string, unknown> {
  return (store as unknown as { entries: Map<string, unknown> }).entries;
}

/**
 * Rebuild a durable file from the page groups actually sitting in the
 * persistence store — deliberately NOT through the backend, whose in-process
 * chunk cache would answer from RAM and prove nothing about what a reload
 * would find.
 */
function reassemblePersistedFile(store: MemoryFlatSqlPersistenceStore, persistenceKey: string, path: string): Uint8Array {
  const entries = storeEntries(store);
  const keyPrefix = `${flatSqlIoPrefixForKey(persistenceKey)}keys/${path}`;
  const metaBytes = entries.get(`${keyPrefix}#meta`);
  if (!(metaBytes instanceof Uint8Array)) throw new Error(`no persisted length record for ${path}`);
  const meta = JSON.parse(new TextDecoder().decode(metaBytes)) as { length: number; chunkBytes: number };
  const out = new Uint8Array(meta.length);
  for (let index = 0; index * meta.chunkBytes < meta.length; index++) {
    const chunk = entries.get(`${keyPrefix}#${index}`);
    if (chunk instanceof Uint8Array) out.set(chunk.subarray(0, Math.min(chunk.length, meta.length - index * meta.chunkBytes)), index * meta.chunkBytes);
  }
  return out;
}

describe('production FlatSQL engine I/O wiring', () => {
  test('the store the product creates leaves the shared engine disk-capable', async () => {
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    const persistenceKey = 'durable-prod-lane';

    // The PRODUCT entry point. Nothing test-specific initialises the engine.
    const store = await createLocalFlatSqlStore({
      persistenceKey,
      persistenceStore,
      schemas: [{ standardId: 'OMM', tableName: 'OMM', fileId: '$OMM', schema: OMM_SCHEMA }],
    });
    expect(store).toBeTruthy();

    const flatsql = await getSharedFlatSql();
    const prefix = flatSqlIoPrefixForKey(persistenceKey);
    const router = getSharedFlatSqlIoRouter();
    const dbPath = `${prefix}durability-probe.db`;

    const { bytes, records } = corpusStream();

    // 1. Open at a PATH inside the store's own namespace and ingest real
    //    size-prefixed $OMM frames.
    await hydrateFlatSqlDatabase(router, dbPath);
    let db = flatsql.openDatabase(OMM_SCHEMA, 'sds', dbPath, 2);
    db.registerFileId('$OMM', 'OMM');
    db.query('PRAGMA temp_store=MEMORY');

    // THE ASSERTION. Without `io:` at init this is false and everything below
    // is a lie told by an in-memory arena.
    expect(db.isDiskBacked()).toBe(true);

    db.ingest(bytes);
    const rowsBefore = db.query('SELECT COUNT(*) AS n FROM OMM').rows[0][0];
    expect(rowsBefore).toBe(records);
    const before = digest(db.queryRawFlatBufferStream('SELECT _data FROM OMM ORDER BY _rowid ASC'));
    expect(db.flushIndex()).toBe(0);
    const mark = db.flushedOffset();
    expect(mark).toBe(bytes.length);

    // 2. Tear the engine down; the async store becomes durable HERE.
    db.destroy();
    await router.flush();

    // 3. The bytes are in the NODE'S OWN persistence store — chunked page
    //    groups under this key's prefix, not a snapshot blob.
    const persistedKeys: string[] = [];
    for (const key of (persistenceStore as unknown as { entries: Map<string, unknown> }).entries.keys()) {
      if (key.startsWith(prefix)) persistedKeys.push(key);
    }
    expect(persistedKeys.length).toBeGreaterThan(0);

    // 4. Reopen and get the SAME BYTES back, not merely the same rows.
    await hydrateFlatSqlDatabase(router, dbPath);
    db = flatsql.openDatabase(OMM_SCHEMA, 'sds', dbPath, 2);
    db.registerFileId('$OMM', 'OMM');
    db.query('PRAGMA temp_store=MEMORY');
    const restored = restoreFlatSqlState(db);
    expect(restored.rederived).toBe(false);
    expect(restored.stateCode).toBeGreaterThanOrEqual(0);
    expect(db.flushedOffset()).toBe(mark);
    expect(db.query('SELECT COUNT(*) AS n FROM OMM').rows[0][0]).toBe(records);
    expect(digest(db.queryRawFlatBufferStream('SELECT _data FROM OMM ORDER BY _rowid ASC'))).toBe(before);

    // 5. wire == disk == query, on the production engine: the arena file is the
    //    ingested stream verbatim, and the query hands back exactly those bytes.
    const arena = db.queryRawFlatBufferStream('SELECT _data FROM OMM ORDER BY _rowid ASC');
    expect(digest(arena)).toBe(digest(bytes));

    db.destroy();
    await router.flush();
  }, 120_000);

  /**
   * THE RETIREMENT GATE (flatsql 1.4.5).
   *
   * The store used to persist by SNAPSHOT EXPORT: `SELECT _data FROM
   * "Table@source"` per (standard, source), written whole on every flush and
   * re-ingested frame by frame at boot. It existed only because flatsql <= 1.4.4
   * restored base tables and lost source partitions (measured: 60/20 -> 0/0).
   *
   * 1.4.5 keeps `_flatsql_sources` / `_flatsql_source_ranges` in the index file,
   * so `openState` restores the partitions itself. This asserts the consequence
   * the product must show: after teardown, the ONLY bytes in the persistence
   * store are the engine's own page groups — there is literally nothing left to
   * re-ingest from — and a reopen still returns every partition, byte for byte.
   */
  test('per-source partitions survive teardown and reopen with no snapshot to re-ingest', async () => {
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    const persistenceKey = 'durable-source-partitions';
    const sources = corpusSources();
    expect(sources.map((source) => source.records)).toEqual([60, 20, 5]);

    const first = await createLocalFlatSqlStore({
      persistenceKey,
      persistenceStore,
      schemas: [{ ...OMM_STANDARD }],
    });
    for (const source of sources) {
      expect(await first.ingestFlatBufferStream('OMM', source.bytes, { source: source.name })).toBe(source.records);
    }
    const before = new Map(sources.map((source) => [
      source.name,
      digest(first.queryRawFlatBufferStream('OMM', `SELECT _data FROM "OMM@${source.name}" ORDER BY _rowid ASC`)),
    ]));
    // wire == disk: each partition hands back exactly the frames ingested into it.
    for (const source of sources) expect(before.get(source.name)).toBe(digest(source.bytes));
    const unifiedBefore = digest(first.queryRawFlatBufferStream('OMM', 'SELECT _data FROM OMM ORDER BY _rowid ASC'));
    await first.flush('OMM');
    first.destroy();

    // THE RETIRED LAYOUT IS GONE. No source list, no per-source stream blob —
    // for any source, including the default `local` partition.
    const keys = [...storeEntries(persistenceStore).keys()];
    expect(keys.filter((key) => key.startsWith(`${persistenceKey}:OMM:src:`))).toEqual([]);
    expect(keys).not.toContain(`${persistenceKey}:OMM:sources`);
    expect(keys).not.toContain(`${persistenceKey}:OMM`);

    // What IS there is the engine's own index + arena, and the arena is the
    // ingested wire stream verbatim — read back from the STORE, not the cache.
    const dbPath = flatSqlDatabasePathForKey(persistenceKey, 'OMM');
    const arenaOnDisk = reassemblePersistedFile(persistenceStore, persistenceKey, `${dbPath}.fsdata`);
    expect(digest(arenaOnDisk)).toBe(digest(Buffer.concat(sources.map((source) => Buffer.from(source.bytes)))));
    expect(reassemblePersistedFile(persistenceStore, persistenceKey, dbPath).byteLength).toBeGreaterThan(0);

    // Reopen: nothing is re-registered and nothing is re-ingested, because the
    // only durable bytes are the engine's. Partitions and the unified view come
    // back from openState alone.
    const reopened = await createLocalFlatSqlStore({
      persistenceKey,
      persistenceStore,
      schemas: [{ ...OMM_STANDARD }],
    });
    expect(reopened.listSources('OMM').sort()).toEqual(sources.map((source) => source.name).sort());
    for (const source of sources) {
      expect(reopened.query(`SELECT COUNT(*) AS n FROM "OMM@${source.name}"`, 'OMM').rows[0][0]).toBe(source.records);
      expect(digest(reopened.queryRawFlatBufferStream('OMM', `SELECT _data FROM "OMM@${source.name}" ORDER BY _rowid ASC`)))
        .toBe(before.get(source.name));
    }
    expect(reopened.query('SELECT COUNT(*) AS n FROM OMM', 'OMM').rows[0][0]).toBe(85);
    expect(digest(reopened.queryRawFlatBufferStream('OMM', 'SELECT _data FROM OMM ORDER BY _rowid ASC'))).toBe(unifiedBefore);
    // `_source` still carries the FULL shadow-table name, as the server does.
    expect(reopened.query("SELECT COUNT(*) AS n FROM OMM WHERE _source = 'OMM@celestrak-gp'", 'OMM').rows[0][0]).toBe(60);
    reopened.destroy();

    // Clearing a disk-backed standard deletes the engine's FILES, so the next
    // open finds nothing — a clear that only dropped side JSON would "restore".
    await clearLocalFlatSqlStore({ persistenceKey, standardIds: ['OMM'], persistenceStore });
    const cleared = await createLocalFlatSqlStore({
      persistenceKey,
      persistenceStore,
      schemas: [{ ...OMM_STANDARD }],
    });
    expect(cleared.listSources('OMM')).toEqual([]);
    expect(cleared.getStats()[0].recordCount).toBe(0);
    cleared.destroy();
  }, 120_000);

  /**
   * Upgrade path. A node that ran the old build has per-source snapshot streams
   * and no engine state. Boot must fold them into the durable database ONCE,
   * delete them, and never write them again — a node that upgrades must not
   * lose its catalog, and must not keep paying for the retired layout.
   */
  test('migrates a pre-1.4.5 snapshot-export store into disk state exactly once', async () => {
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    const persistenceKey = 'legacy-snapshot-upgrade';
    const sources = corpusSources();

    // Seed the RETIRED layout by hand: the source list plus one aligned stream
    // per source, exactly what the old persistStandard wrote.
    await persistenceStore.writeJson(`${persistenceKey}:OMM:sources`, sources.map((source) => source.name).sort());
    for (const source of sources) {
      await persistenceStore.writeBytes(`${persistenceKey}:OMM:src:${source.name}`, source.bytes);
    }

    const upgraded = await createLocalFlatSqlStore({
      persistenceKey,
      persistenceStore,
      schemas: [{ ...OMM_STANDARD }],
    });
    expect(upgraded.listSources('OMM').sort()).toEqual(sources.map((source) => source.name).sort());
    expect(upgraded.query('SELECT COUNT(*) AS n FROM OMM', 'OMM').rows[0][0]).toBe(85);
    upgraded.destroy();

    // The legacy keys are gone, and the records are in the engine's files.
    const keys = [...storeEntries(persistenceStore).keys()];
    expect(keys.filter((key) => key.startsWith(`${persistenceKey}:OMM:src:`))).toEqual([]);
    expect(keys).not.toContain(`${persistenceKey}:OMM:sources`);

    // Reopen with NOTHING legacy left: still 85 records, still partitioned.
    const afterMigration = await createLocalFlatSqlStore({
      persistenceKey,
      persistenceStore,
      schemas: [{ ...OMM_STANDARD }],
    });
    expect(afterMigration.query('SELECT COUNT(*) AS n FROM OMM', 'OMM').rows[0][0]).toBe(85);
    for (const source of sources) {
      expect(afterMigration.query(`SELECT COUNT(*) AS n FROM "OMM@${source.name}"`, 'OMM').rows[0][0]).toBe(source.records);
    }
    afterMigration.destroy();
  }, 120_000);
});
