/**
 * Vendored `spacedatastandards.org` FlatBuffers IDL loading + a pure `.fbs`
 * parser (loop task U3.5 — DATA view EXPLORER/GENERATE tabs).
 *
 * `spacedatastandards.org` is already an `sdn-js` dependency, pinned to
 * `v1.136.0` (`package.json`), installed as the FULL upstream repo (a git
 * tag tarball, not an npm-packed subset) — so every one of its 169 standard
 * codes has `node_modules/spacedatastandards.org/schema/<CODE>/main.fbs` on
 * disk. This file imports ALL of them at build time via a single
 * `import.meta.glob(..., { query: '?raw', import: 'default', eager: true })`
 * so the DATA view's EXPLORER/IDL/GENERATE tabs work with zero runtime
 * fetches (the LOCK SCOPE's "zero external requests from the UI" rule) and
 * survive being bundled into the single-file embedded artifact
 * (`vite.spaceaware.config.mts`'s `assetsInlineLimit` doesn't even apply
 * here — `?raw` imports become inline JS string literals directly, never a
 * separate asset).
 *
 * The glob pattern is a RELATIVE path (`../../../../node_modules/...`), not
 * the root-relative `/node_modules/...` form the loop task suggested trying
 * first — that root-relative form does NOT resolve here, because the
 * dedicated SpaceAware Vite config (`ui/vite.spaceaware.config.mts`) sets
 * `root: __dirname` = `ui/`, and `node_modules` lives one level up at the
 * `sdn-js` package root, not under `ui/`. `import.meta.glob` patterns that
 * start with `./`/`../` are resolved relative to the IMPORTING FILE (this
 * one, at `ui/src/spaceaware/lib/standards-fbs.ts` — four `../` segments
 * reaches the `sdn-js` root), independent of the configured Vite `root`, so
 * the same relative pattern resolves correctly under both the SpaceAware
 * Vite build AND plain `vitest` (whose config has no explicit `root`, so it
 * defaults to `sdn-js/` — the relative form works there too, verified by
 * this file's own tests). This was confirmed empirically before committing
 * to it, not assumed.
 */

// ---------------------------------------------------------------------------
// Vendored raw source map (CODE -> the exact bytes of that standard's
// `main.fbs`, byte-for-byte — the EXPLORER tab's IDL code block and its
// "↓ .fbs" download both render/emit this string verbatim, never
// reformatted).
// ---------------------------------------------------------------------------

const RAW_FBS_MODULES = import.meta.glob('../../../../node_modules/spacedatastandards.org/schema/*/main.fbs', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const SCHEMA_PATH_CODE_RE = /schema\/([A-Za-z0-9_]+)\/main\.fbs$/;

function buildVendoredFbsByCode(modules: Record<string, string>): ReadonlyMap<string, string> {
  const map = new Map<string, string>();
  for (const [key, source] of Object.entries(modules)) {
    const match = SCHEMA_PATH_CODE_RE.exec(key);
    if (!match) continue;
    map.set(match[1], source);
  }
  return map;
}

/** CODE -> raw `main.fbs` text for every vendored standard (169 on `v1.136.0`). */
export const VENDORED_FBS_BY_CODE: ReadonlyMap<string, string> = buildVendoredFbsByCode(RAW_FBS_MODULES);

/**
 * Raw vendored `.fbs` text for `code`, or `null` when this build's
 * `spacedatastandards.org` package has no schema for it. A live node's
 * `GET /api/v1/channels` can list MORE standard codes than this vendored
 * package ships (channels are server-side content, not gated by which
 * schemas got bundled into this UI build) — every caller here treats a
 * `null` as an honest "no local schema to explore" state, never a crash.
 */
export function getVendoredFbsSource(code: string): string | null {
  return VENDORED_FBS_BY_CODE.get(code) ?? null;
}

// ---------------------------------------------------------------------------
// Pure `.fbs` parser (raw text -> the root table's fields). No file I/O, no
// Vite magic — takes a string, returns data, never throws. Exercised
// directly (not just through the vendored map) by this file's tests so the
// edge cases (tables, enums, includes, unions, vector fields, nested
// tables, multi-line doc comments, attributes after the type) are provable
// without depending on which real standards happen to hit each shape today.
// ---------------------------------------------------------------------------

export interface FbsField {
  /** Exact field identifier, capitalization preserved verbatim (e.g. `NORAD_CAT_ID`). */
  name: string;
  /** The field's declared `.fbs` type token only — vector brackets kept (`[double]`), but any trailing ` = default` or ` (attribute)` stripped (that's metadata, not the type). */
  type: string;
  /**
   * First line of the `///` doc comment block directly above the field,
   * trimmed — NOT the full multi-line block (per the loop task's spec).
   * `''` when the field has no `///` comment (a plain `//` comment, or no
   * comment at all, never counts).
   */
  note: string;
}

export interface FbsSchema {
  /** Root table name (equals `standardCode` for every real vendored schema on this build — verified — but the resolution still falls back honestly when it doesn't, see `findRootTableName`). */
  tableName: string;
  /** First line of the doc comment directly above `table NAME {`, or `tableName` itself when the table has no leading doc comment. */
  label: string;
  /** This standard's own per-schema version from the file's `// Version: X.Y.Z` header line, or `null` if that header is missing (defensive — every vendored file has it today). */
  version: string | null;
  fields: FbsField[];
  /** The exact source text this was parsed from (byte-for-byte) — kept on the result so callers never need to re-fetch it from `VENDORED_FBS_BY_CODE` separately. */
  raw: string;
}

const TABLE_DECL_RE = /table\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{/g;
const ROOT_TYPE_RE = /root_type\s+([A-Za-z_][A-Za-z0-9_]*)\s*;/;
const HEADER_VERSION_RE = /^\/\/\s*Version:\s*(.+)$/m;
const FIELD_LINE_RE = /^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*([^;]+);/;

/** Every `table NAME {` declaration in `source`, in file order (includes nested/wrapper tables, e.g. `CDMObject`, `CelestialFrameWrapper` — callers filter by name, not by position). */
function listTableNames(source: string): string[] {
  const names: string[] = [];
  let match: RegExpExecArray | null;
  TABLE_DECL_RE.lastIndex = 0;
  while ((match = TABLE_DECL_RE.exec(source))) {
    names.push(match[1]);
  }
  return names;
}

/**
 * Resolves which table is the "root" one to explore: the table whose name
 * matches `standardCode` exactly (true for all 169 vendored schemas on this
 * build), else the file's own `root_type` declaration (when that name is
 * actually a declared table), else the first table declared in the file.
 * Returns `null` only when the file declares no table at all.
 */
export function findRootTableName(source: string, standardCode: string): string | null {
  const names = listTableNames(source);
  if (names.includes(standardCode)) return standardCode;
  const rootTypeMatch = ROOT_TYPE_RE.exec(source);
  const declared = rootTypeMatch?.[1];
  if (declared && names.includes(declared)) return declared;
  return names[0] ?? null;
}

/** Extracts the body text between `table NAME {` and its closing `}` (flatbuffers table bodies never nest braces, so a non-greedy match to the first `}` is exact). `null` when no such table is declared. */
function extractTableBody(source: string, tableName: string): string | null {
  const re = new RegExp(`table\\s+${tableName}\\s*\\{([\\s\\S]*?)\\}`);
  const match = re.exec(source);
  return match ? match[1] : null;
}

/**
 * First line of the `///` doc-comment block immediately above
 * `table NAME {` (walking upward past blank lines to find it, then taking
 * only the first line of that contiguous `///` block — same "first line"
 * rule as field notes). `null` when there's no such comment.
 */
function extractLeadingTableDoc(source: string, tableName: string): string | null {
  const declRe = new RegExp(`table\\s+${tableName}\\s*\\{`);
  const match = declRe.exec(source);
  if (!match) return null;
  const before = source.slice(0, match.index).split('\n');
  let i = before.length - 1;
  while (i >= 0 && before[i].trim() === '') i -= 1;
  const docLines: string[] = [];
  while (i >= 0 && before[i].trim().startsWith('///')) {
    docLines.unshift(before[i].trim());
    i -= 1;
  }
  if (docLines.length === 0) return null;
  return docLines[0].replace(/^\/\/\/\s?/, '').trim();
}

/** This schema's own `// Version: X.Y.Z` header line (distinct from the global `spacedatastandards.org` package version — see `standards-data.ts`'s `SDS_PACKAGE_VERSION`). */
export function extractFbsHeaderVersion(source: string): string | null {
  const match = HEADER_VERSION_RE.exec(source);
  return match ? match[1].trim() : null;
}

/**
 * Extracts `{name, type, note}` for every direct field of a table body.
 * Handles every real shape seen across the vendored corpus:
 *   - scalar/string/bool fields (`OBJECT_ID: string;`)
 *   - vector fields (`PAYLOADS: [PLD];`, `COVARIANCE:[double];`)
 *   - enum-typed fields with a default value (`OBJECT_TYPE: spaceObjectClass = UNKNOWN;`)
 *     — the type column is `spaceObjectClass`, the default is dropped (not
 *     part of the type)
 *   - attributes after the type (`LICENSE_ID: string (required);`) —
 *     dropped the same way
 *   - nested/other-table-typed fields (`OWNER: legacyCountryCode;`, `POC:EPM;`)
 *   - a `///` doc comment spanning MULTIPLE lines directly above a field —
 *     only the FIRST line becomes `note` (spec'd behavior, not a bug)
 *   - a plain `//` comment (not `///`) directly above a field — ignored,
 *     `note` stays `''` (real example: `CDM/main.fbs`'s `OBJECT:CAT;`, which
 *     sits under a `// Catalog Definition` line, not a `///` doc comment)
 *   - fields with no comment at all (`note: ''`)
 */
export function parseFbsFields(tableBody: string): FbsField[] {
  const fields: FbsField[] = [];
  let pendingNote: string | null = null;
  for (const rawLine of tableBody.split('\n')) {
    const line = rawLine.trim();
    if (line === '') continue;
    if (line.startsWith('///')) {
      if (pendingNote === null) pendingNote = line.replace(/^\/\/\/\s?/, '').trim();
      continue;
    }
    if (line.startsWith('//')) continue; // plain comment — never a note source
    const match = FIELD_LINE_RE.exec(line);
    if (match) {
      const rawType = match[2].trim();
      const type = rawType.split(/\s+/)[0] ?? rawType;
      fields.push({ name: match[1], type, note: pendingNote ?? '' });
    }
    pendingNote = null;
  }
  return fields;
}

/**
 * Full pipeline: raw `.fbs` source -> the root table's schema (name, label,
 * per-file version, field list). `null` when `source` is blank/has no
 * table at all — callers (see `standards-data.ts`) treat that exactly like
 * "no vendored schema for this code", never a crash.
 */
export function parseFbsSchema(source: string, standardCode: string): FbsSchema | null {
  if (!source.trim()) return null;
  const tableName = findRootTableName(source, standardCode);
  if (!tableName) return null;
  const body = extractTableBody(source, tableName);
  if (body === null) return null;
  return {
    tableName,
    label: extractLeadingTableDoc(source, tableName) ?? tableName,
    version: extractFbsHeaderVersion(source),
    fields: parseFbsFields(body),
    raw: source,
  };
}

// ---------------------------------------------------------------------------
// Memoized per-code accessor — the DATA view re-derives this on every
// selection change, so parsing (cheap, but non-trivial for the larger
// schemas) only happens once per code actually viewed, not once per render.
// ---------------------------------------------------------------------------

const schemaCache = new Map<string, FbsSchema | null>();

/** `parseFbsSchema` over the vendored source for `code`, cached per code. `null` for a code with no vendored `.fbs` (see `getVendoredFbsSource`) or an unparseable one. */
export function getVendoredSchema(code: string): FbsSchema | null {
  if (schemaCache.has(code)) return schemaCache.get(code) ?? null;
  const source = getVendoredFbsSource(code);
  const parsed = source ? parseFbsSchema(source, code) : null;
  schemaCache.set(code, parsed);
  return parsed;
}

// ---------------------------------------------------------------------------
// Field type -> EXPLORER table TYPE-column color. Port of the mock's own
// `typeColor` rule (`f[1].indexOf('[')>=0` -> vector purple; `'string'` ->
// green; an uppercase-first custom type name -> amber; everything else
// (native scalars, lowercase-first enum type names) -> the neutral cyan),
// applied to REAL parsed type strings instead of the mock's fixture data.
// ---------------------------------------------------------------------------

export function fbsFieldTypeColor(type: string): string {
  if (type.startsWith('[')) return '#c77dff';
  if (type === 'string') return '#5ad6a0';
  if (/^[A-Z]/.test(type)) return '#ffb24d';
  return '#9fd4f5';
}
