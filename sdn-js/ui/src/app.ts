export interface RenderAppShellOptions {
  mountWalletUI?: (host: HTMLElement) => void | Promise<void>;
}

export async function renderAppShell(
  root: HTMLElement,
  options: RenderAppShellOptions = {},
): Promise<void> {
  root.innerHTML = `
    <main class="sdn-shell">
      <header class="sdn-hero">
        <div>
          <p class="sdn-kicker">Space Data Network</p>
          <h1>Live browser control room for real module delivery.</h1>
          <p class="sdn-copy">
            Browser-first SDN UI using Helia, libp2p, and the canonical wallet identity surface.
          </p>
        </div>
        <a class="sdn-webui-link" href="/webui/" target="_blank" rel="noreferrer">Open IPFS WebUI</a>
      </header>

      <section class="sdn-grid">
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
            <h2>Marketplace</h2>
            <span class="sdn-chip">Canonical PLG</span>
          </div>
          <p class="sdn-copy">
            Dynamic module listings keyed by <code>PLUGIN_ID + VERSION</code> from signed PLG manifests.
          </p>
          <div class="sdn-control-grid">
            <label class="sdn-field">
              <span>Live listings</span>
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
                Canonical PLG metadata and provider details will appear here.
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

        <article class="sdn-panel">
          <div class="sdn-panel__header">
            <h2>Identity</h2>
            <span class="sdn-chip">hd-wallet-ui</span>
          </div>
          <p class="sdn-copy">
            Address lookup and vCard identity management reuse the existing wallet UI directly.
          </p>
          <div class="sdn-action-row">
            <button id="sdn-wallet-load" type="button" class="sdn-button">Open wallet identity</button>
          </div>
          <div id="sdn-wallet-panel"></div>
        </article>
      </section>
    </main>
  `;

  const walletHost = root.querySelector('#sdn-wallet-panel');
  const walletLoadButton = root.querySelector('#sdn-wallet-load');
  if (walletHost && walletLoadButton) {
    const mountWalletUI = options.mountWalletUI;
    if (mountWalletUI && 'addEventListener' in walletLoadButton) {
      let mounted = false;
      walletLoadButton.addEventListener('click', async () => {
        if (mounted) {
          return;
        }
        mounted = true;
        await mountWalletUI(walletHost as HTMLElement);
      });
    }
  }
}
