import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { afterEach, describe, expect, it, vi } from 'vitest';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../../..');

describe('sdn-ui package scripts', () => {
  it('keeps sdn-js/ui as the SDN UI build target and exposes a fast dev script', () => {
    const packageJson = JSON.parse(
      fs.readFileSync(path.resolve(__dirname, '../../package.json'), 'utf8'),
    ) as { scripts: Record<string, string> };

    expect(packageJson.scripts['build:ui']).toBe('vite build --config ui/vite.config.mts');
    expect(packageJson.scripts['build:sdn-ui']).toBe('vite build --config ui/vite.config.mts');
    expect(packageJson.scripts['dev:sdn-ui']).toBe('vite --config ui/vite.config.mts');
    expect(packageJson.scripts['check:sdn-ui']).toBe('svelte-check --tsconfig ui/tsconfig.json');
  });

  it('keeps Svelte dependencies scoped to tooling until the product UI imports them', () => {
    const packageJson = JSON.parse(
      fs.readFileSync(path.resolve(__dirname, '../../package.json'), 'utf8'),
    ) as {
      dependencies: Record<string, string>;
      devDependencies: Record<string, string>;
    };

    expect(packageJson.dependencies['lucide-svelte']).toBeUndefined();
    expect(packageJson.dependencies.svelte).toBeUndefined();
    expect(packageJson.devDependencies.svelte).toBe('^5.0.0');
    expect(packageJson.devDependencies['@sveltejs/vite-plugin-svelte']).toBe('^5.0.0');
    expect(packageJson.devDependencies['svelte-check']).toBe('^4.0.0');
  });

  it('keeps svelte-check scoped away from the legacy upstream webui bridge', () => {
    const tsconfig = JSON.parse(
      fs.readFileSync(path.resolve(__dirname, '../../ui/tsconfig.json'), 'utf8'),
    ) as { compilerOptions: { noEmit?: boolean }; include: string[] };

    expect(tsconfig.compilerOptions.noEmit).toBe(true);
    expect(tsconfig.include).toContain('src/**/*.svelte');
    expect(tsconfig.include).toContain('src/lib/**/*.ts');
    expect(tsconfig.include).not.toContain('src/**/*.ts');
    expect(tsconfig.include).not.toContain('vite.config.mts');
  });
});

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

  it('allows serving sibling upstream webui assets in dev without a proxy target', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const server = typeof config.server === 'function' ? config.server({} as never) : config.server;

    expect(server?.fs?.allow).toContain(repoRoot);
  });

  it('keeps sdn-js/ui as the Vite root and build output location', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const build = typeof config.build === 'function' ? config.build({} as never) : config.build;

    expect(config.root).toBe(path.resolve(__dirname, '../../ui'));
    expect(config.base).toBe('./');
    expect(build?.outDir).toBe('dist');
  });

  it('builds module workers as ES chunks for the FlatSQL runtime', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const worker = typeof config.worker === 'function' ? config.worker({} as never) : config.worker;

    expect(worker?.format).toBe('es');
  });

  it('emits the COI service worker at the UI root for static browser hosting', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const plugins = Array.isArray(config.plugins) ? config.plugins.flat() : [];
    const plugin = plugins.find((entry) => entry && typeof entry === 'object' && entry.name === 'sdn-coi-service-worker');

    expect(plugin).toBeDefined();
    expect(typeof plugin?.generateBundle).toBe('function');
    expect(typeof plugin?.configureServer).toBe('function');
  });

  it('fails a public web build whose emitted graph contains the protected wallet crypto runtime', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const plugins = Array.isArray(config.plugins) ? config.plugins.flat() : [];
    const plugin = plugins.find((entry) => entry && typeof entry === 'object' && entry.name === 'sdn-public-web-protected-runtime-boundary');

    expect(plugin).toBeDefined();
    expect(typeof plugin?.generateBundle).toBe('function');

    const protectedChunk = {
      type: 'chunk' as const,
      fileName: 'assets/app.js',
      facadeModuleId: '/repo/sdn-js/ui/src/main.ts',
      moduleIds: ['/repo/sdn-js/ui/src/main.ts', '/repo/sdn-js/src/crypto/hd-wallet.ts'],
      modules: {
        '/repo/sdn-js/ui/src/main.ts': {},
        '/repo/sdn-js/src/crypto/hd-wallet.ts': {},
      },
      code: 'export const publicUi = true;',
    };

    expect(() => plugin?.generateBundle?.call({} as never, {} as never, {
      'assets/app.js': protectedChunk,
    } as never, false)).toThrow(/protected wallet runtime/i);
  });

  it('installs the Svelte plugin before the legacy upstream webui plugins', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const plugins = Array.isArray(config.plugins) ? config.plugins.flat() : [];
    const firstPlugin = plugins[0];

    expect(firstPlugin).toBeDefined();
    expect(firstPlugin && typeof firstPlugin === 'object' && 'name' in firstPlugin ? firstPlugin.name : '').toBe('vite-plugin-svelte');
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

  it('uses JSX parsing for upstream JavaScript during dev dependency scanning', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const optimizeDeps = typeof config.optimizeDeps === 'function'
      ? config.optimizeDeps({} as never)
      : config.optimizeDeps;
    const loader = optimizeDeps?.esbuildOptions?.loader;

    expect(loader).toBeDefined();
    expect(loader?.['.js']).toBe('jsx');
  });

  it('patches the upstream react-virtualized proptype-only import for Vite', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const plugins = Array.isArray(config.plugins) ? config.plugins : [];
    const patchPlugin = plugins.find((plugin) => plugin && typeof plugin === 'object' && plugin.name === 'sdn-react-virtualized-window-scroller-patch');
    const onScrollPath = path.join(repoRoot, 'webui', 'node_modules', 'react-virtualized', 'dist', 'es', 'WindowScroller', 'utils', 'onScroll.js');
    const source = [
      "export function registerScrollListener() {}",
      'import { bpfrpt_proptype_WindowScroller } from "../WindowScroller.js";',
    ].join('\n');

    expect(patchPlugin).toBeDefined();
    expect(typeof patchPlugin?.transform).toBe('function');

    const result = await patchPlugin?.transform?.(source, onScrollPath);
    const code = typeof result === 'string'
      ? result
      : result && typeof result === 'object' && 'code' in result
        ? result.code
        : '';

    expect(code).toContain('registerScrollListener');
    expect(code).not.toContain('bpfrpt_proptype_WindowScroller');
  });

  it('pins copied upstream bootstrap dependencies and root-only browser shims', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const resolve = typeof config.resolve === 'function' ? config.resolve({} as never) : config.resolve;
    const alias = Array.isArray(resolve?.alias) ? resolve.alias : [];

    const reactAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^react$/');
    const reduxBundlerAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^redux-bundler$/');
    const bundlerAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^redux-bundler-react$/');
    const nobleCurvesAlias = findAliasFor(alias, '@noble/curves/ed25519');
    const nobleHashesAlias = findAliasFor(alias, '@noble/hashes/sha256');
    const bufferAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^buffer$/');
    const qrcodeAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^qrcode$/');
    const vcardAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^vcard-cryptoperson$/');
    const flatbuffersAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^flatbuffers$/');
    const scureBaseAlias = findAliasFor(alias, '@scure/base');
    const ipldProvidersAlias = findAliasFor(alias, 'ipld-explorer-components/providers');
    const millisecondsAlias = findAliasFor(alias, 'milliseconds');

    expect(String(reactAlias?.replacement)).toContain('/webui/node_modules/react');
    expect(String(reduxBundlerAlias?.replacement)).toContain('/sdn-js/ui/shims/redux-bundler-bound-timers.js');
    expect(String(bundlerAlias?.replacement)).toContain('/webui/node_modules/redux-bundler-react');
    expect(alias.some((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^hd-wallet-wasm$/')).toBe(false);
    expect(String(nobleCurvesAlias?.replacement)).toContain('/sdn-js/node_modules/@noble/curves/$1');
    expect(String(nobleHashesAlias?.replacement)).toContain('/sdn-js/node_modules/@noble/hashes/$1');
    expect(String(bufferAlias?.replacement)).toContain('/sdn-js/node_modules/buffer/index.js');
    expect(String(qrcodeAlias?.replacement)).toContain('/sdn-js/node_modules/qrcode');
    expect(String(vcardAlias?.replacement)).toContain('/sdn-js/node_modules/vcard-cryptoperson');
    expect(String(flatbuffersAlias?.replacement)).toContain('/sdn-js/node_modules/flatbuffers');
    expect(String(scureBaseAlias?.replacement)).toContain('/sdn-js/node_modules/@scure/base');
    expect(String(ipldProvidersAlias?.replacement)).toContain('/webui/node_modules/ipld-explorer-components/dist/providers/index.js');
    expect(String(millisecondsAlias?.replacement)).toContain('/sdn-js/ui/shims/milliseconds.js');
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
    const nodeInfoAdvancedOverride = await brandingPlugin?.resolveId?.(
      './NodeInfoAdvanced.js',
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
    expect(String(nodeInfoAdvancedOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/status/NodeInfoAdvanced.js');
    expect(String(connectedOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/components/connected/Connected.js');
    expect(String(routesOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/bundles/routes.js');
    expect(String(welcomeConnectedOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/components/is-connected/IsConnected.js');
    expect(String(aboutWebUiOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/components/about-webui/AboutWebUI.js');
    expect(String(aboutIpfsOverride)).toContain('/sdn-js/ui/src/upstream-webui/overrides/components/about-ipfs/AboutIpfs.js');
  });
});

function findAliasFor(alias: unknown[], specifier: string) {
  return alias.find((entry) => {
    if (!entry || typeof entry !== 'object' || !('find' in entry)) {
      return false;
    }
    const matcher = entry.find;
    if (matcher instanceof RegExp) {
      return matcher.test(specifier);
    }
    return matcher === specifier;
  }) as { replacement?: string } | undefined;
}
