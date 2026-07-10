/**
 * Real daemon-surface wiring for the PEERS console view (loop task U3.3).
 * Ground truth: the `<!-- ============ PEERS ============ -->` block in
 * `design_handoff/sdn_console/SDN Console.dc.html` (DIRECTORY FILTER toolbar
 * + TRUSTED & OBSERVED PEERS table + PEER DETAIL card) — see
 * `PeersView.svelte`'s doc comment for the pixel-level styling port. This
 * file is the U3.2-style "parse → filter/mark → view-model" pipeline for
 * that view, following `lib/node-data.ts`'s established shape.
 *
 * Endpoints probed live on this build (NOT what the mock/tracker guessed):
 *
 *   1. `GET  /api/v1/peers`             — `{"peers":[{"peer_id","addrs"}]}`,
 *      today's swarm connections. No `dn`/trust/standards/agent_version yet.
 *      Reuses `node-data.ts`'s `parseNodePeers`/`RawPeer` (same endpoint,
 *      same shape) rather than re-parsing it here.
 *   2. `GET  /api/v1/peers/{peerId}`    — `{"addrs","connection_count","peer_id"}`,
 *      gated to an authenticated session (401 otherwise — `sdn-server/internal/api/coreapi.go`
 *      `getPeer`, mounted at line ~301). Only fetched for the currently
 *      selected peer.
 *   3. `POST /api/v1/peers/connect`     — admin-only
 *      (`sdn-server/internal/api/coreapi.go:149`/`160`,
 *      `requireAuth(peers.Admin, h.handlePeerConnect)`). Request body is
 *      `{"addr":"<single multiaddr string>"}` (NOT `addrs`/`peer_id`) —
 *      see `handlePeerConnect`, coreapi.go:355-402. Success: 200
 *      `{"peer_id":"...","connected":true}`. Failure modes actually
 *      returned by that handler: 401 `{"code":"unauthorized",...}` (no
 *      session), 403 `{"code":"forbidden",...}` (session below `Admin`
 *      trust), 400 `INVALID_REQUEST`/`INVALID_MULTIADDR`, 502
 *      `CONNECT_FAILED`, 503 `SERVICE_UNAVAILABLE` (no libp2p host).
 *   4. `POST /api/storefront/listings/search` (server ROOT, not under
 *      `/api/v1` — `sdn-server/internal/storefront/api.go:68`) with an
 *      empty `{}` body → `{"listings":null,"total":0,"facets":{...}}` on
 *      this build. `listings[].provider_peer_id` is the field that marks a
 *      peer a PAID provider — see `markPaidProviders` below, which is
 *      null-safe for the `listings:null` case this node returns today.
 *
 * There is NO EPM/dn/trust surface for peers today (M4 follow-up per the
 * wiring analysis) — every parser/view-model builder below degrades to an
 * honest `'—'`/`'NOT PUBLISHED'`-style placeholder rather than fabricating
 * a name, trust level, feed list, or EPM CID. Nothing here ever throws: a
 * missing/malformed field degrades, matching `node-data.ts`'s contract.
 */

import type { SdnApiClient, SdnTrustLevel } from '../../lib/auth/sdn-api-client';
import { SdnApiError } from '../../lib/auth/sdn-api-client';
import type { AuthSessionState } from '../../lib/auth/auth-store';
import { EPM_CID_NOT_PUBLISHED, parseNodePeers, truncateMiddle, type RawPeer } from './node-data';
import { hexToRgba, peerTrustColor } from './console';

export type { RawPeer };

// ---------------------------------------------------------------------------
// Small JSON helpers (mirrors node-data.ts's private helpers — not exported
// from there, so duplicated narrowly here, same rationale as that file's own
// doc comment for why it duplicates login.ts's helpers).
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
// Raw endpoint parsers (payload: unknown → typed snapshot, never throws)
// ---------------------------------------------------------------------------

/**
 * Parses `GET /api/v1/peers` — a straight re-export of `node-data.ts`'s
 * `parseNodePeers` (same endpoint, same `{"peers":[...]}` shape). Kept under
 * this view's own name so callers here don't need to know it's shared.
 */
export const parsePeersResponse = parseNodePeers;

export interface PeerDetail {
  peerId: string;
  addrs: string[];
  connectionCount: number | null;
}

/**
 * Parses `GET /api/v1/peers/{peerId}` (`{"addrs","connection_count","peer_id"}`).
 * Returns `null` for a non-object payload or one with no `peer_id` — this
 * endpoint 401s for an anonymous/non-admin session, so callers treat a
 * failed fetch (see `fetchPeerDetail`) exactly the same as a `null` parse:
 * fall back to the peer-list row's own `addrs`.
 */
export function parsePeerDetail(payload: unknown): PeerDetail | null {
  const rec = isPlainRecord(payload) ? payload : null;
  if (!rec) return null;
  const peerId = pickString(rec, 'peer_id');
  if (!peerId) return null;
  return {
    peerId,
    addrs: pickStringArray(rec, 'addrs'),
    connectionCount: pickNumber(rec, 'connection_count'),
  };
}

/**
 * Marks which of `peers` are PAID storefront providers, from a raw
 * `POST /api/storefront/listings/search` response. Null-safe for this
 * node's real `{"listings":null,...}` shape (no listings yet) — a `null`,
 * missing, or non-array `listings` field simply yields an empty set, never
 * a fabricated PAID badge. A listing's `provider_peer_id` (the storefront
 * wire field) must exactly match a `RawPeer.peerId` to mark that peer paid.
 */
export function markPaidProviders(listingsPayload: unknown, peers: readonly RawPeer[]): ReadonlySet<string> {
  const rec = isPlainRecord(listingsPayload) ? listingsPayload : {};
  const listings = Array.isArray(rec.listings) ? rec.listings : [];
  const providerIds = new Set(
    listings
      .filter(isPlainRecord)
      .map((l) => pickString(l, 'provider_peer_id'))
      .filter((v): v is string => v !== null),
  );
  const knownPeerIds = new Set(peers.map((p) => p.peerId));
  const matched = new Set<string>();
  for (const id of providerIds) {
    if (knownPeerIds.has(id)) matched.add(id);
  }
  return matched;
}

// ---------------------------------------------------------------------------
// Filtering (DIRECTORY FILTER toolbar: ALL / TRUSTED / OBSERVED / PROVIDERS
// tabs + free-text search)
// ---------------------------------------------------------------------------

/**
 * Trust classification for a peer row. `'trusted'` has no real backing
 * surface today (see `buildPeerRows`, which never produces it) — the union
 * member is kept so `filterPeers`'s TRUSTED tab and its tests are honest
 * about "filters correctly, currently always empty" rather than the tab
 * simply not existing in the type system.
 */
export type PeerTrust = 'trusted' | 'observed';

export interface PeerRow {
  peerId: string;
  addrs: string[];
  /** Always `null` today — no per-peer display-name (`dn`) surface exists yet. */
  name: string | null;
  trust: PeerTrust;
  paid: boolean;
}

/**
 * Builds `PeerRow`s from the real `/api/v1/peers` list. Every connected
 * swarm peer is honestly `'observed'` (never a fabricated `'trusted'` — see
 * `PeerTrust`'s doc comment); `paid` comes from `markPaidProviders`.
 */
export function buildPeerRows(peers: readonly RawPeer[], paidPeerIds: ReadonlySet<string>): PeerRow[] {
  return peers.map((p) => ({
    peerId: p.peerId,
    addrs: p.addrs,
    name: null,
    trust: 'observed',
    paid: paidPeerIds.has(p.peerId),
  }));
}

export type PeerFilterTab = 'all' | 'trusted' | 'observed' | 'providers';

export interface PeerFilterTabSpec {
  id: PeerFilterTab;
  label: string;
}

/** Verbatim `[['all','ALL'],['trusted','TRUSTED'],['observed','OBSERVED'],['providers','PROVIDERS']]` from the mock's `peerFilters`. */
export const PEER_FILTER_TABS: readonly PeerFilterTabSpec[] = [
  { id: 'all', label: 'ALL' },
  { id: 'trusted', label: 'TRUSTED' },
  { id: 'observed', label: 'OBSERVED' },
  { id: 'providers', label: 'PROVIDERS' },
];

export interface PeerFilterTabStyle {
  color: string;
  border: string;
  background: string;
}

/** Port of the mock's `peerFilters` styling (`console.ts`.dc.html line ~1096): active tab gets the ice-blue accent, inactive tabs stay neutral gray. */
export function peerFilterTabStyle(tabId: PeerFilterTab, activeTab: PeerFilterTab): PeerFilterTabStyle {
  const active = tabId === activeTab;
  return {
    color: active ? '#9fd4f5' : '#7d929b',
    border: active ? 'rgba(120,190,230,0.5)' : 'rgba(90,150,180,0.28)',
    background: active ? 'rgba(74,166,224,0.1)' : 'transparent',
  };
}

/**
 * DIRECTORY FILTER + search, applied to already-built `PeerRow`s. `tab`
 * gating: `trusted`→`row.trust==='trusted'` (today always empty — honest,
 * not a bug), `observed`→`row.trust==='observed'` (today: everything, since
 * every real peer is a connected swarm peer), `providers`→`row.paid`.
 * `query` matches (case-insensitively) against the full `peerId` and, when
 * present, `name` — never against the honest `'—'` FEEDS placeholder, since
 * matching literal dash text would be meaningless.
 */
export function filterPeers(query: string, tab: PeerFilterTab, peers: readonly PeerRow[]): PeerRow[] {
  const q = query.trim().toLowerCase();
  return peers.filter((row) => {
    if (tab === 'trusted' && row.trust !== 'trusted') return false;
    if (tab === 'observed' && row.trust !== 'observed') return false;
    if (tab === 'providers' && !row.paid) return false;
    if (q) {
      const hay = `${row.name ?? ''} ${row.peerId}`.toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });
}

// ---------------------------------------------------------------------------
// Empty-state labels (TRUSTED & OBSERVED PEERS table body)
// ---------------------------------------------------------------------------

export const PEERS_LOADING_LABEL = 'LOADING PEERS…';
export const PEERS_EMPTY_LABEL = 'NO PEERS CONNECTED';
export const PEERS_FILTERED_EMPTY_LABEL = 'NO PEERS MATCH THIS FILTER';

/** Which empty-state string (if any — `''` when rows exist) the peer table body should render. */
export function peersEmptyStateLabel(loaded: boolean, totalCount: number, filteredCount: number): string {
  if (!loaded) return PEERS_LOADING_LABEL;
  if (totalCount === 0) return PEERS_EMPTY_LABEL;
  if (filteredCount === 0) return PEERS_FILTERED_EMPTY_LABEL;
  return '';
}

// ---------------------------------------------------------------------------
// View-model builders (typed rows → exact render strings)
// ---------------------------------------------------------------------------

const PEER_ADDRESS_UNKNOWN = '—';
const PEER_FIELD_UNKNOWN = '—';
/** Neutral gray for detail fields with no backing surface (matches `peerTrustColor('observed')`, reused as the "we don't know" tone). */
const PEER_DETAIL_UNKNOWN_COLOR = '#7d929b';

export interface PeerRowView {
  peerId: string;
  /**
   * Middle-truncated `peerId` fallback display name — there is no `dn`
   * surface for peers yet, so this always stands in for a name (dimmed
   * styling is a PeersView.svelte CSS concern, not this view-model's).
   */
  name: string;
  /** `true` when `name` is a truncated-peer-id stand-in rather than a real display name — drives the dimmed-name styling `PeersView.svelte` applies (always `true` today, since no `dn` surface exists yet). */
  isFallbackName: boolean;
  /** Full, untruncated peer id — rendered as the row's sub-line (mock's `shortId` slot), so a truncated-then-truncated-again ellipsis never hides real data the user might want to copy. */
  fullPeerId: string;
  trustLabel: string;
  trustColor: string;
  feeds: string;
  address: string;
  paid: boolean;
  selected: boolean;
}

/** TRUSTED & OBSERVED PEERS table rows. */
export function buildPeerRowViews(rows: readonly PeerRow[], selectedPeerId: string | null): PeerRowView[] {
  return rows.map((row) => ({
    peerId: row.peerId,
    name: row.name ?? truncateMiddle(row.peerId),
    isFallbackName: row.name === null,
    fullPeerId: row.peerId,
    trustLabel: row.trust.toUpperCase(),
    trustColor: peerTrustColor(row.trust),
    feeds: PEER_FIELD_UNKNOWN,
    address: row.addrs[0] ?? PEER_ADDRESS_UNKNOWN,
    paid: row.paid,
    selected: row.peerId === selectedPeerId,
  }));
}

export const PEER_EPM_NOT_AVAILABLE_TOOLTIP = 'Peer EPM not available on this build — M4';
export const PEER_CONNECT_REQUIRES_ADMIN_TOOLTIP = 'Sign in with an admin session to connect to peers.';
export const PEER_CONNECT_NO_ADDRESS_TOOLTIP = 'This peer has no known address to connect to.';
export const PEER_CONNECT_TOOLTIP = 'Connect to this peer using its known address.';
export const PEER_PAID_CALLOUT_TEXT = 'PAID PROVIDER · this peer has an active storefront listing on this node.';

export interface PeerDetailView {
  peerId: string;
  name: string;
  isFallbackName: boolean;
  subtitle: string;
  trustLabel: string;
  trustColor: string;
  trustBorderColor: string;
  paid: boolean;
  paidCalloutText: string;
  ownertrust: string;
  ownertrustColor: string;
  agent: string;
  address: string;
  feeds: string;
  epmCid: string;
  connectEnabled: boolean;
  connectTooltip: string;
  vcardTooltip: string;
  qrTooltip: string;
}

/**
 * PEER DETAIL card. `row` is the selected peer's list entry; `detail` is
 * the (possibly `null` — unauthenticated/non-admin/offline) result of
 * `GET /api/v1/peers/{peerId}`, preferred for `address`/subtitle
 * connection-count when present. `canConnect` comes from `canConnectPeers`
 * (admin-trust session gate) — CONNECT is enabled only when that AND the
 * peer has a known address to dial.
 *
 * OWNERTRUST/AGENT/DATA FEEDS/EPM CID have no backing surface at all on
 * this build (M4 follow-up) — always the honest placeholders, never the
 * mock's fabricated `'ultimate'`/`'spacedatanetwork/1.0.3'`/etc. vCARD/QR
 * are always disabled: there is no peer EPM surface to encode (unlike the
 * NODE view's own identity, which encodes ITS OWN vCard client-side).
 */
export function buildPeerDetailView(row: PeerRow | null, detail: PeerDetail | null, canConnect: boolean): PeerDetailView | null {
  if (!row) return null;
  const trustColor = peerTrustColor(row.trust);
  const addr = detail?.addrs[0] ?? row.addrs[0] ?? null;
  const hasAddr = addr !== null && addr !== undefined;
  const connectEnabled = canConnect && hasAddr;
  const connectTooltip = !canConnect
    ? PEER_CONNECT_REQUIRES_ADMIN_TOOLTIP
    : !hasAddr
      ? PEER_CONNECT_NO_ADDRESS_TOOLTIP
      : PEER_CONNECT_TOOLTIP;
  const connectionCount = detail?.connectionCount ?? null;
  return {
    peerId: row.peerId,
    name: row.name ?? truncateMiddle(row.peerId),
    isFallbackName: row.name === null,
    subtitle:
      connectionCount != null
        ? `Connected swarm peer · ${connectionCount} connection${connectionCount === 1 ? '' : 's'}`
        : 'Connected swarm peer',
    trustLabel: row.trust.toUpperCase(),
    trustColor,
    trustBorderColor: hexToRgba(trustColor, 0.4),
    paid: row.paid,
    paidCalloutText: PEER_PAID_CALLOUT_TEXT,
    ownertrust: PEER_FIELD_UNKNOWN,
    ownertrustColor: PEER_DETAIL_UNKNOWN_COLOR,
    agent: PEER_FIELD_UNKNOWN,
    address: addr ?? PEER_ADDRESS_UNKNOWN,
    feeds: PEER_FIELD_UNKNOWN,
    epmCid: EPM_CID_NOT_PUBLISHED,
    connectEnabled,
    connectTooltip,
    vcardTooltip: PEER_EPM_NOT_AVAILABLE_TOOLTIP,
    qrTooltip: PEER_EPM_NOT_AVAILABLE_TOOLTIP,
  };
}

// ---------------------------------------------------------------------------
// CONNECT gating (admin-trust session check — mirrors the server's
// `requireAuth(peers.Admin, ...)` gate on `POST /api/v1/peers/connect`)
// ---------------------------------------------------------------------------

/**
 * Session trust levels that satisfy the server's `peers.Admin` minimum
 * (`sdn-server/internal/peers` PGP-scale ordering: `never < unknown <
 * marginal < standard < full < admin < ultimate` — the server rejects only
 * STRICTLY-below-Admin sessions, so `admin` and `ultimate` both pass).
 */
const CONNECT_ALLOWED_TRUST_LEVELS: ReadonlySet<SdnTrustLevel> = new Set(['admin', 'ultimate']);

/** Whether the current session can call `POST /api/v1/peers/connect` — used to gate the CONNECT button honestly instead of letting it 401/403 silently. */
export function canConnectPeers(authState: Pick<AuthSessionState, 'status' | 'user'>): boolean {
  if (authState.status !== 'authenticated' || !authState.user) return false;
  return CONNECT_ALLOWED_TRUST_LEVELS.has(authState.user.trust_level);
}

// ---------------------------------------------------------------------------
// Fetch orchestration — takes the shared SdnApiClient (see
// `../../lib/auth/sdn-api-client.ts`). Every function here swallows its own
// fetch failure (never throws), matching `node-data.ts`'s contract: a
// missing/unreachable surface degrades to an honest empty/null result.
// ---------------------------------------------------------------------------

/** Structural subset of `SdnApiClient` this module needs — lets tests pass a plain fake instead of constructing a real client. */
export type PeersApiClient = Pick<SdnApiClient, 'requestJson'>;

async function fetchPeersList(apiClient: PeersApiClient): Promise<RawPeer[]> {
  try {
    const result = await apiClient.requestJson<unknown>('/peers');
    return parsePeersResponse(result.data);
  } catch {
    return [];
  }
}

async function fetchPaidProviderIds(apiClient: PeersApiClient, peers: readonly RawPeer[]): Promise<ReadonlySet<string>> {
  try {
    const result = await apiClient.requestJson<unknown>('/api/storefront/listings/search', {
      method: 'POST',
      base: 'root',
      body: {},
    });
    return markPaidProviders(result.data, peers);
  } catch {
    return new Set();
  }
}

export interface PeersDashboardData {
  peers: RawPeer[];
  paidPeerIds: ReadonlySet<string>;
}

/**
 * Fetches the PEERS directory (list + PAID-provider marking) in one call.
 * Never rejects — a fully offline/unreachable node resolves to
 * `{peers: [], paidPeerIds: new Set()}`, which the view-model builders
 * above render as honest empty states.
 */
export async function loadPeersDashboardData(apiClient: PeersApiClient): Promise<PeersDashboardData> {
  const peers = await fetchPeersList(apiClient);
  const paidPeerIds = await fetchPaidProviderIds(apiClient, peers);
  return { peers, paidPeerIds };
}

/**
 * Fetches `GET /api/v1/peers/{peerId}` for the PEER DETAIL card. Returns
 * `null` on ANY failure — including the expected 401 for an
 * anonymous/non-admin session — so `PeersView.svelte` falls back to the
 * selected row's own `addrs` rather than surfacing an error for something
 * that isn't actually broken.
 */
export async function fetchPeerDetail(apiClient: PeersApiClient, peerId: string): Promise<PeerDetail | null> {
  try {
    const result = await apiClient.requestJson<unknown>(`/peers/${encodeURIComponent(peerId)}`);
    return parsePeerDetail(result.data);
  } catch {
    return null;
  }
}

export interface ConnectPeerResult {
  ok: boolean;
  peerId: string | null;
  /** Honest, human-readable error text for an inline (no-toast-library) failure message. `null` on success. */
  message: string | null;
}

/**
 * Calls `POST /api/v1/peers/connect` with `{"addr": addr}` (the server's
 * exact contract — coreapi.go:355-402 — a single multiaddr string, not a
 * peer id or an addrs array). Never throws: every failure mode (400/401/
 * 403/502/503, or a network-level throw) resolves to `{ok:false, message}`
 * with an honest, specific message instead of a generic "something broke".
 */
/**
 * The server's connect handler resolves the target via
 * `peer.AddrInfoFromP2pAddr`, which REQUIRES the multiaddr to end in
 * `/p2p/<peer-id>` — but `/api/v1/peers` swarm addrs come WITHOUT that
 * suffix, so a raw addr always 400s ("cannot extract peer info"). Append
 * the suffix unless the addr already carries one.
 */
export function buildConnectAddr(addr: string, peerId: string | null | undefined): string {
  const a = addr.trim();
  if (!peerId || a.includes('/p2p/')) return a;
  return `${a.replace(/\/+$/, '')}/p2p/${peerId}`;
}

export async function connectToPeer(apiClient: PeersApiClient, addr: string): Promise<ConnectPeerResult> {
  try {
    const result = await apiClient.requestJson<unknown>('/peers/connect', {
      method: 'POST',
      body: { addr },
    });
    const rec = isPlainRecord(result.data) ? result.data : {};
    const peerId = pickString(rec, 'peer_id');
    if (rec.connected === true) {
      return { ok: true, peerId, message: null };
    }
    return { ok: false, peerId, message: 'Connect request completed without confirmation from the daemon.' };
  } catch (err) {
    if (err instanceof SdnApiError) {
      if (err.status === 401) {
        return { ok: false, peerId: null, message: 'Not authenticated — sign in with an admin session to connect.' };
      }
      if (err.status === 403) {
        return { ok: false, peerId: null, message: 'Insufficient trust level — an admin session is required to connect.' };
      }
      return { ok: false, peerId: null, message: err.body?.message || err.message || `Connect failed (HTTP ${err.status}).` };
    }
    return { ok: false, peerId: null, message: 'Connect failed — network error.' };
  }
}
