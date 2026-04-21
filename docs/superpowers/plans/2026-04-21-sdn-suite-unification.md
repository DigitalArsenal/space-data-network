# SDN Suite Unification Plan

Status: canonical planning document
Date: 2026-04-21
Supersedes: every file previously under `docs/superpowers/plans/` and `docs/superpowers/specs/`

## Outcome

Build Space Data Network as one suite, modeled after IPFS:

- a desktop app
- a server daemon
- a JavaScript client/runtime

All three must share the maximum possible code. The browser-hosted and desktop-hosted experiences must use the same SDN UI core and the same remote-control contract, and either host must be able to control any SDN server that authorizes the current user.

This plan also makes `spacedatastandards.org` a pinned suite dependency, defines an in-place network update path for both core code and standards updates, and replaces the single hardcoded SDN version advertisement with a versioned multi-flag broadcast window that keeps at least the last five advertised flags live during rollout.

## Hard Decisions

1. `sdn-js` becomes the single shared control-plane and UI core.
   - The browser app and desktop app are hosts around the same `sdn-js` runtime, controllers, auth client, and workspace UI.
   - `sdn-server` serves that same built UI at `/`.

2. `/`, `/webui`, and `/admin` stay separate product surfaces.
   - `/` is the authenticated SDN UI and the suite's main dashboard.
   - The `/` dashboard UX baseline is the upstream IPFS WebUI itself, not a bespoke SDN shell.
   - SDN starts from an exact WebUI copy and replaces data providers incrementally, beginning with peers.
   - `/webui` is the authenticated full upstream IPFS WebUI with its current upstream capabilities retained.
   - `/admin` is auth and admin-only workflow space.

3. Standards versioning becomes explicit and suite-wide.
   - One pinned `spacedatastandards.org` version is selected for each suite release.
   - JS and Go must both consume that same pinned version.
   - The pin must be enforced by tooling, build scripts, and CI.

4. Network updates are first-class product behavior.
   - Nodes must be able to discover, download, verify, stage, and apply signed suite updates in place.
   - The same mechanism must support updates to core suite artifacts and standards-version bumps.

5. Node advertising becomes versioned and backward-tolerant.
   - Advertising cannot be one hardcoded string.
   - Each node publishes a versioned flag set and keeps at least the latest five flags broadcast so lagging nodes have a compatibility window.

## Current State

The codebase is partway there, but the contract is fragmented:

- Shared browser/server UI work already exists in `sdn-js/ui/src/bootstrap.ts` and `sdn-js/src/ui/runtime/*`.
- The desktop subtree still behaves like upstream IPFS Desktop and serves `webui/build` directly from `desktop/src/webui/index.js`.
- `spacedatastandards.org` is pinned at `1.85.0` in both `sdn-js/package.json` and `sdn-server/go.mod`, but repo enforcement does not currently treat SDS as the suite's canonical version contract.
- Version consistency tooling in `scripts/check-version-consistency.js` checks FlatBuffers and HD wallet versions, but not SDS or suite release metadata.
- Full-node packaging and deployment currently ship `sdn-js/ui/dist` and `webui/build` as separate assets via:
  - `deployment/scripts/deploy.sh`
  - `deployment/scripts/package-linux-vm-bundle.sh`
- Node and edge version advertisement are still hardcoded as `spacedatanetwork/1.0.0` in:
  - `sdn-server/internal/node/node.go`
  - `sdn-server/cmd/spacedatanetwork/main.go`
  - `sdn-server/cmd/spacedatanetwork-edge/main.go`
- The repo still exposes `spacedatastandards-site` as a local product script in `package.json`, which conflicts with the product-surface contract.

## Target Architecture

### 1. Shared Suite Core

`sdn-js` owns:

- remote server auth client
- shared session model
- runtime-config loading
- workspace controllers
- observed-peer model
- provider discovery
- update status UI
- browser-hosted shell
- desktop-hosted shell

`sdn-server` owns:

- authenticated serving of the SDN UI at `/`
- authenticated serving of the full upstream IPFS WebUI at `/webui`
- admin/auth flows at `/admin`
- server APIs for auth, control, update status, node version status, and rollout windows
- provider-side update application

`desktop` owns only host-specific concerns:

- Electron lifecycle
- tray/menu/window shell
- filesystem and installer integration
- OS-level auto-update bootstrap
- local daemon management when running a colocated node

The desktop must stop owning a separate web UI product. It should host the same built SDN UI bundle from `sdn-js` as the main dashboard, while preserving `/webui` as the full authenticated upstream IPFS dashboard rather than a reduced or re-skinned fork.

The main SDN dashboard at `/` should therefore be implemented as an SDN-mode distribution of upstream IPFS WebUI:

- same route structure
- same component and layout baseline
- same visual language
- same interaction model
- SDN-specific data providers swapped in behind the UI

The first mandatory swap is peers:

- peer tables, counts, and related summaries at `/` must show SDN peers only
- those peers must come from SDN discovery, observed-peer evidence, server registry state, and SDN trust metadata
- `/webui` continues to show raw upstream IPFS/Kubo peer state untouched

### 2. Control Model

Both browser and desktop control servers through the same server-target abstraction:

- authenticated session against a remote SDN server
- permission-resolved capabilities returned by the server
- same workspace model regardless of host
- local-only capabilities injected as optional host adapters

The rule is:

- if the user is authorized on the target server, browser and desktop can both control it
- desktop may add extra local OS affordances, but it may not fork the core SDN control flow

### 3. Version Model

Add a single tracked manifest at repo root:

- `suite.versions.json`

This file becomes the canonical source for:

- suite release version
- pinned `spacedatastandards.org` version
- pinned `hd-wallet-wasm` and `hd-wallet-ui` versions
- pinned IPFS WebUI build identifier
- advertised SDN protocol/version flags
- update channel metadata

Generated outputs should be derived from it for Go and TS consumption:

- `sdn-js/src/version-info.generated.ts`
- `sdn-server/internal/versioninfo/generated.go`

Nothing else should hardcode suite or advertising version strings after this migration.

### 4. Update Model

We need one signed release and update pipeline for the whole suite.

Use two layers:

1. Suite release manifest
   - describes the suite version, SDS pin, artifact hashes, compatibility policy, and rollout flags
   - distributed as signed metadata over the network and via normal release channels

2. Host-specific installers/applicators
   - server applies daemon/UI/WebUI/auxiliary artifact updates in place
   - desktop applies desktop-shell updates through Electron release flow, then syncs the shared SDN UI/assets bundle
   - browser-only clients do not self-update binaries, but they consume the new hosted UI and new pinned protocol metadata automatically

The update path must support:

- core code updates
- standards-version bumps
- staged rollout
- rollback to the previous known-good suite version
- signature verification before staging
- hash verification before activation

### 5. Versioned Node Advertising

Replace the single `spacedatanetwork/1.0.0` advertisement with a versioned flag ring:

- each suite release emits one canonical flag identifier
- each node advertises the current flag plus the previous four flags
- discovery and status APIs report:
  - current suite version
  - current SDS pin
  - current advertisement flag
  - compatibility window of the last five flags

This lets older nodes discover newer ones during rollout while making the active target version explicit.

## Execution Plan

### Phase 1. Canonical Version Source

Create `suite.versions.json` and make it authoritative.

Files:

- `suite.versions.json`
- `package.json`
- `sdn-js/package.json`
- `sdn-server/go.mod`
- `scripts/check-version-consistency.js`
- `README.md`

Tasks:

- define a root manifest with suite, SDS, wallet, WebUI, and advertisement versions
- remove hardcoded duplicate version declarations wherever possible
- extend the consistency checker to fail when:
  - `sdn-js/package.json` SDS pin diverges from the manifest
  - `sdn-server/go.mod` SDS pin diverges from the manifest
  - wallet versions diverge from the manifest
  - advertised flags diverge from generated outputs
- document the release/update contract in the README
- remove `spacedatastandards-site` as an active root script surface

Exit criteria:

- one tracked manifest controls all suite-visible version pins
- CI fails on SDS drift

### Phase 2. Shared UI and Control-Plane Consolidation

Make `sdn-js` the sole SDN UI/runtime core for browser, hosted server, and desktop.

Files:

- `sdn-js/ui/src/bootstrap.ts`
- `sdn-js/src/ui/runtime/*`
- `sdn-js/ui/vite.config.mts`
- `desktop/src/index.js`
- `desktop/src/webui/index.js`
- `desktop/src/tray.js`
- `desktop/src/preload*`
- `scripts/admin-dev.sh`
- `sdn-server/cmd/spacedatanetwork/main.go`

Tasks:

- formalize a host adapter boundary:
  - browser/server host adapter
  - desktop host adapter
  - local embedded-node adapter
- replace the current bespoke `sdn-js` dashboard shell with an upstream-WebUI-based SDN dashboard shell at `/`
- keep the `/webui` bundle untouched and upstream-aligned
- introduce an SDN-mode data-provider layer for the WebUI baseline, starting with peers
- ensure the peer views at `/` are sourced from SDN peer discovery and trust state, not Kubo swarm peers
- move any remaining desktop-specific SDN logic out of Electron views and into `sdn-js`
- change desktop to serve or embed the built `sdn-js` UI for `/`
- keep `/webui` as the full upstream IPFS surface, not the desktop’s primary shell
- ensure browser and desktop can connect to arbitrary authorized servers through the same server-target model

Exit criteria:

- the same UI bundle and controllers drive browser-hosted and desktop-hosted SDN control
- the `/` dashboard is recognizably upstream IPFS WebUI in structure and behavior
- the `/` peer surfaces are SDN-only
- desktop no longer behaves as a separate UI product fork

### Phase 3. Auth and Route Contract Stabilization

Lock the user-facing route behavior around the shared suite contract.

Files:

- `sdn-server/internal/auth/*`
- `sdn-server/cmd/spacedatanetwork/main.go`
- `sdn-js/src/transport/auth.ts`
- `sdn-js/src/ui/runtime/server-adapter.ts`
- `sdn-js/ui/src/bootstrap.ts`

Tasks:

- require auth for `/`, `/webui`, and `/admin`
- route unauthenticated users only through the wallet-backed login surface
- after auth, route:
  - standard users to `/`
  - authorized admins to `/admin` when explicitly requested
  - authenticated users to `/webui` only if permitted
- keep `xpub` off the wire and use signing-key-based challenge/response only

Exit criteria:

- there is one coherent dev/prod route contract
- browser and desktop sign in the same way against the same server APIs

### Phase 4. In-Place Update System

Add a suite-native update channel using signed manifests and staged artifact activation.

Files:

- `sdn-server/internal/license/*`
- `sdn-server/internal/modulert/*`
- `sdn-server/internal/node/*`
- `sdn-server/internal/api/*`
- `sdn-js/src/module-delivery.ts`
- `sdn-js/src/storefront/*`
- `desktop/src/auto-updater/index.js`
- `deployment/scripts/deploy.sh`
- `deployment/scripts/package-linux-vm-bundle.sh`
- new `updates/` documentation and release tooling

Tasks:

- define the signed suite update manifest format upstream where schema is needed
- use existing encrypted distribution primitives where they fit, but do not overload PLG for suite-core rollout without upstream schema support
- add server endpoints and background jobs for:
  - checking update channels
  - downloading and verifying artifacts
  - staging updates
  - activating updates with rollback support
- add desktop wiring so Electron shell updates and shared SDN bundle updates stay in sync
- add admin UI views for:
  - current version
  - available update
  - staged version
  - rollback target
  - SDS pin delta

Exit criteria:

- a node can update core code and SDS pin in place from signed release metadata
- rollback to the prior suite version is possible

### Phase 5. Versioned Advertising and Compatibility Window

Implement the five-flag broadcast window.

Files:

- `sdn-server/internal/node/node.go`
- `sdn-server/cmd/spacedatanetwork/main.go`
- `sdn-server/cmd/spacedatanetwork-edge/main.go`
- `sdn-js/src/edge-discovery.ts`
- `sdn-js/src/discovery.ts`
- `sdn-js/src/ui/runtime/types.ts`
- `sdn-js/src/ui/runtime/network*`

Tasks:

- replace hardcoded `spacedatanetwork/1.0.0` strings with generated suite version info
- define advertisement flag derivation from the suite manifest
- publish current flag plus previous four flags
- update relay status and node status responses to return:
  - suite version
  - SDS version
  - advertised flags
  - preferred/current flag
- update discovery code to consume compatibility windows rather than a single opaque version string

Exit criteria:

- new nodes and slightly older nodes can overlap for at least five advertised versions
- operators can see exactly which version/flag a node is on

### Phase 6. Release and Verification Pipeline

Make the suite releasable as one product.

Files:

- `.github/workflows/*`
- `scripts/check-version-consistency.js`
- `scripts/subtree-update.sh`
- `deployment/scripts/*`
- `desktop/package.json`
- `package.json`

Tasks:

- make the release job read `suite.versions.json`
- build and sign:
  - server bundle
  - desktop bundle
  - hosted SDN UI bundle
  - hosted IPFS WebUI bundle
- verify SDS pin consistency before any publish
- stop treating desktop and WebUI refreshes as disconnected manual chores
- convert subtree/submodule refresh scripts into inputs to the versioned release process rather than ad hoc repo state changes

Exit criteria:

- one release action produces coherent artifacts for desktop, daemon, and hosted UI
- published versions always describe the SDS pin they carry

## Upstream Dependencies

Some parts of this plan require upstream schema or packaging work and must not be implemented as repo-local shadow formats.

Needed upstream work:

- canonical schema for suite update metadata, if FlatBuffer-backed metadata is required
- any additions to marketplace/distribution metadata beyond current `PLG`
- published version identifiers from `spacedatastandards.org` consumed here as normal dependencies

Repo rule:

- no new local `.fbs` schema files for this plan unless they are first defined upstream and consumed through published bindings

## Risks

1. Desktop is still an IPFS Desktop subtree.
   - This is useful for acceleration, but it means we must isolate SDN host integration from upstream-owned behavior instead of scattering SDN assumptions throughout the subtree.

2. Update transport and update authority are easy to conflate.
   - Distribution can use the network.
   - Trust must come from signed release metadata, not from transport origin alone.

3. Standards updates can break runtime compatibility even when the suite code does not change.
   - SDS pin changes must be visible in rollout UI and rollback history.

4. Route/auth churn can destabilize local development if not verified against the single-server dev flow.
   - Dev must keep one visible server origin and one login flow.

## Verification Strategy

For each phase, require focused verification in the owned area:

- versioning:
  - `node scripts/check-version-consistency.js`
- JS/runtime/UI:
  - focused `vitest` runs
  - `cd sdn-js && npm run build`
- server/auth/routing:
  - focused Go tests under `sdn-server`
- IPFS WebUI hosting:
  - `cd webui && npm run build`
  - focused server route tests
- packaging/deploy:
  - dry-run or smoke packaging for:
    - `deployment/scripts/package-linux-vm-bundle.sh`
    - `deployment/scripts/deploy.sh`
- desktop:
  - Electron smoke tests around the shared hosted shell and update flow

## Immediate TODO List

1. Add `suite.versions.json` and generated Go/TS readers.
2. Extend `scripts/check-version-consistency.js` to enforce the SDS pin and advertised version data.
3. Remove `spacedatastandards-site` from root product scripts and docs.
4. Make desktop host the shared `sdn-js` SDN UI rather than its own forked primary surface.
5. Add suite version/status APIs and replace all hardcoded `spacedatanetwork/1.0.0` strings.
6. Design the signed suite update manifest and implement staged in-place update application.
7. Implement the five-flag advertisement retention window across full nodes, edge nodes, and discovery clients.

## Definition Of Done

This effort is complete when:

- browser, desktop, and server all use the same SDN UI/runtime core
- an authorized browser user and an authorized desktop user can both control the same server through the same control model
- the suite ships with one explicit pinned SDS version
- nodes can update in place from signed network-distributed release metadata
- nodes advertise the current suite flag plus the previous four flags
- the repo has one canonical planning document for this work
