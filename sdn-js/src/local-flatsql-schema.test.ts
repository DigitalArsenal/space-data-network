import { describe, expect, it } from 'vitest';
import { createLocalFlatSqlStore } from './local-flatsql';

const schema = {
  standardId: 'TEST', tableName: 'Record', fileId: 'TEST',
  schema: 'include "../types/value.fbs"; table Record { value:Value; } root_type Record; file_identifier "TEST";',
  includes: { '../types/value.fbs': 'struct Value { number:int; }' },
};

describe('local binary schema compilation', () => {
  it('resolves supplied relative includes in the real compiler and engine', async () => {
    const store = await createLocalFlatSqlStore({ schemas: [schema] });
    try {
      expect((await store.query('SELECT count(*) AS n FROM Record', 'TEST')).records).toEqual([{ n: 0 }]);
    } finally { await store.destroy(); }
  });

  it('does not reuse includes from an earlier compilation', async () => {
    const store = await createLocalFlatSqlStore({ schemas: [schema] });
    await store.destroy();
    await expect(createLocalFlatSqlStore({ schemas: [{ ...schema, includes: undefined }] }))
      .rejects.toThrow(/unable to load include file/);
  });

  it('rejects include paths above the virtual filesystem root', async () => {
    await expect(createLocalFlatSqlStore({ schemas: [{
      ...schema, includes: { '../../outside.fbs': 'table Outside {}' },
    }] })).rejects.toThrow(/Invalid schema include path/);
  });
});
