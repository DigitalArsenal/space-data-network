import { describe, expect, it } from 'vitest';
import {
  isSchemaSyncProgressStalled,
  nextSchemaSyncStallState,
  schemaSyncProgressFingerprint,
} from './schema-sync-stall';

describe('schema sync stall detection', () => {
  it('fingerprints the row and cursor fields that prove sync advanced', () => {
    expect(schemaSyncProgressFingerprint({
      localRows: 10,
      pinnedRows: 8,
      syncedRows: 10,
      downloadedBytes: 1024,
      cursor: 'a',
      nextCursor: 'b',
      highWaterMark: 'hwm',
      chunkHash: 'sha256',
    })).toBe('10|8|10|1024|a|b|hwm|sha256');
  });

  it('marks a syncing feed stalled after repeated no-advance observations past the age threshold', () => {
    const first = nextSchemaSyncStallState(null, {
      status: 'syncing',
      totalRows: 100,
      missingRows: 90,
      localRows: 10,
      pinnedRows: 10,
      syncedRows: 10,
      cursor: 'cursor-10',
    }, '2026-05-15T00:00:00.000Z');

    const second = nextSchemaSyncStallState(first, {
      status: 'syncing',
      totalRows: 100,
      missingRows: 90,
      localRows: 10,
      pinnedRows: 10,
      syncedRows: 10,
      cursor: 'cursor-10',
    }, '2026-05-15T00:00:30.000Z', { minAgeMs: 60_000, observationLimit: 2 });

    expect(second.stalledSince).toBeNull();

    const third = nextSchemaSyncStallState(second, {
      status: 'syncing',
      totalRows: 100,
      missingRows: 90,
      localRows: 10,
      pinnedRows: 10,
      syncedRows: 10,
      cursor: 'cursor-10',
    }, '2026-05-15T00:01:01.000Z', { minAgeMs: 60_000, observationLimit: 2 });

    expect(third.stallObservationCount).toBe(2);
    expect(third.stalledSince).toBe('2026-05-15T00:01:01.000Z');
    expect(isSchemaSyncProgressStalled({ status: 'syncing', missingRows: 90, ...third })).toBe(true);
  });

  it('resets the stalled state when rows advance or the feed stops syncing', () => {
    const stalled = nextSchemaSyncStallState({
      progressFingerprint: '10|10|10|0|cursor-10|||',
      lastAdvancedAt: '2026-05-15T00:00:00.000Z',
      stallObservationCount: 2,
      stalledSince: '2026-05-15T00:01:01.000Z',
    }, {
      status: 'syncing',
      totalRows: 100,
      missingRows: 90,
      localRows: 10,
      pinnedRows: 10,
      syncedRows: 10,
      cursor: 'cursor-10',
    }, '2026-05-15T00:02:00.000Z', { minAgeMs: 60_000, observationLimit: 2 });

    expect(stalled.stalledSince).toBe('2026-05-15T00:01:01.000Z');

    const advanced = nextSchemaSyncStallState(stalled, {
      status: 'syncing',
      totalRows: 100,
      missingRows: 80,
      localRows: 20,
      pinnedRows: 20,
      syncedRows: 20,
      cursor: 'cursor-20',
    }, '2026-05-15T00:02:10.000Z', { minAgeMs: 60_000, observationLimit: 2 });

    expect(advanced.stallObservationCount).toBe(0);
    expect(advanced.stalledSince).toBeNull();
    expect(advanced.lastAdvancedAt).toBe('2026-05-15T00:02:10.000Z');

    const stopped = nextSchemaSyncStallState(advanced, {
      status: 'idle',
      totalRows: 100,
      missingRows: 80,
      localRows: 20,
      pinnedRows: 20,
      syncedRows: 20,
      cursor: 'cursor-20',
    }, '2026-05-15T00:02:20.000Z', { minAgeMs: 60_000, observationLimit: 2 });

    expect(stopped.stallObservationCount).toBe(0);
    expect(stopped.stalledSince).toBeNull();
    expect(isSchemaSyncProgressStalled({ status: 'idle', missingRows: 80, ...stopped })).toBe(false);
  });
});
