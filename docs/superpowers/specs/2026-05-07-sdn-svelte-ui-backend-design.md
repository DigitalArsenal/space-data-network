# SDN Svelte UI And Backend Adapter Design

## Purpose

Replace the current SDN-branded upstream IPFS WebUI shell with a native Svelte
product UI that lives under `sdn-js/ui`, runs quickly in browser during
development through Vite, and uses one typed backend contract for local desktop
nodes and remote SDN servers.

The UI is the SDN product surface. Upstream IPFS WebUI remains available as a
separate compatibility surface under `/webui`.

## Source References

- Existing SDN UX brief:
  `design/sdn_ui_clean_design/sdn_clean_ux_ui_design_brief.md`
- Cleaner SDN screen snapshots:
  `design/sdn_ui_clean_design/assets/sdn-clean-01-node.png`
  `design/sdn_ui_clean_design/assets/sdn-clean-02-peers-mission.png`
  `design/sdn_ui_clean_design/assets/sdn-clean-03-local-data.png`
  `design/sdn_ui_clean_design/assets/sdn-clean-04-claim-core.png`
- Visual style target:
  `https://styles.refero.design/style/764b6a64-c233-4e0f-b8e1-bc01e2f8aa16`
- Optional motion/reference assets:
  `design/300 FUI Elements Pack by Nawaz Alamgir/Widgets/`
- Existing runtime code:
  `sdn-js/src/ui/runtime/*`
- Current transitional UI host:
  `sdn-js/ui/src/upstream-webui/*`

## Product Shape

The SDN UI has three primary screens:

- `Node`: node identity, runtime mode, wallet/EPM state, Core import/export,
  roles, permissions, and advanced node controls.
- `Peers`: trusted peers, marketplace data feeds, modules, schemas, discovery,
  library ownership, and mission loadout.
- `Local Data`: pinned/cached/encrypted objects, storage pressure, rulesets,
  provenance, SQL dashboards, and object inspection.

The primary navigation must stay limited to those three items. Everything else
is a tab, drawer, modal, inspector, command palette action, or advanced panel.

## Visual System

The UI uses the Refero Apple-style reference as the controlling aesthetic:

- Base canvas: pitch black and deep graphite.
- Surfaces: graphite cards with subtle borders and minimal shadow.
- Typography: system/SF Pro style, with Inter as the practical fallback.
- Hierarchy: strong type scale, fewer labels, generous whitespace.
- Color: blue for primary action and active state; green, amber, and red only
  for status; purple only for encrypted/imported/special state.
- Shape: content cards and controls use consistent operational radii. Avoid
  fully rounded bubble controls for ordinary SDN actions; reserve pill shapes
  only for compact tags or status chips where the design explicitly calls for
  them.
- Motion: restrained. FUI video/widget assets may inform loaders, mission
  previews, or advanced diagnostics, but must not become the base chrome.
- Icons: one outline icon family, 20-24 px, consistent stroke weight.

The result should feel like a precision SDN instrument: calm, high-contrast,
dark, professional, and sparse.

## Architecture

Replace the current `sdn-js/ui` app entrypoint in place:

```text
sdn-js/ui/
```

This directory owns the SDN Svelte UI. It builds to a static bundle that can be
served by:

- SDN Desktop under `/sdn`.
- SDN server under `/`.
- local Vite dev server during product development.

Keep the existing upstream IPFS WebUI mirror available separately under
`/webui`. Do not move upstream IPFS UI routes into the SDN product shell.

The current `sdn-js/ui/src/upstream-webui` integration remains in place only as
a temporary compatibility reference while the Svelte app reaches parity. It
should not receive new long-lived product behavior once this migration begins.

## Backend Contract

All screens call a typed `SdnBackend` interface. Screens do not scatter raw
`fetch('/api/...')` calls.

The contract should expose these groups:

```text
runtime
  connect()
  getCapabilities()
  getNodeSummary()
  getHealth()

identity
  getNodeProfile()
  saveNodeProfile()
  listWalletsAndEpms()
  beginClaimEpm()
  exportCore()
  importCore()

peers
  listObservedPeers()
  listTrustedPeers()
  searchDirectory()
  connectPeer()

marketplace
  searchListings()
  listOwnedItems()
  requestGrant()
  installModule()
  subscribeDataFeed()

localData
  getStorageSummary()
  listObjects()
  inspectObject()
  pinObject()
  unpinObject()
  listRulesets()
  saveRuleset()
  runSqlQuery()

ipfs
  getKuboStatus()
  listFiles()
  resolveCid()
  readGatewayUrl()
```

Every method returns either data plus capability metadata, or a typed degraded
state. Unsupported actions are not silent no-ops. The UI must be able to show:

- available
- degraded
- unavailable
- permission required
- remote-only
- local-only

## Backend Adapters

### `desktop-local`

Used when the Svelte app is served inside SDN Desktop or when the Vite dev
server is pointed at a local desktop/Kubo instance.

It combines:

- Kubo RPC API for upstream IPFS-compatible operations.
- Kubo gateway for CID/IPLD reads.
- Desktop SDN adapter routes for local SDN concepts not exposed by Kubo.
- Optional remote SDN server calls for marketplace or directory data when the
  local node is connected to remote SDN peers.

Required local routes include:

```text
/api/peers/sdn
/api/peers
/api/peers/graph
/api/node/epm/json
/api/node/epm
```

The adapter must use real Kubo/SDN identify data for peer IDs. Configured SSH
aliases, DNS names, and seed labels are connection hints only.

### `remote-sdn`

Used when the app connects to a remote SDN server such as
`https://sdn.spaceaware.io`.

It uses server HTTP APIs for SDN features and remote node management. Personal
wallet/Core actions must run locally unless explicitly granted by the remote
permission model. Remote nodes do not silently become the user personal wallet
machine.

### `browser-node`

Designed now, implemented in Milestone 4.

The browser-node adapter represents a local browser-based IPFS/SDN node using
browser transports and storage. It is limited compared with daemon Kubo, but
it is still treated as a real node with explicit capabilities:

- local identity
- local wallet/EPM flows
- local data cache where browser storage allows
- Helia/libp2p connectivity where supported
- direct module/data workflows where browser transports permit

Unavailable daemon-only features must show degraded capability state rather
than being hidden behind misleading errors.

## Development Workflow

The primary UI development loop must not require rebuilding the desktop app.

Add a Vite development mode for `sdn-js/ui`:

```sh
npm --prefix sdn-js run dev:sdn-ui
```

The dev server should:

- serve the Svelte app from localhost;
- accept explicit backend mode and URLs through env vars or query params;
- proxy `/api/*` to a local SDN desktop/server target when requested;
- connect directly to local Kubo RPC and gateway when configured;
- support remote targets such as `https://sdn.spaceaware.io`;
- make active backend mode and capability status visible in the UI.

Example local development target:

```sh
SDN_UI_BACKEND=desktop-local \
SDN_UI_API_URL=http://127.0.0.1:5001 \
SDN_UI_GATEWAY_URL=http://127.0.0.1:8081 \
SDN_UI_PROXY_TARGET=http://127.0.0.1:17890 \
npm --prefix sdn-js run dev:sdn-ui
```

Example remote development target:

```sh
SDN_UI_BACKEND=remote-sdn \
SDN_UI_SERVER_URL=https://sdn.spaceaware.io \
npm --prefix sdn-js run dev:sdn-ui
```

Desktop packaging remains required for final desktop-host verification, tray
menu routing, bundled asset checks, and install/restart validation. It is not
part of the inner UI iteration loop.

## Component System

Create Svelte components for the SDN product shell:

```text
AppShell
TopStatusBar
SideNav
CommandPalette
MissionDrawer
AdvancedDrawer

NodeScreen
NodeIdentityCard
RuntimeModeBadge
WalletEpmList
WalletEpmInspector
ClaimEpmWizard
CoreActionPanel
AccessRoleCard

PeersScreen
PeerSearchBar
PeerCard
DataProductCard
ModuleCard
MarketplaceFilters
MissionLoadoutPanel
CompatibilityChecker
CesiumPreviewPanel

LocalDataScreen
StorageSummary
PinTable
ObjectInspector
RulesetList
RulesetBuilder
SqlWorkbench
SchemaBrowser
ProvenanceTimeline
```

The first Svelte implementation should produce real screens for `Node`,
`Peers`, and `Local Data`, even if some deeper actions open disabled/degraded
states until their backend endpoint is implemented.

## Widget Reuse

The cinematic FUI widget assets under `design/300 FUI Elements Pack by Nawaz
Alamgir/Widgets/` are references for restrained status/motion details only.
They are not a license to add noisy HUD chrome.

Reusable functional widget references:

- `spaceaware.io/src/lib/components/ChartWidget.svelte` for Svelte 5 chart
  widget patterns and interactive data dashboard behavior.
- `spaceaware.io/src/lib/components/GlassSearchBar.svelte` for search/control
  surface patterns, restyled to the Apple/Refero SDN system.
- OrbPro/Cesium widgets only for mission preview or visualization integration
  where the user is actually opening or previewing space data.

## Routing

Svelte routes:

```text
/node
/peers
/local-data
/advanced
/claim-core
```

Compatibility redirects:

```text
/status        -> /node
/settings      -> /node?panel=advanced
/peers         -> /peers
/files         -> /local-data
/pins          -> /local-data?tab=pins
/explore/:cid  -> /local-data?inspect=:cid
/modules       -> /peers?tab=modules
/marketplace   -> /peers?tab=marketplace
```

SDN Desktop tray menu items must route to the Svelte SDN routes under `/sdn`.
The IPFS menu may route to upstream `/webui`.

## Migration Plan

Milestone 1: Svelte app foundation

- Convert `sdn-js/ui` to the Svelte SDN product app while keeping the current
  upstream-WebUI code as a temporary compatibility reference.
- Add Svelte, Vite, TypeScript, and design tokens.
- Add `SdnBackend` contract and `desktop-local` plus `remote-sdn` adapters.
- Add Vite dev workflow for local Kubo/desktop and remote SDN server targets.
- Build `AppShell`, `TopStatusBar`, `SideNav`, and capability state display.

Milestone 2: Product screens

- Build `Node`, `Peers`, and `Local Data` screens from real adapter data.
- Add degraded states for unsupported actions.
- Keep raw Kubo/IPFS details inside `AdvancedDrawer`.

Milestone 3: Desktop/server integration

- Serve built `sdn-js/ui` bundle from desktop `/sdn`.
- Serve built `sdn-js/ui` bundle from SDN server `/`.
- Keep upstream IPFS WebUI under `/webui`.
- Preserve desktop packaging/reinstall/restart verification for desktop-hosted
  changes.

Milestone 4: Browser-node adapter

- Implement browser-node as a real capability-limited adapter.
- Add Helia/libp2p identity and local browser storage flows.
- Mark unsupported daemon-only features as degraded.

## Testing And Guardrails

Contract tests:

- `desktop-local` maps live Kubo identify data into real SDN peers.
- `remote-sdn` uses remote URLs and credentials correctly.
- configured aliases never become Peer IDs.
- capability states are explicit for unsupported methods.
- browser-node deferred adapter reports designed degraded capabilities until
  Milestone 4 implementation is active.

UI tests:

- App shell renders the three primary nav items only.
- `Node` loads profile and capability state from the selected backend.
- `Peers` shows observed peers from adapter data.
- `Local Data` shows objects, storage, and SQL capability state.
- Advanced drawer contains raw Kubo/IPFS diagnostics.
- No SDN route sends users to upstream IPFS WebUI pages.

Visual tests:

- Desktop and mobile viewport screenshots for `Node`, `Peers`, and `Local Data`.
- Verify text does not overlap or overflow cards/buttons.
- Verify buttons use the SDN operational radius, not fully rounded bubble
  corners except compact chips/tags.
- Verify palette remains mostly black/graphite/white/gray with blue primary
  actions and restrained status colors.

Build and host tests:

- `npm --prefix sdn-js run build:sdn-ui`
- Vite dev smoke test against local Kubo plus desktop target.
- Vite dev smoke test against remote SDN target.
- Desktop package/reinstall/restart before claiming desktop-host integration.

## Non-Goals

- Do not remove upstream IPFS WebUI.
- Do not implement browser-node in the first milestone.
- Do not rebuild all IPFS WebUI pages in Svelte.
- Do not use FUI videos as constant background decoration.
- Do not treat remote nodes as personal wallet machines by default.
- Do not add new schema records for this UI migration unless an endpoint
  contract requires SDS changes in a separate schema task.

## Open Decisions Resolved

- `browser-node` is defined now and implemented in Milestone 4.
- Vite browser development is the primary UI iteration loop.
- Desktop recompilation is reserved for desktop-host verification.
- `sdn-js/ui` is the long-term SDN product UI location.
- `/webui` remains the upstream IPFS compatibility UI.
