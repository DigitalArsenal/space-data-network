# Screen Inventory

## Node

Primary job: show whether the local SDN node is usable and whether identity is
ready.

Content:
- node status and peer ID
- public and loopback addresses
- storage usage
- identity lock state
- EPM/vCard/QR export actions
- service lifecycle and update status

## Peers

Primary job: make provider discovery and trust state clear.

Content:
- observed and trusted peers
- SpaceAware and CelesTrak providers
- ownertrust
- peer identity metadata
- data feeds
- vCard and QR affordances

## Data

Primary job: let users find, sync, inspect, and query standards-based space
data.

Content:
- provider and data search
- standards: CAT, EPM, MPE, OMM, PNM, SPW
- schema sync state
- local FlatSQL store status
- query output modes: row/table, JSON, CSV
- row inspection

## Channels

Primary job: make encrypted exchange understandable and actionable.

Content:
- channel visibility and encryption state
- subscription state
- grant state
- recipient and key envelope controls
- stream publish/open actions
- monitor/detail pane

## Conjunction

Primary job: screen private maneuver ephemeris without revealing maneuver
intent to competitors.

Content:
- primary and secondary source selection
- grant and private channel inputs
- assessor peer and module version
- result channel
- table/JSON/CSV output modes
- provenance and encrypted workflow summary
