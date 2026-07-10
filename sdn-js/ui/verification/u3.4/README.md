# U3.4 verification evidence — PEER MAP globe wired

Captured 2026-07-10 by the coordinator against a locally built daemon
(isolated scratch home) at `http://127.0.0.1:15080/console/node`, live
admin session, real swarm (~66–110 peers over the pass).

## Files

- `console-node-netmap-3d-1440x900.png` — 3D orthographic globe: dotted
  landmass, arcs from THIS NODE (amber, labeled) to real peers, dim
  markers for unresolved locations.
- `console-node-netmap-2d-1440x900.png` — 2D equirectangular mode via
  the header toggle, same real points.
- `console-node-netmap-tooltip-after-drag-1440x900.png` — canvas
  tooltip on a hovered peer dot ("12D3KooW…QHTsYu / location
  unresolved") after a drag-to-rotate gesture (THIS NODE visibly moved
  ~165px from the pre-drag frame).

## What's real vs honest

- Points: every plotted dot is a real `/api/v1/peers` swarm peer (cap
  200); THIS NODE placed by the same rules as any peer, no
  special-cased fake coordinate.
- Geo per D8 v1: a 3-entry vendored static table (the documented
  production hosts — sdn.spaceaware.io NYC ×2 addrs, celestrak SFO;
  sources cited in `lib/netmap-data.ts`). Everything else gets a
  deterministic peer-id-hashed placement rendered DIM with a
  "location unresolved" tooltip — stable across renders, never
  claiming a country.
- `N COUNTRIES` counts distinct countries among RESOLVED peers only
  ("1 COUNTRIES" live: both resolved hosts are US); LINKS/CONNECTIONS
  stay wired to `stats.connected_peers` (U3.2).
- Trust kinds: no surface exists, so all remote peers render as PEERS —
  no fabricated provider/client classifications; legend chrome
  untouched.
- Coordinator fix: the mock's footer credit "Locations · MaxMind
  GeoLite2" would be a FALSE attribution (no such database ships) —
  replaced with "Locations · static map · approximate".

## Engine

`lib/globe/SdnGlobe.ts` (the shared canvas engine the U0-era demo used)
extended additively with `sublabel` + `resolved` point fields;
`GlobeDemoPanel.svelte` deleted (unreferenced since U3.3). DPR-aware
canvas, rAF torn down on unmount.

## Audits

- Console: completely clean through load, toggle, hover and drag.
- Network: 7 same-origin requests on a fresh load (document assets
  inline; health polls periodically). ZERO external requests — no geo
  API, no tiles.
