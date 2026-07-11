# U4.1 verification evidence — M1 node-status capability + flow + UI

Captured 2026-07-11 by the coordinator against a locally built daemon
(isolated scratch home) at `http://127.0.0.1:15080/console/node`, live
admin session, node-status flow installed in the scratch flow store and
mounted at `/api/v1/node/status`.

## The M1 pipeline (three cycles, two repos)

1. **Cycle A — sdn-server `d9cb2c99`**: `node_status_read.status`
   hostcall (`internal/modulert/caps/nodestatus.go`): uptime, store
   totals + path, disk statfs (null on failure), honest service state
   (`autostart_known:false` — no surface), libp2p bandwidth
   totals/rates + 24-sample 5s history ring (additive BandwidthReporter
   option + race-safe sampler). 19 Go tests.
2. **Cycle B — space-data-network-modules `d50e7f5`**:
   `hostcap/node-status` wasm capability node + `node-status.flow.json`
   (http-route 0.6.0 `route_node_status` → status → http-respond),
   JSON-only (no SDS NodeStatus schema exists — fb presentation
   blocked-on-schema per generated-types-only; all ~180 codes checked).
   Flow harness 5/5.
3. **Cycle C**: flow installed + mounted; UI widgets wired.

## Server acceptance (cold client)

- Anonymous `curl /api/v1/node/status` → **401**: the manifest's
  `anonymous:false` is enforced by the host's flow-route
  anonymousPolicy (the cycle-B worker's "declarative-only" concern
  resolved empirically — no native shadow route needed).
- Authenticated → **200 application/json** with real values: bandwidth
  history filled to the full 24×5s ring, disk
  `capacity_bytes:1995218165760` (1.8 TB), real uptime.
- `/api/v1/openapi.json` gained `/api/v1/node/status` from the flow
  manifest.

## UI acceptance (`console-node-live-status-1440x900.png`)

- NODE HEALTH storage: `108.9 KB / 1.8 TB` with a real (honestly tiny)
  fill — capacity was "— capacity unknown" before.
- SERVICE UPTIME: real (`00:01` fresh boot; cross-checked 130 s ↔
  `00:02` a minute later); AUTOSTART stays an honest `—`
  (`autostart_known:false`).
- NETWORK THROUGHPUT: mock's widget body restored with REAL data —
  `81.27 KB/s ↓ 2.81 ↑`, bars = the history ring normalized, axis
  `−1m/NOW` reflects the REAL covered span (not the mock's fixed
  −60s); < 2 samples renders a "collecting" line, bandwidth null keeps
  the honest no-telemetry line. 44 new TS tests;
  svelte-check warnings 14 → 7 (all throughput selectors now used).
- Console clean; all requests same-origin.

## Residuals

- Deploy-time: production configs need the
  `path: /api/v1/node/status → flow com.digitalarsenal.flows.node-status`
  mount + the flow artifact installed (this scratch install wrote the
  store directly; the admin `POST /api/v1/flows/deploy` endpoint is the
  production path). Release-gated with everything else.
- Anonymous reduced subset ({uptime, state}) needs caller-trust info in
  the $HTQ envelope (recorded in the flow manifest).
- FlatBuffers presentation blocked on an SDS NodeStatus-style schema.
- `node_status_read` not yet in the SDK's RecommendedCapabilityIds
  (cosmetic flow-check warning).
- ACTIVITY LOG stays honest no-data — that's U4.2 (M2 events flow).
