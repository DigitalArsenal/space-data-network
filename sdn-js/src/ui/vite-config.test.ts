import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { afterEach, describe, expect, it, vi } from 'vitest';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../../..');

describe('admin vite config', () => {
  afterEach(() => {
    delete process.env.SDN_UI_PROXY_TARGET;
    vi.resetModules();
  });

  it('proxies the IPFS dashboard paths to the backend in dev', async () => {
    process.env.SDN_UI_PROXY_TARGET = 'http://127.0.0.1:5010';
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const server = typeof config.server === 'function' ? config.server({} as never) : config.server;
    const proxy = server?.proxy;

    expect(proxy).toBeDefined();
    expect(proxy).toHaveProperty('/webui');
    expect(proxy).toHaveProperty('/ipfs');
  });

  it('injects a browser process shim for upstream webui dependencies', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const build = typeof config.build === 'function' ? config.build({} as never) : config.build;
    const output = build?.rollupOptions?.output;
    const banner = Array.isArray(output)
      ? output[0]?.banner
      : output?.banner;

    expect(typeof banner).toBe('string');
    expect(String(banner)).toContain('globalThis.process');
    expect(String(banner)).toContain('var process = globalThis.process');
    expect(String(banner)).toContain('cwd: () => "/"');
    expect(String(banner)).toContain('browser: true');
  });

  it('pins copied upstream bootstrap dependencies to the upstream webui package tree', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const resolve = typeof config.resolve === 'function' ? config.resolve({} as never) : config.resolve;
    const alias = Array.isArray(resolve?.alias) ? resolve.alias : [];

    const reactAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^react$/');
    const bundlerAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^redux-bundler-react$/');
    const walletWasmAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^hd-wallet-wasm$/');

    expect(String(reactAlias?.replacement)).toContain('/webui/node_modules/react');
    expect(String(bundlerAlias?.replacement)).toContain('/webui/node_modules/redux-bundler-react');
    expect(String(walletWasmAlias?.replacement)).toContain('/sdn-js/node_modules/hd-wallet-wasm/src/index.mjs');
  });

  it('routes root-only upstream branding modules to SDN-local overrides', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const plugins = Array.isArray(config.plugins) ? config.plugins : [];
    const brandingPlugin = plugins.find((plugin) => plugin && typeof plugin === 'object' && plugin.name === 'sdn-upstream-webui-root-overrides');
    const appImporter = path.join(repoRoot, 'webui', 'src', 'App.js');
    const bundlesImporter = path.join(repoRoot, 'webui', 'src', 'bundles', 'index.js');
    const statusPageImporter = path.join(repoRoot, 'webui', 'src', 'status', 'StatusPage.js');
    const welcomePageImporter = path.join(repoRoot, 'webui', 'src', 'welcome', 'WelcomePage.js');

    expect(brandingPlugin).toBeDefined();
    expect(typeof brandingPlugin?.resolveId).toBe('function');

    const navOverride = await brandingPlugin?.resolveId?.(
      './navigation/NavBar.js',
      appImporter,
    );
    const statusOverride = await brandingPlugin?.resolveId?.(
      './StatusConnected.js',
      statusPageImporter,
    );
    const nodeInfoOverride = await brandingPlugin?.resolveId?.(
      './NodeInfo.js',
      statusPageImporter,
    );
    const connectedOverride = await brandingPlugin?.resolveId?.(
      './components/connected/Connected.js',
      appImporter,
    );
    const routesOverride = await brandingPlugin?.resolveId?.(
      './routes.js',
      bundlesImporter,
    );
    const welcomeConnectedOverride = await brandingPlugin?.resolveId?.(
      '../components/is-connected/IsConnected.js',
      welcomePageImporter,
    );
    const aboutWebUiOverride = await brandingPlugin?.resolveId?.(
      '../components/about-webui/AboutWebUI.js',
      welcomePageImporter,
    );
    const aboutIpfsOverride = await brandingPlugin?.resolveId?.(
      '../components/about-ipfs/AboutIpfs.js',
      welcomePageImporter,
    );

    expect(String(navOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/navigation/NavBar.js');
    expect(String(statusOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/status/StatusConnected.js');
    expect(String(nodeInfoOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/status/NodeInfo.js');
    expect(String(connectedOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/components/connected/Connected.js');
    expect(String(routesOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/bundles/routes.js');
    expect(String(welcomeConnectedOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/components/is-connected/IsConnected.js');
    expect(String(aboutWebUiOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/components/about-webui/AboutWebUI.js');
    expect(String(aboutIpfsOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/components/about-ipfs/AboutIpfs.js');
  });
});
