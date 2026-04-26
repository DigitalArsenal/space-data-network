# Data And Modules Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add SDN `Data` and `Modules` surfaces that expose authenticated data publishing, FlatSQL-backed directory/query behavior, signed PNM/log-head distribution, encrypted data storefronts, and signed/encrypted WASM module marketplace and licensing flows without changing the upstream IPFS WebUI at `/webui`.

**Architecture:** Keep `/` as the SDN UI, `/admin` as authenticated admin, and `/webui` as the upstream-style IPFS dashboard. Data publication uses canonical `spacedatastandards.org` schemas, immutable FlatBuffer shards, signed publication log heads, `PNM` network notifications, FlatSQL materialized indexes, and `STF` storefront records. Module publication uses canonical `PLG` listings, encrypted WASM bundles, provider discovery through the existing module-delivery namespace, and `sdn.spaceaware.io` as the first licensing/provider node.

**Tech Stack:** Go (`sdn-server`, SQLite/FlatSQL, Kubo/libp2p, PNM queue), JavaScript/TypeScript (`sdn-js`, Vite, upstream IPFS WebUI overlays, Helia-compatible runtime adapters), `spacedatastandards.org` FlatBuffer bindings, `space-data-module-sdk`, `hd-wallet-wasm`, `hd-wallet-ui`.

---

## Council Findings Converted To Decisions

- `Data` and `Modules` are SDN-only menu items. Do not modify the `/webui` IPFS dashboard beyond keeping its existing separate entrypoint.
- Data storefront listings use `STF`. Module/plugin storefront listings use `PLG`. Do not create a second module listing record.
- Search and discovery derive from signed canonical records, not sidecar shadow records.
- No repo-local `.fbs` files or shadow bindings should be introduced for new schema work. Schema changes start in `../spacedatastandards.org`, are published, and are then consumed here.
- EPM remains the canonical signed entity profile. Node identity and user identity remain separate.
- PNMs announce signed publication state. They are not the database. The durable source of truth is immutable content plus signed log heads.
- Mutable append files are not the right primitive for IPFS. Use immutable shards and update signed manifests/log heads that point to those shards.
- FlatSQL is a local materialized query index. Distributed FlatSQL should replicate and verify signed immutable shards and log heads, not rely on mutable centralized SQL.
- Per-field encryption is schema-driven. FlatSQL stores encrypted fields as opaque ciphertext unless an explicit plaintext or encrypted-index rule exists.
- Homomorphic encryption is a governed extension track for narrow numeric aggregate use cases. Do not promise arbitrary SQL over ciphertext.
- `sdn.spaceaware.io` can be the first licensing/provider node, but browser clients must keep the direct module-delivery path and must not route through helper services.

## Existing Implementation Inventory

- SDN UI route overlays live in `sdn-js/ui/src/upstream-webui/overrides/bundles/routes.js` and `sdn-js/ui/src/upstream-webui/overrides/navigation/NavBar.js`.
- Current SDN override pages live in `sdn-js/ui/src/upstream-webui/overrides/directory/`, `sdn-js/ui/src/upstream-webui/overrides/settings/`, and `sdn-js/ui/src/upstream-webui/overrides/status/`.
- Shared SDN UI runtime adapters live in `sdn-js/src/ui/runtime/`.
- Data read APIs currently start in `sdn-server/internal/api/data.go` and `sdn-server/internal/api/catalog.go`.
- Authenticated publish APIs currently start in `sdn-server/internal/api/publish.go`.
- Publication log APIs currently start in `sdn-server/internal/api/log.go`.
- FlatSQL persistence currently starts in `sdn-server/internal/storage/flatsql.go`.
- PNM queue and policy currently start in `sdn-server/internal/pubsub/tipqueue.go` and `sdn-server/internal/pubsub/config.go`.
- EPM directory exchange currently starts in `sdn-server/internal/node/epm_exchange_notifee.go` and `sdn-server/internal/directory/`.
- Module-delivery client flow currently starts in `sdn-js/src/module-delivery.ts` and `sdn-js/src/ui/runtime/live-delivery.ts`.
- Module marketplace/listing client logic currently starts in `sdn-js/src/ui/runtime/marketplace-source.ts`, `sdn-js/src/ui/runtime/plg-listings.ts`, and `sdn-js/src/storefront/`.
- Provider/listing server logic currently starts in `sdn-server/internal/storefront/` and the module-delivery handlers wired from `sdn-server/cmd/spacedatanetwork/main.go`.

## Target Information Architecture

- `#/data`: standards catalog, supported `spacedatastandards.org` version, enabled schemas, storage totals, replication health, recent publication heads.
- `#/data/browse`: schema-aware record explorer backed by FlatSQL, with standard/schema filters, time/object filters, and verification status.
- `#/data/publish`: authenticated upload wizard for FlatBuffer, JSON, or NDJSON that validates, converts to canonical FlatBuffer, stores shards, updates log heads, and emits PNMs by ruleset.
- `#/data/rules`: publish, pin, fetch, announce, retention, and FlatSQL indexing rules per standard/schema.
- `#/data/streams`: PNM inbox, publication log heads, pin decisions, verification errors, and replay controls.
- `#/modules`: PLG-derived module marketplace with provider trust evidence, price/license terms, and discovery health.
- `#/modules/installed`: local loaded modules, grants, runtime status, and SDK compatibility.
- `#/modules/delivery`: live delivery timeline showing provider discovery, challenge, grant, encrypted CID fetch, unwrap, decrypt, SDK load, and invocation result.
- `#/modules/grants`: purchased, issued, revoked, and expired grants.
- `#/modules/publish`: signed/encrypted WASM upload wizard that creates or imports PLG listings and publishes encrypted artifacts.

## Default Ruleset Model

- Authenticated writes only. Anonymous data publishing and module publishing are disabled by default.
- Signature required before network announcement. Unsigned PNM, PLG, STF, EPM, and provider claims are rejected.
- Default inbound pinning is conservative: `autoFetch=false`, `autoPin=false`, `ttl=24h`.
- Auto-fetch and auto-pin may be enabled by trusted source EPM, trust level, schema, purchase/grant, or explicit user action.
- Large imports emit one signed log-head update and one PNM for that update, not one PNM per row.
- Each published content shard has a real IPFS CID or an explicitly named non-IPFS content hash. Do not label SHA-256 hex digests as CIDs.
- Stable references resolve through signed log heads or manifests, not mutable file CIDs.

## Per-Standard Storage Pattern

- Organize records by canonical standard/schema family from `spacedatastandards.org`.
- Use per-standard FlatSQL tables for high-value query paths. Use `REC` only for heterogeneous import/export bundles.
- OMM default partition: `standard=OMM`, `epoch_day=YYYY-MM-DD`, `object_key=international_designator || norad_id || object_name`, `segment_version=monotonic or timestamp`.
- OMM shard contents: immutable FlatBuffer segment for one epoch day and object key. Appending data creates a new immutable segment and updates the signed log head.
- OMM PNM trigger: emit one PNM referencing the new signed head and new segment CID/hash after the shard is validated and indexed.
- All other standards should start with catalog-derived default partitions: time-bearing records partition by epoch/day or validity interval; object-bearing records add object key; static metadata records partition by entity id; binary payload records partition by payload type and digest.
- Generate the full per-standard ruleset table from published `spacedatastandards.org` metadata once the upstream schema package exposes indexable field metadata.

## Security And Trust Requirements

- Verify PNM/log-head/STF/PLG signatures against keys resolved from trusted EPMs before indexing as trusted data.
- Keep node identity and user access identity separate. Node EPMs identify provider/network nodes; user EPMs authorize administration and purchases.
- Use secp256k1 identity public keys for PeerID/provider discovery where required by the node identity contract.
- Use signing keys for record signatures and encryption keys for content-key wrapping. Do not reuse raw ECDH output directly; use HKDF with context and authenticated encryption.
- Store encrypted fields as ciphertext plus metadata. Index only plaintext fields explicitly allowed by the ruleset or encrypted search tokens explicitly produced by the schema policy.
- Never publish mnemonics, xprivs, private keys, or unwrapped content keys.
- Reject PLG/STF records that do not bind artifact hash, encrypted CID/hash, provider identity, schema/version, timestamp/nonce, and signature.

### Task 1: Publish The Upstream Schema And Documentation Contract

**Files:**
- Modify: `../spacedatastandards.org` canonical schema/docs files for `EPM`, `PNM`, `STF`, `PLG`, publication log heads, encryption metadata, and per-standard index metadata.
- Modify after upstream release: `sdn-js/package.json`
- Modify after upstream release: `sdn-server/go.mod`
- Modify after upstream release: `sdn-server/go.sum`
- Test: `sdn-js/src/schemas.test.ts`
- Test: `sdn-server/internal/sds/registry_test.go`
- Test: `sdn-server/internal/sds/roundtrip_test.go`

- [ ] **Step 1: Define upstream signing semantics**

Document that signed EPM/STF/PLG/PNM records are signed over canonical bytes with signature and mutable timestamp fields omitted only when the schema explicitly defines those fields as the signature envelope. The verifier must reconstruct the same canonical bytes before verification.

- [ ] **Step 2: Define publication log-head semantics**

Document immutable shard records, signed log heads, previous-head links, schema id, producer EPM id, timestamp, segment CID/hash, and revocation/replacement behavior. The schema name must not conflict with canonical `PLG`.

- [ ] **Step 3: Define data marketplace ownership**

Document that data listings are `STF` records and module listings are `PLG` records. `PLG` remains one canonical listing per `PLUGIN_ID + VERSION`.

- [ ] **Step 4: Define encryption metadata**

Document per-field encryption metadata, content-key wrapping, allowed plaintext indexes, encrypted search tokens, and the first homomorphic aggregate extension scope.

- [ ] **Step 5: Publish upstream packages**

Release `spacedatastandards.org` JavaScript and Go bindings. Record the published version in the implementation PR.

- [ ] **Step 6: Consume the published version here**

Run:

```bash
cd /Users/tj/software/space-data-network
npm --prefix sdn-js install spacedatastandards.org@latest
cd sdn-server && go get github.com/DigitalArsenal/spacedatastandards.org/lib/go@latest && go mod tidy
```

- [ ] **Step 7: Verify bindings**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run src/schemas.test.ts
cd /Users/tj/software/space-data-network/sdn-server && go test ./internal/sds -count=1
```

- [ ] **Step 8: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-js/package.json sdn-js/package-lock.json sdn-server/go.mod sdn-server/go.sum
git commit -m "Update Space Data Standards bindings"
```

### Task 2: Add SDN-Only Data And Modules Menu Shells

**Files:**
- Modify: `sdn-js/ui/src/upstream-webui/overrides/navigation/NavBar.js`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/bundles/routes.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/data/DataPage.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/data/DataBrowsePage.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/data/DataPublishPage.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/data/DataRulesPage.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/data/DataStreamsPage.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/modules/ModulesPage.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/modules/ModuleDeliveryPage.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/modules/ModuleGrantsPage.js`
- Create: `sdn-js/ui/src/upstream-webui/overrides/modules/ModulePublishPage.js`
- Modify: `sdn-js/src/ui/upstream-webui/branding.test.ts`
- Test: `sdn-js/src/ui/upstream-webui/branding.test.ts`

- [ ] **Step 1: Add tests for SDN-only navigation**

Assert that root SDN routes include `directory`, `data`, and `modules`, and assert that no `/webui` route or diagnostics behavior is changed by these entries.

- [ ] **Step 2: Add left-nav entries**

Use IPFS WebUI styling: dark navy sidebar, filled 46px icons, uppercase labels, current active state, and existing spacing. Add `DATA` and `MODULES` only to the SDN shell.

- [ ] **Step 3: Add route shells**

Create route shells with real headings, empty states, and runtime adapter loading states. Do not wire fake counts or demo data.

- [ ] **Step 4: Verify UI route build**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js
npx vitest run src/ui/upstream-webui/branding.test.ts
npm run build:ui
```

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-js/ui/src/upstream-webui/overrides sdn-js/src/ui/upstream-webui/branding.test.ts
git commit -m "Add SDN data and modules navigation"
```

### Task 3: Align Data Runtime Adapters And Server APIs

**Files:**
- Create: `sdn-js/src/ui/runtime/data.ts`
- Create: `sdn-js/src/ui/runtime/data.test.ts`
- Modify: `sdn-js/src/ui/runtime/server-adapter.ts`
- Modify: `sdn-js/src/ui/runtime/local-adapter.ts`
- Modify: `sdn-js/src/ui/runtime/helia-directory.ts`
- Modify: `sdn-server/internal/api/data.go`
- Modify: `sdn-server/internal/api/catalog.go`
- Test: `sdn-js/src/ui/runtime/data.test.ts`
- Test: `sdn-server/internal/api`

- [ ] **Step 1: Define shared data adapter types**

Expose catalog summary, schema capabilities, query request, query result, publication head, ruleset, pin decision, and verification status from `sdn-js/src/ui/runtime/data.ts`.

- [ ] **Step 2: Add server adapter methods**

Map UI calls to `GET /api/v1/catalog`, `GET /api/v1/data/query/{schema}`, `GET /api/v1/log/heads`, and ruleset endpoints added in later tasks.

- [ ] **Step 3: Add Helia-compatible fallback behavior**

For browser-only Helia clients, support local imported records, local FlatSQL/WASM indexes when available, and remote signed head discovery. Return explicit capability flags when server-only operations are unavailable.

- [ ] **Step 4: Add server query endpoint parity**

Add `GET /api/v1/data/query/{schema}` if missing, with schema allowlist validation, pagination, sorting, and filter parsing backed by FlatSQL.

- [ ] **Step 5: Verify adapters and API**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run src/ui/runtime/data.test.ts src/ui/runtime/server-adapter.test.ts src/ui/runtime/local-adapter.test.ts
cd /Users/tj/software/space-data-network/sdn-server && go test ./internal/api -count=1
```

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-js/src/ui/runtime sdn-server/internal/api
git commit -m "Add shared data runtime adapter"
```

### Task 4: Implement Data Publish, Rulesets, And Pinning Defaults

**Files:**
- Modify: `sdn-server/internal/api/publish.go`
- Modify: `sdn-server/internal/api/pinning.go`
- Modify: `sdn-server/internal/pubsub/config.go`
- Modify: `sdn-server/internal/pubsub/tipqueue.go`
- Create: `sdn-server/internal/storage/data_rules.go`
- Create: `sdn-server/internal/storage/data_rules_test.go`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/data/DataPublishPage.js`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/data/DataRulesPage.js`
- Test: `sdn-server/internal/api`
- Test: `sdn-server/internal/pubsub`
- Test: `sdn-server/internal/storage`

- [ ] **Step 1: Persist rulesets**

Store per-schema rules for authentication, validation, partitioning, indexing, PNM emission, auto-fetch, auto-pin, TTL, and trust requirements in FlatSQL-backed config tables.

- [ ] **Step 2: Add default rules**

Seed defaults: authenticated writes, signature required, manual fetch, manual pin, 24h inbound TTL, no row-level PNM spam, and explicit trusted-source allow rules.

- [ ] **Step 3: Add upload wizard behavior**

Accept FlatBuffer directly. Accept JSON/NDJSON only when conversion to canonical FlatBuffer succeeds against the selected schema or detected file metadata.

- [ ] **Step 4: Add rules UI**

Show schema, partition rule, index rule, PNM trigger, pin/fetch policy, trust requirement, and enabled state. Keep controls disabled when the runtime adapter reports read-only mode.

- [ ] **Step 5: Verify rules and publish flow**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
go test ./internal/api ./internal/pubsub ./internal/storage -run 'Publish|Pin|Rule|TipQueue' -count=1
cd /Users/tj/software/space-data-network/sdn-js
npx vitest run src/ui/runtime/data.test.ts
```

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/api sdn-server/internal/pubsub sdn-server/internal/storage sdn-js/ui/src/upstream-webui/overrides/data
git commit -m "Add data publishing rulesets"
```

### Task 5: Replace Mutable Data References With Immutable Shards And Signed Log Heads

**Files:**
- Create: `sdn-server/internal/storage/publication_log.go`
- Create: `sdn-server/internal/storage/publication_log_test.go`
- Modify: `sdn-server/internal/api/log.go`
- Modify: `sdn-server/internal/api/publish.go`
- Modify: `sdn-server/internal/pubsub/tipqueue.go`
- Modify: `sdn-server/internal/sds/validator.go`
- Test: `sdn-server/internal/storage/publication_log_test.go`
- Test: `sdn-server/internal/api`
- Test: `sdn-server/internal/pubsub`

- [ ] **Step 1: Define local publication log storage**

Persist schema id, producer EPM id, previous head id, head CID/hash, segment CID/hash, signature, timestamp, source peer id, verification status, and pin status.

- [ ] **Step 2: Generate real content identifiers**

When content is placed in IPFS, store CID. When content is not placed in IPFS, store an explicitly named hash field. Do not use an IPFS CID column for SHA-256-only identifiers.

- [ ] **Step 3: Emit PNMs for log-head updates**

Emit one PNM after the new shard and signed head are persisted and verified. The PNM references the log head and segment identifier.

- [ ] **Step 4: Add replay and verification**

Allow a node to fetch a signed head, verify signature against EPM, fetch immutable shards according to pin rules, and materialize FlatSQL indexes.

- [ ] **Step 5: Verify log-head behavior**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
go test ./internal/storage ./internal/api ./internal/pubsub -run 'PublicationLog|LogHead|PNM|TipQueue' -count=1
```

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/storage sdn-server/internal/api sdn-server/internal/pubsub sdn-server/internal/sds
git commit -m "Add signed data publication log heads"
```

### Task 6: Implement FlatSQL Distribution And Encryption Controls

**Files:**
- Modify: `sdn-server/internal/storage/flatsql.go`
- Create: `sdn-server/internal/storage/flatsql_replication.go`
- Create: `sdn-server/internal/storage/flatsql_replication_test.go`
- Create: `sdn-server/internal/storage/encrypted_fields.go`
- Create: `sdn-server/internal/storage/encrypted_fields_test.go`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/data/DataBrowsePage.js`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/data/DataStreamsPage.js`
- Test: `sdn-server/internal/storage`

- [ ] **Step 1: Add schema-derived index metadata**

Read allowed plaintext indexes and encrypted-token indexes from published schema metadata or local rules generated from that metadata.

- [ ] **Step 2: Store encrypted fields safely**

Persist ciphertext, encryption metadata, key id, and AAD context. Do not index ciphertext unless the rule explicitly defines an encrypted token.

- [ ] **Step 3: Replicate from signed heads**

Materialize FlatSQL records by replaying verified publication heads and shards. Mark records untrusted until signatures and source trust checks pass.

- [ ] **Step 4: Add UI verification states**

Show trusted, untrusted, signature failed, missing shard, pinned, and expired states in browse and streams views.

- [ ] **Step 5: Verify encrypted field behavior**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
go test ./internal/storage -run 'FlatSQL|Encrypted|Replication' -count=1
```

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/storage sdn-js/ui/src/upstream-webui/overrides/data
git commit -m "Add FlatSQL replication and encrypted field controls"
```

### Task 7: Build Data Storefront Support With STF

**Files:**
- Modify: `sdn-server/internal/storefront/`
- Create: `sdn-server/internal/storefront/stf_data.go`
- Create: `sdn-server/internal/storefront/stf_data_test.go`
- Modify: `sdn-js/src/ui/runtime/store-search.ts`
- Modify: `sdn-js/src/ui/runtime/store-search.test.ts`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/data/DataPage.js`
- Test: `sdn-server/internal/storefront`
- Test: `sdn-js/src/ui/runtime/store-search.test.ts`

- [ ] **Step 1: Index verified STF records**

Index data listings from signed `STF` records only. Include schema, producer EPM, content/log-head reference, pricing terms, sample policy, license terms, and trust state.

- [ ] **Step 2: Bind purchases to key grants**

When a user buys data, issue or fetch a grant that wraps the content key or field key to the buyer encryption key from their EPM.

- [ ] **Step 3: Display storefront data separately from modules**

Keep data listings under `Data`. Keep modules under `Modules`. Shared payment infrastructure can remain in `sdn-server/internal/storefront/`.

- [ ] **Step 4: Verify STF behavior**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server && go test ./internal/storefront -run 'STF|Data|Store' -count=1
cd /Users/tj/software/space-data-network/sdn-js && npx vitest run src/ui/runtime/store-search.test.ts
```

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/storefront sdn-js/src/ui/runtime sdn-js/ui/src/upstream-webui/overrides/data
git commit -m "Add STF data storefront indexing"
```

### Task 8: Build Modules Marketplace And Delivery UI From PLG

**Files:**
- Modify: `sdn-js/src/ui/runtime/marketplace-source.ts`
- Modify: `sdn-js/src/ui/runtime/plg-listings.ts`
- Modify: `sdn-js/src/ui/runtime/live-delivery.ts`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/modules/ModulesPage.js`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/modules/ModuleDeliveryPage.js`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/modules/ModuleGrantsPage.js`
- Test: `sdn-js/src/ui/runtime/marketplace-source.test.ts`
- Test: `sdn-js/src/ui/runtime/plg-listings.test.ts`
- Test: `sdn-js/src/ui/runtime/live-delivery.test.ts`

- [ ] **Step 1: Require verified PLG-derived listings**

Render module listings only when PLG signature, plugin id, version, provider identity, content hash, encrypted artifact reference, payment terms, and timestamp/nonce verification pass.

- [ ] **Step 2: Show provider evidence**

Display provider EPM, provider PeerID, provider public key, discovery source, DHT/source evidence, protocol support, and last successful grant.

- [ ] **Step 3: Show delivery timeline**

Reuse the existing live delivery events: discovery, challenge, grant, encrypted CID fetch, hash verification, key unwrap, decrypt, SDK load, and invoke result.

- [ ] **Step 4: Verify marketplace UI runtime**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js
npx vitest run src/ui/runtime/marketplace-source.test.ts src/ui/runtime/plg-listings.test.ts src/ui/runtime/live-delivery.test.ts
npm run build:ui
```

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-js/src/ui/runtime sdn-js/ui/src/upstream-webui/overrides/modules
git commit -m "Add PLG module marketplace UI"
```

### Task 9: Add Signed And Encrypted Module Publish Flow

**Files:**
- Modify: `sdn-server/internal/storefront/`
- Modify: `sdn-server/internal/node/licensing_bootstrap.go`
- Modify: `sdn-server/internal/node/licensing_bootstrap_test.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `sdn-js/ui/src/upstream-webui/overrides/modules/ModulePublishPage.js`
- Test: `sdn-server/internal/storefront`
- Test: `sdn-server/internal/node`
- Test: `sdn-js/src/module-delivery.test.ts`
- Test: `sdn-js/src/module-delivery-sdk-compat.test.ts`

- [ ] **Step 1: Define publish envelope validation**

Require signed PLG, encrypted WASM artifact, artifact hash, encrypted content-key material, provider identity proof, supported runtime/sdk version, payment policy, timestamp, nonce, and revocation reference.

- [ ] **Step 2: Store encrypted artifacts only**

Pin/store encrypted bytes and wrapped key material. Do not persist plaintext WASM bundles in the provider store.

- [ ] **Step 3: Wire licensing provider**

Make `sdn.spaceaware.io` able to issue grants for published modules through the existing module-delivery protocol and licensing runtime.

- [ ] **Step 4: Add module publish UI**

Upload WASM, validate signature/encryption envelope, preview PLG, show provider identity, and publish. Keep this authenticated and disabled for runtimes without server capability.

- [ ] **Step 5: Verify delivery compatibility**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
go test ./internal/storefront ./internal/node -run 'Module|PLG|Licens|Grant' -count=1
cd /Users/tj/software/space-data-network/sdn-js
npx vitest run src/module-delivery.test.ts src/module-delivery-sdk-compat.test.ts
```

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/storefront sdn-server/internal/node sdn-server/cmd/spacedatanetwork sdn-js/ui/src/upstream-webui/overrides/modules
git commit -m "Add encrypted module publishing"
```

### Task 10: Add End-To-End Verification And Deployment Gates

**Files:**
- Modify: `tests/isomorphic/`
- Modify: `plugin-demo/tests/`
- Modify: `scripts/`
- Modify: `.github/workflows/`
- Test: full local CI

- [ ] **Step 1: Add end-to-end data publish test**

Start a local node, publish an OMM FlatBuffer, write a signed log head, emit a PNM, replay into FlatSQL, and query the record from the `Data` adapter.

- [ ] **Step 2: Add end-to-end module publish test**

Publish an encrypted WASM module envelope, verify PLG indexing, issue a grant, fetch encrypted content, unwrap/decrypt locally, load through the SDK, and invoke one method.

- [ ] **Step 3: Add browser smoke coverage**

Use the in-app browser or Playwright against a running local server to verify `#/data`, `#/data/publish`, `#/data/rules`, `#/modules`, and `#/modules/delivery` render without console errors.

- [ ] **Step 4: Run full verification**

Run:

```bash
cd /Users/tj/software/space-data-network
npm test
cd sdn-server && go test ./... -count=1
cd ../sdn-js && npm test && npm run build
```

Use the repo pre-push hook as the final local gate before deployment.

- [ ] **Step 5: Deploy**

Run the existing deployment flow:

```bash
cd /Users/tj/software/space-data-network
tmp_config="$(mktemp /tmp/sdn-deploy.XXXXXX.yaml)"
cat > "$tmp_config" <<'YAML'
full_nodes:
  - ip: 104.131.11.220
    region: nyc3
    name: sdn-spaceaware
YAML
./deployment/scripts/deploy.sh -c "$tmp_config" -k "$HOME/.ssh/id_ed25519" -u root -b deploy full
rm -f "$tmp_config"
```

- [ ] **Step 6: Verify live deployment**

Run:

```bash
curl -fsSI https://sdn.spaceaware.io/
curl -fsSI https://sdn.spaceaware.io/admin/
ssh -o BatchMode=yes root@104.131.11.220 'systemctl show -p ActiveState -p SubState -p MainPID space-data-network.service; systemctl is-active spacedatanetwork.service || true'
```

Expected: `/` returns 200, `/admin/` redirects to login when unauthenticated, `space-data-network.service` is active/running, and the old duplicate `spacedatanetwork.service` is inactive.

## Review Decisions Needed Before Implementation

- Confirm the upstream names for publication log-head schemas so they do not collide semantically with `PLG`.
- Confirm whether first release supports JSON ingestion for all schemas or only FlatBuffer plus OMM JSON/NDJSON.
- Confirm the first homomorphic encryption aggregate scope. Recommended first scope: count and sum over explicitly quantized numeric fields only.
- Confirm first payment entitlement path. Recommended first path: grant records tied to EPM encryption keys, with Stripe or on-chain payment adapters behind the same storefront interface.
- Confirm whether browser-only Helia clients must support local FlatSQL immediately or whether the first release can expose read-only imported-record search with a server-backed full implementation.

## Initial Release Slice

- Ship `Data` and `Modules` menu shells with real runtime capability detection.
- Ship `Data` catalog, browse, upload, and rules views over server-backed APIs.
- Ship immutable shard plus signed log-head publication for OMM first.
- Ship PNM emission for log-head updates and replay into FlatSQL.
- Ship PLG-derived module marketplace view and delivery timeline from existing module-delivery events.
- Keep encrypted data storefront, homomorphic aggregates, and module publish uploads behind disabled capability flags until upstream schemas and verification gates are complete.
