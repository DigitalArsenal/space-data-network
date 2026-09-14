# The SDN Technical Path

This document is the authoritative statement of the architecture Space Data
Network is building toward — the design the owner has ruled, in the order the
data flows. Every subsystem below is either live, landing, or in flight; none
of it is aspiration without an implementation lane. When code and this
document disagree, one of them is drift, and drift gets fixed, not accepted.

---

## 1. One format, end to end: streamed SDS FlatBuffers

The unit of data everywhere is a [Space Data Standards](https://spacedatastandards.org)
FlatBuffer record, size-prefixed, in wire form. The same bytes that travel
libp2p pubsub are appended to disk, indexed in place, queried without
deserialization, and served back out. There is no second representation:
**network → disk → query → network is one format.**

- On disk, records live in append-only stream files.
- Standards are neutral, durable contracts: descriptions name capability
  classes, never vendors, sites, or organizations. Provenance (which archive,
  which measurement program) is *data*, carried in record fields at runtime.

## 2. Storage: FlatSQL only, durable on disk

FlatSQL is the only storage engine in the stack — no embedded SQLite side
databases, no secondary engines. It runs as a WASM module in every runtime
and persists **its own** structures:

- **Tables and B-tree indexes are zero-copy indexes over the record
  streams** — they store offsets into the FlatBuffer stream files, never
  copies of payload bytes. Query execution reads fields directly out of the
  streamed records.
- The engine writes its pages through its **own VFS**: seven explicit,
  offset-addressed host imports (`flatsql_io_open/read/write/truncate/sync/
  size/close`) with identical signatures in the browser and under WasmEdge.
  The browser satisfies them from chunked key-value persistence stores
  (IndexedDB and friends); the Go host satisfies them over real files.
- **Boot is open**, not re-derivation: the control database holds every
  record, its index and the node's auxiliary state, so a store-heavy node
  boots in I/O-bound seconds and serves at once. The engine hot window (a
  bounded cache of the routed standards) is reconciled in the background from
  its own persisted arena plus a residency ledger; only the auxiliary journal
  still carries a resume mark. There is no record journal and no fallback
  re-derivation: the database is the record store, and a file that does not
  open is refused, never discarded.

## 3. Compute: isomorphic WASM, host as connectors only

All functionality is WASM. The Go host and the browser JS host are thin,
application-blind connector layers exposing the same small set of generic
hooks (network, storage, file I/O, signing) — a module compiled once runs
identically under WasmEdge on a node and in a browser tab, threaded via the
wasi-threads contract.

- Divergence between runtimes is a P1 defect, caught by running identical
  scenarios against both artifacts.
- Every surface that consumes a propagator (or any provider-shaped
  capability) takes it as a **pluggable port** — nothing is hardwired to a
  single implementation.
- Engine resources are reached the same way: a generalized **provider-access
  ABI** gives modules control of imagery/terrain providers and read access
  to the tile and heightmap bytes already resident in memory — one copy into
  WASM linear memory, no re-fetch, no re-decode.

## 4. Identity: one HD key, human faces, machine aliases

A node's identity is a single HD wallet key, encrypted at rest with a
machine-derived key (fail closed on machine change; identities survive
rebuilds).

- The public identity a human sees is a **contact card**: name, title,
  organization, photo, human email — never key material.
- The machine identity rides in the vCard/EPM/QR artifacts, and only there:
  official vCard properties only, the xpub and HD signing/encryption paths
  expressed as EMAIL aliases, the EPM signature chain verifiable end to end.
  QR payloads stay small enough to scan.
- Peer IDs, multiaddrs, and protocol lists are derivable or operational
  detail — they appear on no card and no QR.

## 5. Keys: separated domains, derived children

Signing authority is split by domain, structurally:

- **The fleet update root is isolated.** It signs binary updates and nothing
  else.
- **Grant signing uses an HD-derived child of the node identity** — the
  module-delivery grant lane advertises the child's public key in its
  provider descriptor, and a node refuses to serve grants if the grant key
  and update root ever collide (fail closed, loudly).
- Publisher trust is priced by an adversarial-security bond on chain
  addresses derived from the same identity — trust in code is trust in a
  bonded key, not a filename.

## 6. Modules: protected artifacts, delivered over SDN

Modules — propagators, RF models, analysis engines, whole applications — are
signed, and closed modules are **encrypted** with per-user grants:

- Dotted, registry-safe identifiers; manifests that declare their protection
  honestly.
- Delivery is over SDN's module-delivery protocol with a challenge/grant
  handshake verified against the KRF-advertised grant key. **There is no
  same-origin fallback**: a published web artifact contains zero closed-module
  files (enforced by a publish-chokepoint gate), and a demo that cannot reach
  SDN delivery fails loudly rather than quietly serving unprotected bytes.
- The update lane for node binaries is the same discipline applied to the
  fleet: signed manifests, in-place apply, per-box rollback.

## 7. Network: an honest mesh with browser-grade doors

The mesh is libp2p. Fleet nodes and enrolled peers are structurally
protected; anonymous churn is admitted, tagged by observed behavior (speaking
an SDN protocol, joining SDN pubsub topics), and trimmed by watermark — the
connection manager never evicts the fleet or a browser that is actually
using the network.

- Browsers dial **CA-authenticated wss endpoints** — a node with a real
  certificate on its API port, or AutoTLS (`libp2p.direct`) on a node with no
  DNS name at all. No pinned certhashes in shipped bundles; they rot.
- Data reaches browsers over the same pubsub streams and sync protocols the
  nodes use between themselves — the browser is a peer, not a client of a
  bespoke API.

## 8. Interfaces: one app, human-first, verified live

The UI is one modular Svelte application with two faces (the node dashboard
and the orbital console), built from a design round trip whose exports are
transcribed verbatim — engine functionality is always the real engine's
native API, never a reimplementation.

- Surfaces are human-first: contact cards, not hex dumps; legends and
  physical boundaries, not sampling artifacts.
- Owner rulings become **executable gates**: unit tests that ban struck
  content structurally, build-time copy checks on the embedded artifact, and
  a live fleet-surface conformance checker that runs on a schedule against
  production — a ruling that isn't a failing test waiting to happen is a
  ruling that will drift.

## 9. The discipline that holds it together

- Verify the premise: directive facts about hosts, keys, versions, and
  deployed state are checked against live systems before anyone acts on them.
- Land on main: work ends merged, pinned, tagged, and demonstrably identical
  to what is live.
- Canary before fleet: a roll that regresses a measured baseline halts at
  one box and rolls back; the regression gets a task, not an excuse.
- Every measurement that gates a decision states its pin and its load; A/B/A
  on one box or it isn't evidence.

---

## Definitions

Explicit meanings for the terms of art above. Each entry states what the
thing **is**, where it **lives**, what it **does**, and what it **must never
do**. These definitions are normative: code that contradicts one is a defect.

### Keys and trust

**Node identity.**
The single BIP-32 HD (hierarchical deterministic) wallet root keypair that
*is* a node. Every other key the node uses is derived from it by a documented
path; nothing about the node's identity exists outside this root. It lives
only on the node's own disk, always encrypted (see machine-bound key-at-rest).
Its extended public key (xpub) is the node's public name — the libp2p peer ID
is *derivable from* the identity, which is why peer IDs are never treated as
identity themselves.

**Machine-bound key-at-rest.**
The rule for how the node identity is stored: encrypted on disk with a key
deterministically derived from the machine itself (hardware/OS identifiers),
so the file is useless if copied to another machine. Decryption fails closed
on machine change. A rebuilt box with the same machine identity recovers its
node identity; a stolen disk recovers nothing.

**HD derivation path / derived child key.**
A BIP-32 path (e.g. `m/44'/0'/0'/0/0`) that deterministically produces a
child keypair from the identity root. The same root and path always produce
the same child, so a child key never needs separate backup — custody of the
root is custody of every child. Paths are published (in the EPM/vCard
artifacts), private keys never are.

**Grant-signing child.**
The specific derived child key that signs module-delivery **grants**. It is
derived from the node identity by a fixed, documented path; its *public* key
is advertised in the provider's KRF so clients know what to verify against.
It signs grants and only grants. It must never sign updates, records, or
anything in another domain — and the node refuses to serve grants at boot if
the grant key and the update root are ever found to be the same key
(fail-closed domain-collision guard).

**Isolated update root.**
The Ed25519 keypair whose signature makes a fleet binary update trusted.
"Isolated" is a hard claim with three parts: (1) it signs **update
manifests and nothing else** — no grants, no records, no cards; every
signing call site in the codebase provably uses a different key; (2) it is
held only where updates are published from, never distributed to consumers
(nodes hold only its public half, pinned in their update configuration);
(3) compromise of any other key — including the full node identity of any
fleet box — does not confer the ability to ship code to the fleet, and
compromise of the update root does not confer the ability to sign grants or
impersonate identity. One key, one power.

**Publisher bond.**
The economic half of update trust: chain addresses (BTC/ETH/SOL) derived
from the publisher's identity hold value that the publisher forfeits by
signing malicious code (adversarial-security model). Verifiers can price
their trust in a binary by the bond behind its signature. The bond binds to
the *identity*, which is why the update root's isolation matters — the bond
is meaningless if unrelated lanes can spend the same trust.

**KRF (Key Reference Frame).**
The SDS record a provider serves that enumerates its public keys and what
each is *for* — including `PROVIDER_SIGNING_KEY.PUBLIC_KEY`, the
grant-signing child's public key. Clients verify grants against the KRF, not
against hardcoded keys. A KRF field left zero is a defect, not a default
(this was found live: grants were being "verified" against 32 zero bytes).

**Grant / challenge.**
The module-delivery handshake. The client proves control of its own key by
answering a **challenge**; the provider answers with a **grant** — a signed
(by the grant-signing child), time-bounded authorization naming the module
the client may fetch and the key material to decrypt it. No grant, no bytes.

### Data and storage

**Control database.**
FlatSQL's own on-disk database file (`control.flatsqldb`) holding the record
store: every record's bytes on its producer's table, the global record index
(`sdn_record_index`, whose rowid is the datasync cursor), provenance tags and
summaries, the node's auxiliary tables, and the engine's own index. It is the
source of truth. It is written only by the node's single FlatSQL engine, in
TRUNCATE journal mode; a crash loses nothing that was committed.

**Engine record arena.**
`control.flatsqldb.fsdata`, the FlatSQL engine's persisted copy of the
records resident in its hot window. A cache: discarded and rebuilt from the
control tables when it is unusable, never the other way round.

**Auxiliary journal.**
`auxiliary.flatsqlmeta`, the append-only log of the node's OWN state — its
encrypted EPM, pin ledger, dataset shard publications, source-batch licences,
asset-pin audit. Replayed from a resume mark at every boot. It is not records.

**Resume mark.**
A checkpoint stored inside the control database recording how much of the
auxiliary journal is already reflected in its tables, and the index rowid
the engine hot window had been mirrored through when its arena was last
flushed.

**Zero-copy index.**
A FlatSQL table/B-tree whose entries are `(key → stream offset, length)` —
pointers into the record streams, never copies of payload bytes. Query
execution follows the offset and reads fields directly from the FlatBuffer
in place. One copy of the data exists, in the stream.

**FlatSQL VFS (the seven imports).**
The engine's only doorway to persistence: seven explicit host functions
(`flatsql_io_open/read/write/truncate/sync/size/close`), offset-addressed,
with byte-identical names and signatures in every runtime. The browser
satisfies them over chunked key-value stores; the Go host over real files
under the store's base path. A host that provides no backing registers the
imports and refuses them, so an ephemeral engine *fails* a disk-backed open
rather than silently succeeding against RAM.

### Compute

**Isomorphic module.**
A WASM artifact that runs with identical observable behavior in the browser
JS host and under WasmEdge on a node — same ABI, same imports, same results,
threaded via the wasi-threads contract (`clang wasm32-wasip1-threads`, never
emscripten pthreads). "Runs in both but differently" is not isomorphic; it
is a P1 defect.

**Connectors-only host.**
The rule bounding the Go server and the browser JS shim: they expose small,
generic, application-blind capabilities (HTTP, TCP, storage, file I/O,
signing, provider access) and contain **no application logic**. If a feature
can be described without naming a protocol-level capability, it belongs in a
WASM module, not the host.

**Pluggable port.**
Any capability a module consumes through a declared, swappable interface —
a propagator, a data provider, a terrain source. The law: no surface may
hardwire a single implementation of a port-shaped dependency. Ports are what
make modules composable into flows and let closed implementations replace
open ones without touching consumers.

**Provider-access ABI.**
The generalized port through which WASM modules reach the engine's imagery
and terrain providers: a **control plane** (enumerate, select, configure,
prefetch — always via the engine's native provider objects) and a **data
plane** (read the decoded tile pixels and heightmap arrays already resident
in engine memory into WASM linear memory — at most one copy, never a
re-fetch or re-decode).

### Network

**Admission policy.**
How a node spends its connection budget. A **protected set** (bootstrap
peers, configured trusted peers, pinned peers, registry-trusted peers, and
peers proven interactive — speaking an SDN protocol or joining SDN pubsub
topics, including tunnelled browsers) is never trimmed. Anonymous churn is
admitted, tagged by observed behavior, and trimmed by **watermarks** (a
low/high band below the hard ceiling with reserved headroom) so the fleet
and real users always have room. Trimming is the library's own connection
manager doing its job — no bespoke reaper runs on the connection path.

**Browser door (wss / AutoTLS).**
A CA-authenticated WebSocket-secure endpoint a stock browser can dial: either
the node's own TLS listener on its API port, or an AutoTLS
(`libp2p.direct`) certificate on a node with no DNS name. Shipped bundles
carry **no pinned certhashes** (they rot on rotation) — browsers resolve
current addresses at runtime. A browser that connects is a **peer**: it
speaks the same pubsub and sync protocols nodes speak to each other.

### Verification

**Fleet-surface conformance.**
The live checker (`deployment/checks/fleet-surface-conformance.mjs`) that
runs against *production* on a schedule and fails on violations of owner
rulings — banned card properties, oversized QR payloads, struck identity
labels, unnamed board rows. It measures served bytes with browser-shaped
requests, refuses to pass an empty (dead-node) body, and its checks are
proven to fail against known-bad artifacts before they are trusted to pass.

**Canary roll.**
No binary reaches the fleet without one box demonstrating it first, A/B/A
against the incumbent, with measured gates (boot phases, serving latency,
data-path dials). A canary that regresses a measured baseline halts the
roll and rolls itself back; the regression becomes a task with the
measurement attached.

---

*Linked from the repository README. Update this document in the same commit
as any change that alters the path it describes.*
