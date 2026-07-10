/**
 * Pure logic extracted from `login/Login.dc.html` (design ground truth, loop
 * task U1.1) so it is unit-testable independent of the Svelte component and
 * the canvas it draws into:
 *
 *  - the deterministic seed-42 Park-Miller ("minimal standard" Lehmer) PRNG
 *    that drives the starfield, plus the starfield/orbit-arc generators
 *  - operator-form / node-key form validation used by the mock auth gate
 *  - the staged mock-auth timing constants + per-step view-model builder
 *
 * `screens/LoginScreen.svelte` should only glue these to the DOM (canvas
 * drawing, `$state`, timers) — it must not re-implement the algorithms here.
 *
 * U1.2 adds a second layer of pure helpers below (real-auth step mapping,
 * telemetry/health/node-info parsing+formatting, node-key peer-identity
 * resolution) that replace the U1.1 mock-staged sequence's control flow —
 * `AUTH_STEP_TIMINGS_MS`/`buildAuthSteps` stay exactly as they were (golden
 * values, still exported/tested) since `buildAuthSteps` is reused verbatim
 * to render whatever step index the REAL flow below computes.
 */

import type { AuthStage } from '../../lib/auth/auth-store';
import { LocalWalletError } from '../../lib/auth/local-wallet';
import { SdnApiError } from '../../lib/auth/sdn-api-client';

// ---------------------------------------------------------------------------
// Starfield PRNG
// ---------------------------------------------------------------------------

/** Star count drawn per `Login.dc.html`'s `drawStars()`. */
export const STAR_COUNT = 170;

/** Seed used by `Login.dc.html` so the field is identical across redraws. */
export const STAR_SEED = 42;

const LEHMER_MULTIPLIER = 16807;
const LEHMER_MODULUS = 2147483647; // 2^31 - 1

/**
 * Deterministic Park-Miller "minimal standard" LCG, exactly as implemented
 * inline in `Login.dc.html`:
 *
 * ```js
 * let seed = 42;
 * const rnd = () => { seed = (seed * 16807) % 2147483647; return seed / 2147483647; };
 * ```
 */
export function createSeededRandom(seed: number = STAR_SEED): () => number {
  let s = seed;
  return () => {
    s = (s * LEHMER_MULTIPLIER) % LEHMER_MODULUS;
    return s / LEHMER_MODULUS;
  };
}

export interface Star {
  x: number;
  y: number;
  r: number;
  /** `"r,g,b"` fragment, ready to interpolate into an `rgba(...)` string. */
  color: string;
  /** Already rounded to 2 decimals, matching the source's `.toFixed(2)`. */
  alpha: number;
}

/**
 * Regenerate the 170-star field for a `w`x`h` canvas. Deterministic for a
 * given `(w, h, seed)` — same inputs always produce the same stars, which is
 * what lets `Login.dc.html` redraw on resize without the field "twinkling".
 *
 * Per-star draw order matches the source exactly: x, y, r, color-threshold,
 * alpha (5 `rnd()` calls/star) — color/alpha distribution is 80%
 * `207,227,236` / 15% `127,215,226` / 5% `255,217,160`, r in [0.4, 1.3),
 * alpha in [0.12, 0.72).
 */
export function generateStarfield(w: number, h: number, seed: number = STAR_SEED): Star[] {
  const rnd = createSeededRandom(seed);
  const stars: Star[] = [];
  for (let i = 0; i < STAR_COUNT; i++) {
    const x = rnd() * w;
    const y = rnd() * h;
    const r = 0.4 + rnd() * 0.9;
    const t = rnd();
    const color = t < 0.8 ? '207,227,236' : t < 0.95 ? '127,215,226' : '255,217,160';
    const alpha = Number((0.12 + rnd() * 0.6).toFixed(2));
    stars.push({ x, y, r, color, alpha });
  }
  return stars;
}

export interface OrbitArc {
  cx: number;
  cy: number;
  rx: number;
  ry: number;
  startAngle: number;
  endAngle: number;
  strokeStyle: string;
  lineWidth: number;
  dash: number[];
}

/**
 * The 3 faint elliptical orbit arcs swept from below the viewport. No PRNG
 * involved — purely a function of canvas size, per `Login.dc.html`.
 */
export function generateOrbitArcs(w: number, h: number): OrbitArc[] {
  const cx = w * 0.5;
  const cy = h * 1.85;
  const radii = [h * 1.32, h * 1.5, h * 1.68];
  return radii.map((r, i) => ({
    cx,
    cy,
    rx: r * 1.25,
    ry: r,
    startAngle: Math.PI * 1.05,
    endAngle: Math.PI * 1.95,
    strokeStyle: i === 1 ? 'rgba(53,201,216,0.09)' : 'rgba(90,150,180,0.06)',
    lineWidth: 1,
    dash: i === 2 ? [2, 7] : [],
  }));
}

// ---------------------------------------------------------------------------
// Form validation
// ---------------------------------------------------------------------------

export const OPERATOR_REQUIRED_ERROR = 'OPERATOR ID AND PASSPHRASE REQUIRED';
export const NODE_KEY_REQUIRED_ERROR = 'PEER ID OR MULTIADDR REQUIRED';
export const NODE_KEY_FORMAT_ERROR = 'UNRECOGNIZED PEER KEY FORMAT';

/** Recognized node peer-key / multiaddr prefixes, per `Login.dc.html`. */
export const NODE_KEY_PREFIXES = ['16Uiu', '12D3Koo', '/ip4', '/dns'] as const;

/** Returns an error message, or `null` when the operator form is valid. */
export function validateOperatorForm(opId: string, pass: string): string | null {
  if (!opId.trim() || !pass) return OPERATOR_REQUIRED_ERROR;
  return null;
}

export function isRecognizedNodeKey(key: string): boolean {
  return NODE_KEY_PREFIXES.some((prefix) => key.startsWith(prefix));
}

/** Returns an error message, or `null` when the node-key form is valid. */
export function validateNodeKeyForm(nodeKey: string): string | null {
  const k = nodeKey.trim();
  if (!k) return NODE_KEY_REQUIRED_ERROR;
  if (!isRecognizedNodeKey(k)) return NODE_KEY_FORMAT_ERROR;
  return null;
}

// ---------------------------------------------------------------------------
// Mock staged-auth sequence
// ---------------------------------------------------------------------------

/** Timings (ms, relative to auth start) from `Login.dc.html`'s `startAuth()`. */
export const AUTH_STEP_TIMINGS_MS = {
  step1: 700,
  step2: 1450,
  complete: 2150,
  redirect: 2900,
} as const;

export type AuthPhase = 'idle' | 'auth' | 'ok';

export function operatorStepLabels(): readonly [string, string, string] {
  return ['CREDENTIAL CHECK', 'P2P HANDSHAKE', 'SESSION SEALED'];
}

export function nodeStepLabels(): readonly [string, string, string] {
  return ['PEER KEY VERIFY', 'P2P HANDSHAKE', 'SESSION SEALED'];
}

export interface AuthStepView {
  label: string;
  glyph: string;
  anim: string;
  color: string;
  labelColor: string;
  status: string;
}

/**
 * Build the 3 step-row view models for the current `step` index, matching
 * `Login.dc.html`'s `renderVals().authSteps` exactly: pending `·` (queued,
 * `#44586a`) / active `◌` (running, spinning, `#35c9d8`) / done `✓` (ok,
 * `#5ad6a0`).
 */
export function buildAuthSteps(labels: readonly string[], step: number): AuthStepView[] {
  return labels.map((label, i) => {
    const done = step > i;
    const active = step === i;
    return {
      label,
      glyph: done ? '✓' : active ? '◌' : '·',
      anim: active ? 'sa-spin 0.9s linear infinite' : 'none',
      color: done ? '#5ad6a0' : active ? '#35c9d8' : '#44586a',
      labelColor: done || active ? '#cfe3ec' : '#5a7a8a',
      status: done ? 'OK' : active ? 'RUNNING' : 'QUEUED',
    };
  });
}

// ---------------------------------------------------------------------------
// U1.2 — real-auth dwell timing + operator-tab stage mapping
// ---------------------------------------------------------------------------

/**
 * Anti-flash floor (ms) for a single step row: real transitions (challenge
 * fetch, verify round-trip, node-key resolve) can complete faster than a
 * human can register the row changing state. Kept well under the ≤300ms
 * ceiling from the U1.2 task spec.
 */
export const MIN_STEP_DWELL_MS = 220;

/** Floor for the whole 3-row sequence (`screens/LoginScreen.svelte` applies this once, not per-row — see its comment). */
export const MIN_SEQUENCE_DWELL_MS = MIN_STEP_DWELL_MS * 3;

/** How long the terminal "ACCESS GRANTED" / "PEER VERIFIED" banner stays up before navigating away. */
export const GRANTED_DWELL_MS = 380;

/** Remaining wait (ms, never negative) before `minDwellMs` has elapsed since a display change. */
export function remainingDwellMs(elapsedMs: number, minDwellMs: number = MIN_STEP_DWELL_MS): number {
  if (!Number.isFinite(elapsedMs) || elapsedMs < 0) return minDwellMs;
  return Math.max(0, minDwellMs - elapsedMs);
}

/**
 * Maps `AuthStore.state.stage` (`lib/auth/auth-store.ts`) to the operator
 * tab's 0-based step index: CREDENTIAL CHECK (local wallet unlock/signing +
 * the challenge fetch) → P2P HANDSHAKE (signature verify round-trip) →
 * SESSION SEALED (confirmed via `auth/me`, published by `loginWithWallet`'s
 * trailing `hydrate()`). `idle` covers both "hasn't started" and "still
 * unlocking the wallet locally" (the store has nothing to report yet).
 */
export function operatorStepIndexForStage(stage: AuthStage): number {
  switch (stage) {
    case 'confirmed':
      return 2;
    case 'verify':
      return 1;
    case 'challenge':
    case 'idle':
    default:
      return 0;
  }
}

// ---------------------------------------------------------------------------
// U1.2 — real-auth error text
// ---------------------------------------------------------------------------

/**
 * Shown when `unlockLocalWallet` reports no stored wallet for the given
 * label (D1: "operator ID" selects a LOCAL wallet, "passphrase" decrypts
 * it). SpaceAware has no wallet-creation screen yet, so this is the honest
 * current-state message rather than a fabricated success path.
 */
export const NO_LOCAL_WALLET_ERROR =
  'NO LOCAL WALLET FOR THIS OPERATOR ID · WALLET CREATION IS NOT YET AVAILABLE IN THIS UI';

const NO_LOCAL_WALLET_MESSAGE_PATTERN = /no local wallet labeled/i;

/** Turns a thrown error from the real auth/resolve flow into the uppercase banner text `Login.dc.html`'s error row expects. */
export function describeAuthFailure(err: unknown): string {
  if (err instanceof LocalWalletError) {
    if (NO_LOCAL_WALLET_MESSAGE_PATTERN.test(err.message)) return NO_LOCAL_WALLET_ERROR;
    return err.message.toUpperCase();
  }
  if (err instanceof SdnApiError) {
    // `err.message` is never empty — SdnApiError's own constructor already
    // synthesizes `HTTP <status>` when there is no JSON body.
    return (err.body?.message || err.message).toUpperCase();
  }
  if (err instanceof Error) return err.message.toUpperCase();
  return 'AUTHENTICATION FAILED';
}

// ---------------------------------------------------------------------------
// U1.2 — node-key tab: peer-ID extraction + EPM identity resolution (D2 v1)
// ---------------------------------------------------------------------------

export const NODE_KEY_NO_PEER_ID_ERROR = 'MULTIADDR HAS NO EMBEDDED PEER ID (/p2p/<id>) TO RESOLVE';

/**
 * Extracts a bare libp2p peer ID from a node-key form value already accepted
 * by `validateNodeKeyForm`: either the peer ID itself (`16Uiu…`/`12D3Koo…`
 * prefix) or a multiaddr with a trailing `/p2p/<id>` component. Returns
 * `null` for a well-formed multiaddr with no embedded peer ID (e.g. a bare
 * `/dns/…/tcp/4001`) — `GET /api/v1/peers/{peerId}` has nothing to resolve
 * in that case.
 */
export function extractPeerIdFromKey(key: string): string | null {
  const trimmed = key.trim();
  const marker = '/p2p/';
  const idx = trimmed.lastIndexOf(marker);
  if (idx >= 0) {
    let rest = trimmed.slice(idx + marker.length);
    const slash = rest.indexOf('/');
    if (slash >= 0) rest = rest.slice(0, slash);
    return rest || null;
  }
  if (trimmed.startsWith('16Uiu') || trimmed.startsWith('12D3Koo')) {
    return trimmed;
  }
  return null;
}

export interface ResolvedPeerIdentity {
  peerId: string;
  /** EPM `dn` (distinguished/display name), when the peer surface returns one. */
  dn: string | null;
  /** Whether the resolved record carries a non-empty EPM self-signature. */
  signed: boolean;
  /** Upper-cased `address_type` of the peer's signing key (e.g. `ED25519`), when present. */
  keyAlgorithm: string | null;
}

const DN_FIELDS = ['dn', 'DN', 'display_name', 'legal_name'];

/**
 * Normalizes the JSON body of `GET /api/v1/peers/{peerId}` into a peer
 * identity view. Field names follow the EPM directory-record shape emitted
 * by `sdn-server/internal/epm/directory_record.go` and `service.go`'s
 * `GetNodeEPMJSON` (`dn`, `signature`, `keys[].{key_type,address_type}`,
 * signing key `address_type: "ed25519"`) when a discovery flow serves the
 * peer read surface. The legacy native `getPeer` handler (no flow mounted)
 * returns only `peer_id`/`addrs`/`connection_count` for a CONNECTED peer —
 * every field here degrades to `null`/`false` gracefully rather than
 * throwing, so both server shapes render something honest.
 */
export function resolvePeerIdentity(peerId: string, payload: unknown): ResolvedPeerIdentity {
  const rec: Record<string, unknown> = isPlainRecord(payload) ? payload : {};
  const dn = pickString(rec, DN_FIELDS) ?? null;
  const signatureValue = pickString(rec, ['signature', 'SIGNATURE']);

  const keys = Array.isArray(rec.keys) ? rec.keys : [];
  const signingKeys = keys.filter(
    (k): k is Record<string, unknown> => isPlainRecord(k) && k.key_type === 'signing',
  );
  const ed25519Key = signingKeys.find((k) => k.address_type === 'ed25519');
  const algoSource = ed25519Key ?? signingKeys[0];
  const algorithm = algoSource ? pickString(algoSource, ['address_type']) : undefined;

  return {
    peerId,
    dn,
    signed: Boolean(signatureValue),
    keyAlgorithm: algorithm ? algorithm.toUpperCase() : null,
  };
}

/** Compact `DN · SIGNED/UNSIGNED · ALGORITHM` line for the node-key tab's granted panel. */
export function formatPeerIdentitySummary(view: ResolvedPeerIdentity): string {
  const label = view.dn ?? view.peerId;
  const signedLabel = view.signed ? 'SIGNED' : 'UNSIGNED';
  const algoLabel = view.keyAlgorithm ?? 'KEY UNKNOWN';
  return `${label} · ${signedLabel} · ${algoLabel}`;
}

// ---------------------------------------------------------------------------
// U1.2 — node telemetry (bottom-left panel) + footer identity
// ---------------------------------------------------------------------------

export type NodeHealthStatus = 'NOMINAL' | 'DEGRADED' | 'ALERT';

/** Maps `GET /api/v1/data/health`'s `status` field to the network chip's tri-state. Anything unrecognized (or absent) reads as `ALERT`. */
export function networkStatusFromHealth(status: string | null | undefined): NodeHealthStatus {
  const s = (status ?? '').trim().toLowerCase();
  if (s === 'ok' || s === 'healthy' || s === 'nominal') return 'NOMINAL';
  if (s === 'degraded' || s === 'warn' || s === 'warning') return 'DEGRADED';
  return 'ALERT';
}

export interface StatsSnapshot {
  totalRecords: number | null;
  connectedPeers: number | null;
  schemaCount: number | null;
}

/** Parses `GET /api/v1/stats` (`sdn-server/internal/api/coreapi.go` `handleStats`). */
export function parseStatsResponse(payload: unknown): StatsSnapshot {
  const rec: Record<string, unknown> = isPlainRecord(payload) ? payload : {};
  return {
    totalRecords: pickFiniteNumber(rec, ['total_records']),
    connectedPeers: pickFiniteNumber(rec, ['connected_peers']),
    schemaCount: Array.isArray(rec.schemas) ? rec.schemas.length : null,
  };
}

/** Parses `GET /api/v1/data/health` (`sdn-server/internal/api/data.go` `handleHealth`). */
export function parseHealthResponse(payload: unknown): NodeHealthStatus {
  const rec: Record<string, unknown> = isPlainRecord(payload) ? payload : {};
  return networkStatusFromHealth(typeof rec.status === 'string' ? rec.status : null);
}

export interface NodeInfoSnapshot {
  peerId: string | null;
  agentVersion: string | null;
}

/** Parses `GET /api/node/info` (`sdn-server/cmd/spacedatanetwork/main.go` `handleNodeInfo`). */
export function parseNodeInfoResponse(payload: unknown): NodeInfoSnapshot {
  const rec: Record<string, unknown> = isPlainRecord(payload) ? payload : {};
  return {
    peerId: pickString(rec, ['peer_id']) ?? null,
    agentVersion: pickString(rec, ['agent_version']) ?? null,
  };
}

/** `31,000`-style grouped count, or an em dash placeholder when unavailable. */
export function formatTelemetryCount(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—';
  return Math.trunc(value).toLocaleString('en-US');
}

/** `3 CONNECTED`-style PEERS row value. */
export function formatPeersConnected(connectedPeers: number | null | undefined): string {
  if (connectedPeers == null || !Number.isFinite(connectedPeers)) return '—';
  return `${Math.trunc(connectedPeers)} CONNECTED`;
}

/** `9 SYNCED`-style FEEDS row value. */
export function formatFeedsSynced(schemaCount: number | null | undefined): string {
  if (schemaCount == null || !Number.isFinite(schemaCount)) return '—';
  return `${Math.trunc(schemaCount)} SYNCED`;
}

/**
 * Shortens a peer ID for the fixed-width footer strip, e.g.
 * `16Uiu2HAm1Lbvwj…Z5Fm45`. Falls back to `UNKNOWN` — never fabricates one.
 */
export function shortenPeerId(peerId: string | null | undefined): string {
  const v = (peerId ?? '').trim();
  if (!v) return 'UNKNOWN';
  if (v.length <= 24) return v;
  return `${v.slice(0, 15)}…${v.slice(-6)}`;
}

/**
 * `THIS NODE · 16Uiu2HAm1Lbvwj…Z5Fm45`-style footer label. U1.2 note: the
 * U1.1 mock also hardcoded a `· COLORADO SPRINGS, US` suffix; a stock node
 * has no geolocation data source, so that suffix is DROPPED here rather
 * than fabricated (see `screens/LoginScreen.svelte`'s footer markup).
 */
export function formatFooterNodeLabel(peerId: string | null | undefined): string {
  return `THIS NODE · ${shortenPeerId(peerId)}`;
}

/**
 * `agent_version` is `"<name>/<suiteVersion>"` (`sdn-server`
 * `internal/versioninfo.AgentVersion`, e.g. `spacedatanetwork/1.4.2`) — take
 * the part after the last `/` for the `NODE v1.4.2`-style footer label.
 */
export function formatAgentVersionLabel(agentVersion: string | null | undefined): string {
  const v = (agentVersion ?? '').trim();
  if (!v) return 'NODE VERSION UNKNOWN';
  const slash = v.lastIndexOf('/');
  const version = slash >= 0 ? v.slice(slash + 1) : v;
  return `NODE v${version}`;
}

// ---------------------------------------------------------------------------
// Small JSON helpers shared by the parsers above
// ---------------------------------------------------------------------------

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function pickString(record: Record<string, unknown>, keys: string[]): string | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string') {
      const trimmed = value.trim();
      if (trimmed) return trimmed;
    }
  }
  return undefined;
}

function pickFiniteNumber(record: Record<string, unknown>, keys: string[]): number | null {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'number' && Number.isFinite(value)) return value;
  }
  return null;
}
