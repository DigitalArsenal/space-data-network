/*
 * The NODE dashboard's LAYOUT ENGINE — the template's own rules, transcribed
 * from the design source they are declared against:
 *
 *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
 *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
 *   WIDGETS :863-871 · DEFAULT_LAYOUT :873 · LKEY :862 · mutators :883-889
 *
 * IRIS ruling 2026-07-30 §1: "No import, no conversion, no hand-edit" — the
 * export STAYS in SpaceAware-UI and the widgets are implemented HERE, from the
 * design system's own primitives. Every mutator the design defines (`:883-889`)
 * is a PURE FUNCTION here, so node-console-logic.test.js can prove the rules
 * that the export only demonstrates.
 *
 * WHAT THIS FILE IS NO LONGER (sdn-dashboard-modularize-for-parallelism): the
 * widget TABLE. `WIDGETS`, `PRIVILEGED_WIDGETS` and both layouts used to be
 * hand-written literals here, which made this file — and the eight-branch
 * `{#if w.id}` chain that matched it — a file every widget task had to edit.
 * They are now DISCOVERED from `widgets/*\/widget.js` (see widgets/registry.js);
 * a widget declares its own title, spans, privilege and layout membership in its
 * own directory. The exports below are re-exported unchanged so every consumer
 * and every test reads exactly the shape they always did.
 */
import {
  WIDGETS as DISCOVERED_WIDGETS,
  PRIVILEGED_WIDGETS as DISCOVERED_PRIVILEGED,
  DEFAULT_LAYOUT as DISCOVERED_DEFAULT,
  PUBLIC_LAYOUT as DISCOVERED_PUBLIC,
} from './widgets/registry.js';

/** The design's localStorage key, verbatim (`:862`). */
export const LAYOUT_KEY = 'sdn_node_layout_v1';

/**
 * The template's eight widgets and its span vocabulary (`:863-871`), as each
 * widget directory declares itself. `spans` is the cycle EDIT LAYOUT offers;
 * `def` is the span a widget takes when it is added.
 *
 * TITLES ARE LABELS, NOT SPEC SHEETS (owner directive 2026-07-30, given twice:
 * "remove ... ALL superfluous descriptions / tags from all the menus here"). The
 * template's own titles carried implementation tags after a middot — `PEER MAP ·
 * GEOIP` named the geolocation database, `STORAGE · FLATSQL` named the storage
 * engine. Neither is a fact the reader of a panel header needs. Each widget now
 * carries that correction in its own `widget.js`, next to the panel that prints
 * it, so the two cannot drift apart again.
 */
export const WIDGETS = DISCOVERED_WIDGETS;

/**
 * The widgets that CANNOT render without the Admin snapshot, and are therefore
 * absent from an anonymous layout no matter what a stored layout asks for:
 * SERVICE and NETWORK THROUGHPUT (contract item 5), STORAGE (its bytes are
 * `store.total_bytes`/`disk.capacity_bytes`, admin-only — IRIS §3) and ACTIVITY
 * LOG (the ring is behind an Admin read surface). A stored layout naming one is
 * not corrupt — the same browser may have been signed in a minute ago — so it is
 * FILTERED for this render rather than discarded.
 *
 * Each of those four says so itself (`privileged: true` in its `widget.js`);
 * this set is the roll-call, not the decision.
 */
export const PRIVILEGED_WIDGETS = DISCOVERED_PRIVILEGED;

/**
 * The template's DEFAULT_LAYOUT (`:873`). This is the layout in the owner's
 * screenshot and it tiles the 12-column grid exactly: 4+4+4 then 8+4. Assembled
 * from the widgets that declare a `defaultSpan`, in registry order.
 */
export const DEFAULT_LAYOUT = DISCOVERED_DEFAULT;

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
 * `spans:[4,6]`, netmap `spans:[6,8,12]`) — each declared as that widget's own
 * `publicSpan`.
 *
 * Both layouts therefore tile perfectly, and the ADMIN view — the owner's view —
 * is the template default unchanged.
 */
export const PUBLIC_LAYOUT = DISCOVERED_PUBLIC;

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
