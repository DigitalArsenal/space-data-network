# Serving contract

This file freezes the surface that clients (the dashboard, the console, the
SDK, fleet harnesses) are written against. A change to anything listed here
requires a change to this file in the same commit, a new `serving_api` entry
or a renamed one, and a stack pin bump. Anything not listed here is internal
and may change without notice.

Frozen at the space-data-network commit that carries this file on `main`
(`git log -1 --format=%H -- docs/SERVING-CONTRACT.md`); nodes report the
matching executable as `build_sha256`, and a fleet must show one value.

## Identity a node reports

`GET /api/node/info` (anonymous, JSON). Fields clients may rely on:

| Field | Meaning |
| --- | --- |
| `peer_id` | libp2p identity of the node |
| `version`, `agent_version`, `suite_version`, `standards_version` | build and SDS versions |
| `build_sha256` | SHA-256 of the running executable; a harness refuses a mixed fleet by comparing it |
| `serving_api` | the list below; a client checks membership before calling a route |
| `record_form` | `"bare"`: every record a stream hands out is a finished FlatBuffer with its file identifier at byte 4, never size-prefixed |
| `listen_addresses`, `multiformat_address` | dial addresses |
| `reachability` | `verdict` (`direct` / `relayed` / `unconfirmed` / `unreachable`), `autonat`, `nat.mappings`, `nat.unmapped_listeners`, `nat.rebuilds`, `ipfs.reachability` |
| `tor_alive` | present only when a managed Tor process is configured |

`serving_api` values and the routes they name:

| Value | Route | Shape |
| --- | --- | --- |
| `catalog` | `GET /api/v1/catalog` | per-schema `record_count`, `total_bytes`, `index_state`; `catalog_cached`, `catalog_age_seconds`; served from a 30 s single-flight cache |
| `catalog.index_state` | same route | `index_state` per schema is `building` or `ready`; a client does not search a schema whose index is `building` |
| `data.query` | `POST /api/v1/data/query` | JSON body with `schema`, `offset`, `limit`, optional `provider_id`, `source_name`, `batch_id`, `producer_peer_id`, `cursor`, `search`, `query_profile`, `include_data`; `Accept: application/vnd.sdn.flatbuffers.stream` returns u32 size-prefixed frames of bare records with `x-sdn-total-count` |
| `data.search` | `POST /api/v1/search/data` | full-text over the canonical record; returns the same frame form |
| `data.remote` | `POST /api/v1/data/remote/{peerID}/{op}` | `op` = `read-chunk` (LIMIT 1..1000), `list-published` (LIMIT 1..1000, CURSOR offset ≤ 16000) or `read-shard` (CID, CURSOR byte offset, MAX_BYTES 1..4 MiB); body = one `$QRP` Request frame (`application/vnd.sdn.flatbuffers.stream`, ≤ 64 KiB); answer = the peer's flatsql-sync 2.0.0 reply verbatim; errors are one `$QRP` Error frame; 20 s deadline, 8 MiB cap, HTTP 502 when the remote node does not answer in time |
| `publish.batch` | `POST /api/v1/data/publish/batch/{schema}` (admin alias `/api/v1/admin/publish/batch`) | u32 LE length-framed bodies; a size-prefixed FlatBuffer (identifier at byte 8) is normalized to the bare record before its CID is computed; the response names the envelope form that arrived |

## Records

- Stored and served form: bare canonical SDS FlatBuffer, identifier at byte 4.
- Content ID: sha256 of exactly those bytes (raw CIDv1, sha2-256). Clients verify frames against `results[i].cid`.
- Source tags (`provider_id`, `source_name`, `batch_id`, `producer_peer_id`) are metadata, never bytes inside the record.

## Node-to-node

flatsql-sync 2.0.0: one libp2p protocol per op,
`/space-data-network/flatsql-sync/2.0.0/<op>`. 1.0.0 is not served (a dial
fails negotiation). Request = one size-prefixed `$QRP` Request frame (u32 LE
length). Every reply starts with a `$QRP` Page or Error frame.

| op | request | reply |
| --- | --- | --- |
| `read-chunk` | SCHEMA_NAME, lane, SEARCH, CURSOR / RESUME_CURSOR, LIMIT | same as `POST /api/v1/data/scan`: runs of [Page][RECORD_COUNT record frames] |
| `open-manifest` | SCHEMA_NAME, lane, LIMIT | Page: TOTAL_COUNT, ETAG = manifest id, CURSOR = snapshot, SEGMENT rows (ROLE `segment` / `shard-group-car`) |
| `list-published` | SCHEMA_NAME, lane, CURSOR = offset, LIMIT | Page: TOTAL_COUNT, CURSOR, NEXT_CURSOR, SEGMENT rows |
| `read-shard`, `read-index` | SCHEMA_NAME, lane, CID, CURSOR = byte offset, MAX_BYTES = length (0 = rest) | Page with one SEGMENT row (BYTE_OFFSET, BYTE_LENGTH, BYTE_COUNT = file size), then BYTE_LENGTH bytes |
| `read-shard-batch` | SCHEMA_NAME, lane, FILTERS SHARD_CID In `a,b,c` | Page with one row per shard, then the shards concatenated |
| `wire-speed-probe` | MAX_BYTES | Page MAX_BYTES = payload, then that many bytes (private-read peers) |
| `list-datastores` | — | Page rows DATASTORE_KEY, SCHEMA_NAME, PROVIDER_ID, SOURCE_NAME, BATCH_ID, QUERY_PROFILE, PRODUCER_PEER_ID, PRODUCER_PUBLIC_KEY, UPDATED_AT_MS (private-read peers) |

Lane = PROVIDER_ID, SOURCE_NAME, BATCH_ID (+ PRODUCER_PEER_ID, ORIGIN_ID for
`read-chunk`). FILTERS: QUERY_PROFILE Eq, DATASTORE_KEY Eq (private-read
peers). SEGMENT rows use one column set (`syncwire.SegmentColumns`), read by
column name; empty strings and bytes are Null cells. A `source_name`-only
filter is indexed (`(schema_name, source_name, cid)`).

Feed heads: topic `/space-data-network/feed-heads/2.0.0/<SCHEMA>.fbs`, message =
one `$QRP` Page with one SEGMENT row; SIGNATURE (Bytes) = Ed25519 over
`"SDN-FEED-HEAD\0"` + that frame rebuilt with SIGNATURE and SIGNATURE_TYPE Null;
SIGNATURE_TYPE `Ed25519`; SIGNING_PUBLIC_KEY (Bytes, 32).

## Maintenance (admin session or loopback dev auto-admin)

- `POST /api/v1/admin/store/hydrate[?force=true]`
- `POST /api/v1/admin/store/supersede[?dry_run=true]` with `{schema, provider_id, source_name, keep_batch}`; retires every other batch of that source and reports what it retired.

## Dashboard authentication

`/api/auth/challenge`, `/api/auth/verify`, `/api/auth/status`, `/api/auth/me`,
`/api/auth/users`, `/api/auth/logout`, `/api/auth/epm`, `/api/auth/attest`.
Signed-in sessions are wallet fingerprints; node admission is a status, never a
gate on the account view.

## Out of scope

`/api/v1/openapi.json` currently documents 25 of the routes above; it is not
the freeze. Everything under `/api/v1/` not named in this file (trust, storefront,
modules, pubsub, channels, conjunction, demo, stats) is internal to the node and
its own programs and may change.
