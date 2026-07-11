/**
 * CONJUNCTION console view (loop task U3.9). Ground truth: the
 * `<!-- ============ CONJUNCTION ============ -->` block in
 * `design_handoff/sdn_console/SDN Console.dc.html` (lines ~587-742) — a red-
 * accent "CONJUNCTION TASK · CONFIGURE" panel (SCREEN TARGET pill row +
 * status strip, then three numbered columns: ① DATA SOURCES precedence
 * stack, ② PROPAGATOR radio cards, ③ SCREENING CRITERIA steppers, plus a
 * LIVE STREAM STATUS card and a ONE-OFF RUN popover) above a SCREENING
 * RESULTS panel (TABLE/JSON/CSV) and a PROVENANCE · ENCRYPTED WORKFLOW
 * panel — and its `class Component extends DCLogic` logic block (lines
 * ~771-790, ~1187-1243: `CONJ_SOURCES`, `CONJ_PROPS`, `moveSource`,
 * `toggleSource`, `bumpCrit`, `cyclePc`, `toggleLive`/`toggleOneOff`/
 * `runOneOff`, `stateOf`, `conjSourceRows`/`conjPropRows`/`conjTargetPills`/
 * `conjRows` builders). See `ConjunctionView.svelte` and its sibling panel
 * components for the pixel-level styling port.
 *
 * DECISION D4 (demo-mode, real sources) — this view mixes two honesty
 * tiers, unlike GROUPS (loop U3.8, `groups-data.ts`) which is client-local
 * throughout:
 *
 *   1. SCREEN TARGET (pills + status strip) and ① DATA SOURCES are REAL,
 *      driven by actual local/daemon surfaces — see each section below.
 *   2. ② PROPAGATOR / ③ SCREENING CRITERIA are honest CLIENT-SIDE UI STATE
 *      (steppers/radios that don't fabricate any backing computation — no
 *      screening engine reads them on this build, but the state itself
 *      isn't a lie, it's just an unused configuration knob).
 *   3. The LIVE STREAM STATUS card, SCREENING RESULTS, and PROVENANCE panel
 *      are FABRICATED demo data (verbatim from the mock's `CONJ` fixture
 *      and static provenance fields) — every one of those three surfaces
 *      carries a visible DEMO tag (`CONJUNCTION_*_DEMO_TAG_TITLE` below,
 *      styled like `Bmc2TopBar.svelte`'s `.bmc-demo-tag` / `groups-data.ts`'s
 *      `GROUPS_*_DEMO_TAG_TITLE`) because no conjunction-screening engine is
 *      wired to any UI surface on this build (same root cause as GROUPS'
 *      own conjunction-status demo tag).
 *
 * ① DATA SOURCES — real-surface wiring (NOT the mock's fabricated
 * `CONJ_SOURCES` fixture, which invents an `mpe`/`saCat`/`celestrak`/`local`
 * four-row stack unconditionally):
 *
 *   - `GET /api/v1/stats` (`node-data.ts`'s `parseNodeStats`) backs the ONE
 *     row that is ALWAYS real on this build: "Local SDN Catalog · CATALOG ·
 *     LOCAL STORE" — `stats.totalRecords` renders in the row's tooltip
 *     (never fabricated as "N records" text in the row itself, since a
 *     failed stats fetch has no honest count to show inline).
 *   - `GET /api/v1/peers` has NO `dn`/EPM surface for peers today (see
 *     `peers-data.ts`'s `RawPeer`, which never parses one) — so
 *     `parseConjunctionSourcePeers` below is a FORWARD-COMPATIBLE parser
 *     that defensively reads a `dn` field per peer anyway (never present on
 *     a real fetch today, always `null`) rather than a dead branch: the
 *     moment a peer-EPM surface ships, `buildSourceRows` starts rendering a
 *     real "CATALOG · PEER PROVIDER" row per identified peer with ZERO code
 *     changes here. Proven with a synthetic fixture in the test file, since
 *     no live peer carries this field yet.
 *   - `GET /api/v1/channels` (`channels-data.ts`'s `parseChannelsCollection`)
 *     has NO private/sealed MPE channel on this build (every real channel
 *     is `visibility:'public'`, `encryptionState:'none'` — see that file's
 *     own doc comment). `buildSourceRows` still checks for a
 *     `standardCode:'MPE'` row with `encryptionState:'encrypted'` (or a
 *     `visibility` starting with `'private'`) and, when one exists, renders
 *     the padlocked "SpaceAware MPE" ephemeris row with a `PRIVATE ·
 *     SEALED` tag — proven with a synthetic fixture, never fabricated live.
 *
 *   Net effect: `buildSourceRows(peers, channels, stats)` against this
 *   build's REAL data renders exactly one row (Local SDN Catalog) — never
 *   the mock's four-row stack. Precedence reorder (`moveSourceOrder`) and
 *   on/off toggling (`toggleSourceOff`) work client-side on whatever rows
 *   are actually present, same as the mock's own `moveSource`/`toggleSource`.
 *
 * ② PROPAGATOR — SGP4/SDP4 are honest, selectable, unused (no propagation
 * engine reads the selection). Numerical stays PAID-locked exactly like the
 * mock (`paid:true`), but per this build's honesty rules (no storefront
 * listing exists for it — see `peers-data.ts`'s `markPaidProviders` finding
 * that `POST /api/storefront/listings/search` returns `listings:null`
 * today) `selectPropagator` refuses to select it and the row renders
 * `disabled` with an explanatory tooltip rather than silently doing
 * nothing.
 *
 * ③ SCREENING CRITERIA — `bumpMissDistance`/`bumpScreenWindow`/
 * `bumpHardBodyRadius`/`bumpStepSize`/`cyclePcThreshold` port the mock's
 * `bumpCrit`/`cyclePc` deltas and clamp ranges verbatim (`SDN
 * Console.dc.html:785-786,1210-1214`).
 *
 * SCREENING RESULTS — `CONJUNCTION_RESULTS_FIXTURE` is the mock's `CONJ`
 * array verbatim (`SDN Console.dc.html:921-925`). One deliberate deviation
 * from the mock's own logic (documented, not silently different): the mock
 * recomputes STATE from the live criteria sliders for the TABLE view
 * (`stateOf()`) but renders the JSON/CSV outputs from each fixture row's
 * OWN hardcoded `state` field instead — i.e. its three output modes can
 * disagree with each other once a user nudges a stepper. This view instead
 * derives ALL THREE modes from the same `buildResultRows(criteria)` call
 * (`computeRowState`, a verbatim port of `stateOf()`), so TABLE/JSON/CSV
 * always agree — a strict improvement with no fabrication risk, since this
 * is already demo-tagged fixture data. JSON/CSV rendering itself reuses the
 * U3.6 schema-exact passthrough builders (`buildQueryJsonOutput`/
 * `buildQueryCsvOutput` from `query-data.ts`) rather than duplicating
 * `JSON.stringify`/CSV-join logic.
 *
 * PROVENANCE — the mock's MODE/MODULE/RESULT CHANNEL/GRANT/QUERY HASH/
 * RESULT HASH/enclave-note fields are entirely static fixture text with no
 * `st.`-derived value anywhere in the mock's own `renderVals()` (lines
 * 727-738) — `buildProvenanceView` ports them verbatim as a constant.
 *
 * ONE-OFF RUN — the mock's `toggleOneOff`/`bumpOneOff`/`runOneOff`
 * (`SDN Console.dc.html:788-790`) open a small corner popover with a
 * LOOK-BACK stepper (1-72h, default 6h) and a "RUN ONCE" button that just
 * flips `oneOffRan:true` and closes the popover — there is no real backfill
 * screening surface behind it, so (loop task's own directive: "all
 * client-side demo, DEMO-tagged") this view adds a DEMO tag to the popover
 * header that the mock itself doesn't have, since "RUN ONCE" completing
 * instantly with a canned "done" message would otherwise read as a real
 * computation. The `▸` glyph on the "ONE-OFF RUN" button is kept verbatim
 * (pixel ground truth) even though this app's no-arrow-glyph convention
 * normally bans arrows that fake navigation affordance — this one opens an
 * inline popover, not a navigation, and is explicitly called out as a
 * ground-truth exception in the loop task spec.
 *
 * LIVE STREAM STATUS — `streamTick` ticks client-side every 2.6s while
 * `live` is true (`SDN Console.dc.html:931`'s `setInterval`), driving a
 * fabricated "Δ Ns ago · N deltas" counter (`lastDelta`, line 1225) ported
 * verbatim by `formatLastDeltaLabel`. PAUSE/RESUME STREAM
 * (`toggleLive`/`liveBtnLabel`) only stops/starts that local animation —
 * never a real subscription — so the LIVE card and its DEMO tag stay
 * adjacent per the loop task's directive ("the animation can't be mistaken
 * for real telemetry").
 */

import type { SdnApiClient } from '../../lib/auth/sdn-api-client';
import { parseNodeStats, truncateMiddle, type NodeStatsSnapshot } from './node-data';
import { parseChannelsCollection, type ChannelCollectionRow } from './channels-data';
import { buildQueryCsvOutput, buildQueryJsonOutput, type QueryOutputMode } from './query-data';
import { groupColor, groupGlyph, groupOrbitalPath, type SharedGroup } from './groups-data';

// ---------------------------------------------------------------------------
// Small JSON helpers (mirrors node-data.ts/peers-data.ts/channels-data.ts's
// private helpers — not exported from there, so duplicated narrowly here,
// same rationale as those files' own doc comments).
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

function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, value));
}

// ---------------------------------------------------------------------------
// ① DATA SOURCES — real-surface parsing + source-stack synthesis
// ---------------------------------------------------------------------------

export interface ConjunctionSourcePeer {
  peerId: string;
  /** See file doc comment — always `null` on a real `/api/v1/peers` fetch today; kept parseable for forward compatibility. */
  dn: string | null;
}

/** Reads `GET /api/v1/peers`'s `{"peers":[...]}` envelope, defensively picking a `dn` field per entry (see file doc comment — no live peer carries one yet). Entries with no `peer_id` are dropped. */
export function parseConjunctionSourcePeers(payload: unknown): ConjunctionSourcePeer[] {
  const rec = isPlainRecord(payload) ? payload : {};
  const list = Array.isArray(rec.peers) ? rec.peers : [];
  return list
    .filter(isPlainRecord)
    .map((p) => ({ peerId: pickString(p, 'peer_id') ?? '', dn: pickString(p, 'dn') }))
    .filter((p) => p.peerId);
}

export const LOCAL_CATALOG_SOURCE_ID = 'local';

export type ConjunctionSourceType = 'EPHEMERIS' | 'CATALOG';

export interface ConjunctionSourceRow {
  id: string;
  name: string;
  type: ConjunctionSourceType;
  tag: string;
  tagColor: string;
  /** Single-glyph lock/dot marker (mock's `enc`). */
  enc: string;
  /** Tooltip text — carries the real record count for the local catalog row (see file doc comment). */
  detail: string;
}

const LOCAL_TAG_COLOR = '#9fb3bc';
const PEER_CATALOG_TAG_COLOR = '#35c9d8';
const SEALED_TAG_COLOR = '#ff9b9b';

function isSealedMpeChannel(channel: ChannelCollectionRow): boolean {
  if (channel.standardCode.trim().toUpperCase() !== 'MPE') return false;
  const encryption = channel.encryptionState.trim().toLowerCase();
  const visibility = channel.visibility.trim().toLowerCase();
  return encryption === 'encrypted' || visibility.startsWith('private');
}

function localCatalogDetail(stats: NodeStatsSnapshot | null): string {
  const n = stats?.totalRecords;
  return n != null ? `${n.toLocaleString()} records synced locally` : 'record count unavailable — /api/v1/stats did not respond';
}

/**
 * Synthesizes the DATA SOURCES precedence stack from real surfaces only
 * (see file doc comment). Default row order (highest precedence first,
 * matching the mock's own `mpe, saCat, celestrak, local` default): any
 * sealed MPE ephemeris row(s), then any peer-catalog row(s), then the
 * always-present Local SDN Catalog row last. Never fabricates a row for a
 * surface that doesn't actually exist — on this build's real data (no
 * peer-EPM, no sealed MPE channel) this always returns exactly one row.
 */
export function buildSourceRows(
  peers: readonly ConjunctionSourcePeer[],
  channels: readonly ChannelCollectionRow[],
  stats: NodeStatsSnapshot | null,
): ConjunctionSourceRow[] {
  const rows: ConjunctionSourceRow[] = [];

  for (const channel of channels) {
    if (!isSealedMpeChannel(channel)) continue;
    rows.push({
      id: `mpe:${channel.standardCode}`,
      name: 'SpaceAware MPE',
      type: 'EPHEMERIS',
      tag: 'PRIVATE · SEALED',
      tagColor: SEALED_TAG_COLOR,
      enc: '🔒',
      detail: `sealed channel · ${channel.topic || channel.standardCode}`,
    });
  }

  for (const peer of peers) {
    if (!peer.dn) continue;
    rows.push({
      id: `peer:${peer.peerId}`,
      name: peer.dn,
      type: 'CATALOG',
      tag: 'PEER PROVIDER',
      tagColor: PEER_CATALOG_TAG_COLOR,
      enc: '◈',
      detail: `peer catalog · ${truncateMiddle(peer.peerId)}`,
    });
  }

  rows.push({
    id: LOCAL_CATALOG_SOURCE_ID,
    name: 'Local SDN Catalog',
    type: 'CATALOG',
    tag: 'LOCAL STORE',
    tagColor: LOCAL_TAG_COLOR,
    enc: '●',
    detail: localCatalogDetail(stats),
  });

  return rows;
}

// ---------------------------------------------------------------------------
// Precedence reorder + on/off toggle (client-side UI state — mock's
// `moveSource`/`toggleSource`, `SDN Console.dc.html:782-783`)
// ---------------------------------------------------------------------------

/** Swaps `id` with its adjacent neighbor (`direction: -1` raises precedence / `+1` lowers it). No-op past either end or for an unknown id — matches the mock's silent no-op at the array boundary. */
export function moveSourceOrder(order: readonly string[], id: string, direction: -1 | 1): string[] {
  const i = order.indexOf(id);
  if (i < 0) return order.slice();
  const j = i + direction;
  if (j < 0 || j >= order.length) return order.slice();
  const next = order.slice();
  const tmp = next[i]!;
  next[i] = next[j]!;
  next[j] = tmp;
  return next;
}

export function toggleSourceOff(off: Readonly<Record<string, boolean>>, id: string): Record<string, boolean> {
  return { ...off, [id]: !off[id] };
}

export interface ConjunctionSourceRowView extends ConjunctionSourceRow {
  precedence: number;
  off: boolean;
  rowOpacity: string;
  nameColor: string;
  tagColorEffective: string;
  toggleBg: string;
  toggleBorder: string;
  toggleKnob: string;
  toggleKnobBg: string;
  canMoveUp: boolean;
  canMoveDown: boolean;
}

/**
 * Zips the real source rows with the client's precedence `order` and on/off
 * `off` map into display rows. `order` need not already match `rows` 1:1 —
 * any id in `order` no longer present in `rows` is dropped, and any row not
 * yet in `order` (a source that just appeared, e.g. after a re-fetch) is
 * appended at the end — so a stale order never hides a newly-available
 * source or crashes on a since-removed one.
 */
export function buildSourceRowViews(
  rows: readonly ConjunctionSourceRow[],
  order: readonly string[],
  off: Readonly<Record<string, boolean>>,
): ConjunctionSourceRowView[] {
  const byId = new Map(rows.map((r) => [r.id, r]));
  const known = order.filter((id) => byId.has(id));
  const knownSet = new Set(known);
  const missing = rows.filter((r) => !knownSet.has(r.id)).map((r) => r.id);
  const finalOrder = known.concat(missing);
  return finalOrder.map((id, i) => {
    const row = byId.get(id)!;
    const isOff = !!off[id];
    return {
      ...row,
      precedence: i + 1,
      off: isOff,
      rowOpacity: isOff ? '0.5' : '1',
      nameColor: isOff ? '#6f8693' : '#eaf6f8',
      tagColorEffective: isOff ? '#5a6a72' : row.tagColor,
      toggleBg: isOff ? 'transparent' : 'rgba(53,201,216,0.25)',
      toggleBorder: isOff ? 'rgba(110,170,190,0.35)' : '#35c9d8',
      toggleKnob: isOff ? '1px' : '11px',
      toggleKnobBg: isOff ? '#7d929b' : '#eaf6f8',
      canMoveUp: i > 0,
      canMoveDown: i < finalOrder.length - 1,
    };
  });
}

export const CONJUNCTION_SOURCES_PRECEDENCE_FOOTNOTE = 'Higher rows override lower when an object appears in multiple sources.';

// ---------------------------------------------------------------------------
// ② PROPAGATOR (mock's `CONJ_PROPS`/`selectConjProp`, `SDN
// Console.dc.html:777-781,784,1208`)
// ---------------------------------------------------------------------------

export type ConjunctionPropagatorKey = 'sgp4' | 'sdp4' | 'num';

export interface ConjunctionPropagatorSpec {
  key: ConjunctionPropagatorKey;
  name: string;
  detail: string;
  paid: boolean;
}

/** Verbatim `CONJ_PROPS` order from the mock. */
export const CONJUNCTION_PROPAGATORS: readonly ConjunctionPropagatorSpec[] = [
  { key: 'sgp4', name: 'SGP4', detail: 'GP · LEO / MEO', paid: false },
  { key: 'sdp4', name: 'SDP4', detail: 'deep-space GP · GEO / HEO', paid: false },
  { key: 'num', name: 'Numerical', detail: 'Cowell · high-precision', paid: true },
];

export const CONJUNCTION_DEFAULT_PROPAGATOR: ConjunctionPropagatorKey = 'sgp4';

export const CONJUNCTION_NUMERICAL_PAID_TOOLTIP =
  'Numerical propagation is a paid module — no storefront listing exists for it on this build yet.';

export interface ConjunctionPropagatorRowView {
  key: ConjunctionPropagatorKey;
  name: string;
  detail: string;
  paid: boolean;
  selected: boolean;
  disabled: boolean;
  tooltip: string;
  rowBg: string;
  rowBorder: string;
  radioBorder: string;
  radioDot: string;
  nameColor: string;
  stateLabel: string;
  stateColor: string;
}

export function buildPropagatorRows(selected: ConjunctionPropagatorKey): ConjunctionPropagatorRowView[] {
  return CONJUNCTION_PROPAGATORS.map((p) => {
    const isSelected = p.key === selected;
    return {
      key: p.key,
      name: p.name,
      detail: p.detail,
      paid: p.paid,
      selected: isSelected,
      disabled: p.paid,
      tooltip: p.paid ? CONJUNCTION_NUMERICAL_PAID_TOOLTIP : `Use ${p.name} for this screening run.`,
      rowBg: isSelected ? 'rgba(53,201,216,0.08)' : 'rgba(255,255,255,0.02)',
      rowBorder: isSelected ? 'rgba(53,201,216,0.45)' : 'rgba(90,150,180,0.18)',
      radioBorder: isSelected ? '#35c9d8' : 'rgba(110,170,190,0.4)',
      radioDot: isSelected ? '#35c9d8' : 'transparent',
      nameColor: isSelected ? '#eaf6f8' : '#9fb3bc',
      stateLabel: isSelected ? 'IN USE' : '',
      stateColor: isSelected ? '#35c9d8' : '#5a7a8a',
    };
  });
}

/** Selects `key`, unless it names a paid (locked) propagator — clicking Numerical does nothing but the tooltip explains why (see file doc comment). Returns `current` unchanged for an unknown key too. */
export function selectPropagator(current: ConjunctionPropagatorKey, key: ConjunctionPropagatorKey): ConjunctionPropagatorKey {
  const spec = CONJUNCTION_PROPAGATORS.find((p) => p.key === key);
  if (!spec || spec.paid) return current;
  return key;
}

export function propagatorName(key: ConjunctionPropagatorKey): string {
  return CONJUNCTION_PROPAGATORS.find((p) => p.key === key)?.name ?? '—';
}

// ---------------------------------------------------------------------------
// ③ SCREENING CRITERIA steppers (mock's `bumpCrit`/`cyclePc`, `SDN
// Console.dc.html:785-786,1209-1214`)
// ---------------------------------------------------------------------------

export interface ConjunctionCriteria {
  miss: number;
  pcExp: number;
  window: number;
  hbr: number;
  step: number;
}

/** Verbatim `state.conjCrit` initial values from the mock. */
export const CONJUNCTION_DEFAULT_CRITERIA: ConjunctionCriteria = { miss: 5, pcExp: 4, window: 72, hbr: 20, step: 60 };

export function bumpMissDistance(criteria: ConjunctionCriteria, delta: number): ConjunctionCriteria {
  const v = Math.round((criteria.miss + delta) * 100) / 100;
  return { ...criteria, miss: clamp(v, 0.5, 50) };
}

export function bumpScreenWindow(criteria: ConjunctionCriteria, delta: number): ConjunctionCriteria {
  return { ...criteria, window: clamp(criteria.window + delta, 12, 336) };
}

export function bumpHardBodyRadius(criteria: ConjunctionCriteria, delta: number): ConjunctionCriteria {
  return { ...criteria, hbr: clamp(criteria.hbr + delta, 5, 200) };
}

export function bumpStepSize(criteria: ConjunctionCriteria, delta: number): ConjunctionCriteria {
  return { ...criteria, step: clamp(criteria.step + delta, 30, 600) };
}

/** Verbatim `opts=[3,4,5,6]` fixed cycle from the mock's `cyclePc`. */
const PC_THRESHOLD_EXPONENTS: readonly number[] = [3, 4, 5, 6];

export function cyclePcThreshold(criteria: ConjunctionCriteria): ConjunctionCriteria {
  const i = PC_THRESHOLD_EXPONENTS.indexOf(criteria.pcExp);
  const next = PC_THRESHOLD_EXPONENTS[(i + 1) % PC_THRESHOLD_EXPONENTS.length]!;
  return { ...criteria, pcExp: next };
}

export function formatMissDistanceLabel(criteria: ConjunctionCriteria): string {
  return criteria.miss.toFixed(1);
}

export function formatPcThresholdLabel(criteria: ConjunctionCriteria): string {
  return `1e-${criteria.pcExp}`;
}

// ---------------------------------------------------------------------------
// SCREENING RESULTS — demo fixture (mock's `CONJ`, `SDN
// Console.dc.html:921-925`) + criteria-driven state (mock's `stateOf`, line
// 1197) — see file doc comment for the TABLE/JSON/CSV-agreement deviation.
// ---------------------------------------------------------------------------

export interface ConjunctionEventFixture {
  object: string;
  tca: string;
  miss: string;
  pc: string;
}

/** Verbatim `CONJ` fixture array from the mock. */
export const CONJUNCTION_RESULTS_FIXTURE: readonly ConjunctionEventFixture[] = [
  { object: 'SAT-39210', tca: '2026-06-26T11:55:00Z', miss: '0.42', pc: '7.3e-4' },
  { object: 'SAT-44713', tca: '2026-06-25T18:42:00Z', miss: '1.84', pc: '2.1e-5' },
  { object: 'SAT-57944', tca: '2026-06-26T03:10:00Z', miss: '8.92', pc: '4.8e-7' },
];

export type ConjunctionRowState = 'WARN' | 'REVIEW' | 'CLEAR';

/** Verbatim port of the mock's `stateOf(mv,pv)` — `pcThresh = 10^-pcExp`. */
export function computeRowState(missKm: number, pc: number, criteria: ConjunctionCriteria): ConjunctionRowState {
  const pcThreshold = 10 ** -criteria.pcExp;
  if (missKm <= criteria.miss && pc >= pcThreshold) return 'WARN';
  if (missKm <= criteria.miss * 2 || pc >= pcThreshold / 10) return 'REVIEW';
  return 'CLEAR';
}

/** Port of the mock's `caStateColor()` — the STATE cell's own text color. */
export function conjunctionStateColor(state: ConjunctionRowState): string {
  if (state === 'WARN') return '#ff6b6b';
  if (state === 'REVIEW') return '#ffb24d';
  return '#5ad6a0';
}

/** Port of the mock's `missColor()` — the MISS km cell's value color (distinct ramp from `conjunctionStateColor`). */
export function conjunctionMissValueColor(state: ConjunctionRowState): string {
  if (state === 'WARN') return '#ff6b6b';
  if (state === 'REVIEW') return '#ffb24d';
  return '#cfe3ec';
}

export interface ConjunctionResultRowView {
  object: string;
  tca: string;
  missLabel: string;
  pc: string;
  state: ConjunctionRowState;
  stateColor: string;
  missColor: string;
}

export function buildResultRows(criteria: ConjunctionCriteria): ConjunctionResultRowView[] {
  return CONJUNCTION_RESULTS_FIXTURE.map((r) => {
    const missKm = Number.parseFloat(r.miss);
    const pc = Number.parseFloat(r.pc);
    const state = computeRowState(missKm, pc, criteria);
    return {
      object: r.object,
      tca: r.tca,
      missLabel: r.miss,
      pc: r.pc,
      state,
      stateColor: conjunctionStateColor(state),
      missColor: conjunctionMissValueColor(state),
    };
  });
}

/** Schema-exact records for the JSON/CSV modes — verbatim key set from the mock's own `conjText` builders (`object,tca,missDistanceKm,pc,state`). */
export function buildResultRecords(criteria: ConjunctionCriteria): Record<string, unknown>[] {
  return buildResultRows(criteria).map((r) => ({
    object: r.object,
    tca: r.tca,
    missDistanceKm: Number(r.missLabel),
    pc: r.pc,
    state: r.state.toLowerCase(),
  }));
}

export const CONJUNCTION_DEFAULT_RESULT_MODE: QueryOutputMode = 'table';

/** Reuses the U3.6 schema-exact JSON builder (`query-data.ts`) — see file doc comment. */
export function buildResultsJsonOutput(criteria: ConjunctionCriteria): string {
  return buildQueryJsonOutput(buildResultRecords(criteria));
}

/** Reuses the U3.6 schema-exact CSV builder (`query-data.ts`) — see file doc comment. */
export function buildResultsCsvOutput(criteria: ConjunctionCriteria): string {
  return buildQueryCsvOutput(buildResultRecords(criteria));
}

export const CONJUNCTION_RESULTS_DEMO_TAG_TITLE =
  'Demo screening results — no conjunction-screening engine is wired to this view; these rows mirror the design mock’s fixture data.';

// ---------------------------------------------------------------------------
// LIVE STREAM STATUS (mock's `screenLive`/`streamTick`/`toggleLive`,
// `SDN Console.dc.html:787,931,1217-1229`)
// ---------------------------------------------------------------------------

/** Mock's `setInterval(..., 2600)` tick period. */
export const CONJUNCTION_STREAM_TICK_INTERVAL_MS = 2600;

export function nextStreamTick(tick: number): number {
  return tick + 1;
}

/** Verbatim port of the mock's `lastDelta` formula. */
export function formatLastDeltaLabel(live: boolean, streamTick: number): string {
  if (!live) return 'no deltas while paused';
  return `Δ ${(streamTick % 8) + 1}s ago · ${1240 + streamTick * 3} deltas`;
}

export interface ConjunctionLiveCardView {
  label: string;
  dotColor: string;
  textColor: string;
  borderColor: string;
  bgColor: string;
  pulseOn: boolean;
  subText: string;
  sourceCountLabel: string;
  propagatorLabel: string;
  lastDeltaLabel: string;
  buttonLabel: string;
  buttonBorderColor: string;
  buttonColor: string;
}

export function buildLiveCardView(
  live: boolean,
  streamTick: number,
  enabledSourceCount: number,
  propagatorLabel: string,
): ConjunctionLiveCardView {
  return {
    label: live ? 'SCREENING · LIVE' : 'STREAM PAUSED',
    dotColor: live ? '#5ad6a0' : '#f0b54a',
    textColor: live ? '#bfe6d4' : '#f0d49a',
    borderColor: live ? 'rgba(90,214,160,0.42)' : 'rgba(240,181,74,0.42)',
    bgColor: live ? 'rgba(90,214,160,0.06)' : 'rgba(240,181,74,0.06)',
    pulseOn: live,
    subText: live ? 'Assessing continuously as encrypted ephemeris arrives.' : 'Halted — incoming ephemeris is not being screened.',
    sourceCountLabel: `${enabledSourceCount} sources`,
    propagatorLabel,
    lastDeltaLabel: formatLastDeltaLabel(live, streamTick),
    buttonLabel: live ? 'PAUSE STREAM' : 'RESUME STREAM',
    buttonBorderColor: live ? 'rgba(240,181,74,0.45)' : 'rgba(90,214,160,0.45)',
    buttonColor: live ? '#f0d49a' : '#bfe6d4',
  };
}

export const CONJUNCTION_LIVE_DEMO_TAG_TITLE =
  'Demo live indicator — this build has no continuous screening engine; the delta counter ticks client-side for demonstration only.';

// ---------------------------------------------------------------------------
// ONE-OFF RUN popover (mock's `oneOffOpen`/`oneOffWin`/`oneOffRan`,
// `SDN Console.dc.html:788-790`) — see file doc comment for the added DEMO
// tag (a deviation from the mock, which has none here).
// ---------------------------------------------------------------------------

export const CONJUNCTION_ONE_OFF_DEFAULT_WINDOW = 6;

export function bumpOneOffWindow(hours: number, delta: number): number {
  return clamp(hours + delta, 1, 72);
}

export function buildOneOffMessage(ran: boolean, windowHours: number): string {
  return ran ? `last backfill · ${windowHours}h window · done` : '';
}

export const CONJUNCTION_ONE_OFF_DEMO_TAG_TITLE =
  'Demo one-off run — this build has no screening engine; running once only flips a local flag, no computation occurs.';

// ---------------------------------------------------------------------------
// SCREEN TARGET pills + status strip (real groups — reuses `groups-data.ts`,
// mock's `conjTargetPills`/`conjTarget`, `SDN Console.dc.html:1200-1206`)
// ---------------------------------------------------------------------------

export interface ConjunctionTargetPillView {
  id: string;
  name: string;
  glyph: string;
  glyphColor: string;
  conjColorDot: string;
  selected: boolean;
  bg: string;
  border: string;
  color: string;
}

const CONJ_DOT_COLORS: Record<string, string> = { clear: '#5ad6a0', watch: '#ffb24d', critical: '#ff6b6b' };

function targetPillConjDot(conj: string): string {
  return CONJ_DOT_COLORS[conj] ?? '#5a7a8a';
}

export function buildTargetPills(groups: readonly SharedGroup[], selectedId: string | null): ConjunctionTargetPillView[] {
  return groups.map((g) => {
    const selected = g.id === selectedId;
    return {
      id: g.id,
      name: g.name,
      glyph: groupGlyph(g),
      glyphColor: groupColor(g),
      conjColorDot: targetPillConjDot(g.conj),
      selected,
      bg: selected ? 'rgba(255,107,107,0.12)' : 'rgba(255,255,255,0.02)',
      border: selected ? 'rgba(255,107,107,0.5)' : 'rgba(90,150,180,0.2)',
      color: selected ? '#ffd2d2' : '#9fb3bc',
    };
  });
}

export interface ConjunctionTargetStripView {
  name: string;
  glyph: string;
  glyphColor: string;
  countLabel: string;
  kindLabel: string;
  kindColor: string;
  ownerName: string;
  openIn3dPath: string;
}

/** Port of the mock's `conjTarget` (line 1205-1206) — "Screening {glyph} {name} · {count} OBJECTS · {kind} defined by {ownerName}" status-strip fields, from a REAL group's own real `count`/`ownerName`. */
export function buildTargetStripView(group: SharedGroup): ConjunctionTargetStripView {
  const isMine = group.owner === 'self';
  return {
    name: group.name,
    glyph: groupGlyph(group),
    glyphColor: groupColor(group),
    countLabel: `${group.count} OBJECTS`,
    kindLabel: isMine ? 'MY GROUP' : 'PEER GROUP',
    kindColor: groupColor(group),
    ownerName: group.ownerName,
    openIn3dPath: groupOrbitalPath(group.id),
  };
}

// ---------------------------------------------------------------------------
// PROVENANCE · ENCRYPTED WORKFLOW (mock's static fixture fields, `SDN
// Console.dc.html:727-738`) — see file doc comment for the DEMO tag.
// ---------------------------------------------------------------------------

export interface ConjunctionProvenanceView {
  mode: string;
  module: string;
  resultChannel: string;
  grant: string;
  queryHash: string;
  resultHash: string;
  enclaveNote: string;
}

const CONJUNCTION_PROVENANCE_FIXTURE: ConjunctionProvenanceView = {
  mode: 'private-maneuver-ephemeris',
  module: 'sdn-ca-screen/1.0.0',
  resultChannel: 'ca-results-private',
  grant: 'grant-mpe-alpha',
  queryHash: 'sha256:designerqueryexample',
  resultHash: 'sha256:designerresultexample',
  enclaveNote:
    'Screening runs in the SpaceAware assessor enclave. Only the signed result returns to your private channel — input MPE is never disclosed.',
};

export function buildProvenanceView(): ConjunctionProvenanceView {
  return { ...CONJUNCTION_PROVENANCE_FIXTURE };
}

export const CONJUNCTION_PROVENANCE_DEMO_TAG_TITLE =
  'Demo provenance — no private-MPE channel or assessor enclave exists on this build; these fields mirror the design mock’s fixture data.';

// ---------------------------------------------------------------------------
// Fetch orchestration — takes the shared SdnApiClient (see
// `../../lib/auth/sdn-api-client.ts`). Every function here swallows its own
// fetch failure (never throws), matching `node-data.ts`/`peers-data.ts`'s
// contract: a missing/unreachable surface degrades to an honest empty/null
// result.
// ---------------------------------------------------------------------------

/** Structural subset of `SdnApiClient` this module needs — lets tests pass a plain fake instead of constructing a real client. */
export type ConjunctionApiClient = Pick<SdnApiClient, 'requestJson'>;

async function fetchConjunctionPeers(apiClient: ConjunctionApiClient): Promise<ConjunctionSourcePeer[]> {
  try {
    const result = await apiClient.requestJson<unknown>('/peers');
    return parseConjunctionSourcePeers(result.data);
  } catch {
    return [];
  }
}

async function fetchConjunctionChannels(apiClient: ConjunctionApiClient): Promise<ChannelCollectionRow[]> {
  try {
    const result = await apiClient.requestJson<unknown>('/channels');
    return parseChannelsCollection(result.data);
  } catch {
    return [];
  }
}

async function fetchConjunctionStats(apiClient: ConjunctionApiClient): Promise<NodeStatsSnapshot | null> {
  try {
    const result = await apiClient.requestJson<unknown>('/stats');
    return parseNodeStats(result.data);
  } catch {
    return null;
  }
}

export interface ConjunctionSourcesData {
  peers: ConjunctionSourcePeer[];
  channels: ChannelCollectionRow[];
  stats: NodeStatsSnapshot | null;
}

/**
 * Fetches every DATA SOURCES input surface in parallel. Never rejects — a
 * fully offline node resolves to `{peers:[], channels:[], stats:null}`,
 * which `buildSourceRows` renders as the honest single-row (Local SDN
 * Catalog only, with an "unavailable" detail tooltip) state.
 */
export async function loadConjunctionSources(apiClient: ConjunctionApiClient): Promise<ConjunctionSourcesData> {
  const [peers, channels, stats] = await Promise.all([
    fetchConjunctionPeers(apiClient),
    fetchConjunctionChannels(apiClient),
    fetchConjunctionStats(apiClient),
  ]);
  return { peers, channels, stats };
}
