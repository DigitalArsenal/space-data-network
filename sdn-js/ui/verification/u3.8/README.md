# U3.8 verification evidence — GROUPS view (client-local per D5)

Captured 2026-07-11 by the coordinator against a locally built daemon
(isolated scratch home) at `http://127.0.0.1:15080/console/groups`.

## Files

- `console-groups-reference-1440x900.png` — the mock's GROUPS route.
- `console-groups-port-1440x900.png` — the port on first load (seeded
  fixture groups): PRIMITIVES legend with a DEMO tag on PEER GROUPS,
  ALL/MY GROUPS/PEER GROUPS filters, directory with hexagon glyphs and
  "3 mine · 3 peer-defined" caption, ✕ remove controls on MY-GROUP rows
  only, "+ NEW GROUP" row, detail card with a DEMO tag on CONJUNCTION
  MONITOR and both mock action buttons.
- `console-groups-created-persisted-1440x900.png` — a user-created
  group surviving a full page reload.

## D5 data model

`localStorage['sdn_shared_groups']`, schema extracted verbatim from the
mock's own fixture array (`SDN Console.dc.html:839-846`) and asserted
by a schema-stability test: `{id, name, owner, ownerName, count,
regime, scope, conj, conjN, maxPc, nextTca, tcaH, updated}` — verified
live: a created group serialized exactly those 13 keys, so the Orbital
port (U5.x) can share the store unchanged.

## Honesty (D4)

- The mock has NO native CRUD (its script only reads the key) — the
  "+ NEW GROUP" row and MY-GROUP-only ✕ controls are intended
  additions mandated by the task, styled to the design system.
- The 3 provider fixture groups and the whole conjunction column /
  CONJUNCTION MONITOR are demo data (no peer-group or conjunction
  surface exists) — DEMO-tagged at the legend and monitor level;
  user-created groups render an honest "—" conjunction cell and 0 OBJ.

## Live checks

- Create → row + storage (13-key schema), survives reload; delete →
  removed from storage; peer/demo rows immutable (exactly 3 ✕ controls,
  one per MY group).
- Filters: MY GROUPS / PEER GROUPS / ALL partition the directory.
- `?group=` deep link selects the named group in the detail card.
- OPEN IN 3D → `/orbital?group=coordinator-test-group` — the QUERY
  SURVIVES navigation now: coordinator fixed `router.ts` `navigate()`,
  which canonicalized the whole path through `matchSpaceAwareRoute` and
  silently dropped query strings (pre-existing; also affected `?route=`
  hand-offs). SCREEN FOR CONJUNCTIONS → `/console/conjunction`.
- Console clean; 3 same-origin requests (document, health, auth/me) —
  the view itself needs zero API calls (client-local per D5).

## Residuals

- No membership editor (user groups honestly show 0 OBJ) — future task.
- Cross-screen check (group appears in Orbital's groups panel) deferred
  to U5.3 per the tracker: Orbital is still a scaffold.
