import type { MountedWalletUI } from '../../src/ui/runtime/wallet-ui';

export interface RenderAppShellOptions {
  mountWalletUI?: (host: HTMLElement) => MountedWalletUI | void | Promise<MountedWalletUI | void>;
}

export async function renderAppShell(
  root: HTMLElement,
  options: RenderAppShellOptions = {},
): Promise<void> {
  root.innerHTML = `
    <main class="sdn-admin-shell">
      <aside class="sdn-admin-rail">
        <div class="sdn-admin-brand">
          <p class="sdn-kicker">Space Data Network</p>
          <strong>Admin</strong>
        </div>
        <nav class="sdn-admin-nav" aria-label="Primary">
          <button type="button" class="sdn-admin-nav__item sdn-admin-nav__item--active" data-nav="network">Network</button>
          <button type="button" class="sdn-admin-nav__item" data-nav="directory">Directory</button>
          <button type="button" class="sdn-admin-nav__item" data-nav="store">Store</button>
          <button type="button" class="sdn-admin-nav__item" data-nav="frontend">Frontend</button>
          <button type="button" class="sdn-admin-nav__item" data-nav="wallet">Wallet</button>
          <a class="sdn-admin-nav__item sdn-admin-nav__item--link" data-nav="ipfs-dashboard" href="/webui/" target="_blank" rel="noreferrer">IPFS Dashboard</a>
        </nav>
      </aside>

      <section class="sdn-admin-main">
        <header class="sdn-admin-topbar">
          <div class="sdn-admin-topbar__copy">
            <p class="sdn-kicker">Isomorphic control shell</p>
            <h1>Run SDN from the browser or attach to a server node.</h1>
            <p class="sdn-copy">
              Shared admin surface for live delivery, linked module/data discovery, and browser-first identity.
            </p>
            <div class="sdn-admin-meta">
              <span class="sdn-chip" id="sdn-active-target">Local backend</span>
              <span class="sdn-chip" id="sdn-connection-status-top">Idle</span>
            </div>
          </div>
          <div class="sdn-admin-topbar__actions">
            <button id="sdn-mode-switch" type="button" class="sdn-ghost-button">Local</button>
            <button id="sdn-connect-server" type="button" class="sdn-ghost-button">Connect Server</button>
            <button id="sdn-account-button" type="button" class="sdn-button sdn-button--dark">Account</button>
          </div>
        </header>

        <section class="sdn-admin-workspace sdn-admin-workspace--active" data-workspace="network">
          <div class="sdn-grid">
            <article class="sdn-panel">
              <div class="sdn-panel__header">
                <h2>Network</h2>
                <span class="sdn-chip">Observed SDN peers</span>
              </div>
              <div class="sdn-control-grid">
                <label class="sdn-field">
                  <span>Provider descriptor URL</span>
                  <input id="sdn-provider-url" type="url" placeholder="https://server.example/api/module-delivery/provider" />
                </label>
                <div class="sdn-action-row">
                  <button id="sdn-refresh-provider" type="button" class="sdn-button">Load live provider</button>
                </div>
              </div>
              <div class="sdn-metric">
                <strong id="sdn-observed-peer-count">0</strong>
                <span>Observed SDN peers</span>
              </div>
              <dl class="sdn-detail-list">
                <div><dt>Provider descriptor</dt><dd id="sdn-provider-descriptor">Awaiting live discovery</dd></div>
                <div><dt>Connection status</dt><dd id="sdn-connection-status">Idle</dd></div>
                <div><dt>Recent sightings</dt><dd id="sdn-sightings">DHT, provider, and protocol evidence will stream here.</dd></div>
              </dl>
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
                <div class="sdn-action-row">
                  <button id="sdn-address-lookup-run" type="button" class="sdn-button">Lookup node</button>
                </div>
              </div>
            </article>

            <article class="sdn-panel">
              <div class="sdn-panel__header">
                <h2>Directory</h2>
                <span class="sdn-chip">Active Directory</span>
              </div>
              <p class="sdn-copy">
                Peer, publisher, organization, address, and trust relationships will be organized here.
              </p>
              <div id="sdn-directory-panel" class="sdn-stack">
                <div class="sdn-empty">
                  Foundation slice: shell and hosting are landing first. Directory data will move here next.
                </div>
              </div>
            </article>
          </div>
        </section>

        <section class="sdn-admin-workspace" data-workspace="store">
          <div class="sdn-grid sdn-grid--store">
            <article class="sdn-panel">
              <div class="sdn-panel__header">
                <h2>Store</h2>
                <span class="sdn-chip">Modules + Data</span>
              </div>
              <p class="sdn-copy">
                Linked SDN-native catalogs. Modules and data stay first-class and relationships come from SDS metadata.
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
                <h2>Data Catalog</h2>
                <span class="sdn-chip">SDS-linked</span>
              </div>
              <div id="sdn-data-store-panel" class="sdn-stack">
                <div class="sdn-empty">
                  Data listings and module compatibility edges will appear here as the store model is implemented.
                </div>
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
                  <div class="sdn-action-row">
                    <button id="sdn-run-live-flow" type="button" class="sdn-button">Run live flow</button>
                  </div>
                </section>
                <section>
                  <h3>Timeline</h3>
                  <div id="sdn-delivery-timeline" class="sdn-empty">
                    Challenge, grant, fetch, decrypt, load, and invoke events appear in order.
                  </div>
                </section>
                <section>
                  <h3>Module metadata</h3>
                  <div id="sdn-module-metadata" class="sdn-empty">
                    Canonical PLG metadata, publisher details, and related data listings will appear here.
                  </div>
                </section>
                <section>
                  <h3>Raw technical detail</h3>
                  <pre id="sdn-raw-event-detail" class="sdn-code">Waiting for live protocol events.</pre>
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
              <span class="sdn-chip">Workspace</span>
            </div>
            <p class="sdn-copy">
              Monaco, file tree, drag-drop upload, and wallet-backed git will land on this shared workspace surface.
            </p>
            <div id="sdn-frontend-workspace" class="sdn-stack">
              <div class="sdn-empty">
                Foundation slice: the hosted admin shell is live, and the dedicated IDE workspace lands next.
              </div>
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
              Address lookup, signatures, and the vCard identity flow reuse the canonical wallet UI directly.
            </p>
            <div class="sdn-action-row">
              <button id="sdn-wallet-load" type="button" class="sdn-button">Open wallet identity</button>
            </div>
            <div id="sdn-wallet-panel"></div>
          </article>
        </section>
      </section>
    </main>
  `;

  const walletHost = root.querySelector('#sdn-wallet-panel');
  const walletLoadButton = root.querySelector('#sdn-wallet-load');
  const accountButton = root.querySelector('#sdn-account-button');
  if (walletHost && (walletLoadButton || accountButton)) {
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
    if (mountWalletUI && accountButton && 'addEventListener' in accountButton) {
      accountButton.addEventListener('click', async () => {
        const wallet = await ensureWalletMounted();
        await wallet?.openAccount?.();
      });
    }
  }
}
