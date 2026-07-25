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

/** Unix seconds → relative "just now" / "12s ago" / "5m ago" / "never". */
export function formatLastSeen(unixSeconds, nowMs = Date.now()) {
  const ts = Math.floor(unixSeconds || 0);
  if (!ts) return 'never';
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
