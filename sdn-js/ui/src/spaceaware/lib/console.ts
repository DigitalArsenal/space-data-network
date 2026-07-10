/**
 * Pure logic for the SDN Console shell + NODE dashboard (loop task U3.1),
 * lifted out of `screens/ConsoleShell.svelte` / `screens/console/*.svelte`
 * so the rail nav model, deep-link mapping, header-chip state, the QR
 * placeholder pattern, and the NODE widget layout engine are all
 * unit-testable without mounting anything.
 *
 * Ground truth: `design_handoff/sdn_console/SDN Console.dc.html` (inline
 * styles/markup + its `class Component extends DCLogic` logic block) and
 * its README.md. `globe.js` (the PEER MAP canvas) is explicitly OUT of
 * scope here — it lands in loop task U3.4 — so the `netmap` widget below
 * only carries the placeholder caption data the mock renders around the
 * canvas (connection/country counts, the 3D/2D tab state, the legend), not
 * the globe itself.
 *
 * Only the six-view shell + the NODE view's widget grid are ported now;
 * PEERS/GROUPS/DATA/CHANNELS/CONJUNCTION stay on a placeholder panel
 * (rendered *inside* this shell instead of the old full-page
 * `ScaffoldScreen`) until their own loop tasks land.
 */

import { CONSOLE_VIEWS, type ConsoleView } from '../router';
import type { AuthStatus } from '../../lib/auth/auth-store';
import { networkStatusFromHealth, type NodeHealthStatus } from './login';

// ---------------------------------------------------------------------------
// Rail navigation model (ROUTES in the .dc.html)
// ---------------------------------------------------------------------------

export type ConsoleNavGroup = 'identity' | 'operations';

export interface ConsoleNavItem {
  id: ConsoleView;
  label: string;
  /** Single-glyph icon, verbatim from the mock's ROUTES table. */
  icon: string;
  /** F-key tag shown next to the label when the rail is expanded/pinned. */
  fkey: string;
  group: ConsoleNavGroup;
}

/**
 * Left-to-right/top-to-bottom rail order, split into the "IDENTITY & GROUPS"
 * and "OPERATIONS" sections exactly as `ROUTES.filter(r=>r[4]==='id'|'op')`
 * does in the mock.
 */
export const CONSOLE_NAV_ITEMS: readonly ConsoleNavItem[] = [
  { id: 'node', label: 'NODE', icon: '◉', fkey: 'N1', group: 'identity' },
  { id: 'peers', label: 'PEERS', icon: '◍', fkey: 'N2', group: 'identity' },
  { id: 'groups', label: 'GROUPS', icon: '⬡', fkey: 'N3', group: 'identity' },
  { id: 'data', label: 'DATA', icon: '▤', fkey: 'N4', group: 'operations' },
  { id: 'channels', label: 'CHANNELS', icon: '⧉', fkey: 'N5', group: 'operations' },
  { id: 'conjunction', label: 'CONJUNCTION', icon: '⊘', fkey: 'N6', group: 'operations' },
];

export function consoleNavItemsForGroup(group: ConsoleNavGroup): ConsoleNavItem[] {
  return CONSOLE_NAV_ITEMS.filter((item) => item.group === group);
}

export interface ConsoleNavItemStyle {
  background: string;
  barColor: string;
  labelColor: string;
  iconColor: string;
}

const CONSOLE_NAV_ACTIVE_BG = 'rgba(74,166,224,0.1)';
const CONSOLE_NAV_INACTIVE_COLOR = '#7390a0';

/**
 * Per-item rail styling, matching the mock's `mkNav`: the CONJUNCTION item
 * carries a red accent when active, every other item cyan; inactive items
 * never take an accent bar/icon color.
 */
export function consoleNavItemStyle(item: Pick<ConsoleNavItem, 'id'>, activeView: ConsoleView): ConsoleNavItemStyle {
  const active = item.id === activeView;
  const accent = item.id === 'conjunction' ? '#ff6b6b' : '#35c9d8';
  return {
    background: active ? CONSOLE_NAV_ACTIVE_BG : 'transparent',
    barColor: active ? accent : 'transparent',
    labelColor: active ? '#eaf6f8' : CONSOLE_NAV_INACTIVE_COLOR,
    iconColor: active ? accent : CONSOLE_NAV_INACTIVE_COLOR,
  };
}

// ---------------------------------------------------------------------------
// Header titles (TITLES / SUBS / titleAccent in the .dc.html)
// ---------------------------------------------------------------------------

export const CONSOLE_TITLES: Record<ConsoleView, string> = {
  node: 'NODE',
  peers: 'PEERS',
  groups: 'GROUPS',
  data: 'DATA',
  channels: 'CHANNELS',
  conjunction: 'CONJUNCTION',
};

export const CONSOLE_SUBTITLES: Record<ConsoleView, string> = {
  node: '· LOCAL · DESKTOP-LOCAL',
  peers: '· DISCOVERY & TRUST',
  groups: '· SHARED ACROSS NODES',
  data: '· STANDARDS WORKBENCH',
  channels: '· ENCRYPTED EXCHANGE',
  conjunction: '· PRIVATE SCREENING',
};

export function consoleTitleAccent(view: ConsoleView): string {
  return view === 'conjunction' ? '#d68a8a' : '#5d7681';
}

// ---------------------------------------------------------------------------
// Deep-link compatibility: `?route=` / `?group=` → `/console/{view}` paths
// ---------------------------------------------------------------------------

export interface ConsoleDeepLink {
  /** Valid CONSOLE_VIEWS member from `?route=`, or `null` when absent/unrecognized. */
  view: ConsoleView | null;
  /** Trimmed `?group=` value, or `null` when absent/blank. Consumed by the GROUPS view (a later loop task) — captured here so the query-param scheme keeps working once that view lands. */
  group: string | null;
}

function isConsoleView(value: string): value is ConsoleView {
  return (CONSOLE_VIEWS as readonly string[]).includes(value);
}

/** Parses the `.dc.html` prototype's own deep-link query params. Never throws on a malformed `search` string. */
export function parseConsoleDeepLinkQuery(search: string): ConsoleDeepLink {
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search);
  } catch {
    return { view: null, group: null };
  }
  const routeParam = params.get('route');
  const groupParam = params.get('group');
  const view = routeParam && isConsoleView(routeParam) ? routeParam : null;
  const group = groupParam && groupParam.trim() ? groupParam.trim() : null;
  return { view, group };
}

/**
 * Resolves the History-API path a `?route=`/`?group=` deep link should land
 * on, given the console view already showing. Returns `null` when there is
 * nothing to navigate to (no `?route=`, or it already matches `currentView`)
 * — callers should only `navigate()` when this is non-null, so a plain
 * `/console/{view}` visit with no query string never triggers a redundant
 * history push.
 */
export function resolveConsoleDeepLinkPath(search: string, currentView: ConsoleView): string | null {
  const { view } = parseConsoleDeepLinkQuery(search);
  if (!view || view === currentView) return null;
  return `/console/${view}`;
}

// ---------------------------------------------------------------------------
// Rail pin persistence (the mock's `railPinned` prop, made user-togglable —
// see ConsoleRail.svelte's doc comment for why a click toggle replaces what
// was a design-tool-only prop in the prototype)
// ---------------------------------------------------------------------------

export const CONSOLE_RAIL_PIN_STORAGE_KEY = 'sdn_console_rail_pinned';

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;

export function loadRailPinned(storage: StorageLike | null | undefined): boolean {
  try {
    return storage?.getItem(CONSOLE_RAIL_PIN_STORAGE_KEY) === '1';
  } catch {
    return false;
  }
}

export function saveRailPinned(storage: StorageLike | null | undefined, pinned: boolean): void {
  try {
    storage?.setItem(CONSOLE_RAIL_PIN_STORAGE_KEY, pinned ? '1' : '0');
  } catch {
    // localStorage unavailable (private mode, etc.) — non-fatal.
  }
}

// ---------------------------------------------------------------------------
// Header chips: health (GET /api/v1/data/health) + session (GET /api/auth/me)
// ---------------------------------------------------------------------------

export type ConsoleHealthChipState = 'ONLINE' | 'DEGRADED' | 'OFFLINE';

/** Maps the shared `NodeHealthStatus` tri-state (see `lib/login.ts`) onto the console header chip's label vocabulary. */
export function consoleHealthChipState(status: NodeHealthStatus): ConsoleHealthChipState {
  if (status === 'NOMINAL') return 'ONLINE';
  if (status === 'DEGRADED') return 'DEGRADED';
  return 'OFFLINE';
}

/** Re-exported so callers only need one import for "parse `/data/health` → chip state". */
export function consoleHealthChipStateFromStatusField(status: string | null | undefined): ConsoleHealthChipState {
  return consoleHealthChipState(networkStatusFromHealth(status));
}

export interface ConsoleChipStyle {
  label: string;
  color: string;
}

/**
 * `#rrggbb` → `rgba(r,g,b,alpha)`, so chip styling can reuse the mock's
 * exact border/background alpha values (`rgba(...,0.4)` border,
 * `rgba(...,0.06)` fill) against a dynamic status color instead of a fixed
 * one. Falls back to a transparent-ish gray for a malformed hex input
 * (never throws — this only ever feeds an inline `style` attribute).
 */
export function hexToRgba(hex: string, alpha: number): string {
  const match = /^#?([0-9a-fA-F]{6})$/.exec(hex);
  if (!match) return `rgba(125,146,155,${alpha})`;
  const value = match[1]!;
  const r = Number.parseInt(value.slice(0, 2), 16);
  const g = Number.parseInt(value.slice(2, 4), 16);
  const b = Number.parseInt(value.slice(4, 6), 16);
  return `rgba(${r},${g},${b},${alpha})`;
}

export function consoleHealthChipStyle(state: ConsoleHealthChipState): ConsoleChipStyle {
  if (state === 'ONLINE') return { label: 'ONLINE', color: '#5ad6a0' };
  if (state === 'DEGRADED') return { label: 'DEGRADED', color: '#ffb24d' };
  return { label: 'OFFLINE', color: '#ff6b6b' };
}

/**
 * Session chip style from the shared `AuthStore`'s `status` (already
 * hydrated via `GET /api/auth/me` at the app level — see
 * `SpaceAwareApp.svelte`'s `authStore`/`authState` threading). `/console` is
 * guarded to authenticated sessions only (`auth-store.ts`
 * `requiresAuthenticatedSession`), so `anonymous`/`unknown` here are
 * transient (mid-redirect / mid-hydration), not steady states.
 */
export function consoleSessionChipStyle(status: AuthStatus): ConsoleChipStyle {
  switch (status) {
    case 'authenticated':
      return { label: 'IDENTITY CONFIRMED', color: '#5ad6a0' };
    case 'authenticating':
      return { label: 'AUTHENTICATING…', color: '#9fd4f5' };
    case 'anonymous':
      return { label: 'ANONYMOUS SESSION', color: '#ffb24d' };
    case 'error':
      return { label: 'SESSION ERROR', color: '#ff6b6b' };
    case 'unknown':
    default:
      return { label: 'CHECKING SESSION…', color: '#7d929b' };
  }
}

// ---------------------------------------------------------------------------
// QR overlay: deterministic placeholder pattern (real EPM QR lands in U3.2)
// ---------------------------------------------------------------------------

function qrFinderCellOn(r: number, c: number, n: number): boolean {
  if (r === 0 || r === 2 || c === 0 || c === 2 || r === n - 1 || c === n - 1) return true;
  return (r === 1 && c === 1) || (r === 1 && c === n - 2) || (r === n - 2 && c === 1);
}

/**
 * Port of the mock's `qrPattern()`: an 11×11 deterministic finder-pattern +
 * pseudo-random fill, `true` = dark cell. No `Math.random` involved (uses
 * the cell index through a fixed multiplier), so it renders identically
 * every time — same rationale as the mock's own comment ("deterministic
 * QR-like pattern"). This is a VISUAL PLACEHOLDER only; encoding the real
 * EPM/vCARD payload is U3.2's job.
 */
export function generateQrPlaceholderPattern(size = 11): boolean[] {
  const cells: boolean[] = [];
  for (let i = 0; i < size * size; i += 1) {
    const r = Math.floor(i / size);
    const c = i % size;
    const finder = (r < 3 && c < 3) || (r < 3 && c > size - 4) || (r > size - 4 && c < 3);
    const on = finder ? qrFinderCellOn(r, c, size) : (((i * 2654435761) >>> 0) % 100) / 100 > 0.52;
    cells.push(on);
  }
  return cells;
}

// ---------------------------------------------------------------------------
// NODE dashboard: widget catalog + layout engine (WIDGETS / DEFAULT_LAYOUT /
// loadLayout / setLayout / onDragEnterWidget / cycleSpan / removeWidget /
// addWidget / resetLayout in the .dc.html)
// ---------------------------------------------------------------------------

export type NodeWidgetId =
  | 'health'
  | 'identity'
  | 'service'
  | 'netmap'
  | 'throughput'
  | 'peersum'
  | 'storage'
  | 'activity';

export interface NodeWidgetSpec {
  id: NodeWidgetId;
  title: string;
  /** Allowed `grid-column: span N` values, in cycle order. */
  spans: readonly number[];
  /** Span used when the widget is (re-)added via ADD WIDGET or RESET. */
  defaultSpan: number;
}

/** Catalog order also drives the ADD WIDGET tray's left-to-right order. */
export const NODE_WIDGET_ORDER: readonly NodeWidgetId[] = [
  'health',
  'identity',
  'service',
  'netmap',
  'throughput',
  'peersum',
  'storage',
  'activity',
];

export const NODE_WIDGETS: Record<NodeWidgetId, NodeWidgetSpec> = {
  health: { id: 'health', title: 'NODE HEALTH', spans: [4, 6], defaultSpan: 4 },
  identity: { id: 'identity', title: 'IDENTITY', spans: [4, 6], defaultSpan: 4 },
  service: { id: 'service', title: 'SERVICE', spans: [4, 6], defaultSpan: 4 },
  netmap: { id: 'netmap', title: 'PEER MAP · GEOIP', spans: [6, 8, 12], defaultSpan: 8 },
  throughput: { id: 'throughput', title: 'NETWORK THROUGHPUT', spans: [4, 6], defaultSpan: 4 },
  peersum: { id: 'peersum', title: 'PEER SUMMARY', spans: [4, 6], defaultSpan: 4 },
  storage: { id: 'storage', title: 'STORAGE · FLATSQL', spans: [4, 6], defaultSpan: 4 },
  activity: { id: 'activity', title: 'ACTIVITY LOG', spans: [4, 8, 12], defaultSpan: 8 },
};

export interface NodeLayoutEntry {
  id: NodeWidgetId;
  span: number;
}

export type NodeLayout = NodeLayoutEntry[];

export const NODE_DEFAULT_LAYOUT: NodeLayout = [
  { id: 'health', span: 4 },
  { id: 'identity', span: 4 },
  { id: 'service', span: 4 },
  { id: 'netmap', span: 8 },
  { id: 'throughput', span: 4 },
];

export const NODE_LAYOUT_STORAGE_KEY = 'sdn_node_layout_v1';

export function cloneNodeLayout(layout: NodeLayout): NodeLayout {
  return layout.map((w) => ({ ...w }));
}

export function isValidNodeLayout(value: unknown): value is NodeLayout {
  return (
    Array.isArray(value) &&
    value.length > 0 &&
    value.every(
      (w) =>
        w &&
        typeof w === 'object' &&
        typeof (w as NodeLayoutEntry).id === 'string' &&
        (w as NodeLayoutEntry).id in NODE_WIDGETS &&
        typeof (w as NodeLayoutEntry).span === 'number',
    )
  );
}

/** Reads `localStorage['sdn_node_layout_v1']`; falls back to `NODE_DEFAULT_LAYOUT` on missing/corrupt/unavailable storage — mirrors the mock's `loadLayout()` exactly (including "any unknown widget id invalidates the whole saved layout"). */
export function loadNodeLayout(storage: StorageLike | null | undefined): NodeLayout {
  try {
    const raw = storage?.getItem(NODE_LAYOUT_STORAGE_KEY);
    if (raw) {
      const parsed: unknown = JSON.parse(raw);
      if (isValidNodeLayout(parsed)) return parsed;
    }
  } catch {
    // Corrupt JSON or storage unavailable — fall back below.
  }
  return cloneNodeLayout(NODE_DEFAULT_LAYOUT);
}

export function saveNodeLayout(storage: StorageLike | null | undefined, layout: NodeLayout): void {
  try {
    storage?.setItem(NODE_LAYOUT_STORAGE_KEY, JSON.stringify(layout));
  } catch {
    // localStorage unavailable — non-fatal, matches the mock's `catch(e){}`.
  }
}

/** Drag-reorder: moves `draggedId` to `targetId`'s position. No-op if either id is missing or they're the same. */
export function reorderNodeLayout(layout: NodeLayout, draggedId: NodeWidgetId, targetId: NodeWidgetId): NodeLayout {
  if (draggedId === targetId) return layout;
  const from = layout.findIndex((w) => w.id === draggedId);
  const to = layout.findIndex((w) => w.id === targetId);
  if (from < 0 || to < 0) return layout;
  const next = layout.slice();
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved);
  return next;
}

/** Cycles a widget's `span` through its catalog's `spans` list, wrapping around. */
export function cycleWidgetSpan(layout: NodeLayout, id: NodeWidgetId): NodeLayout {
  return layout.map((w) => {
    if (w.id !== id) return w;
    const spans = NODE_WIDGETS[id].spans;
    const i = spans.indexOf(w.span);
    const next = spans[(i + 1) % spans.length] ?? spans[0];
    return { ...w, span: next };
  });
}

export function removeNodeWidget(layout: NodeLayout, id: NodeWidgetId): NodeLayout {
  return layout.filter((w) => w.id !== id);
}

/** No-op if `id` is already present (matches the mock's `if(cur.find(...)) return`). */
export function addNodeWidget(layout: NodeLayout, id: NodeWidgetId): NodeLayout {
  if (layout.some((w) => w.id === id)) return layout;
  return layout.concat({ id, span: NODE_WIDGETS[id].defaultSpan });
}

export function resetNodeLayout(): NodeLayout {
  return cloneNodeLayout(NODE_DEFAULT_LAYOUT);
}

/** Widgets not currently in `layout`, in catalog order — feeds the ADD WIDGET tray. */
export function availableNodeWidgets(layout: NodeLayout): NodeWidgetSpec[] {
  const used = new Set(layout.map((w) => w.id));
  return NODE_WIDGET_ORDER.filter((id) => !used.has(id)).map((id) => NODE_WIDGETS[id]);
}

/** `W4`-style span badge text shown on the resize control in edit mode. */
export function widgetSpanLabel(span: number): string {
  return `W${span}`;
}

// ---------------------------------------------------------------------------
// NODE widget placeholder datasets (typed placeholder data — real wiring is
// U3.2; PEER MAP's actual globe render is U3.4)
// ---------------------------------------------------------------------------

export type PeerTrustLevel = 'trusted' | 'observed' | 'unknown';

/** Mirrors the mock's `trustColor()`. */
export function peerTrustColor(trust: string): string {
  if (trust === 'trusted') return '#5ad6a0';
  if (trust === 'observed') return '#7d929b';
  return '#ffb24d';
}

export interface NodePeerSummaryRow {
  name: string;
  trust: string;
  trustColor: string;
  feeds: string;
}

/** `PEERS.slice(0,3)` from the mock, mapped through `trustColor()`. */
export const NODE_PEER_SUMMARY_PLACEHOLDER: readonly NodePeerSummaryRow[] = [
  { name: 'SpaceAware.io', trust: 'TRUSTED', trustColor: peerTrustColor('trusted'), feeds: 'EPM · MPE · PNM' },
  { name: 'CelesTrak Provider', trust: 'TRUSTED', trustColor: peerTrustColor('trusted'), feeds: 'CAT · OMM · SPW' },
  { name: 'OrbitalEdge Node', trust: 'OBSERVED', trustColor: peerTrustColor('observed'), feeds: 'OMM' },
];

export interface NodeActivityRow {
  time: string;
  color: string;
  text: string;
}

/** Verbatim `ACTIVITY` array from the mock. */
export const NODE_ACTIVITY_PLACEHOLDER: readonly NodeActivityRow[] = [
  { time: '12:04:22', color: '#5ad6a0', text: 'Channel mpe-screening-alpha · grant accepted' },
  { time: '11:58:10', color: '#9fd4f5', text: 'Schema sync · OMM updated to 9,120 rows' },
  { time: '11:42:03', color: '#7d929b', text: 'Observed peer · OrbitalEdge Node' },
  { time: '11:30:51', color: '#35c9d8', text: 'SpaceAware analytics entitlement renewed' },
  { time: '11:15:09', color: '#5ad6a0', text: 'Service autostart · node came online' },
];

/** Verbatim `SPARK` array from the mock (%-height bars, 60s window). */
export const NODE_THROUGHPUT_SPARK: readonly number[] = [38, 52, 44, 67, 58, 79, 48, 62, 85, 54, 70, 41];

/** Bar index 8 (the tallest, "current") gets the brighter ice gradient; every other bar gets the cyan gradient. */
export function throughputBarGradient(index: number): string {
  return index === 8
    ? 'linear-gradient(180deg,#4aa6e0,rgba(74,166,224,0.2))'
    : 'linear-gradient(180deg,#35c9d8,rgba(53,201,216,0.2))';
}

export type NodeMapMode = '2d' | '3d';

export interface NodeMapTabStyle {
  background3d: string;
  color3d: string;
  background2d: string;
  color2d: string;
}

/** 3D/2D tab pill styling for the PEER MAP widget header (mock treats anything but `'2d'` as 3D, i.e. `'3d'` is the implicit default). */
export function nodeMapTabStyle(mode: NodeMapMode): NodeMapTabStyle {
  const is3d = mode !== '2d';
  const activeBg = 'rgba(53,201,216,0.18)';
  const activeColor = '#9fe9f2';
  const inactiveColor = '#7d929b';
  return {
    background3d: is3d ? activeBg : 'transparent',
    color3d: is3d ? activeColor : inactiveColor,
    background2d: !is3d ? activeBg : 'transparent',
    color2d: !is3d ? activeColor : inactiveColor,
  };
}

/**
 * PEER MAP caption counts. Real geo-resolved peer data is a later gap (M3 /
 * Decision D8 in `SPACEAWARE_UI_WIRING_ANALYSIS.md`) and the canvas draw
 * itself is U3.4 (`globe.js` port) — this only reproduces the two static
 * caption numbers the mock renders next to the (here, inert) canvas, from
 * its own `HOME`/`CONNECTIONS` fixture (also used verbatim by
 * `screens/GlobeDemoPanel.svelte`, loop U0.2).
 */
export const NODE_NETMAP_PLACEHOLDER = {
  connectionCount: 16,
  countryCount: 15,
} as const;

// ---------------------------------------------------------------------------
// NODE HEALTH / IDENTITY / SERVICE / STORAGE static placeholder text
// (verbatim mock copy — real values land in U3.2)
// ---------------------------------------------------------------------------

export const NODE_HEALTH_PLACEHOLDER = {
  mode: 'MODE · DESKTOP-LOCAL',
  peerId: '12D3KooWDesignerLocalNode',
  api: '127.0.0.1:5001',
  gateway: '127.0.0.1:8080',
  storageUsed: '4.8 GB',
  storageTotal: '32 GB',
  storagePercent: 15,
} as const;

export const NODE_IDENTITY_PLACEHOLDER = {
  name: 'SDN Operator',
  subtitle: 'Entity Profile Metadata · self-issued',
  epmCid: 'bafkreidesignerpublicepmexample',
  vcard: 'Space Data Network Operator',
} as const;

export const NODE_SERVICE_PLACEHOLDER = {
  state: 'RUNNING',
  version: 'v0.47.0 · current · headless-capable ✓',
  autostart: 'ENABLED',
  uptime: '4d 02:11',
} as const;

export const NODE_STORAGE_PLACEHOLDER = {
  used: '4.8',
  total: '32 GB',
  percent: 15,
  standardsSynced: '6 STANDARDS SYNCED',
  freshness: 'FRESH',
  schema: 'v1.0.3 · synced',
} as const;
