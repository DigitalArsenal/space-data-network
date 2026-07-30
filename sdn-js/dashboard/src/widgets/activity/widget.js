/** ACTIVITY LOG — the design's `SDN Console.dc.html:871` entry (add-only). */
export const id = 'activity';
export const title = 'ACTIVITY LOG';
export const spans = [4, 8, 12];
export const def = 8;
export const order = 8;
/**
 * Admin-gated: the ring is behind an Admin read surface because it names the
 * peers this host talks to (§16.5).
 */
export const privileged = true;
/* Add-only. */
