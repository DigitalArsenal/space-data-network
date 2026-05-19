export const DEFAULT_SYNC_STALL_OBSERVATION_LIMIT = 2;
export const DEFAULT_SYNC_STALL_MIN_AGE_MS = 60_000;

export interface SchemaSyncStallSnapshot {
  status?: string | null;
  localRows?: number | null;
  pinnedRows?: number | null;
  syncedRows?: number | null;
  downloadedBytes?: number | null;
  totalRows?: number | null;
  missingRows?: number | null;
  cursor?: string | null;
  nextCursor?: string | null;
  highWaterMark?: string | null;
  chunkHash?: string | null;
  error?: string | null;
  progressFingerprint?: string | null;
  lastAdvancedAt?: string | null;
  lastProgressObservedAt?: string | null;
  stallObservationCount?: number | null;
  stalledSince?: string | null;
}

export interface SchemaSyncStallState {
  progressFingerprint: string | null;
  lastAdvancedAt: string | null;
  lastProgressObservedAt: string | null;
  stallObservationCount: number;
  stalledSince: string | null;
}

export interface SchemaSyncStallOptions {
  observationLimit?: number;
  minAgeMs?: number;
}

export function schemaSyncProgressFingerprint(progress: SchemaSyncStallSnapshot): string {
  return [
    normalizedCount(progress.localRows),
    normalizedCount(progress.pinnedRows),
    normalizedCount(progress.syncedRows),
    normalizedCount(progress.downloadedBytes),
    normalizedString(progress.cursor),
    normalizedString(progress.nextCursor),
    normalizedString(progress.highWaterMark),
    normalizedString(progress.chunkHash),
  ].join('|');
}

export function nextSchemaSyncStallState(
  previous: SchemaSyncStallSnapshot | null | undefined,
  current: SchemaSyncStallSnapshot,
  nowIso = new Date().toISOString(),
  options: SchemaSyncStallOptions = {},
): SchemaSyncStallState {
  const fingerprint = schemaSyncProgressFingerprint(current);
  const activeWithMissingRows = current.status === 'syncing' &&
    !normalizedString(current.error) &&
    normalizedCount(current.totalRows) > 0 &&
    normalizedCount(current.missingRows) > 0;

  if (!activeWithMissingRows) {
    return {
      progressFingerprint: fingerprint,
      lastAdvancedAt: previous?.lastAdvancedAt ?? nowIso,
      lastProgressObservedAt: nowIso,
      stallObservationCount: 0,
      stalledSince: null,
    };
  }

  const previousFingerprint = normalizedString(previous?.progressFingerprint);
  const advanced = !previousFingerprint || previousFingerprint !== fingerprint;
  const lastAdvancedAt = advanced ? nowIso : previous?.lastAdvancedAt ?? nowIso;
  const stallObservationCount = advanced ? 0 : Math.max(0, normalizedCount(previous?.stallObservationCount)) + 1;
  const minAgeMs = Math.max(0, options.minAgeMs ?? DEFAULT_SYNC_STALL_MIN_AGE_MS);
  const observationLimit = Math.max(1, Math.floor(options.observationLimit ?? DEFAULT_SYNC_STALL_OBSERVATION_LIMIT));
  const elapsedSinceAdvanceMs = Math.max(0, Date.parse(nowIso) - Date.parse(lastAdvancedAt));
  const stalledSince = stallObservationCount >= observationLimit && elapsedSinceAdvanceMs >= minAgeMs
    ? previous?.stalledSince ?? nowIso
    : null;

  return {
    progressFingerprint: fingerprint,
    lastAdvancedAt,
    lastProgressObservedAt: nowIso,
    stallObservationCount,
    stalledSince,
  };
}

export function isSchemaSyncProgressStalled(progress: SchemaSyncStallSnapshot): boolean {
  return progress.status === 'syncing' &&
    normalizedCount(progress.missingRows) > 0 &&
    Boolean(normalizedString(progress.stalledSince));
}

function normalizedCount(value: unknown): number {
  const numeric = Number(value);
  if (!Number.isFinite(numeric) || numeric < 0) return 0;
  return Math.floor(numeric);
}

function normalizedString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}
