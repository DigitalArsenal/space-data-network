export type SchemaSyncMode = 'preview' | 'sync';
export type SchemaSyncStatus = 'idle' | 'syncing' | 'synced' | 'capped' | 'error';

export interface SchemaSyncStatusLabelInput {
  preferenceMode: SchemaSyncMode;
  progressStatus: SchemaSyncStatus;
  localRows: number;
  remoteRows: number;
}

export interface EffectiveSchemaSyncStatusInput {
  active: boolean;
  complete: boolean;
  persistedStatus?: SchemaSyncStatus | null;
}

export function effectiveSchemaSyncStatus(input: EffectiveSchemaSyncStatusInput): SchemaSyncStatus {
  if (input.active) return 'syncing';
  if (input.complete) return 'synced';
  return input.persistedStatus === 'syncing' ? 'idle' : input.persistedStatus ?? 'idle';
}

export function schemaSyncStatusLabel(input: SchemaSyncStatusLabelInput): string {
  if (input.preferenceMode !== 'sync') return 'Preview only';
  if (input.progressStatus === 'error') return 'Sync error';
  if (input.progressStatus === 'capped') return 'Storage cap reached';
  if (input.progressStatus === 'syncing') return 'Syncing';
  if (input.progressStatus === 'synced' || (input.remoteRows > 0 && input.localRows >= input.remoteRows)) {
    return 'Synced';
  }
  if (input.remoteRows > input.localRows) return 'Queued';
  return 'Ready';
}
