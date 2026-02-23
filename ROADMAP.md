# Space Data Network Roadmap

This document outlines planned features and development priorities for the Space Data Network.

---

## In Progress

### Chrome Browser Extension
**Issue:** [#1](https://github.com/DigitalArsenal/space-data-network/issues/1)

A Chrome extension that allows users to interact with SDN directly from their browser toolbar:
- Connect to SDN using the `sdn-js` SDK
- Subscribe to data feeds with desktop notifications
- View network status and connected peers
- Quick publish SDS-formatted data
- Identity/key management in secure browser storage
- Bookmark and track specific satellites

---

## Planned Features

### Data Marketplace

A commercial layer enabling data providers to monetize their space data:

- **Per-Customer Encryption** - Data encrypted with each customer's public key using ECIES
- **Plugin Marketplace** - Third-party analysis tools and algorithms available for purchase
- **Payment Gateway** - Credit card processing via Stripe/similar for seamless purchases
- **Subscription Models** - Support for one-time purchases and recurring subscriptions
- **Revenue Sharing** - Automated splits between data providers and platform
- **Usage Metering** - Track and bill based on data consumption

### Network Improvements

- **Bootstrap Node Registry** - Curated list of reliable full nodes for initial connection
- **Geographic Routing** - Optimize peer selection based on latency
- **Bandwidth Metering** - Track and optionally limit data transfer
- **Priority Messaging** - QoS tiers for time-critical data (conjunction warnings)

### Developer Tools

- **Firefox Extension** - Port Chrome extension to Firefox
- **VS Code Extension** - SDN integration for development workflows
- **CLI Improvements** - Enhanced command-line tools for data management
- **GraphQL API** - Alternative query interface for complex data access

### Mobile

- **React Native SDK** - Mobile app support for iOS and Android
- **Mobile App** - Standalone SDN client for mobile devices

### Analytics & Visualization

- **Network Dashboard** - Real-time visualization of network topology and traffic
- **Data Explorer** - Web UI for browsing and querying published data
- **Conjunction Visualization** - 3D visualization of close approaches

---

## Completed

- [x] Core libp2p networking (Go server)
- [x] JavaScript SDK for browser/Node.js
- [x] All Space Data Standards schemas
- [x] Ed25519 identity and signatures
- [x] GossipSub pub/sub messaging
- [x] Circuit relay for NAT traversal
- [x] Edge relay for browser connectivity
- [x] Desktop application (Electron)
- [x] WASM crypto module
- [x] Documentation website

---

## Contributing

Have a feature request? [Open an issue](https://github.com/DigitalArsenal/space-data-network/issues/new) to discuss!

See [CONTRIBUTING.md](./CONTRIBUTING.md) for contribution guidelines.
