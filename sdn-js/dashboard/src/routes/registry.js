/*
 * THE ROUTE REGISTRY — discovered, not hand-wired.
 *
 * Every directory under `routes/` that contains a `route.js` IS a route: it
 * appears in the rail, it owns a title, and it renders its own `Route.svelte`.
 * Adding a page means creating ONE directory and editing NO existing file.
 *
 * PEERS is the reason this exists. It was added by hand on 2026-07-30 and that
 * one page touched the SECTIONS table, the ROUTE_TITLE table, an import and a
 * branch of App.svelte's route `{#if}` — four edits to a file every other
 * feature also had to edit.
 *
 * Split from `components.js` for the same reason the widget registry is: this
 * module is plain data and stays importable without compiling Svelte.
 *
 * The declaration a route makes about itself:
 *
 *   id        required, and must equal its directory name
 *   label     the rail's entry
 *   glyph     the rail's icon — the template's own (`SDN Console.dc.html:892`)
 *   section   the rail heading it files under
 *   title     the ConsoleHeader title
 *   sub       the ConsoleHeader subtitle. Empty for every route today, and
 *             deliberately: "A ROUTE IS ITS NAME" (owner directive 2026-07-30,
 *             issued twice). The pair shape is kept so ConsoleHeader's `.sub`
 *             span still collapses through the `:empty` rule in App.svelte.
 *   order     its place in the rail
 *   landing   the route rendered before any selection, and the title fallback
 *             for an unknown route
 *   boot      OPTIONAL. Called once on mount for EVERY registered route, not
 *             only the active one, and returns a teardown. It is how a route
 *             keeps page-lifetime work that must not wait for its first visit —
 *             PEERS warms the semantic search model 800ms after first paint from
 *             here, which is exactly what App.svelte used to do on its behalf.
 */

const modules = import.meta.glob('./*/route.js', { eager: true });

const dirOf = (p) => p.split('/')[1];

/** Every registered route, in rail order. */
export const ROUTES = Object.entries(modules)
  .map(([p, m]) => {
    const dir = dirOf(p);
    const id = m.id ?? dir;
    if (id !== dir) {
      throw new Error(`route "${dir}" declares id "${id}" — the id must be the directory name`);
    }
    if (!m.label) throw new Error(`route "${dir}" declares no rail label`);
    return {
      id,
      label: m.label,
      glyph: m.glyph ?? '',
      section: m.section ?? 'NETWORK',
      title: m.title ?? m.label,
      sub: m.sub ?? '',
      order: Number.isFinite(m.order) ? m.order : 1e6,
      landing: Boolean(m.landing),
      boot: typeof m.boot === 'function' ? m.boot : null,
    };
  })
  .sort((a, b) => a.order - b.order || a.id.localeCompare(b.id));

/**
 * The rail's sections, in the order their first route declares them. The
 * template's own rail is one NETWORK group (`SDN Console.dc.html:892`); the
 * grouping is derived so a route can open a second one without editing this
 * file.
 */
export const SECTIONS = (() => {
  const byLabel = new Map();
  for (const r of ROUTES) {
    if (!byLabel.has(r.section)) byLabel.set(r.section, []);
    byLabel.get(r.section).push({ id: r.id, label: r.label, glyph: r.glyph });
  }
  return [...byLabel].map(([label, items]) => ({ label, items }));
})();

/** id -> [title, sub], the pair ConsoleHeader is given. */
export const ROUTE_TITLE = Object.fromEntries(ROUTES.map((r) => [r.id, [r.title, r.sub]]));

/**
 * The route the page opens on, and the title fallback for an unknown one. The
 * fallback used to name `ROUTE_TITLE.nodes` — a key that has never existed — so
 * an unknown route rendered `undefined[0]` and threw.
 */
export const LANDING = (ROUTES.find((r) => r.landing) ?? ROUTES[0])?.id ?? '';
