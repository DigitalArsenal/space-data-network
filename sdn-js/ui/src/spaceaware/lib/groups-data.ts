/**
 * Client-local pure logic for the SDN Console GROUPS view (loop task U3.8).
 * Ground truth: the `<!-- ============ GROUPS ============ -->` block in
 * `design_handoff/sdn_console/SDN Console.dc.html` (lines ~321-402 — a
 * PRIMITIVES legend + ALL/MY GROUPS/PEER GROUPS filter strip, a GROUP
 * DIRECTORY table, and a group detail + CONJUNCTION MONITOR card) plus its
 * `class Component extends DCLogic` logic block (lines ~832-1139 —
 * `PRIMITIVES`, `GROUPS`, `allGroups()`, `groupColor`/`groupGlyph`,
 * `conjColor`/`conjLabel`, `renderVals()`'s `GROUPS —` section).
 *
 * DECISION D5 (client-local groups, no server surface): unlike PEERS/DATA/
 * CHANNELS (which read a real daemon HTTP API — see those files' own doc
 * comments), there is no `/api/v1/groups`-style endpoint on this build and
 * no peer-group discovery/administration protocol. Every group definition
 * here lives ONLY in `localStorage['sdn_shared_groups']` on this browser —
 * see `loadSharedGroups`/`saveSharedGroups` below. This mirrors the mock's
 * OWN persistence choice almost exactly: `allGroups()`
 * (`SDN Console.dc.html:847`) reads `localStorage.getItem('sdn_shared_groups')`
 * and merges any entries not already in its hardcoded `GROUPS` fixture. The
 * one behavioral difference: the mock keeps its 6 fixture groups in memory
 * forever (never persisted) and only ever *adds* extra entries from
 * storage; this view instead SEEDS those same 6 fixtures into storage on
 * first load (see `loadSharedGroups`) so a later loop task (U5.x, the
 * ported Orbital screen) can read the exact same `localStorage` key and see
 * a consistent, editable group set — the loop task's own directive.
 *
 * EXTRACTED SCHEMA (verbatim field names/types from the mock's `GROUPS`
 * fixture array, `SDN Console.dc.html:839-846` — e.g.
 * `{ id:'leo-a', name:'LEO Constellation A', owner:'self',
 * ownerName:'THIS NODE', count:42, regime:'LEO',
 * scope:'53° shell · operated assets', conj:'watch', conjN:2,
 * maxPc:'7.3e-5', nextTca:'19h 40m', tcaH:19.7, updated:'2m ago' }`):
 *
 *   id: string          — stable identifier ('leo-a', 'ct-active', …)
 *   name: string         — display name
 *   owner: string        — 'self' for a locally-administered group, else a
 *                           peer/provider key ('celestrak','spaceaware','obs1')
 *   ownerName: string    — display name for `owner` ('THIS NODE', 'CelesTrak Provider', …)
 *   count: number        — member/object count
 *   regime: string        — 'LEO' | 'GEO' | 'MEO' | 'ALL' | 'MIXED' in the fixtures
 *   scope: string         — free-text scope description
 *   conj: string          — 'watch' | 'clear' | 'critical' in the fixtures ('' here for
 *                           a user-created group with no conjunction data — see below)
 *   conjN: number         — conjunction event count
 *   maxPc: string          — formatted probability-of-collision string ('7.3e-5', '<1e-7')
 *   nextTca: string        — formatted next time-of-closest-approach ('19h 40m', '—')
 *   tcaH: number           — `nextTca` in hours (used by the mock's watch-list sort; 999 = none)
 *   updated: string        — the mock stores a literal relative-time string ('2m ago',
 *                           'just now'); THIS view instead stores a real ISO 8601
 *                           timestamp for any group it writes (create/delete leave
 *                           other rows untouched, so seeded fixtures keep their
 *                           literal mock strings until edited) — `formatUpdatedLabel`
 *                           below renders either shape correctly (see its doc comment).
 *
 * `SharedGroup` below reproduces this shape FIELD-FOR-FIELD (see
 * `groups-data.test.ts`'s schema-stability test, which asserts the exact
 * serialized key set) so a later loop task's Orbital screen can read
 * `localStorage['sdn_shared_groups']` and get the identical object shape.
 *
 * HONESTY / DEMO-BADGING (decision D4): this build has no real peer-group
 * discovery protocol and no conjunction-screening engine wired to any UI
 * surface. Two independent axes of fabricated data get demo-badged rather
 * than presented as real:
 *   1. OWNERSHIP — the mock's 3 provider-owned fixtures (`ct-active`,
 *      `sa-highrisk`, `oe-leo`, all `owner!=='self'`) imply a peer group
 *      surface that doesn't exist; `isPeerOwnershipDemo` flags them and the
 *      view renders a DEMO tag near their ownership indicators. The mock's
 *      3 `owner:'self'` fixtures (`leo-a`, `geo-watch`, `iss-env`) are
 *      NOT demo-tagged — per the loop task's own directive, a client-local
 *      "your own group definitions" set is honestly real regardless of
 *      where its starting values came from (you can rename/delete them).
 *   2. CONJUNCTION STATUS — every fixture's `conj`/`conjN`/event list is
 *      fabricated (there is no screening engine); `isConjunctionDemo` flags
 *      any group with a non-empty `conj` and the CONJUNCTION MONITOR
 *      section carries its own DEMO tag independent of axis 1 (a `owner:
 *      'self'` group can still have demo conjunction data). A user-created
 *      group gets `conj:''` — an honest "—" cell, never a fabricated
 *      WATCH/CLEAR/CRITICAL (see `buildGroupConjunctionSection`).
 *
 * CRUD (the mock has NO create/delete UI for groups anywhere in this
 * template — `allGroups()` only ever READS `sdn_shared_groups`; nothing in
 * `renderVals()`/the event-handler list writes to it). The loop task's own
 * CRUD mandate requires one anyway, so `GroupsView.svelte` adds a minimal
 * "+ NEW GROUP" affordance and a per-row "✕" remove control (MY GROUPS
 * only) styled with this app's existing ghost-button convention
 * (`PeersView.svelte`'s `.sdn-peers-btn--ghost`) — see that file's doc
 * comment for the exact placement. `createGroup`/`deleteGroup` below are
 * the pure functions backing those controls.
 */

// ---------------------------------------------------------------------------
// Storage plumbing (mirrors console.ts's `loadNodeLayout`/`saveNodeLayout`
// pattern: reads/writes are pure given an injected `StorageLike`, corrupt or
// missing data never throws, and a fallback is returned in-memory without
// eagerly writing itself back to storage).
// ---------------------------------------------------------------------------

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;

export const GROUPS_STORAGE_KEY = 'sdn_shared_groups';

export interface SharedGroup {
  id: string;
  name: string;
  owner: string;
  ownerName: string;
  count: number;
  regime: string;
  scope: string;
  /** `'watch' | 'clear' | 'critical'` on a seeded fixture; `''` (no data) on an honest user-created group. */
  conj: string;
  conjN: number;
  maxPc: string;
  nextTca: string;
  tcaH: number;
  updated: string;
}

/** Verbatim port of the mock's `PRIMITIVES` array (`SDN Console.dc.html:833-838`) — the legend strip's 4 entries. */
export interface GroupPrimitive {
  id: string;
  glyph: string;
  label: string;
  sub: string;
  color: string;
}

export const GROUP_PRIMITIVES: readonly GroupPrimitive[] = [
  { id: 'self', glyph: '◉', label: 'THIS NODE', sub: 'your identity', color: '#35c9d8' },
  { id: 'peer', glyph: '◍', label: 'PEER NODES', sub: 'other identities', color: '#9fd4f5' },
  { id: 'mygroup', glyph: '⬢', label: 'MY GROUPS', sub: 'you administer', color: '#c77dff' },
  { id: 'peergroup', glyph: '⬡', label: 'PEER GROUPS', sub: 'monitor only', color: '#ff9e64' },
];

export const GROUPS_LEGEND_CAPTION = 'one group set · administered here · monitored in the 3D console';

/** Verbatim port of the mock's `GROUPS` fixture array (`SDN Console.dc.html:839-846`) — see file doc comment for the extracted shape. */
export const SEED_GROUPS: readonly SharedGroup[] = [
  {
    id: 'leo-a',
    name: 'LEO Constellation A',
    owner: 'self',
    ownerName: 'THIS NODE',
    count: 42,
    regime: 'LEO',
    scope: '53° shell · operated assets',
    conj: 'watch',
    conjN: 2,
    maxPc: '7.3e-5',
    nextTca: '19h 40m',
    tcaH: 19.7,
    updated: '2m ago',
  },
  {
    id: 'geo-watch',
    name: 'GEO Belt Watch',
    owner: 'self',
    ownerName: 'THIS NODE',
    count: 18,
    regime: 'GEO',
    scope: 'GEO 75°E – 105°E slots',
    conj: 'clear',
    conjN: 0,
    maxPc: '<1e-7',
    nextTca: '—',
    tcaH: 999,
    updated: '5m ago',
  },
  {
    id: 'iss-env',
    name: 'ISS Debris Envelope',
    owner: 'self',
    ownerName: 'THIS NODE',
    count: 9,
    regime: 'LEO',
    scope: '400 – 430 km custody shell',
    conj: 'critical',
    conjN: 1,
    maxPc: '1.2e-3',
    nextTca: '4h 12m',
    tcaH: 4.2,
    updated: 'just now',
  },
  {
    id: 'ct-active',
    name: 'CelesTrak Active Cat',
    owner: 'celestrak',
    ownerName: 'CelesTrak Provider',
    count: 128,
    regime: 'ALL',
    scope: 'all active payloads',
    conj: 'watch',
    conjN: 5,
    maxPc: '9.1e-5',
    nextTca: '8h 03m',
    tcaH: 8.05,
    updated: '1m ago',
  },
  {
    id: 'sa-highrisk',
    name: 'SpaceAware High-Risk',
    owner: 'spaceaware',
    ownerName: 'SpaceAware.io',
    count: 23,
    regime: 'MIXED',
    scope: 'Pc > 1e-4 · next 72 h',
    conj: 'critical',
    conjN: 3,
    maxPc: '2.4e-3',
    nextTca: '1h 27m',
    tcaH: 1.45,
    updated: '30s ago',
  },
  {
    id: 'oe-leo',
    name: 'OrbitalEdge LEO Track',
    owner: 'obs1',
    ownerName: 'OrbitalEdge Node',
    count: 14,
    regime: 'LEO',
    scope: 'observed LEO track set',
    conj: 'clear',
    conjN: 0,
    maxPc: '<1e-7',
    nextTca: '—',
    tcaH: 999,
    updated: '8m ago',
  },
];

export function cloneGroups(groups: readonly SharedGroup[]): SharedGroup[] {
  return groups.map((g) => ({ ...g }));
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

/** Structural validator for one persisted `SharedGroup` — every field present with the right primitive type. Rejects anything else (missing key, wrong type, extra-but-malformed shape) so a corrupted/partial record invalidates the whole load rather than rendering a broken row. */
export function isValidSharedGroup(value: unknown): value is SharedGroup {
  if (!isPlainRecord(value)) return false;
  return (
    typeof value.id === 'string' &&
    value.id.length > 0 &&
    typeof value.name === 'string' &&
    typeof value.owner === 'string' &&
    typeof value.ownerName === 'string' &&
    typeof value.count === 'number' &&
    typeof value.regime === 'string' &&
    typeof value.scope === 'string' &&
    typeof value.conj === 'string' &&
    typeof value.conjN === 'number' &&
    typeof value.maxPc === 'string' &&
    typeof value.nextTca === 'string' &&
    typeof value.tcaH === 'number' &&
    typeof value.updated === 'string'
  );
}

export function isValidSharedGroupList(value: unknown): value is SharedGroup[] {
  return Array.isArray(value) && value.every(isValidSharedGroup);
}

/**
 * Reads `localStorage['sdn_shared_groups']`. Missing key, corrupt JSON, or a
 * payload that fails `isValidSharedGroupList` all fall back to a fresh clone
 * of `SEED_GROUPS` — never throws, and never writes the fallback back to
 * storage itself (mirrors `console.ts`'s `loadNodeLayout`; the caller's next
 * `saveSharedGroups` call is what actually persists it).
 */
export function loadSharedGroups(storage: StorageLike | null | undefined): SharedGroup[] {
  try {
    const raw = storage?.getItem(GROUPS_STORAGE_KEY);
    if (raw) {
      const parsed: unknown = JSON.parse(raw);
      if (isValidSharedGroupList(parsed)) return parsed;
    }
  } catch {
    // Corrupt JSON or storage unavailable — fall back below.
  }
  return cloneGroups(SEED_GROUPS);
}

export function saveSharedGroups(storage: StorageLike | null | undefined, groups: readonly SharedGroup[]): void {
  try {
    storage?.setItem(GROUPS_STORAGE_KEY, JSON.stringify(groups));
  } catch {
    // localStorage unavailable — non-fatal, matches the mock's `catch(e){}`.
  }
}

// ---------------------------------------------------------------------------
// CRUD (see file doc comment — the mock has no create/delete UI; these are
// this loop task's own addition, required by its CRUD mandate).
// ---------------------------------------------------------------------------

export const GROUP_REGIME_OPTIONS = ['ALL', 'LEO', 'MEO', 'GEO', 'MIXED'] as const;
export type GroupRegimeOption = (typeof GROUP_REGIME_OPTIONS)[number];

export interface CreateGroupInput {
  name: string;
  regime: string;
  scope: string;
}

function slugify(value: string): string {
  const slug = value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return slug || 'group';
}

/** Deterministic, collision-free id: `slug`, then `slug-2`, `slug-3`, … against every id already present (fixtures + user groups alike). */
export function generateGroupId(name: string, existing: readonly SharedGroup[]): string {
  const base = slugify(name);
  const taken = new Set(existing.map((g) => g.id));
  if (!taken.has(base)) return base;
  let n = 2;
  while (taken.has(`${base}-${n}`)) n += 1;
  return `${base}-${n}`;
}

export const GROUPS_CREATE_NAME_REQUIRED_MESSAGE = 'A group name is required.';

/** Client-side mirror of the "name required" rule `GroupsView.svelte`'s create form enforces before calling this. `null` = valid. */
export function validateCreateGroupInput(input: CreateGroupInput): string | null {
  return input.name.trim() ? null : GROUPS_CREATE_NAME_REQUIRED_MESSAGE;
}

/**
 * Appends a new `owner:'self'` group. Honest starting values per the loop
 * task's spec: `count:0` (no membership editor in this task — see file doc
 * comment "residual"), `conj:''`/`conjN:0` (no fabricated conjunction
 * status), `updated` set to `now`'s ISO timestamp. No-op (returns `groups`
 * unchanged) when `input.name` is blank — callers should check
 * `validateCreateGroupInput` first to surface that as a form error instead.
 */
export function createGroup(groups: readonly SharedGroup[], input: CreateGroupInput, now: Date = new Date()): SharedGroup[] {
  const name = input.name.trim();
  if (!name) return groups.slice();
  const regime = input.regime.trim() || 'ALL';
  const scope = input.scope.trim();
  const group: SharedGroup = {
    id: generateGroupId(name, groups),
    name,
    owner: 'self',
    ownerName: 'THIS NODE',
    count: 0,
    regime,
    scope,
    conj: '',
    conjN: 0,
    maxPc: '',
    nextTca: '',
    tcaH: 999,
    updated: now.toISOString(),
  };
  return groups.concat(group);
}

/** True only for a locally-administered (`owner:'self'`) group — peer/provider groups are immutable from this client. */
export function canDeleteGroup(group: Pick<SharedGroup, 'owner'>): boolean {
  return group.owner === 'self';
}

/**
 * Removes `id` when it names a `owner:'self'` group; a no-op (returns
 * `groups` unchanged, same array-copy contract as every other mutator here)
 * for a peer/provider group's id or an id that isn't present — "delete only
 * for MY GROUPS, peer/demo groups immutable" per the loop task's data model.
 */
export function deleteGroup(groups: readonly SharedGroup[], id: string): SharedGroup[] {
  return groups.filter((g) => !(g.id === id && canDeleteGroup(g)));
}

// ---------------------------------------------------------------------------
// Filtering + counts (ALL / MY GROUPS / PEER GROUPS filter strip; GROUP
// DIRECTORY's "N mine · N peer-defined" caption)
// ---------------------------------------------------------------------------

export type GroupFilterTab = 'all' | 'mine' | 'peer';

export interface GroupFilterTabSpec {
  id: GroupFilterTab;
  label: string;
}

export const GROUP_FILTER_TABS: readonly GroupFilterTabSpec[] = [
  { id: 'all', label: 'ALL' },
  { id: 'mine', label: 'MY GROUPS' },
  { id: 'peer', label: 'PEER GROUPS' },
];

export interface GroupFilterTabStyle {
  color: string;
  border: string;
  background: string;
}

/** Port of the mock's `groupFilterRows` styling (`SDN Console.dc.html:1110-1111`) — identical formula to `peers-data.ts`'s `peerFilterTabStyle`. */
export function groupFilterTabStyle(tabId: GroupFilterTab, activeTab: GroupFilterTab): GroupFilterTabStyle {
  const active = tabId === activeTab;
  return {
    color: active ? '#9fd4f5' : '#7d929b',
    border: active ? 'rgba(120,190,230,0.5)' : 'rgba(90,150,180,0.28)',
    background: active ? 'rgba(74,166,224,0.1)' : 'transparent',
  };
}

export function filterGroups(groups: readonly SharedGroup[], tab: GroupFilterTab): SharedGroup[] {
  if (tab === 'mine') return groups.filter((g) => g.owner === 'self');
  if (tab === 'peer') return groups.filter((g) => g.owner !== 'self');
  return groups.slice();
}

export interface GroupsCountCaption {
  mineCount: number;
  peerCount: number;
}

/** Port of the mock's `myGroupCount`/`peerGroupCount` (`SDN Console.dc.html:1108-1109`) — always computed against the FULL (unfiltered) group set, matching the directory header caption. */
export function groupsCountCaption(groups: readonly SharedGroup[]): GroupsCountCaption {
  return {
    mineCount: groups.filter((g) => g.owner === 'self').length,
    peerCount: groups.filter((g) => g.owner !== 'self').length,
  };
}

// ---------------------------------------------------------------------------
// Ownership / conjunction color + glyph mapping (mock's `groupColor`/
// `groupGlyph`/`conjColor`/`conjLabel`, `SDN Console.dc.html:848-851`)
// ---------------------------------------------------------------------------

export function groupGlyph(group: Pick<SharedGroup, 'owner'>): string {
  return group.owner === 'self' ? '⬢' : '⬡';
}

export function groupColor(group: Pick<SharedGroup, 'owner'>): string {
  return group.owner === 'self' ? '#c77dff' : '#ff9e64';
}

const CONJ_COLORS: Record<string, string> = { clear: '#5ad6a0', watch: '#ffb24d', critical: '#ff6b6b' };
const CONJ_LABELS: Record<string, string> = { clear: 'CLEAR', watch: 'WATCH', critical: 'CRITICAL' };

export function conjColor(conj: string): string {
  return CONJ_COLORS[conj] ?? '#9fb3bc';
}

export function conjLabel(conj: string): string {
  return CONJ_LABELS[conj] ?? (conj ? conj.toUpperCase() : '—');
}

// ---------------------------------------------------------------------------
// Demo-badging (decision D4 — see file doc comment's HONESTY section)
// ---------------------------------------------------------------------------

export function isPeerOwnershipDemo(group: Pick<SharedGroup, 'owner'>): boolean {
  return group.owner !== 'self';
}

export function isConjunctionDemo(group: Pick<SharedGroup, 'conj'>): boolean {
  return group.conj !== '';
}

export const GROUPS_OWNERSHIP_DEMO_TAG_TITLE =
  'Demo peer group — no real peer-group discovery/administration protocol exists on this build; this entry mirrors the design mock’s fixture data.';
export const GROUPS_CONJUNCTION_DEMO_TAG_TITLE =
  'Demo conjunction status — seeded fixture data; no conjunction-screening engine is wired to this view yet.';

// ---------------------------------------------------------------------------
// GROUP DIRECTORY rows
// ---------------------------------------------------------------------------

export interface GroupConjunctionCellView {
  hasData: boolean;
  dotColor: string;
  label: string;
  /** `'· N'` when `hasData`, else `''` — appended after `label` in the directory column. */
  countSuffix: string;
}

/** Directory-column CONJUNCTION cell: an honest dash (no dot, no count) for a user-created group with `conj:''`, else the fixture's colored dot/label/count — never a fabricated status. */
export function buildGroupConjunctionCell(group: Pick<SharedGroup, 'conj' | 'conjN'>): GroupConjunctionCellView {
  if (!group.conj) {
    return { hasData: false, dotColor: '#5a7a8a', label: '—', countSuffix: '' };
  }
  return {
    hasData: true,
    dotColor: conjColor(group.conj),
    label: conjLabel(group.conj),
    countSuffix: `· ${group.conjN}`,
  };
}

export interface GroupRowView {
  id: string;
  name: string;
  /** `"{REGIME} · {scope}"` sub-line. */
  regimeScope: string;
  glyph: string;
  glyphColor: string;
  ownerName: string;
  ownerColor: string;
  /** `"{count} OBJ"`. */
  countLabel: string;
  conj: GroupConjunctionCellView;
  isMine: boolean;
  selected: boolean;
}

export function buildGroupRows(groups: readonly SharedGroup[], selectedId: string | null): GroupRowView[] {
  return groups.map((g) => ({
    id: g.id,
    name: g.name,
    regimeScope: `${g.regime} · ${g.scope}`,
    glyph: groupGlyph(g),
    glyphColor: groupColor(g),
    ownerName: g.ownerName,
    ownerColor: groupColor(g),
    countLabel: `${g.count} OBJ`,
    conj: buildGroupConjunctionCell(g),
    isMine: g.owner === 'self',
    selected: g.id === selectedId,
  }));
}

// ---------------------------------------------------------------------------
// UPDATED column — relative time (see file doc comment: a seeded fixture's
// `updated` is the mock's own literal string, e.g. "2m ago"; a group this
// view has written carries a real ISO 8601 timestamp instead)
// ---------------------------------------------------------------------------

const MS_MINUTE = 60_000;
const MS_HOUR = 3_600_000;
const MS_DAY = 86_400_000;

/**
 * Renders `updated` as a relative-time label. When it parses as a real
 * timestamp (`Date.parse` succeeds — true for every ISO 8601 string this
 * view itself writes), computes `"{n}s/m/h/d ago"` / `"just now"` against
 * `now`. When it does NOT parse (`Date.parse` returns `NaN` — true for
 * every one of the mock's literal fixture strings, e.g. `"2m ago"`,
 * `"just now"`, since none of those are valid `Date` inputs), the string is
 * returned verbatim — this is what keeps a freshly-seeded fixture group
 * showing its mock-authored label instead of crashing or showing "Invalid
 * Date".
 */
export function formatUpdatedLabel(updated: string, now: Date = new Date()): string {
  const parsed = Date.parse(updated);
  if (Number.isNaN(parsed)) return updated;
  const deltaMs = now.getTime() - parsed;
  if (deltaMs < 45_000) return 'just now';
  if (deltaMs < MS_MINUTE) return `${Math.round(deltaMs / 1000)}s ago`;
  if (deltaMs < MS_HOUR) return `${Math.round(deltaMs / MS_MINUTE)}m ago`;
  if (deltaMs < MS_DAY) return `${Math.round(deltaMs / MS_HOUR)}h ago`;
  return `${Math.round(deltaMs / MS_DAY)}d ago`;
}

// ---------------------------------------------------------------------------
// CONJUNCTION MONITOR section (detail card) — demo event fixture + formatting
// (mock's `CONJ` array + `groupEvents`/`missColor`, `SDN Console.dc.html:921-927,1127`)
// ---------------------------------------------------------------------------

interface ConjEventFixture {
  object: string;
  tca: string;
  miss: string;
  pc: string;
  state: string;
}

/** Verbatim port of the mock's `CONJ` fixture array — a fixed, global event list (not per-group), sliced by the selected group's `conj` status (see `buildConjunctionEvents`). */
const CONJ_EVENTS_FIXTURE: readonly ConjEventFixture[] = [
  { object: 'SAT-39210', tca: '2026-06-26T11:55:00Z', miss: '0.42', pc: '7.3e-4', state: 'warn' },
  { object: 'SAT-44713', tca: '2026-06-25T18:42:00Z', miss: '1.84', pc: '2.1e-5', state: 'review' },
  { object: 'SAT-57944', tca: '2026-06-26T03:10:00Z', miss: '8.92', pc: '4.8e-7', state: 'clear' },
];

/** Port of the mock's `r.tca.slice(5,16).replace('T',' ')` — `"2026-06-26T11:55:00Z"` → `"06-26 11:55"`. */
export function formatEventTca(tca: string): string {
  return tca.slice(5, 16).replace('T', ' ');
}

/** Port of the mock's `missColor()` — keyed by the EVENT's own `state`, not the group's `conj`. */
export function eventStateColor(state: string): string {
  if (state === 'warn') return '#ff6b6b';
  if (state === 'review') return '#ffb24d';
  return '#cfe3ec';
}

export interface GroupConjunctionEventView {
  object: string;
  tca: string;
  missKm: string;
  pc: string;
  stateColor: string;
}

/** Port of the mock's `(selG.conj==='clear' ? [] : this.CONJ.slice(0, selG.conj==='critical'?2:1))`. */
export function buildConjunctionEvents(conj: string): GroupConjunctionEventView[] {
  const n = conj === 'clear' ? 0 : conj === 'critical' ? 2 : 1;
  return CONJ_EVENTS_FIXTURE.slice(0, n).map((ev) => ({
    object: ev.object,
    tca: formatEventTca(ev.tca),
    missKm: ev.miss,
    pc: ev.pc,
    stateColor: eventStateColor(ev.state),
  }));
}

function conjunctionSubText(conj: string): string {
  if (conj === 'critical') return 'High-Pc event inside screening window — review maneuver options.';
  if (conj === 'watch') return 'Events approaching screening thresholds. Monitored continuously.';
  if (conj === 'clear') return 'No events above thresholds in the current window.';
  return 'No conjunction screening data on this build — this group has no conjunction-engine surface yet.';
}

export interface GroupConjunctionSectionView {
  /** True for any fixture-sourced `conj` (mine or peer) — see `isConjunctionDemo`. */
  isDemo: boolean;
  label: string;
  dotColor: string;
  subText: string;
  events: GroupConjunctionEventView[];
}

/** CONJUNCTION MONITOR section view-model. `conj:''` (a user-created group) renders an honest "—"/no-events state with `isDemo:false` — never a fabricated status. */
export function buildGroupConjunctionSection(group: Pick<SharedGroup, 'conj' | 'conjN'>): GroupConjunctionSectionView {
  if (!isConjunctionDemo(group)) {
    return { isDemo: false, label: '—', dotColor: '#5a7a8a', subText: conjunctionSubText(''), events: [] };
  }
  return {
    isDemo: true,
    label: conjLabel(group.conj),
    dotColor: conjColor(group.conj),
    subText: conjunctionSubText(group.conj),
    events: buildConjunctionEvents(group.conj),
  };
}

// ---------------------------------------------------------------------------
// Detail card (right panel) — kind chip, DEFINED BY box, REGIME/MEMBERS/
// UPDATED grid, button row (mock's `renderVals()` `selGroup` block,
// `SDN Console.dc.html:1117-1128`, and the `isMine`/`isPeer` button-row
// `sc-if`s at lines 393-401)
// ---------------------------------------------------------------------------

export const GROUPS_READ_ONLY_NOTE =
  'Peer-defined group — membership is read-only. You can monitor its conjunctions and view it in 3D, but only its owner can edit members.';

export interface GroupScreenButtonView {
  label: string;
  glyph: string;
  color: string;
  border: string;
  background: string;
}

/**
 * The mock renders TWO distinct buttons here depending on ownership — a
 * `owner:'self'` group gets "⊘ SCREEN FOR CONJUNCTIONS" (red accent), a
 * peer/provider group gets "◉ MONITOR CONJUNCTIONS" (amber accent) — both
 * call the same `screenGroup(id)` action (`SDN Console.dc.html:393-398`).
 * Both are ported here verbatim (pixel ground truth) even though the loop
 * task's own prose only mentions the "mine" variant.
 */
function screenButtonView(isMine: boolean): GroupScreenButtonView {
  if (isMine) {
    return { label: 'SCREEN FOR CONJUNCTIONS', glyph: '⊘', color: '#ffb3b3', border: 'rgba(255,107,107,0.5)', background: 'rgba(255,107,107,0.14)' };
  }
  return { label: 'MONITOR CONJUNCTIONS', glyph: '◉', color: '#ffc89e', border: 'rgba(255,158,100,0.5)', background: 'rgba(255,158,100,0.12)' };
}

/** `/orbital?group={id}` — the mock's `openHref` (`SDN Console.dc.html:1125`), ported as a real app path for `navigate()`. */
export function groupOrbitalPath(id: string): string {
  return `/orbital?group=${encodeURIComponent(id)}`;
}

/** `SCREEN FOR CONJUNCTIONS`/`MONITOR CONJUNCTIONS` both route here — the CONJUNCTION console view (a later loop task; today renders the shared "not yet ported" placeholder). */
export const GROUPS_CONJUNCTION_CONSOLE_PATH = '/console/conjunction';

export interface GroupDetailView {
  id: string;
  glyph: string;
  glyphColor: string;
  name: string;
  scope: string;
  kindLabel: string;
  kindColor: string;
  kindBorder: string;
  kindBg: string;
  ownerName: string;
  ownerColor: string;
  regime: string;
  /** `"{count} obj"` — lowercase, matches the mock's detail-grid markup (distinct from the directory row's uppercase `"N OBJ"`). */
  membersLabel: string;
  updatedLabel: string;
  isMine: boolean;
  isPeer: boolean;
  isOwnershipDemo: boolean;
  screenButton: GroupScreenButtonView;
  openIn3dPath: string;
  readOnlyNote: string | null;
  conjunction: GroupConjunctionSectionView;
  deletable: boolean;
}

export function buildGroupDetailView(group: SharedGroup, now: Date = new Date()): GroupDetailView {
  const isMine = group.owner === 'self';
  const isPeer = !isMine;
  return {
    id: group.id,
    glyph: groupGlyph(group),
    glyphColor: groupColor(group),
    name: group.name,
    scope: group.scope,
    kindLabel: isMine ? 'MY GROUP · YOU ADMINISTER' : 'PEER GROUP · MONITOR ONLY',
    kindColor: groupColor(group),
    kindBorder: isMine ? 'rgba(199,125,255,0.4)' : 'rgba(255,158,100,0.4)',
    kindBg: isMine ? 'rgba(199,125,255,0.07)' : 'rgba(255,158,100,0.07)',
    ownerName: group.ownerName,
    ownerColor: groupColor(group),
    regime: group.regime,
    membersLabel: `${group.count} obj`,
    updatedLabel: formatUpdatedLabel(group.updated, now),
    isMine,
    isPeer,
    isOwnershipDemo: isPeerOwnershipDemo(group),
    screenButton: screenButtonView(isMine),
    openIn3dPath: groupOrbitalPath(group.id),
    readOnlyNote: isPeer ? GROUPS_READ_ONLY_NOTE : null,
    conjunction: buildGroupConjunctionSection(group),
    deletable: canDeleteGroup(group),
  };
}

/** Resolves the detail-card selection: the group matching `selectedId`, else the first group — mirrors `PeersView.svelte`/`ChannelsView.svelte`'s "always resolve to a usable row" contract. `null` only when `groups` itself is empty. */
export function resolveSelectedGroup(groups: readonly SharedGroup[], selectedId: string | null): SharedGroup | null {
  return groups.find((g) => g.id === selectedId) ?? groups[0] ?? null;
}

// ---------------------------------------------------------------------------
// `?group=` deep link (captured but unconsumed by `console.ts`'s
// `parseConsoleDeepLinkQuery` since U3.1 — this view is what finally reads
// it, per the loop task's directive)
// ---------------------------------------------------------------------------

/** Matches a `?group=` value against `id` first, then a case-insensitive `name` match. `null` when blank or no match — callers should leave the existing selection alone in that case. */
export function resolveDeepLinkGroupId(groups: readonly SharedGroup[], groupParam: string | null): string | null {
  if (!groupParam || !groupParam.trim()) return null;
  const byId = groups.find((g) => g.id === groupParam);
  if (byId) return byId.id;
  const needle = groupParam.trim().toLowerCase();
  const byName = groups.find((g) => g.name.trim().toLowerCase() === needle);
  return byName ? byName.id : null;
}
