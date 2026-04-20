import {
  accountIconSvg,
  brandMarkSvg,
  connectIconSvg,
  directoryIconSvg,
  featureCarouselArrowSvg,
  frontendIconSvg,
  ipfsDashboardIconSvg,
  networkIconSvg,
  pinningIconSvg,
  refreshIconSvg,
  searchIconSvg,
  storeIconSvg,
  walletIconSvg,
} from './icons';

export interface RenderAppShellOptions {
  mountWalletUI?: (host: HTMLElement) => unknown;
}

interface NavItem {
  id: string;
  label: string;
  icon: string;
  href?: string;
}

interface FeatureSlideLink {
  label: string;
  workspaceId?: string;
  href?: string;
}

interface FeatureSlide {
  id: string;
  title: string;
  summary: string;
  links: FeatureSlideLink[];
}

const navItems: NavItem[] = [
  { id: 'network', label: 'Network', icon: networkIconSvg },
  { id: 'directory', label: 'Directory', icon: directoryIconSvg },
  { id: 'store', label: 'Store', icon: storeIconSvg },
  { id: 'pinning', label: 'Pinning', icon: pinningIconSvg },
  { id: 'frontend', label: 'Frontend', icon: frontendIconSvg },
  { id: 'wallet', label: 'Wallet', icon: walletIconSvg },
  { id: 'ipfs-dashboard', label: 'IPFS', icon: ipfsDashboardIconSvg, href: '/webui/' },
];

const featureSlides: FeatureSlide[] = [
  {
    id: 'marketplace',
    title: 'Marketplace Search',
    summary:
      'Search live authors, plugins, and SDS-linked data references from signed PLG metadata instead of a parallel listing format.',
    links: [
      { label: 'Open Store', workspaceId: 'store' },
      { label: 'Open Pinning', workspaceId: 'pinning' },
    ],
  },
  {
    id: 'directory',
    title: 'Active Directory + Trust',
    summary:
      'Track peers, users, runtime roles, and node context through wallet-backed identity, server rosters, and observed SDN discovery evidence.',
    links: [
      { label: 'Open Directory', workspaceId: 'directory' },
      { label: 'Open Wallet', workspaceId: 'wallet' },
    ],
  },
  {
    id: 'pinning',
    title: 'Pinning Rules',
    summary:
      'Apply schema-aware pinning policy by SDS standard, publisher, and node role so live operators can retain what matters without manual block micromanagement.',
    links: [
      { label: 'Open Pinning', workspaceId: 'pinning' },
      { label: 'Open IPFS Dashboard', href: '/webui/' },
    ],
  },
  {
    id: 'workspace',
    title: 'Browser Workspace',
    summary:
      'Edit the public frontend with Monaco, drag-drop uploads, and the same shell against either the browser-local Helia backend or a connected server node.',
    links: [
      { label: 'Open Frontend', workspaceId: 'frontend' },
      { label: 'Open Network', workspaceId: 'network' },
    ],
  },
  {
    id: 'identity',
    title: 'Wallet + Identity',
    summary:
      'Reuse hd-wallet-wasm for addresses, deterministic SSH material, signatures, vCards, node lookup, and the shared account surface across local and server modes.',
    links: [
      { label: 'Open Wallet', workspaceId: 'wallet' },
      { label: 'Open Directory', workspaceId: 'directory' },
    ],
  },
];

export async function renderAppShell(
  root: HTMLElement,
  options: RenderAppShellOptions = {},
): Promise<void> {
  void options;
  root.innerHTML = `
    <main class="sdn-admin-shell">
      <section class="sdn-admin-main">
        <header class="sdn-admin-topbar">
          <div class="sdn-admin-topbar__primary">
            <label class="sdn-command-bar" aria-label="Provider descriptor URL">
              <span class="sdn-command-bar__icon">${brandMarkSvg}</span>
              <input
                id="sdn-provider-url"
                type="url"
                placeholder="https://server.example/api/module-delivery/provider"
              />
              <button id="sdn-refresh-provider" type="button" class="sdn-command-bar__action">
                ${refreshIconSvg}
                <span>Refresh</span>
              </button>
            </label>
            <div class="sdn-admin-meta">
              <span class="sdn-chip" id="sdn-active-target">Local backend</span>
              <span class="sdn-chip" id="sdn-connection-status-top">Idle</span>
            </div>
          </div>
          <div class="sdn-admin-topbar__actions">
            <button id="sdn-mode-switch" type="button" class="sdn-ghost-button">Local</button>
            <button id="sdn-connect-server" type="button" class="sdn-ghost-button">
              ${connectIconSvg}
              <span>Connect Server</span>
            </button>
            <a class="sdn-ghost-button sdn-ghost-button--link" href="/webui/" target="_blank" rel="noreferrer">
              <span>IPFS Dashboard</span>
            </a>
            <button id="sdn-account-button" type="button" class="sdn-account-button" aria-label="Account">
              ${accountIconSvg}
            </button>
          </div>
        </header>

        <div class="sdn-admin-page">
          <section class="sdn-admin-workspace sdn-admin-workspace--active" data-workspace="network">
            <div class="sdn-hero">
              <div class="sdn-hero__copy">
                <p class="sdn-kicker">Space Data Network</p>
                <h1>A peer-to-peer control plane for space data, software modules, and signed identity.</h1>
                <p class="sdn-copy">
                  Space Data Network combines SDS records, libp2p and IPFS transport, encrypted WASM module delivery,
                  wallet-backed identity, and isomorphic browser/server operation so operators can publish, discover,
                  verify, run, and manage space software and data without a central broker.
                </p>
                <section id="sdn-feature-carousel" class="sdn-feature-carousel" aria-label="Space Data Network feature tour">
                  <div class="sdn-feature-carousel__viewport">
                    <button type="button" class="sdn-feature-carousel__arrow sdn-feature-carousel__arrow--prev" data-feature-prev aria-label="Previous feature">
                      ${featureCarouselArrowSvg}
                    </button>
                    <div class="sdn-feature-carousel__slides">
                      ${featureSlides.map((slide, index) => `
                        <article
                          id="sdn-feature-slide-${slide.id}"
                          class="sdn-feature-slide${index === 0 ? ' sdn-feature-slide--active' : ''}"
                          data-feature-slide="${slide.id}"
                          role="tabpanel"
                          aria-hidden="${index === 0 ? 'false' : 'true'}"
                        >
                          <div class="sdn-feature-slide__body">
                            <h2>${slide.title}</h2>
                            <p>${slide.summary}</p>
                          </div>
                          <div class="sdn-feature-slide__links">
                            ${slide.links.map((link) => renderFeatureLink(link)).join('')}
                          </div>
                        </article>
                      `).join('')}
                    </div>
                    <button type="button" class="sdn-feature-carousel__arrow sdn-feature-carousel__arrow--next" data-feature-next aria-label="Next feature">
                      ${featureCarouselArrowSvg}
                    </button>
                  </div>
                  <div class="sdn-feature-carousel__indicators" role="tablist" aria-label="Feature slides">
                    ${featureSlides.map((slide, index) => `
                      <button
                        type="button"
                        class="sdn-feature-carousel__indicator${index === 0 ? ' sdn-feature-carousel__indicator--active' : ''}"
                        data-feature-target="${slide.id}"
                        role="tab"
                        aria-label="Go to feature ${index + 1}"
                        aria-selected="${index === 0 ? 'true' : 'false'}"
                        aria-controls="sdn-feature-slide-${slide.id}"
                      ></button>
                    `).join('')}
                  </div>
                </section>
              </div>
              <div class="sdn-hero__summary">
                <div class="sdn-metric-card">
                  <span class="sdn-metric-card__label">Observed SDN peers</span>
                  <strong id="sdn-observed-peer-count">0</strong>
                </div>
                <div class="sdn-metric-card">
                  <span class="sdn-metric-card__label">Connection status</span>
                  <strong id="sdn-connection-status">Idle</strong>
                </div>
              </div>
            </div>

            <div class="sdn-grid sdn-grid--network">
              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Provider Descriptor</h2>
                  <span class="sdn-chip">Seed + DHT</span>
                </div>
                <p class="sdn-copy">
                  The live provider descriptor carries the transport trust root plus the node&apos;s published identity:
                  peer routing, major chain addresses, IPNS entries, and any ENS names surfaced through the node&apos;s SDS identity data.
                </p>
                <pre id="sdn-provider-descriptor" class="sdn-code">Awaiting live discovery</pre>
              </article>

              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Recent Sightings</h2>
                  <span class="sdn-chip">Observed SDN peers</span>
                </div>
                <div id="sdn-sightings" class="sdn-stack">
                  <div class="sdn-empty">DHT, provider, and protocol evidence will stream here.</div>
                </div>
              </article>

              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Lookup Node</h2>
                  <span class="sdn-chip">Address-based</span>
                </div>
                <div class="sdn-control-grid">
                  <label class="sdn-field">
                    <span>Lookup chain</span>
                    <select id="sdn-address-lookup-chain">
                      <option value="bitcoin">bitcoin</option>
                      <option value="ethereum">ethereum</option>
                      <option value="solana">solana</option>
                    </select>
                  </label>
                  <label class="sdn-field">
                    <span>Blockchain address</span>
                    <input id="sdn-address-lookup-value" type="text" placeholder="bc1..., 0x..., or sol..." />
                  </label>
                </div>
                <div class="sdn-action-row">
                  <button id="sdn-address-lookup-run" type="button" class="sdn-button">Lookup node</button>
                </div>
              </article>

              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Raw Technical Detail</h2>
                  <span class="sdn-chip">Runtime</span>
                </div>
                <pre id="sdn-raw-event-detail" class="sdn-code">Waiting for live protocol events.</pre>
              </article>

              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Module Workflow</h2>
                  <span class="sdn-chip">Live comms</span>
                </div>
                <div id="sdn-delivery-timeline" class="sdn-empty">
                  Challenge, grant, fetch, decrypt, load, and invoke events appear in order.
                </div>
              </article>

              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Completion State</h2>
                  <span class="sdn-chip">Browser + node</span>
                </div>
                <div id="sdn-completion-state" class="sdn-empty">
                  Select a plugin in the Store and run the live workflow to stream completion state here.
                </div>
              </article>
            </div>
          </section>

          <section class="sdn-admin-workspace" data-workspace="directory">
            <div class="sdn-grid">
              <article class="sdn-panel sdn-panel--directory">
                <div class="sdn-panel__header">
                  <h2>Active Directory</h2>
                  <span class="sdn-chip">People + Nodes</span>
                </div>
                <p class="sdn-copy">
                  Identity, permissions, and node context follow the selected backend. In server mode, the roster and role model change with the target node.
                </p>
                <div id="sdn-directory-panel" class="sdn-stack">
                  <div class="sdn-empty">Directory data will appear here.</div>
                </div>
              </article>
            </div>
          </section>

          <section class="sdn-admin-workspace" data-workspace="store">
            <div class="sdn-grid sdn-grid--store">
              <article class="sdn-panel sdn-panel--store-results">
                <div class="sdn-panel__header">
                  <h2>Store</h2>
                  <span class="sdn-chip">Steam-style</span>
                </div>
                <p class="sdn-copy">
                  Canonical storefront search comes from signed PLG metadata. Search across authors, plugins, and SDS-linked data references from live manifests.
                </p>
                <div class="sdn-store-toolbar">
                  <label class="sdn-store-search" aria-label="Search the live store">
                    <span class="sdn-store-search__icon">${searchIconSvg}</span>
                    <input
                      id="sdn-store-search"
                      type="search"
                      placeholder="Search by author, plugin, data, or SDS standard"
                    />
                  </label>
                  <button id="sdn-refresh-marketplace" type="button" class="sdn-button">Refresh listings</button>
                </div>
                <div id="sdn-store-results" class="sdn-store-results">
                  <div id="sdn-store-spotlight" class="sdn-stack"></div>
                  <div id="sdn-store-feed" class="sdn-stack"></div>
                </div>
              </article>

              <article class="sdn-panel sdn-panel--store-detail">
                <div class="sdn-panel__header">
                  <h2>Selection Detail</h2>
                  <span class="sdn-chip">Author + plugin + data</span>
                </div>
                <div id="sdn-store-detail" class="sdn-stack">
                  <div class="sdn-empty">
                    Select an author, plugin, or SDS data standard to inspect its live metadata, related standards, and available actions.
                  </div>
                </div>
              </article>
            </div>
          </section>

          <section class="sdn-admin-workspace" data-workspace="pinning">
            <div class="sdn-grid sdn-grid--pinning">
              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Pinning Rules</h2>
                  <span class="sdn-chip">Schema + source</span>
                </div>
                <p class="sdn-copy">
                  Pinning policy should be driven by Space Data Standards, publisher identity, and node context instead of arbitrary CID bookkeeping.
                </p>
                <div class="sdn-control-grid">
                  <label class="sdn-field">
                    <span>SDS standard</span>
                    <select id="sdn-pinning-standard">
                      <option value="OMM">OMM</option>
                      <option value="OEM">OEM</option>
                      <option value="CDM">CDM</option>
                      <option value="TDM">TDM</option>
                    </select>
                  </label>
                  <label class="sdn-field">
                    <span>Publisher or peer</span>
                    <input id="sdn-pinning-peer" type="text" placeholder="16Uiu2..., xpub..., or publisher handle" />
                  </label>
                  <label class="sdn-field">
                    <span>Rule action</span>
                    <select id="sdn-pinning-action">
                      <option value="pin">Pin and retain</option>
                      <option value="cache">Cache until expiry</option>
                      <option value="ignore">Observe only</option>
                    </select>
                  </label>
                  <label class="sdn-field">
                    <span>Retention TTL</span>
                    <input id="sdn-pinning-ttl" type="text" value="168h" />
                  </label>
                </div>
                <div class="sdn-action-row">
                  <button type="button" class="sdn-button" data-workspace-link="store">Browse store references</button>
                  <a class="sdn-ghost-button sdn-ghost-button--link" href="/webui/" target="_blank" rel="noreferrer">
                    <span>Open IPFS Dashboard</span>
                  </a>
                </div>
                <div id="sdn-pinning-rules" class="sdn-stack">
                  <div class="sdn-empty">
                    No live pinning rules loaded. Rules will scope by SDS standard, publisher identity, and server/local node context.
                  </div>
                </div>
              </article>

              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Rule Model</h2>
                  <span class="sdn-chip">Policy</span>
                </div>
                <div class="sdn-stack">
                  <div class="sdn-empty">Prioritize schema-aware rules first, then source-specific overrides, then node-default retention.</div>
                  <div class="sdn-empty">Use the Store to discover which plugins and publishers reference OMM, OEM, CDM, TDM, and other SDS records before creating retention policy.</div>
                  <div class="sdn-empty">The IPFS dashboard remains available for raw block inspection, pin status, and lower-level operational detail.</div>
                </div>
              </article>
            </div>
          </section>

          <section class="sdn-admin-workspace" data-workspace="frontend">
            <article class="sdn-panel">
              <div class="sdn-panel__header">
                <h2>Frontend</h2>
                <span class="sdn-chip">Browser IDE</span>
              </div>
              <p class="sdn-copy">
                Monaco editor, drag-drop upload, and the browser/server workspace share the same shell so local Helia and remote nodes feel identical.
              </p>
              <div id="sdn-frontend-workspace" class="sdn-frontend-shell">
                <aside class="sdn-frontend-shell__sidebar">
                  <div class="sdn-frontend-shell__toolbar">
                    <button id="sdn-frontend-upload" type="button" class="sdn-button">Upload</button>
                    <input id="sdn-frontend-upload-input" type="file" multiple hidden />
                    <button id="sdn-frontend-save" type="button" class="sdn-ghost-button">Save</button>
                  </div>
                  <div id="sdn-frontend-status" class="sdn-empty">Connect to a backend to manage the public frontend.</div>
                  <div id="sdn-frontend-tree" class="sdn-frontend-tree" aria-label="Frontend file tree"></div>
                </aside>
                <section class="sdn-frontend-shell__editor">
                  <div class="sdn-frontend-shell__toolbar">
                    <input id="sdn-frontend-path" type="text" placeholder="path/to/file.ts" />
                    <button id="sdn-frontend-move" type="button" class="sdn-ghost-button">Move</button>
                    <button id="sdn-frontend-delete" type="button" class="sdn-ghost-button">Delete</button>
                  </div>
                  <div id="sdn-frontend-editor" class="sdn-frontend-editor">
                    <div class="sdn-empty">Select a file to open the editor.</div>
                  </div>
                </section>
              </div>
            </article>
          </section>
        </div>

        <div id="sdn-wallet-modal-host" class="sdn-wallet-modal-host" aria-hidden="true"></div>
      </section>

      <aside class="sdn-admin-rail">
        <a class="sdn-admin-brand" href="#/network" aria-label="Space Data Network Admin">
          <span class="sdn-admin-brand__mark">${brandMarkSvg}</span>
          <span class="sdn-admin-brand__wordmark">
            <strong>Space Data</strong>
            <span>Network</span>
          </span>
        </a>
        <nav class="sdn-admin-nav" aria-label="Primary">
          ${navItems.map((item) => renderNavItem(item)).join('')}
        </nav>
        <div class="sdn-admin-rail__footer">
          <span>Server + Local</span>
          <span>Isomorphic shell</span>
        </div>
      </aside>
    </main>
  `;
}

function renderNavItem(item: NavItem): string {
  if (item.href) {
    return `
      <a
        class="sdn-admin-nav__item sdn-admin-nav__item--link"
        data-nav="${item.id}"
        href="${item.href}"
        target="_blank"
        rel="noreferrer"
        title="${item.label}"
      >
        <span class="sdn-admin-nav__icon">${item.icon}</span>
        <span class="sdn-admin-nav__label">${item.label}</span>
      </a>
    `;
  }

  return `
    <button
      type="button"
      class="sdn-admin-nav__item${item.id === 'network' ? ' sdn-admin-nav__item--active' : ''}"
      data-nav="${item.id}"
      title="${item.label}"
    >
      <span class="sdn-admin-nav__icon">${item.icon}</span>
      <span class="sdn-admin-nav__label">${item.label}</span>
    </button>
  `;
}

function renderFeatureLink(link: FeatureSlideLink): string {
  if (link.href) {
    return `
      <a class="sdn-link-pill" href="${link.href}" target="_blank" rel="noreferrer">
        ${link.label}
      </a>
    `;
  }

  return `
    <button type="button" class="sdn-link-pill" data-workspace-link="${link.workspaceId}">
      ${link.label}
    </button>
  `;
}
