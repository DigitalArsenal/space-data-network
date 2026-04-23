# Node Identity, EPM Directory, And Shared Query Design

## Goal

Make the node's HD-wallet-derived identity the single source of truth for SDN and IPFS node identity, while keeping user access management separate. The node must publish and consume EPM-backed directory data for nodes and users, persist that data in FlatSQL for query, and expose the same directory model to both the server-hosted SDN dashboard and the `sdn-js` Helia/browser runtime.

## Non-Goals

- Replacing the wallet-based user access list with node identity.
- Rebranding or changing the upstream `/webui` product surface.
- Introducing repo-local FlatBuffer schemas.
- Designing bootstrap peers as a requirement for SDN discovery. Node discovery must continue to work through DHT/rendezvous advertisement flags.

## Current Problem

Today the repo has two identity domains mixed together:

1. The SDN daemon has an HD-wallet-backed node identity derived from the encrypted mnemonic under the node data directory.
2. The upstream IPFS WebUI is still backed by a separate Kubo daemon identity from `ipfs.id()`.

That produces visible mismatches:

- `/` shows SDN peer identities on the peers page but Kubo identities on the status page.
- `/webui` talks to a different IPFS identity than the SDN node identity.
- Node identity, node EPM publication, and directory query flows are not unified around a single persisted key root.

The old code path in `5e140f41f029c0a10afeac5991fd456a93b62fa9` used the HD-wallet-derived secp256k1 key as the libp2p/IPFS identity and treated EPM as part of node identity and discovery. The new architecture needs that same property, but implemented in the current `sdn-server` / `sdn-js` split.

## Required Outcome

The finished system must have these properties:

- The node mnemonic is the only persisted root secret for node identity.
- The node libp2p/IPFS identity is derived from that mnemonic.
- The SDN daemon and the managed IPFS/Kubo identity resolve to the same peer identity.
- User login and trust management remain separate and continue to use the configured access list.
- The node has a dedicated identity-management surface for creating and editing its EPM.
- Discovered node and user EPMs are persisted locally in FlatSQL and queryable.
- The same node/user directory query model is usable from both daemon-backed SDN UI and `sdn-js` Helia mode.

## Identity Model

### 1. Separate Node Identity From User Access

The system must preserve two distinct concepts:

- `Node identity`: the daemon's own long-lived identity, keys, EPM, published addresses, and directory presence.
- `User access`: the list of users allowed to authenticate to the node and their trust levels.

These are related operationally but not the same object.

User access continues to use:

- wallet login
- configured `xpub`
- configured or TOFU-bound `signing_pubkey_hex`
- trust level / role

Node identity is not represented as an entry in the user access list unless explicitly added for some other reason. The node is an infrastructure identity, not a logged-in operator identity.

### 2. Single Source Of Truth For Node Keys

The encrypted mnemonic file under the node data directory remains the single persisted source of truth for the node.

All node keys are derived from that mnemonic server-side through `hd-wallet-wasm`:

- `identity key`: secp256k1 key used for libp2p/IPFS peer identity
- `signing key`: key used for node-issued signatures and attestations
- `encryption key`: key used for node encryption surfaces and EPM identity metadata

The derived key set is one identity bundle rooted in one mnemonic. The mnemonic is the single source of truth; the derived keys are not independent roots.

### 3. Bitcoin Address Display

The node-management surface must expose a Bitcoin address for the node identity bundle.

The design requirement is:

- the node UI clearly shows the node peer ID
- the node UI clearly shows the Bitcoin address derived from the node's mnemonic-backed identity bundle
- the EPM includes the relevant identity and chain-proof fields needed to verify that relationship

The canonical root secret remains the mnemonic. The displayed Bitcoin address is derived from that same root, not stored independently.

## Managed IPFS Identity

### 1. Target Behavior

The daemon-managed node identity and the IPFS identity shown by `/webui` must match.

That means:

- the peer ID presented by the SDN node
- the peer ID presented by the managed Kubo/IPFS node
- the peer ID shown on the SDN dashboard status page
- the peer ID shown in the IPFS WebUI status page

must all resolve to the same node identity in managed-node mode.

### 2. Architecture Decision

The daemon must own the node identity and project it into the managed Kubo/IPFS repo identity.

The supported model is:

- `managed IPFS mode`: the daemon controls the Kubo repo identity and ensures it is derived from the node mnemonic

An attached arbitrary external Kubo daemon may still exist as a compatibility mode, but it is not the canonical identity path and it cannot be the basis for the unified identity contract.

In practice, the managed path must:

- derive the node secp256k1 identity key from the node mnemonic
- persist the mnemonic encrypted at rest
- ensure the managed IPFS repo identity uses the corresponding derived private key
- surface one consistent peer ID through both SDN and IPFS APIs

## Node Identity Management Surface

### 1. Separate UI From User Wallet UI

`hd-wallet-wasm` remains the user wallet and user-authentication surface. It should not become the node identity editor.

Instead, the repo needs a separate node-identity management surface with similar capabilities:

- view node identity
- view node peer ID
- view node Bitcoin address
- view derived key metadata and public keys
- create or edit node EPM content
- publish or refresh node EPM
- inspect current published discovery identity data

This surface may reuse HD-wallet derivation logic and visual patterns, but node mnemonic handling stays server-side. The browser must not receive the node mnemonic.

### 2. Authorization

Only authenticated admins can access node-management actions that mutate node identity metadata or republish node EPMs.

This surface is part of the SDN dashboard/admin capability set, not the public login flow.

## EPM As Part Of Discovery

### 1. Discovery Contract

Node discovery continues to rely on the SDN advertisement flag window and DHT/routing-discovery rendezvous.

EPM becomes a first-class companion of that discovery path:

- each node publishes its current node EPM
- discovery resolves peers through the SDN advertisement flag rendezvous
- once a peer is discovered, the system resolves and fetches its EPM
- the fetched EPM is stored locally and indexed

The design goal is:

- advertisement proves candidate SDN presence
- EPM provides the durable node/user identity card and searchable profile data

### 2. Node Information In EPM

The node EPM must include enough information to support directory and identity workflows:

- peer ID / multiformat address data
- published addresses
- signing and encryption public key metadata
- Bitcoin address and other supported chain-proof data
- human-readable node identity fields

This follows the canonical upstream `spacedatastandards.org` EPM schema. No repo-local replacement schema is introduced.

## Local FlatSQL Directory Index

### 1. Storage Requirement

Discovered and locally authored EPMs must be stored in a local FlatSQL-backed directory index.

The directory index must support at least:

- `node EPMs`
- `user EPMs`
- raw EPM bytes
- extracted searchable fields
- source metadata such as local, discovered, imported, or refreshed
- timestamps and version/update markers

### 2. Query Requirement

The SDN dashboard needs query flows for both nodes and users.

Minimum query shapes:

- by peer ID
- by Bitcoin address
- by name / DN / legal name
- by alternate name
- by multiformat address
- by public key fingerprint or hex where applicable

The dashboard must not depend on transient in-memory discovery snapshots for directory search. It should query the local indexed FlatSQL directory.

## Shared Directory Interface Across Server And Helia

### 1. Common Query Model

`sdn-js` must define a shared directory query interface that works in both:

- daemon-backed mode
- Helia/browser-only mode

The UI must use this shared interface rather than special-casing query semantics per runtime.

### 2. Adapter Model

Recommended adapter split:

- `server directory adapter`: queries daemon-backed APIs over HTTP
- `helia directory adapter`: queries the local browser/runtime-backed directory store using the same domain model

The same UI views and search semantics should work on top of either adapter.

### 3. Local Persistence In Browser/Helia Mode

For Helia/browser mode, the same conceptual directory model must exist locally:

- discovered node/user EPMs are cached locally
- searchable fields are indexed locally
- the UI can query nodes and users with the same filters as the server-backed dashboard

The browser persistence mechanism can differ from server FlatSQL, but the query contract must match the server-facing domain model.

## UI Surface Contract

### `/`

The SDN dashboard at `/` becomes the node-aware surface:

- shows SDN node identity, not upstream Kubo identity
- supports node and user directory queries
- supports node identity management for admins
- continues to show SDN-only peers based on SDN discovery evidence

### `/webui`

The IPFS dashboard at `/webui` remains the upstream-style IPFS surface:

- upstream IPFS features stay intact
- identity shown there must still match the managed node identity in managed-IPFS mode
- no SDN-specific directory UX is added there

### `/admin`

`/admin` remains for admin/auth flows and must not be collapsed back into `/webui`.

## Migration Strategy

### Phase 1: Lock The Canonical Node Identity Contract

- document the node mnemonic as the only root secret
- document the derived node key set and paths
- make status and info surfaces consistently use SDN node info on `/`

### Phase 2: Unify Managed IPFS Identity

- teach the managed IPFS/Kubo repo identity to load from the node mnemonic-derived identity key
- eliminate the SDN-vs-Kubo peer ID mismatch in managed mode

### Phase 3: Node Identity Management UI

- add the separate node-management surface
- allow viewing and editing node identity profile data
- allow generating and republishing the node EPM

### Phase 4: EPM Discovery And FlatSQL Indexing

- persist local and discovered EPMs
- build extracted searchable indexes
- expose query APIs for nodes and users

### Phase 5: Shared `sdn-js` Directory Query Layer

- define the common query model
- implement server and Helia adapters
- move SDN dashboard queries onto that interface

## Testing Requirements

### Server

- node startup derives stable identity from mnemonic
- managed IPFS identity matches daemon identity
- node EPM publish/refresh path includes node identity fields
- discovered EPMs are persisted and queryable in FlatSQL
- directory APIs return correct results for nodes and users

### UI

- `/` status page shows SDN node peer ID and SDN version
- `/webui` remains upstream-compatible
- admin node-management flow renders and updates correctly
- node and user directory queries work against server-backed adapters

### `sdn-js` / Helia

- shared directory query interface behaves identically across adapters
- Helia mode can cache and query discovered EPMs locally
- SDN dashboard views render correctly from either adapter

## Risks And Constraints

- Kubo repo identity migration must avoid silently orphaning existing data or creating confusing identity flips.
- The node mnemonic must remain server-local and encrypted at rest.
- User wallet login and node key management must remain clearly separated in code and UI.
- Directory indexing must not invent local shadow schemas; it must derive fields from canonical upstream EPM structures.
- The initial implementation must stay focused on managed-node identity unification and directory query foundations, not full marketplace or trust-graph expansion.

## Recommended Execution Order

1. Fix `/` to use SDN node identity everywhere the root dashboard shows identity.
2. Define and lock the canonical node identity derivation contract.
3. Make managed IPFS/Kubo identity read from that same root.
4. Add the node-management UI and node EPM editing/publishing flow.
5. Persist discovered/authored EPMs into the local FlatSQL directory index.
6. Add node/user directory query APIs.
7. Add the shared `sdn-js` server/Helia directory adapters.

## Decision Summary

- Keep `node identity` and `user access` separate.
- Use the encrypted node mnemonic as the single persisted root secret.
- Make managed IPFS identity derive from that same root.
- Add a separate node-management surface instead of overloading `hd-wallet-wasm`.
- Treat EPM as part of SDN discovery and directory indexing.
- Store discovered node/user EPMs in FlatSQL for local query.
- Expose one shared directory query model across daemon-backed and Helia-backed `sdn-js`.
