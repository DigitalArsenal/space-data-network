<script setup lang="ts">
import { useData } from 'vitepress'
import HomeHero from './HomeHero.vue'

const { frontmatter } = useData()
</script>

<template>
  <div class="home-layout">
    <HomeHero />

    <!-- Features Section -->
    <section class="features-section">
      <div class="section-container">
        <h2 class="section-title">Built for the Future of Space</h2>
        <div class="features-grid">
          <div class="feature-card">
            <div class="feature-icon">
              <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <circle cx="12" cy="12" r="10"/>
                <path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
              </svg>
            </div>
            <h3 class="feature-title">Built on IPFS</h3>
            <p class="feature-description">
              Leverages battle-tested IPFS/libp2p networking with DHT discovery, GossipSub messaging, and circuit relay for browsers.
            </p>
          </div>

          <div class="feature-card">
            <div class="feature-icon">
              <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
                <polyline points="7.5 4.21 12 6.81 16.5 4.21"/>
                <polyline points="7.5 19.79 7.5 14.6 3 12"/>
                <polyline points="21 12 16.5 14.6 16.5 19.79"/>
                <polyline points="3.27 6.96 12 12.01 20.73 6.96"/>
                <line x1="12" y1="22.08" x2="12" y2="12"/>
              </svg>
            </div>
            <h3 class="feature-title">32 Space Data Standards</h3>
            <p class="feature-description">
              Full support for all Space Data Standards schemas including OMM, CDM, EPM, TDM, and more.
            </p>
          </div>

          <div class="feature-card">
            <div class="feature-icon">
              <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/>
                <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
              </svg>
            </div>
            <h3 class="feature-title">Transport Encryption</h3>
            <p class="feature-description">
              All connections secured with Noise Protocol Framework providing forward secrecy and mutual authentication.
            </p>
          </div>

          <div class="feature-card">
            <div class="feature-icon">
              <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>
              </svg>
            </div>
            <h3 class="feature-title">Encryption at Rest</h3>
            <p class="feature-description">
              WASM-powered AES-256-GCM encryption protects stored data with Argon2 key derivation for password-based security.
            </p>
          </div>

          <div class="feature-card">
            <div class="feature-icon">
              <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/>
                <circle cx="12" cy="7" r="4"/>
              </svg>
            </div>
            <h3 class="feature-title">Digital Identity</h3>
            <p class="feature-description">
              Ed25519 cryptographic identities with vCard-style Entity Profile Manifests for verified organizational data.
            </p>
          </div>

          <div class="feature-card">
            <div class="feature-icon">
              <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <rect x="2" y="3" width="20" height="14" rx="2" ry="2"/>
                <line x1="8" y1="21" x2="16" y2="21"/>
                <line x1="12" y1="17" x2="12" y2="21"/>
              </svg>
            </div>
            <h3 class="feature-title">Cross-Platform</h3>
            <p class="feature-description">
              Run full nodes on servers, edge relays on embedded devices, or connect directly from browsers.
            </p>
          </div>
        </div>
      </div>
    </section>

    <!-- Stats Section -->
    <section class="stats-section">
      <div class="section-container">
        <div class="stats-grid">
          <div class="stat-item">
            <div class="stat-number">32</div>
            <div class="stat-label">Space Data Standards</div>
          </div>
          <div class="stat-item">
            <div class="stat-number">IPFS</div>
            <div class="stat-label">Built on libp2p</div>
          </div>
          <div class="stat-item">
            <div class="stat-number">P2P</div>
            <div class="stat-label">Fully Decentralized</div>
          </div>
          <div class="stat-item">
            <div class="stat-number">Open</div>
            <div class="stat-label">Source & Standards</div>
          </div>
        </div>
      </div>
    </section>

    <!-- Code Example Section -->
    <section class="code-section">
      <div class="section-container">
        <h2 class="section-title">Quick Start</h2>
        <div class="code-grid">
          <div class="code-block">
            <div class="code-header">
              <span class="code-lang">Server (Go)</span>
            </div>
            <pre><code># Install and run a full node
curl -sSL https://spacedatanetwork.org/install.sh | bash
spacedatanetwork daemon</code></pre>
          </div>
          <div class="code-block">
            <div class="code-header">
              <span class="code-lang">Browser (TypeScript)</span>
            </div>
            <pre><code>import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Subscribe to orbital data
node.subscribe('OMM', (data, peer) => {
  console.log(`Received OMM from ${peer}`);
});

// Publish conjunction data
await node.publish('CDM', conjunctionData);</code></pre>
          </div>
        </div>
      </div>
    </section>

    <!-- Vision Section -->
    <section class="vision-section">
      <div class="section-container">
        <h2 class="section-title">The Vision: Open Space Traffic Management</h2>
        <p class="vision-intro">
          As space becomes increasingly congested, coordination is essential. SDN provides <strong>open infrastructure</strong> that enables:
        </p>
        <div class="vision-grid">
          <div class="vision-item">
            <h4>Space Agencies</h4>
            <p>Share conjunction warnings and tracking data globally without intermediaries</p>
          </div>
          <div class="vision-item">
            <h4>Satellite Operators</h4>
            <p>Coordinate maneuvers and publish orbital elements in real-time</p>
          </div>
          <div class="vision-item">
            <h4>STM Providers</h4>
            <p>Build services on open, verifiable data from across the ecosystem</p>
          </div>
          <div class="vision-item">
            <h4>Researchers</h4>
            <p>Access live space data for analysis and algorithm development</p>
          </div>
          <div class="vision-item">
            <h4>New Entrants</h4>
            <p>Participate in space operations without expensive proprietary integrations</p>
          </div>
        </div>
        <p class="vision-tagline">
          No central authority. No single point of failure. Just open collaboration.
        </p>
      </div>
    </section>

    <!-- CTA Section -->
    <section class="cta-section">
      <div class="section-container">
        <h2 class="section-title">Ready to Get Started?</h2>
        <div class="cta-buttons">
          <a href="/guide/getting-started" class="cta-button primary">Read the Guide</a>
          <a href="/downloads" class="cta-button secondary">Download Now</a>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.home-layout {
  background: #000000;
}

/* Section Styles */
section {
  padding: 160px 0;
}

.section-container {
  max-width: 1440px;
  margin: 0 auto;
  padding: 0 64px;
}

.section-title {
  font-size: 56px;
  font-weight: 600;
  line-height: 1.1;
  color: #F5F5F7;
  text-align: center;
  margin: 0 0 64px;
  letter-spacing: -0.02em;
}

/* Features Section */
.features-section {
  background: #000000;
}

.features-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 24px;
}

.feature-card {
  background: rgba(42, 42, 45, 0.72);
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 28px;
  padding: 32px;
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  transition: all 0.2s ease;
}

.feature-card:hover {
  background: rgba(66, 66, 69, 0.72);
  border-color: rgba(134, 134, 139, 0.5);
}

.feature-icon {
  width: 56px;
  height: 56px;
  background: rgba(42, 42, 45, 0.72);
  border-radius: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  margin-bottom: 20px;
  color: #F5F5F7;
}

.feature-title {
  font-size: 28px;
  font-weight: 600;
  line-height: 1.15;
  color: #F5F5F7;
  margin: 0 0 12px;
}

.feature-description {
  font-size: 18px;
  font-weight: 400;
  line-height: 1.5;
  color: rgba(255, 255, 255, 0.8);
  margin: 0;
}

/* Stats Section */
.stats-section {
  background: #000000;
  padding: 124px 0;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 24px;
}

.stat-item {
  text-align: center;
  padding: 32px;
  background: rgba(42, 42, 45, 0.72);
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 28px;
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
}

.stat-number {
  font-size: 48px;
  font-weight: 600;
  color: #F5F5F7;
  line-height: 1.1;
}

.stat-label {
  font-size: 18px;
  color: rgba(134, 134, 139, 1.0);
  margin-top: 8px;
}

/* Code Section */
.code-section {
  background: #000000;
}

.code-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 24px;
}

.code-block {
  background: rgba(42, 42, 45, 0.72);
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 28px;
  overflow: hidden;
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
}

.code-header {
  padding: 16px 24px;
  border-bottom: 1px solid rgba(134, 134, 139, 0.2);
}

.code-lang {
  font-size: 14px;
  font-weight: 600;
  color: rgba(134, 134, 139, 1.0);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.code-block pre {
  margin: 0;
  padding: 24px;
  overflow-x: auto;
}

.code-block code {
  font-family: 'SF Mono', 'Fira Code', 'Monaco', monospace;
  font-size: 14px;
  line-height: 1.6;
  color: #F5F5F7;
}

/* Vision Section */
.vision-section {
  background: #000000;
}

.vision-intro {
  font-size: 22px;
  line-height: 1.4;
  color: rgba(255, 255, 255, 0.8);
  text-align: center;
  max-width: 800px;
  margin: 0 auto 48px;
}

.vision-intro strong {
  color: #F5F5F7;
}

.vision-grid {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 24px;
  margin-bottom: 48px;
}

.vision-item {
  text-align: center;
  padding: 24px;
}

.vision-item h4 {
  font-size: 18px;
  font-weight: 600;
  color: #F5F5F7;
  margin: 0 0 8px;
}

.vision-item p {
  font-size: 16px;
  color: rgba(134, 134, 139, 1.0);
  margin: 0;
  line-height: 1.4;
}

.vision-tagline {
  font-size: 28px;
  font-weight: 600;
  color: #F5F5F7;
  text-align: center;
  margin: 0;
}

/* CTA Section */
.cta-section {
  background: #000000;
  padding: 124px 0 160px;
}

.cta-buttons {
  display: flex;
  gap: 16px;
  justify-content: center;
}

.cta-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 16px 32px;
  font-size: 18px;
  font-weight: 600;
  text-decoration: none;
  border-radius: 28px;
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  transition: all 0.2s ease;
}

.cta-button.primary {
  background: rgba(42, 42, 45, 0.72);
  border: 1px solid rgba(134, 134, 139, 0.3);
  color: #F5F5F7;
}

.cta-button.primary:hover {
  background: rgba(66, 66, 69, 0.72);
  border-color: rgba(134, 134, 139, 0.5);
}

.cta-button.secondary {
  background: transparent;
  border: 1px solid rgba(134, 134, 139, 0.3);
  color: #F5F5F7;
}

.cta-button.secondary:hover {
  background: rgba(42, 42, 45, 0.72);
  border-color: rgba(134, 134, 139, 0.5);
}

/* Responsive */
@media (max-width: 1068px) {
  section {
    padding: 124px 0;
  }

  .section-container {
    padding: 0 32px;
  }

  .section-title {
    font-size: 48px;
    margin-bottom: 48px;
  }

  .features-grid {
    grid-template-columns: repeat(2, 1fr);
  }

  .stats-grid {
    grid-template-columns: repeat(2, 1fr);
  }

  .code-grid {
    grid-template-columns: 1fr;
  }

  .vision-grid {
    grid-template-columns: repeat(3, 1fr);
  }
}

@media (max-width: 734px) {
  section {
    padding: 80px 0;
  }

  .section-container {
    padding: 0 24px;
  }

  .section-title {
    font-size: 36px;
    margin-bottom: 32px;
  }

  .features-grid {
    grid-template-columns: 1fr;
  }

  .stats-grid {
    grid-template-columns: repeat(2, 1fr);
  }

  .stat-number {
    font-size: 32px;
  }

  .vision-grid {
    grid-template-columns: 1fr;
    gap: 16px;
  }

  .vision-tagline {
    font-size: 22px;
  }

  .cta-buttons {
    flex-direction: column;
    align-items: center;
  }

  .cta-button {
    width: 100%;
    max-width: 280px;
  }
}
</style>
