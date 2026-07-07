# SDN Network Gateway API (Phase G design, loops G.1–G.5)

Status: DESIGN + G.1–G.5 implementation record (2026-07-06/07).
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
| `/api/v1/peers` | GET | Known peers (peerstore + DHT + EPM profile) with the standards each publishes | **live** (peers-discovery flow) | G.2 |
| `/api/v1/peers/{peerId}` | GET | One peer's EPM profile + published standards | **live** (peers-discovery flow) | G.2 |
| `/api/v1/standards` | GET | Standards published across known peers, with publishers | **live** (standards-discovery flow) | G.2 |
| `/api/v1/peers/{peerId}/pnm?limit=N` | GET | The peer's newest signed PNMs (default `limit=1`, clamp 100), VERIFIABLE provenance — stored `$PNM` frames verbatim, publisher-attributed by signature | **live** (pnm-history flow) | G.3 |
| `/api/v1/peers/{peerId}/{standard}/latest` | GET | Provider's newest published dataset **batch** for the standard, served from this node's **opt-in** pin (or the node's own publications); unpinned/unmaterialized = honest 404/503 + PNM pointer | **live** (latest-dataset flow) | G.4 |
| `/api/v1/query` | GET | Queryable-surface listing (tables/views/columns + effective caps, enumerated live from the engine) | **live** (public-query flow) | G.5 |
| `/api/v1/query` | POST | Sandboxed public raw SELECT (in-engine read-only sandbox: authorizer, single SELECT, statement timeout, row/byte caps) | **live** (public-query flow) | G.5 |
| `/api/v1/data/omm/bulk` | GET | Per-object OMM epoch-profile stream (data-retrieval flow) | **live** — DELETED in G.6, superseded by `/{peerId}/omm/latest` | C.4 → G.6 |
| `/api/v1/data/…/query` | POST | data-retrieval flow's internal SQL route (UNSANDBOXED, engine-linked) | live; **superseded for public use** by `/api/v1/query` (G.5) — retained as the auth-gated operator surface, see §11 | C.3c → G.5 |
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

**Property-name capitalization — HARD RULE** (user 2026-07-06, memory
`json-schema-capitalization-rule`): every JSON rendering of an SDS record,
anywhere in the stack, uses property names EXACTLY matching the
spacedatastandards.org IDL / JSON Schema capitalization — e.g. OMM fields
are `NORAD_CAT_ID`, `OBJECT_NAME`, `EPOCH`, `MEAN_MOTION`, `BSTAR`, …;
PNM fields are `FILE_ID`, `FILE_NAME`, `CID`, `PUBLISH_TIMESTAMP`,
`SIGNATURE`, `SIGNATURE_TYPE`; EPM fields are `DN`, `LEGAL_NAME`,
`ALTERNATE_NAMES`, `MULTIFORMAT_ADDRESS`, `ENTITY_TYPE`. Never lowercased.
The authoritative source is `spacedatastandards.org/schema/<TYPE>/main.fbs`
(and `lib/json/<TYPE>/*.schema.json`); emitters derive keys from the
generated accessors where possible, and suites cross-check emitted keys
against the IDL so drift fails the build. API-synthesized envelope fields
that are NOT schema fields (`signature_verified`, `attribution`,
`publisher_key`, `peer_id`, `standard`, `schema`, `batch_id`, `error`, …)
STAY lowercase `snake_case` — the case distinction is the contract that
separates schema data from API metadata. Internal host↔module hostcall
envelopes are wire protocol, not public renderings; only the public JSON
surface (and anything spliced into it verbatim, like the latest-dataset
`pnm` pointer) carries the schema-exact names.

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
   Since G.2 the decision is MECHANICAL (`internal/gateway/anonymous.go`,
   wired in `cmd/spacedatanetwork/main.go` after `MountFlows`): a mounted
   route is admitted anonymously iff it declares `anonymous: true` AND
   node config does not veto it (`gateway.anonymous.deny`), plus config
   may extend read access (`gateway.anonymous.allow`); the static
   `isPublicAPIRequest` list remains the native baseline, and deny
   entries veto it too. Path templates (`{peerId}`) match one segment;
   entries ending in `/` are prefixes. ONE predicate object serves the
   auth wall, CORS, CSRF, and the OpenAPI generator: the generator stamps
   the EFFECTIVE decision (`x-sdn-anonymous`) next to the declared one
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
| `/api/v1/peers` (native connected-peers JSON) | anonymous | **REPLACED in G.2** by the peers-discovery flow at the same path (bare-array/$EPM-stream response). When the flow mount claims the path, `CoreAPIHandler.RegisterRoutesWithFlowMounts` yields the read surface and keeps the admin control plane native via method-scoped mux patterns (`POST /api/v1/peers/connect`, `DELETE /api/v1/peers/{peerID}`); without the mount the legacy native routes register unchanged |
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

## 7. G.2 implementation record (discovery)

Delivered 2026-07-06. The G.2 rows in §1 are live; the planned OpenAPI
entries auto-shadow when the mounts are configured.

**Capability node (host side)** — `p2p_read` (registered in
`Node.buildCapRegistry`, handler `internal/modulert/caps/p2p.go`, op prefix
`p2p`). Read-only, deterministic snapshots; the host supplies MATERIALS,
the wasm flow makes every response decision:

- `p2p.peers_snapshot {peer_id?}` — merged peerstore + connected + DHT
  routing table + trust-registry view (self listed first), per-peer addrs /
  connectedness / agent version / published standards (PNM-derived) /
  stored size-prefixed `$EPM` profile frames in the binary stream segment
  (`{"$bin":0}`). Peers known only through stored PNMs are included.
- `p2p.standards_snapshot {peer_id?}` — newest stored signed `$PNM` per
  (publishing peer, standard); the standard is the `.fbs` segment of the
  colon-delimited `FILE_ID` (dataset-pnms CLI rule); frames verbatim in
  entry order, sorted (peer, standard).

**Flows (modules repo)** — `flows/peers-discovery` 0.1.0 (mounted
`/api/v1/peers/`; routes `""` + `"{peerId}"`) and
`flows/standards-discovery` 0.1.0 (mounted `/api/v1/standards`), both
bridge-linked, capability union `[p2p_read]`, `flow check` PASS (cycle
detection + the new SDK api-block validation). Node chain: http-route
`discover` → hostcap/p2p-discovery (decision passthrough + raw snapshot
envelope; `not_found` short-circuits without a hostcall) →
foundation/discovery-shape (stored `$EPM`/`$PNM` frames spliced VERBATIM —
signature-preserving; peers without a stored profile get a synthesized
minimal unsigned EPM: DN = peer id, `/p2p/<peerId>` multiaddr,
ENTITY_TYPE=Node; bare-array json presentation; ONE word-folded FNV-1a-64
etag per logical stream shared by both encodings) → http-respond → egress.

**Host plumbing changes**:

- Trailing-slash flow mounts also register the trimmed EXACT alias
  (`RegisterFlowMounts` → `registerMountAlias`), so `GET /api/v1/peers`
  answers 200 instead of the mux's 301 — §4.1 holds on flow mounts.
- `gateway.anonymous.allow`/`gateway.anonymous.deny` config landed
  (`config.GatewayConfig`); the §4.4 mechanical allowlist is implemented in
  `internal/gateway/anonymous.go` and feeds wall + spec from one predicate.
- Native peers routes yield per §4's disposition table
  (`RegisterRoutesWithFlowMounts`).

**Verification** — unit: `caps/p2p_test.go`, `gateway/anonymous_test.go`,
`api/docs_test.go` (G.2 shadow + anonymous stamps),
`api/coreapi_test.go` (flow-claimed registration); integration:
`flowrt/discovery_flow_integration_test.go` mounts the REAL compiled
bundles at the production paths over the real cap handler — no-redirect
alias, `$EPM`/`$PNM` frame verification, verbatim splice, bare-array json
with peerID + standards, shared ETag across encodings, If-None-Match 304,
unknown-peer/deep-path/POST 404s, hostcall short-circuit. Modules-side
parity: `flows/discovery/tests/flow.test.mjs` runs the SAME bundles in the
JS flow runtime host. Live cold-client evidence is recorded in the loop
doc (G.2 entry).

## 8. G.3 implementation record (PNM provenance)

Delivered 2026-07-06. The G.3 row in §1 is live; the planned OpenAPI entry
auto-shadows when the mount is configured.

**Capability op (host side)** — `p2p.pnm_history {peer_id, limit}` (same
`p2p_read` capability, `internal/modulert/caps/p2p.go`): the stored signed
`$PNM` frames PUBLISHED BY one peer, newest first (PUBLISH_TIMESTAMP
descending, store arrival as tiebreak), deduplicated per publication.

**Attribution honesty** (the point of this surface): the store records only
the GOSSIP-DELIVERING peer id per PNM, which is not necessarily the
publisher. The op therefore attributes by SIGNATURE — an entry belongs to
the requested peer iff its Ed25519 `SIGNATURE` (hex, `SIGNATURE_TYPE=
Ed25519`, payload `"SDN-DPM-PNM\0" + FILE_ID + "\0" + CID` — the
dataset-publication contract in `internal/storage/manifest.go` /
`internal/channels/pnm_verifier.go`) verifies under one of the peer's
publication keys. Key resolution (`Node.buildP2PCapOptions` →
`PublisherKeys`) is the LOCAL half of the exact path the host's own ingest
verification uses (`datasetPublicationPublicKey`): (1) the Ed25519 identity
key extracted from the peer id, (2) the `key_type=signing` /
`address_type=ed25519` key of the peer's EPM directory record (the surface
healed by the 7d2713c5 EPM self-signature fix) — no live network fetch, so
snapshots stay deterministic. Gossip-attributed frames that FAIL
verification are excluded (counted in `gossip_only_excluded`); only when NO
key resolves does the op fall back to gossip attribution, and every entry
carries `signature_verified` + `attribution` (`"signature"`|`"gossip"`) so
provenance is never overstated. Unsigned/malformed frames never appear.

**Client verification chain** (cold client, response bytes only):

1. `GET /api/v1/peers/{peerId}/pnm` → aligned size-prefixed `$PNM` frames
   VERBATIM as stored — signatures intact.
2. From each frame read `FILE_ID`, `CID`, `SIGNATURE` (hex),
   `SIGNATURE_TYPE` (must be `Ed25519`).
3. Publisher key: decode the libp2p peer id (identity-multihash Ed25519
   ids embed it) and/or `GET /api/v1/peers/{peerId}` → the `$EPM` frame's
   `KEYS` entry with `KEY_TYPE=Signing`/`ADDRESS_TYPE=ed25519` (hex
   `PUBLIC_KEY`).
4. `ed25519.Verify(key, "SDN-DPM-PNM\0" + FILE_ID + "\0" + CID, SIGNATURE)`.

**Flow (modules repo)** — `flows/pnm-history` 0.1.0, mounted at the Go 1.22
mux pattern `/api/v1/peers/{peerId}/pnm` (more specific than the
`/api/v1/peers/` subtree, so both mounts coexist; the anonymous-policy
`templateMatch` already speaks `{param}` templates). Node chain: http-route
`discover` (new `pnm_history` route; `?limit` clamped IN-WASM to [1, 100],
default 1 = newest) → hostcap/p2p-discovery `pnm_history` →
foundation/discovery-shape `shape_pnm` (frames verbatim / bare-array json
with the provenance fields / shared word-folded FNV-1a-64 etag; empty
history → 404) → http-respond → egress. Module versions: http-route 0.3.0,
p2p-discovery 0.2.0, discovery-shape 0.2.0. Route-ownership guards landed
with this change: each discovery flow's snapshot node hostcalls only for
routes it OWNS and each shape method 404s sibling routes, so a
pnm_history-routed request hitting the peers mount (e.g. on a node without
the G.3 mount) answers 404 instead of misinterpreting.

**Verification** — unit: `caps/p2p_test.go` (signature attribution incl.
relayed-frame reclaim + impostor exclusion + gossip fallback + newest-first
+ dedup + limit), `api/docs_test.go` (G.3 shadow + anonymous stamps);
integration: `flowrt/pnm_flow_integration_test.go` mounts the REAL compiled
bundle at the production mux pattern alongside the peers subtree over the
real cap handler — 120 REAL-signed fixtures, served newest frame
byte-verbatim and signature-verified from response bytes only, in-wasm
clamp observable (limit=5000 → 100), provenance json fields, shared ETag +
304, impostor/unsigned exclusion, unknown-publisher/POST 404s.
Modules-side parity: `flows/discovery/tests/flow.test.mjs` runs the SAME
bundle in the JS flow runtime host. Live cold-client evidence is recorded
in the loop doc (G.3 entry).

## 9. G.1 verification record

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

## 10. G.4 implementation record (provider-scoped latest + opt-in pinning)

Delivered 2026-07-06. The G.4 row in §1 is live; the planned OpenAPI entry
auto-shadows when the mount is configured.

### "Latest dataset", defined honestly

`GET /api/v1/peers/{peerId}/{standard}/latest` serves **one publication
batch**: the newest batch whose signed `$PNM` verifies under the peer's
publication keys (the G.3 attribution machinery — FILE_ID
`<datasetID>:<schema>:<batchID>[:part-N]` names the batch) AND whose full
shard content is materialized locally. The response bytes are the
provider's **published shard files spliced in window order, byte-verbatim**,
re-verified against the recorded SHA-256 at every materialization
(`storage.MaterializedDatasetBatch`) — exactly the content the PNM/DPM
signature chain covers, NOT "whatever rows are in the store's tables".
Batch-scoping honesty notes:

- Publication batches are **content deltas**: the store's repeat-CID dedup
  keeps a record's original batch tag, so a batch's export carries the
  records **first seen in that batch**. For OMM full-catalog cycles this is
  effectively the whole catalog (epochs churn every record); for
  slow-moving standards (CAT) the newest publication is a delta — `/latest`
  still serves it verbatim because that IS the provider's newest published
  dataset.
- Multi-part batches are served only with a **contiguous window chain from
  offset 0** whose final part is a short window (`record_count <
  window_limit`). A batch whose last known part exactly fills its window is
  indistinguishable from a mid-import series and is NOT served (production
  full-catalog windows are 50K vs ~32K records, so the tail is always
  short).
- When the newest batch is not yet fully materialized, the newest
  **fully-materialized** batch serves instead (the cap marks `fresh:false`
  in its materials); if none is materialized the answer is the honest 503.

### `gateway.pin` (config) — pinning is OPT-IN, never a default

```yaml
gateway:
  pin:
    - peer: 16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3
      standard: OMM        # "OMM" | "omm" | "OMM.fbs"; ""/"*"/"all" = every standard
```

`gateway.pin` gates the **serving surface and the supersede lifecycle** —
deliberately NOT the materialization path:

- **Serving**: `/latest` answers 200 only for pinned `(peer, standard)`
  pairs, plus the node's OWN publications (a provider serving its own
  datasets is not "pinning"; `peerId == self` is always servable).
- **Supersede**: for pinned pairs, each newly materialized publication
  batch evicts the previously pinned batch
  (`storage.SupersedeSourceBatches`, hooked on feed-head import + dataset
  PNM materialization): superseded source-tag rows are deleted and records
  whose CID no longer carries any tag are removed from the legacy table,
  the routed (producer, standard) tables, and the record index — records
  shared with the kept batch survive. Eviction is CHUNKED (2048 CIDs per
  store-lock window + transaction, the 17576fbb lock-window discipline) and
  idempotent. Cached shard/index files of superseded batches are deleted;
  their publication **metadata rows are kept** — they are the few-hundred-
  byte provenance record AND the trusted-peer catch-up dedup key
  (`datasetShardPublicationAlreadyCached`); deleting them would make the
  next catch-up cycle re-materialize the very batch that was just evicted.
- **Interaction with existing catch-up (documented semantics)**: dataset
  feed-head materialization is and remains a TRUSTED-PEER datasync behavior
  (`materializeDatasetFeedHeadAnnouncement` imports only from trust-registry
  peers, `security.require_signed_feed_heads` untouched). With
  `gateway.pin` absent, a node still materializes trusted providers'
  publications into its own store — that feeds the node's own features
  (e.g. host-01's engine-served `/api/v1/data/omm/bulk`) and is NOT
  gateway pinning: `/latest` for such a pair answers 503 + PNM pointer,
  and no supersede/eviction runs (history accumulates exactly as before
  G.4). Turning a pin ON adds serving + bounded retention for that pair;
  turning it OFF restores the pre-G.4 behavior. A pin for a peer that is
  NOT trusted never materializes (no new import path was added) and serves
  503 + pointer — on-demand p2p fetch-through remains a possible
  G-follow-on, decided separately.
- **NOT evicted** (deliberately): append-only FlatSQL stream file bytes
  (control rows are the deletion unit, the payload substrate is append-only
  by design — disk reclamation is a store-compaction concern, not a pin
  concern) and the in-memory engine hot window (a bounded cache — max
  `storage.engine_hot_window` records — rebuilt from the surviving control
  rows at boot; live tombstoning would need a cid→vtab-sequence mapping the
  control tables do not keep).

### 404 vs 503 (documented choice)

- **404** — provider/standard unknown: the node holds NO
  signature-attributable publication PNM for the pair (or the standard is
  not a validated SDS name here). No pointer exists, so none is served.
- **503** — known via signed PNM but not served here: not pinned (opt-in!)
  or pinned-but-not-yet-materialized. Body carries the newest PNM pointer:
  `{"error": …, "pnm": {"cid", "file_id", "batch_id", "publish_timestamp",
  "standard", "schema", "signature_verified", "attribution"}}` — the client
  fetches the publication itself over p2p. **No silent proxying, ever.**
- **406** — `format=json` for a standard without a JSON presentation
  adapter (v1 ships foundation/omm-json only); the fb stream always works.

### Capability op (host side)

`p2p.latest_dataset {peer_id, standard, deliver?}` on the existing
`p2p_read` capability (`internal/modulert/caps/p2p.go`). Materials only —
`known`/`pinned`/`self` flags, the newest PNM pointer, and (when servable)
the batch stream + metadata (`batch_id`, provider/source, record count,
`etag_fnv1a64` = word-folded FNV-1a-64 of the stream, `fresh`); ALL response
decisions stay in wasm. `deliver:"ref"` (the default fb path) delivers the
stream as a hostcall-bridge **body reference** (`p2p_read` became
bridge-aware): the bytes never enter the flow's linear memory — the C.5c
chain `PutBodyRef → {"$sdnbodyref"} descriptor → $HTR BODY_REF →
httpmount TakeBodyRef`. The json path receives the bytes as an inline
envelope segment (omm-json shapes them in wasm). Batch candidates come from
`caps.LatestBatchCandidates` — the SAME selection rule (signed-PNM
newest-first, batch-deduplicated) the node's supersede evaluation uses.

### Flow (modules repo)

`flows/latest-dataset` 0.1.0, mounted at the Go 1.22 mux pattern
`/api/v1/peers/{peerId}/{standard}/latest` (coexists with the peers subtree
and the pnm pattern; anonymous templateMatch speaks multi-`{param}` paths).
Node chain: http-route `discover` (new `latest_dataset` route) →
hostcap/p2p-discovery `latest_dataset` (hostcall; `deliver:"ref"` unless
`format=json`) → discovery-shape `shape_latest` (200 body/body-ref + shared
etag; json → raw `$OMM` stream on the `stream` port; honest
404/503/406 decision rewrites carrying the PNM pointer) →
foundation/omm-json `encode` (json path only) → http-respond (gained the
generic `route=error` decision: explicit status + `pnm` object forwarded
into the body) → egress. Module versions: http-route 0.4.0, p2p-discovery
0.3.0, discovery-shape 0.3.0, http-respond 0.2.0. Fix landed with
http-respond 0.2.0: its `json_string_field` is now colon-anchored — the
G.3 key-vs-value lesson again (`{"route":"error"}` made the bare `"error"`
needle match the route VALUE).

### Verification

Unit: `caps/p2p_test.go` (unknown standard / unpinned pointer / pinned
serve / stale-batch fallback + freshness / self-serving / ref delivery via
a real HostBridge / candidate order+dedup), `config/gateway_pin_test.go`
(defaults-off + normalization + wildcards),
`storage/dataset_latest_test.go` (byte-verbatim serving, SHA/tamper/
truncation refusals, supersede eviction counts incl. shared-CID survival +
multi-chunk windows + idempotence, publication-row retention). Integration:
`flowrt/latest_flow_integration_test.go` mounts the REAL compiled bundle at
the production pattern alongside the peers subtree over the real cap
handler AND real hostcall bridge (BODY_REF end-to-end), with the REAL
`config.GatewayConfig.PinnedStandard` predicate: verbatim bytes + count +
shared etag + 304, bare-array json, unpinned 503 + verified pointer,
unknown 404s, POST 404. Modules-side parity:
`flows/discovery/tests/flow.test.mjs` runs the SAME bundle in the JS flow
runtime host. Live cold-client evidence is recorded in the loop doc (G.4
entry).

## 11. G.5 implementation record (sandboxed public query)

Delivered 2026-07-07. The G.5 rows in §1 are live; the planned OpenAPI
entry auto-shadows when the mount is configured
(`flows.mounts: /api/v1/query` → public-query 0.1.0).

### Sandbox design (defense-in-depth, enforced IN THE ENGINE)

The public surface accepts RAW SELECT text from anonymous callers. Layers,
innermost first (flatsql `flatsql_query_sandboxed`, engine v1.2.0):

1. **wasm memory sandbox** — the engine is a WASI module; nothing it does
   can touch host memory or the filesystem.
2. **Read-only session, structurally** — a `sqlite3_set_authorizer`
   installed for the prepare denies every DDL/DML verb, PRAGMA,
   ATTACH/DETACH, TRANSACTION/SAVEPOINT and temp-object creation, and
   restricts `SQLITE_READ` to the record vtabs / per-source shadow tables /
   unified views (control tables — `sdn_record_index`, auth/session/trust
   rows — and `sqlite_*` reserved names are NOT readable; CTE aliases are
   recognized against a schema enumeration that FAILS CLOSED).
   `sqlite3_stmt_readonly` + result-column checks back it up, and the
   record vtabs have no xUpdate at all. Violations are typed
   (`sandbox: not-authorized: …`) and map to 400.
3. **Single statement** — any non-whitespace prepare tail rejects
   (`multi-statement`, 400).
4. **Statement timeout** — a `sqlite3_progress_handler` steady-clock
   deadline aborts runaway statements with SQLITE_INTERRUPT
   (`timeout`, 422). The engine keeps NO handler installed outside
   sandboxed statements (hot-path cost: one compare per VDBE jump op).
5. **Row/byte caps** — enforced inside the step loop; oversized results
   REJECT (`row-cap`/`byte-cap`, 422), never truncate.

The sandbox bypasses the statement/query/raw-stream caches and the host
raw-stream mirror entirely (no pollution, nothing to invalidate), and a
rejection latches a clean error — no throw, no poison, the engine instance
(the LIVE store engine) stays healthy. This is the C.8b read-only
discipline applied per-statement to the live engine session: no second
store open, typed errors, writes structurally impossible.

**Caps are host policy** (`gateway.query` config; wired through
`caps.StorageCapOptions.QueryCaps` → the `storage.query_sandboxed` op):
`timeout_ms` (default 5000), `max_rows` (default 200000), `max_bytes`
(default 134217728). Zero/absent knobs take the defaults — there is
deliberately no way to configure "unlimited". Abuse posture (user
decision): per-request cost is bounded by the sandbox, so the route is
anonymous; the node ships NO rate limiting beyond the caps — volumetric
abuse is an operator/CDN concern.

### Capability op (host side)

`storage.query_sandboxed {sql, params[tagged], want, deliver}` on the
storage adapter (`internal/modulert/caps/storage.go`), gated on the
DEDICATED `storage_query` grant. `want=stream` executes the all-BLOB raw
form (aligned frames; body-reference delivery like the other gateway
flows); `want=rows` returns the engine-assembled bare-array JSON;
`want=auto` (the json path) tries stream first and falls back to rows for
projections (one extra bounded execution). Sandbox rejections come back
as `{"ok":false,"error":{"message","sandbox":"<code>"}}` — the wasm flow
maps CODES to statuses, never string-matches messages.
`storage.query_surface {}` returns the queryable surface + effective caps.

### Flow (modules repo)

`flows/public-query` 0.1.0, mounted at `/api/v1/query` (bridge linkage,
capability union `[storage_query]`). Node chain: http-route
`route_public_query` (POST body = `{sql, params, format, limit, sort,
profile, epoch, source}` JSON object or raw SQL text, `?format` wins;
GET/HEAD = surface listing; others 404) → flatsql-query `sandbox_query`
(composes the effective SQL IN-WASM: validated `sort`/`limit` wrapping
`SELECT * FROM (sql) ORDER BY "COL" [DIR] LIMIT n`, or the engine epoch
profiles `nearest|as_of|forward` over the unified OMM view with default
epoch = clock.now and the `source` mapped to its SHADOW-TABLE name) →
foundation/omm-json (json presentation of full-record streams) →
http-respond (0.2.1: error decisions forward a machine-readable `code`)
→ egress. Module versions: http-route 0.5.0, flatsql-query 0.2.0,
http-respond 0.2.1.

### Result encodings, honestly

For arbitrary SELECT the result is not necessarily a full record — the
two encodings are defined without inventing a fake fb framing:

- **`format=flatbuffer` (default)** requires an all-BLOB projection
  (typically `SELECT _data …`): the response is the aligned size-prefixed
  frame stream, byte-parity with the engine's unsandboxed raw-stream path.
  The frames are SELF-IDENTIFYING record buffers (file identifier at
  bytes 4–8), so the stream's type is whatever BLOB column was selected —
  `$OMM` today. Projections with scalar cells answer **406**
  (`not-a-record-stream`) directing the caller to `format=json`.
- **`format=json`**: full-record results serve the bare-array record
  presentation (omm-json; SCHEMA-EXACT keys per the hard rule —
  `NORAD_CAT_ID`, `MEAN_MOTION`); tabular projections serve an
  engine-assembled bare array whose keys are the SQL column names
  VERBATIM (`sqlite3_column_name` → schema-exact by construction; BLOB
  cells base64, NaN/Inf → null). Both encodings of the same logical
  stream share ONE word-folded FNV-1a-64 ETag; `If-None-Match` → 304.

### Queryable-surface documentation (no drift by construction)

`GET /api/v1/query` returns the surface enumerated LIVE from the engine
(`storage.PublicQuerySurface`): every readable relation (unified views
with `_source`, shadow tables per provider-source) with columns and
current hot-window record counts, plus the effective caps. The same set
is what the authorizer permits — the listing and the enforcement derive
from the same engine state. Honesty note: the surface is the engine HOT
WINDOW (up to `storage.engine_hot_window` records per schema, default
400K) — the same data `omm/bulk` serves; full history lives in the
append-only stream files, not in SQL.

### Alias-vs-supersede decision (the old query routes)

`POST /api/v1/data/query` (data-retrieval flow, suffix-matched) is
**superseded for public use** and NOT aliased: it is engine-LINKED
(direct wasm linkage, no sandbox, arbitrary SQL incl. control tables) and
sits behind the auth wall per §4 — it remains the OPERATOR/datasync
escape hatch exactly as the §4 disposition table already recorded. The
two routes are different trust domains, so aliasing one onto the other
would either weaken the operator surface or silently un-sandbox the
public one. Its retirement is a per-route decision after Phase G (§4);
the OpenAPI planned entry for `/api/v1/query` documented the supersession
and now serves the real flow ops.

### Verification

Unit: flatsql `test/sandbox-query.test.ts` (the engine-level injection
matrix on BOTH engine hosts, 150/150 repo-green), flatsqlrt
`sandbox_test.go` (typed rejection codes, byte-parity with the raw-stream
path, bounded timeout, no poisoning), `api/docs_test.go` (G.5 shadow +
anonymous stamps incl. the documented anonymous-POST exception).
Integration: `flowrt/query_flow_integration_test.go` mounts the REAL
compiled bundle at `/api/v1/query` over the real storage cap and a REAL
FlatSQL store+engine: verbatim BODY_REF stream + shared etag + 304,
schema-exact json (both record and projection shapes), sort/limit/profile
composition, the queryable surface from the live engine, and an
18-case injection/abuse suite (writes, multi-statement, PRAGMA, ATTACH,
temp writes, transactions, control-table/sqlite_master reads, runaway
recursive CTE, cartesian blowup, oversized results, sort injection, bad
SQL, empty body) — every case the correct status + code in bounded time,
store intact afterwards. Modules-side parity:
`flows/public-query/tests/flow.test.mjs` runs the SAME bundle in the JS
flow runtime host. Live cold-client evidence is recorded in the loop doc
(G.5 entry).
