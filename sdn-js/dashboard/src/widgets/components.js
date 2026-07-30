/*
 * The widget COMPONENTS, discovered the same way their metadata is.
 *
 * Separate from registry.js on purpose: registry.js is imported by the pure
 * layout logic and its tests, which run in vitest's `node` environment. This
 * module compiles eight Svelte components and is imported ONLY by the renderer.
 *
 * `eager: true` is required, not stylistic. The dashboard ships as ONE
 * self-contained file (viteSingleFile) served under a strict CSP with a sha256
 * per inline script; a lazy glob emits dynamic chunks, which is a second served
 * file the CSP and the single-file law both forbid.
 */
const modules = import.meta.glob('./*/Widget.svelte', { eager: true });

/** id -> component, keyed by the directory the component was found in. */
export const WIDGET_COMPONENTS = Object.fromEntries(
  Object.entries(modules).map(([p, m]) => [p.split('/')[1], m.default])
);
