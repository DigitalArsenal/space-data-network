/**
 * Unit tests for the SpaceAware land-dot grid loader (loop U0.2).
 *
 * The grid replaces the design prototype's runtime CDN GeoJSON fetch with a
 * build-time precomputed asset embedded in the single-file artifact; the
 * localStorage['sdn_land_dots_v3_2deg'] cache-key semantics and the
 * graticule-only fallback must be preserved.
 */
import { describe, expect, it } from 'vitest';
import {
  decodeLandDots,
  LAND_DOTS_CACHE_KEY,
  loadLandDots,
  type LandDot,
} from '../ui/src/lib/globe/land-dots';
import { LAND_DOTS_ENCODED } from '../ui/src/lib/globe/land-dots-data';

function memoryStorage(initial: Record<string, string> = {}) {
  const map = new Map<string, string>(Object.entries(initial));
  return {
    map,
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      map.set(k, v);
    },
  };
}

describe('decodeLandDots (embedded 2° grid)', () => {
  const dots = decodeLandDots();

  it('decodes a plausible number of land cells for a 2° grid', () => {
    // ~12,700 grid cells between lat -58..83; land covers roughly a third.
    expect(dots.length).toBeGreaterThan(3000);
    expect(dots.length).toBeLessThan(6000);
  });

  it('emits only on-grid integer coordinates within the prototype bounds', () => {
    for (const [lat, lon] of dots) {
      expect(Number.isInteger(lat)).toBe(true);
      expect(Number.isInteger(lon)).toBe(true);
      expect(lat).toBeGreaterThanOrEqual(-58);
      expect(lat).toBeLessThanOrEqual(83);
      expect(lon).toBeGreaterThanOrEqual(-179);
      expect(lon).toBeLessThanOrEqual(179);
      // grid: lat ≡ 0 (mod 2) offset from -58 (even), lon odd
      expect((lat + 58) % 2).toBe(0);
      expect(Math.abs(lon % 2)).toBe(1);
    }
  });

  it('is sorted lat-major exactly like the prototype rasterizer output', () => {
    for (let i = 1; i < dots.length; i++) {
      const [aLat, aLon] = dots[i - 1];
      const [bLat, bLon] = dots[i];
      expect(bLat > aLat || (bLat === aLat && bLon > aLon)).toBe(true);
    }
  });

  it('has no duplicate cells', () => {
    const keys = new Set(dots.map(([lat, lon]) => `${lat}|${lon}`));
    expect(keys.size).toBe(dots.length);
  });

  it('contains known land cells and excludes known ocean cells', () => {
    const has = (lat: number, lon: number) =>
      dots.some(([a, b]) => a === lat && b === lon);
    // Continental interiors (grid-aligned): Colorado, central Europe,
    // Siberia, Australia, Brazil.
    expect(has(38, -105)).toBe(true);
    expect(has(50, 9)).toBe(true);
    expect(has(62, 99)).toBe(true);
    expect(has(-24, 133)).toBe(true);
    expect(has(-10, -55)).toBe(true);
    // Open ocean: mid-Pacific, mid-Atlantic, southern Indian Ocean.
    expect(has(0, -139)).toBe(false);
    expect(has(30, -41)).toBe(false);
    expect(has(-40, 85)).toBe(false);
  });

  it('rejects malformed encodings instead of returning garbage', () => {
    expect(() => decodeLandDots('')).toThrow();
    expect(() => decodeLandDots('nonsense')).toThrow();
    expect(() => decodeLandDots('5:zz')).toThrow();
    expect(() => decodeLandDots(':1+2')).toThrow();
  });
});

describe('loadLandDots (cache-key semantics + fallback)', () => {
  it('uses the exact prototype cache key', () => {
    expect(LAND_DOTS_CACHE_KEY).toBe('sdn_land_dots_v3_2deg');
  });

  it('cache miss: decodes the embedded grid and writes the cache back as JSON pairs', () => {
    const store = memoryStorage();
    const dots = loadLandDots(store);
    expect(dots.length).toBeGreaterThan(3000);
    const written = store.map.get(LAND_DOTS_CACHE_KEY);
    expect(written).toBeDefined();
    // Prototype-compatible cache format: JSON array of [lat, lon] pairs.
    const parsed = JSON.parse(written!) as LandDot[];
    expect(parsed).toEqual(dots);
  });

  it('cache hit: returns the cached pairs verbatim (a prototype-written cache keeps working)', () => {
    const prototypeCache: LandDot[] = [
      [38, -105],
      [50.1, 8.7], // the prototype rounded to 0.1° — still valid pairs
    ];
    const store = memoryStorage({
      [LAND_DOTS_CACHE_KEY]: JSON.stringify(prototypeCache),
    });
    expect(loadLandDots(store)).toEqual(prototypeCache);
  });

  it('corrupt cache: falls back to the embedded grid', () => {
    for (const bad of ['not json', '{"a":1}', '[]', '[[1]]', '[["x","y"]]']) {
      const store = memoryStorage({ [LAND_DOTS_CACHE_KEY]: bad });
      const dots = loadLandDots(store);
      expect(dots.length).toBeGreaterThan(3000);
    }
  });

  it('throwing storage: still returns the embedded grid (never throws)', () => {
    const store = {
      getItem: () => {
        throw new Error('denied');
      },
      setItem: () => {
        throw new Error('quota');
      },
    };
    const dots = loadLandDots(store);
    expect(dots.length).toBeGreaterThan(3000);
  });

  it('no storage available: returns the embedded grid', () => {
    const dots = loadLandDots(null);
    expect(dots.length).toBeGreaterThan(3000);
  });

  it('embedded asset is compact (single-file artifact size guard)', () => {
    // The whole point of the encoding: a couple of KB inside the artifact,
    // not tens of KB of JSON pairs or hundreds of KB of GeoJSON.
    expect(LAND_DOTS_ENCODED.length).toBeGreaterThan(500);
    expect(LAND_DOTS_ENCODED.length).toBeLessThan(8192);
  });
});
