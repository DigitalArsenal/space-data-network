# Space Data Network UI Redesign Brief

You are redesigning the Space Data Network Desktop and bundled SDN UI.

The current UI is not acceptable. Keep the product capabilities, but redesign
the visual hierarchy, layout, navigation, controls, and state presentation.

Use this package as a design-mode prototype. It is intentionally standalone:
it does not call the SDN daemon, Kubo, Electron, or the live network.

## Product Context

Space Data Network is a peer-to-peer data network for space situational
awareness data. Users run a local node, manage identity, discover trusted
providers, query standards-based data, exchange encrypted data channels, and
screen private maneuver ephemeris for conjunction assessment.

## Required Top-Level Surfaces

- Node
- Peers
- Data
- Channels
- Conjunction

## Design Objective

Make the UI feel like a serious space-operations and network-console product.
It should be dense, scannable, composed, and useful for repeated operational
work. Do not turn it into a marketing landing page.

## Preserve These Product Ideas

- Local SDN node and service health
- Identity, EPM, vCard, and QR export
- Trusted and observed peer directory
- Provider and data-standard search
- Local and subscribed data workbench
- Encrypted channels, grants, and key envelopes
- Private maneuver ephemeris screening
- CLI/Desktop parity

## Freedom To Change

You may change layout, information hierarchy, navigation model, typography,
spacing, color, component style, and interaction grouping inside the prototype.

When a design change needs a backend change, call that out explicitly in your
notes rather than silently changing the product contract.
