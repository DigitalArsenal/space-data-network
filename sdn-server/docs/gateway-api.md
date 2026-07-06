# SDN Network Gateway API (Phase G design, loop G.1)

Status: DESIGN + G.1 implementation record (2026-07-06).
Authority: user directives 2026-07-06 recorded in
`SDN_FLATSQL_REWRITE_LOOP.md` §Phase G; architecture constraints in
`ARCHITECTURE_FLATSQL_FIRST.md` (flows-only, FlatBuffer-first, isomorphic).

**Every SDN node can be an HTTP gateway for the whole network.** Gateway
endpoints are FLOWS — wasm modules mounted on listener paths by node
configuration (`flows.mounts`), never Go handlers. The daemon's only HTTP
job is socket plumbing ($HTQ frame in, $HTR frames out,
`internal/flowrt/httpmount.go`). This document fixes the route scheme, the
response/header conventions, the anonymous-access policy, the flow-manifest
API extension, and the OpenAPI/docs generator that makes the published spec
structurally unable to drift from what is actually mounted.

---

## 1. Route scheme

All gateway routes live under `/api/v1`. `{peerId}` is a libp2p peer ID;
`{standard}` is a space-data-standard name (`omm`, `cat`, `spw`, …).

| Route | Method | Serves | Status | Phase |
|---|---|---|---|---|
| `/api/v1/peers` | GET | Known peers (peerstore + DHT + EPM profile) with the standards each publishes | planned | G.2 |
| `/api/v1/peers/{peerId}` | GET | One peer's EPM profile + published standards | planned | G.2 |
| `/api/v1/standards` | GET | Standards published across known peers, with publishers | planned | G.2 |
| `/api/v1/peers/{peerId}/pnm?limit=N` | GET | The peer's newest signed PNMs (default `limit=1`), verifiable provenance | planned | G.3 |
| `/api/v1/peers/{peerId}/{standard}/latest` | GET | Provider's newest published dataset for the standard, served from this node's **opt-in** pin | planned | G.4 |
| `/api/v1/query` | POST | Sandboxed public raw SELECT (read-only session, single statement, timeout, row/byte caps) | planned | G.5 |
| `/api/v1/data/omm/bulk` | GET | Per-object OMM epoch-profile stream (data-retrieval flow) | **live** — DELETED in G.6, superseded by `/{peerId}/omm/latest` | C.4 → G.6 |
| `/api/v1/data/…/query` | POST | data-retrieval flow's internal SQL route | live (suffix-matched); superseded by `/api/v1/query` in G.5 | C.3c → G.5 |
| `/api/v1/openapi.json` | GET | Generated OpenAPI 3.1 document | **live** (temporary native, §6) | G.1 |
| `/api/v1/docs` (+`/docs/scalar.js`) | GET | Self-hosted Scalar reference UI | **live** (temporary native, §6) | G.1 |

Design rules:

- **Provider-scoped, not node-scoped.** Data URLs name the *publishing
  peer* (`/peers/{peerId}/{standard}/latest`), so any gateway node can
  serve any provider's data it pins, and clients discover rather than
  hardcode hosts. The E-era `/api/v1/data/omm/bulk` (node-implicit
  provider) is a transitional surface and is deleted in G.6 with the
  OrbPro demos migrated in the same change.
- **Pinning is an OPT-IN node option** (`gateway.pin`, G.4), never a
  default. Each publish SUPERSEDES the previous pin (no accumulation).
  Unpinned + unavailable = honest 404/503 carrying the PNM pointer; **no
  silent proxying** in v1 (on-demand p2p fetch-through is a possible
  follow-on, decided separately).
- **No CDN/cache machinery in the node.** Responses carry strong ETags and
  correct conditional-GET semantics; an operator who wants edge caching
  puts a CDN in front of the gateway.
- **Flows only.** Each route group above is a flow (G.2–G.5 add
  capability nodes: p2p/peers, standards-from-PNM, pnm-retrieval,
  pinned-dataset, sandboxed-query). No new native Go routes, with the one
  documented bootstrap exception (§6).

## 2. Response conventions

Two encodings, one surface — the SAME records, the SAME metadata:

- **Default: aligned FlatBuffer stream** —
  `application/vnd.sdn.flatbuffers.stream`, size-prefixed 8-byte-aligned
  frames (`[u32le length][bytes]`), streamed chunked. This is the engine's
  native zero-copy result format (`flatsql_query_raw_flatbuffer_stream`)
  and the wire format OrbPro/sdn-js ingest directly.
- **Opt-in: `?format=json`** — `application/json`, a **BARE top-level
  array** of records (user decision 2026-07-06, implemented in
  foundation/omm-json + http-respond). No envelope object, no `count`/
  `records` wrapper keys: JSON is a presentation adapter over the same
  stream, so its body is the record list and nothing else.

## 3. Header conventions (metadata never rides in the body)

On **both** encodings, every record-stream response carries:

| Header | Meaning |
|---|---|
| `X-SDN-Record-Count` | Number of records: frames of the fb stream / top-level elements of the bare array |
| `ETag` | Content-derived, deterministic (today the flow emits a weak FNV-1a-64 tag, `W/"fnv1a64-…"`, over the stream bytes; engine response-artifact cache keys are the planned upgrade). Identical logical stream ⇒ identical tag on both encodings. |
| `Content-Type` | One of the two media types above |

Conditional GET: `If-None-Match` → `304` (empty body, `ETag` repeated)
works for both encodings. Future stream-level metadata follows the same
rule: new `X-SDN-*` headers, never body envelopes. Producers emit
lower-case header names ($HTR canonical form); HTTP header names are
case-insensitive and specs/docs write `X-SDN-Record-Count`.

## 4. Anonymous-access allowlist policy

**Policy statements:**

1. **API paths never redirect.** Any request to `/api/**` is answered with
   a status code (`401`/`403` JSON), never `302 /login` — regardless of
   `Accept` headers. Implemented in G.1
   (`internal/auth/middleware.go` `RequireAuth` + `isAPIPath`). This
   retires the "epoch endpoint answers 302" oddity: an anonymous browser
   GET of `/api/v1/data/epoch` now reads `401`, not a login page.
2. **Default-deny stays.** Everything under `/api/` requires a session
   unless explicitly allowlisted (`cmd/spacedatanetwork/main.go`
   `isPublicAPIRequest`/`isPublicReadAPIPath`).
3. **The public gateway surface is anonymous by design.** Discovery
   (`/peers`, `/standards`, `/pnm`), provider data (`/{standard}/latest`,
   today `omm/bulk`), the sandboxed `/query`, and the docs
   (`/openapi.json`, `/docs`) are GET/read endpoints on *published* data —
   they join the anonymous allowlist as their flows land. Abuse control on
   `/query` is the sandbox (read-only session, single SELECT, timeout,
   row/byte caps), not authentication.
4. **Declared vs effective.** A flow's manifest REQUESTS anonymous
   placement per route (`api.routes[].anonymous`); the node DECIDES.
   Today the decision is the static allowlist; when G.2 lands, mounts
   feed it mechanically: a mounted route is admitted anonymously iff it
   declares `anonymous: true` AND node config does not veto it
   (`gateway.anonymous.deny`), plus config may extend
   (`gateway.anonymous.allow`). The OpenAPI generator stamps the
   EFFECTIVE decision (`x-sdn-anonymous`, computed by the same predicate
   the auth wall evaluates) next to the declared one
   (`x-sdn-anonymous-requested`), so policy drift is visible in the spec
   itself.
5. **State-changing = authenticated, always.** Anonymous POST exists only
   for `/api/v1/query` (semantically a read) and the auth bootstrap
   (`/api/auth/challenge|verify`).

**Where each existing route lands** (disposition under this policy):

| Surface (today) | Today | Under this policy |
|---|---|---|
| `/api/v1/data/omm/bulk` (flow) | anonymous | anonymous until G.6, then **deleted** (provider-scoped `/latest` replaces it) |
| `/api/v1/data/health` | anonymous | anonymous; native → flow later |
| `/api/v1/data/{summary,datastores,scan,stream,query,epoch,records/,local-replica-stats}` | auth (epoch was the 302 oddity) | stay auth-gated **operator/datasync surface**, now always 401 JSON when anonymous; public equivalents are the G routes (`/query`, `/latest`, `/pnm`). Native → flow or retirement decided per-route after G.5 |
| `/api/v1/peers` (native connected-peers JSON) | anonymous | **replaced in G.2** by the peers flow at the same path (bare-array/EPM-stream response) |
| `/api/v1/peers/connect`, `/api/v1/admin/**`, `/api/v1/flows/`, `/api/v1/modules/runtime*` | admin | unchanged (admin control plane, never anonymous) |
| `/api/v1/{id,version,stats,catalog}` | anonymous | anonymous (node identity/health belong to discovery); native → flow later |
| `/api/v1/pubsub/{topics,messages}` GET | anonymous | anonymous read; `publish` stays auth |
| `/api/v1/{channels,demo,log}/…`, `/api/directory/`, `/api/storefront/listings…` | anonymous read | unchanged; outside the gateway spec (product surfaces, not network gateway) |
| `/api/v1/{search,trust,conjunction}/…` | mixed auth | unchanged; candidates for flow migration after Phase G |
| `/api/node/{info,epm*}`, `/api/module-delivery/*`, `/api/relay/status`, `/api/auth/status` | anonymous | anonymous; `node/info` documented in the spec as native until the discovery flow subsumes it |
| `/api/v1/{openapi.json,docs,docs/*}` | — (new) | anonymous (G.1; an API whose docs need a login is not a public gateway) |

## 5. Flow-manifest API extension

A flow bundle's `flow.json` MAY carry a top-level **`api`** block declaring
the HTTP surface the flow serves. It is authored next to the graph, copied
**verbatim** into the compiled bundle by the SDK flow compiler
(`compileFlowProgram` writes the source flow.json into `dist/`), installed
with the bundle (FlowStore triple), and read at mount time by
`internal/flowrt` (`apidoc.go`) — so the generator documents exactly the
artifact that is serving requests.

```jsonc
"api": {
  "basePath": "/api/v1/data",   // author's canonical mount hint (docs only —
                                // the node's ACTUAL config mount path wins)
  "tag": "data",                // spec tag grouping (+ optional tagDescription)
  "routes": [
    {
      "path": "omm/bulk",       // suffix relative to the mount path;
                                // "{param}" templates pass through
      "method": "GET",          // default GET
      "operationId": "getOmmBulk",
      "summary": "…",
      "description": "…",
      "deprecated": true,        // e.g. scheduled deletion (G.6)
      "anonymous": true,         // REQUESTED allowlist placement (§4.4)
      "params": [                // OpenAPI parameter objects, verbatim
        { "name": "epoch", "in": "query", "schema": { "type": "string" }, "description": "…" }
      ],
      "requestBody": { … },      // OpenAPI requestBody object, verbatim
      "responses": {
        "200": {
          "description": "…",
          "recordStream": true,  // ⇒ generator adds the §3 standard headers
                                 // (X-SDN-Record-Count + ETag) — the header
                                 // definitions have ONE owner, the generator
          "content": {
            "application/vnd.sdn.flatbuffers.stream": {
              "sds": { "schemaName": "OMM.fbs", "fileIdentifier": "$OMM", "rootTypeName": "OMM" },
              "description": "aligned size-prefixed $OMM frames"
            },
            "application/json": {
              "schema": { "type": "array", "items": { "type": "object" } },
              "description": "bare top-level array"
            }
          },
          "headers": { … }       // extra OpenAPI header objects, verbatim
        },
        "304": { "description": "…" },
        "404": { "description": "…" }
      }
    }
  ]
}
```

Semantics:

- The block is **declarative metadata**; the wasm flow remains the single
  runtime authority for routing/params/format/ETag. A mismatch between the
  block and the flow's behavior is a bundle bug, same class as a wrong
  method description in `plugin-manifest.json`.
- `sds` names the space-data-standards FlatBuffer type carried by a stream
  media type; the generator emits it as `x-sds-schema` (media-type level).
- Unknown-key tolerance in the SDK loader means older compilers/hosts
  ignore the block harmlessly; hosts without the G.1 reader simply serve
  no spec entry for the flow.
- Follow-on (G.2, with the first new flow): `space-data-module flow check`
  gains validation of the block (shape, method names, path templates,
  param objects).

First real carrier: `space-data-network-modules/flows/data-retrieval.flow.json`
v0.2.3 (routes `omm/bulk` GET + `query` POST).

## 6. OpenAPI generator + docs UI

`internal/api/docs.go` (G.1):

- **Inputs**: the mounted-flow list (`Node.MountedFlows()` → each flow's
  parsed `api` block + REAL mount path + program ID + bundle version), the
  daemon version, and the node's real anonymous-allowlist predicate
  (`isPublicAPIRequest` — the exact function the auth wall calls).
- **Output**: OpenAPI 3.1 JSON, generated once at daemon start (the mount
  table is fixed per process), served at `GET /api/v1/openapi.json` with a
  strong ETag. Shared `components.headers` define `X-SDN-Record-Count` and
  `ETag` once.
- **Three sources, explicitly marked, flows win**:
  1. `x-sdn-served-by: "flow"` — operations from mounted flows
     (+`x-sdn-flow`, `x-sdn-flow-version`, `x-sdn-mount`,
     `x-sdn-anonymous`, `x-sdn-anonymous-requested`). Authoritative.
  2. `x-sdn-served-by: "native"` — a SMALL static set: `/api/v1/data/health`,
     `/api/node/info`, and the docs surface itself. Skipped automatically
     if a mounted flow claims the path. These migrate to flows later; the
     wider native API (§4 table) is deliberately NOT in the gateway spec.
  3. `x-sdn-status: "planned"` + `x-sdn-planned-in: "G.n"` — the §1 Phase G
     routes with their designed shapes; summaries carry a `[PLANNED G.n]`
     prefix. Each planned entry is skipped automatically the moment a
     mounted flow claims its path — the spec self-updates as Phase G lands.
- **Docs UI**: `GET /api/v1/docs` serves a minimal page bootstrapping
  **Scalar** from `/api/v1/docs/scalar.js` — the vendored
  `@scalar/api-reference` standalone bundle
  (`internal/api/assets/scalar.standalone.js`, MIT, see
  `assets/SCALAR-LICENSE.md`), `withDefaultFonts: false`. The page ships a
  strict CSP (`default-src 'none'`; same-origin script/style/connect;
  `worker-src blob:` for the highlighter) so it **cannot** fetch anything
  off-node — no CDN, no font service. Browser-verified: zero external
  requests.

**Known temporary native routes (bootstrap exception to flows-only):**
`/api/v1/openapi.json`, `/api/v1/docs`, `/api/v1/docs/scalar.js` are native
Go handlers in G.1 because the generator needs the host-side mount table,
which a flow cannot see without a dedicated capability node. Exit path: a
`hostcap/mount-table` capability node (read-only mount+manifest snapshot)
plus a docs flow (spec-assembly node → http-respond), letting the docs
surface itself be a mounted flow. Until then this is the ONLY sanctioned
native addition; it is marked `x-sdn-served-by: native` in its own spec
entry.

## 7. G.1 verification record

- `openapi.json` generated from the REAL mounted data-retrieval flow
  (v0.2.3 bundle mounted at `/api/v1/data/` on a local daemon; operations
  stamped `x-sdn-served-by: flow`).
- Scalar docs page loaded and rendered in a real browser (chrome-devtools)
  against the local daemon; zero external network requests; zero console
  errors.
- Unit tests: `internal/flowrt/apidoc_test.go` (extension parser + REAL
  bundle carries the block), `internal/api/docs_test.go` (generator:
  flow/native/planned marking, record-stream headers, shadowing,
  handler/ETag/CSP), `internal/auth/middleware_test.go`
  (`TestRequireAuth_APIPathsGet401NotRedirect`).
