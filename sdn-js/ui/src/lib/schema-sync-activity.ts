import type { SchemaSyncStatus } from './schema-sync-labels';

export interface PublishedSnapshotProbeInput {
  queryProfile?: string | null;
  retentionPolicy?: string | null;
  progressQueryProfile?: string | null;
  progressStatus?: SchemaSyncStatus | string | null;
  localRows: number;
  totalRows: number;
}

export interface PublishedRemoteRowsInput {
  queryProfile?: string | null;
  progressQueryProfile?: string | null;
  advertisedRemoteRows: number;
  progressTotalRows: number;
}

export function shouldRunPublishedSnapshotProbe(input: PublishedSnapshotProbeInput): boolean {
  const localRows = nonNegativeInteger(input.localRows);
  const totalRows = nonNegativeInteger(input.totalRows);
  return input.queryProfile === 'dataset-publication-offset-v1'
    && input.retentionPolicy === 'replace-snapshot'
    && input.progressQueryProfile === 'dataset-publication-offset-v1'
    && input.progressStatus === 'synced'
    && totalRows > 0
    && localRows >= totalRows;
}

export function effectivePublishedRemoteRows(input: PublishedRemoteRowsInput): number {
  const advertisedRemoteRows = nonNegativeInteger(input.advertisedRemoteRows);
  const progressTotalRows = nonNegativeInteger(input.progressTotalRows);
  if (
    input.queryProfile === 'dataset-publication-offset-v1'
    && input.progressQueryProfile === 'dataset-publication-offset-v1'
    && progressTotalRows > 0
  ) {
    return progressTotalRows;
  }
  return Math.max(advertisedRemoteRows, progressTotalRows);
}

function nonNegativeInteger(value: number): number {
  return Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}
