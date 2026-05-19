import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import {
  assertPublishedSegmentMaterializedRowCount,
  pendingPublishedSegmentItems,
  publishedSegmentBatchItems,
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
  });

  it('guards the worker pin-ledger write with the materialized row-count check', () => {
    const source = readFileSync(new URL('./local-flatsql.worker.ts', import.meta.url), 'utf8');
    const guardIndex = source.indexOf('assertPublishedSegmentMaterializedRowCount({');
    const pinIndex = source.indexOf('recordPinLedgerEntries([pinLedgerEntryForPublishedSegment');

    expect(guardIndex).toBeGreaterThan(-1);
    expect(pinIndex).toBeGreaterThan(guardIndex);
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
