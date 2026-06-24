# SDN CLI, Desktop, UI, Release, And Live-DHT Parity Design

Date: 2026-06-24
Status: design for review

## Goal

Make the first SDN release behave like one product across the command line,
Desktop app, bundled UI, installers, updater, release artifacts, documentation,
and live-DHT verification. A user should be able to install SDN on macOS,
Linux, or Windows, join the public IPFS Kademlia DHT-backed SDN network, manage
identity, discover providers, search data, use encrypted maneuver ephemeris and
conjunction-assessment workflows, update the running daemon, and remove the
install without learning separate product models for CLI and Desktop.

## Product Contract

The CLI, Desktop, and UI must expose the same user-visible capabilities unless
one surface has a deliberate platform-only reason to omit a control. When a
capability is omitted, the help text or UI must name the equivalent supported
surface.

| Capability | CLI | Desktop/UI | Shared backend requirement |
| --- | --- | --- | --- |
| Install | `install.sh` and `install.ps1`, no `gh`, user-scoped by default | release installers linked from website | release manifest includes CLI and Desktop artifacts |
| Identity bootstrap | installer runs safe init; `show-identity`; `identity wizard` | node identity panel and EPM editor | one encrypted EPM/FlatBuffer source of truth |
| EPM/vCard export | text, JSON, CSV, FlatBuffer, QR | download/copy JSON, vCard, QR where useful | routes serve canonical FlatBuffer-derived projections |
| Directory EPM/vCard | import, list, show, download | directory search/download | one directory API and local store projection |
| Lifecycle | start, stop, restart, status, service, remove | tray/menu/UI equivalents where useful | shared service status and install/remove semantics |
| Provider search | local, daemon/API, live DHT; table/row, JSON, CSV | same filters and result fields | one provider search service and schema |
| Data search/query | standards, providers, schemas, data products; table/JSON/CSV | same query controls and result fields | one data API contract for scan/query/stream |
| Provider interaction | list, show, connect/query, descriptor lookup | provider detail/connect/query flows | descriptor and connection APIs backed by DHT/API |
| Encrypted CA/MPE | private MPE discovery, grant/channel selection, retrieval, screening, export | equivalent guided workflow | CA source selection, encrypted channels, provenance, result export |
| Update | check/stage/apply through SDN update provider; running daemon in-place update | same trust/feed path through Desktop updater | SDN-owned signed feed, wrapped upstream IPFS/Kubo refresh, rollback |
| Remove | removes current install location; data preserve by default, purge opt-in | uninstall/remove UX or documented OS uninstall plus SDN purge control | install registry discovers active install roots |

## Architecture

### Shared Capability Registry

The machine-readable contract lives at
`deployment/release/sdn-parity-contract.json` and is checked by
`deployment/release/sdn-parity-contract.test.mjs`. The contract should list
stable capability IDs such as `identity.export`, `search.providers`,
`desktop.route.node_epm_vcard`, `update.daemon_in_place`, and
`release.desktop_artifacts`. CLI tests, Desktop route tests, UI runtime tests,
and docs tests should assert the capabilities they expose against this contract.
This does not replace implementation tests; it prevents future accidental drift.

### Server And Desktop API Parity

The SDN daemon remains the authoritative server implementation. Desktop should
either proxy to the daemon or implement the same route contract locally when the
daemon is embedded. The immediate route gaps are:

- full `/api/v1/data/*` behavior expected by the UI;
- `/api/node/epm/vcard`;
- `/api/peers/{peerId}/epm` and `/api/peers/{peerId}/epm/vcard`;
- `/api/auth/users/{xpub}` routes used by bundled overrides;
- provider and data search routes used by both Desktop and UI.

Desktop route handlers must keep the existing loopback-only bind and Host
header guard. Profile edits must keep encrypted size-prefixed `EPM.fbs` bytes
as the source of truth; JSON and vCard are derived views only.

### Search And Provider Discovery

Move provider/data/standards search behind one reusable search service. The CLI
should support:

- `--mode local|daemon|dht` or equivalent flags;
- `--api-url` for explicit daemon/API targeting;
- `--format table|row|json|csv`;
- provider, schema, standard, source, and capability filters.

Desktop/UI should call the same daemon/Desktop search API and render the same
result fields. DHT mode must seed from the production bootstrap nodes and use
the live IPFS Kademlia DHT, not Tailscale or a private-only network.

### Identity And Directory Flow

The installer must auto-initialize identity without overwriting existing
mnemonic, private keys, config, or EPM data. CLI wizard fields must match the
UI profile fields, including given name, family name, entity type, legal name,
organization, email, telephone, URLs, keys, and SDN-specific service metadata.

CLI directory commands should cover:

- `identity directory list`;
- `identity directory show`;
- `identity directory import`;
- `identity directory download`;
- export format selection for text, JSON, CSV, FlatBuffer, and QR where the
  target artifact supports it.

### Encrypted CA And Maneuver Ephemeris

Add a domain workflow rather than exposing only generic channel plumbing. The
workflow must let a user select OMM, OCM, OEM, MPE, CDM, FlatSQL/PNM, or
provider-backed sources, then retrieve encrypted/private data through grants or
channels, run or stage conjunction assessment, and export results with
provenance. Provenance must include selected providers, PNM CIDs or query
hashes, module artifact hash, module version, run timestamp, CA configuration,
and encrypted channel or grant identifiers.

The UI should make private maneuver ephemeris screening explicit: the user can
screen maneuvers without broadcasting maneuver intent to competitors. The CLI
should expose the same flow for automation.

### Lifecycle, Remove, And Update

`spacedatanetwork start` remains the simple persistent service path. Manual
`daemon` mode remains available. Remove must discover the current installed
version and launchers wherever the supported installers placed them. Data is
preserved by default; `--purge-data` is explicit.

The update path must be SDN-owned end to end. The CLI and Desktop should check
the SDN update provider server, verify the signed feed, stage the replacement,
stop or quiesce the running daemon, swap the bundle, restart, verify health,
and roll back on failure. The SDN update wrapper may consume upstream IPFS/Kubo
updates internally, but users see SDN update metadata and trust roots.

### Release And Website

The main beta release lane must publish:

- Linux, macOS, and Windows CLI archives;
- Linux server/container artifacts currently supported by the release lane;
- Desktop artifacts produced by Electron Builder for macOS, Windows, and Linux;
- release body links for every published artifact;
- `https://spacedatanetwork.org/install.sh`;
- `https://spacedatanetwork.org/install.ps1`;
- website download sections for CLI and Desktop.

Docs, README, CLI help, Desktop Help/About, and the website must use
`spacedatanetwork.org` for user-facing install/update documentation. Upstream
IPFS links may remain only where the text is explicitly describing upstream
technology or license/about attribution.

## Implementation Slices

1. **Parity Contract And Desktop Route Parity**
   - Add the parity contract file and tests.
   - Add missing Desktop API routes used by current bundled UI pages.
   - Replace user-facing Desktop Help/About IPFS links with SDN links where
     those links are product documentation.

2. **Shared Search And Provider Interaction**
   - Extract or wrap the existing CLI search implementation behind a shared
     daemon/Desktop API.
   - Add CLI live/API modes and provider interaction commands.
   - Update Desktop/UI to call the same result contract.

3. **Identity Directory Parity**
   - Fill CLI wizard field gaps.
   - Add CLI directory import/list/show/download.
   - Ensure Desktop and UI derive JSON/vCard from canonical EPM bytes.

4. **Encrypted CA And MPE Workflow**
   - Add CLI commands and UI screens/actions for private MPE discovery,
     encrypted retrieval, CA source selection, run staging, and result export.
   - Use existing CA module contracts and SDN data/channel APIs; add adapters
     only where required by OCM/OEM/MPE source framing.

5. **Lifecycle, Remove, And Signed Update**
   - Align CLI and Desktop service status and remove semantics.
   - Implement SDN update-provider check/stage/apply with running-daemon
     swap/restart/rollback.
   - Wire Desktop updater to the same feed and trust path.

6. **Release, Install, CI, And Docs**
   - Add Desktop artifacts to the release assembly and release body.
   - Add clean-user installer smokes from published endpoints.
   - Extend live-DHT GitHub Actions to prove Linux Docker, macOS, and Windows
     clients discover each other after at least five minutes of DHT
     registration and execute identity, provider search, data search, and one
     retrieval/query path.
   - Update README, docs, CLI help, Desktop help, and website.

## Test Plan

- Go CLI tests for command registration, flags, output formats, API/live mode
  behavior, service/remove semantics, status, and update lifecycle.
- Desktop unit tests for static-server data routes, identity/EPM/vCard routes,
  peer EPM routes, auth user routes, provider/search routes, Host guardrails,
  tray/menu links, updater staging, and service lifecycle controls.
- SDN JS runtime tests for search/provider/data contracts and Desktop adapter
  behavior.
- UI tests for provider search, directory identity, private CA/MPE workflow,
  service/update prompts, and data query state.
- Release tests for artifact assembly, desktop artifacts, release body links,
  one-line installer scripts, update feed metadata, and docs parity.
- Live-DHT workflow tests for Linux Docker, macOS, and Windows clients using
  public IPFS Kademlia DHT discovery with a minimum five-minute registration
  wait.
- Desktop packaging verification after any Desktop behavior, tray/menu,
  updater, packaging, or bundled UI asset change: package, reinstall or refresh
  the local installed app, restart it, and record the result.

## Acceptance Criteria

- Fresh macOS/Linux install works with
  `curl -fsSL https://spacedatanetwork.org/install.sh | bash`.
- Fresh Windows install works with
  `irm https://spacedatanetwork.org/install.ps1 | iex`.
- Neither installer requires `gh`; user-scoped installs do not require
  sudo/admin.
- `spacedatanetwork show-identity`, `status`, and search commands work
  immediately after install without manual init.
- CLI, Desktop, and UI expose the same identity, provider discovery, data
  search/query, encrypted CA/MPE, lifecycle, update, and remove capabilities.
- The live-DHT cross-platform test waits at least five minutes and proves
  Linux, macOS, and Windows clients can discover and query each other.
- Desktop artifacts are built, released, and linked from the release body and
  website.
- README, docs, CLI help, Desktop help/about, and website agree on install,
  update, search, identity, encrypted CA, and maneuver ephemeris behavior.
- No compatibility path for an older SDN build is introduced for this first
  version unless a current SDN artifact test requires it.
