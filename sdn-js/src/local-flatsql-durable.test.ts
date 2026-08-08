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
  createLocalFlatSqlStore,
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
});
