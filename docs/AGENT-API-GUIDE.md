# Space Data Network — Interaction Guide

Commands below ran against a live local daemon at `http://localhost:7173`
(suite 1.0.4, standards 1.186.0) unless noted.

Local nodes listen on `http://127.0.0.1:7173` by default; `spacedatanetwork status`
prints the admin URL, `spacedatanetwork open` opens it.

---

## 1. Verify a node is alive and read data anonymously

```bash
# 1. Who is this node?
curl http://localhost:7173/api/v1/version
# -> {"agent_version":"spacedatanetwork/1.0.4","kubo_version":"0.40.0-dev",
#     "standards_version":"1.186.0","suite_version":"1.0.4"}

curl http://localhost:7173/api/v1/id
# -> {"peer_id":"16Uiu2HAku...","listen_addresses":["/ip4/...","..."],
#     "agent_version":"...","suite_version":"...","standards_version":"..."}

# 2. What does this node hold, and what are its rate limits?
curl http://localhost:7173/api/v1/catalog
# -> {"peer_id":"...","capabilities":["data_query","data_publish","pubsub"],
#     "rate_limits":{"max_record_bytes":10485760,"publish_per_minute":10,"query_per_minute":1000},
#     "schemas":[{"name":"TBS.fbs","record_count":1078755,"total_bytes":758000000}, ...]}

# 3. The anonymous record index (CIDs + a few indexed columns, no content):
curl "http://localhost:7173/api/v1/data/index?schema=TBS&page=1&limit=3"
# -> {"as_of":"2026-08-27T18:52:13Z","limit":3,"page":1,"rows":[
#       {"cid":"bafkreia2224p3z4dnboyfhfach4bhalke76gch7tb73gto5e6nj5fi2k5a","epoch":null,"norad":null}, ...],
#     "schema":"TBS.fbs","stale":false,"total":1078755}

# 4. Health check and the human-readable API reference (both anonymous):
curl http://localhost:7173/api/v1/data/health
# -> {"component":"spaceaware-data-api","status":"ok","time":"..."}

curl http://localhost:7173/api/v1/docs
# -> 200 text/html  (the Gateway API reference UI; zero off-node requests)
```

If step 1 fails, the daemon is not running or not on that port. Record CONTENT
is a separate gate from the index (section 2).

---

## 2. The anonymous surface

The auth wall is default-deny: `/api/**` admits an unauthenticated request only
in three cases.

### What is anonymous

1. **Unconditional POST** (no session, no CSRF):
   - `POST /api/auth/challenge`, `POST /api/auth/verify` — sign-in ceremony
     (section 3); public by design (no session yet).
   - `POST /api/storefront/listings/search`
   - `POST /api/storefront/payments/stripe/webhook`

2. **GET/HEAD of the public read paths** (verbatim source:
   `sdn-server/cmd/spacedatanetwork/main.go`, `isPublicReadAPIPath`):
   - `GET /api/v1/version`, `/api/v1/id`, `/api/v1/stats`
   - `GET /api/node/info` — full public identity: peer_id, versions,
     EPM-derived addresses, chain proofs, listen addresses, onion address.
   - `GET /api/node/epm`, `/api/node/epm/json`, `/api/node/epm/vcard`,
     `/api/node/epm/qr`
   - `GET /api/relay/status` — peer_id, connections, configured_nodes,
     max_connections, load, mode, versions, uptime_seconds.
   - `GET /api/v1/catalog`, `/api/v1/data/health`
   - `GET /api/v1/data/index?schema=<CODE>&page=&limit=` — anonymous CID index
     (capped at 200 rows/page; `limit` > 200 clamped).
   - `GET /api/v1/pubsub/topics`, `/api/v1/pubsub/messages?schema=&limit=`
   - `GET /api/v1/channels`, `/api/v1/channels/<standardCode>`
   - `GET /api/apps`, `/api/v1/apps/default`, `/api/v1/apps/records/<...>` —
     app feed and app record bytes.
   - `GET /api/storefront/listings`, `/api/storefront/listings/<anything>`,
     `/api/storefront/trust/`
   - `GET /api/v1/trust/bond` — the node's security bond.
   - `GET /api/module-delivery/provider`, `/api/module-delivery/listings` —
     the provider descriptor and module listings.
   - `GET /api/v1/docs`, `/api/v1/openapi.json`, `/api/v1/docs/<asset>` — the
     API reference itself is anonymous.
   - `GET /api/auth/status` — `{"admin_configured","root_admin_available",
     "users_configured","config_path","wallet_ui_configured", ...}`; tells
     whether a node has an operator yet.
   - `GET /api/directory/...`, `/api/v1/demo/...`, `/api/v1/log/...`
   - `GET /api/v1/peers` — legacy native peer list.
   - `GET /sdn/libp2p.js`

3. **Per-schema record content** — `GET /api/v1/data/<CODE>/bulk` — by
   allow-list, below.

### The fail-closed allow-list: public readability is a property of the standard

`/api/v1/data/<CODE>/bulk` serves record CONTENT, gated by a schema-classified
allow-list, not the path; a schema is anonymous only if named in it. Fail-closed:
a new standard is NOT readable until deliberately allow-listed — the right
default for a store that also holds key material, access grants,
node-internal ledgers.

Public: `OMM` (orbital element sets — the primary catalogue), `CAT` (catalogue
entries), `MPE` (mean parameter ephemerides), `SPW` (space weather indices),
`RFB` (published emitter catalogue), `LKS` (link status), `PNM` (publication
notifications — already broadcast on public pubsub topics), `DPM` (dataset
publication manifests — already pinned publicly), `EPM` (entity profile
messages — the identity records), `APP` (application package manifests),
`EGP` (entity groups — membership assertions over already-public catalogue
records).

NOT public: `TBS` (telemetry bitstreams), `IQC` (integripulse quality check) —
both answer `401` (verified live):

```bash
curl -i "http://localhost:7173/api/v1/data/tbs/bulk?limit=1"
# -> HTTP/1.1 401 Unauthorized
#    {"code":"unauthorized","message":"not authenticated"}

curl -i "http://localhost:7173/api/v1/data/iqc/bulk?limit=1"
# -> HTTP/1.1 401 Unauthorized
#    {"code":"unauthorized","message":"not authenticated"}
```

- Accept spelling is flexible: `OMM`, `omm`, `OMM.fbs` all resolve.
- The anonymous INDEX is served for ANY schema: listing CIDs is not reading
  records. Verified live: the TBS index above queried 1,078,755 records
  anonymously; the TBS content route still refused.
- The allow-list governs READ only. Publishing is a separate, authenticated
  surface (section 4).

### Reading record content: bulk

Served by the node's data-retrieval engine (a WASM flow inside the node, not a
plain Go endpoint):

```bash
curl "http://localhost:7173/api/v1/data/<CODE>/bulk?limit=100&epoch=2026-08-27T00:00:00Z"
```

Parameters: `epoch` (RFC3339 or unix seconds; default now), `profile` or `mode`
(`nearest` / `as_of` / `forward`; default per node config), `limit` (min 1;
default unbounded), `source` (a registered provider source), `format`
(`flatbuffer` default; `json` accepted by the OMM route only).

Response: size-prefixed FlatBuffer stream (8-byte aligned
`[u32le length][bytes]` frames; content type `application/vnd.sdn.flatbuffers.stream`)
or, for OMM `format=json`, a bare JSON array with schema-exact keys
(`NORAD_CAT_ID`, `EPOCH`, `MEAN_MOTION` — never lowercased). Both encodings
carry a record count header and an ETag; send `If-None-Match` for
`304 Not Modified`.

Failure modes:

- `401` — standard not on the allow-list (TBS, IQC, every not-yet-listed
  standard). Permanent by design; not a retry condition.
- `503 "flow unavailable: engine hot window not ready"` — standard IS public
  but the query engine has not hydrated its hot window yet (observed verbatim
  on the authoring node for `omm/bulk` and `pnm/bulk`). Warm-up: retry with
  backoff. Light routes (`/api/v1/catalog`, `/api/v1/data/index`,
  `/api/v1/data/health`) stay live meanwhile.

---

## 3. The authenticated surface — signing in as an operator

Ed25519 challenge-response with a wallet key — no passwords, no bearer tokens.
Identity: BIP-32 xpub + Ed25519 signing public key (64 hex chars). The signing
key signs; the xpub is the account address the operator row is stored under.

### The ceremony

```bash
TS=$(date +%s)
# The challenge request carries the signing public key and, optionally, the xpub.
# ts must be within 2 minutes of the server clock (verified: outside that window
# the answer is 400 {"code":"invalid_timestamp", ...}).
curl -X POST http://localhost:7173/api/auth/challenge \
  -H 'Content-Type: application/json' \
  -d "{\"xpub\":\"xpub6...\",\"client_pubkey_hex\":\"<64-hex Ed25519 public key>\",\"ts\":$TS}"
# -> {"challenge_id":"cca6041ac1...","challenge":"q4XAAWmLAvjCTZDBeBuwSA4UKNLr3DKSmCV/WAJrlG0",
#     "expires_at":1787856891}

# Then sign the raw challenge bytes with the Ed25519 private key and:
curl -X POST http://localhost:7173/api/auth/verify \
  -H 'Content-Type: application/json' \
  -d '{"challenge_id":"...","challenge":"...","client_pubkey_hex":"<64-hex>","xpub":"xpub6...","signature_hex":"<128-hex Ed25519 signature of the raw challenge bytes>"}'
# -> 200, Set-Cookie: sdn_wallet_session=...; HttpOnly; SameSite=Lax; [Secure]
#    {"user":{"xpub_fingerprint":"...","name":"...","trust_level":"admin"},"expires_at":...}
```

Session facts (verified from source):

- Cookie `sdn_wallet_session`: `HttpOnly`, `SameSite=Lax`, `Secure` over TLS or
  `X-Forwarded-Proto: https`. Legacy name `sdn_session` also recognized.
- Challenges single-use, 60 s expiry; limits 60/min per IP and 30/min per xpub
  (challenge), 120/min and 60/min (verify).
- Unknown identities get a real-looking challenge that can never verify — the
  node deliberately does not store it, so no enrollment enumeration. Verified
  live: a made-up xpub returned `200` with a valid `challenge_id`; verification
  would fail.
- Key binding is first-use-wins (TOFU): the first signing key at a successful
  verify binds permanently to the xpub; a conflicting key is refused later. An
  admin can pre-seed the signing key when creating the operator row.
- The node's own root account signs in at admin level without any store row.
  With NO operator at all (no config users, no database rows), the first wallet
  that completes a verify becomes the "Initial Admin" (first-admin bootstrap).
  A BIP-32 master xpub (depth 0) is refused here — it would enumerate the whole
  wallet.

### How to call as an operator from a script

The CLI demonstrates the two sanctioned ways:

1. Auto sign-in as the node's root key: every operator command
   (`spacedatanetwork accounts list`, `... status`, `... providers`, ...)
   completes the full challenge-verify ceremony with the node's own key and
   reuses the session cookie.
2. Pass an existing session token explicitly:

```bash
export SDN_SESSION_TOKEN="<token>"
spacedatanetwork accounts list
# or on every command:
spacedatanetwork accounts list --session-token "$SDN_SESSION_TOKEN"
```

There is no way to mint a session token without completing the challenge-verify
ceremony once.

### What an operator can see and do

Trust levels: `never`, `unknown`, `marginal`, `standard`, `full`, `admin`,
`ultimate` (seven values).

- Anonymous + sign-in reads: anything in section 2, plus `GET /api/auth/me`
  (who you are, at any trust tier).
- `standard` (default floor for publishing): write data (section 4).
- `admin`: everything, including the operator matrix (`/api/auth/users`), peer
  trust management (`/api/peers`), the admin data surface
  (`/api/v1/data/scan|stream|query|records|...`), imports/exports, and
  `GET /api/accounts` (the merged account view).
- `ultimate` is reserved for the node's own identity. Operator rows: `unknown`
  through `admin`, never `never` (lockout) and never `ultimate`; both refused
  with `400 {"code":"invalid_trust_level"}`.
- Below the required tier: `403 {"code":"forbidden"}`.

Operator life cycle:

- Operator rows are created by: the node's `users:` config block, the
  dashboard's "Enrol a key" form (`POST /api/auth/users`, admin-only), the
  first-admin bootstrap, or the root sign-in's record row.
- `spacedatanetwork accounts trust --xpub <xpub> --level <level>` UPDATEs an
  existing row (`PUT /api/auth/users/<xpub>`); never creates one. A nonexistent
  xpub answers `400 {"code":"update_failed","message":"user not found"}`.
- Config-managed operators cannot have trust changed or be removed through the
  API.

---

## 4. Writing data to a node (vendor ingest)

Publish surface: `POST /api/v1/data/publish/{schema}` and
`POST /api/v1/data/publish/batch/{schema}`. Mounted only when the node's
config enables publishing; requires a session. Anonymous attempts are refused
at the wall (verified: `401 {"code":"unauthorized"}`).

### Admission path, in the exact order the node checks it

1. Publishing enabled? No → `403 data publishing is disabled on this node`.
2. Schema from the URL path: missing or invalid → `400`.
3. Schema in the publishing allow-list (config; empty list = all)? No →
   `403 schema not allowed for publishing: <schema>`.
4. Session present? No → `401 no session`.
5. Trust gate (default `standard`, node-configurable). Below → `403`.
6. ABAC policy (if configured): denied → `403 access denied by policy: <reason>`.
7. Body over max record bytes (10 MiB default) → `413 request body too large`.
8. Record's OWN size prefix + FlatBuffer file identifier decide the schema;
   mismatch with the URL path → `400` naming both. A caller can never reach
   another schema's table through an allowed path.
9. Structural FlatBuffer validation → `400 validation failed: ...`.
10. Storage quota → `403`.
11. Stored → `201 {"cid":"<sha256 hex>","schema":"<canonical>","stored_at":"<RFC3339>","bytes":N}`.

Batch mode: same ladder, body of repeated `[u32le size][flatbuffer]` frames,
total capped at 10 times the max record bytes, per-frame results in the
response. Single record body: raw size-prefixed FlatBuffer bytes of the
standard (content type `application/vnd.sdn.flatbuffers` or similar binary
media — not JSON).

Optional provenance query parameters:
`?source_name=...&provider_id=...&batch_id=...&source_url=...` — tagged records
feed the per-source, per-batch progress aggregates on `/api/v1/stats`;
untagged publishes keep the legacy path.

### Streaming

The same `[u32le size][flatbuffer]` framing streams in both directions.

Reads: `GET /api/v1/data/<CODE>/bulk` streams frames as they are written
(`X-SDN-Stream-Format: flatsql-size-prefixed-le-u32`); consume incrementally,
one chunk in memory at a time. sdn-js: `transport.streamData({schema})` returns
an async frame iterator; `iterateSizePrefixedFrameStream(source)` decodes any
byte stream in the same wire format.

Writes: `transport.publishBatchStream(schema, records)` takes any sync or
async iterable of record buffers and carries it in size-bounded batch requests
(default 8 MiB each, under the server's batch body cap), aggregating the
per-record results. Unbounded sources publish without buffering the whole set.

### What is verified — and what is not

Verified on admission: session identity, trust tier, policy, quota, and
structural FlatBuffer validity (well-formed, correct file identifier, schema
conformance).

Individual records are not signed or encrypted, by design. Authentication is
the ingest gate: once a session is admitted and authorized to publish, the
records it sends need no per-message signature.

Signing happens at the publication layer instead. `$PNM` is the digital
signature message: it carries `SIGNATURE` over the `CID`,
`TIMESTAMP_SIGNATURE` over the publish time, the `SIGNATURE_TYPE` naming the
scheme, and `MULTIFORMAT_ADDRESS` — the canonical location where the
corresponding data is found. A PNM rides in a stream of messages or travels on
its own. For dataset updates the CID points at a `$DPM`, which carries the full
verification contract: provider identity, retrieval protocol, canonical query,
result hash, Merkle roots, and `FILE_ID`.

One signature attests a whole publication, bound to its content address —
not one signature per record.

No JSON/NDJSON bulk-write endpoint: batch is size-prefixed FlatBuffer frames
only. JSON is a read-side presentation format only.

Rate limits: per-minute publish rate (this node advertises 10/min in
`/api/v1/catalog`) and per-publisher storage quotas.

The node also runs credentialed first-party acquisition (`spacedatanetwork ingest`
— Space-Track and UDL, each with its own credentialed sign-in) and scheduled
flow-based ingestion; separate lanes from the vendor HTTP API above.

---

## 5. Refusal semantics — what a client should do

HTTP status first, body shape second: the auth wall uses `{"code","message"}`;
the data API uses `{"error":{"message":...}}`.

| Status | Body (real examples) | Meaning | What the client should do |
|---|---|---|---|
| 200 | index/catalog rows; bulk stream; `{"status":"ok"}` | Success | Nothing |
| 201 | `{"cid":"...","schema":"...","stored_at":"...","bytes":N}` | Record stored | Trust the CID; use it for reference |
| 304 | empty | `If-None-Match` hit on a conditional read | Use your cached copy |
| 400 | `{"code":"invalid_timestamp","message":"timestamp outside allowable skew"}` | Bad request: skew, malformed body, route/header schema mismatch, invalid trust level, unknown operator on config-managed row | Fix the request; do not retry unchanged |
| 401 | `{"code":"unauthorized","message":"not authenticated"}` | No or expired session; OR standard not on the anonymous allow-list (bulk reads) | TBS/IQC bulk: give up, permanent by design. Otherwise: sign in (section 3) |
| 403 | `{"code":"forbidden"}`; `data publishing is disabled on this node`; `schema not allowed...`; `access denied by policy...`; `CSRF validation failed (...)` | Below required trust tier, or a policy/CSRF refusal | Elevate trust or change lanes. CSRF: by design for cross-origin browsers — use the documented request signing scheme or a server-side caller |
| 404 | `{"error":{"message":...}}` | No such route / record / flow param | Check the path; never retry blindly |
| 413 | `request body too large` | Over max record bytes (10 MiB default) | Shrink or split the payload |
| 429 | `{"code":"too_many_requests",...}` | Challenge/verify or publish rate limit | Back off; respect `Retry-After` if present |
| 503 | `{"error":{"code":"STORE_BUSY","message":"record store is busy; no cached page for this query yet"},"stale":true}` + `Retry-After: 1` | Record-store read lock held (e.g. a running ingest); no cached index page yet | Retry after ~1 s; the node answers from a cache once one exists |
| 503 | `flow unavailable: engine hot window not ready` (plain text) | Standard is public but the query engine has not hydrated its hot window | Retry with backoff — warm-up, not denial; light routes stay live |
| 503 | `local storage unavailable in edge mode` | Edge mode, no local store | Read-only node |

State-changing browser requests with a session cookie are CSRF-gated:
same-origin Origin, Referer, or `X-Requested-With` (the CLI sends
`X-Requested-With: XMLHttpRequest`). Anonymous public routes skip CSRF — safe
for cross-origin reads.

---

## 6. For AI agents — discovering capability at runtime

Do not hardcode this document's lists; they change as standards are added and
flows are mounted. Discover instead:

1. **Who the node is**: `GET /api/v1/id` then `GET /api/node/info` — peer_id,
   agent/suite/standards versions, the identity record.
2. **What it holds and its limits**: `GET /api/v1/catalog` —
   `capabilities` (`data_query`, `data_publish`, `pubsub`), per-schema
   `record_count` / `total_bytes`, `rate_limits` (`query_per_minute`,
   `publish_per_minute`, `max_record_bytes`).
3. **The generated API reference**: `GET /api/v1/openapi.json` (strong ETag —
   send `If-None-Match`; 304 comes back cheap). Read the stamps:
   - `x-sdn-served-by: flow|native` and `x-sdn-anonymous: true|false` per
     operation, stamped with the EFFECTIVE decision at boot.
   - Unstamped entries are planned routes not mounted yet. Verified live on the
     authoring node: `/api/v1/query` appears in the spec with no stamp and
     answers `401` today.
   - The spec is not a complete route map — the light read surfaces
     (`/api/v1/catalog`, `/api/v1/data/index`, `/api/v1/version`,
     `/api/v1/pubsub/topics`, ...) live outside it. Cross-check both sources.
   - The human-readable form of the same spec is `GET /api/v1/docs` (self-hosted
     UI; its configuration explicitly disables external fonts and network
     agents).
4. **Public readability is a property of the schema, not of a path.** Probe the
   content route, read the refusal:

   ```bash
   curl -i "http://localhost:7173/api/v1/data/<CODE>/bulk?limit=1"
   # 200      -> public, and the engine is hot
   # 503      -> public, engine still warming (retry with backoff)
   # 401      -> not on the anonymous allow-list; content stays behind the gate
   ```

   The only durable definition of public is the allow-list itself
   (`sdn-server/internal/sds/public_read.go`); it is fail-closed, so when in
   doubt, probe.
5. **Prefer reads with ETags**: bulk reads and the spec carry ETags or record
   counts; conditional requests with `If-None-Match` get cheap 304s. Keep the
   ETag from the first response and replay it.
6. **Writing**: same admission ladder as a human (section 4). If you are an
   agent given an operator session token, send it as the `sdn_wallet_session`
   cookie (or use the node's CLI, which proxies the ceremony for you). Do not
   sign individual records — authentication is the gate, and attestation is a
   `$PNM` over the publication's CID (section 4).

---

## 7. Not yet supported

- Any JSON/NDJSON bulk-write endpoint. Batch publish is size-prefixed
  FlatBuffer frames only.
- Any bearer-token scheme. `sdn_wallet_session` is HttpOnly by design; the
  deliberate escapes are the root-key CLI ceremony and `$SDN_SESSION_TOKEN`.
- `spacedatanetwork identity new` — referenced in some example configs but not
  present in the current binary; `spacedatanetwork derive-xpub` derives an xpub
  from an existing BIP-39 mnemonic; key generation happens in the browser
  wallet ceremony.
- A CLI command that creates an operator row: `accounts trust --xpub` updates
  only (creation paths: section 3).
- TLS "origin" mode: TLS is `disabled`, `static`, or managed (automatic
  certificates). HTTP stays plaintext in local/dev modes.
- `/api/v1/query`, `/api/v1/standards`, `/api/v1/peers/{peerId}/pnm`,
  `/api/v1/peers/{peerId}/{standard}/latest`: planned gateway routes served
  only when the corresponding flow bundle is mounted on a node. Unstamped in
  the OpenAPI spec today; probe before relying on them.
- The `peers-discovery` endpoints (described in the API reference) are served
  by optional flow mounts; may not exist on every node — probe first.

---

## 8. Reference — what requires a session

Everything under `/api/**` not listed in section 2 requires a session.
`admin`-gated examples: `/api/v1/data/scan`, `/api/v1/data/stream`,
`/api/v1/data/query`, `/api/v1/data/records/<schema>/<cid>`, `/api/accounts`,
`/api/peers/*` — including `/api/peers/sdn`, `/api/peers/graph` and
`/api/peers/graph/schema`, all of which answer `401` without a session,
`/api/auth/users`, `/api/v1/conjunction/screen`,
`/api/import`, `/api/export`, `/api/v1/modules/runtime/`.

The same classification logic — a single predicate — drives the auth wall,
CORS, and the OpenAPI generator's anonymity stamps.
