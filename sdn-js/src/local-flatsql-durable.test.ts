import { createHash } from 'node:crypto';
import { mkdtempSync, readdirSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, test } from 'vitest';
import { clearLocalFlatSqlStore, createLocalFlatSqlStore, MemoryFlatSqlPersistenceStore } from './local-flatsql';
import { publishedSchema } from './test-schema-helper';
import { buildEpochStoreCorpus } from '../scripts/generate-flatsql-parity-vectors.mjs';

const schema = { standardId: 'OMM', tableName: 'OMM', fileId: '$OMM', ...publishedSchema('OMM') };
const digest = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex');
const sources = () => buildEpochStoreCorpus().sources.map((source: { name: string; streamBase64: string; recordCount: number }) => ({
  name: source.name, bytes: new Uint8Array(Buffer.from(source.streamBase64, 'base64')), records: source.recordCount,
}));
function filesystemBytes(root: string): number {
  return readdirSync(root).reduce((sum, name) => {
    const file = join(root, name), stat = statSync(file);
    return sum + (stat.isDirectory() ? filesystemBytes(file) : stat.size);
  }, 0);
}

describe('binary engine filesystem durability', () => {
  test('the product store persists byte-identical records without a snapshot backend', async () => {
    const root = mkdtempSync(join(tmpdir(), 'binary-durable-'));
    const persistenceStore = new MemoryFlatSqlPersistenceStore();
    const options = { schemas: [schema], engine: { root }, persistenceKey: 'disk-records', persistenceStore };
    const source = sources()[0];
    try {
      const first = await createLocalFlatSqlStore(options);
      expect(await first.ingestFlatBufferStream('OMM', source.bytes)).toBe(source.records);
      expect((await first.query('SELECT count(*) AS n FROM OMM', 'OMM')).rows[0][0]).toBe(source.records);
      expect(digest(await first.queryRawFlatBufferStream!('OMM', 'SELECT _data FROM OMM ORDER BY _rowid'))).toBe(digest(source.bytes));
      await first.destroy();
      expect(filesystemBytes(root)).toBeGreaterThan(source.bytes.length);
      // Reopening has no snapshot backend to hydrate: only filesystem state.
      const second = await createLocalFlatSqlStore({ ...options, persistenceStore: new MemoryFlatSqlPersistenceStore() });
      expect((await second.getStats())[0].recordCount).toBe(source.records);
      expect(digest(await second.queryRawFlatBufferStream!('OMM', 'SELECT _data FROM OMM ORDER BY _rowid'))).toBe(digest(source.bytes));
      await second.destroy();
    } finally { rmSync(root, { recursive: true, force: true }); }
  }, 120_000);

  test('per-source partitions survive teardown and reopen with no snapshot to re-ingest', async () => {
    const root = mkdtempSync(join(tmpdir(), 'binary-partitions-'));
    const options = { schemas: [schema], engine: { root }, persistenceKey: 'disk-partitions' };
    const corpus = sources();
    expect(corpus.map(source => source.records)).toEqual([60, 20, 5]);
    try {
      const first = await createLocalFlatSqlStore(options);
      for (const source of corpus) {
        expect(await first.ingestFlatBufferStream('OMM', source.bytes, { source: source.name })).toBe(source.records);
        expect(digest(await first.queryRawFlatBufferStream!('OMM', `SELECT _data FROM "OMM@${source.name}" ORDER BY _rowid`))).toBe(digest(source.bytes));
      }
      const unified = digest(await first.queryRawFlatBufferStream!('OMM', 'SELECT _data FROM OMM ORDER BY _rowid'));
      await first.destroy();
      expect(filesystemBytes(root)).toBeGreaterThan(corpus.reduce((sum, source) => sum + source.bytes.length, 0));
      const reopened = await createLocalFlatSqlStore(options);
      expect((await reopened.listSources!('OMM')).sort()).toEqual(corpus.map(source => source.name).sort());
      for (const source of corpus) {
        expect((await reopened.query(`SELECT count(*) FROM "OMM@${source.name}"`, 'OMM')).rows[0][0]).toBe(source.records);
        expect(digest(await reopened.queryRawFlatBufferStream!('OMM', `SELECT _data FROM "OMM@${source.name}" ORDER BY _rowid`))).toBe(digest(source.bytes));
      }
      expect((await reopened.query('SELECT count(*) FROM OMM', 'OMM')).rows[0][0]).toBe(85);
      expect(digest(await reopened.queryRawFlatBufferStream!('OMM', 'SELECT _data FROM OMM ORDER BY _rowid'))).toBe(unified);
      expect((await reopened.query("SELECT count(*) FROM OMM WHERE _source = 'OMM@celestrak-gp'", 'OMM')).rows[0][0]).toBe(60);
      await reopened.destroy();
      await clearLocalFlatSqlStore({ ...options, standardIds: ['OMM'] });
      const cleared = await createLocalFlatSqlStore(options);
      expect(await cleared.listSources!('OMM')).toEqual([]);
      expect((await cleared.getStats())[0].recordCount).toBe(0);
      await cleared.destroy();
    } finally { rmSync(root, { recursive: true, force: true }); }
  }, 120_000);
});
