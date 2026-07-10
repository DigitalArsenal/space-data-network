# U3.3 verification evidence — PEERS view wired

Captured 2026-07-10 by the coordinator against a locally built daemon
(isolated scratch home) at `http://127.0.0.1:15080/console/peers`, live
admin session.

## Files

- `console-peers-reference-1440x900.png` — the mock's PEERS route
  (`SDN Console.dc.html?route=peers`, fixture peers).
- `console-peers-port-real-1440x900.png` — the port with 128 REAL swarm
  peers.

## Pixel/geometry results

Chrome (kickers, table title, all four column headers, detail-card
labels, button row) measured element-by-element against the mock:
positions, font sizes, letter-spacings and colors match exactly. An
apparent 12px/5px tab offset turned out to be a DOM-nesting measurement
artifact (the mock measures an inner text span, the port the padded
button — same 4px/11px padding, same box). Content differs by design:
the mock renders four fixture peers with fabricated names/trust/feeds;
the port renders the REAL swarm, honestly:

- NAME = middle-truncated peer id (no name surface exists), full id
  beneath for copyability.
- TRUST = neutral OBSERVED for connected swarm peers — never a
  fabricated TRUSTED; FEEDS/OWNERTRUST/AGENT/DATA FEEDS = honest `—`;
  EPM CID = `NOT PUBLISHED`.
- No PAID chips/callout: `/api/storefront/listings/search` returns zero
  listings on this node (PAID marking unit-tested against synthetic
  listings; `provider_peer_id` is the match field).
- Detail subtitle = "Connected swarm peer · N connection(s)" (real
  `connection_count` from the auth-gated `/api/v1/peers/{id}`), not the
  mock's fabricated "Provider · Analytics" role.

## Interactions verified in-browser

- Filter tabs: TRUSTED and PROVIDERS → honest "NO PEERS MATCH THIS
  FILTER" with `0 PEERS` (no trust/listing surfaces yet); OBSERVED/ALL
  show the real list; count reflects the filtered set.
- Search: peer-id substring narrows 128 → 1 live.
- Row selection populates the detail card (first row auto-selected).
- CONNECT: really POSTs `/api/v1/peers/connect`. Coordinator fix: the
  handler resolves the target via `AddrInfoFromP2pAddr`, which requires
  a `/p2p/<peer-id>` suffix that swarm addrs lack — every connect 400d
  ("cannot extract peer info"). `buildConnectAddr` now appends the
  suffix (tests added); a live connect then returned 200
  `{connected:true}` and the view refreshed. Failure paths render
  honest inline messages (401/403/400/502 mapped).
- vCARD/QR disabled with honest tooltips (no peer-EPM surface — M4).
- No document scroll at 1440×900; panels scroll internally.

## Audits

- Console: clean (fixed one DevTools form-field issue by naming the
  search input; placeholder aligned to the mock's exact text).
- Network: exactly 6 requests, all same-origin — document, `auth/me`,
  `data/health`, `v1/peers`, `v1/peers/{first}`, storefront search.

## Residuals

- No peer-EPM/name/trust/ownertrust/feeds surfaces (M4); PAID badges
  dormant until real listings exist; header TRUSTED-PEERS chip still
  the U3.1 placeholder (M4).
