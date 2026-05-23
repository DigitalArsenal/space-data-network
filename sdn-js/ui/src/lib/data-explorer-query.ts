export interface DataExplorerQueryResultLike {
  columns: string[];
  rows: unknown[][];
  records: Array<Record<string, unknown>>;
}

export interface LocalDataExplorerQueryRequest {
  standardId: string;
  page: number;
  pageSize: number;
  searchText?: string;
  searchColumns?: string[];
  columnFilters?: Record<string, string>;
  filterColumns?: string[];
}

export interface LocalDataExplorerQuery {
  rowsSql: string;
  countSql: string;
  hasDatasetFilters: boolean;
}

type NumericFilter =
  | { kind: 'comparison'; operator: NumericComparisonOperator; value: number }
  | { kind: 'between'; min: number; max: number };

type NumericComparisonOperator = '=' | '!=' | '<' | '<=' | '>' | '>=';

const NUMERIC_PATTERN = String.raw`[+-]?(?:(?:\d+(?:\.\d+)?)|(?:\.\d+))(?:[eE][+-]?\d+)?`;
const NUMERIC_VALUE_REGEX = new RegExp(`^${NUMERIC_PATTERN}$`);
const NUMERIC_COMPARISON_REGEX = new RegExp(`^(<=|>=|!=|<>|=|<|>)\\s*(${NUMERIC_PATTERN})$`, 'i');
const NUMERIC_RANGE_REGEX = new RegExp(`^(${NUMERIC_PATTERN})\\s*(?:\\.\\.|\\.\\.\\.|to)\\s*(${NUMERIC_PATTERN})$`, 'i');
const DATE_TIME_RANGE_SEPARATOR = '..';
const DATE_TIME_FILTER_REGEX = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})(?::(\d{2})(?:\.\d{1,3})?)?Z?$/;

const NUMERIC_DATA_EXPLORER_COLUMNS = new Set([
  'AP1',
  'AP2',
  'AP3',
  'AP4',
  'AP5',
  'AP6',
  'AP7',
  'AP8',
  'AP_AVG',
  'APOGEE',
  'ARG_OF_PERICENTER',
  'BSTAR',
  'BSRN',
  'C9',
  'CCSDS_OMM_VERS',
  'CP',
  'DRAG_AREA',
  'DRAG_COEFF',
  'ECCENTRICITY',
  'ELEMENT_SET_NO',
  'F107_ADJ',
  'F107_ADJ_CENTER81',
  'F107_ADJ_LAST81',
  'F107_OBS',
  'F107_OBS_CENTER81',
  'F107_OBS_LAST81',
  'GM',
  'INCLINATION',
  'ISN',
  'KP1',
  'KP2',
  'KP3',
  'KP4',
  'KP5',
  'KP6',
  'KP7',
  'KP8',
  'KP_SUM',
  'MASS',
  'MEAN_ANOMALY',
  'MEAN_MOTION',
  'MEAN_MOTION_DDOT',
  'MEAN_MOTION_DOT',
  'ND',
  'NORAD_CAT_ID',
  'PERIGEE',
  'PERIOD',
  'RA_OF_ASC_NODE',
  'RCS',
  'REV_AT_EPOCH',
  'SEMI_MAJOR_AXIS',
  'SIZE',
  'SOLAR_RAD_AREA',
  'SOLAR_RAD_COEFF',
  'USER_DEFINED_BIP_0044_TYPE',
  'USER_DEFINED_EPOCH_TIMESTAMP',
  'USER_DEFINED_MICROSECONDS',
]);

const DEFAULT_TEXT_SEARCH_COLUMNS: Record<string, string[]> = {
  CAT: [
    'OBJECT_NAME',
    'OBJECT_ID',
    'NORAD_CAT_ID',
    'OBJECT_TYPE',
    'OPS_STATUS_CODE',
    'OWNER',
    'LAUNCH_SITE',
    'ORBIT_TYPE',
  ],
  EPM: [
    'dn',
    'legal_name',
    'family_name',
    'given_name',
    'additional_name',
    'job_title',
    'occupation',
    'email',
    'entity_type',
    'directory_kind',
    'peer_id',
    'signing_public_key',
    'encryption_public_key',
    'alternate_names',
    'multiformat_address',
  ],
  OMM: [
    'OBJECT_NAME',
    'OBJECT_ID',
    'NORAD_CAT_ID',
    'CLASSIFICATION_TYPE',
    'ORIGINATOR',
    'CENTER_NAME',
    'MEAN_ELEMENT_THEORY',
    'TIME_SYSTEM',
    'EPHEMERIS_TYPE',
  ],
  PNM: [
    'FILE_ID',
    'CID',
    'FILE_NAME',
    'MULTIFORMAT_ADDRESS',
    'SIGNATURE_TYPE',
    'TIMESTAMP_SIGNATURE_TYPE',
  ],
};

const DEFAULT_METADATA_SEARCH_COLUMNS = [
  'schemaName',
  'cid',
  'peerId',
  'providerId',
  'sourceName',
  'batchId',
  'timestamp',
];

export function buildLocalDataExplorerQuery(request: LocalDataExplorerQueryRequest): LocalDataExplorerQuery {
  const tableName = quoteSqlIdentifier(standardTableName(request.standardId));
  const pageSize = normalizePositiveInteger(request.pageSize, 10, 100);
  const page = normalizeNonNegativeInteger(request.page);
  const filters = buildDatasetFilterSql(request);
  const whereSql = filters.length > 0 ? ` WHERE ${filters.join(' AND ')}` : '';

  return {
    rowsSql: `SELECT * FROM ${tableName}${whereSql} LIMIT ${pageSize} OFFSET ${page * pageSize}`,
    countSql: `SELECT COUNT(*) AS __total FROM ${tableName}${whereSql}`,
    hasDatasetFilters: filters.length > 0,
  };
}

export function localDataExplorerCountFromResult(result: DataExplorerQueryResultLike | null | undefined): number | null {
  const record = result?.records[0];
  const row = result?.rows[0];
  if (record) {
    for (const key of ['__total', 'COUNT(*)', 'count(*)', 'COUNT', 'count']) {
      const count = normalizedCount(record[key]);
      if (count !== null) return count;
    }
    for (const value of Object.values(record)) {
      const count = normalizedCount(value);
      if (count !== null) return count;
    }
  }
  if (row) {
    for (const value of row) {
      const count = normalizedCount(value);
      if (count !== null) return count;
    }
  }
  return null;
}

export function isNumericDataExplorerColumn(column: string): boolean {
  return NUMERIC_DATA_EXPLORER_COLUMNS.has(column.trim().toUpperCase());
}

export function isEpochDataExplorerColumn(column: string): boolean {
  return column.trim().toUpperCase() === 'EPOCH';
}

export function localDataExplorerSearchColumns(standardId: string, availableColumns: string[] = []): string[] {
  const standardColumns = DEFAULT_TEXT_SEARCH_COLUMNS[standardTableName(standardId)] ?? [];
  const available = uniqueStrings(availableColumns);
  const availableSet = new Set(available);
  const defaultColumns = [...standardColumns, ...DEFAULT_METADATA_SEARCH_COLUMNS];
  const selectedDefaults = availableSet.size > 0
    ? defaultColumns.filter((column) => availableSet.has(column))
    : defaultColumns;
  const additionalTextColumns = available.filter((column) => isTextSearchColumnCandidate(column) && !defaultColumns.includes(column));
  return uniqueStrings([...selectedDefaults, ...additionalTextColumns]);
}

export function parseNumericColumnFilter(value: string): NumericFilter | null {
  const trimmed = value.trim();
  if (!trimmed) return null;

  const rangeMatch = trimmed.match(NUMERIC_RANGE_REGEX);
  if (rangeMatch) {
    const left = Number(rangeMatch[1]);
    const right = Number(rangeMatch[2]);
    if (!Number.isFinite(left) || !Number.isFinite(right)) return null;
    return { kind: 'between', min: Math.min(left, right), max: Math.max(left, right) };
  }

  const comparisonMatch = trimmed.match(NUMERIC_COMPARISON_REGEX);
  if (comparisonMatch) {
    const operator = comparisonMatch[1] === '<>' ? '!=' : comparisonMatch[1];
    const numeric = Number(comparisonMatch[2]);
    if (!isNumericOperator(operator) || !Number.isFinite(numeric)) return null;
    return { kind: 'comparison', operator, value: numeric };
  }

  if (NUMERIC_VALUE_REGEX.test(trimmed)) {
    const numeric = Number(trimmed);
    if (Number.isFinite(numeric)) return { kind: 'comparison', operator: '=', value: numeric };
  }

  return null;
}

function buildDatasetFilterSql(request: LocalDataExplorerQueryRequest): string[] {
  const filters: string[] = [];
  const searchCondition = plaintextSearchSql(request.searchText ?? '', request.searchColumns ?? []);
  if (searchCondition) filters.push(searchCondition);

  const activeFilterColumns = new Set((request.filterColumns ?? []).filter(Boolean));
  for (const [column, value] of Object.entries(request.columnFilters ?? {})) {
    const trimmed = value.trim();
    if (!trimmed) continue;
    if (activeFilterColumns.size > 0 && !activeFilterColumns.has(column)) continue;
    filters.push(columnFilterSql(column, trimmed));
  }
  return filters;
}

function plaintextSearchSql(value: string, columns: string[]): string | null {
  const trimmed = value.trim();
  if (!trimmed) return null;
  const searchColumns = uniqueStrings(columns).filter(Boolean);
  if (searchColumns.length === 0) return null;
  const pattern = `%${escapeSqlLikePattern(trimmed)}%`;
  const conditions = searchColumns.map((column) => `${textColumnExpression(column)} LIKE '${pattern}' ESCAPE '\\'`);
  return `(${conditions.join(' OR ')})`;
}

function columnFilterSql(column: string, value: string): string {
  if (isEpochDataExplorerColumn(column)) {
    const epochSql = epochColumnFilterSql(column, value);
    if (epochSql) return epochSql;
  }
  if (isNumericDataExplorerColumn(column)) {
    const numericSql = numericColumnFilterSql(column, value);
    if (numericSql) return numericSql;
  }
  return `${textColumnExpression(column)} LIKE '%${escapeSqlLikePattern(value)}%' ESCAPE '\\'`;
}

function numericColumnFilterSql(column: string, value: string): string | null {
  const filter = parseNumericColumnFilter(value);
  if (!filter) return null;
  const expression = `CAST(${quoteSqlIdentifier(column)} AS REAL)`;
  if (filter.kind === 'between') {
    return `${expression} BETWEEN ${formatSqlNumber(filter.min)} AND ${formatSqlNumber(filter.max)}`;
  }
  return `${expression} ${filter.operator} ${formatSqlNumber(filter.value)}`;
}

function epochColumnFilterSql(column: string, value: string): string | null {
  const [rawStart = '', rawStop = ''] = value.split(DATE_TIME_RANGE_SEPARATOR);
  const start = normalizeDateTimeFilterLiteral(rawStart);
  const stop = normalizeDateTimeFilterLiteral(rawStop);
  const expression = textColumnExpression(column);
  const conditions: string[] = [];
  if (start) conditions.push(`${expression} >= '${start}'`);
  if (stop) conditions.push(`${expression} < '${stop}'`);
  if (conditions.length === 0) return null;
  return conditions.length === 1 ? conditions[0] : `(${conditions.join(' AND ')})`;
}

function textColumnExpression(column: string): string {
  return `CAST(${quoteSqlIdentifier(column)} AS TEXT)`;
}

function isTextSearchColumnCandidate(column: string): boolean {
  const normalized = column.trim();
  if (!normalized || normalized.startsWith('_')) return false;
  if (isNumericDataExplorerColumn(normalized)) return false;
  return !['dataBytes', 'data_base64', 'bytes', 'sizeBytes'].includes(normalized);
}

function quoteSqlIdentifier(value: string): string {
  return `"${value.replace(/"/g, '""')}"`;
}

function escapeSqlLikePattern(value: string): string {
  return value
    .replace(/\\/g, '\\\\')
    .replace(/%/g, '\\%')
    .replace(/_/g, '\\_')
    .replace(/'/g, "''");
}

function normalizeDateTimeFilterLiteral(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return '';
  const match = trimmed.match(DATE_TIME_FILTER_REGEX);
  if (!match) return '';
  return `${match[1]}T${match[2]}:${match[3] ?? '00'}.000Z`;
}

function standardTableName(standardId: string): string {
  return standardId.trim().split('.')[0]?.toUpperCase() || 'EPM';
}

function uniqueStrings(values: string[]): string[] {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean)));
}

function normalizePositiveInteger(value: number, fallback: number, max: number): number {
  const numeric = Math.floor(Number(value));
  if (!Number.isFinite(numeric) || numeric <= 0) return fallback;
  return Math.max(1, Math.min(max, numeric));
}

function normalizeNonNegativeInteger(value: number): number {
  const numeric = Math.floor(Number(value));
  if (!Number.isFinite(numeric) || numeric < 0) return 0;
  return numeric;
}

function normalizedCount(value: unknown): number | null {
  const numeric = Number(value);
  if (!Number.isFinite(numeric) || numeric < 0) return null;
  return Math.floor(numeric);
}

function formatSqlNumber(value: number): string {
  return String(value);
}

function isNumericOperator(value: string): value is NumericComparisonOperator {
  return value === '=' || value === '!=' || value === '<' || value === '<=' || value === '>' || value === '>=';
}
