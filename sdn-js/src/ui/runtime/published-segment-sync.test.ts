import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import {
  assertPublishedSegmentMaterializedRowCount,
  completedPublishedRowsForSegments,
  completedPublishedSegmentRowCounts,
  pendingPublishedSegmentItems,
  publishedSnapshotProbeStartStatus,
  publishedSegmentsForRetention,
  publishedSegmentBatchItems,
  shouldResetPublishedSnapshotStore,
  shouldPersistPublishedSegmentCheckpoint,
} from './published-segment-sync';

describe('published segment sync planning', () => {
  it('does not resume a changed published shard by FlatSQL table row count', () => {
    const pending = pendingPublishedSegmentItems(
      [
        { cid: 'bafy-old-complete', rowCount: 50_000 },
        { cid: 'bafy-republished-tail', rowCount: 50_000 },
        { cid: 'bafy-new-tail', rowCount: 3_485 },
      ],
      new Set(['bafy-old-complete', 'bafy-previous-tail']),
    );

    expect(pending.map((item) => ({
      cid: item.segment.cid,
      cumulativeRows: item.cumulativeRows,
      segmentEnd: item.segmentEnd,
      skipRecords: item.skipRecords,
    }))).toEqual([
      {
        cid: 'bafy-republished-tail',
        cumulativeRows: 50_000,
        segmentEnd: 100_000,
        skipRecords: 0,
      },
      {
        cid: 'bafy-new-tail',
        cumulativeRows: 100_000,
        segmentEnd: 103_485,
        skipRecords: 0,
      },
    ]);
  });

  it('rejects verified shard pinning when FlatSQL materializes fewer rows than the segment advertises', () => {
    expect(() => assertPublishedSegmentMaterializedRowCount({
      cid: 'bafy-partial',
      standardId: 'OMM',
      expectedRows: 50_000,
      materializedRows: 49_999,
    })).toThrow('published shard bafy-partial materialized 49,999/50,000 OMM rows');

    expect(() => assertPublishedSegmentMaterializedRowCount({
      cid: 'bafy-complete',
      standardId: 'OMM',
      expectedRows: 50_000,
      materializedRows: 50_000,
    })).not.toThrow();

    expect(() => assertPublishedSegmentMaterializedRowCount({
      cid: 'bafy-cat-deduped',
      standardId: 'CAT',
      expectedRows: 50_000,
      materializedRows: 15_900,
      allowFewerRows: true,
    })).not.toThrow();
  });

  it('guards the worker pin-ledger write with the materialized row-count check', () => {
    const source = readFileSync(new URL('./local-flatsql.worker.ts', import.meta.url), 'utf8');
    const guardIndex = source.indexOf('assertPublishedSegmentMaterializedRowCount({');
    const ledgerEntryIndex = source.indexOf('const ledgerEntry = pinLedgerEntryForPublishedSegment');
    const pinIndex = source.indexOf('recordPinLedgerEntries([ledgerEntry]');

    expect(guardIndex).toBeGreaterThan(-1);
    expect(ledgerEntryIndex).toBeGreaterThan(guardIndex);
    expect(pinIndex).toBeGreaterThan(ledgerEntryIndex);
  });

  it('requires active store replacement for changed published replace-snapshot manifests', () => {
    expect(shouldResetPublishedSnapshotStore({
      retentionPolicy: 'replace-snapshot',
      localRows: 972_000,
      completedRows: 0,
      totalRows: 69_050,
    })).toBe(true);
    expect(shouldResetPublishedSnapshotStore({
      retentionPolicy: 'replace-snapshot',
      localRows: 972_000,
      completedRows: 69_050,
      totalRows: 69_050,
    })).toBe(true);
    expect(shouldResetPublishedSnapshotStore({
      retentionPolicy: 'replace-snapshot',
      localRows: 50_000,
      completedRows: 50_000,
      totalRows: 69_050,
    })).toBe(false);
    expect(shouldResetPublishedSnapshotStore({
      retentionPolicy: 'replace-snapshot',
      localRows: 0,
      completedRows: 69_050,
      totalRows: 69_050,
    })).toBe(false);
    expect(shouldResetPublishedSnapshotStore({
      retentionPolicy: 'replace-snapshot',
      localRows: 69_050,
      completedRows: 69_050,
      totalRows: 69_050,
    })).toBe(false);
    expect(shouldResetPublishedSnapshotStore({
      retentionPolicy: 'replace-snapshot',
      localRows: 69_050,
      completedRows: 69_050,
      totalRows: 69_050,
      requiresMaterializedKeyRepair: true,
    })).toBe(true);
    expect(shouldResetPublishedSnapshotStore({
      retentionPolicy: 'append-only',
      localRows: 972_000,
      completedRows: 0,
      totalRows: 69_050,
    })).toBe(false);
  });

  it('keeps completed published snapshot manifest checks visually stable', () => {
    expect(publishedSnapshotProbeStartStatus({
      hasPublishedManifest: true,
      currentStatus: 'synced',
    })).toBe('synced');
    expect(publishedSnapshotProbeStartStatus({
      hasPublishedManifest: true,
      currentStatus: 'syncing',
    })).toBe('syncing');
    expect(publishedSnapshotProbeStartStatus({
      hasPublishedManifest: false,
      currentStatus: 'synced',
    })).toBe('syncing');
  });

  it('uses only the current manifest head for replace-snapshot feeds', () => {
    const segments = publishedSegmentsForRetention([
      { index: 0, cid: 'bafy-old-a', rowCount: 15_000, feedHead: 'head-old-a' },
      { index: 1, cid: 'bafy-old-b', rowCount: 15_100, feedHead: 'head-old-b' },
      { index: 2, cid: 'bafy-current', rowCount: 15_900, feedHead: 'head-current' },
    ], {
      retentionPolicy: 'replace-snapshot',
      manifestHead: 'head-current',
    });

    expect(segments).toEqual([
      expect.objectContaining({ cid: 'bafy-current', rowCount: 15_900 }),
    ]);
    expect(publishedSegmentsForRetention(segments, {
      retentionPolicy: 'append-only',
      manifestHead: 'head-current',
    })).toHaveLength(1);
  });

  it('uses the latest complete cursor chain for replace-snapshot manifests with retained history', () => {
    const segments = publishedSegmentsForRetention([
      { index: 0, cid: 'bafy-old-0', rowCount: 15_855, cursor: 'MA', nextCursor: 'MA', feedHead: 'old-0' },
      { index: 1, cid: 'bafy-old-1', rowCount: 15_856, cursor: 'MA', nextCursor: 'MA', feedHead: 'old-1' },
      { index: 2, cid: 'bafy-old-2', rowCount: 15_858, cursor: 'MA', nextCursor: 'MA', feedHead: 'old-2' },
      { index: 7, cid: 'bafy-current-a', rowCount: 15_762, cursor: 'MA', nextCursor: 'NTAwMDA', feedHead: 'older-head' },
      { index: 8, cid: 'bafy-current-b', rowCount: 32_315, cursor: 'NTAwMDA', nextCursor: '', feedHead: 'current-head' },
    ], {
      retentionPolicy: 'replace-snapshot',
      manifestHead: 'current-head',
    });

    expect(segments.map((segment) => segment.cid)).toEqual([
      'bafy-current-a',
      'bafy-current-b',
    ]);
  });

  it('counts completed replacement rows from the materialized pin ledger row counts', () => {
    const rows = completedPublishedSegmentRowCounts([{
      cid: 'bafy-current',
      standardId: 'CAT',
      schemaName: 'CAT.fbs',
      role: 'shard',
      rowCount: 15_900,
      byteCount: 1,
      verificationState: 'verified',
      materializedAt: '2026-05-24T00:00:00.000Z',
    }]);

    expect(completedPublishedRowsForSegments(
      [{ cid: 'bafy-current', rowCount: 32_315 }],
      new Set(['bafy-current']),
      rows,
    )).toBe(15_900);
  });

  it('stages replace-snapshot published stores and commits only after replacement shards are fetched', () => {
    const source = readFileSync(new URL('./local-flatsql.worker.ts', import.meta.url), 'utf8');
    const resetIndex = source.indexOf('shouldResetPublishedSnapshotStore({');
    const stageIndex = source.indexOf('await options.currentStore.createStandardReplacementStore(options.request.standardId');
    const fetchIndex = source.indexOf('fetchPublishedSegmentsInOrder(options.segments');
    const completeIndex = source.indexOf('const replacementComplete = pendingPublishedSegmentItems(options.segments, completedSegmentCids).length === 0');
    const swapIndex = source.indexOf('await options.currentStore.replaceStandardFrom(options.request.standardId');

    expect(resetIndex).toBeGreaterThan(-1);
    expect(stageIndex).toBeGreaterThan(resetIndex);
    expect(fetchIndex).toBeGreaterThan(stageIndex);
    expect(completeIndex).toBeGreaterThan(fetchIndex);
    expect(swapIndex).toBeGreaterThan(completeIndex);
  });

  it('seeds published shard range source preference with the manifest segment index', () => {
    const source = readFileSync(new URL('./local-flatsql.worker.ts', import.meta.url), 'utf8');

    expect(source).toContain('publishedShardPreferredSourceIndex(options.segment, range.index)');
    expect(source).toContain('function publishedShardPreferredSourceIndex(');
  });

  it('checkpoints published shard persistence by bytes or final completion instead of every shard', () => {
    expect(shouldPersistPublishedSegmentCheckpoint({
      unpersistedBytes: 48 * 1024 * 1024,
      checkpointBytes: 256 * 1024 * 1024,
      completedRows: 50_000,
      totalRows: 2_000_000,
    })).toBe(false);

    expect(shouldPersistPublishedSegmentCheckpoint({
      unpersistedBytes: 257 * 1024 * 1024,
      checkpointBytes: 256 * 1024 * 1024,
      completedRows: 500_000,
      totalRows: 2_000_000,
    })).toBe(true);

    expect(shouldPersistPublishedSegmentCheckpoint({
      unpersistedBytes: 16 * 1024 * 1024,
      checkpointBytes: 256 * 1024 * 1024,
      completedRows: 2_000_000,
      totalRows: 2_000_000,
    })).toBe(true);
  });

  it('groups pending published shards into bounded batch reads', () => {
    const pending = pendingPublishedSegmentItems(
      [
        { cid: 'bafy-a', rowCount: 10, byteCount: 60, index: 0 },
        { cid: 'bafy-b', rowCount: 10, byteCount: 50, index: 1 },
        { cid: 'bafy-c', rowCount: 10, byteCount: 40, index: 2 },
        { cid: 'bafy-d', rowCount: 10, byteCount: 20, index: 3 },
      ],
      new Set(),
    );

    const batches = publishedSegmentBatchItems(pending, {
      maxBatchBytes: 100,
      maxBatchSegments: 2,
    });

    expect(batches.map((batch) => ({
      cids: batch.items.map((item) => item.segment.cid),
      byteCount: batch.byteCount,
      preferredSourceIndex: batch.preferredSourceIndex,
    }))).toEqual([
      { cids: ['bafy-a'], byteCount: 60, preferredSourceIndex: 0 },
      { cids: ['bafy-b', 'bafy-c'], byteCount: 90, preferredSourceIndex: 1 },
      { cids: ['bafy-d'], byteCount: 20, preferredSourceIndex: 3 },
    ]);
  });
});
