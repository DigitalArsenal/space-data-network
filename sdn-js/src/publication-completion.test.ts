import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';
import { decodeFlatSqlPublishedList } from './flatsql-sync';

it('reads exact-multiple completion from Go and leaves old publications without a hint', () => {
  const read = (name: string) => decodeFlatSqlPublishedList(new Uint8Array(readFileSync(new URL(`./testdata/${name}.qrp`, import.meta.url))));
  const complete = read('publication-completion');
  expect(complete.publications).toHaveLength(2);
  for (const row of complete.publications) {
    expect(row).toMatchObject({ limit: 1, recordCount: 1, batchCompletion: 'complete', batchTotalRecords: 2 });
    expect(row.batchRevision).toMatch(/^[a-f0-9]{64}$/);
  }
  expect(complete.publications[0].batchRevision).toBe(complete.publications[1].batchRevision);
  const older = read('publication-availability').publications[0];
  expect(older.batchCompletion).toBeUndefined();
  expect(older.batchRevision).toBeUndefined();
  expect(older.batchTotalRecords).toBeUndefined();
});
