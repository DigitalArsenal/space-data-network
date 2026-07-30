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
 * WAVE 1 IS READ-ONLY (IRIS §2). There is no drag, no span cycling, no ADD menu
 * and nothing is persisted — LAYOUT_KEY is declared here so wave 2's EDIT LAYOUT
 * inherits the design's exact key instead of inventing one, and nothing reads or
 * writes it yet.
 *
 * The three add-only widgets (peersum / storage / activity, `SDN Console.dc.html`
 * :869-871) are deliberately ABSENT: they are wave 2, and a registry entry for a
 * widget with no renderer is a hole waiting for the first stale layout to find.
 */

/** The design's localStorage key, verbatim (`:862`). Wave 2 uses it. */
export const LAYOUT_KEY = 'sdn_node_layout_v1';

/**
 * The five wave-1 widgets, with the template's own titles and span vocabulary.
 * `spans` is the cycle EDIT LAYOUT will offer; `def` is the span a widget takes
 * when it is added.
 */
export const WIDGETS = {
  health: { title: 'NODE HEALTH', spans: [4, 6], def: 4 },
  identity: { title: 'IDENTITY', spans: [4, 6], def: 4 },
  service: { title: 'SERVICE', spans: [4, 6], def: 4 },
  netmap: { title: 'PEER MAP · GEOIP', spans: [6, 8, 12], def: 8 },
  throughput: { title: 'NETWORK THROUGHPUT', spans: [4, 6], def: 4 },
};

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
