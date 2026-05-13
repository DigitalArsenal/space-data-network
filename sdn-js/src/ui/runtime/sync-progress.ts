export interface SyncRowCountInput {
  localRows: number;
  syncedRows: number;
  remoteRows: number;
  totalRows: number;
  pinnedRows?: number | null;
}

export interface SyncRowCountSummary {
  syncedRows: number;
  pinnedRows: number;
  missingRows: number;
  totalRows: number;
}

export function syncRowCountSummary(input: SyncRowCountInput): SyncRowCountSummary {
  const syncedRows = Math.max(nonNegativeInteger(input.localRows), nonNegativeInteger(input.syncedRows));
  const totalRows = Math.max(nonNegativeInteger(input.remoteRows), nonNegativeInteger(input.totalRows), syncedRows);
  const pinnedRows = Math.min(totalRows, Math.max(0, nonNegativeInteger(input.pinnedRows ?? syncedRows)));
  return {
    syncedRows,
    pinnedRows,
    missingRows: Math.max(0, totalRows - syncedRows),
    totalRows,
  };
}

export function syncRowCountSummaryLabel(summary: SyncRowCountSummary): string {
  return `Synced ${formatInteger(summary.syncedRows)}/${formatInteger(summary.totalRows)}; Pinned ${formatInteger(summary.pinnedRows)} / Missing ${formatInteger(summary.missingRows)}`;
}

function nonNegativeInteger(value: number | null | undefined): number {
  if (!Number.isFinite(value ?? NaN)) return 0;
  return Math.max(0, Math.floor(value as number));
}

function formatInteger(value: number): string {
  return new Intl.NumberFormat('en-US').format(value);
}
