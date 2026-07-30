/*
 * The route COMPONENTS, discovered the same way their metadata is.
 *
 * `eager: true` for the same reason the widget glob is eager: the dashboard
 * ships as ONE self-contained file under a strict CSP with a sha256 per inline
 * script, and a lazy glob emits dynamic chunks — a second served file, which the
 * CSP and the single-file law both forbid.
 */
const modules = import.meta.glob('./*/Route.svelte', { eager: true });

/** id -> component, keyed by the directory the component was found in. */
export const ROUTE_COMPONENTS = Object.fromEntries(
  Object.entries(modules).map(([p, m]) => [p.split('/')[1], m.default])
);
