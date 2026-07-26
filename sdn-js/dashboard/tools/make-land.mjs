#!/usr/bin/env node
/*
 * One-time generator for src/land.json — the inline coastline geometry the
 * status-dashboard globe renders (single-file page law: zero external tiles
 * or runtime map fetches; the only geo inputs at runtime are the feed's
 * mmdb-resolved LAT/LON fields).
 *
 * Source: Natural Earth 1:110m land via the world-atlas package
 * (https://cdn.jsdelivr.net/npm/world-atlas@2.0.2/land-110m.json), public
 * domain. This script decodes the TopoJSON arcs (delta-encoded, quantized)
 * into lon/lat polygon rings, drops micro-islands, rounds to 2 decimals
 * (~1.1 km — far below the globe's rendering resolution) and writes
 * src/land.json as [[ [lon,lat], ... ], ...].
 *
 * Run: node dashboard/tools/make-land.mjs   (from sdn-js; network required)
 * The output is COMMITTED; rerun only to refresh the source data.
 */
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SRC = 'https://cdn.jsdelivr.net/npm/world-atlas@2.0.2/land-110m.json';
const out = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../src/land.json');

const topo = await (await fetch(SRC)).json();
const { scale, translate } = topo.transform;

// Decode one delta-encoded arc into absolute [lon, lat] pairs.
function decodeArc(arc) {
  let x = 0;
  let y = 0;
  return arc.map(([dx, dy]) => {
    x += dx;
    y += dy;
    return [x * scale[0] + translate[0], y * scale[1] + translate[1]];
  });
}

const arcs = topo.arcs.map(decodeArc);

// Stitch a TopoJSON ring (list of arc indexes, ~i = reversed arc i) into one
// coordinate ring.
function ring(arcIndexes) {
  const pts = [];
  for (const index of arcIndexes) {
    const coords = index < 0 ? [...arcs[~index]].reverse() : arcs[index];
    // Consecutive arcs share their junction point; skip the duplicate.
    for (const pt of pts.length ? coords.slice(1) : coords) pts.push(pt);
  }
  return pts;
}

const land = topo.objects.land;
const rings = [];
for (const geom of land.geometries ?? [land]) {
  const polys = geom.type === 'Polygon' ? [geom.arcs] : geom.arcs;
  for (const poly of polys) {
    for (const r of poly) rings.push(ring(r));
  }
}

const rounded = rings
  .map((r) => r.map(([lon, lat]) => [Math.round(lon * 100) / 100, Math.round(lat * 100) / 100]))
  .filter((r) => r.length >= 8); // drop micro-islands below drawing resolution

fs.writeFileSync(out, JSON.stringify(rounded));
const points = rounded.reduce((n, r) => n + r.length, 0);
console.log(`[make-land] wrote ${out}: ${rounded.length} rings, ${points} points, ${fs.statSync(out).size} bytes`);
