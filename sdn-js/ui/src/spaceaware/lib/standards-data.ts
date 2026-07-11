/**
 * Real daemon-surface wiring for the DATA console view (loop task U3.5 —
 * spacedatastandards.org standards + schema explorer). Ground truth: the
 * `<!-- ============ DATA ============ -->` block in
 * `design_handoff/sdn_console/SDN Console.dc.html` — a "spacedatastandards.org"
 * banner (wordmark + version chip + count + FLATSQL STORE status +
 * DATA STANDARDS/MODULES toggle), a STANDARDS left panel, and a right panel
 * with EXPLORER/GENERATE/DATA tabs. See `DataView.svelte`/
 * `DataStandardsExplorer.svelte` for the pixel-level styling port.
 *
 * Endpoints probed live on this build (NOT the mock's fabricated
 * `STANDARDS`/`SDS_VERSION` fixtures):
 *
 *   1. `GET /api/v1/channels` (PUBLIC, 200) —
 *      `{"count":173,"results":[{"standardCode":"ACL","topic":"...",
 *      "encryptionState":"none","grantState":"not-required",
 *      "subscribed":false,"visibility":"public"}, ...]}` — one entry per
 *      standard code. This IS the STANDARDS list; there is no
 *      `/api/v1/standards` endpoint (planned-only — an authenticated fetch
 *      404s on this build, a real server gap, not a client bug).
 *      `encryptionState` is a genuine two-value enum server-side
 *      (`sdn-server/internal/channels/metadata.go`): `"none"` or
 *      `"encrypted"` — never `"signed"` or anything else — so
 *      `standardIsEncrypted` below is a straight equality check, not a
 *      guess at an open-ended vocabulary.
 *   2. `GET /api/v1/stats` — reuses `node-data.ts`'s `parseNodeStats`
 *      (`{schemas:[{schema:"PRR.fbs",count,total_bytes}],...}`). The server
 *      (`FlatSQLStore.DataSummary`) only appends a schema entry when its
 *      `count > 0` — a standard with ZERO synced rows is simply ABSENT from
 *      the array, never present with `count:0`. That means "no stats entry"
 *      is not an unknown — it's a confirmed zero, so `joinStandardsWithStats`
 *      below renders an honest `0` rather than a `—` placeholder (documented
 *      here once, not re-litigated at each call site).
 *   3. `GET /api/node/info` — reuses `node-data.ts`'s `parseNodeInfo` for
 *      `standardsVersion` (`"1.136.0"` on this build — the real
 *      `spacedatastandards.org` package pin, NOT the mock's fabricated
 *      `v2026.02.05`). This is the banner's PACKAGE version chip; it is
 *      DISTINCT from each individual standard's own per-schema version
 *      (`FbsSchema.version`, parsed from that standard's vendored
 *      `main.fbs` `// Version:` header — see `standards-fbs.ts`), which is
 *      what the STANDARDS list rows and the selected-standard header chip
 *      show (the mock conflates these into one `STANDARDS[].version`
 *      fixture; the real data genuinely has two distinct version numbers).
 *
 * A standard's DISPLAY NAME (left panel sub-line, selected-standard header)
 * has no backing field in `/channels` or `/stats` either — the only honest
 * real source is the vendored schema's own leading `///` doc comment above
 * its root table (e.g. CAT's `/// Catalog Entity Message`). A channel whose
 * standard code has no vendored `.fbs` in this build's
 * `spacedatastandards.org` pin (a live node's channel list can list MORE
 * codes than a given UI build vendors) falls back to the code itself as its
 * label, `'—'` for its version, and an honest "no local schema" message in
 * the EXPLORER/GENERATE tabs — never a fabricated name.
 */

import type { SdnApiClient } from '../../lib/auth/sdn-api-client';
import { parseNodeInfo, parseNodeStats, type NodeInfoSnapshot, type NodeStatsSnapshot } from './node-data';
import { fbsFieldTypeColor, getVendoredSchema, type FbsSchema } from './standards-fbs';

// ---------------------------------------------------------------------------
// Small JSON helpers (mirrors node-data.ts/peers-data.ts's private
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

// ---------------------------------------------------------------------------
// Raw endpoint parser: GET /api/v1/channels
// ---------------------------------------------------------------------------

export interface RawChannel {
  standardCode: string;
  topic: string;
  /** Always `'none'` or `'encrypted'` on this build's server (see file doc comment) — kept as a string rather than a union so an unrecognized future value degrades instead of failing to typecheck against stale test fixtures. */
  encryptionState: string;
  grantState: string;
  subscribed: boolean;
  visibility: string;
}

/** Parses `GET /api/v1/channels`'s `{"count":N,"results":[...]}` envelope. Entries with no `standardCode` are dropped — there is nothing to key them by. Extra fields some rows carry (verified-publication metadata: `channelId`, `pnmVerified`, etc.) are simply ignored — this view only needs the 6 base fields every row has. */
export function parseChannelsResponse(payload: unknown): RawChannel[] {
  const rec = isPlainRecord(payload) ? payload : {};
  const list = Array.isArray(rec.results) ? rec.results : [];
  return list
    .filter(isPlainRecord)
    .map((c) => ({
      standardCode: pickString(c, 'standardCode') ?? '',
      topic: pickString(c, 'topic') ?? '',
      encryptionState: pickString(c, 'encryptionState') ?? 'none',
      grantState: pickString(c, 'grantState') ?? '',
      subscribed: c.subscribed === true,
      visibility: pickString(c, 'visibility') ?? '',
    }))
    .filter((c) => c.standardCode);
}

// ---------------------------------------------------------------------------
// Channel + stats join (STANDARDS list source of truth)
// ---------------------------------------------------------------------------

export interface StandardEntry {
  code: string;
  channel: RawChannel;
  /** Rows synced to the local FlatSQL store for this standard. `0` for a code absent from `/api/v1/stats` — a confirmed zero, not an unknown (see file doc comment). */
  rows: number;
}

/** Strips the schema fields' `.fbs` suffix (always present server-side) so `"PRR.fbs"` joins against channel `standardCode` `"PRR"`. */
function stripFbsSuffix(schemaName: string): string {
  return schemaName.replace(/\.fbs$/i, '');
}

/** Joins the real `/api/v1/channels` list against `/api/v1/stats`' per-schema row counts. `stats: null` (a failed/unreachable stats fetch, distinct from "fetch succeeded, array empty") still yields `rows: 0` for every entry — the STANDARDS list itself never blocks on stats; see `buildSchemaSyncBannerView` for the SEPARATE "did stats load at all" signal the banner needs. */
export function joinStandardsWithStats(channels: readonly RawChannel[], stats: NodeStatsSnapshot | null): StandardEntry[] {
  const rowsByCode = new Map<string, number>();
  for (const schema of stats?.schemas ?? []) {
    rowsByCode.set(stripFbsSuffix(schema.schema), schema.count);
  }
  return channels.map((channel) => ({
    code: channel.standardCode,
    channel,
    rows: rowsByCode.get(channel.standardCode) ?? 0,
  }));
}

/**
 * Sort order (loop task spec): standards WITH stored rows first, descending
 * by row count (ties broken alphabetically by code); standards with zero
 * rows after, alphabetically. Never mutates its input.
 */
export function sortStandardEntries(entries: readonly StandardEntry[]): StandardEntry[] {
  return entries.slice().sort((a, b) => {
    const aHas = a.rows > 0;
    const bHas = b.rows > 0;
    if (aHas !== bHas) return aHas ? -1 : 1;
    if (aHas && a.rows !== b.rows) return b.rows - a.rows;
    return a.code.localeCompare(b.code);
  });
}

// ---------------------------------------------------------------------------
// Encryption / padlock (MPE 🔒 badge — driven by the real channel state,
// never fabricated: today's live channels are all `encryptionState:'none'`,
// so nothing shows a padlock; the logic itself is exercised with a
// synthetic `encryptionState:'encrypted'` fixture in the test file)
// ---------------------------------------------------------------------------

export function standardIsEncrypted(channel: Pick<RawChannel, 'encryptionState'>): boolean {
  return channel.encryptionState === 'encrypted';
}

// ---------------------------------------------------------------------------
// Banner status ("FLATSQL STORE · SCHEMA SYNCED" — degrades honestly when
// the stats fetch itself failed, distinct from "succeeded with zero rows")
// ---------------------------------------------------------------------------

export interface SchemaSyncBannerView {
  label: string;
  dotColor: string;
}

export function buildSchemaSyncBannerView(statsLoaded: boolean): SchemaSyncBannerView {
  if (statsLoaded) return { label: 'FLATSQL STORE · SCHEMA SYNCED', dotColor: '#5ad6a0' };
  return { label: 'FLATSQL STORE · SYNC STATUS UNKNOWN', dotColor: '#7d929b' };
}

// ---------------------------------------------------------------------------
// Banner text: package version chip + standards count caption
// ---------------------------------------------------------------------------

/** `spacedatastandards.org` PACKAGE version chip (`node/info.standards_version`, prefixed `v`) — honest `'—'` when that field is unavailable, never the mock's fabricated `v2026.02.05`. */
export function formatSdsPackageVersionChip(standardsVersion: string | null | undefined): string {
  const v = (standardsVersion ?? '').trim();
  return v ? `v${v}` : '—';
}

export function formatStandardsCountCaption(count: number): string {
  return `${count} standard${count === 1 ? '' : 's'} · FlatBuffers IDL`;
}

// ---------------------------------------------------------------------------
// STANDARDS list rows (left panel)
// ---------------------------------------------------------------------------

/** Honest per-row status indicator: padlock for an encrypted channel, a green dot when rows are actually synced locally, a neutral gray dot when nothing has synced yet — never the mock's fabricated `fresh`/`stale` distinction, which has no backing surface. */
export function standardStatusIndicator(rows: number, encrypted: boolean): { glyph: string; color: string } {
  if (encrypted) return { glyph: '🔒', color: '#ffb24d' };
  if (rows > 0) return { glyph: '●', color: '#5ad6a0' };
  return { glyph: '●', color: '#5a7a8a' };
}

export interface StandardsListRowView {
  code: string;
  label: string;
  versionLabel: string;
  rowsLabel: string;
  encrypted: boolean;
  statusGlyph: string;
  statusColor: string;
  selected: boolean;
}

/** STANDARDS left-panel rows: `entries` must already be sorted (see `sortStandardEntries`) — this only maps, never re-sorts. */
export function buildStandardsListRows(entries: readonly StandardEntry[], selectedCode: string | null): StandardsListRowView[] {
  return entries.map((entry) => {
    const schema = getVendoredSchema(entry.code);
    const encrypted = standardIsEncrypted(entry.channel);
    const status = standardStatusIndicator(entry.rows, encrypted);
    return {
      code: entry.code,
      label: schema?.label ?? entry.code,
      versionLabel: schema?.version ? `v${schema.version}` : '—',
      rowsLabel: `${entry.rows.toLocaleString()} rows`,
      encrypted,
      statusGlyph: status.glyph,
      statusColor: status.color,
      selected: entry.code === selectedCode,
    };
  });
}

// ---------------------------------------------------------------------------
// Selected-standard header (right panel)
// ---------------------------------------------------------------------------

export interface SelectedStandardHeaderView {
  code: string;
  name: string;
  versionChip: string;
  encrypted: boolean;
  rowsCaption: string;
}

export function formatTableRowsCaption(code: string, rows: number): string {
  return `table ${code} · ${rows.toLocaleString()} rows`;
}

export function buildSelectedStandardHeader(entry: StandardEntry | null): SelectedStandardHeaderView | null {
  if (!entry) return null;
  const schema = getVendoredSchema(entry.code);
  return {
    code: entry.code,
    name: schema?.label ?? entry.code,
    versionChip: schema?.version ? `v${schema.version}` : '—',
    encrypted: standardIsEncrypted(entry.channel),
    rowsCaption: formatTableRowsCaption(entry.code, entry.rows),
  };
}

// ---------------------------------------------------------------------------
// EXPLORER tab: field table + IDL download
// ---------------------------------------------------------------------------

export interface ExplorerFieldRowView {
  name: string;
  type: string;
  typeColor: string;
  note: string;
}

export function buildExplorerFieldRows(schema: FbsSchema | null): ExplorerFieldRowView[] {
  if (!schema) return [];
  return schema.fields.map((f) => ({ name: f.name, type: f.type, typeColor: fbsFieldTypeColor(f.type), note: f.note }));
}

export const NO_VENDORED_SCHEMA_MESSAGE =
  'No local FlatBuffers schema is vendored for this standard in this build — nothing to explore or generate from here.';

export function fbsDownloadFilename(code: string): string {
  return `${code}.fbs`;
}

// ---------------------------------------------------------------------------
// GENERATE tab: 8 target languages + client-side stub codegen
// ---------------------------------------------------------------------------

export type CodegenLanguageId = 'typescript' | 'python' | 'cpp' | 'go' | 'rust' | 'csharp' | 'java' | 'swift';

export interface CodegenLanguageSpec {
  id: CodegenLanguageId;
  name: string;
  ext: string;
}

/** Verbatim `LANGUAGES` order from the mock — exactly these 8, in this order. */
export const CODEGEN_LANGUAGES: readonly CodegenLanguageSpec[] = [
  { id: 'typescript', name: 'TypeScript', ext: 'ts' },
  { id: 'python', name: 'Python', ext: 'py' },
  { id: 'cpp', name: 'C++', ext: 'h' },
  { id: 'go', name: 'Go', ext: 'go' },
  { id: 'rust', name: 'Rust', ext: 'rs' },
  { id: 'csharp', name: 'C#', ext: 'cs' },
  { id: 'java', name: 'Java', ext: 'java' },
  { id: 'swift', name: 'Swift', ext: 'swift' },
];

export function findCodegenLanguage(id: CodegenLanguageId): CodegenLanguageSpec {
  return CODEGEN_LANGUAGES.find((l) => l.id === id) ?? CODEGEN_LANGUAGES[0];
}

/** `NORAD_CAT_ID` -> `NoradCatId` (PascalCase, e.g. for accessor method names). */
export function toPascalCaseFieldName(name: string): string {
  return name
    .toLowerCase()
    .split('_')
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join('');
}

/** `NORAD_CAT_ID` -> `noradCatId` (camelCase, e.g. TypeScript/Java/Swift/C# field names). */
export function toCamelCaseFieldName(name: string): string {
  const pascal = toPascalCaseFieldName(name);
  return pascal ? pascal.charAt(0).toLowerCase() + pascal.slice(1) : pascal;
}

/** Honest, non-fabricated header comment — unlike the mock's `"Generated by flatc"` claim, this UI never actually runs `flatc` in the browser. `sdsVersion` is the PACKAGE version (`node/info.standards_version`), omitted from the sentence entirely when unavailable rather than asserting a version we don't know. */
export function codegenHeaderComment(code: string, sdsVersion: string | null): string {
  const versionSuffix = sdsVersion ? ` ${sdsVersion}` : '';
  return `Stub reader generated client-side from ${code}.fbs (spacedatastandards.org${versionSuffix}) — for full generated bindings use the spacedatastandards.org package`;
}

/**
 * Client-side stub reader generator, per the loop task's spec: a
 * `readCODE()`-shaped function stub per language, built from the REAL
 * parsed field list (never a fixture). `schema: null` (no vendored `.fbs`
 * for this code) renders only the honest header + `NO_VENDORED_SCHEMA_MESSAGE`
 * comment, never fabricated field accessors.
 */
export function generateReaderStub(
  code: string,
  schema: FbsSchema | null,
  sdsVersion: string | null,
  langId: CodegenLanguageId,
): string {
  const header = codegenHeaderComment(code, sdsVersion);
  if (!schema) {
    return `// ${header}\n// ${NO_VENDORED_SCHEMA_MESSAGE}\n`;
  }
  const T = schema.tableName;
  const fields = schema.fields;
  switch (langId) {
    case 'typescript': {
      let s = `// ${header}\nimport { ByteBuffer } from 'flatbuffers';\nimport { ${T} } from './SDS/${T}';\n\nexport function read${T}(bytes: Uint8Array) {\n  const msg = ${T}.getRootAs${T}(new ByteBuffer(bytes));\n  return {\n`;
      for (const f of fields) s += `    ${toCamelCaseFieldName(f.name)}: msg.${toCamelCaseFieldName(f.name)}(),\n`;
      s += `  };\n}\n`;
      return s;
    }
    case 'python': {
      let s = `# ${header}\nimport flatbuffers\nfrom SDS.${T} import ${T}\n\n\ndef read_${T.toLowerCase()}(buf: bytes) -> dict:\n    msg = ${T}.GetRootAs(buf, 0)\n    return {\n`;
      for (const f of fields) s += `        "${f.name}": msg.${toPascalCaseFieldName(f.name)}(),\n`;
      s += `    }\n`;
      return s;
    }
    case 'cpp': {
      let s = `// ${header}\n#include "${T}_generated.h"\nusing namespace SDS;\n\nconst ${T}* read_${T.toLowerCase()}(const void* buf) {\n  auto msg = Get${T}(buf);\n`;
      for (const f of fields) s += `  // msg->${f.name.toLowerCase()}()\n`;
      s += `  return msg;\n}\n`;
      return s;
    }
    case 'go': {
      let s = `// ${header}\npackage sds\n\nimport flatbuffers "github.com/google/flatbuffers/go"\n\nfunc Read${T}(buf []byte) *${T} {\n\tmsg := GetRootAs${T}(buf, 0)\n`;
      for (const f of fields) s += `\t// msg.${toPascalCaseFieldName(f.name)}()\n`;
      s += `\treturn msg\n}\n`;
      return s;
    }
    case 'rust': {
      let s = `// ${header}\nuse sds::${T.toLowerCase()}_generated::*;\n\npub fn read_${T.toLowerCase()}(buf: &[u8]) -> ${T} {\n    let msg = root_as_${T.toLowerCase()}(buf).unwrap();\n`;
      for (const f of fields) s += `    // msg.${f.name.toLowerCase()}()\n`;
      s += `    msg\n}\n`;
      return s;
    }
    case 'csharp': {
      let s = `// ${header}\nusing Google.FlatBuffers;\nusing SDS;\n\npublic static class ${T}Reader {\n  public static ${T} Read(byte[] buf) {\n    var msg = ${T}.GetRootAs${T}(new ByteBuffer(buf));\n`;
      for (const f of fields) s += `    // msg.${toPascalCaseFieldName(f.name)}\n`;
      s += `    return msg;\n  }\n}\n`;
      return s;
    }
    case 'java': {
      let s = `// ${header}\nimport SDS.${T};\nimport java.nio.ByteBuffer;\n\npublic class ${T}Reader {\n  public static ${T} read(byte[] buf) {\n    ${T} msg = ${T}.getRootAs${T}(ByteBuffer.wrap(buf));\n`;
      for (const f of fields) s += `    // msg.${toCamelCaseFieldName(f.name)}()\n`;
      s += `    return msg;\n  }\n}\n`;
      return s;
    }
    case 'swift': {
      let s = `// ${header}\nimport FlatBuffers\n\nfunc read${T}(_ buf: [UInt8]) -> ${T} {\n  var bb = ByteBuffer(bytes: buf)\n  let msg = ${T}.getRootAs${T}(bb: &bb)\n`;
      for (const f of fields) s += `  // msg.${toCamelCaseFieldName(f.name)}\n`;
      s += `  return msg\n}\n`;
      return s;
    }
    default:
      return `// ${header}\n`;
  }
}

export function generatedCodeFilename(code: string, lang: CodegenLanguageSpec): string {
  return `${code}_generated.${lang.ext}`;
}

// ---------------------------------------------------------------------------
// DATA STANDARDS / MODULES toggle + EXPLORER/GENERATE/DATA tab strip
// (styling-only pure functions, matching peers-data.ts's `peerFilterTabStyle`
// convention)
// ---------------------------------------------------------------------------

export type DataViewToggle = 'standards' | 'modules';

export interface DataViewToggleSpec {
  id: DataViewToggle;
  label: string;
}

export const DATA_VIEW_TOGGLES: readonly DataViewToggleSpec[] = [
  { id: 'standards', label: 'DATA STANDARDS' },
  { id: 'modules', label: 'MODULES' },
];

export interface DataViewToggleStyle {
  background: string;
  color: string;
}

export function dataViewToggleStyle(id: DataViewToggle, active: DataViewToggle): DataViewToggleStyle {
  const isActive = id === active;
  return {
    background: isActive ? 'rgba(74,166,224,0.2)' : 'transparent',
    color: isActive ? '#9fd4f5' : '#7d929b',
  };
}

export type StandardDetailTab = 'explorer' | 'generate' | 'data';

export interface StandardDetailTabSpec {
  id: StandardDetailTab;
  label: string;
}

export const STANDARD_DETAIL_TABS: readonly StandardDetailTabSpec[] = [
  { id: 'explorer', label: 'EXPLORER' },
  { id: 'generate', label: 'GENERATE' },
  { id: 'data', label: 'DATA' },
];

export interface StandardDetailTabStyle {
  color: string;
  borderColor: string;
}

export function standardDetailTabStyle(id: StandardDetailTab, active: StandardDetailTab): StandardDetailTabStyle {
  const isActive = id === active;
  return {
    color: isActive ? '#eaf6f8' : '#7d929b',
    borderColor: isActive ? '#35c9d8' : 'transparent',
  };
}

// ---------------------------------------------------------------------------
// Honest placeholders — the DATA tab (query workbench) and the MODULES
// toggle are both explicitly out of THIS task's scope (loop task U3.6);
// these match `ConsolePlaceholder.svelte`'s copy convention ("no
// placeholder data is rendered until it is wired to real surfaces") rather
// than porting the mock's fabricated query-output/module-list fixtures.
// ---------------------------------------------------------------------------

export const DATA_TAB_PLACEHOLDER_TITLE = 'DATA · NOT YET WIRED';
export const DATA_TAB_PLACEHOLDER_COPY =
  'This view is intentionally empty: the local FlatSQL query workbench for this standard lands in loop task U3.6 — no placeholder query results are rendered until it is wired to a real surface.';

export const MODULES_PLACEHOLDER_TITLE = 'MODULES · NOT YET WIRED';
export const MODULES_PLACEHOLDER_COPY =
  'This view is intentionally empty: the analysis & propagation modules workbench lands in loop task U3.6 — no placeholder module list is rendered until it is wired to a real surface.';

// ---------------------------------------------------------------------------
// Fetch orchestration — takes the shared SdnApiClient (see
// `../../lib/auth/sdn-api-client.ts`). Every function here swallows its own
// fetch failure (never throws), matching `node-data.ts`/`peers-data.ts`'s
// contract: a missing/unreachable surface degrades to an honest empty/null
// result.
// ---------------------------------------------------------------------------

/** Structural subset of `SdnApiClient` this module needs — lets tests pass a plain fake instead of constructing a real client. */
export type StandardsApiClient = Pick<SdnApiClient, 'requestJson'>;

async function fetchChannels(apiClient: StandardsApiClient): Promise<RawChannel[]> {
  try {
    const result = await apiClient.requestJson<unknown>('/channels');
    return parseChannelsResponse(result.data);
  } catch {
    return [];
  }
}

async function fetchStandardsStats(apiClient: StandardsApiClient): Promise<NodeStatsSnapshot | null> {
  try {
    const result = await apiClient.requestJson<unknown>('/stats');
    return parseNodeStats(result.data);
  } catch {
    return null;
  }
}

async function fetchStandardsNodeInfo(apiClient: StandardsApiClient): Promise<NodeInfoSnapshot | null> {
  try {
    const result = await apiClient.requestJson<unknown>('/api/node/info', { base: 'root' });
    return parseNodeInfo(result.data);
  } catch {
    return null;
  }
}

export interface StandardsDashboardData {
  channels: RawChannel[];
  /** `null` only when the fetch itself failed — see `buildSchemaSyncBannerView`, which needs to tell that apart from "loaded, zero schemas". */
  stats: NodeStatsSnapshot | null;
  nodeInfo: NodeInfoSnapshot | null;
}

/**
 * Fetches every DATA view dashboard surface in parallel. Never rejects — a
 * fully offline node resolves to `{channels:[], stats:null, nodeInfo:null}`,
 * which the view-model builders above render as honest empty/degraded
 * states rather than stale or fabricated data.
 */
export async function loadStandardsDashboardData(apiClient: StandardsApiClient): Promise<StandardsDashboardData> {
  const [channels, stats, nodeInfo] = await Promise.all([
    fetchChannels(apiClient),
    fetchStandardsStats(apiClient),
    fetchStandardsNodeInfo(apiClient),
  ]);
  return { channels, stats, nodeInfo };
}
