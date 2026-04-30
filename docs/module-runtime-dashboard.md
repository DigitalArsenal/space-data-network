# Module Runtime Dashboard Operations

The module runtime dashboard is an authenticated SDN admin surface. Runtime
snapshot reads use `GET /api/v1/modules/runtime`; mutations use same-origin
cookie auth plus CSRF protection through `X-Requested-With: XMLHttpRequest`.

## Runtime Controls

- Option mutations use
  `PATCH /api/v1/modules/runtime/{moduleId}/options/{optionKey}` with
  `{"value":"..."}`.
- Timer and cron interval options are `live-only`: they update the in-memory
  scheduler and restart cron goroutines, but they do not rewrite config files.
- Option metadata includes units, min/max, default value, restart-required, and
  persistence fields so UI controls can validate before submit.
- Lifecycle actions use
  `POST /api/v1/modules/runtime/{moduleId}/actions/{actionId}`.
- `stop` and `clear-error` are implemented in the base manager. `start`,
  `restart`, and `reload-manifest` remain advertised but disabled until a
  specific runtime can recreate module instances safely.

## Dashboard Data

Each module snapshot entry can include:

- status and recent status history;
- manifest methods, protocols, timers, and capabilities;
- WASM memory stats;
- host/runtime stats such as invoke count, error count, last invoke time,
  average latency, timer run count, and last timer status;
- mutation-ready runtime options;
- lifecycle actions;
- log and event links.

## OrbPro Integration Decision

OrbPro should keep the SDN dashboard as a separate SDN admin surface for now.
Deep links from OrbPro are appropriate once an embedded/admin route and trust
boundary are finalized, but OrbPro should not proxy state-changing dashboard
mutations until the admin auth and CSRF model is explicitly carried through the
proxy.

The stack should advance its `repos/space-data-network` pin to the SDN commit
that contains this dashboard API after the SDN component commit is pushed. The
stack pin must not move before the component commit exists remotely.
