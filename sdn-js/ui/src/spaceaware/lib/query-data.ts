/**
 * Real daemon-surface wiring for the DATA tab (loop task U3.6 — the local
 * FlatSQL query workbench inside the STANDARDS detail panel). Ground truth:
 * the `<!-- sc-if value="{{ isDataTab }}" -->` block in
 * `design_handoff/sdn_console/SDN Console.dc.html` — kicker "LOCAL FLATSQL ·
 * QUERY OUTPUT", a right-aligned TABLE/JSON/CSV toggle, and a scrollable
 * mono output block (same code-block styling as the EXPLORER tab's IDL
 * block). See `DataStandardsExplorer.svelte`'s doc comment for the
 * pixel-level styling port.
 *
 * Endpoints probed live on this build (NOT the mock's fabricated
 * `STANDARDS.map(...)` fixture, which pretends every standard has decoded
 * `rows`/`label`/`provider`/`state` columns):
 *
 *   1. `POST /api/v1/query` (the G.5 decoded-row query engine,
 *      `sdn-server/internal/api/docs.go:702` — `plannedIn: "G.5"`) does NOT
 *      exist yet: an authenticated request 404s. This module never assumes
 *      it stays missing forever — `resolveQueryEngine` probes it ONCE per
 *      DATA-tab activation (not once per keystroke/standard switch) with
 *      the real `{sql}` call shape a future G.5 will accept, and
 *      `loadQueryTabData` only reuses that cached availability rather than
 *      re-probing on every standard switch. The moment G.5 ships, this
 *      component starts rendering DECODED rows (`NORAD_CAT_ID`-style
 *      columns) with zero code changes beyond whatever G.5's real response
 *      envelope turns out to be (`parseDecodedQueryRows` best-effort-reads a
 *      handful of conventional envelope shapes so it doesn't crash the
 *      instant G.5 appears, but its exact shape is unverifiable today).
 *   2. `POST /api/v1/data/query` (`sdn-server/internal/api/data.go:159`
 *      `handleRawQuery`, Admin-trust-gated —
 *      `cmd/spacedatanetwork/main.go:1998` `isAdminOnlyAPIPath`) is the REAL
 *      fallback used until G.5 ships. Body `{"schema":"<CODE>.fbs","limit":
 *      N,"offset":N}` — the `.fbs` suffix is REQUIRED (`"EPM"` alone returns
 *      `count:0`; `"EPM.fbs"` returns rows — `handleRawQuery` does a literal
 *      schema-name match, no suffix-stripping). Response:
 *      `{"schema":"EPM.fbs","count":N,"results":[...]}` where each result is
 *      a RECORD-METADATA object (`rawRecordRow`, data.go:745) —
 *      `schema_name`/`cid`/`peer_id`/`timestamp`/`size_bytes`/
 *      `flatbuffer_uri` always present, `provider_id`/`source_name`/
 *      `batch_id`/`source_url`/`content_key_id`/`producer_peer_id`/
 *      `producer_public_key` only present when non-empty server-side (never
 *      a fabricated blank column). There is NO decoded-field surface here —
 *      the raw FlatBuffer payload itself is fetched separately via
 *      `flatbuffer_uri` (`/api/v1/data/records/{schema}/{cid}`), never
 *      decoded client-side by this module.
 *
 * SCHEMA-EXACT KEY RULE (loop task spec): every render path (TABLE columns,
 * JSON pretty-print, CSV header) passes each row's keys through completely
 * unmodified — no camelCase/snake_case normalization, no renaming. This is
 * deliberate: it's the same code path a future G.5 decoded row (with
 * `NORAD_CAT_ID`/`OBJECT_NAME`-style keys) will flow through, so proving the
 * passthrough is verbatim today (via a synthetic fixture in the test file)
 * is what makes the G.5 cutover a zero-code-change event.
 */

import { SdnApiError, type SdnApiClient } from '../../lib/auth/sdn-api-client';

// ---------------------------------------------------------------------------
// Small JSON helpers (mirrors standards-data.ts/peers-data.ts's private
// helpers — not exported from there, so duplicated narrowly here, same
// rationale as those files' own doc comments).
// ---------------------------------------------------------------------------

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function pickString(record: Record<string, unknown>, key: string): string | null {
  const value = record[key];
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  return trimmed ? trimmed : null;
}

function pickNumber(record: Record<string, unknown>, key: string): number | null {
  const value = record[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

// ---------------------------------------------------------------------------
// TABLE / JSON / CSV output-mode toggle (styling-only, matches
// `standards-data.ts`'s `standardDetailTabStyle`/`dataViewToggleStyle`
// convention)
// ---------------------------------------------------------------------------

export type QueryOutputMode = 'table' | 'json' | 'csv';

export interface QueryOutputModeSpec {
  id: QueryOutputMode;
  label: string;
}

/** Verbatim `['table','json','csv']` order from the mock's `dataModes`. */
export const QUERY_OUTPUT_MODES: readonly QueryOutputModeSpec[] = [
  { id: 'table', label: 'TABLE' },
  { id: 'json', label: 'JSON' },
  { id: 'csv', label: 'CSV' },
];

/** The mock's `state.dataMode` initial value (`'json'`) — JSON is the default active mode. */
export const QUERY_DEFAULT_MODE: QueryOutputMode = 'json';

export interface QueryOutputModeStyle {
  background: string;
  border: string;
  color: string;
}

/** Port of the mock's `dataModes` button styling (line ~1143). */
export function queryOutputModeStyle(id: QueryOutputMode, active: QueryOutputMode): QueryOutputModeStyle {
  const isActive = id === active;
  return {
    background: isActive ? 'rgba(74,166,224,0.18)' : 'transparent',
    border: isActive ? 'rgba(120,190,230,0.55)' : 'rgba(90,150,180,0.28)',
    color: isActive ? '#9fd4f5' : '#7d929b',
  };
}

// ---------------------------------------------------------------------------
// Row limit / schema-name contract
// ---------------------------------------------------------------------------

/** "run the query on tab activation (limit 50)" — loop task spec. */
export const QUERY_ROW_LIMIT = 50;

/**
 * `CAT` → `CAT.fbs`. CRITICAL (see file doc comment #2): `/api/v1/data/query`
 * does a literal schema-name match — omitting `.fbs` silently returns
 * `count:0` instead of a 400, so this is the one place that suffix gets
 * appended; every caller must route through here rather than passing
 * `entry.code` straight into the request body.
 */
export function metadataQuerySchemaName(code: string): string {
  return `${code.trim().toUpperCase()}.fbs`;
}

export function metadataQueryRequestBody(
  code: string,
  limit: number = QUERY_ROW_LIMIT,
  offset = 0,
): { schema: string; limit: number; offset: number } {
  return { schema: metadataQuerySchemaName(code), limit, offset };
}

// ---------------------------------------------------------------------------
// Raw response parser: POST /api/v1/data/query
// ---------------------------------------------------------------------------

export interface MetadataQueryResult {
  schema: string | null;
  count: number;
  rows: Record<string, unknown>[];
}

/**
 * Parses `{"schema":"EPM.fbs","count":N,"results":[...]}`. Each entry in
 * `results` is kept as a plain object with its keys UNTOUCHED (see file doc
 * comment's "SCHEMA-EXACT KEY RULE") — this never reshapes `batch_id` into
 * `batchId` or drops an unrecognized field.
 */
export function parseMetadataQueryResponse(payload: unknown): MetadataQueryResult {
  const rec = isPlainRecord(payload) ? payload : {};
  const list = Array.isArray(rec.results) ? rec.results : [];
  const rows = list.filter(isPlainRecord);
  return {
    schema: pickString(rec, 'schema'),
    count: pickNumber(rec, 'count') ?? rows.length,
    rows,
  };
}

// ---------------------------------------------------------------------------
// G.5 decoded-row engine adapter (feature-detected, not assumed present —
// see file doc comment #1)
// ---------------------------------------------------------------------------

/**
 * `SELECT * FROM "<CODE>" LIMIT <limit> OFFSET <offset>` — the `{sql}` call
 * shape G.5 is expected to accept (mirrors the pre-existing, already-shipped
 * `ui/src/lib/data-explorer-query.ts` SQL builder used by the older upstream
 * webui overlay's own local query workbench, so this isn't a guessed
 * convention). Table identifier is double-quote-escaped defensively even
 * though standard codes are a closed, already-validated vocabulary.
 */
export function buildDecodedQuerySql(code: string, limit: number = QUERY_ROW_LIMIT, offset = 0): string {
  const identifier = `"${code.trim().toUpperCase().replace(/"/g, '""')}"`;
  return `SELECT * FROM ${identifier} LIMIT ${limit} OFFSET ${offset}`;
}

/**
 * Best-effort row extraction from a hypothetical G.5 response. Since G.5
 * doesn't exist on this build, its real envelope is unverifiable — this
 * checks a handful of conventional shapes (a bare array, or an object
 * carrying `rows`/`records`/`results`) so the adapter doesn't crash the
 * instant G.5 appears; whichever shape turns out to be real, `loadQueryTabData`
 * still renders it through the same schema-exact TABLE/JSON/CSV builders
 * below, so decoded rows load-bear zero additional client code.
 */
export function parseDecodedQueryRows(payload: unknown): Record<string, unknown>[] {
  if (Array.isArray(payload)) return payload.filter(isPlainRecord);
  if (isPlainRecord(payload)) {
    for (const key of ['rows', 'records', 'results']) {
      const value = payload[key];
      if (Array.isArray(value)) return value.filter(isPlainRecord);
    }
  }
  return [];
}

export type QueryEngineAvailability = 'unknown' | 'available' | 'unavailable';

/** Structural subset of `SdnApiClient` this module needs — lets tests pass a plain fake instead of constructing a real client. */
export type QueryApiClient = Pick<SdnApiClient, 'requestJson'>;

/**
 * Probes `POST /api/v1/query` ONCE (call this on DATA-tab activation, cache
 * the result in component state, and pass it into `loadQueryTabData` for
 * every subsequent standard switch — never re-probe per keystroke). A 404
 * (the real, expected response on this build) resolves to `'unavailable'`
 * silently — this is a confirmed gap, not an error worth surfacing to the
 * user. Any other failure (network error, 401/403/5xx) ALSO resolves to
 * `'unavailable'` rather than getting stuck retrying: a flaky/broken G.5 on
 * a future build should fail open to the honest metadata surface, not block
 * the DATA tab.
 */
export async function resolveQueryEngine(apiClient: QueryApiClient, probeSql: string): Promise<QueryEngineAvailability> {
  try {
    await apiClient.requestJson<unknown>('/query', { method: 'POST', body: { sql: probeSql } });
    return 'available';
  } catch {
    return 'unavailable';
  }
}

// ---------------------------------------------------------------------------
// Fetch orchestration
// ---------------------------------------------------------------------------

export type QueryTabEngine = 'g5' | 'metadata';
export type QueryTabErrorKind = 'unauthenticated' | 'forbidden' | 'other';

export const QUERY_SIGN_IN_MESSAGE = 'Sign in with a node session to query the local FlatSQL store.';
export const QUERY_FORBIDDEN_MESSAGE = 'Insufficient trust level to query the local FlatSQL store — an admin session is required.';

export interface QueryTabResult {
  engine: QueryTabEngine;
  rows: Record<string, unknown>[];
  /** The server's own echoed schema name (e.g. `"EPM.fbs"`) — `null` for the `'g5'` engine (no such field in that hypothetical response) or on error. */
  schema: string | null;
  /** Server-reported total row count for this query — `null` on error. For the `'g5'` engine this is `rows.length` (no separate count field exists in any hypothetical envelope this adapter recognizes). */
  count: number | null;
  errorKind: QueryTabErrorKind | null;
  errorMessage: string | null;
}

function queryErrorResult(engine: QueryTabEngine, err: unknown): QueryTabResult {
  if (err instanceof SdnApiError) {
    if (err.status === 401) {
      return { engine, rows: [], schema: null, count: null, errorKind: 'unauthenticated', errorMessage: QUERY_SIGN_IN_MESSAGE };
    }
    if (err.status === 403) {
      return { engine, rows: [], schema: null, count: null, errorKind: 'forbidden', errorMessage: QUERY_FORBIDDEN_MESSAGE };
    }
    return {
      engine,
      rows: [],
      schema: null,
      count: null,
      errorKind: 'other',
      errorMessage: err.body?.message || err.message || `Query failed (HTTP ${err.status}).`,
    };
  }
  return { engine, rows: [], schema: null, count: null, errorKind: 'other', errorMessage: 'Query failed — network error.' };
}

/** Queries the real metadata fallback (`POST /api/v1/data/query`). Never throws — every failure mode (401/403/other/network) resolves to an honest `QueryTabResult` with `errorKind` set instead. */
export async function fetchRecordMetadataRows(
  apiClient: QueryApiClient,
  code: string,
  limit: number = QUERY_ROW_LIMIT,
  offset = 0,
): Promise<QueryTabResult> {
  try {
    const result = await apiClient.requestJson<unknown>('/data/query', {
      method: 'POST',
      body: metadataQueryRequestBody(code, limit, offset),
    });
    const parsed = parseMetadataQueryResponse(result.data);
    return { engine: 'metadata', rows: parsed.rows, schema: parsed.schema, count: parsed.count, errorKind: null, errorMessage: null };
  } catch (err) {
    return queryErrorResult('metadata', err);
  }
}

/**
 * Queries the G.5 decoded-row engine directly (no re-probing — call this
 * only when `resolveQueryEngine` already returned `'available'` for this
 * DATA-tab activation).
 */
export async function fetchDecodedQueryRows(
  apiClient: QueryApiClient,
  code: string,
  limit: number = QUERY_ROW_LIMIT,
  offset = 0,
): Promise<QueryTabResult> {
  try {
    const sql = buildDecodedQuerySql(code, limit, offset);
    const result = await apiClient.requestJson<unknown>('/query', { method: 'POST', body: { sql } });
    const rows = parseDecodedQueryRows(result.data);
    return { engine: 'g5', rows, schema: null, count: rows.length, errorKind: null, errorMessage: null };
  } catch (err) {
    return queryErrorResult('g5', err);
  }
}

/**
 * Top-level DATA-tab query orchestrator. `engineAvailability` is the cached
 * result of one `resolveQueryEngine` probe for the current DATA-tab
 * activation (see that function's doc comment) — passing `'available'`
 * queries G.5 directly; anything else (`'unknown'`/`'unavailable'`) goes
 * straight to the real metadata fallback. If G.5 was reported available but
 * this particular call still fails, it falls back to the metadata surface
 * rather than showing an error for something the user didn't cause.
 */
export async function loadQueryTabData(
  apiClient: QueryApiClient,
  code: string,
  engineAvailability: QueryEngineAvailability,
  limit: number = QUERY_ROW_LIMIT,
  offset = 0,
): Promise<QueryTabResult> {
  if (engineAvailability === 'available') {
    const g5Result = await fetchDecodedQueryRows(apiClient, code, limit, offset);
    if (g5Result.errorKind === null) return g5Result;
  }
  return fetchRecordMetadataRows(apiClient, code, limit, offset);
}

// ---------------------------------------------------------------------------
// View-model builders: TABLE columns / JSON / CSV (schema-exact passthrough)
// ---------------------------------------------------------------------------

/** Union of every row's own keys, in first-seen order — real metadata rows carry a common core plus optional fields (`batch_id`, `provider_id`, ...) only when the server actually has them, so a union (not just `rows[0]`'s keys) is the only way to never silently drop a later row's column. */
export function buildQueryTableColumns(rows: readonly Record<string, unknown>[]): string[] {
  const seen = new Set<string>();
  const columns: string[] = [];
  for (const row of rows) {
    for (const key of Object.keys(row)) {
      if (!seen.has(key)) {
        seen.add(key);
        columns.push(key);
      }
    }
  }
  return columns;
}

/** Cell text for the TABLE mode — scalars render as-is, nested objects/arrays fall back to compact JSON rather than `"[object Object]"`. */
export function queryTableCellText(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

/** JSON mode: the rows array, pretty-printed verbatim — same object identity/keys the server sent. */
export function buildQueryJsonOutput(rows: readonly Record<string, unknown>[]): string {
  return JSON.stringify(rows, null, 2);
}

function csvField(value: string): string {
  return /[",\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value;
}

/** CSV mode: header row from `buildQueryTableColumns`, one row per record, same schema-exact keys as the header. Empty input renders `''` (no header-only CSV for zero rows — the empty state message covers that case in the view). */
export function buildQueryCsvOutput(rows: readonly Record<string, unknown>[]): string {
  if (rows.length === 0) return '';
  const columns = buildQueryTableColumns(rows);
  const header = columns.map(csvField).join(',');
  const body = rows.map((row) => columns.map((col) => csvField(queryTableCellText(row[col]))).join(','));
  return [header, ...body].join('\n');
}

// ---------------------------------------------------------------------------
// Empty / caption copy
// ---------------------------------------------------------------------------

export const QUERY_LOADING_LABEL = 'RUNNING QUERY…';

/** Honest "no rows" panel per the loop task spec (`"NO ROWS STORED FOR <CODE>"`). */
export function queryEmptyStateLabel(code: string, loaded: boolean, rowCount: number): string {
  if (!loaded) return QUERY_LOADING_LABEL;
  if (rowCount === 0) return `NO ROWS STORED FOR ${code.trim().toUpperCase()}`;
  return '';
}

export const QUERY_METADATA_CAPTION = 'record metadata · decoded field queries land with /api/v1/query (G.5)';
export const QUERY_G5_CAPTION = 'decoded rows · local FlatSQL query engine';

/** Dim caption line under the kicker — honestly distinguishes "this is metadata, not decoded fields" from the (currently hypothetical) G.5 decoded-row path. */
export function queryEngineCaption(engine: QueryTabEngine): string {
  return engine === 'g5' ? QUERY_G5_CAPTION : QUERY_METADATA_CAPTION;
}
