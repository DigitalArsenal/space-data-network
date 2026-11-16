import { describe, expect, it } from 'vitest';
import { compileTestSchema, serverStandardSchemas } from './test-schema-helper';
import { createLocalFlatSqlStore } from './ui/runtime/local-flatsql';

const responses = serverStandardSchemas();
describe('server standard schemas in the dashboard engine', () => {
  it('includes the required root and dependency edge cases', () => {
    expect(responses.map(item => item.code)).toEqual(expect.arrayContaining(['OMM', 'CAT', 'OCM', 'CDM', 'EPM', 'RFM', 'PGR']));
  });
  it.each(responses)('$code compiles to a binary schema', async ({ text }) => {
    expect((await compileTestSchema(text)).byteLength).toBeGreaterThan(0);
  });
  const required = ['OMM', 'CAT', 'OCM', 'CDM', 'EPM', 'RFM', 'PGR'];
  it.each(responses.filter(item => required.includes(item.code)))('$code registers and exposes the served root', async ({ code, text, table, fileId }) => {
    expect(text).toContain(`root_type ${table};`);
    const store = await createLocalFlatSqlStore({ schemas: [{ standardId: code, schema: text, tableName: table, fileId }] });
    try {
      const result = await store.query(`SELECT * FROM ${table} LIMIT 0`, code);
      expect(result.records).toEqual([]);
      expect(result.columns.length).toBeGreaterThan(0);
    } finally { await store.destroy(); }
  });
});
