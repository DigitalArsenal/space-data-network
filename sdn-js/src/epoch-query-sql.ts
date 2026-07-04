/**
 * Epoch query profiles (loop D.2) — THE primary public query API surface.
 *
 * Two layers live here:
 *
 * 1. ENGINE-NATIVE epoch profiles (`nearest` / `as_of` / `forward`): the
 *    exact per-object window queries the sdn-server runs inside the
 *    FlatSQL-WASM engine over the unified per-standard view
 *    (sdn-server/internal/storage/engine_records.go). The SQL built here for
 *    OMM MUST stay BYTE-IDENTICAL to the server's constants — same engine
 *    query-cache identity, same aligned result streams on every host. The
 *    shared fixture `shared-test-vectors/flatsql-parity.json`
 *    (`engineEpochSql`) pins the strings; sdn-js asserts builder === fixture
 *    (src/flatsql-parity.test.ts) and the Go host asserts constants ===
 *    fixture (sdn-server/internal/storage/engine_records_parity_test.go).
 *
 *    Positional params, in this exact order (server contract):
 *      ?1 source shadow-table name (`OMM@celestrak-gp`; '' = all sources)
 *      ?2 epoch unix seconds (fractional allowed)
 *      ?3 limit (-1 = unlimited)
 *
 *    TLV note: the JS engine binding encodes integral JS numbers as INT64
 *    and fractional numbers as FLOAT64, while the Go host always binds the
 *    epoch as FLOAT64. SQLite numeric comparison makes the RESULT STREAMS
 *    byte-identical either way (proven by the parity harness for both an
 *    integral and a fractional epoch); for byte-identical TLV/cache identity
 *    with the server, pass a fractional epoch (the default epoch — now — is
 *    effectively always fractional).
 *
 * 2. Control-path profile SQL for the webUI/LLM query context (day / window
 *    / coverage plus row-oriented as_of/forward/nearest over the EPOCH
 *    string column). Promoted unchanged from `src/ui/runtime/` (the old
 *    path re-exports this module).
 */

// ---------------------------------------------------------------------------
// Engine-native epoch profiles (server mirror — engine_records.go)
// ---------------------------------------------------------------------------

export const ENGINE_EPOCH_PROFILES = ['nearest', 'as_of', 'forward'] as const;
export type EngineEpochProfile = (typeof ENGINE_EPOCH_PROFILES)[number];

/** Columns the engine profile SQL is built from for one standard. */
export interface EngineEpochProfileSpec {
  /** Engine table / unified-view name (`OMM`). */
  tableName: string;
  /** Per-object partition column (`NORAD_CAT_ID`). */
  partitionColumn: string;
  /** Numeric epoch column (`USER_DEFINED_EPOCH_TIMESTAMP`). */
  epochColumn: string;
}

/**
 * Built-in per-standard specs. OMM is the only standard the server routes
 * into the engine today; the registry (plus the per-schema `epochSpec`
 * override on `LocalFlatSqlSchema`) is how CAT/MPE/SPW pick up engine epoch
 * profiles WITHOUT code changes once their columns are configured.
 */
export const DEFAULT_ENGINE_EPOCH_SPECS: Record<string, Omit<EngineEpochProfileSpec, 'tableName'>> = {
  OMM: {
    partitionColumn: 'NORAD_CAT_ID',
    epochColumn: 'USER_DEFINED_EPOCH_TIMESTAMP',
  },
};

/** Retrieval-module fallback profile (modules data-source/retrieval). */
export const ENGINE_EPOCH_FALLBACK_PROFILE: EngineEpochProfile = 'nearest';
/** Retrieval-module fallback limit (modules data-source/retrieval). */
export const ENGINE_EPOCH_FALLBACK_LIMIT = 50000;

/** Accepts `nearest` / `as_of` / `forward`, with or without the `epoch.` prefix. */
export function normalizeEngineEpochProfile(profile: string): EngineEpochProfile {
  const normalized = profile.trim().replace(/^epoch\./, '');
  if (!(ENGINE_EPOCH_PROFILES as readonly string[]).includes(normalized)) {
    throw new Error(
      `unsupported engine epoch profile "${profile}" (want nearest, as_of, or forward)`,
    );
  }
  return normalized as EngineEpochProfile;
}

/**
 * Engine profile SQL. For the OMM spec the output is byte-identical to
 * sdn-server internal/storage/engine_records.go (engineEpochNearestSQL /
 * engineEpochAsOfSQL / engineEpochForwardSQL) — asserted against the shared
 * parity fixture on both hosts. Do NOT reformat these strings.
 */
export function buildEngineEpochProfileSql(
  profile: EngineEpochProfile | string,
  spec: EngineEpochProfileSpec,
): string {
  const t = spec.tableName;
  const p = spec.partitionColumn;
  const e = spec.epochColumn;
  switch (normalizeEngineEpochProfile(String(profile))) {
    case 'nearest':
      return `SELECT _data FROM (SELECT _data, ROW_NUMBER() OVER (PARTITION BY ${p} ORDER BY ABS(${e} - ?2)) rn FROM ${t} WHERE (?1 = '' OR _source = ?1)) WHERE rn = 1 LIMIT ?3`;
    case 'as_of':
      return `SELECT _data FROM (SELECT _data, ROW_NUMBER() OVER (PARTITION BY ${p} ORDER BY ${e} DESC) rn FROM ${t} WHERE (?1 = '' OR _source = ?1) AND ${e} <= ?2) WHERE rn = 1 LIMIT ?3`;
    case 'forward':
      return `SELECT _data FROM (SELECT _data, ROW_NUMBER() OVER (PARTITION BY ${p} ORDER BY ${e} ASC) rn FROM ${t} WHERE (?1 = '' OR _source = ?1) AND ${e} >= ?2) WHERE rn = 1 LIMIT ?3`;
  }
}

/** Per-request epoch query fields (all optional — config/fallback fill the rest). */
export interface EngineEpochQueryRequest {
  /** `nearest` / `as_of` / `forward` (optionally `epoch.`-prefixed). */
  profile?: string | null;
  /** Bare source name (`celestrak-gp`); empty/absent = all sources. */
  source?: string | null;
  /** Epoch unix seconds (fractional allowed). Default: now. */
  epoch?: number | null;
  /** Row limit; any value <= 0 means unlimited. Default: 50000 (retrieval-module fallback). */
  limit?: number | null;
}

/**
 * Per-standard default query profile — the same JSON shape the retrieval
 * module reads through plugin.getConfig:
 * `{"profiles":{"OMM.fbs":{"profile":"nearest","limit":50000,...}}}`.
 */
export type EngineEpochProfileDefaults = EngineEpochQueryRequest;

/** Per-standard profile config keyed by schema name (`OMM.fbs`) or standard id (`OMM`). */
export type EngineEpochQueryProfilesConfig = Record<string, EngineEpochProfileDefaults>;

/** A fully resolved engine epoch query, ready for queryRawFlatBufferStream. */
export interface EngineEpochQuery {
  profile: EngineEpochProfile;
  sql: string;
  /**
   * Positional params exactly as the server binds them:
   * [sourceShadowName, epochUnixSeconds, limit].
   */
  params: [string, number, number];
}

/**
 * Resolve an engine epoch query with the retrieval module's precedence:
 * request > per-standard config defaults > compiled fallback
 * (`nearest`, epoch = now, limit 50000).
 */
export function resolveEngineEpochQuery(options: {
  spec: EngineEpochProfileSpec;
  request?: EngineEpochQueryRequest | null;
  defaults?: EngineEpochProfileDefaults | null;
  nowSeconds?: (() => number) | null;
}): EngineEpochQuery {
  const request = options.request ?? {};
  const defaults = options.defaults ?? {};
  const profile = normalizeEngineEpochProfile(
    firstNonEmptyString(request.profile, defaults.profile) ?? ENGINE_EPOCH_FALLBACK_PROFILE,
  );
  const source = firstNonEmptyString(request.source, defaults.source) ?? '';
  const epoch = firstFiniteNumber(request.epoch, defaults.epoch)
    ?? (options.nowSeconds ?? defaultNowSeconds)();
  const limitCandidate = firstFiniteNumber(request.limit, defaults.limit)
    ?? ENGINE_EPOCH_FALLBACK_LIMIT;
  // Server contract (engine_records.go): limit <= 0 -> SQLite LIMIT -1 = unlimited.
  const limit = limitCandidate > 0 ? Math.floor(limitCandidate) : -1;
  return {
    profile,
    sql: buildEngineEpochProfileSql(profile, options.spec),
    // The unified view's _source column carries the FULL shadow-table name
    // ("OMM@celestrak-gp"), not the bare source (server mirror).
    params: [source ? `${options.spec.tableName}@${source}` : '', epoch, limit],
  };
}

function defaultNowSeconds(): number {
  return Date.now() / 1000;
}

function firstNonEmptyString(...values: Array<string | null | undefined>): string | null {
  for (const value of values) {
    const trimmed = typeof value === 'string' ? value.trim() : '';
    if (trimmed) return trimmed;
  }
  return null;
}

function firstFiniteNumber(...values: Array<number | null | undefined>): number | null {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
  }
  return null;
}

// ---------------------------------------------------------------------------
// Control-path profile SQL (webUI / LLM query context) — promoted unchanged
// from src/ui/runtime/epoch-query-sql.ts (loop D.2).
// ---------------------------------------------------------------------------

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
