/**
 * PEERS — the swarm map and the peer directory it plots.
 *
 * Owner directive 2026-07-30: "move the peers table with add peer form, along
 * with the globe, to a whole new menu called Peers". The rail entry is the
 * template's own (`SDN Console.dc.html:892`: `['peers','PEERS','◍','N2']`).
 */
export const id = 'peers';
export const label = 'PEERS';
export const glyph = '◍';
export const section = 'NETWORK';
export const title = 'PEERS';
/** A ROUTE IS ITS NAME (owner directive 2026-07-30, twice). */
export const sub = '';
export const order = 2;

/**
 * Page-lifetime work this route owns: warming the semantic search model shortly
 * after first paint, and keeping node embeddings current, whichever route is on
 * screen. See search.js for why it is not in the component.
 */
export { boot } from './search.js';
