/**
 * Real daemon-surface wiring for the MODULES sub-view (loop task U3.6 — the
 * DATA STANDARDS/MODULES toggle's second panel). Ground truth: the
 * `<!-- MODULES VIEW -->` block in `design_handoff/sdn_console/SDN Console.dc.html`
 * — a panel titled "ANALYSIS & PROPAGATION MODULES" with a right caption
 * ("loaded from all connected peers · paid & open"), a 2-column card grid,
 * each card carrying a name (+ optional PAID chip), a category pill,
 * a "provider · vX.Y.Z" sub-line, a status label, and a single action button
 * (UNLOAD/LOAD/SUBSCRIBE). See `DataModulesPanel.svelte`'s doc comment for
 * the pixel-level styling port.
 *
 * Endpoints probed live on this build (NOT the mock's fabricated
 * `MODULES_LIST` fixture, which invents a `kind`/`provider`/`tier` vocabulary
 * with no backing server field):
 *
 *   1. `GET /api/module-delivery/listings` (PUBLIC —
 *      `cmd/spacedatanetwork/main.go:2579` `handleModuleDeliveryListings`,
 *      allowlisted at `main.go:1755`) → `{"results":[],"count":0}` on this
 *      build (empty catalog). Each result is
 *      `{"plugin_id","version","data_base64","timestamp"}` where
 *      `data_base64` is an OPAQUE signed FlatBuffer licensing descriptor
 *      (`internal/node/licensing_bootstrap.go` `buildPublicationDescriptorFrame`)
 *      — server-internal, not a marketplace card's name/price/provider. This
 *      module does NOT use this endpoint for card data (see #3).
 *   2. `GET /api/v1/modules/runtime` (session-gated — any `peers.Standard`+
 *      session; `cmd/spacedatanetwork/main.go:1218`
 *      `handleModuleRuntimeSnapshot`, NOT in `isPublicReadAPIPath`, NOT in
 *      `isAdminOnlyAPIPath` either) →
 *      `{"generatedAt","count","modules":[{id,status,ui:{title,description,
 *      icon,color,textColor,url},manifest:{pluginFamily,...},stats,
 *      actions:[{actionId,label,description,enabled}],statusHistory,links}]}`
 *      (`plugins.RuntimeSnapshot`/`RuntimeModuleEntry`, `plugins/manager.go:392`).
 *      Live on this build: one module, `spaceaware-ai-log`, `status:"running"`,
 *      carrying ALL EIGHT lifecycle actions (`load`/`unload`/`pause`/`start`/
 *      `stop`/`restart`/`reload-manifest`/`clear-error`) with
 *      status-derived `enabled` flags (`buildRuntimeModuleActions`,
 *      `plugins/manager.go:1649`) — `load` disabled while running,
 *      `unload` enabled. 401s for an anonymous session.
 *   3. `POST /api/storefront/listings/search` (server ROOT, same endpoint
 *      `peers-data.ts` already uses for its PAID-provider badge —
 *      `internal/storefront/api.go:68`) with an empty `{}` body →
 *      `{"listings":null,"total":0,"facets":{...}}` on this build. THIS is
 *      the real marketplace-catalog surface for module cards: each
 *      `Listing` (`internal/storefront/types.go:174`) carries
 *      `listing_id`/`title`/`protected_delivery.module_id` (the plugin-ID
 *      linkage for `listing_kind:"wasm_module"` listings — `types.go:27`)/
 *      `pricing[].price_amount`+`price_currency`. There is NO slug/link
 *      field anywhere on `Listing`, and NO mounted same-origin `/storefront`
 *      web route exists yet (verified: no route in `router.ts`, no
 *      `/storefront` mux entry in `main.go`) — `MODULE_SUBSCRIBE_HREF` below
 *      is therefore a same-origin path this UI does not yet serve a page
 *      for; see the loop task report for this residual.
 *
 *   4. `POST /api/v1/modules/runtime/{moduleID}/actions/{actionId}`
 *      (`cmd/spacedatanetwork/main.go:2753`, inside
 *      `handleModuleRuntimeMutation`/`parseModuleRuntimeMutationPath` —
 *      Admin-trust-gated via `isAdminOnlyAPIPath`'s
 *      `strings.HasPrefix(path, "/api/v1/modules/runtime/")` match,
 *      `main.go:2002`). Success: 200
 *      `{"ok":true,"moduleId":"...","actionId":"..."}`. Failure: the handler
 *      calls Go's `http.Error(w, err.Error(), http.StatusBadRequest)` for a
 *      rejected action — that's a PLAIN-TEXT body, not JSON, so
 *      `SdnApiClient.toApiError` (which only parses `{"code","message"}`
 *      JSON bodies) can't recover the real reason; `runModuleAction` below
 *      falls back to the generic `HTTP 400`-style message in that case,
 *      same accepted limitation as `peers-data.ts`'s `connectToPeer` for a
 *      bodyless error.
 *
 * CARD MAPPING HONESTY (loop task spec): there is no `kind`
 * (PROPAGATION/INGEST/ANALYSIS) surface anywhere in `RuntimeModuleEntry` —
 * the closest REAL field is the optional `manifest.pluginFamily`, which this
 * module renders verbatim (whatever string the module itself declares) when
 * present, and omits the pill entirely otherwise — never a fabricated
 * PROPAGATION/INGEST/ANALYSIS label. There is likewise no `provider` field —
 * the sub-line falls back to the module's own `id` (optionally with its
 * `version`), documented once here rather than re-litigated at each call
 * site. `PAID`/`LOCKED`/`SUBSCRIBE` only ever appear for a card backed by a
 * REAL storefront listing (`protected_delivery.module_id` matching), never
 * fabricated — today that's always zero cards, exercised via a synthetic
 * listing fixture in the test file.
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
// Raw parser: GET /api/v1/modules/runtime
// ---------------------------------------------------------------------------

export interface RuntimeModuleActionEntry {
  actionId: string;
  label: string;
  description: string;
  enabled: boolean;
}

export interface RuntimeModuleUi {
  title: string | null;
  description: string | null;
  icon: string | null;
  color: string | null;
  textColor: string | null;
  url: string | null;
}

export interface RuntimeModuleEntry {
  id: string;
  version: string | null;
  status: string;
  ui: RuntimeModuleUi | null;
  /** From the OPTIONAL `manifest.pluginFamily` — the only real category-like surface (see file doc comment); `null` when no manifest/family is reported. */
  pluginFamily: string | null;
  actions: RuntimeModuleActionEntry[];
}

function parseRuntimeModuleActions(payload: unknown): RuntimeModuleActionEntry[] {
  const list = Array.isArray(payload) ? payload : [];
  return list
    .filter(isPlainRecord)
    .map((a) => ({
      actionId: pickString(a, 'actionId') ?? '',
      label: pickString(a, 'label') ?? '',
      description: pickString(a, 'description') ?? '',
      enabled: a.enabled === true,
    }))
    .filter((a) => a.actionId);
}

function parseRuntimeModuleUi(payload: unknown): RuntimeModuleUi | null {
  if (!isPlainRecord(payload)) return null;
  return {
    title: pickString(payload, 'title'),
    description: pickString(payload, 'description'),
    icon: pickString(payload, 'icon'),
    color: pickString(payload, 'color'),
    textColor: pickString(payload, 'textColor'),
    url: pickString(payload, 'url'),
  };
}

/** Parses one entry of `GET /api/v1/modules/runtime`'s `modules` array. Returns `null` for an entry with no `id` — nothing honest to key it by. */
export function parseRuntimeModuleEntry(payload: unknown): RuntimeModuleEntry | null {
  const rec = isPlainRecord(payload) ? payload : null;
  if (!rec) return null;
  const id = pickString(rec, 'id');
  if (!id) return null;
  const manifest = isPlainRecord(rec.manifest) ? rec.manifest : null;
  return {
    id,
    version: pickString(rec, 'version'),
    status: pickString(rec, 'status') ?? 'unknown',
    ui: parseRuntimeModuleUi(rec.ui),
    pluginFamily: manifest ? pickString(manifest, 'pluginFamily') : null,
    actions: parseRuntimeModuleActions(rec.actions),
  };
}

export interface RuntimeSnapshot {
  generatedAt: string | null;
  count: number;
  modules: RuntimeModuleEntry[];
}

/** Parses `{"generatedAt","count","modules":[...]}`. Malformed entries are dropped, never crash the snapshot. */
export function parseRuntimeSnapshot(payload: unknown): RuntimeSnapshot {
  const rec = isPlainRecord(payload) ? payload : {};
  const list = Array.isArray(rec.modules) ? rec.modules : [];
  const modules = list.map(parseRuntimeModuleEntry).filter((m): m is RuntimeModuleEntry => m !== null);
  return {
    generatedAt: pickString(rec, 'generatedAt'),
    count: pickNumber(rec, 'count') ?? modules.length,
    modules,
  };
}

// ---------------------------------------------------------------------------
// Raw parser: POST /api/storefront/listings/search (module-kind listings)
// ---------------------------------------------------------------------------

export interface StorefrontModuleListing {
  listingId: string;
  title: string;
  /** `protected_delivery.module_id` — `null` for a listing not tied to any WASM module (e.g. a plain data listing). */
  moduleId: string | null;
  /** Lowest/first pricing tier's `price_amount` — `null` when the listing carries no pricing tier at all (never fabricated as `0`, which would read as "free"). */
  priceAmount: number | null;
  priceCurrency: string | null;
}

/** Parses `{"listings":[...] | null, "total":N, "facets":{...}}`, keeping only entries carrying a real `protected_delivery.module_id` link (a data-only listing has nothing to merge against a runtime module). Null-safe for this node's real `listings:null` shape (`peers-data.ts`'s `markPaidProviders` establishes the same null-safety precedent for this exact endpoint). */
export function parseStorefrontModuleListings(payload: unknown): StorefrontModuleListing[] {
  const rec = isPlainRecord(payload) ? payload : {};
  const list = Array.isArray(rec.listings) ? rec.listings : [];
  return list
    .filter(isPlainRecord)
    .map((l) => {
      const delivery = isPlainRecord(l.protected_delivery) ? l.protected_delivery : null;
      const pricing = Array.isArray(l.pricing) ? l.pricing.filter(isPlainRecord) : [];
      const firstTier = pricing[0] ?? null;
      return {
        listingId: pickString(l, 'listing_id') ?? '',
        title: pickString(l, 'title') ?? '',
        moduleId: delivery ? pickString(delivery, 'module_id') : null,
        priceAmount: firstTier ? pickNumber(firstTier, 'price_amount') : null,
        priceCurrency: firstTier ? pickString(firstTier, 'price_currency') : null,
      };
    })
    .filter((l) => l.listingId && l.moduleId);
}

// ---------------------------------------------------------------------------
// Card view-model
// ---------------------------------------------------------------------------

const MODULE_STATUS_LOADED_COLOR = '#5ad6a0';
const MODULE_STATUS_NEUTRAL_COLOR = '#9fd4f5';
const MODULE_STATUS_ERROR_COLOR = '#ff6b6b';
const MODULE_STATUS_LOCKED_COLOR = '#ffb24d';

/** Locked-card action styling (amber) vs. a normal load/unload action (ice-blue) — port of the mock's `actionColor`/`actionBorder`/`actionBg` ternary (line ~1179). */
export interface ModuleCardActionStyle {
  color: string;
  border: string;
  background: string;
}

export function moduleCardActionStyle(actionKind: 'load' | 'start' | 'unload' | 'subscribe' | null): ModuleCardActionStyle {
  if (actionKind === 'subscribe') {
    return { color: '#ffb24d', border: 'rgba(255,178,77,0.4)', background: 'rgba(255,178,77,0.1)' };
  }
  return { color: '#9fd4f5', border: 'rgba(120,190,230,0.5)', background: 'rgba(74,166,224,0.12)' };
}

export type ModuleCardActionKind = 'load' | 'start' | 'unload' | 'subscribe';

export interface ModuleCardView {
  id: string;
  name: string;
  paid: boolean;
  /** `null` when no `manifest.pluginFamily` is reported — the card renders no pill at all rather than a fabricated category (see file doc comment). */
  categoryLabel: string | null;
  providerLine: string;
  statusLabel: string;
  statusColor: string;
  actionLabel: string;
  actionEnabled: boolean;
  actionTooltip: string;
  actionKind: ModuleCardActionKind | null;
  /** Only set for a `'subscribe'` card — same-origin storefront path (see `MODULE_SUBSCRIBE_HREF`'s doc comment for the "no page mounted there yet" residual). */
  subscribeHref: string | null;
}

export const MODULE_LOAD_UNSUPPORTED_TOOLTIP = 'This module does not report a load/unload action for this dashboard.';
/** No listing carries a slug/link field (see file doc comment #3) and no `/storefront` route is mounted yet — this is the best honest same-origin destination until that page ships. */
export const MODULE_SUBSCRIBE_HREF = '/storefront';
export const MODULE_SUBSCRIBE_TOOLTIP = 'Open this module’s storefront listing to subscribe.';

/**
 * Maps ONE installed `RuntimeModuleEntry` to a card. `status==='running'`
 * shows the UNLOAD action; anything else shows LOAD — per the loop task
 * spec, this dashboard surfaces exactly one action button per card, not the
 * module's full 8-action lifecycle. The button's enabled/tooltip state
 * always comes from the SERVER's own `enabled`/`description` for that
 * specific action (`buildRuntimeModuleActions`, `plugins/manager.go:1649`),
 * never guessed client-side.
 */
export function buildInstalledModuleCard(entry: RuntimeModuleEntry, listing: StorefrontModuleListing | null): ModuleCardView {
  const running = entry.status === 'running';
  const statusLabel = running ? 'LOADED' : entry.status.toUpperCase();
  const statusColor = running ? MODULE_STATUS_LOADED_COLOR : entry.status === 'error' ? MODULE_STATUS_ERROR_COLOR : MODULE_STATUS_NEUTRAL_COLOR;
  // Not-running modules prefer "load", but the server only advertises it
  // enabled when the module implements RuntimeModuleLoader (capability
  // gating, plugins/manager.go buildRuntimeModuleActions) — most runtime
  // modules bring themselves up via plain "start" instead, so fall back to
  // it and label the button with the verb that will actually run.
  const candidates: ModuleCardActionKind[] = running ? ['unload'] : ['load', 'start'];
  let wantedActionId: ModuleCardActionKind = candidates[0];
  let action = entry.actions.find((a) => a.actionId === wantedActionId) ?? null;
  for (const candidate of candidates) {
    const found = entry.actions.find((a) => a.actionId === candidate);
    if (found?.enabled) {
      wantedActionId = candidate;
      action = found;
      break;
    }
  }
  const name = entry.ui?.title?.trim() || entry.id;
  const providerLine = entry.version ? `${entry.id} · v${entry.version}` : entry.id;
  const paid = listing !== null && (listing.priceAmount ?? 0) > 0;
  return {
    id: entry.id,
    name,
    paid,
    categoryLabel: entry.pluginFamily,
    providerLine,
    statusLabel,
    statusColor,
    actionLabel: wantedActionId.toUpperCase(),
    actionEnabled: action?.enabled ?? false,
    actionTooltip: action?.description || MODULE_LOAD_UNSUPPORTED_TOOLTIP,
    actionKind: action ? wantedActionId : null,
    subscribeHref: null,
  };
}

/** Maps a marketplace-only listing (a real storefront listing for a module NOT currently installed on this node) to a LOCKED card. */
export function buildLockedModuleCard(listing: StorefrontModuleListing): ModuleCardView {
  const id = listing.moduleId ?? listing.listingId;
  return {
    id,
    name: listing.title || id,
    paid: true,
    categoryLabel: null,
    providerLine: id,
    statusLabel: 'LOCKED',
    statusColor: MODULE_STATUS_LOCKED_COLOR,
    actionLabel: 'SUBSCRIBE',
    actionEnabled: true,
    actionTooltip: MODULE_SUBSCRIBE_TOOLTIP,
    actionKind: 'subscribe',
    subscribeHref: MODULE_SUBSCRIBE_HREF,
  };
}

/**
 * Merges installed runtime modules with the real storefront catalog into the
 * card list the panel renders: every installed module gets a card (PAID
 * badge added when a matching listing exists), plus one LOCKED card per
 * catalog listing for a module NOT yet installed. Never invents a card for
 * anything not backed by one of the two real endpoints.
 */
export function buildModuleCards(
  modules: readonly RuntimeModuleEntry[],
  listings: readonly StorefrontModuleListing[],
): ModuleCardView[] {
  const listingByModuleId = new Map<string, StorefrontModuleListing>();
  for (const listing of listings) {
    if (listing.moduleId) listingByModuleId.set(listing.moduleId, listing);
  }
  const installedIds = new Set(modules.map((m) => m.id));
  const installedCards = modules.map((entry) => buildInstalledModuleCard(entry, listingByModuleId.get(entry.id) ?? null));
  const catalogOnlyCards = listings.filter((l) => l.moduleId && !installedIds.has(l.moduleId)).map(buildLockedModuleCard);
  return [...installedCards, ...catalogOnlyCards];
}

// ---------------------------------------------------------------------------
// Empty / degraded-state copy
// ---------------------------------------------------------------------------

export const MODULES_LOADING_LABEL = 'LOADING MODULES…';
export const MODULES_EMPTY_LABEL = 'NO MODULES · marketplace catalog is empty on this node';
export const MODULES_ANONYMOUS_NOTE =
  'Sign in with a node session to load/unload modules — showing the public marketplace catalog only.';

export function modulesEmptyStateLabel(loaded: boolean, cardCount: number): string {
  if (!loaded) return MODULES_LOADING_LABEL;
  if (cardCount === 0) return MODULES_EMPTY_LABEL;
  return '';
}

// ---------------------------------------------------------------------------
// Fetch orchestration
// ---------------------------------------------------------------------------

/** Structural subset of `SdnApiClient` this module needs — lets tests pass a plain fake instead of constructing a real client. */
export type ModulesApiClient = Pick<SdnApiClient, 'requestJson'>;

interface RuntimeFetchResult {
  snapshot: RuntimeSnapshot | null;
  /** `true` only for a real 401 — distinct from "unreachable"/other errors, so the view can show the honest "sign in to manage modules" note (loop task spec point 8) instead of a generic empty state. */
  unauthorized: boolean;
}

async function fetchRuntimeSnapshot(apiClient: ModulesApiClient): Promise<RuntimeFetchResult> {
  try {
    const result = await apiClient.requestJson<unknown>('/modules/runtime');
    return { snapshot: parseRuntimeSnapshot(result.data), unauthorized: false };
  } catch (err) {
    if (err instanceof SdnApiError && err.status === 401) return { snapshot: null, unauthorized: true };
    return { snapshot: null, unauthorized: false };
  }
}

async function fetchModuleListings(apiClient: ModulesApiClient): Promise<StorefrontModuleListing[]> {
  try {
    const result = await apiClient.requestJson<unknown>('/api/storefront/listings/search', {
      method: 'POST',
      base: 'root',
      body: {},
    });
    return parseStorefrontModuleListings(result.data);
  } catch {
    return [];
  }
}

export interface ModulesDashboardData {
  runtime: RuntimeSnapshot | null;
  listings: StorefrontModuleListing[];
  runtimeUnauthorized: boolean;
}

/**
 * Fetches every MODULES panel surface in parallel. Never rejects — an
 * anonymous/offline session resolves to `{runtime:null, listings:[],
 * runtimeUnauthorized:true|false}`, which `modulesEmptyStateLabel`/
 * `MODULES_ANONYMOUS_NOTE` render as honest degraded states.
 */
export async function loadModulesDashboardData(apiClient: ModulesApiClient): Promise<ModulesDashboardData> {
  const [{ snapshot, unauthorized }, listings] = await Promise.all([
    fetchRuntimeSnapshot(apiClient),
    fetchModuleListings(apiClient),
  ]);
  return { runtime: snapshot, listings, runtimeUnauthorized: unauthorized };
}

// ---------------------------------------------------------------------------
// LOAD / UNLOAD mutation
// ---------------------------------------------------------------------------

export interface ModuleActionResult {
  ok: boolean;
  message: string | null;
}

export const MODULE_ACTION_UNAUTHENTICATED_MESSAGE = 'Not authenticated — sign in to manage modules.';
export const MODULE_ACTION_FORBIDDEN_MESSAGE = 'Insufficient trust level — an admin session is required to manage modules.';

/**
 * Calls `POST /api/v1/modules/runtime/{moduleId}/actions/{actionId}` (the
 * real contract — see file doc comment #4). Never throws: every failure
 * mode resolves to `{ok:false, message}` with as honest a message as the
 * response body allows (a rejected action's body is plain text, not JSON —
 * see that doc comment note — so its specific reason is unrecoverable here
 * today, same accepted gap as `peers-data.ts`'s `connectToPeer`).
 */
export async function runModuleAction(apiClient: ModulesApiClient, moduleId: string, actionId: string): Promise<ModuleActionResult> {
  try {
    const result = await apiClient.requestJson<unknown>(
      `/modules/runtime/${encodeURIComponent(moduleId)}/actions/${encodeURIComponent(actionId)}`,
      { method: 'POST', body: {} },
    );
    const rec = isPlainRecord(result.data) ? result.data : {};
    if (rec.ok === true) return { ok: true, message: null };
    return { ok: false, message: 'Action completed without confirmation from the daemon.' };
  } catch (err) {
    if (err instanceof SdnApiError) {
      if (err.status === 401) return { ok: false, message: MODULE_ACTION_UNAUTHENTICATED_MESSAGE };
      if (err.status === 403) return { ok: false, message: MODULE_ACTION_FORBIDDEN_MESSAGE };
      return { ok: false, message: err.body?.message || err.message || `Action failed (HTTP ${err.status}).` };
    }
    return { ok: false, message: 'Action failed — network error.' };
  }
}
