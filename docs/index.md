---
layout: home

hero:
  name: Space Data Network
  text: Decentralized Space Traffic Management
  tagline: Open infrastructure for global collaboration on space situational awareness—built on IPFS
  image:
    src: /hero-image.svg
    alt: Space Data Network
  actions:
    - theme: brand
      text: Get Started
      link: /guide/getting-started
    - theme: alt
      text: View on GitHub
      link: https://github.com/DigitalArsenal/go-space-data-network
    - theme: alt
      text: Downloads
      link: /downloads

features:
  - title: Built on IPFS
    details: Leverages battle-tested IPFS/libp2p networking—DHT discovery, GossipSub messaging, and circuit relay for browsers.
  - title: 32 Space Data Standards
    details: Full support for all Space Data Standards schemas including OMM, CDM, EPM, TDM, and more.
  - title: Transport Encryption
    details: All connections secured with Noise Protocol Framework—forward secrecy and mutual authentication built in.
  - title: Encryption at Rest
    details: WASM-powered AES-256-GCM encryption protects stored data. Argon2 key derivation for password-based security.
  - title: Digital Identity
    details: Ed25519 cryptographic identities with vCard-style Entity Profile Manifests for verified organizational data.
  - title: Cross-Platform
    details: Run full nodes on servers, edge relays on embedded devices, or connect directly from browsers.
---

<style>
:root {
  --vp-home-hero-name-color: transparent;
  --vp-home-hero-name-background: -webkit-linear-gradient(120deg, #1a1a2e 30%, #4a4a8a);
  --vp-home-hero-image-background-image: linear-gradient(-45deg, #1a1a2e50 50%, #4a4a8a50 50%);
  --vp-home-hero-image-filter: blur(40px);
}
</style>

## Quick Example

### Server (Go)

```bash
# Install and run a full node
curl -sSL https://spacedatanetwork.org/install.sh | bash
spacedatanetwork daemon
```

### Browser (TypeScript)

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Subscribe to orbital data
node.subscribe('OMM', (data, peer) => {
  console.log(`Received OMM from ${peer}`);
});

// Publish conjunction data
await node.publish('CDM', conjunctionData);
```

## By the Numbers

<div class="stats-grid">
  <div class="stat-card">
    <div class="stat-number">32</div>
    <div class="stat-label">Space Data Standards</div>
  </div>
  <div class="stat-card">
    <div class="stat-number">IPFS</div>
    <div class="stat-label">Built on libp2p</div>
  </div>
  <div class="stat-card">
    <div class="stat-number">P2P</div>
    <div class="stat-label">Fully Decentralized</div>
  </div>
  <div class="stat-card">
    <div class="stat-number">Open</div>
    <div class="stat-label">Source & Standards</div>
  </div>
</div>

<style>
.stats-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 1rem;
  margin: 2rem 0;
}

@media (max-width: 768px) {
  .stats-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}

.stat-card {
  background: var(--vp-c-bg-soft);
  border-radius: 8px;
  padding: 1.5rem;
  text-align: center;
}

.stat-number {
  font-size: 2rem;
  font-weight: bold;
  color: var(--vp-c-brand);
}

.stat-label {
  font-size: 0.875rem;
  color: var(--vp-c-text-2);
  margin-top: 0.5rem;
}
</style>

## The Vision: Open Space Traffic Management

As space becomes increasingly congested, coordination is essential. SDN provides **open infrastructure** that enables:

- **Space Agencies** - Share conjunction warnings and tracking data globally without intermediaries
- **Satellite Operators** - Coordinate maneuvers and publish orbital elements in real-time
- **STM Providers** - Build services on open, verifiable data from across the ecosystem
- **Researchers** - Access live space data for analysis and algorithm development
- **New Entrants** - Participate in space operations without expensive proprietary integrations

**No central authority. No single point of failure. Just open collaboration.**

Learn more about the [IPFS foundation](/guide/what-is-sdn#built-on-ipfs) that makes this possible.

## Ready to Get Started?

<div class="cta-buttons">
  <a href="/guide/getting-started" class="cta-button primary">Read the Guide</a>
  <a href="/downloads" class="cta-button secondary">Download Now</a>
</div>

<style>
.cta-buttons {
  display: flex;
  gap: 1rem;
  justify-content: center;
  margin: 2rem 0;
}

.cta-button {
  padding: 0.75rem 1.5rem;
  border-radius: 8px;
  font-weight: 600;
  text-decoration: none;
  transition: all 0.2s;
}

.cta-button.primary {
  background: var(--vp-c-brand);
  color: white;
}

.cta-button.primary:hover {
  background: var(--vp-c-brand-dark);
}

.cta-button.secondary {
  background: var(--vp-c-bg-soft);
  color: var(--vp-c-text-1);
  border: 1px solid var(--vp-c-divider);
}

.cta-button.secondary:hover {
  border-color: var(--vp-c-brand);
}
</style>
