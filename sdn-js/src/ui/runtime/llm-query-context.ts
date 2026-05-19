import { EPOCH_SQL_PROFILE_DESCRIPTIONS, EPOCH_SQL_PROFILES } from './epoch-query-sql';
import { semanticDatasetSummaries, type SemanticDatasetSummary } from './semantic-datasets';

export interface LocalLlmQuerySourceIdentity {
  dataSourceId: string;
  datastoreKey?: string | null;
  providerName: string;
  providerPeerId?: string | null;
  providerPublicKey?: string | null;
  providerId?: string | null;
  sourceName?: string | null;
}

export interface LocalLlmQuerySchemaContext {
  standardId: string;
  schemaName: string;
  tableName: string;
  columns: string[];
}

export interface LocalLlmQueryProfileContext {
  id: string;
  label: string;
  description: string;
}

export interface LocalLlmQueryLimits {
  maxRows: number;
  maxBytes: number;
  timeoutMs: number;
}

export interface LocalLlmQueryContext {
  schema: LocalLlmQuerySchemaContext;
  source: LocalLlmQuerySourceIdentity;
  queryProfile: string;
  queryProfiles: LocalLlmQueryProfileContext[];
  semanticDatasets: SemanticDatasetSummary[];
  sampleRows: Array<Record<string, unknown>>;
  limits: LocalLlmQueryLimits;
}

export interface LocalLlmQueryContextInput {
  standardId: string;
  schemaName: string;
  tableName: string;
  columns: string[];
  source: LocalLlmQuerySourceIdentity;
  queryProfile: string;
  sampleRows: Array<Record<string, unknown>>;
  maxRows?: number | null;
  maxBytes?: number | null;
  timeoutMs?: number | null;
}

export interface LocalLlmQueryRequest {
  ask: string;
  context: LocalLlmQueryContext;
}

export interface LocalLlmQueryDraft {
  sql: string;
  rationale: string;
}

const SAMPLE_ROW_LIMIT = 10;
const DEFAULT_MAX_ROWS = 100;
const DEFAULT_MAX_BYTES = 64_000;
const DEFAULT_TIMEOUT_MS = 5_000;
const INTERNAL_COLUMN_NAMES = new Set(['bytes', 'dataBytes', 'dataBase64', 'data_base64', '_data', '_offset', '_source', '_rowid']);

export function buildLocalLlmQueryContext(input: LocalLlmQueryContextInput): LocalLlmQueryContext {
  const columns = input.columns.filter(isLlmVisibleColumn);
  return {
    schema: {
      standardId: normalizeStandardId(input.standardId),
      schemaName: input.schemaName.trim(),
      tableName: input.tableName.trim(),
      columns,
    },
    source: normalizeSourceIdentity(input.source),
    queryProfile: input.queryProfile.trim(),
    queryProfiles: queryProfilesForStandard(input.standardId),
    semanticDatasets: semanticDatasetSummaries(),
    sampleRows: input.sampleRows.slice(0, SAMPLE_ROW_LIMIT).map((row) => sanitizeSampleRow(row, columns)),
    limits: {
      maxRows: normalizeLimit(input.maxRows, DEFAULT_MAX_ROWS),
      maxBytes: normalizeLimit(input.maxBytes, DEFAULT_MAX_BYTES),
      timeoutMs: normalizeLimit(input.timeoutMs, DEFAULT_TIMEOUT_MS),
    },
  };
}

function queryProfilesForStandard(standardId: string): LocalLlmQueryProfileContext[] {
  if (normalizeStandardId(standardId) !== 'OMM') return [];
  return EPOCH_SQL_PROFILES.map((profile) => ({
    id: profile.id,
    label: profile.label,
    description: EPOCH_SQL_PROFILE_DESCRIPTIONS[profile.id],
  }));
}

function sanitizeSampleRow(row: Record<string, unknown>, columns: string[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const column of columns) {
    const value = row[column];
    if (!isLlmVisibleValue(value)) continue;
    out[column] = value;
  }
  return out;
}

function isLlmVisibleColumn(column: string): boolean {
  const normalized = column.trim();
  if (!normalized) return false;
  if (normalized.startsWith('_')) return false;
  if (INTERNAL_COLUMN_NAMES.has(normalized)) return false;
  return !/signature|private|secret|bytes|base64/i.test(normalized);
}

function isLlmVisibleValue(value: unknown): boolean {
  if (value == null) return false;
  if (value instanceof Uint8Array || value instanceof ArrayBuffer) return false;
  if (typeof value === 'string') return value.length <= 256;
  return typeof value === 'number' || typeof value === 'boolean';
}

function normalizeSourceIdentity(source: LocalLlmQuerySourceIdentity): LocalLlmQuerySourceIdentity {
  return {
    dataSourceId: source.dataSourceId.trim(),
    datastoreKey: optionalString(source.datastoreKey),
    providerName: source.providerName.trim(),
    providerPeerId: optionalString(source.providerPeerId),
    providerPublicKey: optionalString(source.providerPublicKey),
    providerId: optionalString(source.providerId),
    sourceName: optionalString(source.sourceName),
  };
}

function normalizeStandardId(value: string): string {
  return value.trim().split('.')[0]?.toUpperCase() ?? '';
}

function normalizeLimit(value: number | null | undefined, fallback: number): number {
  const numeric = Math.floor(Number(value));
  if (!Number.isFinite(numeric) || numeric <= 0) return fallback;
  return Math.max(1, Math.min(10_000, numeric));
}

function optionalString(value: string | null | undefined): string | null {
  const trimmed = value?.trim() ?? '';
  return trimmed || null;
}
