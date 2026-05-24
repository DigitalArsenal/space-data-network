import type { LocalFlatSqlPinLedgerEntry } from './local-flatsql';

export interface PublishedSegmentForSync {
  cid?: string | null;
  rowCount?: number | null;
  index?: number | null;
  byteCount?: number | null;
}

export interface PublishedSegmentSyncItem<T extends PublishedSegmentForSync> {
  segment: T;
  cumulativeRows: number;
  segmentEnd: number;
  skipRecords: 0;
}

export interface PublishedSegmentMaterializedRowCountInput {
  cid?: string | null;
  standardId: string;
  expectedRows?: number | null;
  materializedRows: number;
}

export interface PublishedSegmentCheckpointInput {
  unpersistedBytes: number;
  checkpointBytes: number;
  completedRows: number;
  totalRows: number;
}

export interface PublishedSnapshotResetInput {
  retentionPolicy?: string | null;
  localRows: number;
  completedRows: number;
  totalRows: number;
}

export interface PublishedSnapshotProbeStartStatusInput {
  hasPublishedManifest: boolean;
  currentStatus?: string | null;
}

export interface PublishedSegmentBatchOptions {
  maxBatchBytes: number;
  maxBatchSegments: number;
}

export interface PublishedSegmentBatchItem<T extends PublishedSegmentForSync> {
  items: Array<PublishedSegmentSyncItem<T>>;
  byteCount: number;
  preferredSourceIndex: number;
}

export interface PublishedSegmentRetentionOptions {
  retentionPolicy?: string | null;
  manifestHead?: string | null;
}

export function pendingPublishedSegmentItems<T extends PublishedSegmentForSync>(
  segments: T[],
  completedCids: ReadonlySet<string>,
): Array<PublishedSegmentSyncItem<T>> {
  const pending: Array<PublishedSegmentSyncItem<T>> = [];
  let cumulativeRows = 0;
  for (const [index, segment] of segments.entries()) {
    const segmentRows = Math.max(0, Math.floor(segment.rowCount ?? 0));
    const segmentEnd = cumulativeRows + segmentRows;
    const cid = segment.cid?.trim() ?? '';
    if (cid && !completedCids.has(cid)) {
      pending.push({
        segment: { ...segment, index: segment.index ?? index },
        cumulativeRows,
        segmentEnd,
        skipRecords: 0,
      });
    }
    cumulativeRows = segmentEnd;
  }
  return pending;
}

export function completedPublishedSegmentCids(entries: LocalFlatSqlPinLedgerEntry[]): Set<string> {
  const completed = new Set<string>();
  for (const entry of entries) {
    const cid = entry.cid?.trim();
    if (!cid) continue;
    if ((entry.role ?? '').trim() !== 'shard') continue;
    if ((entry.verificationState ?? '').trim() !== 'verified') continue;
    if (!entry.materializedAt?.trim()) continue;
    completed.add(cid);
  }
  return completed;
}

export function completedPublishedRowsForSegments<T extends PublishedSegmentForSync>(
  segments: T[],
  completedCids: ReadonlySet<string>,
): number {
  return segments.reduce((sum, segment) => {
    const cid = segment.cid?.trim() ?? '';
    return cid && completedCids.has(cid) ? sum + Math.max(0, Math.floor(segment.rowCount ?? 0)) : sum;
  }, 0);
}

export function assertPublishedSegmentMaterializedRowCount(input: PublishedSegmentMaterializedRowCountInput): void {
  const expectedRows = Math.max(0, Math.floor(input.expectedRows ?? 0));
  const materializedRows = Math.max(0, Math.floor(input.materializedRows));
  if (expectedRows <= 0 && materializedRows > 0) return;
  if (expectedRows > 0 && materializedRows === expectedRows) return;
  const cid = input.cid?.trim() || 'unknown';
  throw new Error(
    `published shard ${cid} materialized ${materializedRows.toLocaleString()}/${expectedRows.toLocaleString()} ${input.standardId} rows`,
  );
}

export function shouldPersistPublishedSegmentCheckpoint(input: PublishedSegmentCheckpointInput): boolean {
  const completedRows = Math.max(0, Math.floor(input.completedRows));
  const totalRows = Math.max(0, Math.floor(input.totalRows));
  if (totalRows > 0 && completedRows >= totalRows) return true;
  const checkpointBytes = Math.max(1, Math.floor(input.checkpointBytes));
  return Math.max(0, Math.floor(input.unpersistedBytes)) >= checkpointBytes;
}

export function shouldResetPublishedSnapshotStore(input: PublishedSnapshotResetInput): boolean {
  if ((input.retentionPolicy ?? '').trim() !== 'replace-snapshot') return false;
  const localRows = Math.max(0, Math.floor(input.localRows));
  const completedRows = Math.max(0, Math.floor(input.completedRows));
  const totalRows = Math.max(0, Math.floor(input.totalRows));
  if (totalRows > 0 && completedRows >= totalRows) {
    return localRows > completedRows;
  }
  if (completedRows === 0) return localRows > 0;
  return localRows !== completedRows;
}

export function publishedSnapshotProbeStartStatus(input: PublishedSnapshotProbeStartStatusInput): 'synced' | 'syncing' {
  return input.hasPublishedManifest && input.currentStatus === 'synced' ? 'synced' : 'syncing';
}

export function publishedSegmentsForRetention<T extends PublishedSegmentForSync & {
  cursor?: string | null;
  nextCursor?: string | null;
  feedHead?: string | null;
}>(
  segments: T[],
  options: PublishedSegmentRetentionOptions,
): T[] {
  if ((options.retentionPolicy ?? '').trim() !== 'replace-snapshot') return segments;
  const manifestHead = options.manifestHead?.trim();
  const cursorChainSegments = latestReplaceSnapshotCursorChain(segments, manifestHead);
  if (cursorChainSegments.length > 0) return cursorChainSegments;
  if (!manifestHead) return segments;
  const currentHeadSegments = segments.filter((segment) => segment.feedHead?.trim() === manifestHead);
  return currentHeadSegments.length > 0 ? currentHeadSegments : segments;
}

function latestReplaceSnapshotCursorChain<T extends PublishedSegmentForSync & {
  cursor?: string | null;
  nextCursor?: string | null;
  feedHead?: string | null;
}>(
  segments: T[],
  manifestHead?: string | null,
): T[] {
  const orderedSegments = segments
    .filter((segment) => Boolean(segment.cid?.trim()))
    .slice()
    .sort((left, right) => (
      Math.max(0, Math.floor(left.index ?? 0)) - Math.max(0, Math.floor(right.index ?? 0))
    ));
  const tailCandidates = orderedSegments.filter((segment) => (
    isEndCursor(segment.nextCursor)
    && (!manifestHead || segment.feedHead?.trim() === manifestHead)
  ));
  const tailSegment = tailCandidates.at(-1) ?? orderedSegments.filter((segment) => isEndCursor(segment.nextCursor)).at(-1);
  if (!tailSegment) return [];

  const chain: T[] = [tailSegment];
  const usedCids = new Set([tailSegment.cid?.trim() ?? '']);
  let cursor = tailSegment.cursor?.trim() ?? '';
  while (cursor && !isStartCursor(cursor)) {
    const firstChainIndex = Math.max(0, Math.floor(chain[0]?.index ?? 0));
    const previousSegment = orderedSegments
      .filter((segment) => {
        const cid = segment.cid?.trim() ?? '';
        return cid
          && !usedCids.has(cid)
          && Math.max(0, Math.floor(segment.index ?? 0)) < firstChainIndex
          && segment.nextCursor?.trim() === cursor;
      })
      .at(-1);
    if (!previousSegment) return [];
    chain.unshift(previousSegment);
    usedCids.add(previousSegment.cid?.trim() ?? '');
    cursor = previousSegment.cursor?.trim() ?? '';
  }
  return chain;
}

function isEndCursor(value?: string | null): boolean {
  return !value?.trim();
}

function isStartCursor(value?: string | null): boolean {
  const cursor = value?.trim() ?? '';
  return cursor === '' || cursor === '0' || cursor === 'MA' || cursor === 'MA==';
}

export function publishedSegmentBatchItems<T extends PublishedSegmentForSync>(
  items: Array<PublishedSegmentSyncItem<T>>,
  options: PublishedSegmentBatchOptions,
): Array<PublishedSegmentBatchItem<T>> {
  const maxBatchBytes = Math.max(1, Math.floor(options.maxBatchBytes));
  const maxBatchSegments = Math.max(1, Math.floor(options.maxBatchSegments));
  const batches: Array<PublishedSegmentBatchItem<T>> = [];
  let current: Array<PublishedSegmentSyncItem<T>> = [];
  let currentBytes = 0;

  const flush = (): void => {
    if (current.length === 0) return;
    batches.push({
      items: current,
      byteCount: currentBytes,
      preferredSourceIndex: Math.max(0, Math.floor(current[0]?.segment.index ?? 0)),
    });
    current = [];
    currentBytes = 0;
  };

  for (const item of items) {
    const itemBytes = Math.max(0, Math.floor(item.segment.byteCount ?? 0));
    const wouldExceedBytes = current.length > 0 && currentBytes + itemBytes > maxBatchBytes;
    const wouldExceedSegments = current.length >= maxBatchSegments;
    if (wouldExceedBytes || wouldExceedSegments) flush();
    current.push(item);
    currentBytes += itemBytes;
    if (itemBytes >= maxBatchBytes || current.length >= maxBatchSegments) flush();
  }
  flush();
  return batches;
}
