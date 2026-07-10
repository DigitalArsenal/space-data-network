# U3.2 verification evidence — NODE widgets wired to real surfaces

Captured 2026-07-10 by the coordinator against a locally built daemon
(isolated scratch home) at `http://127.0.0.1:15080/console/node`, live
admin session (`/api/auth/me` 200). Every rendered value below was
cross-checked against a curl of the same endpoint during the pass.

## Files

- `console-node-real-data-1440x900.png` — default layout, all real data.
- `console-node-qr-real-1440x900.png` — QR overlay with a REAL scannable
  code (client-side `qrcode` encode of the 2.6 KB `/api/node/epm/vcard`
  text).
- `console-node-catalog-widgets-real-1440x900.png` — PEER SUMMARY,
  STORAGE · FLATSQL and ACTIVITY LOG added from the tray, scrolled into
  view.

## Cross-checks (rendered ↔ curl)

- NODE HEALTH: PEER ID `16Uiu2HAm7rB…F8H1Fn`, `MODE · FULL` ↔
  `node/info.peer_id/.mode`; API `127.0.0.1:14001` / GATEWAY
  `127.0.0.1:18080` ↔ the node's OWN `listen_addresses` (see fix below);
  STORAGE `108.9 KB / — capacity unknown` ↔ `stats.total_bytes=111552`
  (no capacity surface exists — honest dash, empty bar).
- IDENTITY: `dn`, `entity_type` (`· NODE`), EPM CID `NOT PUBLISHED`
  (no CID field exists in `epm/json` — M4 follow-up).
- SERVICE: `RUNNING` + `v1.0.4 · suite 1.0.4` ↔
  `node/info.version/.suite_version`; AUTOSTART/UPTIME honest `—` (no
  surface, M1); verbs stay disabled per D6.
- PEER MAP header: `110 LINKS` / `110 CONNECTIONS` ↔
  `stats.connected_peers` (live count, fluctuates); `— COUNTRIES` (no
  GeoIP surface; globe interior still inert → U3.4).
- NETWORK THROUGHPUT: `NO TELEMETRY · pending M1` — no fabricated
  numbers or bars.
- PEER SUMMARY: 3 real swarm peers from `/api/v1/peers` (`{peers:[...]}`
  object, NOT the bare array the plan guessed; entries carry only
  `peer_id`+`addrs` today) — middle-truncated ids, real ADDR counts,
  neutral OBSERVED badge (no fabricated TRUSTED).
- STORAGE · FLATSQL: `108.9 KB / 8 RECORDS`, `STANDARDS 1.136.0`,
  `PRR.fbs 6 RECORDS · 108.6 KB`, `EPM.fbs 2 RECORDS · 384 B` ↔
  `stats.schemas[]` + `node/info.standards_version`, exact.
- ACTIVITY LOG: `NO ACTIVITY DATA · NO SURFACE YET`.

## Coordinator fixes on top of the worker delivery

- `deriveListenAddressRows` picked the first parseable multiaddr — on a
  live node that's a `/p2p-circuit` RELAY reservation, so the widget
  showed the relay's public IP as this node's API address. Circuits are
  now excluded (regression tests added).
- `composeServiceVersionLine` rendered `vspacedatanetwork/1.0.4` — the
  product prefix is now stripped and a duplicated `agent_version`
  dropped (test added).
- QR encode failed at error-correction level M: the vCard (2587 B)
  exceeds M's ~2.3 KB cap — the same reason the server's
  `/api/node/epm/qr` 500s. Encoder now degrades M→L (~2.9 KB) before
  the honest decorative fallback (capacity tests added).
- The encoded QR `<img>` landed in ONE 15 px cell of the fallback's
  11-col CSS grid; it now spans the full grid area.

## Audits

- Console: completely clean on a fresh load.
- Network: exactly 9 requests, all same-origin — document, favicon,
  `auth/me`, `data/health`, `node/info`, `epm/json`, `epm/vcard`,
  `v1/stats`, `v1/peers`. Zero external.

## Residuals (recorded, not fixed here)

- Server `/api/node/epm/qr` 500s on any real vCard ("content too long
  to encode") — server-side gap; UI no longer depends on it.
- No EPM CID surface (M4), no uptime/autostart/telemetry/activity
  surfaces (M1/M2), no storage-capacity surface, no GeoIP country
  counts (U3.4/M2).
- Header `2 TRUSTED PEERS` chip is still the U3.1 placeholder count —
  wiring it to a real trusted-peer surface belongs to U3.3/M4.
