/* Pure formatting helpers for the node status view. */

/** first6…last6 of a peer id (mono short form). */
export function shortId(id) {
  if (!id) return '—';
  return id.length <= 16 ? id : `${id.slice(0, 6)}…${id.slice(-6)}`;
}

/** Seconds → compact "1d 2h", "3h 12m", "5m 20s", "42s". */
export function formatUptime(seconds) {
  const s = Math.max(0, Math.floor(seconds || 0));
  if (s === 0) return '0s';
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${sec}s`;
  return `${sec}s`;
}

/**
 * Unix seconds → relative "just now" / "12s ago" / "5m ago" / "not yet seen".
 *
 * THE ZERO CASE USED TO READ "never" and the owner asked what it meant
 * (2026-07-30: "rows that say 'last seen - never', so how did they get there?").
 * "never" is a verdict — it says this peer will not be seen. The fact is
 * narrower and is now what the cell says: the node has not observed it YET,
 * which for a pinned peer is an ordinary state. peers.js `lastSeenLabel` adds
 * the reason ("PINNED · NOT YET SEEN") where the row knows it.
 */
export function formatLastSeen(unixSeconds, nowMs = Date.now()) {
  const ts = Math.floor(unixSeconds || 0);
  if (!ts) return 'not yet seen';
  const delta = Math.max(0, Math.floor(nowMs / 1000) - ts);
  if (delta < 5) return 'just now';
  if (delta < 60) return `${delta}s ago`;
  if (delta < 3600) return `${Math.floor(delta / 60)}m ago`;
  if (delta < 86400) return `${Math.floor(delta / 3600)}h ago`;
  return `${Math.floor(delta / 86400)}d ago`;
}

/** Coordinate pair → "12.3456, -78.9012" (empty if unresolved). */
export function formatCoords(lat, lon) {
  if (!lat && !lon) return '';
  return `${lat.toFixed(4)}, ${lon.toFixed(4)}`;
}
