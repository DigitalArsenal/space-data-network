# Server Overview — A Space Data Network Node

Companions: [Operator Enrollment](./OPERATOR-ENROLLMENT.md) (becoming an operator or admin — key generation, trust levels, exact HTTP calls); [API Guide](./AGENT-API-GUIDE.md) (the full REST surface, authentication, error semantics, anonymous data plane); `README.md` (getting started); [TECH-PATH.md](./TECH-PATH.md); `sdn-server/docs/gateway-api.md`. On any running node, the served API reference is at `http://<node>/api/v1/docs`.

> In the dashboard: [Node](#/node) · [Identity](#/identity) · [Peers](#/peers) ·
> [Store](#/store) · [Accounts](#/accounts) — these links open the page beside
> this guide when it is read inside the dashboard's documentation panel.

## 1. What a node is

Run the binary and it describes itself:

```
$ spacedatanetwork --help
spacedatanetwork is a specialized fork of IPFS tailored for the Space Data Network.
It replaces generic content-addressed storage with FlatBuffer-native data handling
and SQLite-based structured storage, optimized for space data standards.
```

Two qualifications to that self-description:

- "Fork of IPFS": the server is a libp2p Go application (streams, DHT, pubsub, bitswap-style sync all over libp2p), and the repo carries an in-tree fork of Kubo, the IPFS reference implementation, hosting the node console and the IPFS side of publishing. Both derive their identity from the same node key.
- "SQLite-based structured storage" is legacy wording. The structured storage engine is **FlatSQL**, a SQL engine that runs as a WASM module and reads FlatBuffer records directly — no embedded SQLite in the stack. Details in [Section 3](#3-storage-flatsql-append-only-engine-routed).

A node is one long-running daemon per machine, reachable over libp2p (including through NAT via relay and hole punching), storing FlatBuffer records in append-only streams, serving them over HTTP and pubsub, and running WASM modules and flows for everything above socket plumbing.

Operational shape:

- One daemon per box. `daemon --help` exposes an `--allow-multi-daemon` flag marked "DEVELOPMENT ONLY"; production is a single instance managed as a background service (`init`, `start`, `status`, `stop`, `restart`).
- The CLI talks to the daemon over its local HTTP API (`status` prints `admin_url=http://127.0.0.1:7173/` by default; `open` prints the UI URL).
- Two topology tiers: full nodes on public IPs (DHT, relay, pin, publish) and light peers — including browsers — behind NAT. A browser running the SDN library is a peer, not a client: with the same mnemonic as a server node, it has the same cryptographic identity.
- Fleet software updates are pushed by signed update signals over an update feed; a node fetches, verifies, upgrades itself in place, and can roll back.

## 2. One format: SDS FlatBuffers end to end

The unit of data everywhere is a [Space Data Standards](https://spacedatastandards.org) FlatBuffer record, size-prefixed, in wire form. The same bytes that travel over pubsub are appended to disk, indexed in place, queried without deserialization, and served back out:

> network → disk → query → network is one format.

The node embeds 228 `.fbs` schema files (all the ratified Space Data Standards plus a few SDN-internal record schemas such as the publication log and ping records) and validates every record it stores against the matching schema. Two consequences:

- **Zero-copy reads.** Query results are aligned, size-prefixed FlatBuffer streams; a reader walks fields out of the wire bytes directly.
- **Keys match the schema exactly.** As JSON, field names match the IDL capitalization exactly (`NORAD_CAT_ID`, not `norad_cat_id`). Never re-case them at the edges.

Standards are neutral contracts: descriptions name capability classes, never vendors or organizations. Provenance — which archive, which measurement program — is carried as record data at runtime, not baked into the standard.

## 3. Storage: FlatSQL, append-only, engine-routed

Storage is **FlatSQL**: a SQL engine compiled to WASM, hosted in-process by the Go server (via the WasmEdge runtime), running the identical artifact the browser runs.

- **Durability on disk.** Tables and B-tree indexes are zero-copy indexes **over the record streams**: they store offsets into the FlatBuffer stream files, never copies of payload bytes. The engine writes files only through seven explicit, offset-addressed host imports, so an engine that cannot open real disk storage fails loudly rather than silently running in memory.
- **Append-only streams, journal as source of truth.** Records land in append-only stream files; nothing is rewritten in place. Boot is open + verify + tail over the record journal — the journal is the source of truth; the persisted index is a recovery accelerator that can cost time but never data.
- **Engine-routed by default.** Every embedded standard routes through the engine: each standard's records mirror into a per-source shadow table (`OMM@celestrak-gp`, for example) as stored. There are no per-standard hard-coded query paths; a query reads the standard's table. The canonical query shape for the latest records of a type is:

  ```sql
  SELECT _data FROM <TYPE> ORDER BY _rowid DESC LIMIT ?
  ```

  Two categories of standards are deliberately not routed: those whose IDL declares no file identifier, and those whose IDL declares an `(encrypted)` field. The latter are **sealed at rest** — the stream holds an encrypted envelope — so they are never mirrored into the public query surface, and their table names do not even resolve in SQL.

- **Per-record content addressing.** Records are content-addressed by SHA-256: the same FlatBuffer bytes always produce the same CID, so deduplication and network sync can trust byte equality.

### The anonymous read surface is a schema property

What "anyone can read" is decided per **schema**, not per URL path, and it is fail-closed: a schema is anonymously readable only if deliberately on the allowlist. Today that is the public catalog and its provenance lane:

`OMM` (orbital element sets), `CAT` (catalog entries), `MPE` (mean parameter ephemerides), `SPW` (space weather indices), `RFB` (RF band specifications / emitter catalog), `LKS` (link status), `PNM` (publication notifications), `DPM` (dataset publication manifests), `EPM` (entity profile messages / identity records), `APP` (application package manifests), and `EGP` (entity groups).

Everything else — including `TBS` and `IQC` — is anonymous-read CLOSED by default until deliberately opened. Anonymous read never implies anonymous publish.

## 4. Identity and signatures

### One HD key tree

A node's entire cryptography descends from one BIP-39 mnemonic, turned into seed via SLIP-10 with the standard BIP-44 path structure (coin type 0):

```text
BIP-39 Mnemonic → seed → SLIP-10 Master Key
    ├── m/44'/0'/0'/0'/0'  →  Ed25519 Signing Key (also the libp2p PeerID)
    └── m/44'/0'/0'/1'/0'  →  X25519 Encryption Key
```

The signed identity is also the network identity: the Ed25519 signing key **is** the libp2p PeerID. Purpose-separated child keys follow the grammar `m/44'/0'/<account>'/<purpose>'/0'`, fully hardened at every level, so publishing a child's public half reveals nothing about the parent or its siblings. The purposes in use:

- **Identity signing** (purpose 0) — signs sign-in challenges, identity and publication records, dataset publications, module publications, and update manifests. Whatever this key signs, every node in the fleet will install and run.
- **Encryption** (purpose 1) — key agreement for encrypted records and module bundles; signs nothing.
- **Licensing grants** (purpose 2) — signs module-delivery and storefront access grants; clients verify grants against its public half.

At rest, the mnemonic is encrypted with a machine-derived key and fails closed if the machine changes. The managed IPFS repo identity is derived from the same root, never a separate key.

### What is published vs kept private

The node publishes an identity record (EPM) with human and machine faces: a contact card with name, organization, and photo, plus the literal public keys that verify it — email aliases ride the vCard/QR forms as machine-read identifiers. The xpub and the derivation paths are private. Identity records are self-signed: the record's signatures must verify against keys carried in the record itself, and the card's primary signing key must be exactly the key the contact role names.

### Domain-separated signatures

Any endpoint that can produce a signature over content the node did not author is a potential oracle for forging another protocol's signature — sign a raw SHA-256 of one thing and you may have signed something else. So every detached signature the node produces is domain-separated:

```text
statement = domain-label || 0x00 || SHA-256(content)
```

Domains come from a closed registry (`SDN-MODULE-PUBLICATION-V1`, `SDN-UPDATE-MANIFEST-V1`, `SDN-UPDATE-SIGNAL-V1`, dataset publication bindings, and others). A signature minted for one statement kind can never be replayed as another; adding a new signed statement kind is a reviewed change, never a request parameter.

### First login binds the key

A wallet first signs in with its Ed25519 key; that key is then bound to the identity (first use wins). A conflicting key presented later is refused. Possession of key material is thus proof of identity, in both directions: the node's own key is always admitted as an administrator.

## 5. Peers and operators are one kind of thing

From the CLI help, verbatim: "a node running somewhere and an account that logs in are the same kind of thing: a PRR-shaped identity carrying a trust level."

`accounts` is the command you use for both:

```
$ spacedatanetwork accounts --help
List and manage accounts (network peers and login operators are one thing)
```

- `accounts add` adds a **peer** by `--peer-id`, `--public-key`, or `--vcard`. It refuses an xpub on purpose: an xpub identifies a wallet account, not a libp2p host.
- `accounts list` shows the merged table: peer rows and operator rows, unified by the key that both derive (the secp256k1 account public key from an operator's xpub maps to a libp2p peer ID, which is what merges a peer row and an operator row for the same entity).
- `accounts trust` changes trust explicitly: `--peer-id` targets the peer; `--xpub` targets the operator. It only updates an existing row; it cannot create one.

### Trust levels

Trust levels follow the PGP ownertrust scale, persisted as a signed integer:

| Level | Value | Meaning |
|-------|-------|---------|
| `never` | -1 | Deliberate distrust: a hard veto that no web-of-trust computation can override |
| `unknown` / `untrusted` | 0 | No assertion made; the fail-closed default |
| `marginal` / `limited` | 1 | Weakest positive assertion |
| `standard` | 2 | Between marginal and full; the default for ordinary peer access |
| `full` / `trusted` | 3 | Full confidence |
| `admin` | 4 | Operational super-user: operator sessions, admin API routes |
| `ultimate` | 5 | Reserved exclusively for the node's own identity; never granted to a remote peer or session |

Assignable to operators up to `admin`; `ultimate` is the node's own key's level and `never` is not supported as an operator lockout. For peers, the full range applies. The node also computes a web-of-trust validity from the peer graph, which feeds effective trust for access decisions.

Web-of-trust detail: the peer graph records each peer's role (standard, bootstrap, relay, gateway, seed); connection admission is trust-aware; fleet connections (known bootstraps, configured trusted peers, pins) are protected from trimming.

### How operator rows come to exist

Four ways, and it matters which one you use:

1. **Configuration**: a `users:` block in `config.yaml` (xpub, trust level, optional name and signing public key). Config entries win over database rows on every read, cannot have their trust changed through the API, and cannot be removed through the API.
2. **The dashboard**: the ACCOUNTS view's "Enrol a key" form (xpub, name, trust, signing public key in hex), which creates the row through the admin API.
3. **First-admin bootstrap**: if no admin exists anywhere, the next wallet that signs in is minted as the initial admin. The node therefore cannot be permanently locked out at the operator level by deleting the last admin row.
4. **The root ceremony**: the node's own key is always admitted as an admin, with or without a stored row.

### Sign-in

No passwords. Sign-in is an Ed25519 challenge-response: `POST /api/auth/challenge` returns a random challenge; the wallet signs it; `POST /api/auth/verify` admits the session and sets an HttpOnly session cookie. The CLI authenticates the same way using the node's own root key, or accepts an existing session token via `--session-token` or `$SDN_SESSION_TOKEN`.

The full enrollment story — key generation, sign-in requirements, withdrawal rules — is in [Operator Enrollment](./OPERATOR-ENROLLMENT.md).

## 6. How data moves: libp2p, pubsub, IPFS

### The network layer

Every node runs a libp2p host: TLS and noise for transport security, an autonomous system-style connection manager with trust-aware gating, resource management, NAT port mapping, hole punching, and client relay with automatic relay peer discovery. Full-public nodes participate in the DHT and can serve relay for light peers (relay is a config-gated, donated-bandwidth service). When serving TLS directly, the node advertises `libp2p.direct` addresses that browsers can dial.

Stream protocols:

- `/spacedatanetwork/sds-exchange/1.0.0` — record exchange
- `/space-data-network/id-exchange/1.0.0` — identity records
- `/space-data-network/chat/1.0.0`
- `/space-data-network/flatsql-sync/1.0.0` — published shard sync (the high-throughput data lane)

### Pubsub lanes

- **Per-schema data topics** — `/spacedatanetwork/sds/<SCHEMA>` — the streaming record lane: one topic per standard, joined for every validated schema. Publication notifications ride the PNM standard's own data topic, `/spacedatanetwork/sds/PNM.fbs`.
- **Feed heads** — `/space-data-network/feed-heads/1.0.0/<schema>` — small, signed, mutable pointer messages. A feed head announces where a standard's latest immutable materialized set lives (record count, byte count, shard / query-index / manifest CIDs, publication CID) and is what replicas subscribe to *before* fetching the immutable shards.
- **Adjuncts** — `/spacedatanetwork/edge-relays` (relay announcements), storefront topics `/sdn/storefront/{listings,purchases,reviews}`, per-buyer delivery topics `/sdn/data/{listing_id}/{buyer_peer_id}`, and channel discovery topics.

### The publication lifecycle (dataset lane)

Providers publish materialized datasets, and the whole lifecycle is signed end to end:

1. A provider ingests source data and normalizes it into SDS FlatBuffers.
2. Records are stored locally and mirrored into the engine's tables.
3. The provider exports the authoritative set as a `DATA_SHARD` (the streams) plus a `QUERY_INDEX` asset.
4. It builds and signs a dataset publication manifest (**DPM**) binding the file identifier, source and byte hashes, schema hash, and the assets' content addresses, and pins it to IPFS.
5. It signs and announces a publication notification (**PNM**) on the open pubsub topics.
6. Consumers resolve the latest PNM by peer + standard, verify the signature against the publisher's identity, fetch the manifest and assets by content address over libp2p, verify hashes, roots, and signatures, and import the shard into their own store.

Content addressing is what makes this safe without a trusted third party: bytes are their own addresses, and the signature chain binds names to bytes. Published-shard downloads run at near wire speed (the documented gate is 2 Gbit/s sustained, 99% of wire speed). Over HTTP, the same records are served against the per-schema anonymous allowlist of Section 3.

## 7. Everything compute is WASM; the host only connects

**All functionality is WASM.** The Go host and the browser JavaScript host are thin, application-blind connector layers exposing the same small set of generic hooks (network, storage, file I/O, signing). A module compiled once runs identically under WasmEdge on a node and in a browser tab, threaded through the wasi-threads contract. Divergence between the two runtimes is treated as a defect, caught by running identical scenarios against both artifacts.

In practice:

- **The SQL engine is itself a WASM module** hosted in-process by the Go server, using the same FlatSQL artifact the browser uses (byte-parity between the artifacts is enforced by shared test vectors). The server-side build is the no-exceptions variant, because the AOT compiler must be able to precompile it; interpreted execution is far too slow for query work. Long-running engine calls are bounded by an execution timeout.
- **HTTP routes are flows.** The public record-retrieval surface (`/api/v1/data/...`) is compiled WASM flow bundles mounted by configuration. The host does request plumbing and nothing else:

  > There is no gateway: the only host glue is socket plumbing with zero decisions — HTTP request → one request frame → response frames written verbatim; all routing, query parsing, format selection, caching, and ETag logic live inside the WASM flow. Which flow owns which listener path is configuration, never Go code.

  Requests are serialized per flow instance; each mount runs a small pool of instances (default 4).
- **Ingestion runners are flows too.** The credentialed source workers (Space-Track, UDL, and friends — `ingest` in the CLI) run as flows over the host's timer, HTTP egress, and guarded persistence, not as custom Go fetchers.
- **Host functions are few.** The shared set is essentially `clock_now_ms`, `random_bytes`, and `log`. Everything else a module can do comes through the capability bridge.
- **Capabilities are explicit grants.** A module's access to storage is granted (and refused) as named capabilities — `storage.write` (write a record of a schema) vs `storage.query` vs `storage.delete`. Write does not imply read; a module that cannot load cannot touch anything.

The browser SDK follows the same boundary: a browser peer hosts the same wasm-threaded modules, so a flow behaves identically whether its host is a server daemon or a page.

## 8. Apps and the store

### Apps

An "app" is a loadable runtime module or a timer-served flow bundle — the ingest workers are apps in this sense. The app registry treats an app entry as data like anything else: an installed entry is *decoded from* a signed app-package FlatBuffer record (`APP`), never invented from metadata, and the node's own dashboard record is built from the artifact bytes it serves, so the record and the served page can never disagree. `apps list` reads the public app feed; `apps run` fires an app's schedule on demand.

A served app page is self-contained by contract: CSS and JavaScript inlined, assets as data URIs, zero external requests. Pages served by a node load nothing from external origins — the sibling [API Guide](./AGENT-API-GUIDE.md) assumes the same rule for anything you mount.

### The marketplace / store

- Listing, purchase-request, and review records (`STF`, `PUR`, `REV`) live on dedicated pubsub topics and in the store; the catalog is DHT-broadcast; delivery of purchased data goes over per-buyer topics.
- Canonical discovery is the `PLG` manifest published on spacedatastandards.org: exactly one signed listing per plugin ID and version, so the network can agree on what "the plugin `X` version `Y`" means.
- Module delivery is a signed handshake: discover the manifest, connect to the provider peer, complete a challenge/grant exchange verified against the licensing-grant key advertised in the provider's identity, fetch the encrypted WASM bundle by content address, unwrap the content key, decrypt locally, and load it through the standard module harness. Nothing server side ever runs the module or sees the content key.
- Encrypted record streams advertise their own field-level policy in-band: `FSM` records mark each field Public / Encrypted / Redacted / Unavailable, so a consumer can handle a record it cannot decrypt correctly.

`marketplace` in the CLI filters listings by kind (`data_stream` or `wasm_module`), access type (`one-time`, `subscription`, `streaming`, `query`), standard, tags, and provider ID.

## 9. How the pieces fit

Follow one record from origin to a consumer's screen:

1. **Origin.** A provider (human, script, or flow worker) acquires source data — say, an orbital element set — and encodes it as a size-prefixed SDS FlatBuffer (`OMM`). The bytes are now the record; there is no other form.
2. **Validation and admission.** `ingest`-style workers or the publish API present the bytes. The node checks the schema is allowed for publishing on this node, checks the session/trust/authorization policy, caps the body size, and validates the record structurally against the schema. The record's own size prefix and file identifier decide which standard's table it enters — a caller can never steer an `OMM` record onto another standard's table through an allowed path.
3. **Storage.** The bytes are appended to the standard's append-only stream (content-addressed, CID-deduplicated), indexed, journaled, and mirrored into the engine's per-source shadow table for the standard. On disk: one stream, one engine, one format.
4. **Announcement.** A signed feed head goes out on the schema's feed-heads topic; the record may also stream on the schema's pubsub topic. The provider periodically materializes the authoritative set into a shard + query index, signs a DPM, pins it to IPFS, and signs a PNM.
5. **Propagation.** Other nodes verify the feed head / PNM signatures against the publisher's published identity key, fetch the shard by content address over `flatsql-sync`, verify hashes and signatures, and import it. Full nodes with public addresses serve and pin; light peers and browsers sync over relay.
6. **Serving.** Records answer queries from the engine: the public schemas over the anonymous HTTP data plane, everything else behind authentication. A requesting browser is itself a node — it fetched, verified, and stores the same bytes locally rather than trusting the server's word.

That is the whole design: one format, one identity tree, signed at every boundary, with the host acting only as a connector.

## 10. Where to go next

- **[Operator Enrollment](./OPERATOR-ENROLLMENT.md)** — become an operator or admin: key generation, `derive-xpub`, the dashboard enrollment form, trust levels, withdrawal and lockout rules, and every exact HTTP call.
- **[API Guide](./AGENT-API-GUIDE.md)** — the REST surface in full: authentication, the anonymous allowlist, write paths, error and refusal semantics, and CORS/same-origin behavior.
- On a running node: `http://<node>/api/v1/docs` serves the generated OpenAPI reference for exactly the routes that node mounted (anonymous, same-origin).
- [TECH-PATH.md](./TECH-PATH.md) — the authoritative statement of the architecture, with rationale and the order data flows.
- `sdn-server/docs/gateway-api.md` — the gateway API design and implementation record.
- `README.md` — installation, quick start (`init`, `start`, `status`), and components.

## Not yet supported

- No CLI command creates an operator (xpub) row. Enrollment happens through the dashboard, `config.yaml`, the first-admin bootstrap, or the node-root ceremony — see [Operator Enrollment](./OPERATOR-ENROLLMENT.md) for each path and its exact behavior.
- Anonymous publishing is not enabled by default on a node; it is an explicit opt-in configuration, and even then records are attributed to an untrusted principal.
- The bulk record-retrieval routes present JSON for the orbital-element standard only; other standards answer in the native streaming FlatBuffer format. JSON is a read-side presentation choice, not a second wire format.
