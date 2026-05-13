import { describe, expect, it } from 'vitest';

import { syncRowCountSummary, syncRowCountSummaryLabel } from './sync-progress';

describe('sync progress row counts', () => {
  it('reports synced, pinned, and missing rows from local and remote counts', () => {
    const summary = syncRowCountSummary({
      localRows: 120,
      syncedRows: 100,
      pinnedRows: 80,
      remoteRows: 200,
      totalRows: 150,
    });

    expect(summary).toEqual({
      syncedRows: 120,
      pinnedRows: 80,
      missingRows: 80,
      totalRows: 200,
    });
    expect(syncRowCountSummaryLabel(summary)).toBe('Synced 120/200; Pinned 80 / Missing 80');
  });

  it('treats locally synced rows as pinned when no separate pinned count exists yet', () => {
    expect(syncRowCountSummary({
      localRows: 12,
      syncedRows: 12,
      remoteRows: 12,
      totalRows: 12,
    })).toEqual({
      syncedRows: 12,
      pinnedRows: 12,
      missingRows: 0,
      totalRows: 12,
    });
  });
});
