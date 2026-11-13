import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';
import { decodeFlatSqlPublishedList } from './flatsql-sync';

it('retains the missing publication and the holder check from the real Go E2E', () => {
  const page = decodeFlatSqlPublishedList(new Uint8Array(readFileSync(new URL('./testdata/publication-availability.qrp', import.meta.url))));
  expect(page.totalCount).toBe(1);
  expect(page.publications).toHaveLength(1);
  expect(page.publications[0]).toMatchObject({ availability: 'unavailable', availabilityReason: 'shard_missing', providerId: 'availability-e2e', recordCount: 1 });
  expect(page.publications[0].availabilityCheckedAtMs).toBeGreaterThan(0);
  expect(page.publications[0].shardCid).not.toBe('');
});
