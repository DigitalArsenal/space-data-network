/**
 * Real daemon-surface wiring for the NODE dashboard (loop task U3.2),
 * replacing the U3.1 typed placeholder data (`lib/console.ts`'s
 * `NODE_*_PLACEHOLDER` fixtures) with live reads from the endpoints probed
 * on this build:
 *
 *   1. `GET /api/node/info`      — identity + listen addrs + versions
 *   2. `GET /api/node/epm/json`  — the self-issued EPM identity document
 *   3. `GET /api/node/epm/vcard` — the same identity as a vCard (text)
 *   4. `GET /api/v1/stats`       — FlatSQL storage/record/schema counters
 *   5. `GET /api/v1/peers`       — the connected-peer swarm list
 *
 * `GET /api/node/epm/qr` 500s ("content too long to encode") on this build
 * — a real server gap, not a client bug — so the QR overlay encodes the
 * vCard text itself client-side via the `qrcode` package (already an
 * `sdn-js` dependency, see `ui/src/components/IdentityPanel.svelte` for the
 * established lazy-import pattern this file's `encodeQrDataUrl` follows).
 *
 * Every parser below takes `unknown` (already-`JSON.parse`d or raw text)
 * and NEVER throws — a missing/malformed field degrades to `null`/`[]`
 * rather than crashing the dashboard, because these daemon surfaces are
 * expected to be unreachable when the console is used offline/pre-boot.
 * View-model builders below then turn those parsed snapshots into the exact
 * strings `NodeView.svelte` renders — no widget ever fabricates a number:
 * a field with no backing surface renders an honest `'—'`/`'NOT PUBLISHED'`/
 * `'NO DATA'`-style placeholder instead (see each builder's doc comment for
 * which real field, if any, it is missing).
 */

import type { SdnApiClient } from '../../lib/auth/sdn-api-client';
import { peerTrustColor } from './console';

// ---------------------------------------------------------------------------
// Small JSON helpers (self-contained — mirrors the private helpers in
// `lib/login.ts`, not exported from there, so duplicated narrowly here).
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

function pickStringArray(record: Record<string, unknown>, key: string): string[] {
  const value = record[key];
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : [];
}

// ---------------------------------------------------------------------------
// Formatting helpers
// ---------------------------------------------------------------------------

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const;

/**
 * Adaptive byte formatter (`115.1 KB` / `4.8 GB`), whole-number precision
 * for bytes, one decimal beyond that. `null`/`undefined`/negative/non-finite
 * input renders `'—'` — this is the ONLY honest way to represent "no
 * capacity/size surface" (never fabricate a `0` or a fake unit).
 */
export function formatBytes(bytes: number | null | undefined): string {
  if (bytes == null || !Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes === 0) return '0 B';
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < BYTE_UNITS.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  const precision = unitIndex === 0 ? 0 : 1;
  return `${value.toFixed(precision)} ${BYTE_UNITS[unitIndex]}`;
}

/**
 * Middle-truncates a long identifier (peer IDs, xpubs) for fixed-width UI
 * rows: `12D3KooW…Z5Fm45`. Short values pass through untouched; blank input
 * renders `'—'` rather than an empty string (so a field never silently
 * disappears from the layout).
 */
export function truncateMiddle(value: string | null | undefined, headLen = 8, tailLen = 6): string {
  const v = (value ?? '').trim();
  if (!v) return '—';
  if (v.length <= headLen + tailLen + 1) return v;
  return `${v.slice(0, headLen)}…${v.slice(-tailLen)}`;
}

const MULTIADDR_HOST_RE = /\/(ip4|ip6|dns4|dns6|dns|dnsaddr)\/([^/]+)/;
const MULTIADDR_PORT_RE = /\/(tcp|udp)\/(\d+)/;

/**
 * Extracts a `host:port` string from a libp2p multiaddr, e.g.
 * `/ip4/127.0.0.1/tcp/5001/p2p/12D3Koo...` → `127.0.0.1:5001`. Only `ip6`
 * (a literal IPv6 address) gets bracketed — `dns6` hostnames don't need it.
 * Returns `null` for anything without both a resolvable host segment and a
 * `tcp`/`udp` port segment (e.g. `/unix/...` sockets, host-only addrs) —
 * callers must never guess a port here.
 */
export function extractHostPort(multiaddr: unknown): string | null {
  if (typeof multiaddr !== 'string') return null;
  const hostMatch = MULTIADDR_HOST_RE.exec(multiaddr);
  const portMatch = MULTIADDR_PORT_RE.exec(multiaddr);
  if (!hostMatch || !portMatch) return null;
  const [, proto, host] = hostMatch;
  const port = portMatch[2];
  return proto === 'ip6' ? `[${host}]:${port}` : `${host}:${port}`;
}

export interface ListenAddressRows {
  api: string;
  gateway: string;
}

/**
 * Derives the NODE HEALTH widget's API/GATEWAY rows from `node/info`'s
 * `listen_addresses`. There is no dedicated "this is the HTTP gateway"
 * surface in that array (it's the libp2p swarm's listen set) — this takes
 * the first parseable `host:port` as API and, only when a second DISTINCT
 * one exists, uses it for GATEWAY. Anything that can't be derived renders
 * an honest `'—'` — do NOT hardcode the classic 127.0.0.1:5001/8080 pair.
 *
 * `/p2p-circuit` entries are excluded first: those are relay reservations
 * THROUGH other nodes, so their host:port is the RELAY's public address,
 * not anything this node listens on — rendering one here would show
 * another node's IP as this node's API endpoint.
 */
export function deriveListenAddressRows(listenAddresses: readonly string[] | null | undefined): ListenAddressRows {
  const list = Array.isArray(listenAddresses) ? listenAddresses : [];
  const own = list.filter((a) => typeof a !== 'string' || !a.includes('/p2p-circuit'));
  const parsed = own.map(extractHostPort).filter((v): v is string => v !== null);
  const unique = Array.from(new Set(parsed));
  return { api: unique[0] ?? '—', gateway: unique[1] ?? '—' };
}

/** `MODE · DESKTOP-LOCAL`-style row; honest `—` when `node/info.mode` is absent. */
export function formatModeLabel(mode: string | null | undefined): string {
  const m = (mode ?? '').trim();
  return m ? `MODE · ${m.toUpperCase()}` : 'MODE · —';
}

/** `N ADDRS`-style PEER SUMMARY meta text from a peer's `addrs` array length. */
export function formatAddrCount(count: number): string {
  const n = Number.isFinite(count) && count >= 0 ? Math.trunc(count) : 0;
  return `${n} ${n === 1 ? 'ADDR' : 'ADDRS'}`;
}

/** `N CONNECTED`→ just the count as a string for the PEER MAP header ("N LINKS"/"N CONNECTIONS"); honest `—` when the stats surface is unavailable. */
export function formatConnectedPeersCount(count: number | null | undefined): string {
  if (count == null || !Number.isFinite(count)) return '—';
  return String(Math.trunc(count));
}

/**
 * Composes the SERVICE widget's version sub-line honestly from whatever
 * `node/info` fields are present (`version`, `suite_version`,
 * `agent_version`) — unlike the U3.1 mock's fixed
 * `"v0.47.0 · current · headless-capable ✓"`, this never asserts
 * "current"/"headless-capable" since no surface backs those claims.
 */
export function composeServiceVersionLine(fields: {
  version?: string | null;
  suiteVersion?: string | null;
  agentVersion?: string | null;
}): string {
  // `version`/`agent_version` arrive as "spacedatanetwork/1.0.4" — strip the
  // product prefix before prepending "v" (a naive `v${version}` renders the
  // nonsense "vspacedatanetwork/1.0.4"), and drop `agent_version` when it
  // repeats `version` verbatim.
  const bare = (v: string) => v.replace(/^spacedatanetwork\//, '');
  const parts: string[] = [];
  if (fields.version) parts.push(`v${bare(fields.version)}`);
  if (fields.suiteVersion) parts.push(`suite ${fields.suiteVersion}`);
  if (fields.agentVersion && fields.agentVersion !== fields.version) parts.push(fields.agentVersion);
  return parts.length ? parts.join(' · ') : '—';
}

/** SERVICE widget's status-dot color: green only when the state is a confirmed `RUNNING`, neutral gray otherwise (never a fabricated red/alert — we simply don't know). */
export function serviceStatusDotColor(state: string): string {
  return state === 'RUNNING' ? '#5ad6a0' : '#5a7a8a';
}

/**
 * IDENTITY subtitle, derived from `entity_type` rather than the U3.1 mock's
 * fixed `"Entity Profile Metadata · self-issued"` — "self-issued" is an
 * assertion about signing provenance this endpoint doesn't confirm, so it
 * is dropped when there's no real `entity_type` to append.
 */
export function deriveIdentitySubtitle(entityType: string | null | undefined): string {
  const t = (entityType ?? '').trim();
  return t ? `Entity Profile Metadata · ${t.toUpperCase()}` : 'Entity Profile Metadata';
}

/**
 * IDENTITY widget's EPM CID row. `GET /api/node/epm/json` has NO CID field
 * anywhere in its response body on this build — surfacing the real EPM CID
 * (once the daemon publishes one addressably) is an M4 follow-up. This is a
 * fixed honest label, not a formatter, kept as a named export so the one
 * "we don't have this yet" string lives in exactly one place.
 */
export const EPM_CID_NOT_PUBLISHED = 'NOT PUBLISHED';

const FN_LINE_RE = /^FN:(.*)$/m;

/** Cheap vCard `FN:` (formatted name) line extractor — no full vCard parser, just the one field the IDENTITY widget's vCARD row needs. Unescapes the vCard-spec `\,` `\;` `\\` `\n` sequences. Returns `null` for missing/blank `FN`. */
export function parseVCardFn(vcardText: string | null | undefined): string | null {
  if (!vcardText) return null;
  const match = FN_LINE_RE.exec(vcardText);
  const raw = match?.[1]?.trim();
  if (!raw) return null;
  return raw
    .replace(/\\n/gi, ' ')
    .replace(/\\,/g, ',')
    .replace(/\\;/g, ';')
    .replace(/\\\\/g, '\\');
}

// ---------------------------------------------------------------------------
// Raw endpoint parsers (payload: unknown → typed snapshot, never throws)
// ---------------------------------------------------------------------------

export interface NodeInfoSnapshot {
  peerId: string | null;
  mode: string | null;
  version: string | null;
  suiteVersion: string | null;
  standardsVersion: string | null;
  agentVersion: string | null;
  listenAddresses: string[];
  dn: string | null;
  entityType: string | null;
}

/** Parses `GET /api/node/info`. */
export function parseNodeInfo(payload: unknown): NodeInfoSnapshot {
  const rec = isPlainRecord(payload) ? payload : {};
  return {
    peerId: pickString(rec, 'peer_id'),
    mode: pickString(rec, 'mode'),
    version: pickString(rec, 'version'),
    suiteVersion: pickString(rec, 'suite_version'),
    standardsVersion: pickString(rec, 'standards_version'),
    agentVersion: pickString(rec, 'agent_version'),
    listenAddresses: pickStringArray(rec, 'listen_addresses'),
    dn: pickString(rec, 'dn'),
    entityType: pickString(rec, 'entity_type'),
  };
}

export interface EpmIdentitySnapshot {
  dn: string | null;
  entityType: string | null;
}

/** Parses `GET /api/node/epm/json` — the identity fields the IDENTITY widget needs (no `listen_addresses`/`mode`, no CID field, on this build). */
export function parseEpmIdentity(payload: unknown): EpmIdentitySnapshot {
  const rec = isPlainRecord(payload) ? payload : {};
  return {
    dn: pickString(rec, 'dn'),
    entityType: pickString(rec, 'entity_type'),
  };
}

export interface NodeSchemaStat {
  schema: string;
  count: number;
  totalBytes: number;
}

export interface NodeStatsSnapshot {
  connectedPeers: number | null;
  totalBytes: number | null;
  totalRecords: number | null;
  schemas: NodeSchemaStat[];
}

/** Parses `GET /api/v1/stats` (`{connected_peers, schemas:[{count,schema,total_bytes}], total_bytes, total_records}`). */
export function parseNodeStats(payload: unknown): NodeStatsSnapshot {
  const rec = isPlainRecord(payload) ? payload : {};
  const rawSchemas = Array.isArray(rec.schemas) ? rec.schemas : [];
  const schemas: NodeSchemaStat[] = rawSchemas.filter(isPlainRecord).map((s) => ({
    schema: pickString(s, 'schema') ?? 'UNKNOWN',
    count: pickNumber(s, 'count') ?? 0,
    totalBytes: pickNumber(s, 'total_bytes') ?? 0,
  }));
  return {
    connectedPeers: pickNumber(rec, 'connected_peers'),
    totalBytes: pickNumber(rec, 'total_bytes'),
    totalRecords: pickNumber(rec, 'total_records'),
    schemas,
  };
}

export interface RawPeer {
  peerId: string;
  addrs: string[];
}

/**
 * Parses `GET /api/v1/peers` — an OBJECT with a `peers` array (NOT a bare
 * array), each entry today carrying only `peer_id`/`addrs` (no
 * `dn`/trust/standards yet). Entries with no `peer_id` are dropped — there
 * is nothing honest to render for them.
 */
export function parseNodePeers(payload: unknown): RawPeer[] {
  const rec = isPlainRecord(payload) ? payload : {};
  const list = Array.isArray(rec.peers) ? rec.peers : [];
  return list
    .filter(isPlainRecord)
    .map((p) => ({ peerId: pickString(p, 'peer_id') ?? '', addrs: pickStringArray(p, 'addrs') }))
    .filter((p) => p.peerId);
}

// ---------------------------------------------------------------------------
// Widget view-model builders (typed snapshots → exact render strings)
// ---------------------------------------------------------------------------

export interface NodeHealthView {
  mode: string;
  peerId: string;
  api: string;
  gateway: string;
  storageUsed: string;
  storageTotal: string;
  storagePercent: number;
}

/** NODE HEALTH widget. `totalBytes` is `stats.total_bytes` — there is no capacity surface, so `storageTotal` is always the honest "capacity unknown" label and `storagePercent` is always 0 (bar renders hidden/0-width). */
export function buildNodeHealthView(info: NodeInfoSnapshot | null, totalBytes: number | null): NodeHealthView {
  const addrs = deriveListenAddressRows(info?.listenAddresses);
  return {
    mode: formatModeLabel(info?.mode),
    peerId: info?.peerId ?? '—',
    api: addrs.api,
    gateway: addrs.gateway,
    storageUsed: formatBytes(totalBytes),
    storageTotal: '— capacity unknown',
    storagePercent: 0,
  };
}

export interface NodeIdentityView {
  name: string;
  subtitle: string;
  epmCid: string;
  vcard: string;
}

/** IDENTITY widget. Prefers the vCard's own `FN:` line for the vCARD row (it's literally what the vCARD download contains); falls back to `dn`. */
export function buildNodeIdentityView(
  identity: EpmIdentitySnapshot | null,
  vcardText: string | null,
): NodeIdentityView {
  const dn = identity?.dn ?? null;
  return {
    name: dn ?? '—',
    subtitle: deriveIdentitySubtitle(identity?.entityType),
    epmCid: EPM_CID_NOT_PUBLISHED,
    vcard: parseVCardFn(vcardText) ?? dn ?? '—',
  };
}

export interface NodeServiceView {
  state: string;
  version: string;
  autostart: string;
  uptime: string;
}

/** SERVICE widget. `autostart`/`uptime` have no daemon surface at all (M1 follow-up) — always an honest `'—'`, never the mock's fixed `ENABLED`/`4d 02:11`. */
export function buildNodeServiceView(info: NodeInfoSnapshot | null): NodeServiceView {
  return {
    state: info ? 'RUNNING' : '—',
    version: composeServiceVersionLine({
      version: info?.version,
      suiteVersion: info?.suiteVersion,
      agentVersion: info?.agentVersion,
    }),
    autostart: '—',
    uptime: '—',
  };
}

export interface NodeNetmapView {
  connectionCount: string;
  countryCount: string;
}

/** PEER MAP caption counts. `connectionCount` is the real `stats.connected_peers`; `countryCount` has no GeoIP surface wired yet (globe render itself is U3.4) so it always renders the honest `'—'`. */
export function buildNodeNetmapView(stats: NodeStatsSnapshot | null): NodeNetmapView {
  return {
    connectionCount: formatConnectedPeersCount(stats?.connectedPeers),
    countryCount: '—',
  };
}

export interface NodePeerSummaryRowView {
  name: string;
  feeds: string;
  trust: string;
  trustColor: string;
}

/** `NO PEERS` honest empty-state label for the PEER SUMMARY widget. */
export const NODE_PEER_SUMMARY_EMPTY_LABEL = 'NO PEERS';

/**
 * PEER SUMMARY rows from `GET /api/v1/peers`. Today's peer records carry no
 * trust/feeds data, so `trust` is always the neutral `OBSERVED` badge (never
 * a fabricated `TRUSTED`) and `feeds` is repurposed to show the real address
 * count instead of a feed list that doesn't exist yet.
 */
export function buildNodePeerSummary(peers: readonly RawPeer[], limit = 3): NodePeerSummaryRowView[] {
  return peers.slice(0, limit).map((p) => ({
    name: truncateMiddle(p.peerId),
    feeds: formatAddrCount(p.addrs.length),
    trust: 'OBSERVED',
    trustColor: peerTrustColor('observed'),
  }));
}

export interface NodeStorageSchemaRowView {
  label: string;
  value: string;
}

export interface NodeStorageView {
  used: string;
  total: string;
  percent: number;
  standardsSynced: string;
  freshness: string;
  freshnessKnown: boolean;
  schemaRows: NodeStorageSchemaRowView[];
}

/**
 * STORAGE · FLATSQL widget. `total_bytes`/`total_records`/`schemas[]` are
 * all real (`stats`); `standardsSynced` comes from `node/info`'s
 * `standards_version`. There is no "freshness" (last-sync-age) surface at
 * all, so unlike the U3.1 mock's fixed green `FRESH`, `freshness` is always
 * the honest `'—'` (`freshnessKnown` stays `false` so the view never applies
 * the "fresh" green styling to an unverified claim). Like NODE HEALTH's
 * storage row, there's no capacity concept for FlatSQL's cumulative byte
 * count either, so `percent` is always 0 (bar hidden).
 */
export function buildNodeStorageView(
  stats: NodeStatsSnapshot | null,
  standardsVersion: string | null | undefined,
): NodeStorageView {
  const totalRecords = stats?.totalRecords ?? null;
  const version = (standardsVersion ?? '').trim();
  return {
    used: formatBytes(stats?.totalBytes ?? null),
    total: totalRecords != null ? `${Math.trunc(totalRecords)} RECORDS` : '— RECORDS',
    percent: 0,
    standardsSynced: version ? `STANDARDS ${version}` : '—',
    freshness: '—',
    freshnessKnown: false,
    schemaRows: (stats?.schemas ?? []).map((s) => ({
      label: s.schema,
      value: `${s.count} RECORDS · ${formatBytes(s.totalBytes)}`,
    })),
  };
}

// ---------------------------------------------------------------------------
// Identity export payloads (JSON / CSV / vCARD download buttons)
// ---------------------------------------------------------------------------

function csvField(value: string): string {
  return /[",\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value;
}

/**
 * Flattens the raw EPM JSON document's top-level SCALAR fields (string /
 * number / boolean) into a 2-row CSV (header + values), per the CSV
 * export button's spec ("CSV of flat scalar fields from the same JSON").
 * Nested objects/arrays are skipped — there is no stable column scheme for
 * them. Returns `''` when there is nothing scalar to export.
 */
export function flattenJsonToCsv(json: unknown): string {
  const rec = isPlainRecord(json) ? json : {};
  const entries = Object.entries(rec).filter(
    ([, v]) => typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean',
  );
  if (entries.length === 0) return '';
  const header = entries.map(([k]) => csvField(k)).join(',');
  const row = entries.map(([, v]) => csvField(String(v))).join(',');
  return `${header}\n${row}`;
}

/** Filesystem-safe slug for a display name, e.g. `"SDN Operator"` → `"sdn-operator"`. Falls back to `'identity'` for blank/unsafe input. */
export function slugifyForFilename(value: string | null | undefined): string {
  const slug = (value ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return slug || 'identity';
}

/** `epm-<slug>.<ext>`-style filename for the IDENTITY widget's JSON/CSV/vCARD download buttons. */
export function buildEpmDownloadFilename(dn: string | null | undefined, extension: 'json' | 'csv' | 'vcf'): string {
  return `epm-${slugifyForFilename(dn)}.${extension}`;
}

// ---------------------------------------------------------------------------
// QR encoding (client-side — `/api/node/epm/qr` 500s on this build)
// ---------------------------------------------------------------------------

interface QrCodeModule {
  toDataURL: (input: string, options?: Record<string, unknown>) => Promise<string>;
}

/**
 * Encodes `payload` (the fetched vCard text) as a QR PNG data URL using the
 * `qrcode` package already an `sdn-js` dependency (see package.json — this
 * follows the exact lazy dynamic-import pattern already established in
 * `ui/src/components/IdentityPanel.svelte`). `GET /api/node/epm/qr` itself
 * 500s ("content too long to encode") on this build, so the overlay can't
 * just fetch a server-rendered image — this generates one client-side from
 * data we already have. Returns `null` (never throws) on empty input or an
 * encoding failure — callers keep their existing decorative placeholder in
 * that case rather than showing a broken image.
 */
export async function encodeQrDataUrl(payload: string | null | undefined): Promise<string | null> {
  const text = (payload ?? '').trim();
  if (!text) return null;
  // @ts-expect-error qrcode does not ship TypeScript declarations in this package (matches IdentityPanel.svelte's import).
  const mod = await import('qrcode').catch(() => null);
  if (!mod) return null;
  const qr = ((mod as { default?: QrCodeModule }).default ?? mod) as QrCodeModule;
  // A full vCard (chain proofs included) already exceeds level-M capacity
  // (~2.3 KB) — the server's own /api/node/epm/qr 500s on exactly this — so
  // degrade to level L (~2.9 KB) before giving up on an honest fallback.
  for (const errorCorrectionLevel of ['M', 'L'] as const) {
    try {
      return await qr.toDataURL(text, {
        color: { dark: '#0a141b', light: '#eaf6f8' },
        errorCorrectionLevel,
        margin: 1,
        width: 240,
      });
    } catch {
      // Payload too large for this level — try the next, or fall through.
    }
  }
  return null;
}

// ---------------------------------------------------------------------------
// Fetch orchestration — takes the shared SdnApiClient (see
// `../../lib/auth/sdn-api-client.ts`) and raw `fetch` for the one
// non-JSON endpoint (vCard text). Every function here swallows its own
// errors (never throws, never `console.error`s) since these daemon surfaces
// are expected to be unreachable when the console is used offline/pre-boot.
// ---------------------------------------------------------------------------

/** Structural subset of `SdnApiClient` this module needs — lets tests pass a plain fake instead of constructing a real client. */
export type NodeInfoApiClient = Pick<SdnApiClient, 'requestJson'>;

async function fetchNodeInfo(apiClient: NodeInfoApiClient): Promise<NodeInfoSnapshot | null> {
  try {
    const result = await apiClient.requestJson<unknown>('/api/node/info', { base: 'root' });
    return parseNodeInfo(result.data);
  } catch {
    return null;
  }
}

async function fetchEpmIdentity(
  apiClient: NodeInfoApiClient,
): Promise<{ identity: EpmIdentitySnapshot; raw: Record<string, unknown> } | null> {
  try {
    const result = await apiClient.requestJson<unknown>('/api/node/epm/json', { base: 'root' });
    const raw = isPlainRecord(result.data) ? result.data : {};
    return { identity: parseEpmIdentity(result.data), raw };
  } catch {
    return null;
  }
}

async function fetchNodeStats(apiClient: NodeInfoApiClient): Promise<NodeStatsSnapshot | null> {
  try {
    const result = await apiClient.requestJson<unknown>('/stats');
    return parseNodeStats(result.data);
  } catch {
    return null;
  }
}

async function fetchNodePeers(apiClient: NodeInfoApiClient): Promise<RawPeer[]> {
  try {
    const result = await apiClient.requestJson<unknown>('/peers');
    return parseNodePeers(result.data);
  } catch {
    return [];
  }
}

/**
 * Raw text fetch for `GET /api/node/epm/vcard` (200 `text/vcard`) — the
 * only endpoint here that isn't JSON, so it bypasses `SdnApiClient`'s
 * JSON-only `requestJson`/`requestBareArray` and hits the relative,
 * same-origin path directly with `credentials:'include'` (mirroring how
 * `SdnApiClient.rawRequest` attaches the session cookie). `fetchImpl` is
 * injectable for tests; defaults to the global `fetch`.
 */
export async function fetchVCardText(
  fetchImpl: typeof fetch = globalThis.fetch.bind(globalThis),
): Promise<string | null> {
  try {
    const resp = await fetchImpl('/api/node/epm/vcard', { credentials: 'include' });
    if (!resp.ok) return null;
    return await resp.text();
  } catch {
    return null;
  }
}

export interface NodeDashboardData {
  nodeInfo: NodeInfoSnapshot | null;
  identity: EpmIdentitySnapshot | null;
  epmJsonRaw: Record<string, unknown> | null;
  vcardText: string | null;
  stats: NodeStatsSnapshot | null;
  peers: RawPeer[];
}

/**
 * Fetches every NODE dashboard surface in parallel. Each individual fetch
 * already swallows its own failure (see the helpers above), so this never
 * rejects — a fully offline node simply resolves to an all-`null`/empty
 * snapshot, which the view-model builders above render as honest empty
 * states rather than stale or fabricated data.
 */
export async function loadNodeDashboardData(
  apiClient: NodeInfoApiClient,
  fetchImpl?: typeof fetch,
): Promise<NodeDashboardData> {
  const [nodeInfo, epmResult, vcardText, stats, peers] = await Promise.all([
    fetchNodeInfo(apiClient),
    fetchEpmIdentity(apiClient),
    fetchVCardText(fetchImpl),
    fetchNodeStats(apiClient),
    fetchNodePeers(apiClient),
  ]);
  return {
    nodeInfo,
    identity: epmResult?.identity ?? null,
    epmJsonRaw: epmResult?.raw ?? null,
    vcardText,
    stats,
    peers,
  };
}
