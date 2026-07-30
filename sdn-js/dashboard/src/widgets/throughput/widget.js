/** NETWORK THROUGHPUT — the design's `SDN Console.dc.html:867` entry. */
export const id = 'throughput';
export const title = 'NETWORK THROUGHPUT';
export const spans = [4, 6];
export const def = 4;
export const order = 5;
/**
 * Admin-gated, and physically unrenderable otherwise: libp2p bandwidth is not on
 * the anonymous surface at all.
 */
export const privileged = true;
export const defaultSpan = 4;
/* No publicSpan — see `privileged`. */
