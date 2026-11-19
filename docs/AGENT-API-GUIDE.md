# Space Data Network — Interaction Guide

Commands below ran against a live local daemon at `http://localhost:7173`
(suite 1.0.4, standards 1.186.0) unless noted.

Local nodes listen on `http://127.0.0.1:7173` by default; `spacedatanetwork status`
prints the admin URL, `spacedatanetwork open` opens it.

Public immutable downloads can also use an enabled publisher's full peer-ID
subdomain. See [Peer-ID URLs and the public cache](peer-id-cdn.md) for the working
CelesTrak example, CID verification, native discovery, and the distinction
between cached artifacts and this node's paginated query API. The in-app API
panel and `/api/v1/docs` include the same public example.

---

## 1. Verify a node is alive and read data anonymously

```bash
# 1. Who is this node?
curl http://localhost:7173/api/v1/version
# -> {"agent_version":"spacedatanetwork/1.0.5","kubo_version":"0.39.0",
#     "standards_version":"1.217.0","suite_version":"1.0.5"}
# kubo_version is the upstream Kubo this node RUNS (suite.versions.json
# kubo.shipped). It used to print "0.40.0-dev", read from the in-repo fork,
# which no release path has ever shipped.

curl http://localhost:7173/api/v1/id
# -> {"peer_id":"16Uiu2HAku...","listen_addresses":["/ip4/...","..."],
#     "agent_version":"...","suite_version":"...","standards_version":"..."}

# 2. What does this node hold, and what are its rate limits?
curl http://localhost:7173/api/v1/catalog
# -> {"peer_id":"...","capabilities":["data_query","data_publish","pubsub"],
#     "rate_limits":{"max_record_bytes":10485760,"publish_per_minute":10,"query_per_minute":1000},
#     "schemas":[{"name":"TBS.fbs","record_count":1078755,"total_bytes":758000000}, ...]}

# 3. The anonymous record index (CIDs + a few indexed columns, no content).
#    The answer is ONE $QRP{KIND=Page} FlatBuffer frame
#    (application/vnd.sdn.flatbuffers.stream: [u32le length][FlatBuffer]):
curl -o page.qrp "http://localhost:7173/api/v1/data/index?schema=TBS&limit=3"
# -> TOTAL_COUNT 1078755, PAGE 1, LIMIT 3, CURSOR, NEXT_CURSOR, STALE false,
#    AS_OF_MS; COLUMNS [cid, norad, epoch_ms]; each ROWS[i].CELLS holds the
#    cid (Text) and norad and epoch_ms (Int, or Null), in store order. Read on
#    with &cursor=<NEXT_CURSOR> until NEXT_CURSOR comes back empty.
#    sdn-js: decodeQrpIndexPage(bytes).

# 4. Health check and the human-readable API reference (both anonymous):
curl -o health.qrp http://localhost:7173/api/v1/data/health
# -> one $QRP{KIND=Page} frame: COLUMNS component, status, time; one row

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
   - `GET /api/v1/data/index?schema=<CODE>&limit=&cursor=` — anonymous CID
     index, answered as one `$QRP` page frame in store order (at most 20,000
     rows a page; a larger `limit` is clamped). It pages as `/api/v1/data/query`
     does (*Querying records*, section 4).
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

Public: `OMM` (orbital element sets — the primary catalog), `CAT` (catalog
entries), `OCM` (orbit comprehensive messages), `MPE` (mean parameter ephemerides),
`SPW` (space weather indices), `WXF` (published weather forecast fields),
`NCD` (published native-container descriptors),
`RFB` (published emitter catalog), `LKS` (link status), `PNM` (publication
notifications — already broadcast on public pubsub topics), `DPM` (dataset
publication manifests — already pinned publicly), `EPM` (entity profile
messages — the identity records), `APP` (application package manifests),
`EGP` (entity groups — membership assertions over already-public catalog
records).

Every `OCM` source on a node is anonymously readable, including sources outside
the catalog. The standard-level classification applies to record sync
`read-chunk` and `open-manifest` and to SDS exchange record requests and queries.
These reads need no session or private-read authorization. Source filters select
records; they do not restrict public access to a particular provider. Published
manifests and shards also remain readable by content identifier. The list in
`sdn-server/internal/sds/public_read.go` defines the complete public set; unlisted
standards retain their existing read authorization requirements.

NOT public: `TBS` (telemetry bitstreams), `IQC` (integripulse quality check) —
both answer `401` (verified live):

```bash
curl -i "http://localhost:7173/api/v1/data/tbs/bulk?limit=1"
# -> HTTP/1.1 401 Unauthorized
#    one $QRP{KIND=Error} frame: ERROR_CODE unauthorized, HTTP_STATUS 401

curl -i "http://localhost:7173/api/v1/data/iqc/bulk?limit=1"
# -> HTTP/1.1 401 Unauthorized
#    one $QRP{KIND=Error} frame: ERROR_CODE unauthorized, HTTP_STATUS 401
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
  (`/api/v1/data/scan|query|records|...`), imports/exports, and
  `GET /api/accounts` (the merged account view).
- `ultimate` is reserved for the node's own identity. Operator rows: `unknown`
  through `admin`, never `never` (lockout) and never `ultimate`; both refused
  with `400 {"code":"invalid_trust_level"}`.
- Below the required tier: `403 {"code":"forbidden"}`.

Operator life cycle:

- Operator rows are created by: the node's `users:` config block, the
  dashboard's "Enroll a key" form (`POST /api/auth/users`, admin-only), the
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


### Scanning a lane

`POST /api/v1/data/scan` (admin, unless the operator lists it in
`admin.public_paths`) pages through every stored record of one standard,
optionally one lane. It is FlatBuffers only: the request body is one
size-prefixed `$QRP{KIND=Request}` frame with content type
`application/vnd.sdn.flatbuffers.stream`, and JSON is refused with `415`.

- Request fields: `SCHEMA_NAME` (required); `PROVIDER_ID`, `SOURCE_NAME`,
  `BATCH_ID`, `PRODUCER_PEER_ID`, `ORIGIN_ID` (the storing peer), `SEARCH`,
  `LIMIT` (at most 50,000) and `CURSOR` (optional).
- Response: runs of one `$QRP{KIND=Page}` header followed by exactly
  `RECORD_COUNT` record frames, the stored bytes verbatim. Each header names
  its lane (`PROVIDER_ID`, `SOURCE_NAME`, `BATCH_ID`, `PRODUCER_PEER_ID`,
  `ORIGIN_ID`) and repeats the page fields (`CURSOR`, `NEXT_CURSOR`,
  `TOTAL_COUNT`, `LIMIT`, `ETAG`, `SCHEMA_NAME`, `FILE_IDENTIFIER`). An empty
  page is one header with `RECORD_COUNT` 0.
- A record's CID comes from its bytes: CIDv1, raw codec, sha2-256
  (`bafkrei...`), the id `/api/v1/data/records/<schema>/<cid>` serves.
- Paging: send `CURSOR` = `NEXT_CURSOR` until it comes back empty. The cursor
  is opaque; `ETAG` identifies a page.
- Resume: the last page of a complete scan (`NEXT_CURSOR` empty) carries a
  `RESUME_CURSOR` on every run header. Send it back as `RESUME_CURSOR`, never
  with `CURSOR`, with the same query (schema, lane fields, search): the answer
  holds only the rows stored after that scan, byte-identical and in store
  order, paged by `NEXT_CURSOR`, and its last page carries a fresh
  `RESUME_CURSOR`. With nothing new it is one empty page and a fresh cursor.
  `TOTAL_COUNT` is 0 (unknown) on resumed pages. The cost follows the new rows,
  not the feed, and the cursor survives restarts. It is opaque and only the
  node issues it.
- Errors: one `$QRP{KIND=Error}` frame with `ERROR_CODE`, `MESSAGE` and
  `RETRY_AFTER_MS`. `STATUS` `Busy` or `Cold` means retry after that delay.
  `410` `resume_cursor_expired` (the store was rebuilt or compacted) means drop
  the cursor and scan again from the first page. `400`
  `resume_cursor_query_mismatch`, `invalid_resume_cursor` and `bad_request`
  (`CURSOR` with `RESUME_CURSOR`) refuse a resume as sent.

sdn-js: `encodeQrpScanRequest`, `decodeQrpScanStream`, `decodeQrpIndexPage` and
`decodeQrpError` (`sdn-js/src/ui/runtime/qrp-frames.ts`).

### Subscribing to a lane (importing from another node)

A node imports only the lanes it subscribes to (1.2.0). As an operator agent,
use the commands (they sign in with the node's root key and take `--json`):

```bash
spacedatanetwork sync subscribe --schema OMM --provider-id <provider> \
  --source-name <source> --from <producer peer id> --json
# -> {"plan":{"start":"current","rule":"replace-current","producer_batch":"...",
#     "first_read_records":2000,"first_read_bytes":640000,"free_disk_bytes":...,
#     "record_ceiling":32000000,"reserved_records":0,"reserved_bytes":0,"refusal":""},
#     "subscribed":true}
spacedatanetwork sync subscriptions --json
# -> {"subscriptions":[{"schema":"OMM.fbs","provider_id":"...","source_name":"...",
#     "producer":"16Uiu2...","subscribed_since":"...","connected":true,
#     "received_records":2500,"received_bytes":...,"current_batch":"b6",
#     "producer_batch":"b6","producer_batch_records":2500,"lag_records":0}]}
spacedatanetwork sync unsubscribe --schema OMM --provider-id <provider> \
  --source-name <source> --from <producer peer id>
```

A refused first read exits non-zero with `refusal` set (free disk or record
ceiling; `--force` subscribes anyway). Over HTTP (admin): `GET
/api/v1/sync/plan`, `POST /api/v1/sync` with a `$DSS` Subscribe naming
`PROVIDER_PEER_ID` (`?start=current|beginning`, `?force=1`; 202, or 409
`first_read_refused`; `?acknowledge_unread=1` on an existing subscription
clears its `unread`), `GET /api/v1/sync/subscriptions`. The anonymous `GET
/api/v1/sync` `$DSS` of a followed lane reads `SUBSCRIBED` and `SYNC_PROTOCOL`
`flatsql-sync read-chunk follow`. Details: `docs/SUBSCRIPTIONS.md`.


### Querying records

`POST /api/v1/data/query` (admin) selects records with conditions. The body is
one size-prefixed `$QRP{KIND=Request}` frame; JSON is refused with `415` and
`GET` with `405` (`method_not_allowed`).

- Fields: `SCHEMA_NAME`; the lane fields `PROVIDER_ID`, `SOURCE_NAME`,
  `BATCH_ID`, `PRODUCER_PEER_ID`, `ORIGIN_ID`; `SQL` (the sync-filter grammar);
  `SEARCH`; `FROM_EPOCH_MS` and `TO_EPOCH_MS`; `TIME_PROFILE` (`All`,
  `Nearest`, `AsOf`, `Latest`) with `PROFILE_EPOCH_MS`; `FILTERS`
  (`NORAD_CAT_ID`, `ENTITY_ID`, `MAX_DELTA_SECONDS`); `PAGE` (1-based);
  `LIMIT` (at most 20,000); `CURSOR`; `ARCHIVE_ID`.
- The answer has the scan's shape: runs of a `$QRP{KIND=Page}` lane header
  and its `RECORD_COUNT` records, verbatim.
- Order: store sequence ascending, the scan's order, so a batch comes back in
  the order it was stored. There is no other order: `SORT_FIELD` or
  `SORT_DESC` is refused with a `$QRP` Error `400`.
- The first page (no `CURSOR`) pins a snapshot at the lane's sequence head. It
  holds the snapshot's records `[(PAGE-1)×LIMIT, PAGE×LIMIT)`, and
  `TOTAL_COUNT` counts the snapshot.
- Every page header carries `CURSOR` and `NEXT_CURSOR`, both opaque and issued
  by the node. `NEXT_CURSOR` is empty on the snapshot's last page. Send it back
  as `CURSOR` with the same query: the next `LIMIT` records come from the same
  snapshot, `PAGE` is not used, and `TOTAL_COUNT` stays the same. A cursor
  walk returns every record of the snapshot exactly once, even while ingest or
  expiry runs. Records stored after the first page are not in it.
- `PAGE` without a cursor pins a fresh snapshot for each page, so
  `TOTAL_COUNT` can grow. Sequences only grow, so a `PAGE` walk is exact under
  append but not under expiry. Walk with the cursor for an exact read.
- Refused with a `$QRP` Error `400`: a malformed cursor, a cursor issued for
  another lane or query, and `CURSOR` on a `TIME_PROFILE` query or on a
  small-body (`SBD`) query with an `SQL` field filter. Those two keep their own
  order and page by `PAGE`: a time profile in the epoch index's profile order,
  a small-body filter in sequence order.
- The index (`GET /api/v1/data/index`) pages the same way, with the cursor as
  `&cursor=`. Its rows (`cid`, `norad`, `epoch_ms`) come in the query's order.
- Time profiles ride on the query; there is no separate epoch route. A time
  profile needs the schema's epoch profile: a schema without one answers `400`.

The node's status routes answer one `$QRP{KIND=Page}` each, with `COLUMNS` and
typed `ROWS` (cells `Text`, `Int`, `Float`, `Bool`, `Bytes` or `Null`):
`GET /api/v1/data/health`, `/api/v1/data/summary`, `/api/v1/data/datastores`,
`/api/v1/data/local-replica-stats` and `/api/v1/data/sources`. In `summary`,
the lane rows come first, then one total row per schema (lane fields empty),
then the store total (`schema_name` empty).

`POST /api/v1/query` (admin) runs SQL in the node's sandbox: a `$QRP` Request
with `SQL` and `PARAMETERS`, answered by one `COLUMNS` + `ROWS` page, with
blobs as `Bytes` cells.

sdn-js: `src/ui/runtime/qrp-request.ts` builds the requests and
`src/ui/runtime/qrp-frames.ts` decodes the answers.

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

HTTP status first, body shape second. On the data routes (`/api/v1/data/*`,
`/api/v1/query` and the rest of the data plane) a refusal is one
`$QRP{KIND=Error}` frame (`ERROR_CODE`, `MESSAGE`, `STATUS`, `RETRY_AFTER_MS`,
`HTTP_STATUS`), and the HTTP status matches it. Elsewhere the auth wall
answers `{"code","message"}`.

| Status | Body (real examples) | Meaning | What the client should do |
|---|---|---|---|
| 200 | `$QRP` index page; catalog rows; bulk stream; `{"status":"ok"}` | Success | Nothing |
| 201 | `{"cid":"...","schema":"...","stored_at":"...","bytes":N}` | Record stored | Trust the CID; use it for reference |
| 304 | empty | `If-None-Match` hit on a conditional read | Use your cached copy |
| 503 | `$QRP` Error frame: `STATUS` Busy, `ERROR_CODE` `STORE_BUSY`, `RETRY_AFTER_MS` | The store is busy (index, scan) | Retry after `RETRY_AFTER_MS` |
| 400 | `{"code":"invalid_timestamp","message":"timestamp outside allowable skew"}` | Bad request: skew, malformed body, route/header schema mismatch, invalid trust level, unknown operator on config-managed row | Fix the request; do not retry unchanged |
| 401 | `$QRP` Error `unauthorized` on data routes; `{"code":"unauthorized","message":"not authenticated"}` elsewhere | No or expired session; OR standard not on the anonymous allow-list (bulk reads) | TBS/IQC bulk: give up, permanent by design. Otherwise: sign in (section 3) |
| 400 | `$QRP` Error `resume_cursor_query_mismatch`, `invalid_resume_cursor`, `bad_request` | A scan resume refused as sent | Resend the same query with the node's own `RESUME_CURSOR`; never build one |
| 410 | `$QRP` Error `resume_cursor_expired` | The store was rebuilt or compacted since the cursor was issued | Drop the cursor; scan again from the first page |
| 415 | `$QRP` Error `unsupported_media_type` | A JSON body on a FlatBuffer route | Send one size-prefixed `$QRP` Request |
| 403 | `{"code":"forbidden"}`; `data publishing is disabled on this node`; `schema not allowed...`; `access denied by policy...`; `CSRF validation failed (...)` | Below required trust tier, or a policy/CSRF refusal | Elevate trust or change lanes. CSRF: by design for cross-origin browsers — use the documented request signing scheme or a server-side caller |
| 404 | `$QRP` Error `not_found` on data routes (a record miss); `{"error":{"message":...}}` elsewhere | No such route / record / flow param | Check the path; never retry blindly |
| 413 | `request body too large` | Over max record bytes (10 MiB default) | Shrink or split the payload |
| 429 | `{"code":"too_many_requests",...}` | Challenge/verify or publish rate limit | Back off; respect `Retry-After` if present |
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
   - Unstamped entries are planned routes not mounted yet.
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
- `/api/v1/standards`, `/api/v1/peers/{peerId}/pnm`,
  `/api/v1/peers/{peerId}/{standard}/latest`: planned gateway routes served
  only when the corresponding flow bundle is mounted on a node. Unstamped in
  the OpenAPI spec today; probe before relying on them.
- The `peers-discovery` endpoints (described in the API reference) are served
  by optional flow mounts; may not exist on every node — probe first.

---

## 8. Reference — what requires a session

Everything under `/api/**` not listed in section 2 requires a session.
`admin`-gated examples: `/api/v1/data/scan`,
`/api/v1/data/query`, `/api/v1/query`, `/api/v1/data/records/<schema>/<cid>`, `/api/accounts`,
`/api/peers/*` — including `/api/peers/sdn`, `/api/peers/graph` and
`/api/peers/graph/schema`, all of which answer `401` without a session,
`/api/auth/users`, `/api/v1/conjunction/screen`,
`/api/import`, `/api/export`, `/api/v1/modules/runtime/`.

The same classification logic — a single predicate — drives the auth wall,
CORS, and the OpenAPI generator's anonymity stamps.

---

## 9. Administering a node's updates

This section is enough to keep a node current. Run the commands on the node's
machine, as the account that runs the node, with the node's config
(`-c <config>` when it is not the default). Every line of output is
`key=value`; read the keys, not the wording.

### What the node runs, and whether an update is waiting

```bash
curl -s http://127.0.0.1:7173/api/v1/id
# -> {..., "bundle_version":"1.1.8", "build_sha256":"<sha256 of the running executable>"}
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:7173/api/v1/ready
# -> 200 when the node serves; 503 while it starts or warms its data services
spacedatanetwork update check
# version=<this build>  channel=release  update_feed_base_url=https://sdn.spaceaware.io/updates
# update_signal_enabled=true  update_health_timeout=10m0s  update_rollout_window=10m0s  rollback_slots=<n>/5
# update_pending=none | update_pending=<update id> version=... sequence=... trigger=signal|feed-check
#   accepted_at=<t> due_at=<t> window_seconds=<w> window_from=config|signal delay_seconds=<d>
# update_failed=<update id> version=... sequence=... at=<t> rolled_back=true|false   (only while it applies)
# provider_update=<update id> status=available version=<newer> ...   (a newer release exists)
# provider_update_check=error error="..."                             (none newer, or the feed is unreachable)
# updates_available=true|false
spacedatanetwork update slots
# current update_id=... version=...;  slot[0] ... (default rollback target)
```

`bundle_version` is the version the node runs. `update_signal_enabled=true` (the
config's `update.enabled`, on by default) means the node installs a release on
its own: when a signal signed by an update root announces it, and at a feed
check every six hours (`update.check_interval_seconds`). Nodes from 1.1.4 to
1.1.6 learn of a release only from a signal; 1.1.7 and later also check the
feed.

`update_pending=` names an update the node accepted and waits to act on: it
acts at `due_at`, its own time within the rollout window
(`update.rollout_window_seconds`, default 1800; `window_from=signal` when the
signal carried a signed window). Waiting is normal; to install now, run
`spacedatanetwork update install`. `spacedatanetwork status` prints the same
`update_pending=` and `update_failed=` lines.

### Every node's version and update state

```bash
spacedatanetwork update fleet
# NAME         PEER         VERSION  SOURCE     LAST SEEN  UPDATE                                    SIGNAL
# (this node)  ...k4kVmQx1  1.2.0    this node  now        current                                   yes
# host-b       ...9TnPzRa2  1.2.0    status     now        pending 1.2.1 until 2026-12-01T10:41:07Z  yes
# -            ...Lw2bYe7c  1.1.9    identify   3m0s ago   not reporting                             -
# latest_signal=<update id> sequence=<n> (the newest signal this node verified)
spacedatanetwork update fleet --json      # the same, as GET /api/peers/versions returns it
```

`GET /api/peers/versions` (admin session) returns `{"generated_at", "latest_signal":
{"update_id","sequence"}, "rows": [...]}`; each row has `peer_id`, `self`,
`name`, `source` (`local`, `status` or `identify`), `version`,
`agent_version`, `online`, `last_seen`, `state` (`current`, `pending`,
`failed`, `not reporting`), `pending` (`update_id`, `version`, `sequence`,
`due_at`), `failed` (`update_id`, `version`, `sequence`, `at`,
`reason_code`, `rolled_back`), `sequence`, `acknowledged`,
`acknowledged_latest` (true when the node accepted the newest signal this
node verified itself, or runs its sequence or a later one) and `status_at`.
`latest_signal` is that newest verified signal; no other node's claim moves
it. The state is what the node itself published (a signed status message on
its update topic, `source: status`), not checked by this node; `not
reporting` means no status from it is held: a node of 1.1.9 or earlier, a
node with `update.publish_status: false`, a node this node has not
identified itself as an SDN node (reached only through other nodes), or a
status not renewed for 30 minutes. `failed` with `rolled_back: true` is the
node saying an update failed its health gate or trial and it went back to the
previous build; with `rolled_back: false` the failed build keeps running (its
revert was refused) and an operator decides. `reason_code` is one of
`not-ready-by-deadline`, `stopped-during-trial`, `trial-record-unreadable`,
`health-failed`, `revert-refused`; the failure's text stays in the failing
node's ledger. Strings in the response come from other nodes and the peer
directory: print them as data (`update fleet` removes control and format
characters).

### Install an update

Where the update goes decides the output (see "Where updates go" below).

Into the account's home (the install folder is not writable by the node's
account):

```bash
spacedatanetwork update install
# installed_update=<update id>  version=<new>  sequence=<n>  update_root=<home bundle>
# daemon_restart=requested pid=<pid>      (a daemon is running: it restarts itself for the update)
# daemon_health=healthy
# daemon_identity=verified bundle_version=<new> build_sha256=<sha256>
```

With no daemon running it ends with
`next=start the node: it runs this build as a trial and keeps it once it answers ready`.
No helper runs: the daemon restarts in its own process (its pid does not
change), and the node's log shows `runs as a trial` and then
`passed its trial`.

In place (the install folder is writable):

```bash
spacedatanetwork update install
# staged_update=<update id>
# helper_started=<path>  helper_pid=<pid>
# next=the helper will apply the update after this command exits
```

The helper keeps writing to the same output after the command returns. Success
is, in order: `prepared_update=`, `daemon_shutdown=requested`,
`applied_update=<update id>`, `version=`, `restart=...`,
`daemon_health=healthy`, `daemon_identity=verified bundle_version=<new> ...`.
With no daemon running, `update install` installs directly and prints
`installed_update=` and `next=restart the SDN daemon to run the new version`.
`--config <config>` is fine while the node runs when it names the file the
daemon was started with (an absolute path on its command line).
A refusal before anything is installed is an error naming the reason
(`untrusted update signing key`, `update sequence rejected`, a store-format
refusal, ...): the node keeps running what it ran.

A failed health gate (either way) prints `daemon_health=unhealthy ...`, then
`rollback=applied restored_version=<previous>` and ends with the error
`daemon health failed after update; rolled back to <previous>`. The node then
runs the previous build, and the failed update is kept aside under the update
root's `updates/failed/<update id>/` and never installed again by itself.

### An update was received but not installed

- `curl -s http://127.0.0.1:7173/api/v1/health` reports
  `"status":"degraded"` with an `update_failed` alert naming the update and the
  reason; `spacedatanetwork status` shows `data_health=degraded` and the alert
  counts. When the helper never took over, or stopped the node and failed
  before the swap, the node keeps (or goes back to) the build it ran and
  retries the same update after `update.min_interval_seconds`, doubling up to
  24 hours.
- The node's log has `Update signal accepted: ...` for a signal it acted on,
  and the reason for anything it refused or could not install.
- The deploy ledger, `deploy-ledger.jsonl` at the update root, has one line
  per step (`apply`, `restart`, `health-passed`, `health-failed`,
  `health-reverted`, `rollback`).
- In a home bundle, `updates/trial.json` exists while a build is installed and
  not yet proven: its `starts` counts trial starts, `interruptions` the
  signal stops during the trial, and `fail_reason` says why a trial gave up. It disappears when the trial passes or is rolled back.

### Where updates go

The update root is the install folder when the node's account can write it.
When it cannot (a bundle under `/opt` owned by root, run by a system service as
an ordinary account), updates go to `~/.spacedatanetwork/bundle/<key>` under
the home folder of the account's passwd entry (one folder per install), and
the install folder and the service unit are never changed. With no usable
home folder (none in the passwd entry, a missing one, or one the account
cannot enter, as under the shipped systemd unit's `ProtectHome=true`) they go
to `<storage.path>/update-bundle/<key>`, and the log says so: a service
account under the shipped unit updates itself into its storage folder. A home
folder that exists and another account can change is refused, never replaced
by the storage folder. No root and no
service account are needed, and updates by signal and by feed check work under
a system service. Every daemon start through the installed path verifies the
build there in full against the installed build's update roots; other commands
reuse that verification while no file of the build has changed. A build that
does not verify, or one another account can change (a folder from the bundle
up to the home folder not owned by the account or writable by its group or
others; a folder or symlink on the resolved path from `/` owned by another
account, or a folder others can write without the sticky bit; a folder or
file inside the build writable by its group or others), is refused, with a
line on stderr:
`update: the build under <folder> is refused: <why>; running the installed build`.
A new build runs as a trial first, with a deadline of
`update.health_timeout_seconds` from its start (default 600 s); one that is not
ready by then, or crashes, is rolled back and kept aside, and the stderr line
reads `update: update <id> failed its trial (<why>); rolled back to <version> and quarantined`.
A trial process stuck so badly that it cannot stop itself keeps running until
it is stopped; the next daemon start rolls it back (a command typed after the
deadline rolls it back only when no daemon runs, and otherwise runs the
installed build with
`update: update <id> did not pass its trial, and the daemon running it is still alive: ...`).
A stop by signal during a trial (a service restart, a reboot) runs the trial
again at the next start with a new deadline, up to three times; the fourth
such stop fails it. `... could NOT be rolled back ...` means the store-format guard refused
the rollback: the failed build keeps running, `update_failed` is raised, and an
operator decides. Root never runs a home build, and a second daemon from the
same install and account is refused. Another build installed in the install
folder by an administrator wins over the home one. To return to the installed
build, stop the node and remove `~/.spacedatanetwork/bundle/<key>` (the folder
`update install` prints as `update_root=` and the log names), or run `spacedatanetwork update rollback
--slot <installed version> --reason "<why>"` while the installed build is still
one of the five slots. Windows needs a writable install folder. A read-only
install of a release older than the one with home updates needs one manual
upgrade.

### Return to an earlier build by hand

```bash
spacedatanetwork stop        # or stop the service
spacedatanetwork update rollback --reason "<why>"            # the previous build
spacedatanetwork update rollback --slot <update id> --reason "<why>"
# restored_update=...  version=...  next=restart the SDN daemon to run the restored version
spacedatanetwork start
```

### Never

- Never edit, copy over or delete files of an installed bundle or of a home
  bundle to change a build: a changed home build is refused (a build on trial
  is rolled back), and a changed installed bundle makes the node set its home
  builds aside. Never edit `updates/trial.json`, `updates/verdict.json` or
  `updates/state.json`.
- Never turn verification off or point `SDN_UPDATE_TRUST_ROOTS` at a key you
  hold for a node you do not administer, and never add a root beside the ones
  a release shipped.
- Never install a build onto nodes that exchange publications unless they all
  run 1.1.6 or later: older nodes refuse publications of newer ones.
- Update 1.1.0 or earlier by hand (stop, install the new release in place,
  keeping `keys/` and the config, start): their updater reverts the updates it
  drives. Desktop, `.deb`/`.rpm`, VM and container installs do not update
  themselves: install the new release.

### Checks after an update

1. `GET /api/v1/ready` answers 200.
2. `GET /api/v1/id` reports the new `bundle_version`.
3. `GET /api/v1/health` reports `"status":"ok"` and no `update_failed` alert.
4. `GET /api/v1/catalog`: every schema's `record_count` is at least what it
   was before the update.

### Publishing a release to a fleet

Two steps, in this order: publish the signed feed (the release pipeline
builds it), then announce the release from the publishing node with the
signals the pipeline signed:

```bash
SDN_UPDATE_FEED_DIR=<published feed folder> \
  spacedatanetwork update signal --from-feed --channel release --version <X>
```

Publishing the feed without the signal is not a push: nodes find the release
only at their next feed check (1.1.7 and later), and nodes from 1.1.4 to 1.1.6
not at all.
