/** STORAGE — the design's `SDN Console.dc.html:870` entry (add-only). */
export const id = 'storage';
/**
 * "· FLATSQL" is not in the title: which engine backs the bytes is not a fact
 * the reader of a STORAGE panel needs (owner directive 2026-07-30, issued
 * twice). The ADD tray reads this title, so it is kept in step with the panel's
 * own kicker.
 */
export const title = 'STORAGE';
export const spans = [4, 6];
export const def = 4;
export const order = 7;
/**
 * Admin-gated: its numbers are `store.total_bytes` / `disk.capacity_bytes` from
 * the Admin snapshot (IRIS §3). /api/v1/stats looks like an anonymous source but
 * seeds `total_bytes: 0` on a budget miss, i.e. it can report an EMPTY store for
 * a busy one.
 */
export const privileged = true;
/* Add-only. */
