export interface SchemaSyncScheduleRow {
  id: string;
  subscriptionId?: string;
  datastoreKey?: string | null;
  syncFilter?: string;
  queryProfile?: string;
  localRows: number;
  remoteRows: number;
  preference: {
    mode: 'preview' | 'sync';
    storageCap: number;
    storageUnit: string;
  };
}

export interface SchemaSyncSchedulerOptions {
  syncSchema: (standardId: string, dataSourceId: string, subscriptionId?: string) => Promise<void> | void;
}

interface SchemaSyncScheduleSnapshot {
  dataSourceId: string;
  rows: SchemaSyncScheduleRow[];
}

export interface SchemaSyncScheduler {
  schedule: (rows: SchemaSyncScheduleRow[], dataSourceId: string) => Promise<void>;
  reset: () => void;
  idle: () => Promise<void>;
}

export function createSchemaSyncScheduler(options: SchemaSyncSchedulerOptions): SchemaSyncScheduler {
  let running = false;
  let rerunRequested = false;
  let lastAcceptedSignature = '';
  let latestSnapshot: SchemaSyncScheduleSnapshot | null = null;
  let currentRun: Promise<void> = Promise.resolve();

  async function drain(): Promise<void> {
    running = true;
    try {
      do {
        rerunRequested = false;
        const snapshot = latestSnapshot;
        if (!snapshot) break;
        const rows = sortedEnabledSchemaRows(snapshot.rows);
        for (const row of rows) {
          if (!latestSnapshot) break;
          await options.syncSchema(row.id, snapshot.dataSourceId, row.subscriptionId);
        }
      } while (rerunRequested);
    } finally {
      running = false;
      if (rerunRequested && latestSnapshot) {
        currentRun = drain();
      }
    }
  }

  return {
    schedule(rows, dataSourceId) {
      const signature = schemaSyncScheduleSignature(rows, dataSourceId);
      if (!signature) {
        lastAcceptedSignature = '';
        latestSnapshot = null;
        return currentRun;
      }
      if (signature === lastAcceptedSignature) return currentRun;
      lastAcceptedSignature = signature;
      latestSnapshot = { dataSourceId, rows };
      if (running) {
        rerunRequested = true;
        return currentRun;
      }
      currentRun = drain();
      return currentRun;
    },
    reset() {
      rerunRequested = false;
      lastAcceptedSignature = '';
      latestSnapshot = null;
    },
    idle() {
      return currentRun;
    },
  };
}

export function sortedEnabledSchemaRows(rows: SchemaSyncScheduleRow[]): SchemaSyncScheduleRow[] {
  return rows
    .filter(shouldScheduleSchemaRow)
    .sort((left, right) => {
      const rowDelta = right.remoteRows - left.remoteRows;
      return rowDelta === 0 ? left.id.localeCompare(right.id) : rowDelta;
    });
}

function shouldScheduleSchemaRow(row: SchemaSyncScheduleRow): boolean {
  if (row.preference.mode !== 'sync') return false;
  if (row.remoteRows > row.localRows) return true;
  return row.queryProfile === 'dataset-publication-offset-v1' && row.remoteRows === 0;
}

function schemaSyncScheduleSignature(rows: SchemaSyncScheduleRow[], dataSourceId: string): string {
  return sortedEnabledSchemaRows(rows)
    .map((row) => [
      dataSourceId,
      row.id,
      row.subscriptionId ?? '',
      row.datastoreKey ?? '',
      row.syncFilter?.trim() ?? '',
      row.queryProfile?.trim() ?? '',
      row.localRows,
      row.remoteRows,
      row.preference.storageCap,
      row.preference.storageUnit,
    ].join(':'))
    .join('|');
}
