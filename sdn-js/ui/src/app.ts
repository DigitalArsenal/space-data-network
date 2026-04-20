import type { MountedWalletUI } from '../../src/ui/runtime/wallet-ui';
import {
  accountIconSvg,
  brandMarkSvg,
  connectIconSvg,
  directoryIconSvg,
  frontendIconSvg,
  ipfsDashboardIconSvg,
  networkIconSvg,
  refreshIconSvg,
  storeIconSvg,
  walletIconSvg,
} from './icons';

export interface RenderAppShellOptions {
  mountWalletUI?: (host: HTMLElement) => MountedWalletUI | void | Promise<MountedWalletUI | void>;
}

interface NavItem {
  id: string;
  label: string;
  icon: string;
  href?: string;
}

const navItems: NavItem[] = [
  { id: 'network', label: 'Network', icon: networkIconSvg },
  { id: 'directory', label: 'Directory', icon: directoryIconSvg },
  { id: 'store', label: 'Store', icon: storeIconSvg },
  { id: 'frontend', label: 'Frontend', icon: frontendIconSvg },
  { id: 'wallet', label: 'Wallet', icon: walletIconSvg },
  { id: 'ipfs-dashboard', label: 'IPFS', icon: ipfsDashboardIconSvg, href: '/webui/' },
];

export async function renderAppShell(
  root: HTMLElement,
  options: RenderAppShellOptions = {},
): Promise<void> {
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

        <section id="sdn-account-dialog" class="sdn-account-dialog" hidden aria-hidden="true">
          <div class="sdn-account-dialog__backdrop" data-account-dismiss="backdrop"></div>
          <div class="sdn-account-dialog__panel" role="dialog" aria-modal="true" aria-labelledby="sdn-account-title">
            <div class="sdn-panel__header">
              <div>
                <p class="sdn-kicker">Wallet + Account</p>
                <h2 id="sdn-account-title">Session</h2>
              </div>
              <button id="sdn-account-close" type="button" class="sdn-ghost-button">Close</button>
            </div>
            <div class="sdn-stack">
              <div id="sdn-account-meta" class="sdn-empty">Wallet state will appear here.</div>
              <div class="sdn-action-row">
                <button id="sdn-account-open-wallet" type="button" class="sdn-button">Open wallet account</button>
                <button id="sdn-account-signout" type="button" class="sdn-ghost-button">Sign out</button>
              </div>
              <div id="sdn-account-wallet-panel"></div>
            </div>
          </div>
        </section>

        <div class="sdn-admin-page">
          <section class="sdn-admin-workspace sdn-admin-workspace--active" data-workspace="network">
            <div class="sdn-hero">
              <div class="sdn-hero__copy">
                <p class="sdn-kicker">Space Data Network</p>
                <h1>Run SDN from the browser or attach to a live node.</h1>
                <p class="sdn-copy">
                  Real module-delivery comms, peer census, provider discovery, and browser-first identity in one shell.
                </p>
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
                  The live provider descriptor drives the real requester flow. Clients seed from the advertised relay and expand from there.
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
              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Store</h2>
                  <span class="sdn-chip">Steam-style</span>
                </div>
                <p class="sdn-copy">
                  Canonical marketplace listings come from signed PLG metadata, not a second listing object.
                </p>
                <div class="sdn-control-grid">
                  <label class="sdn-field">
                    <span>Live module listings</span>
                    <select id="sdn-marketplace-select">
                      <option value="">No live PLG listings loaded</option>
                    </select>
                  </label>
                  <div class="sdn-action-row">
                    <button id="sdn-refresh-marketplace" type="button" class="sdn-button">Refresh listings</button>
                  </div>
                </div>
                <div id="sdn-marketplace-panel" class="sdn-stack">
                  <div class="sdn-empty">Publisher and module listings will populate from live PLG manifests.</div>
                </div>
              </article>

              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Distributed Store</h2>
                  <span class="sdn-chip">Modules + Data</span>
                </div>
                <div id="sdn-data-store-panel" class="sdn-stack">
                  <div class="sdn-empty">
                    SDS-linked data listings, download actions, and pinning hooks will appear here.
                  </div>
                </div>
                <div class="sdn-panel__divider"></div>
                <h3>Module metadata</h3>
                <div id="sdn-module-metadata" class="sdn-empty">
                  Canonical PLG metadata, publisher details, and related data listings will appear here.
                </div>
              </article>

              <article class="sdn-panel">
                <div class="sdn-panel__header">
                  <h2>Delivery</h2>
                  <span class="sdn-chip">Real comms</span>
                </div>
                <div id="sdn-delivery-panel" class="sdn-stack">
                  <section class="sdn-control-grid">
                    <label class="sdn-field">
                      <span>Requester domain</span>
                      <input id="sdn-requester-domain" type="text" value="app.example.com" />
                    </label>
                    <label class="sdn-field">
                      <span>Grant timeout (ms)</span>
                      <input id="sdn-request-timeout" type="number" min="1000" step="1000" value="300000" />
                    </label>
                    <label class="sdn-field">
                      <span>Invoke method</span>
                      <input id="sdn-invoke-method" type="text" value="echo" />
                    </label>
                    <label class="sdn-field">
                      <span>Invoke payload</span>
                      <textarea id="sdn-invoke-payload" rows="3">live browser request</textarea>
                    </label>
                  </section>
                  <div class="sdn-action-row">
                    <button id="sdn-run-live-flow" type="button" class="sdn-button">Run live flow</button>
                  </div>
                  <section>
                    <h3>Timeline</h3>
                    <div id="sdn-delivery-timeline" class="sdn-empty">
                      Challenge, grant, fetch, decrypt, load, and invoke events appear in order.
                    </div>
                  </section>
                  <section>
                    <h3>Completion state</h3>
                    <div id="sdn-completion-state" class="sdn-empty">
                      Decrypt, SDK load, and invoke results will stream here.
                    </div>
                  </section>
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

          <section class="sdn-admin-workspace" data-workspace="wallet">
            <article class="sdn-panel">
              <div class="sdn-panel__header">
                <h2>Wallet</h2>
                <span class="sdn-chip">hd-wallet-ui</span>
              </div>
              <p class="sdn-copy">
                Address lookup, signatures, deterministic SSH identities, and the vCard flow reuse the canonical wallet UI directly.
              </p>
              <div class="sdn-action-row">
                <button id="sdn-wallet-load" type="button" class="sdn-button">Open wallet identity</button>
              </div>
              <div id="sdn-wallet-panel"></div>
            </article>
          </section>
        </div>
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

  const walletHost = root.querySelector('#sdn-wallet-panel');
  const walletLoadButton = root.querySelector('#sdn-wallet-load');
  if (walletHost && walletLoadButton) {
    const mountWalletUI = options.mountWalletUI;
    let mountedWallet: Promise<MountedWalletUI | void> | null = null;
    const ensureWalletMounted = async (): Promise<MountedWalletUI | void> => {
      if (!mountWalletUI) {
        return undefined;
      }
      if (!mountedWallet) {
        mountedWallet = Promise.resolve(mountWalletUI(walletHost as HTMLElement));
      }
      return mountedWallet;
    };
    if (mountWalletUI && walletLoadButton && 'addEventListener' in walletLoadButton) {
      walletLoadButton.addEventListener('click', async () => {
        await ensureWalletMounted();
      });
    }
  }
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
