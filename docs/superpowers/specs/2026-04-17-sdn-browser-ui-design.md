# SDN Browser UI and Canonical Marketplace Design

## Summary

This design replaces the repo's old SDN-specific web surface with a new SDN UI that is shipped by `sdn-js`, can run in a plain browser with a local Helia/libp2p backend, and can also be hosted by `sdn-server` while reusing the same runtime and state model. The server root path `/` will serve the new SDN UI. The IPFS WebUI will be restored to upstream-style behavior and served separately at `/webui`, matching Kubo conventions instead of acting as the SDN UI shell.

The SDN UI must show the real unified licensing/module-delivery flow using actual live network traffic:

- provider discovery / connect
- challenge received
- grant received
- encrypted CID fetch
- wrapped content key unwrap
- bundle decrypt
- SDK load
- module invoke result

The UI must also show a broad but honest browser-visible node census labeled `Observed SDN peers`, sourced from actual libp2p/DHT activity after bootstrapping from the seeded live server.

## Decisions Already Locked In

- The SDN product path stays on the unified wasm licensing/module-delivery flow.
- The public encrypted delivery protocol remains `/space-data-network/module-delivery/1.0.0`.
- Legacy `/orbpro/*` broker/bootstrap flows must not return.
- The UI is browser-first and reusable from `sdn-js`.
- No helper service is used.
- The browser uses `sdn-js` + Helia/libp2p directly.
- The browser bootstraps from the live demo server and expands peer visibility through libp2p/DHT.
- The marketplace is canonicalized around one `PLG` per `PLUGIN_ID + VERSION`.
- `PLG` must be extended upstream in `spacedatastandards.org`; no repo-local shadow schema is allowed.
- The identity layer is based on `hd-wallet-wasm`, and the existing `hd-wallet-ui` vCard flow should be embedded directly instead of rebuilt.
- The UI must support lookup by blockchain address.

## Current Runtime Facts

- Published packages already proving the live path:
  - `space-data-module-sdk@0.7.0`
  - `@spacedatanetwork/sdn-js@2.0.2`
- Live provider descriptor:
  - `publicKey`: `0257d9a39fac79d4c36e017b3b6913f60684586605ebb9370cf417ef44bf0f7cd2`
  - `peerId`: `16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45`
  - advertised relay: `/ip4/104.131.11.220/tcp/8080/ws/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45`
- The older machine reference `159.203.150.8` is not the client seed target. The browser should seed from the live daemon's advertised address.
- External end-to-end delivery is already proven against the real remote server and must be reused as the product reference path.

## Goals

- Ship a new SDN UI at `/` that can run against:
  - a browser-owned `sdn-js` Helia node
  - a server-hosted environment that still reuses the same runtime interface
- Restore IPFS WebUI to upstream-style IPFS-only behavior and serve it at `/webui`
- Discover modules from live `PLG` records instead of hardcoded manifests
- Show the real module-delivery lifecycle from live traffic, not simulated events
- Reuse `hd-wallet-ui` directly for login, identity, addresses, signatures, and vCard UX
- Support decentralized lookup of nodes by blockchain address
- Remove the `spacedatastandards-site` product surface from this repo
- Update `AGENTS.md` so it reflects the real runtime contract and the remaining implementation TODOs

## Non-Goals

- No native app rewrite
- No helper service or custom seed HTTP service
- No repo-local replacement schema for `PLG`
- No fake event timeline
- No revival of the legacy broker/bootstrap product path
- No second marketplace listing record alongside `PLG`

## Architecture

### High-Level Shape

There will be three product layers:

1. `spacedatastandards.org`
   - owns the canonical `PLG` schema extension and generated bindings

2. `sdn-js`
   - owns the browser-first runtime, live discovery, module delivery instrumentation, wallet integration, and SDN UI package

3. `sdn-server`
   - hosts the SDN UI at `/`
   - hosts upstream-style IPFS WebUI at `/webui`
   - optionally exposes public metadata endpoints that the SDN UI can consume, but does not become a required helper service

### SDN UI Runtime Model

The UI must not call libp2p, Helia, or server endpoints directly from view components. Instead, all screens consume a shared runtime adapter.

Proposed runtime adapter modes:

- `helia-local`
  - primary mode
  - the browser creates its own `SDNNode` / Helia / libp2p instance
  - uses the seeded live server multiaddr as the initial foothold
  - expands through libp2p/DHT/provider discovery
  - is the source of truth for live module-delivery visualization

- `server-attached`
  - secondary mode
  - used when the SDN UI is served by `sdn-server`
  - reuses the same runtime state/event interfaces
  - may consume server-exposed public metadata where useful
  - should remain as thin as possible so the product stays browser-capable

The runtime adapter interface exposes:

- local identity state
- backend mode and connection state
- marketplace listings
- provider descriptors
- observed peer sightings
- delivery timeline events
- lookup/search APIs

## Shared SDN UI Modules in `sdn-js`

The new `sdn-js` UI package will be split into focused modules.

### `runtime`

Responsibilities:

- create the selected backend adapter
- own connection/session state
- expose a typed event bus and query API to the UI

### `wallet`

Responsibilities:

- wrap `sdn-js` crypto exports backed by `hd-wallet-wasm`
- derive requester identity, peer ID, xpub, signing key, encryption key
- sign module-delivery challenge proofs
- verify signatures and chain proofs used for trusted listing/identity display

### `delivery`

Responsibilities:

- reuse the proven `requestModuleGrant` / `fetchEncryptedModuleBundle` path
- emit instrumentation events around:
  - provider discovery
  - candidate address resolution
  - connect / protocol dial
  - challenge received
  - grant received
  - encrypted CID fetch started/completed
  - wrapped content key unwrap
  - bundle decrypt
  - SDK load
  - invoke start/result/error

### `marketplace`

Responsibilities:

- discover live `PLG` records from the network
- verify and normalize them
- dedupe by canonical `PLUGIN_ID + VERSION`
- join listing information with verified publisher identity data

### `identity`

Responsibilities:

- embed `hd-wallet-ui` directly
- map wallet UI output into SDN-specific identity display and lookup tools
- surface blockchain addresses, xpub, peer ID, and EPM/vCard artifacts

## UI Surface

The SDN UI will have four primary views.

### Network

Shows:

- current local identity summary
- backend mode: `helia-local` or `server-attached`
- seed connection state
- `Observed SDN peers`
- rolling peer sightings with evidence source:
  - seeded bootstrap
  - DHT discovery
  - provider observation
  - direct protocol contact
  - verified identity/listing evidence
- node lookup tools

### Marketplace

Shows:

- live-discovered canonical `PLG` listings
- publisher card built from `PLG` plus verified identity/EPM data
- textual search and simple filtering over network-visible records
- a `Run live demo` action for any selected live listing

There is no hardcoded demo manifest in the product path.

### Delivery

Shows:

- provider descriptor and current connection status
- real-time event timeline for actual module-delivery comms
- raw technical detail panel
- bundle/grant/key details
- decrypted/loaded/invoked result state

### Identity

Shows:

- embedded `hd-wallet-ui`
- the identity vCard workflow
- derived addresses and SDN identity data
- chain-proof-backed address display
- lookup by blockchain address, peer ID, xpub-derived identity, and related handles

## Peer Census Strategy

The count shown in the UI must be labeled `Observed SDN peers`.

A peer is counted once it has actual evidence of SDN participation, for example:

- it appears in a relevant SDN/provider DHT discovery path
- it advertises or participates in SDN-specific protocol activity
- it is associated with a valid SDN identity/provider record
- it is observed during a successful or failed-but-real module-delivery interaction

This count is intentionally not labeled as "all SDN nodes" because a browser cannot honestly guarantee full network coverage.

## Bootstrap and Peer Expansion

The initial browser bootstrap uses the live daemon's advertised websocket relay:

- `/ip4/104.131.11.220/tcp/8080/ws/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45`

After bootstrapping, peer expansion happens through libp2p/DHT and subsequent live observations. No custom seed HTTP API is added.

## Identity and Address Lookup

### Wallet and vCard

The SDN UI will embed `hd-wallet-ui` directly and reuse its existing features:

- login / unlock
- account and address derivation
- signing hooks
- identity card / vCard editing
- QR export
- signed vCard export

The SDN shell will add an adapter that consumes the wallet UI's login callback as the requester identity used by SDN.

### Lookup by Blockchain Address

The lookup source of truth is EPM identity data using:

- `KEY_ADDRESS`
- `ADDRESS_TYPE`
- `CHAIN_PROOFS`

The decentralized lookup contract will use a deterministic DHT namespace per chain, e.g.:

- `space-data-network/identity/address/bitcoin`
- `space-data-network/identity/address/ethereum`
- `space-data-network/identity/address/solana`

Lookup flow:

1. normalize the input address according to chain rules
2. hash `namespace + normalizedAddress` into a deterministic discovery CID
3. query libp2p/DHT providers for that target
4. fetch the candidate node identity / EPM data
5. verify the chain proof with `hd-wallet-wasm`
6. resolve the node profile only after proof verification succeeds

This keeps the lookup decentralized and verifiable instead of depending on a centralized registry.

## Canonical `PLG` Marketplace Extension

`PLG` remains the only canonical listing artifact per `PLUGIN_ID + VERSION`.

### Existing `PLG` Responsibilities to Keep

- runtime manifest
- encrypted artifact reference (`WASM_CID`, hash, size, provider identity)
- signature-bearing module metadata

### New Field Groups

#### Storefront identity

- `TAGLINE`
- `PUBLISHER_NAME`
- `PUBLISHER_HANDLE`
- `PUBLISHER_URL`
- `SUPPORT_URL`

#### Search and display

- `TAGS`
- `FEATURES`

#### Media

- `SCREENSHOT_URLS`
- `BANNER_URL`

#### Commerce

- `PAYMENT_MODEL`
  - `Free`
  - `OneTime`
  - `Subscription`
- canonical integer USD field such as `PRICE_USD_CENTS`
- optional subscription period/duration field
- optional supported settlement method field if required

#### Listing lifecycle

- `LISTING_STATUS`
  - recommended values: `Public`, `Unlisted`, `Retired`

#### Release/storefront metadata

- `CHANGELOG_URL` or equivalent release notes pointer

### Rules

- No separate listing record type for module marketplace entries
- Search/indexing derive directly from `PLG`
- Signatures continue to cover the canonical listing/runtime record

## `spacedatastandards.org` Publishing Flow

The schema change path must be:

1. update `../spacedatastandards.org`
2. regenerate bindings and archives
3. publish artifacts
4. monitor CI until green
5. update `space-data-network` to consume the published versions

Required generated outputs to refresh and consume:

- JS / TS
- Go
- any other language packages directly required by current SDN repos and runtime/tooling

This repo must not continue using a local shadow `PLG` change after the canonical publish exists.

## `sdn-server` Integration

### Routing

- `/` -> new SDN UI build
- `/webui` -> upstream-style IPFS WebUI
- `/admin` -> admin/auth flows, not the primary IPFS UI

The SDN UI should show an `Open IPFS WebUI` action only when `/webui` is present.

### Repo Cleanup

- remove `spacedatastandards-site` from active scripts/docs/product references
- restore the server-side WebUI surface to IPFS-only behavior matching upstream usage
- stop treating the old customized `webui` as the SDN product shell

## Data Flow

### Live Marketplace + Delivery Flow

1. browser boots a local `sdn-js` Helia node using the seeded live server address
2. runtime expands visibility through libp2p/DHT/provider discovery
3. runtime discovers live `PLG` records and verified publisher identity data
4. UI renders deduped canonical listings by `PLUGIN_ID + VERSION`
5. user selects a listing
6. runtime derives requester identity from `hd-wallet-wasm`
7. runtime performs real module-delivery challenge/proof/grant flow
8. runtime fetches the encrypted bundle CID over Helia/IPFS
9. runtime unwraps the content key and decrypts the bundle
10. runtime loads the bundle with `space-data-module-sdk`
11. runtime invokes the module and streams each stage into the Delivery timeline

### Address Lookup Flow

1. user enters a chain address
2. runtime normalizes and hashes it into the address discovery namespace
3. runtime queries libp2p/DHT providers
4. runtime fetches candidate identity data
5. runtime verifies chain proof(s)
6. UI renders the resolved node identity and related records

## Error Handling

The SDN UI should preserve raw technical truth instead of replacing errors with friendly-but-vague text.

Rules:

- show a concise user-facing summary and the raw technical detail side-by-side
- preserve protocol error codes such as grant rejection reasons
- distinguish:
  - seed/connectivity failure
  - DHT discovery timeout
  - provider mismatch
  - grant rejection
  - CID fetch failure
  - content hash mismatch
  - unwrap/decrypt failure
  - SDK load failure
  - invoke failure
- keep partially completed timeline stages visible after failure

## Testing and Verification Strategy

Implementation should follow TDD.

### Standards

- add tests in `spacedatastandards.org` for new `PLG` fields and generated binding expectations

### `sdn-js`

- add unit tests for:
  - `PLG` normalization/dedupe by `PLUGIN_ID + VERSION`
  - observed-peer accounting
  - address normalization and lookup key generation
  - delivery event instrumentation ordering and payload shape
- add browser-facing integration tests for:
  - wallet identity handoff from `hd-wallet-ui`
  - live listing selection into delivery flow

### Live Verification

- verify the UI against the real seeded server
- verify the displayed event timeline comes from actual traffic
- verify the browser can continue peer expansion after initial seed connection
- verify the default seed uses the live advertised `104.131.11.220` address rather than the stale `159.203.150.8`

## AGENTS.md Required Updates

`AGENTS.md` must be rewritten so it stops describing contradictory or obsolete product direction.

Remove or replace content that conflicts with the approved direction:

- helper-service requirement
- `spacedatastandards-site` as an active product surface in this repo
- SDN-specific use of the old `webui` as the main SDN UI shell
- any implication that legacy browser broker/bootstrap paths are part of the current product

Add explicit TODOs covering:

- upstream `PLG` extension in `spacedatastandards.org`
- published artifact consumption back into `space-data-network`
- new `sdn-js` SDN UI package
- direct `hd-wallet-ui` embedding
- address lookup namespace and verification flow
- server routing changes for `/` and `/webui`
- removal of `spacedatastandards-site`
- restoration of IPFS-only upstream-style WebUI behavior
- live browser verification against the seeded remote server

## Risks

- Browser peer visibility will still be partial; the UI must keep the `Observed SDN peers` label explicit.
- `PLG` schema changes require a clean upstream publish before this repo can fully switch.
- Embedding `hd-wallet-ui` directly may require style/runtime isolation work so it does not collide with the SDN shell.
- Restoring upstream-style IPFS WebUI behavior may reveal assumptions baked into the existing customized `webui` tree.

## Recommended Implementation Sequence

1. extend and publish canonical `PLG` in `../spacedatastandards.org`
2. update `space-data-network` to the published artifacts
3. build the `sdn-js` shared runtime/event/wallet/marketplace layers
4. embed `hd-wallet-ui`
5. build the SDN UI shell and views
6. restore IPFS WebUI routing to `/webui`
7. remove `spacedatastandards-site` product wiring
8. update `AGENTS.md`
9. run live verification against the seeded remote server
