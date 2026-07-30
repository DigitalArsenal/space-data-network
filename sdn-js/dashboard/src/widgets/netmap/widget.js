/** PEER MAP — the design's `SDN Console.dc.html:866` entry. */
export const id = 'netmap';
export const title = 'PEER MAP';
export const spans = [6, 8, 12];
export const def = 8;
export const order = 4;
/** The globe plots the PUBLIC feed; no admin snapshot is involved. */
export const privileged = false;
export const defaultSpan = 8;
/**
 * 12 on the anonymous grid. SERVICE and NETWORK THROUGHPUT are absent there, so
 * the map takes the full row the design already declares for it rather than
 * leaving a hole beside it.
 */
export const publicSpan = 12;
