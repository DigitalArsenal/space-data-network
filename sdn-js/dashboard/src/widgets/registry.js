/*
 * THE WIDGET REGISTRY — discovered, not hand-wired.
 *
 * Every directory under `widgets/` that contains a `widget.js` IS a widget. This
 * file globs them and produces the metadata map the layout engine, the ADD tray
 * and the renderer all read. Adding a widget means creating ONE directory and
 * editing NO existing file; that is the whole point, and it is what
 * `sdn-dashboard-modularize-for-parallelism` is measured against.
 *
 * WHY THE METADATA IS SPLIT FROM THE COMPONENT (`components.js`): this module is
 * imported by node-layout.js, which is imported by the pure-logic tests running
 * in vitest's `node` environment. Metadata must stay importable without dragging
 * eight compiled Svelte components in behind it. The component glob is a
 * separate module that only the renderer imports.
 *
 * The declaration a widget makes about itself:
 *
 *   id            required, and must equal its directory name (asserted below —
 *                 a mismatch would give a stored layout an id nothing renders)
 *   title         the panel kicker + the ADD tray's label
 *   spans         the span vocabulary EDIT LAYOUT cycles through
 *   def           the span it takes when added
 *   order         its place in the registry, i.e. in the ADD tray
 *   privileged    true when it CANNOT render without the admin snapshot
 *   defaultSpan   present = it is in the signed-in default layout, at this span
 *   publicSpan    present = it is in the anonymous layout, at this span
 *
 * The last two are what let a new widget opt itself into a layout without
 * editing the layout file — the final hand-wired list that would otherwise have
 * survived this refactor.
 */

const modules = import.meta.glob('./*/widget.js', { eager: true });

/** `./health/widget.js` -> `health` */
const dirOf = (p) => p.split('/')[1];

const declared = Object.entries(modules)
  .map(([p, m]) => {
    const dir = dirOf(p);
    const id = m.id ?? dir;
    if (id !== dir) {
      // Loud, not silent: a stored layout names ids, so an id that disagrees
      // with the directory it was discovered in resolves for the registry and
      // not for the renderer — a widget-shaped hole no test would explain.
      throw new Error(`widget "${dir}" declares id "${id}" — the id must be the directory name`);
    }
    if (!m.title) throw new Error(`widget "${dir}" declares no title`);
    if (!Array.isArray(m.spans) || m.spans.length === 0) {
      throw new Error(`widget "${dir}" declares no span vocabulary`);
    }
    if (!m.spans.includes(m.def)) {
      throw new Error(`widget "${dir}" default span ${m.def} is not in its own spans [${m.spans}]`);
    }
    return { ...m, id, order: Number.isFinite(m.order) ? m.order : 1e6 };
  })
  .sort((a, b) => a.order - b.order || a.id.localeCompare(b.id));

/**
 * The registry, in declared order. Insertion order IS the ADD tray's order, so
 * the sort above is load-bearing: `import.meta.glob` hands back alphabetical
 * keys, and alphabetical is not the order the design lays these out in.
 *
 * Shape is EXACTLY what the previous hand-written literal exposed
 * (`{title, spans, def}`) plus the new authoring fields, so every existing
 * consumer and test reads it unchanged.
 */
export const WIDGETS = Object.freeze(
  Object.fromEntries(
    declared.map((w) => [
      w.id,
      Object.freeze({
        title: w.title,
        spans: Object.freeze([...w.spans]),
        def: w.def,
        order: w.order,
        privileged: Boolean(w.privileged),
      }),
    ])
  )
);

/**
 * The widgets that CANNOT render without the Admin snapshot, and are therefore
 * absent from an anonymous layout no matter what a stored layout asks for.
 * Derived from each widget's own `privileged` flag rather than listed here.
 */
export const PRIVILEGED_WIDGETS = new Set(declared.filter((w) => w.privileged).map((w) => w.id));

/** The signed-in default layout, assembled from each widget's own `defaultSpan`. */
export const DEFAULT_LAYOUT = Object.freeze(
  declared
    .filter((w) => Number.isFinite(w.defaultSpan))
    .map((w) => Object.freeze({ id: w.id, span: w.defaultSpan }))
);

/** The anonymous layout, assembled from each widget's own `publicSpan`. */
export const PUBLIC_LAYOUT = Object.freeze(
  declared
    .filter((w) => Number.isFinite(w.publicSpan))
    .map((w) => Object.freeze({ id: w.id, span: w.publicSpan }))
);
