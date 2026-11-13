import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';
import { decodeFlatSqlPublishedList } from './flatsql-sync';

it('selects the older available batch by default from the real Go listing, with an explicit unavailable opt-in', () => {
  const bytes = new Uint8Array(readFileSync(new URL('./testdata/publication-availability.qrp', import.meta.url)));
  const page = decodeFlatSqlPublishedList(bytes);
  expect(page.totalCount).toBe(2);
  expect(page.publications).toHaveLength(1);
  const newest = [...page.publications].sort((a, b) => b.publishedAtMs - a.publishedAtMs)[0];
  expect(newest).toMatchObject({ batchId: 'older', availability: 'available', recordCount: 1 });
  const all = decodeFlatSqlPublishedList(bytes, { includeUnavailable: true });
  expect(all.publications).toHaveLength(2);
  expect(all.publications[0]).toMatchObject({ batchId: 'batch', availability: 'unavailable', availabilityReason: 'index_missing', providerId: 'availability-e2e', recordCount: 1 });
  expect(all.publications[0].publishedAtMs).toBeGreaterThan(newest.publishedAtMs);
  expect(all.nextCursor).toBe(page.nextCursor);
  expect(page.publications[0].availabilityCheckedAtMs).toBeGreaterThan(0);
  expect(page.publications[0].shardCid).not.toBe('');
});
