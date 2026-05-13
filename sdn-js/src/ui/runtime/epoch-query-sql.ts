export type EpochSqlProfile =
  | 'epoch.day'
  | 'epoch.window'
  | 'epoch.as_of'
  | 'epoch.forward'
  | 'epoch.nearest'
  | 'epoch.coverage';

export interface EpochProfileSqlOptions {
  standardId: string;
  profile: EpochSqlProfile;
  day?: string | null;
  at?: string | null;
  from?: string | null;
  to?: string | null;
  maxDeltaSeconds?: number | null;
  entityId?: string | null;
  limit?: number | null;
}

export const EPOCH_SQL_PROFILES: Array<{ id: EpochSqlProfile; label: string }> = [
  { id: 'epoch.day', label: 'Day' },
  { id: 'epoch.window', label: 'Window' },
  { id: 'epoch.as_of', label: 'As of' },
  { id: 'epoch.forward', label: 'Forward' },
  { id: 'epoch.nearest', label: 'Nearest' },
  { id: 'epoch.coverage', label: 'Coverage' },
];

export const EPOCH_SQL_PROFILE_DESCRIPTIONS: Record<EpochSqlProfile, string> = {
  'epoch.day': 'All records whose EPOCH falls on one UTC day.',
  'epoch.window': 'All records whose EPOCH falls in a half-open UTC time range.',
  'epoch.as_of': 'One latest record per NORAD_CAT_ID at or before the requested UTC time.',
  'epoch.forward': 'One earliest record per NORAD_CAT_ID at or after the requested UTC time.',
  'epoch.nearest': 'One nearest record per NORAD_CAT_ID on either side of the requested UTC time.',
  'epoch.coverage': 'Per-day OMM row counts plus oldest and newest EPOCH values.',
};

export function buildEpochProfileSql(options: EpochProfileSqlOptions): string {
  const standardId = options.standardId.trim().toUpperCase();
  if (standardId !== 'OMM') {
    throw new Error('Epoch profile SQL is currently available for OMM');
  }

  const limit = clampLimit(options.limit);
  const entityClause = ommEntityClause(options.entityId);
  switch (options.profile) {
    case 'epoch.day': {
      const from = dateStartIso(options.day);
      const to = nextDayStartIso(options.day);
      return `SELECT * FROM OMM WHERE EPOCH >= ${sqlString(from)} AND EPOCH < ${sqlString(to)}${entityClause} ORDER BY EPOCH ASC, NORAD_CAT_ID ASC LIMIT ${limit}`;
    }
    case 'epoch.window': {
      const from = dateTimeIso(options.from, 'from');
      const to = dateTimeIso(options.to, 'to');
      if (from >= to) throw new Error('epoch.window requires from before to');
      return `SELECT * FROM OMM WHERE EPOCH >= ${sqlString(from)} AND EPOCH < ${sqlString(to)}${entityClause} ORDER BY EPOCH ASC, NORAD_CAT_ID ASC LIMIT ${limit}`;
    }
    case 'epoch.as_of':
      return buildFillPolicySql('DESC', options, limit, entityClause);
    case 'epoch.forward':
      return buildFillPolicySql('ASC', options, limit, entityClause);
    case 'epoch.nearest':
      return buildNearestSql(options, limit, entityClause);
    case 'epoch.coverage':
      return buildCoverageSql(options, limit, entityClause);
    default:
      throw new Error(`Unsupported epoch profile ${(options as { profile: string }).profile}`);
  }
}

function buildFillPolicySql(direction: 'ASC' | 'DESC', options: EpochProfileSqlOptions, limit: number, entityClause: string): string {
  const at = dateTimeIso(options.at, 'at');
  const comparison = direction === 'DESC' ? '<=' : '>=';
  const deltaClause = maxDeltaClause(at, options.maxDeltaSeconds);
  return [
    'WITH ranked AS (',
    `SELECT *, ROW_NUMBER() OVER (PARTITION BY NORAD_CAT_ID ORDER BY EPOCH ${direction}, OBJECT_ID ASC, OBJECT_NAME ASC) AS rn`,
    `FROM OMM WHERE EPOCH ${comparison} ${sqlString(at)}${entityClause}${deltaClause}`,
    ')',
    `SELECT * FROM ranked WHERE rn = 1 ORDER BY NORAD_CAT_ID ASC LIMIT ${limit}`,
  ].join(' ');
}

function buildNearestSql(options: EpochProfileSqlOptions, limit: number, entityClause: string): string {
  const at = dateTimeIso(options.at, 'at');
  const deltaClause = maxDeltaClause(at, options.maxDeltaSeconds);
  return [
    'WITH ranked AS (',
    'SELECT *, ROW_NUMBER() OVER (PARTITION BY NORAD_CAT_ID ORDER BY',
    `ABS(strftime('%s', EPOCH) - strftime('%s', ${sqlString(at)})) ASC,`,
    `CASE WHEN EPOCH <= ${sqlString(at)} THEN 0 ELSE 1 END ASC, EPOCH DESC, OBJECT_ID ASC, OBJECT_NAME ASC) AS rn`,
    `FROM OMM WHERE EPOCH IS NOT NULL${entityClause}${deltaClause}`,
    ')',
    `SELECT * FROM ranked WHERE rn = 1 ORDER BY NORAD_CAT_ID ASC LIMIT ${limit}`,
  ].join(' ');
}

function buildCoverageSql(options: EpochProfileSqlOptions, limit: number, entityClause: string): string {
  const filters = ['EPOCH IS NOT NULL'];
  if (options.day?.trim()) {
    filters.push(`EPOCH >= ${sqlString(dateStartIso(options.day))}`);
    filters.push(`EPOCH < ${sqlString(nextDayStartIso(options.day))}`);
  } else {
    if (options.from?.trim()) filters.push(`EPOCH >= ${sqlString(dateTimeIso(options.from, 'from'))}`);
    if (options.to?.trim()) filters.push(`EPOCH < ${sqlString(dateTimeIso(options.to, 'to'))}`);
  }
  if (entityClause) filters.push(entityClause.replace(/^ AND /, ''));
  return `SELECT substr(EPOCH, 1, 10) AS epoch_day, COUNT(*) AS row_count, MIN(EPOCH) AS oldest_epoch, MAX(EPOCH) AS newest_epoch FROM OMM WHERE ${filters.join(' AND ')} GROUP BY epoch_day ORDER BY epoch_day DESC LIMIT ${limit}`;
}

function maxDeltaClause(at: string, value: number | null | undefined): string {
  const maxDelta = Math.floor(Number(value));
  if (!Number.isFinite(maxDelta) || maxDelta <= 0) return '';
  return ` AND ABS(strftime('%s', EPOCH) - strftime('%s', ${sqlString(at)})) <= ${maxDelta}`;
}

function ommEntityClause(value: string | null | undefined): string {
  const trimmed = value?.trim() ?? '';
  if (!trimmed) return '';
  if (!/^\d+$/.test(trimmed)) throw new Error('OMM entity scope expects a NORAD catalog ID');
  return ` AND NORAD_CAT_ID = ${Number(trimmed)}`;
}

function clampLimit(value: number | null | undefined): number {
  const numeric = Math.floor(Number(value));
  if (!Number.isFinite(numeric) || numeric <= 0) return 10;
  return Math.max(1, Math.min(1000, numeric));
}

function dateStartIso(value: string | null | undefined): string {
  const day = normalizedDay(value);
  return `${day}T00:00:00Z`;
}

function nextDayStartIso(value: string | null | undefined): string {
  const day = normalizedDay(value);
  const date = new Date(`${day}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + 1);
  return date.toISOString().replace('.000Z', 'Z');
}

function normalizedDay(value: string | null | undefined): string {
  const trimmed = value?.trim() ?? '';
  if (!/^\d{4}-\d{2}-\d{2}$/.test(trimmed)) throw new Error('epoch.day expected YYYY-MM-DD');
  return trimmed;
}

function dateTimeIso(value: string | null | undefined, field: string): string {
  const trimmed = value?.trim() ?? '';
  if (!trimmed) throw new Error(`epoch profile requires ${field}`);
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(trimmed)) return `${trimmed}:00Z`;
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(trimmed)) return trimmed;
  if (/^\d{4}-\d{2}-\d{2}$/.test(trimmed)) return `${trimmed}T00:00:00Z`;
  throw new Error(`${field} expected UTC ISO time`);
}

function sqlString(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}
