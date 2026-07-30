/*
 * The NODE dashboard's widget registry and layouts — the template's own,
 * transcribed from the design source it is declared against:
 *
 *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
 *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
 *   WIDGETS :863-871 · DEFAULT_LAYOUT :873 · LKEY :862
 *
 * IRIS ruling 2026-07-30 §1: "No import, no conversion, no hand-edit" — the
 * export STAYS in SpaceAware-UI and the widgets are implemented HERE, from the
 * design system's own primitives. This file is the layout half of that: the
 * spans and ids are the template's, verbatim, so a later wave can turn EDIT
 * LAYOUT on over exactly the vocabulary the design defined.
 *
 * WAVE 2 TURNS EDITING ON (IRIS §2/§3). The registry is now the template's full
 * eight — the three add-only widgets (peersum / storage / activity, `:869-871`)
 * arrive together with EDIT LAYOUT because "the ADD menu is empty without them,
 * which is why they belong together". Every mutator the design defines
 * (`:883-889`) is a PURE FUNCTION here, so node-console-logic.test.js can prove
 * the rules that the export only demonstrates.
 */

/** The design's localStorage key, verbatim (`:862`). */
export const LAYOUT_KEY = 'sdn_node_layout_v1';

/**
 * The template's eight widgets, with its own titles and span vocabulary
 * (`:863-871`, verbatim). `spans` is the cycle EDIT LAYOUT offers; `def` is the
 * span a widget takes when it is added.
 */
export const WIDGETS = {
  health: { title: 'NODE HEALTH', spans: [4, 6], def: 4 },
  identity: { title: 'IDENTITY', spans: [4, 6], def: 4 },
  service: { title: 'SERVICE', spans: [4, 6], def: 4 },
  netmap: { title: 'PEER MAP · GEOIP', spans: [6, 8, 12], def: 8 },
  throughput: { title: 'NETWORK THROUGHPUT', spans: [4, 6], def: 4 },
  peersum: { title: 'PEER SUMMARY', spans: [4, 6], def: 4 },
  storage: { title: 'STORAGE · FLATSQL', spans: [4, 6], def: 4 },
  activity: { title: 'ACTIVITY LOG', spans: [4, 8, 12], def: 8 },
};

/**
 * The widgets that CANNOT render without the Admin snapshot, and are therefore
 * absent from an anonymous layout no matter what a stored layout asks for:
 * SERVICE and NETWORK THROUGHPUT (contract item 5), STORAGE (its bytes are
 * `store.total_bytes`/`disk.capacity_bytes`, admin-only — IRIS §3) and ACTIVITY
 * LOG (the ring is behind an Admin read surface). A stored layout naming one is
 * not corrupt — the same browser may have been signed in a minute ago — so it is
 * FILTERED for this render rather than discarded.
 */
export const PRIVILEGED_WIDGETS = new Set(['service', 'throughput', 'storage', 'activity']);

/**
 * The template's DEFAULT_LAYOUT (`:873`), verbatim. This is the layout in the
 * owner's screenshot and it tiles the 12-column grid exactly: 4+4+4 then 8+4.
 */
export const DEFAULT_LAYOUT = [
  { id: 'health', span: 4 },
  { id: 'identity', span: 4 },
  { id: 'service', span: 4 },
  { id: 'netmap', span: 8 },
  { id: 'throughput', span: 4 },
];

/**
 * The layout an anonymous visitor gets.
 *
 * SERVICE and NETWORK THROUGHPUT are Admin-only (contract item 5: "Both
 * admin-gated: anonymous/public view unchanged"), and throughput physically
 * cannot render without the admin snapshot — libp2p bandwidth is not on the
 * anonymous surface. Dropping two 4-wide widgets from a 12-column grid would
 * leave a four-column HOLE in row one, which is precisely the "looks wrong"
 * failure. So the two remaining widgets take the width the missing ones freed,
 * using ONLY spans the design already declares for them (health/identity
 * `spans:[4,6]`, netmap `spans:[6,8,12]`).
 *
 * Both layouts therefore tile perfectly, and the ADMIN view — the owner's view —
 * is the template default unchanged.
 */
export const PUBLIC_LAYOUT = [
  { id: 'health', span: 6 },
  { id: 'identity', span: 6 },
  { id: 'netmap', span: 12 },
];

/** The layout for this viewer. `privileged` = a confirmed Admin session. */
export function layoutFor(privileged) {
  return (privileged ? DEFAULT_LAYOUT : PUBLIC_LAYOUT).map((w) => ({ ...w }));
}

// ---------------------------------------------------------------------------
// EDIT LAYOUT (IRIS §2 wave 2) — the design's own mutators (`:883-889`) as pure
// functions. Every one returns a NEW array and never mutates its input, so the
// caller can persist exactly what it renders and the tests can assert the rule
// rather than the demonstration.
// ---------------------------------------------------------------------------

/**
 * Validate a layout read back from storage.
 *
 * The template's rule, verbatim (`:883`): a layout is accepted only if it is a
 * non-empty array in which EVERY entry names a widget the registry knows.
 * One stale id and the whole layout is rejected — a partial repair would silently
 * change an arrangement the operator chose, and a stale id is the shape a widget
 * RENAME leaves behind.
 *
 * Two rules the export does not state, because a static demo cannot hit them:
 *
 *   · A DUPLICATE id is rejected. The renderer is a keyed `{#each}` over `w.id`;
 *     two panels with one key is a hard Svelte error, i.e. a blank dashboard.
 *   · A span outside the widget's own `spans` vocabulary is SNAPPED to `def`
 *     (not rejected). An off-vocabulary span breaks 12-column tiling and leaves
 *     the cycle button with no index to advance from; the arrangement is still
 *     the operator's, so it is repaired rather than thrown away.
 *
 * Returns null when the input is not a usable layout — the caller falls back to
 * DEFAULT_LAYOUT.
 */
export function sanitizeLayout(raw) {
  if (!Array.isArray(raw) || raw.length === 0) return null;
  const seen = new Set();
  const out = [];
  for (const w of raw) {
    if (!w || typeof w.id !== 'string') return null;
    const def = WIDGETS[w.id];
    if (!def) return null;
    if (seen.has(w.id)) return null;
    seen.add(w.id);
    out.push({ id: w.id, span: def.spans.includes(w.span) ? w.span : def.def });
  }
  return out;
}

/**
 * Read the operator's stored layout, falling back to the template default.
 * `storage` is injected (globalThis.localStorage in the app) so the tests never
 * touch a real browser store; a throwing storage (private mode, disabled
 * cookies) is not an error condition — it is simply a viewer with no saved
 * arrangement.
 */
export function readLayout(storage) {
  try {
    const parsed = JSON.parse(storage?.getItem(LAYOUT_KEY) ?? 'null');
    const clean = sanitizeLayout(parsed);
    if (clean) return clean;
  } catch {
    /* unparseable or unreadable — the default is the honest answer */
  }
  return DEFAULT_LAYOUT.map((w) => ({ ...w }));
}

/** Persist a layout. A storage that refuses is non-fatal: the view still holds. */
export function writeLayout(storage, layout) {
  try {
    storage?.setItem(LAYOUT_KEY, JSON.stringify(layout));
    return true;
  } catch {
    return false;
  }
}

/**
 * Move `dragId` to `overId`'s position (`:885`). The design reorders on
 * dragenter, so the grid re-flows under the pointer instead of only on drop.
 */
export function moveWidget(layout, dragId, overId) {
  if (!dragId || dragId === overId) return layout;
  const next = layout.map((w) => ({ ...w }));
  const from = next.findIndex((w) => w.id === dragId);
  const to = next.findIndex((w) => w.id === overId);
  if (from < 0 || to < 0) return layout;
  next.splice(to, 0, next.splice(from, 1)[0]);
  return next;
}

/** Advance one widget to its next declared span (`:886`), wrapping. */
export function cycleSpan(layout, id) {
  return layout.map((w) => {
    if (w.id !== id) return { ...w };
    const spans = WIDGETS[w.id]?.spans ?? [w.span];
    const i = spans.indexOf(w.span);
    return { ...w, span: spans[(i + 1) % spans.length] };
  });
}

/** Remove a widget (`:887`). */
export function removeWidget(layout, id) {
  return layout.filter((w) => w.id !== id).map((w) => ({ ...w }));
}

/** Append a widget at its default span (`:888`); a duplicate is a no-op. */
export function addWidget(layout, id) {
  if (!WIDGETS[id] || layout.some((w) => w.id === id)) return layout;
  return [...layout.map((w) => ({ ...w })), { id, span: WIDGETS[id].def }];
}

/** The template default, fresh (`:889`). */
export function resetLayout() {
  return DEFAULT_LAYOUT.map((w) => ({ ...w }));
}

/** The ADD WIDGET menu's contents: every registered widget not yet placed. */
export function availableWidgets(layout) {
  const used = new Set(layout.map((w) => w.id));
  return Object.keys(WIDGETS)
    .filter((id) => !used.has(id))
    .map((id) => ({ id, title: WIDGETS[id].title }));
}

/**
 * The layout actually rendered for this viewer.
 *
 * ADMIN gets their own arrangement (stored or default). An anonymous viewer gets
 * PUBLIC_LAYOUT: the four privileged widgets cannot render without the admin
 * snapshot, and dropping them out of a 12-column row would leave the hole IRIS
 * called the "looks wrong" failure. Signing out therefore returns the public
 * arrangement WITHOUT touching what the operator saved.
 */
export function renderLayout(privileged, stored) {
  if (!privileged) return PUBLIC_LAYOUT.map((w) => ({ ...w }));
  const clean = sanitizeLayout(stored) ?? resetLayout();
  return clean;
}

/**
 * Pack a layout into 12-column rows, the way `grid-auto-flow: row dense` will.
 * Used by the tests to prove neither layout can leave a gap; the renderer just
 * hands the grid the spans.
 */
export function rowsOf(layout, columns = 12) {
  const rows = [];
  let row = [];
  let used = 0;
  for (const w of layout) {
    const span = Math.min(columns, Math.max(1, w.span));
    if (used + span > columns) {
      rows.push({ items: row, used });
      row = [];
      used = 0;
    }
    row.push(w);
    used += span;
  }
  if (row.length) rows.push({ items: row, used });
  return rows;
}

/** True when every row of a layout fills all 12 columns — no hole, ever. */
export function tilesExactly(layout, columns = 12) {
  return rowsOf(layout, columns).every((r) => r.used === columns);
}
