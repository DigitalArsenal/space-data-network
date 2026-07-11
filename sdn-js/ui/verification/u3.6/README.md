# U3.6 verification evidence — DATA query tab + MODULES sub-view

Captured 2026-07-11 by the coordinator against a locally built daemon
(isolated scratch home) at `http://127.0.0.1:15080/console/data`, live
admin session.

## Files

- `console-data-datatab-reference-1440x900.png` /
  `console-data-modules-reference-1440x900.png` — the mock's DATA tab
  and MODULES sub-view (fixture data).
- `console-data-datatab-port-1440x900.png` — the port's DATA tab on
  EPM: real record metadata (batch_id `local`, real cid,
  `flatbuffer_uri`, `schema_name: "EPM.fbs"`, size_bytes, timestamp),
  TABLE/JSON/CSV toggle, honest caption "record metadata · decoded
  field queries land with /api/v1/query (G.5)".
- `console-data-modules-port-1440x900.png` — MODULES: the one real
  runtime module (AI Query Log / spaceaware-ai-log / LOADED / UNLOAD),
  no fabricated category/provider/PAID chips (marketplace catalog is
  empty on this node).

## Query surface truths

- `POST /api/v1/query` (G.5 decoded-row engine): does not exist
  (authenticated 404) — the adapter feature-detects it per view load
  and falls back silently, so G.5 slots in without code changes.
- Real fallback `POST /api/v1/data/query`: **Admin**-gated, requires
  the `.fbs`-suffixed schema name (`"EPM"` → 0 rows, `"EPM.fbs"` →
  rows), returns record METADATA only (records themselves are raw
  `application/x-flatbuffers` behind `flatbuffer_uri`).
- Schema-exact passthrough proven live: TABLE columns and the CSV
  header render the served keys verbatim (`batch_id, cid,
  flatbuffer_uri, peer_id, provider_id, schema_name, size_bytes,
  source_name, timestamp`); a unit fixture asserts `NORAD_CAT_ID`-style
  keys pass unmodified (G.5 readiness).

## Modules mutation round-trip (live)

`POST /api/v1/modules/runtime/{id}/actions/{actionId}` (Admin-gated;
`cmd/spacedatanetwork/main.go` handleModuleRuntimeMutation). Verified
in-browser: LOADED → UNLOAD click → UNLOADED → START click → LOADED.

**Server bug found + fixed during acceptance** (`plugins/manager.go`):
the runtime snapshot advertised `load` enabled for any unloaded module
purely from status, while the dispatcher hard-requires the
`RuntimeModuleLoader` interface — spaceaware-ai-log doesn't implement
it, so the advertised button could only ever 400 ("module action
\"load\" is not supported by this runtime"). `buildRuntimeModuleActions`
now takes the plugin and gates `load` (and `pause`/paused-`start`, which
need `RuntimeModulePausable`) on the real capability; Go regression
test added. The card mapping correspondingly falls back from `load` to
an enabled `start`, labeled with the verb that actually runs (TS
regression test added).

## Audits

- Console: completely clean on a fresh load.
- Network: 6 same-origin requests on load (document, `auth/me`,
  `data/health`, `v1/channels`, `v1/stats`, `node/info`); the query and
  runtime calls fire on tab/toggle activation, all same-origin. Zero
  external.

## Residuals

- SUBSCRIBE's same-origin `/storefront` href has no mounted page yet
  (client or server) — dormant until real listings exist anyway.
- `/api/module-delivery/listings` `data_base64` is an opaque signed
  licensing descriptor (no display fields) — module cards use
  `POST /api/storefront/listings/search` (`protected_delivery.
  module_id` linkage) instead.
- G.5's response envelope is a guess (`parseDecodedQueryRows` accepts
  conventional shapes) — re-verify when G.5 lands.
