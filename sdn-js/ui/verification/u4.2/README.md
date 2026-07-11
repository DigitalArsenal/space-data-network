# U4.2 verification evidence — M2 activity/events flow

Captured 2026-07-11 by the coordinator against a locally built daemon
(isolated scratch home), node-activity flow installed + mounted at
`/api/v1/node/activity`.

## The M2 pipeline

1. **Cycle A — sdn-server `19134b3e`**: bounded 256-event activity ring
   (mutex, injectable clock, newest-first, PII-free: peer ids only)
   read by `node_activity_read.activity` ({limit} clamped 1..256 →
   {count,events}). Taps: peer_connected/peer_disconnected (epm
   exchange notifee), pnm_publication (materialized only),
   record_stored (accepted+stored only — rejects/duplicates untapped),
   grant_issued (channels issueGrant via nil-safe optional ring, wired
   from main.go through the new Node.ActivityRing accessor).
2. **Cycle B — modules `ecaf1b4`**: hostcap/node-activity +
   node-activity flow (http-route 0.7.0 `route_node_activity` parses
   ?limit= per the pnm_history clamp convention) → http-respond.
   JSON-only (no SDS activity schema — fb blocked-on-schema).
3. **Cycle C**: installed + mounted; ACTIVITY LOG widget wired
   (buildActivityRows: kind → readable label, truncated peer ids,
   HH:MM:SS times; unknown kinds render verbatim so new server events
   surface without a UI release; anonymous/offline degrades to the
   honest "NO ACTIVITY YET" line).

## Acceptance (cold client + browser)

- Anonymous curl → **401**; authenticated → **200** with REAL events
  (live swarm churn: peer_connected/peer_disconnected with real peer
  ids, newest first, `?limit=` honored).
- `/api/v1/openapi.json` now lists `/api/v1/node/activity` (and
  `/api/v1/node/status`).
- `console-node-activity-live-1440x900.png`: the ACTIVITY LOG widget
  added from the tray renders 8 live events ("16:47:00 Peer connected ·
  12D3KooW…ccPHde", …). Console clean; 10 same-origin requests.
- Tests: Go modulert/api/node suites green (ring -race);
  848 spaceaware TS tests (8 new activity cases); svelte-check clean
  (the sdn-activity-* selectors are used now).

## Residuals

- Deploy-time (release-gated): install
  `flows/node-activity/dist/activity/` + config mount
  `path:/api/v1/node/activity → com.digitalarsenal.flows.node-activity`.
- record_stored/pnm_publication/grant_issued events verified via unit
  taps + the grant flow earlier (U3.7); the scratch node sees mostly
  peer churn organically — richer kinds appear as gossip/grants occur.
- Anonymous reduced subset + fb presentation: same M-level blocks as
  node-status.
