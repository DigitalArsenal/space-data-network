/**
 * Land-dot grid loader for SdnGlobe (loop SDN_SPACEAWARE_UI_LOOP.md U0.2).
 *
 * The design prototype's globe.js fetched Natural Earth 110m land GeoJSON
 * from CDNs at runtime, rasterized it to a 2° dot grid and cached the result
 * in localStorage['sdn_land_dots_v3_2deg'], falling back to a graticule-only
 * globe when offline. Under the packaging hard rule (single self-contained
 * artifact, zero external requests) the rasterization now happens at
 * generation time (scripts/generate-spaceaware-land-dots.mjs) and ships
 * embedded in the bundle as a compact run-length-encoded string.
 *
 * Semantics KEPT from the prototype:
 * - cache key 'sdn_land_dots_v3_2deg', value = JSON array of [lat, lon]
 *   pairs (a cache written by the prototype is read as-is, and the cache we
 *   write is readable by the prototype);
 * - dots sorted lat-major exactly as the prototype's rasterize() emitted them;
 * - graticule-only fallback: any failure yields [] (the prototype's
 *   "all sources failed" state) — never a thrown error.
 */

import { LAND_DOTS_ENCODED } from './land-dots-data';

/** localStorage cache key — MUST match the design prototype's globe.js. */
export const LAND_DOTS_CACHE_KEY = 'sdn_land_dots_v3_2deg';

export type LandDot = [lat: number, lon: number];

/**
 * Decode the run-length-encoded 2° land-dot grid.
 *
 * Format (see generate-spaceaware-land-dots.mjs): rows ';'-joined,
 * row = `<latIdx b36>:<start b36>+<len b36>,…` with lat = latIdx*2 - 58 and
 * lon = lonIdx*2 - 179.
 *
 * Throws on malformed input (callers wanting the graticule-only fallback go
 * through loadLandDots, which catches).
 */
export function decodeLandDots(encoded: string = LAND_DOTS_ENCODED): LandDot[] {
  const dots: LandDot[] = [];
  if (typeof encoded !== 'string' || encoded.length === 0) {
    throw new Error('land-dots: empty encoding');
  }
  for (const row of encoded.split(';')) {
    const sep = row.indexOf(':');
    if (sep <= 0) throw new Error(`land-dots: malformed row "${row}"`);
    const latIdx = parseInt(row.slice(0, sep), 36);
    if (!Number.isInteger(latIdx) || latIdx < 0) {
      throw new Error(`land-dots: bad lat index in "${row}"`);
    }
    const lat = latIdx * 2 - 58;
    for (const run of row.slice(sep + 1).split(',')) {
      const plus = run.indexOf('+');
      if (plus <= 0) throw new Error(`land-dots: malformed run "${run}"`);
      const start = parseInt(run.slice(0, plus), 36);
      const len = parseInt(run.slice(plus + 1), 36);
      if (!Number.isInteger(start) || !Number.isInteger(len) || start < 0 || len <= 0) {
        throw new Error(`land-dots: bad run "${run}"`);
      }
      for (let k = 0; k < len; k++) {
        dots.push([lat, (start + k) * 2 - 179]);
      }
    }
  }
  return dots;
}

function isDotArray(value: unknown): value is LandDot[] {
  return (
    Array.isArray(value) &&
    value.length > 0 &&
    value.every(
      (d) =>
        Array.isArray(d) &&
        d.length === 2 &&
        typeof d[0] === 'number' &&
        typeof d[1] === 'number' &&
        Number.isFinite(d[0]) &&
        Number.isFinite(d[1]),
    )
  );
}

/**
 * Load the land-dot grid: localStorage cache first (prototype semantics),
 * else decode the embedded asset and write the cache back (best-effort).
 * Never throws — returns [] (graticule-only fallback) on total failure.
 */
export function loadLandDots(storage?: Pick<Storage, 'getItem' | 'setItem'> | null): LandDot[] {
  const store =
    storage !== undefined
      ? storage
      : typeof localStorage !== 'undefined'
        ? localStorage
        : null;

  if (store) {
    try {
      const cached = store.getItem(LAND_DOTS_CACHE_KEY);
      if (cached) {
        const parsed: unknown = JSON.parse(cached);
        if (isDotArray(parsed)) return parsed;
        // Corrupt/foreign cache: fall through to the embedded asset.
      }
    } catch {
      // Storage unavailable or unparsable cache — fall through.
    }
  }

  let dots: LandDot[];
  try {
    dots = decodeLandDots();
  } catch {
    return []; // graticule-only fallback, exactly like the prototype offline path
  }

  if (store) {
    try {
      store.setItem(LAND_DOTS_CACHE_KEY, JSON.stringify(dots));
    } catch {
      // Quota/privacy-mode failure is non-fatal (prototype behavior).
    }
  }
  return dots;
}
