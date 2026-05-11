export interface SchemaSyncScheduleRow {
  id: string;
  localRows: number;
  remoteRows: number;
  preference: {
    mode: 'preview' | 'sync';
    storageCap: number;
    storageUnit: string;
  };
}

export interface SchemaSyncSchedulerOptions {
  syncSchema: (standardId: string, dataSourceId: string) => Promise<void> | void;
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
          await options.syncSchema(row.id, snapshot.dataSourceId);
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
    .filter((row) => row.preference.mode === 'sync' && row.remoteRows > row.localRows)
    .sort((left, right) => {
      const rowDelta = right.remoteRows - left.remoteRows;
      return rowDelta === 0 ? left.id.localeCompare(right.id) : rowDelta;
    });
}

function schemaSyncScheduleSignature(rows: SchemaSyncScheduleRow[], dataSourceId: string): string {
  return sortedEnabledSchemaRows(rows)
    .map((row) => [
      dataSourceId,
      row.id,
      row.localRows,
      row.remoteRows,
      row.preference.storageCap,
      row.preference.storageUnit,
    ].join(':'))
    .join('|');
}
