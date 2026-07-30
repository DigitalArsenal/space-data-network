/**
 * NODE — the dashboard.
 *
 * The rail entry is the template's own (`SDN Console.dc.html:892`:
 * `['node','NODE','◉','N1']`), glyph verbatim. No `fkey`: nothing in this
 * dashboard binds N1/N2/N3, so the column labelled shortcuts that do not exist
 * (owner directive 2026-07-30). The data is dropped HERE, in the consumer's own
 * table, and the design tree's span is suppressed by App.svelte's rail override
 * — the design tree itself is never hand-edited (ZIP-SYNC LAW).
 */
export const id = 'node';
export const label = 'NODE';
export const glyph = '◉';
export const section = 'NETWORK';
export const title = 'NODE';
/** A ROUTE IS ITS NAME (owner directive 2026-07-30, twice). */
export const sub = '';
export const order = 1;
export const landing = true;
