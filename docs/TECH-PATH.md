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
- **Boot is open + verify + tail**, not re-derivation: the control database
  and auxiliary state carry resume marks, so a store-heavy node boots in
  I/O-bound seconds (measured in production: sub-second store-open, tens of
  seconds of tail hydration where it used to be minutes to hours). Full
  re-derivation from the record journal remains the always-available
  fallback — the journal is the source of truth; the persisted index is a
  recovery accelerator that can cost time, never data.

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

*Linked from the repository README. Update this document in the same commit
as any change that alters the path it describes.*
