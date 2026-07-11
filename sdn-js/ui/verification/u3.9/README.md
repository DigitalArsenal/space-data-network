# U3.9 verification evidence — CONJUNCTION view (D4 demo-mode, real sources)

Captured 2026-07-11 by the coordinator against a locally built daemon
(isolated scratch home) at `http://127.0.0.1:15080/console/conjunction`,
live admin session.

## Files

- `console-conjunction-reference-1440x900.png` — the mock's CONJUNCTION
  route (fabricated 4-source stack, live counters, results).
- `console-conjunction-port-1440x900.png` — the port on load: real
  group pills from the shared store, ONE honest DATA SOURCES row
  (Local SDN Catalog · CATALOG LOCAL STORE, real record count in the
  tooltip — no fabricated CelesTrak/SpaceAware provider rows), DEMO
  tags on the LIVE card, SCREENING RESULTS, and PROVENANCE headers.
- `console-conjunction-group-deeplink-1440x900.png` — arriving via
  GROUPS' "SCREEN FOR CONJUNCTIONS" with `?group=geo-watch`: the GEO
  Belt Watch pill pre-selected, strip shows the group's real store
  facts (18 OBJECTS · MY GROUP defined by THIS NODE).

## Real vs demo (D4)

- SCREEN TARGET pills, status strip: REAL — the shared
  `sdn_shared_groups` store (D5), ownership coloring intact.
- DATA SOURCES: REAL — built from `/api/v1/peers` + `/api/v1/channels`
  + `/api/v1/stats`; on this build only the local catalog qualifies
  (peers carry no EPM dn, no sealed MPE channel exists). Synthetic
  fixtures unit-prove a dn-bearing provider yields a catalog row and a
  sealed MPE channel yields the padlocked SEALED ephemeris row.
- PROPAGATOR + CRITERIA: client-side state; Numerical stays PAID-locked
  with an honest tooltip (no storefront listing).
- SCREENING·LIVE ticker, RESULTS, PROVENANCE: demo per D4, DEMO-tagged
  (including the ONE-OFF popover, which the mock leaves untagged —
  intended addition since "RUN ONCE" completes instantly).

## Interactions verified live

- Pill switch updates the strip from real store data; `?group=` deep
  link pre-selects (GROUPS hands it over now — coordinator wired
  `groupConjunctionPath` into GroupsView's SCREEN FOR CONJUNCTIONS).
- Criteria steppers, PAUSE STREAM (ticker stops, PAUSED state),
  ONE-OFF RUN popover (LOOK-BACK stepper + RUN ONCE + backfill note),
  TABLE/JSON/CSV modes, source toggle; reorder arrows disabled at
  boundaries (single live row).
- "3D" → `/orbital?group=geo-watch` (query survives).
- Console clean; 6 same-origin requests (document, peers, channels,
  stats, health, auth/me). Zero external.

## Structure

ConsolePlaceholder.svelte deleted — every console view now routes to a
real component; Phase U3 complete.
