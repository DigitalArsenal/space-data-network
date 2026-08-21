# Cellular Density Tiles — the node-served tile contract

Normative server-side contract for node-served, pre-deconflicted cellular
density tiles (graph task `sdn-cellular-density-tiles`).

> THE TILING CONTRACT LIVES SERVER-SIDE, NOT IN THE CLIENT. The client streams
> tiles with the camera and renders what the server sends; it never invents
> tile geometry, thresholds, budgets or deconfliction semantics. This document
> is the single normative source for all three sides: the flow lane that
> serves the tiles (component `modules`, escalated demand
> `modules-cellular-density-tile-lane`), the client library that consumes them
> (`packages/sandcastle/gallery/_shared/cellularTileStream.js` in OrbPro), and
> the demo that drives them (`cellular-network-performance`).

## 1. Why tiles exist

Owner 2026-08-10: the cellular demo must show WORLDWIDE sites, "without labels",
"120fps", "ALL of them". Measured against the live node, the one-shot worldwide
aggregate answers in ~56-68 s and the browser aborts at 45 s; the record count
is on the order of millions of towers. A worldwide PointPrimitiveCollection of
the full population is beyond even the proven 250k-point budget (measured
360 fps), and a per-site sensor object at worldwide scale is explicitly out of
the contract.

Tiles solve it by making the SERVER bound each answer: the viewer gets the
deconflicted store one bounded tile at a time, and the per-answer size is a
server constant, never a client guess.

## 2. Scope split (who owns what)

| Surface | Owner | Where |
| --- | --- | --- |
| Tile + meta endpoints, tile computation, sampling, density collapse | the aggregate flow's publication node (WASM lane) | `flows/cellular-network-aggregate` + `data-source/cell-tower-ingest` (modules component) |
| Tile scheme math, envelope grammar, request planning | client library | OrbPro `_shared/cellularTileStream.js` |
| Camera-driven streaming + rendering | demo | OrbPro `cellular-network-performance` |

The endpoints are served through the SAME mounted surface as the existing
aggregate (`/api/v1/cellular`), by a NEW tile lane inside the aggregate flow —
the same zero-Go-host-hosts-change pattern as the cache lane
(`mod-cellular-aggregate-cache`). No new mount, no CORS work, no operator
remount: the lane appears under the mount that already exists.

## 3. Tile scheme: Web Mercator XYZ

Scheme id: `xyz` (Spherical Mercator, EPSG:3857, the OSM/Google tile
convention). H3 is acknowledged as a future alternative and is NOT this
contract; a client must refuse a scheme it does not know (fail hard, never
render it by guesswork).

Let `n = 2^z`, z in `[minZoom, maxZoom]` (`minZoom = 0`, `maxZoom` server
constant, default `18`).

Lon/lat to tile index:

```
x = floor((lon + 180) / 360 * n)
y = floor((1 - asinh(tan(latRad)) / PI) / 2 * n)
```

with both clamped into `[0, n-1]`. Tile bounds back:

```
lonWest  = x / n * 360 - 180
lonEast  = (x + 1) / n * 360 - 180
latNorth = atan(sinh(PI * (1 - 2y / n)))        (degrees)
latSouth = atan(sinh(PI * (1 - 2(y+1) / n)))    (degrees)
```

Latitude clamps at `+-85.05112878` deg (the Mercator limit). The antipodal tile
edge at `lon = +-180` is the same edge: `x = n-1` east edge and `x = 0` west
edge.

The client's camera-to-zoom mapping (ground resolution):

```
mpp = 2 * heightM * tan(fovV / 2) / viewportHeightPx     // meters per pixel at nadir
z   = clamp(floor(log2(156543.03392 * cos(latDeg) / mpp)), minZoom, maxZoom)
```

`156543.03392 = 40075016.686 / 256` (WGS84 equator circumference / a 256 px
zoom-0 tile). The client passes its own `heightM`, `fovV`, `viewportHeightPx`
and `latitudeDeg`; nothing is hardcoded.

## 4. Endpoints

Served cache-first from the node store (same semantics and honesty rules as the
aggregate cache lane: no provider fetches on the tile path — deconfliction is
precomputed at ingest; an empty store answers instantly AND says it is empty
with the reason).

### 4.1 `GET /api/v1/cellular/tiles/meta`

Contract envelope (API-synthesized fields lowercase; TBS-derived fields keep
record capitalization):

```json
{
  "scheme": "xyz",
  "minZoom": 0,
  "maxZoom": 18,
  "threshold": 2048,
  "budget": 4096,
  "densityN": 16,
  "deconflicted": true,
  "deconfliction": { "method": "HIGHEST_SAMPLE_COUNT", "statement": "..." },
  "dataset": {
    "dataset": "sds_tbs",
    "epoch": "2026-08-21T00:00:00.000Z",
    "records": 1234567,
    "stale": false,
    "cacheState": "warm"
  },
  "serverTime": "2026-08-21T00:00:00.000Z"
}
```

- `threshold` and `budget` are the SERVER's mode-selection constants (section
  5). The client reads them and may display them; it never changes them.
- `dataset.cacheState` is one of `warm | empty | ingesting` — the SAME honest
  staleness language as the aggregate cache lane, so "the tile layer is behind
  the ingest" is never silent.
- `deconfliction` is the store's own consensus statement; a tile is
  PRE-deconflicted, and this field says by what.

### 4.2 `GET /api/v1/cellular/tiles/{z}/{x}/{y}`

Bounds validation: `z in [0, maxZoom]`, `x,y in [0, 2^z)`; anything else is a
`404 not found` (`route == "not_found"`), never a 200 with empty content.

Tile envelope:

```json
{
  "scheme": "xyz",
  "z": 8, "x": 134, "y": 84,
  "mode": "points",
  "count": 812,
  "threshold": 2048,
  "budget": 4096,
  "sampled": false,
  "points": [
    { "LATITUDE": 40.7, "LONGITUDE": -73.9, "RADIO": 1, "ID": "abc/123",
      "MCC": 310, "MNC": 260, "CELL_ID": 45678, "RANGE_M": 450,
      "SAMPLES": 12, "AVERAGE_SIGNAL_DBM": -86, "OPERATOR": "T-Mobile",
      "FREQUENCY_MHZ": 1900, "SITE_NAME": "Brooklyn 4" }
  ],
  "density": null,
  "deconflicted": true,
  "dataset": { "dataset": "sds_tbs", "epoch": "...", "records": 1234567,
               "stale": false, "cacheState": "warm" },
  "serverTime": "2026-08-21T00:00:00.000Z"
}
```

Density mode differs ONLY in `mode`, `points: null` and `density`:

```json
{
  "scheme": "xyz", "z": 8, "x": 134, "y": 84,
  "mode": "density",
  "count": 104376,
  "threshold": 2048,
  "budget": 4096,
  "sampled": false,
  "points": null,
  "density": {
    "n": 16,
    "cells": [
      { "lon": -73.95, "lat": 40.70, "count": 8123 },
      { "lon": -73.85, "lat": 40.70, "count": 3412 }
    ]
  },
  "deconflicted": true,
  "dataset": { "dataset": "sds_tbs", "epoch": "...", "records": 104376002,
               "stale": false, "cacheState": "warm" },
  "serverTime": "2026-08-21T00:00:00.000Z"
}
```

- `density.n` is the grid dimension (default `16` -> 16x16 = 256 cells).
- `density.cells` carries `n*n` entries with the south-west corner of each
  cell and the deconflicted site count inside it. Corner convention: cell
  `(i,j)` spans `lonWest + (east-west)*i/n .. +1/n` and
  `latSouth + (north-south)*j/n .. +1/n` in the tile's own bounds; the `n*n`
  listing is row-major from the south-west corner.
- `count` is ALWAYS the tile's true deconflicted site count, regardless of
  mode. `count > budget` => density collapsed the tile; nothing is lost from
  the count.
- `sampled` is true ONLY in points mode when `threshold < count <= budget`.
- The client MUST render `mode == "points"` as a PointPrimitiveCollection and
  `mode == "density"` as a density/clustered raster; it MUST NOT render points
  it was not sent, and MUST NOT draw density cells in points mode.

Point fields are a SUBSET of `$TBS` (the full record is served only on
selection, section 6): `LATITUDE`, `LONGITUDE` (required), `RADIO`, `ID`,
`MCC`, `MNC`, `CELL_ID`, `LAC`, `TAC`, `RANGE_M`, `SAMPLES`,
`AVERAGE_SIGNAL_DBM`, `OPERATOR`, `FREQUENCY_MHZ`, `SITE_NAME`. Key spelling is
exactly the TBS IDL capitalization (house rule: SDS JSON keys match IDL).

### 4.3 Caching

Tile responses are cache-first: `Cache-Control: public, max-age=...` bounded by
the ingest cadence, `ETag` over the tile payload, and the existing
`x-sdn-cache-*` headers from the aggregate cache lane. A tile request NEVER
triggers a provider fetch. 404s (out-of-bounds tiles) are cached for a short
TTL only; stale-but-present tiles are served with `x-sdn-cache-stale: true`
and the dataset epoch, never as a 502.

## 5. Mode selection (SERVER-ONLY)

With `threshold` (default 2048) and `budget` (default 4096), both server
constants:

```
count <= threshold          -> mode "points", all sites, sampled=false
threshold < count <= budget -> mode "points", DETERMINISTIC sample, sampled=true
count > budget              -> mode "density", 16x16 grid, points=null
```

- The sample in the middle band is DETERMINISTIC (fixed stride or seeded
  reservoir seeded by `(z,x,y)`, never per-request random), so the same tile
  answers the same points on every request and cache hits stay identical.
- The client never re-derives `mode` from `count`: the server's `mode` field
  is authoritative.
- The client performance floor: points mode is bounded by `budget` per tile;
  density mode is bounded by `n*n` cells. 250k points across ~60 visible tiles
  at budget 4096 = the proven 360 fps envelope; the client MAY aggregate
  render work per frame but MUST NOT drop tiles silently (report them).

## 6. Selection contract (FULL RECORDS ONLY ON SELECTION)

"Full per-site records + sensor objects only on selection" — from the task
contract, and it is a SERVER-side promise as much as a client rule:

- Points mode: the tile point carries enough identity for the InfoBox; the
  client may fetch the full single-site `$TBS` record from the existing
  aggregate surface on selection (one bounded query), and builds a Sensor
  volume only then.
- Density mode: selecting a density cell means requesting a smaller area —
  either the tile at `z+1` covering the cell (the natural drill-down) or the
  existing per-site aggregate with `bbox = cell bounds` and a bounded limit.
  No client at any zoom requests per-site data for every cell it can see.
- NO per-site sensor objects at worldwide scale — a worldwide camera position
  renders points and density cells, never volumes. The bounded near-camera
  sensor set from the one-shot aggregate mode is NOT used in tiles mode.

## 7. Client requirements (normative for OrbPro `_shared/cellularTileStream.js`)

1. Streams with the camera: fetch only tiles the camera can see, at the zoom
   the ground resolution selects; no off-screen prefetch beyond one zoom of
   margin.
2. In-flight dedupe + LRU cache by `z/x/y`; superseded tiles (camera moved
   past them) are dropped from the queue, never fetched "because we started".
3. Concurrency cap (default 4 parallel tile fetches); failures surface as
   failures with the tile key and the lane error — no silent fallback.
4. ZERO external-origin bytes: tiles come from the node gateway origin only.
5. `parseTileEnvelope` FAILS HARD on anything off-contract: unknown `scheme`,
   unknown `mode`, negative or missing `count`, missing `threshold`/`budget`,
   `sampled` with no `mode`. A server that drifts from this document must
   break loudly in the client, never render wrong.
6. The client never emits provider fetches, never re-aggregates, never
   re-samples: the server's deconflicted store is the only data path.
7. Honest staleness: `dataset.cacheState` and `stale` are displayed, never
   papered over; "empty store" is shown as such ("no tiles yet — the node has
   not ingested"), never as zero towers at full accuracy.

## 8. Escalation and status

- Server lane: `modules-cellular-density-tile-lane` (graph escalation from
  `sdn-cellular-density-tiles`, this document is its normative input).
- Client + demo: OrbPro `_shared/cellularTileStream.js`,
  `cellularTileStream.test.mjs`, `cellular-network-performance` demo.
- The demo's one-shot worldwide aggregate mode remains as the explicit
  drill-down and diagnostics path; the tile stream is the default camera
  view.

See `contract.json` in this directory for the machine-readable fixtures every
side must parse and produce.
