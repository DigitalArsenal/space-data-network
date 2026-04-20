# Isomorphic SDN Admin Shell Design

## Goal

Replace the current inline `/admin` template with a single isomorphic SDN admin client shipped by `sdn-js`, hosted by `sdn-server` at `/admin`, and reusable in browser-only Helia environments without forking the product.

## Summary

The new admin surface is one product with two backend modes:

- `Local`
  - the browser runs the SDN UI against its own Helia/libp2p/`hd-wallet-wasm` backend
- `Server`
  - the same UI attaches to a selected `sdn-server` node and uses that node's admin/data APIs

This is not a reskin of the current inline admin page. The inline Go template in `sdn-server/internal/peers/admin.go` should be retired and replaced by a built client app from `sdn-js`.

The shell must:

- look and navigate like the IPFS admin surface, with a left-aligned icon rail and a top bar
- expose a top-bar backend switch for `Server` vs `Local`
- provide a `Connect Server` control that can attach the UI to any reachable node
- expose the current node identity, permission context, and current user/session state
- include a direct `IPFS Dashboard` link for the connected server's separate WebUI surface
- use `hd-wallet-ui` for wallet/account flows and make the account icon open that modal
- move sign-out into that account menu instead of a standalone page button

The shell must host five first-class product workspaces:

- `Directory`
- `Store`
- `Frontend`
- `Wallet`
- `Network`

## Current Constraints

### Existing admin implementation

The current `/admin` page is a single inline HTML/CSS/JS template in `sdn-server/internal/peers/admin.go`. It already contains:

- trust and peer registry management
- authenticated-user management
- node identity inspection
- basic frontend file CRUD
- a basic git import action
- wallet tab lazy-loading

That shape is acceptable as a temporary bootstrap, but it is the wrong long-term container for:

- a VS Code-like editor
- backend mode switching
- remote server selection
- a linked directory/store model
- reusable browser-only operation through `sdn-js`

### Existing backend capabilities

`sdn-server` already has:

- wallet-auth session endpoints
- current-user lookup
- authenticated-user admin APIs
- trust/peer/group/blocklist APIs
- node info APIs
- frontend file CRUD/upload APIs
- frontend git import
- plugin upload and plugin manifest endpoints
- separate IPFS WebUI hosting under `/webui`
- Docker build and publish infrastructure in `deployment/docker/*` and `.github/workflows/docker-publish.yml`

These are a useful starting point, but they do not yet define the full backend contract needed by the new shell.

## Decisions Already Locked In

- The product must remain isomorphic and live in `sdn-js`.
- `sdn-server` must host the same built client app at `/admin` instead of maintaining a separate admin UI.
- `sdn-server` must remain a publishable Docker-delivered product, and the new admin shell must ship inside that image.
- `Local` and `Server` are first-class runtime modes inside the same shell.
- The top bar must allow switching modes and selecting a remote server target.
- The admin shell must include a direct link to the separate IPFS dashboard surface.
- The store must not become a generic IPFS content browser.
- The store has two linked first-class catalogs:
  - `Modules`
  - `Data`
- Relationships between modules and data are informed by Space Data Standards metadata, and the standards will be extended upstream to formalize those linkages.
- `hd-wallet-wasm` and `hd-wallet-ui` are the identity source of truth for wallet actions, signatures, deterministic SSH material, address lookup, and the account modal.

## Non-Goals

- Do not merge arbitrary IPFS browsing into the SDN store.
- Do not preserve the inline Go template as the long-term admin product.
- Do not build a separate native app.
- Do not create a separate non-`sdn-js` admin frontend that later needs to be mirrored in browser mode.

## Product Architecture

### One app, two adapters

The admin shell is a client package in `sdn-js`, with shared:

- navigation shell
- state model
- workspace routing
- wallet integration
- store/directory rendering
- editor UX

The only major variation is the backend adapter.

#### `LocalAdapter`

Responsibilities:

- create and own the browser Helia/libp2p/SDN runtime
- use the browser wallet identity directly
- support local search, fetch, pin, and discovery operations that are feasible in browser mode
- maintain a browser-side workspace for the `Frontend` IDE

#### `ServerAdapter`

Responsibilities:

- attach to a selected `sdn-server` node
- use server admin/data APIs for node-managed operations
- surface that server's current auth user, trust level, permissions, and admin state
- proxy store, directory, pinning, frontend sync, and node-control actions through the selected server

The view layer must talk only to the adapter contract, never directly to Helia or raw server endpoints.

### Runtime context

The shell runtime owns a single `Active Backend Context` with:

- current mode: `local` or `server`
- selected server target, if any
- effective permission model
- current wallet identity
- current authenticated server user, if any
- current node descriptor
- feature availability map

Changing the active server target updates:

- displayed node identity
- admin capabilities
- authenticated user list
- trust policy controls
- IPFS dashboard destination
- available pin/publish/store operations

## Shell Layout

### Left icon rail

The shell should mirror the IPFS admin navigation pattern:

- compact vertical rail
- icon-first entries
- labels visible on larger layouts
- active-state highlight

Navigation entries:

- `Directory`
- `Store`
- `Frontend`
- `Wallet`
- `Network`
- `IPFS Dashboard`

`IPFS Dashboard` is an external navigation item:

- in `Server` mode it opens the connected node's `/webui`
- in `Local` mode it is disabled unless a server target is currently selected for reference

### Top bar

The top bar must include:

- active node label
- `Local | Server` mode switch
- `Connect Server` button
- connection status
- account/avatar button

The account button opens the wallet/account modal. That modal owns:

- account summary
- addresses
- vCard / identity card access
- signature/identity actions from `hd-wallet-ui`
- sign-out for the currently attached server session

## Workspace Design

### 1. Directory

`Directory` replaces the current flat peer-management table with an `Active Directory` model.

It should organize identities and nodes around:

- peer identity
- wallet/xpub identity
- blockchain addresses
- publisher/provider identity
- organization
- trust group
- advertised services and supported standards

Primary interactions:

- search by peer ID, xpub, address, handle, publisher, organization, or standard
- view current node trust context and group memberships
- inspect peer cards with identity, addresses, trust state, services, and discovery evidence
- inspect authenticated-user/admin lists for the currently selected server node
- inspect relationships between a publisher, its modules, and its published data

The directory is not just an ACL editor. It is the node/user/service relationship explorer for SDN.

### 2. Store

`Store` is a Steam-like SDN-native distribution surface with two linked first-class catalogs:

- `Modules`
- `Data`

#### Modules catalog

Shows canonical SDN module listings using SDS-native metadata, including:

- title
- publisher
- version
- description/tagline
- screenshots/banner when available
- pricing/payment metadata when standards support it
- listing state
- compatible standards/data relationships
- install/run/pin/download actions

#### Data catalog

Shows SDN-indexed data products, not arbitrary IPFS blobs. Items should expose:

- dataset title
- publisher/provider
- SDS record families and standards
- search facets informed by standard type and relation metadata
- pin/download/open actions
- related modules

#### Relationship model

The store must show relationships between modules and data. Example:

- an OMM dataset should surface compatible SGP4 propagators
- a module should show data standards it can operate on
- a data listing should show tools available from other users that can process it

The canonical long-term source of those relationships is `spacedatastandards.org`.

The design assumption is:

- current SDN metadata can provide a small initial relation layer where practical
- the standards will be extended upstream with formal linkage metadata so the store can derive compatibility from SDS definitions instead of bespoke UI-only logic

### 3. Frontend

`Frontend` becomes a VS Code-like browser IDE for the public frontend workspace.

Required UX:

- x-axis resizable file tree panel
- Monaco editor
- syntax highlighting by file type
- drag-and-drop upload into the file tree
- upload button in the file tree panel
- file create, rename, delete, and save
- open-file tabs or another explicit multi-file affordance
- dirty-state tracking

The workspace model must remain isomorphic.

#### Local mode workspace

In `Local` mode, the frontend workspace lives in browser-managed storage, preferably OPFS-backed persistent storage with a browser-friendly git layer.

That allows:

- the same IDE shell in browser-only mode
- offline-friendly editing
- git operations without needing server filesystem access
- explicit sync/export flows

#### Server mode workspace

In `Server` mode, the IDE attaches to the selected node's frontend publication workspace through admin APIs.

The user experience should still feel like one IDE, but the persistence target is the remote node.

### 4. Wallet

`Wallet` is the canonical identity workspace and directly reuses `hd-wallet-ui`.

It must expose:

- wallet/account management
- address derivation
- signatures
- node identity details
- vCard / identity-card editing
- address-based lookup helpers

The same modal/account flow used in the top bar should be reusable from this workspace.

### 5. Network

`Network` shows the currently selected backend and live network state:

- mode and connection state
- active server target
- node descriptor
- peer observations
- relay and transport details
- service availability
- current permission context

This is also where the user should inspect the currently connected server target before switching to another node.

## Remote Server Selection

`Connect Server` opens a server-target dialog.

Supported target sources:

- manually entered admin base URL
- a provider descriptor or discovered node descriptor
- discovered candidate nodes surfaced from the SDN runtime

Once selected, the UI attaches to that node and recomputes:

- who the authenticated user is on that node
- whether the current wallet identity has admin/trusted/standard access there
- which control actions are available
- which user list and permissions are relevant

This selection is part of runtime state, not hardcoded browser configuration.

## Git Integration

The `Frontend` workspace also exposes Git operations.

Required capability:

- interact with GitHub, GitLab, or custom git remotes
- use deterministic SSH key material derived from `hd-wallet-wasm`
- support clone/fetch/status/diff/commit/push/pull at the UI level

The SSH identity should be derived from the wallet identity rather than stored as an unrelated credential.

Implementation note:

- browser mode should prefer an isomorphic git stack
- server mode may add remote-node git helpers where node-managed repos are necessary
- the UX must stay consistent even if the underlying execution path differs by mode

## API and Backend Contract Changes

### `sdn-server`

The server must stop embedding the current admin template as the long-term UI and instead:

- serve the built `sdn-js` admin client at `/admin`
- continue serving `wallet-ui` assets
- continue serving the separate IPFS WebUI at `/webui`
- expose versioned JSON APIs needed by the admin client
- package the hosted admin shell inside the published `sdn-server` container image

Additional server APIs are needed beyond the current basic frontend CRUD. At minimum:

- frontend tree metadata suitable for a file explorer
- rename/move support
- richer save responses with validation metadata
- git status/log/diff/commit/push/pull or an equivalent repo-control contract
- store catalog endpoints for node-known modules/data when server-backed search is desired
- node-target/session APIs for remote attachment context

### Container distribution

`sdn-server` must continue to ship as a published container image.

Requirements:

- the image must include the hosted `/admin` client build
- the image must preserve the separate `/webui` IPFS dashboard surface
- the repo's existing container build/publish path should be reused instead of creating a parallel Docker packaging story
- image publication remains part of the normal server release flow

Current repo conventions indicate the intended registry path is `ghcr.io`, with existing Dockerfiles under `deployment/docker/*` and an existing publish workflow in `.github/workflows/docker-publish.yml`.

### `sdn-js`

`sdn-js` must own:

- the admin shell package
- adapter interfaces
- local-mode implementations
- shared store/directory models
- wallet/account integration
- Monaco/editor integration

`sdn-server` should be a host and API provider, not the owner of UI behavior.

## Abuse Controls and Progressive Backoff

The new `Server` mode introduces node-selection, session, and control-plane operations that must be hardened.

All control APIs must apply progressive abuse controls, especially:

- auth challenge/verify endpoints
- remote connect / target negotiation endpoints
- frontend write/upload actions
- git actions
- pin/publish/control actions
- user-management and trust-management mutations

Required behavior:

- per-IP rate tracking
- per-identity or per-session rate tracking where available
- progressive backoff windows rather than a single fixed limit
- explicit cooldown signaling to clients
- client honor of server-provided cooldown windows

The goal is to degrade abusive clients cleanly, especially from multiple source IPs, without letting the new admin shell turn into a control-plane amplifier.

## Migration

### Phase 1

- introduce the new isomorphic admin shell package in `sdn-js`
- host it from `sdn-server` at `/admin`
- preserve existing admin APIs long enough to keep current trust/frontend operations working through the new shell

### Phase 2

- move the current trust/user/node/frontend views onto the shared shell
- replace the inline Go template entirely

### Phase 3

- add the full `Directory`
- add `Store` with linked `Modules` and `Data`
- add the IDE-grade `Frontend` workspace
- add full server-target switching and git flows

### Phase 4

- extend SDS metadata upstream for stronger module/data relation derivation
- move compatibility and recommendation logic fully onto canonical standards metadata
- finalize the Docker release path so the published `sdn-server` image carries the completed admin shell

## Error Handling

- backend mode changes must never silently discard the current context; the shell must show what node and mode are active
- when a server session expires, the UI must preserve the selected node context and prompt for re-authentication
- if a feature is unavailable in `Local` mode or on a given server, the shell must show that explicitly instead of hiding the workspace
- failed git or publish actions must return actionable status, not opaque generic errors
- if the IPFS dashboard link is unavailable for the active context, the UI must explain why

## Testing Requirements

### `sdn-js`

- adapter contract tests for `Local` vs `Server`
- workspace navigation tests
- mode-switch state tests
- account-menu and sign-out tests
- Monaco/editor integration tests around open/save/dirty tracking
- store relationship rendering tests for linked modules/data
- directory search/filter/render tests

### `sdn-server`

- `/admin` hosting tests for the built admin app
- API authorization tests per role and mode-sensitive route
- progressive-backoff tests for auth/control endpoints
- IPFS dashboard link/path tests
- frontend workspace API tests
- git API tests
- container build tests that verify the image contains the admin client assets and serves `/admin`

### End-to-end

- browser-only Helia mode
- server-backed admin mode
- server-target switching between two nodes
- wallet login and account modal behavior
- frontend edit/save/upload flow
- linked store browsing for modules and data
- IPFS dashboard launch from the admin shell
- published container smoke test for `/admin`, `/webui`, and wallet/auth asset serving

## Acceptance Criteria

- `/admin` is served by the shared `sdn-js` admin client, not the inline Go template
- the shell has a left icon rail and top bar with `Local | Server` switch
- the shell includes a direct `IPFS Dashboard` entry
- the account icon opens `hd-wallet-ui`, and sign-out lives there
- switching server targets updates the active node, effective permissions, and user list
- `Directory` provides an active-directory style peer and identity explorer
- `Store` has linked `Modules` and `Data` catalogs informed by SDS metadata relationships
- `Frontend` provides a Monaco-based IDE with a resizable file tree and upload support
- the same app runs in browser-only Helia mode and in server-hosted `/admin` mode
- control APIs implement progressive backoff suitable for abusive traffic patterns
- `sdn-server` is buildable and publishable as a Docker image that serves the new admin shell
