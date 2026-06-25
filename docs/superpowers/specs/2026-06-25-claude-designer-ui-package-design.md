# Claude Designer UI Package Design

Date: 2026-06-25
Status: design for review

## Goal

Create an uploadable UI package for Claude Designer so the Space Data Network
desktop/bundled UI can be redesigned in design mode without requiring Claude
Designer to understand the full SDN monorepo, Electron packaging, Kubo, or live
DHT runtime. The package must preserve product meaning and workflow parity while
making the visual and interaction model easy to replace.

The current production UI source remains under `sdn-js/ui`. The Designer
package is a handoff artifact, not a new production app.

## Package Shape

The package is a ZIP archive containing two parts:

1. **Designer handoff brief**
   - `CLAUDE_DESIGNER_BRIEF.md`
   - `SCREEN_INVENTORY.md`
   - `SOURCE_MAP.md`
   - `DESIGN_CONSTRAINTS.md`
   - `IMPLEMENTATION_NOTES.md`

2. **Standalone editable prototype**
   - `prototype/index.html`
   - `prototype/styles.css`
   - `prototype/app.js`
   - `prototype/data/fixtures.json`
   - generated screenshots under `screenshots/`

The prototype must open directly in a browser from the extracted ZIP. It must
not require `npm install`, Vite, Svelte, Electron, Kubo, a daemon, or network
access.

## Designer Brief

`CLAUDE_DESIGNER_BRIEF.md` should give Claude Designer the plain-language task:

- the current UI is not acceptable and needs a serious redesign;
- preserve the five real SDN UI surfaces: Node, Peers, Data, Channels, and
  Conjunction;
- treat SDN as a space-operations/network-console product, not a marketing
  site;
- keep Desktop and CLI parity visible in the workflows;
- prioritize dense, scannable operational UX over decorative presentation;
- make private maneuver ephemeris and encrypted conjunction assessment obvious
  product strengths;
- avoid changing backend semantics unless a proposed UI explicitly calls out
  the backend implication.

The brief should also say that Claude Designer can freely change layout,
navigation, typography, information hierarchy, state presentation, and
component styling inside the prototype.

## Prototype Screens

The prototype covers the same top-level routes as the production Svelte UI.

| Prototype area | Production route | Production source |
| --- | --- | --- |
| Node | `#/node` | `sdn-js/ui/src/screens/NodeScreen.svelte` |
| Peers | `#/peers` | `sdn-js/ui/src/screens/PeersScreen.svelte` |
| Data | `#/data` | `sdn-js/ui/src/screens/LocalDataScreen.svelte` |
| Channels | `#/channels` | `sdn-js/ui/src/screens/ChannelsScreen.svelte` |
| Conjunction | `#/conjunction` | `sdn-js/ui/src/screens/ConjunctionScreen.svelte` |

### Node

Show local node status, peer ID, API/gateway addresses, storage summary,
identity lock state, EPM/vCard export affordances, and update/service status.
This screen should make it clear that the node is part of the live SDN network
and that identity is a first-class product concept.

### Peers

Show observed/trusted peers, SpaceAware and CelesTrak provider identities,
ownertrust, data feeds, vCard/QR affordances, and provider detail. This screen
should communicate provider discovery and trust at a glance.

### Data

Show provider/data search, data standards filters, schema sync status, local
FlatSQL/PNM/OMM/MPE/CAT/SPW datasets, query output modes, and row-level
inspection. This screen should feel like an operational data workbench.

### Channels

Show encrypted channels, grant state, subscription state, stream publish/open
actions, recipient/key envelope fields, and a monitor/detail pane. This screen
should make secure exchange understandable without exposing only raw protocol
terms.

### Conjunction

Show private maneuver ephemeris screening, primary/secondary source selection,
grant/channel inputs, module/version metadata, assessor peer, result channel,
table/JSON/CSV output modes, and provenance. The design must explicitly support
the value proposition: maneuver ephemeris can be screened without broadcasting
to competitors that maneuvers are happening.

## Fixture Data

`prototype/data/fixtures.json` should include realistic, non-secret sample data:

- local node identity and service state;
- SpaceAware peer:
  `16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45`;
- CelesTrak peer:
  `16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3`;
- provider records for SpaceAware and CelesTrak;
- sample standards: `CAT`, `EPM`, `MPE`, `OMM`, `PNM`, `SPW`;
- sample encrypted channel/grant records;
- sample conjunction screening result rows and provenance.

The fixture data must be small and self-contained. It must not include real
private keys, mnemonic phrases, tokens, or user-specific filesystem paths.

## Prototype Behavior

The prototype should be static but interactive enough for design work:

- sidebar route switching updates the main screen without page reload;
- table rows can be selected where detail panes exist;
- segmented controls switch output modes such as table, JSON, and CSV;
- search/filter fields visually update counts or filtered lists when cheap to
  implement;
- copy/export/download buttons may be inert but should be visibly present;
- all states are fixture-driven and deterministic.

No production backend calls should be made from the prototype.

## Visual Direction

The package should not prescribe a final style, but it should give Claude
Designer a useful starting point:

- professional space-operations console;
- dense but organized information hierarchy;
- restrained color system with clear semantic state;
- usable desktop-first layout with sane responsive behavior;
- navigation that makes Node, Peers, Data, Channels, and Conjunction equally
  discoverable;
- cards only for repeated items, detail panels, or framed tools;
- no landing-page hero, decorative orbs, or generic stock-space gloss.

The prototype can start from a clean neutral operations-console look. Designer
is expected to improve it.

## Source Map

`SOURCE_MAP.md` should point from prototype files to production files:

- App shell and routes:
  `sdn-js/ui/src/App.svelte`,
  `sdn-js/ui/src/components/AppShell.svelte`,
  `sdn-js/ui/src/components/SideNav.svelte`,
  `sdn-js/ui/src/components/TopStatusBar.svelte`,
  `sdn-js/ui/src/lib/routes.ts`;
- design tokens and global CSS:
  `sdn-js/ui/src/styles/tokens.css`,
  `sdn-js/ui/src/styles/app.css`;
- screen implementations:
  files under `sdn-js/ui/src/screens/`;
- Desktop packaging bridge:
  `desktop/src/static-http-server.js`,
  `desktop/src/dashboard/index.js`;
- Desktop packaged asset target:
  `desktop/assets/sdn-ui`.

The source map should warn that `webui/` is the upstream IPFS WebUI mirror and
should not be treated as the main SDN app redesign surface unless the redesign
explicitly includes upstream mirror overrides.

## Acceptance Criteria

- The ZIP exists at a predictable path under `artifacts/design/`.
- The ZIP contains the handoff docs and standalone prototype files.
- `prototype/index.html` works from local disk or a simple static server.
- The prototype covers Node, Peers, Data, Channels, and Conjunction.
- The package includes fixtures for peers, providers, identity, data standards,
  encrypted channels, and conjunction results.
- The package includes one generated screenshot per primary screen.
- The docs map prototype surfaces back to production Svelte/Desktop files.
- No secrets, real mnemonic material, tokens, or private keys are included.
- The package can be regenerated by a script so future UI context does not rot.

## Verification

The package build should be verified by:

- running the package generation script;
- checking ZIP contents;
- scanning the package for forbidden secret-like terms and local absolute paths;
- opening `prototype/index.html` through a local static server;
- taking screenshots of each route with Playwright.

## Non-Goals

- Do not replace the production Svelte UI in this task.
- Do not redesign the UI inside the repo before Claude Designer work happens.
- Do not alter daemon, CLI, Desktop backend, or DHT behavior.
- Do not include the full repository or `node_modules` in the upload package.
- Do not make Claude Designer depend on Electron, Kubo, Svelte, or Vite.
