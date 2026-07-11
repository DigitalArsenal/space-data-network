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
 *   6. `GET /api/v1/node/status` — SESSION-GATED (anonymous 401): uptime,
 *      disk capacity, service knownness, and bandwidth/history (loop U4.1
 *      cycle C) — see `parseNodeStatus`/`buildNodeThroughputView` below for
 *      the honest-degradation rules for its nullable `disk`/`bandwidth`.
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
import { peerTrustColor, throughputBarGradient } from './console';

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

/** `true` only for a literal JSON `true` — a missing key, `null`, or any other type reads as `false` rather than throwing. */
function pickBoolean(record: Record<string, unknown>, key: string): boolean {
  return record[key] === true;
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

const RATE_UNITS = ['B/s', 'KB/s', 'MB/s', 'GB/s', 'TB/s'] as const;

/** How many `RATE_UNITS` steps up `bytesPerSecond` climbs before it drops under 1024, capped at the last tier. Shared by `formatRate` and `buildThroughputRateView` so both pick a unit the same way. */
function rateUnitIndex(bytesPerSecond: number): number {
  let value = bytesPerSecond;
  let index = 0;
  while (value >= 1024 && index < RATE_UNITS.length - 1) {
    value /= 1024;
    index += 1;
  }
  return index;
}

/** Expresses `bytesPerSecond` at a SPECIFIC already-chosen unit tier rather than picking its own — lets the NETWORK THROUGHPUT widget's small "up" (`rate_out_bps`) figure share the big "down" figure's unit instead of adaptively picking a different one (the widget prints only one unit label, next to the down figure). */
function formatRateAtUnit(bytesPerSecond: number, unitIndex: number): string {
  const value = bytesPerSecond / 1024 ** unitIndex;
  const precision = unitIndex === 0 ? 0 : 2;
  return value.toFixed(precision);
}

/**
 * Adaptive bytes-per-second formatter for the NETWORK THROUGHPUT widget
 * (`"3.37 KB/s"` / `"3.29 MB/s"`) — same adaptive-unit ladder as
 * `formatBytes` but 2-decimal precision beyond the whole-byte tier (the
 * mock's `"3.42 MB/s"` sample has two decimal places, `formatBytes`'
 * `"4.8 GB"` style has one — these are visually distinct widgets).
 * `null`/`undefined`/negative/non-finite input renders `'—'`, matching
 * `formatBytes`' honesty rule: no fabricated `"0 B/s"` for "we don't know".
 */
export function formatRate(bytesPerSecond: number | null | undefined): string {
  if (bytesPerSecond == null || !Number.isFinite(bytesPerSecond) || bytesPerSecond < 0) return '—';
  if (bytesPerSecond === 0) return '0 B/s';
  const unitIndex = rateUnitIndex(bytesPerSecond);
  return `${formatRateAtUnit(bytesPerSecond, unitIndex)} ${RATE_UNITS[unitIndex]}`;
}

/**
 * `uptime_seconds` → the mock's `"4d 02:11"` style (`Nd HH:MM`; the day
 * count is dropped entirely under 24h, rendering plain `"HH:MM"` rather than
 * `"0d HH:MM"`). Pure duration formatter — no `Date`/timezone involved,
 * `uptime_seconds` is an elapsed span, not a wall-clock timestamp.
 * `null`/`undefined`/negative/non-finite input renders `'—'`.
 */
export function formatUptime(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return '—';
  const total = Math.floor(seconds);
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const hh = String(hours).padStart(2, '0');
  const mm = String(minutes).padStart(2, '0');
  return days > 0 ? `${days}d ${hh}:${mm}` : `${hh}:${mm}`;
}

/**
 * NETWORK THROUGHPUT widget's left axis label — the REAL span the bar chart
 * covers, computed from the sample count at the daemon's fixed 5s cadence
 * (a full 24-sample buffer → `"−2m"`), replacing the mock's fixed `"−60s"`
 * (which asserted a 60s window this widget never actually measures).
 * Renders `"−Ns"` under a minute, `"−Nm"` (rounded) at/above it. Callers
 * only invoke this once there are ≥2 history samples (see
 * `buildNodeThroughputView`) — with 0-1 samples the widget shows a
 * "collecting" line instead of an axis at all.
 */
export function formatThroughputAxisLabel(sampleCount: number): string {
  const n = Number.isFinite(sampleCount) && sampleCount > 0 ? Math.trunc(sampleCount) : 0;
  const seconds = n * 5;
  if (seconds < 60) return `−${seconds}s`;
  return `−${Math.round(seconds / 60)}m`;
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

export interface NodeDiskSnapshot {
  capacityBytes: number | null;
  freeBytes: number | null;
  availableBytes: number | null;
}

export interface NodeStatusServiceSnapshot {
  state: string | null;
  mode: string | null;
  /**
   * Whether the daemon actually KNOWS its own autostart configuration —
   * always `false` on this build. There is no `autostart_enabled`-shaped
   * VALUE anywhere in the payload yet, only this knownness flag; it exists
   * precisely so the SERVICE widget's AUTOSTART field can stay an honest
   * `'—'` today (see `buildNodeServiceView`) and start rendering a real
   * ENABLED/DISABLED badge the moment a real value field ships alongside it,
   * instead of silently guessing in the meantime.
   */
  autostartKnown: boolean;
}

export interface NodeBandwidthHistorySample {
  ts: string | null;
  totalInBytes: number | null;
  totalOutBytes: number | null;
  rateInBps: number | null;
  rateOutBps: number | null;
}

export interface NodeBandwidthSnapshot {
  totalInBytes: number | null;
  totalOutBytes: number | null;
  rateInBps: number | null;
  rateOutBps: number | null;
  /** Oldest-first, up to 24 samples at a 5s cadence (~2 min) — see `formatThroughputAxisLabel`. May be empty (0-2 samples) on a freshly started daemon. */
  history: NodeBandwidthHistorySample[];
}

export interface NodeStoreStatusSnapshot {
  totalBytes: number | null;
  totalRecords: number | null;
  storagePath: string | null;
}

export interface NodeStatusSnapshot {
  uptimeSeconds: number | null;
  startedAt: string | null;
  store: NodeStoreStatusSnapshot | null;
  disk: NodeDiskSnapshot | null;
  service: NodeStatusServiceSnapshot | null;
  bandwidth: NodeBandwidthSnapshot | null;
}

function parseNodeDisk(value: unknown): NodeDiskSnapshot | null {
  if (!isPlainRecord(value)) return null;
  return {
    capacityBytes: pickNumber(value, 'capacity_bytes'),
    freeBytes: pickNumber(value, 'free_bytes'),
    availableBytes: pickNumber(value, 'available_bytes'),
  };
}

function parseNodeStatusService(value: unknown): NodeStatusServiceSnapshot | null {
  if (!isPlainRecord(value)) return null;
  return {
    state: pickString(value, 'state'),
    mode: pickString(value, 'mode'),
    autostartKnown: pickBoolean(value, 'autostart_known'),
  };
}

function parseBandwidthHistorySample(value: unknown): NodeBandwidthHistorySample | null {
  if (!isPlainRecord(value)) return null;
  return {
    ts: pickString(value, 'ts'),
    totalInBytes: pickNumber(value, 'total_in_bytes'),
    totalOutBytes: pickNumber(value, 'total_out_bytes'),
    rateInBps: pickNumber(value, 'rate_in_bps'),
    rateOutBps: pickNumber(value, 'rate_out_bps'),
  };
}

function parseNodeBandwidth(value: unknown): NodeBandwidthSnapshot | null {
  if (!isPlainRecord(value)) return null;
  const rawHistory = Array.isArray(value.history) ? value.history : [];
  return {
    totalInBytes: pickNumber(value, 'total_in_bytes'),
    totalOutBytes: pickNumber(value, 'total_out_bytes'),
    rateInBps: pickNumber(value, 'rate_in_bps'),
    rateOutBps: pickNumber(value, 'rate_out_bps'),
    history: rawHistory.map(parseBandwidthHistorySample).filter((s): s is NodeBandwidthHistorySample => s !== null),
  };
}

function parseNodeStoreStatus(value: unknown): NodeStoreStatusSnapshot | null {
  if (!isPlainRecord(value)) return null;
  return {
    totalBytes: pickNumber(value, 'total_bytes'),
    totalRecords: pickNumber(value, 'total_records'),
    storagePath: pickString(value, 'storage_path'),
  };
}

/**
 * Parses `GET /api/v1/node/status` (loop U4.1 cycles A+B — SESSION-GATED;
 * an anonymous 401 is handled by `fetchNodeStatus` below, never by this pure
 * parser, which only ever sees an already-successful body). `disk` and
 * `bandwidth` are themselves nullable IN THE WIRE PAYLOAD (a fresh or
 * constrained daemon may not report either yet) — that `null` is a real,
 * documented state, not a parse failure, and both `buildNodeHealthView` and
 * `buildNodeThroughputView` treat it as "no capacity/telemetry surface"
 * rather than crashing or fabricating zeros.
 */
export function parseNodeStatus(payload: unknown): NodeStatusSnapshot {
  const rec = isPlainRecord(payload) ? payload : {};
  return {
    uptimeSeconds: pickNumber(rec, 'uptime_seconds'),
    startedAt: pickString(rec, 'started_at'),
    store: parseNodeStoreStatus(rec.store),
    disk: parseNodeDisk(rec.disk),
    service: parseNodeStatusService(rec.service),
    bandwidth: parseNodeBandwidth(rec.bandwidth),
  };
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

/**
 * NODE HEALTH widget. `totalBytes` is `stats.total_bytes` (unchanged since
 * U3.2 — sourced from `/v1/stats`, not `/node/status`). `diskCapacityBytes`
 * (loop U4.1, `/node/status`'s `disk.capacity_bytes`) is the first real
 * capacity surface this widget has ever had: when present, `storageTotal`
 * renders the real capacity (`formatBytes`) and `storagePercent` is the
 * real `used/capacity` ratio, per the mock's `"4.8 GB / 32 GB"` pattern.
 * That ratio will often round to a tiny sliver (a few hundred KB against a
 * multi-hundred-GB disk) and is left UN-clamped: the mock's
 * `.sdn-widget-bar-fill` CSS never defined a minimum-visible-width rule, so
 * inventing one now would be a UI decision this loop task wasn't asked to
 * make — a near-invisible bar is the honest picture. `diskCapacityBytes`
 * absent/`null`/non-positive (still common — a fresh/constrained daemon may
 * not report it) keeps today's honest `'— capacity unknown'` fallback and a
 * 0% bar.
 */
export function buildNodeHealthView(
  info: NodeInfoSnapshot | null,
  totalBytes: number | null,
  diskCapacityBytes: number | null = null,
): NodeHealthView {
  const addrs = deriveListenAddressRows(info?.listenAddresses);
  const capacity =
    diskCapacityBytes != null && Number.isFinite(diskCapacityBytes) && diskCapacityBytes > 0
      ? diskCapacityBytes
      : null;
  const percent =
    capacity != null && totalBytes != null && Number.isFinite(totalBytes)
      ? Math.min(100, Math.max(0, (totalBytes / capacity) * 100))
      : 0;
  return {
    mode: formatModeLabel(info?.mode),
    peerId: info?.peerId ?? '—',
    api: addrs.api,
    gateway: addrs.gateway,
    storageUsed: formatBytes(totalBytes),
    storageTotal: capacity != null ? formatBytes(capacity) : '— capacity unknown',
    storagePercent: percent,
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

/**
 * SERVICE widget. `version` stays sourced from `node/info` (U3.2,
 * unchanged). `uptimeSeconds` (loop U4.1, `/node/status`'s top-level
 * `uptime_seconds`) is the first real uptime surface this widget has ever
 * had — `formatUptime` renders it in the mock's `"4d 02:11"` style, and an
 * absent/`null` surface still degrades to the honest `'—'` `formatUptime`
 * itself already renders for that input.
 *
 * `autostart` stays a hardcoded honest `'—'`, unlike `uptime` — not because
 * nothing was wired, but because what WAS wired (`/node/status`'s
 * `service.autostart_known`, see `NodeStatusServiceSnapshot`) only tells us
 * whether the daemon KNOWS its autostart config, never what it IS. There is
 * still no `autostart_enabled`-shaped value anywhere in the payload to
 * render (and `autostart_known` is always `false` on this build besides),
 * so rendering anything but `'—'` here would be a fabrication regardless of
 * that flag's value.
 */
export function buildNodeServiceView(info: NodeInfoSnapshot | null, uptimeSeconds: number | null = null): NodeServiceView {
  return {
    state: info ? 'RUNNING' : '—',
    version: composeServiceVersionLine({
      version: info?.version,
      suiteVersion: info?.suiteVersion,
      agentVersion: info?.agentVersion,
    }),
    autostart: '—',
    uptime: formatUptime(uptimeSeconds),
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

export interface NodeThroughputRateView {
  downValue: string;
  downUnit: string;
  upValue: string;
}

/**
 * NETWORK THROUGHPUT widget's headline pair. `rate_in_bps` picks its own
 * adaptive unit tier (`formatRate`'s ladder) for the big "down" figure;
 * `rate_out_bps` (the small "up" figure) is expressed at that SAME tier via
 * `formatRateAtUnit` rather than adaptively picking its own — the widget
 * only prints one unit label (next to the down figure, e.g. `"MB/s"`), so
 * the up figure has to already share that scale to be readable alone.
 * `rateInBps` missing/negative/non-finite renders both sides `'—'` (a
 * malformed "down" figure with no unit makes an "up" figure meaningless
 * regardless of its own validity).
 */
export function buildThroughputRateView(rateInBps: number | null, rateOutBps: number | null): NodeThroughputRateView {
  if (rateInBps == null || !Number.isFinite(rateInBps) || rateInBps < 0) {
    return { downValue: '—', downUnit: '', upValue: '—' };
  }
  const unitIndex = rateUnitIndex(rateInBps);
  const downValue = formatRateAtUnit(rateInBps, unitIndex);
  const downUnit = RATE_UNITS[unitIndex];
  const upValue =
    rateOutBps != null && Number.isFinite(rateOutBps) && rateOutBps >= 0
      ? formatRateAtUnit(rateOutBps, unitIndex)
      : '—';
  return { downValue, downUnit, upValue };
}

export interface NodeThroughputBarView {
  percent: number;
  gradient: string;
}

/**
 * Normalizes each history sample's `rate_in_bps` to the max sample in the
 * window for the bar chart's height, pairing each bar with
 * `lib/console.ts`'s `throughputBarGradient` (same accent rule as the
 * retired U3.1 mock fixture: the bar at index 8 gets the brighter "current"
 * gradient, every other bar the dimmer cyan one — a decorative accent
 * carried over verbatim, not a claim about which sample is literally "now").
 * A genuinely idle link (every sample's rate 0, or negative/malformed
 * samples clamped to 0) renders every bar at 0% rather than dividing by
 * zero.
 */
export function buildThroughputBars(history: readonly NodeBandwidthHistorySample[]): NodeThroughputBarView[] {
  const rates = history.map((h) => Math.max(0, h.rateInBps ?? 0));
  const max = Math.max(0, ...rates);
  return rates.map((rate, index) => ({
    percent: max > 0 ? Math.min(100, (rate / max) * 100) : 0,
    gradient: throughputBarGradient(index),
  }));
}

export interface NodeThroughputView {
  /** `false` only when `bandwidth` itself is `null` (no telemetry surface at all) — the widget's original honest no-data line. */
  hasData: boolean;
  downValue: string;
  downUnit: string;
  upValue: string;
  /** `true` when there IS bandwidth data but fewer than 2 history samples yet — too little to draw an honest bar chart from. */
  collecting: boolean;
  bars: NodeThroughputBarView[];
  axisStart: string;
  axisEnd: string;
}

/**
 * NETWORK THROUGHPUT widget (loop U4.1 cycle C — `/node/status`'s
 * `bandwidth`). `rate_in_bps`/`rate_out_bps` are top-level fields on
 * `bandwidth` itself (not derived from `history`), so the headline pair
 * renders as soon as `bandwidth` is non-null, independent of how many
 * history samples exist yet — only the bar chart/axis waits for ≥2 samples,
 * rendering a dim "collecting" line in their place until then.
 * `bandwidth === null` (still common — a fresh/constrained daemon, or an
 * anonymous session `fetchNodeStatus` already degraded to `null`) keeps the
 * pre-U4.1 honest `NO TELEMETRY` line.
 */
export function buildNodeThroughputView(bandwidth: NodeBandwidthSnapshot | null): NodeThroughputView {
  if (!bandwidth) {
    return { hasData: false, downValue: '—', downUnit: '', upValue: '—', collecting: false, bars: [], axisStart: '', axisEnd: '' };
  }
  const rate = buildThroughputRateView(bandwidth.rateInBps, bandwidth.rateOutBps);
  const history = bandwidth.history;
  if (history.length < 2) {
    return { hasData: true, ...rate, collecting: true, bars: [], axisStart: '', axisEnd: '' };
  }
  return {
    hasData: true,
    ...rate,
    collecting: false,
    bars: buildThroughputBars(history),
    axisStart: formatThroughputAxisLabel(history.length),
    axisEnd: 'NOW',
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
 * Fetches `GET /api/v1/node/status` (loop U4.1 — SESSION-GATED, unlike
 * `/stats`/`/peers` this endpoint 401s for an anonymous caller). That 401
 * lands in the same `catch` as any other failure and degrades to `null`
 * here — an anonymous/broken session simply keeps the pre-U4.1 honest
 * no-data states for storage capacity/uptime/throughput, same as a fully
 * offline daemon.
 */
async function fetchNodeStatus(apiClient: NodeInfoApiClient): Promise<NodeStatusSnapshot | null> {
  try {
    const result = await apiClient.requestJson<unknown>('/node/status');
    return parseNodeStatus(result.data);
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------------------
// GET /node/activity (loop U4.2 / M2) — the bounded activity-event ring
// ---------------------------------------------------------------------------

/** One event from `GET /api/v1/node/activity` (newest first). */
export interface NodeActivityEvent {
  ts: string;
  kind: string;
  peerId: string | null;
  detail: string;
}

/** Parses the `{count,events:[{ts,kind,peer_id?,detail}]}` payload; malformed input degrades to `[]`, never throws. */
export function parseNodeActivity(value: unknown): NodeActivityEvent[] {
  if (!isPlainRecord(value) || !Array.isArray(value.events)) return [];
  const out: NodeActivityEvent[] = [];
  for (const entry of value.events) {
    if (!isPlainRecord(entry)) continue;
    const ts = pickString(entry, 'ts');
    const kind = pickString(entry, 'kind');
    if (!ts || !kind) continue;
    out.push({ ts, kind, peerId: pickString(entry, 'peer_id'), detail: pickString(entry, 'detail') ?? '' });
  }
  return out;
}

/**
 * Session-gated like `/node/status`: anonymous 401 / offline degrade to
 * `[]`, which the widget renders as its honest no-data line.
 */
async function fetchNodeActivity(apiClient: NodeInfoApiClient, limit = 24): Promise<NodeActivityEvent[]> {
  try {
    const result = await apiClient.requestJson<unknown>(`/node/activity?limit=${limit}`);
    return parseNodeActivity(result.data);
  } catch {
    return [];
  }
}

export interface ActivityRowView {
  time: string;
  text: string;
}

/**
 * Maps a ring event to the mock's time+text row. The kind vocabulary is the
 * server's own (peer_connected / peer_disconnected / pnm_publication /
 * record_stored / grant_issued); unknown kinds render verbatim rather than
 * being dropped, so new server events surface without a UI release.
 */
export function buildActivityRows(events: readonly NodeActivityEvent[], max = 8): ActivityRowView[] {
  const KIND_LABELS: Record<string, string> = {
    peer_connected: 'Peer connected',
    peer_disconnected: 'Peer disconnected',
    pnm_publication: 'PNM publication',
    record_stored: 'Record stored',
    grant_issued: 'Grant issued',
  };
  return events.slice(0, max).map((e) => {
    const label = KIND_LABELS[e.kind] ?? e.kind;
    const parts = [label];
    if (e.detail) parts.push(e.detail);
    if (e.peerId) parts.push(truncateMiddle(e.peerId));
    // ts → HH:MM:SS UTC — the ring spans ~minutes, so date-less time
    // matches the mock's short time column.
    const m = /T(\d{2}:\d{2}:\d{2})/.exec(e.ts);
    return { time: m ? m[1] : e.ts, text: parts.join(' · ') };
  });
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
  /** `GET /node/status` (loop U4.1) — `null` for an anonymous/broken session or an unreachable daemon, same honest-degradation contract as every other field here. */
  status: NodeStatusSnapshot | null;
  /** `GET /node/activity` (loop U4.2) — `[]` for anonymous/offline, same honest degradation. */
  activity: NodeActivityEvent[];
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
  const [nodeInfo, epmResult, vcardText, stats, peers, status, activity] = await Promise.all([
    fetchNodeInfo(apiClient),
    fetchEpmIdentity(apiClient),
    fetchVCardText(fetchImpl),
    fetchNodeStats(apiClient),
    fetchNodePeers(apiClient),
    fetchNodeStatus(apiClient),
    fetchNodeActivity(apiClient),
  ]);
  return {
    nodeInfo,
    identity: epmResult?.identity ?? null,
    epmJsonRaw: epmResult?.raw ?? null,
    vcardText,
    stats,
    peers,
    status,
    activity,
  };
}
