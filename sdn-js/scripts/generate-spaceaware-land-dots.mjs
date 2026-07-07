/**
 * SpaceAware land-dot grid generator (loop SDN_SPACEAWARE_UI_LOOP.md U0.2).
 *
 * The design's globe.js fetched Natural Earth 110m land GeoJSON from CDNs at
 * runtime and rasterized it to a 2° dot grid cached in
 * localStorage['sdn_land_dots_v3_2deg']. The packaging hard rule forbids any
 * external fetch, so this script performs that exact rasterization ONCE,
 * offline, and emits a compact run-length-encoded TypeScript module
 * (ui/src/lib/globe/land-dots-data.ts) that is committed and inlined into the
 * single-file artifact. Runtime never touches the network.
 *
 * The rasterization is a faithful port of the design handoff's globe.js
 * (design/SpaceAware.io.zip → design_handoff/sdn_console/globe.js): 2° step,
 * lat -58..83, lon -179..179, per-polygon bounding-box prefilter, ray-cast
 * point-in-polygon over all rings, first-hit wins.
 *
 * Usage:
 *   node scripts/generate-spaceaware-land-dots.mjs <path-to-ne_110m_land.geojson>
 *
 * Source data (download once; NOT committed, NOT fetched at build time):
 *   https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_110m_land.geojson
 *   (Natural Earth is public domain.)
 */

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const outPath = path.resolve(__dirname, '..', 'ui', 'src', 'lib', 'globe', 'land-dots-data.ts');

const geojsonPath = process.argv[2];
if (!geojsonPath || !fs.existsSync(geojsonPath)) {
  console.error('usage: node scripts/generate-spaceaware-land-dots.mjs <ne_110m_land.geojson>');
  process.exit(1);
}

const gj = JSON.parse(fs.readFileSync(geojsonPath, 'utf8'));

// --- Rasterization: byte-for-byte port of globe.js (design handoff) ---

// ray-cast point-in-polygon; poly = array of rings, each ring = array of [lon,lat]
function inPoly(lon, lat, rings) {
  let inside = false;
  for (let r = 0; r < rings.length; r++) {
    const ring = rings[r];
    for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
      const xi = ring[i][0];
      const yi = ring[i][1];
      const xj = ring[j][0];
      const yj = ring[j][1];
      if (yi > lat !== yj > lat && lon < ((xj - xi) * (lat - yi)) / (yj - yi) + xi) {
        inside = !inside;
      }
    }
  }
  return inside;
}

function collectPolys(geo) {
  const polys = []; // { rings, bb:[minLon,minLat,maxLon,maxLat] }
  function addPoly(coords) {
    const bb = [180, 90, -180, -90];
    for (let r = 0; r < coords.length; r++) {
      const ring = coords[r];
      for (let i = 0; i < ring.length; i++) {
        const lo = ring[i][0];
        const la = ring[i][1];
        if (lo < bb[0]) bb[0] = lo;
        if (la < bb[1]) bb[1] = la;
        if (lo > bb[2]) bb[2] = lo;
        if (la > bb[3]) bb[3] = la;
      }
    }
    polys.push({ rings: coords, bb });
  }
  const feats = geo.features || geo.geometries || [];
  for (let f = 0; f < feats.length; f++) {
    const g = feats[f].geometry || feats[f];
    if (!g) continue;
    if (g.type === 'Polygon') addPoly(g.coordinates);
    else if (g.type === 'MultiPolygon') {
      for (let m = 0; m < g.coordinates.length; m++) addPoly(g.coordinates[m]);
    }
  }
  return polys;
}

function rasterize(geo) {
  const polys = collectPolys(geo);
  const dots = [];
  const step = 2;
  for (let lat = -58; lat <= 83; lat += step) {
    for (let lon = -179; lon <= 179; lon += step) {
      for (let p = 0; p < polys.length; p++) {
        const bb = polys[p].bb;
        if (lon < bb[0] || lon > bb[2] || lat < bb[1] || lat > bb[3]) continue;
        if (inPoly(lon, lat, polys[p].rings)) {
          dots.push([Math.round(lat * 10) / 10, Math.round(lon * 10) / 10]);
          break;
        }
      }
    }
  }
  return dots;
}

// --- Compact encoding (decoded by ui/src/lib/globe/land-dots.ts) ---
// Grid cells are exact integers: lat ∈ {-58,-56,…,82}, lon ∈ {-179,-177,…,179}.
// Rows (one per lat with ≥1 land cell) are ';'-joined:
//   <latIdx base36>:<run>,<run>,…   run = <startLonIdx base36>+<length base36>
// where latIdx = (lat+58)/2 and lonIdx = (lon+179)/2.
function encode(dots) {
  const rows = new Map(); // latIdx -> sorted lonIdx[]
  for (const [lat, lon] of dots) {
    if (!Number.isInteger(lat) || !Number.isInteger(lon)) {
      throw new Error(`non-integer grid dot: ${lat},${lon}`);
    }
    const latIdx = (lat + 58) / 2;
    const lonIdx = (lon + 179) / 2;
    if (!Number.isInteger(latIdx) || !Number.isInteger(lonIdx)) {
      throw new Error(`off-grid dot: ${lat},${lon}`);
    }
    if (!rows.has(latIdx)) rows.set(latIdx, []);
    rows.get(latIdx).push(lonIdx);
  }
  const parts = [];
  for (const latIdx of [...rows.keys()].sort((a, b) => a - b)) {
    const lons = rows.get(latIdx).sort((a, b) => a - b);
    const runs = [];
    let start = lons[0];
    let prev = lons[0];
    for (let i = 1; i <= lons.length; i++) {
      if (i < lons.length && lons[i] === prev + 1) {
        prev = lons[i];
        continue;
      }
      runs.push(`${start.toString(36)}+${(prev - start + 1).toString(36)}`);
      if (i < lons.length) {
        start = lons[i];
        prev = lons[i];
      }
    }
    parts.push(`${latIdx.toString(36)}:${runs.join(',')}`);
  }
  return parts.join(';');
}

const dots = rasterize(gj);
const encoded = encode(dots);

// Round-trip self-check before writing.
const decoded = [];
for (const row of encoded.split(';')) {
  const [latPart, runsPart] = row.split(':');
  const lat = parseInt(latPart, 36) * 2 - 58;
  for (const run of runsPart.split(',')) {
    const [startPart, lenPart] = run.split('+');
    const start = parseInt(startPart, 36);
    const len = parseInt(lenPart, 36);
    for (let k = 0; k < len; k++) decoded.push([lat, (start + k) * 2 - 179]);
  }
}
const key = (d) => `${d[0]}|${d[1]}`;
const a = new Set(dots.map(key));
const b = new Set(decoded.map(key));
if (a.size !== dots.length || b.size !== decoded.length || a.size !== b.size || [...a].some((k) => !b.has(k))) {
  console.error('round-trip mismatch — refusing to write');
  process.exit(1);
}

const banner = `/**
 * GENERATED FILE — do not edit by hand.
 *
 * 2° land-dot grid rasterized from Natural Earth 110m land (public domain)
 * by scripts/generate-spaceaware-land-dots.mjs, replacing the design
 * prototype's runtime CDN GeoJSON fetch (packaging hard rule: the UI is a
 * single self-contained artifact — zero external requests).
 *
 * ${dots.length} dots. Decoded by ./land-dots.ts (decodeLandDots).
 */

export const LAND_DOTS_ENCODED =
`;

// Emit as a single-line string constant (simplest valid TS; minifier handles it).
fs.writeFileSync(outPath, `${banner}  ${JSON.stringify(encoded)};\n`);

console.log(`land-dots-data.ts written: ${outPath}`);
console.log(`  dots: ${dots.length}`);
console.log(`  encoded: ${Buffer.byteLength(encoded)} bytes (raw JSON pairs would be ~${Buffer.byteLength(JSON.stringify(dots))} bytes)`);
