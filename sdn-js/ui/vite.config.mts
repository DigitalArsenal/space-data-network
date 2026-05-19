import path from 'node:path';
import { fileURLToPath } from 'node:url';
import fs from 'node:fs';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig, transformWithEsbuild } from 'vite';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '..');
const repoRoot = path.resolve(packageRoot, '..');
const stackPackagesRoot = path.resolve(repoRoot, '..');
const upstreamWebUiRoot = path.resolve(repoRoot, 'webui');
const sdnUpstreamWebUiRoot = path.resolve(__dirname, 'src', 'upstream-webui');
const coiServiceWorkerPath = path.resolve(__dirname, 'src', 'lib', 'coi-serviceworker.js');
const proxyTarget = process.env.SDN_UI_PROXY_TARGET?.trim();
const kuboProxyTarget = process.env.SDN_UI_KUBO_PROXY_TARGET?.trim();
const reactVirtualizedWindowScrollerOnScrollPath = path.resolve(
  upstreamWebUiRoot,
  'node_modules',
  'react-virtualized',
  'dist',
  'es',
  'WindowScroller',
  'utils',
  'onScroll.js',
);
const reactVirtualizedWindowScrollerProptypeImportPattern =
  /^\s*import\s+\{\s*bpfrpt_proptype_WindowScroller\s*\}\s+from\s+['"]\.\.\/WindowScroller\.js['"];\s*$/gm;
const rootBrandingOverrides = new Map([
  [
    path.resolve(upstreamWebUiRoot, 'src', 'bundles', 'routes.js'),
    path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'bundles', 'routes.js'),
  ],
  [
    path.resolve(upstreamWebUiRoot, 'src', 'components', 'about-ipfs', 'AboutIpfs.js'),
    path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'components', 'about-ipfs', 'AboutIpfs.js'),
  ],
  [
    path.resolve(upstreamWebUiRoot, 'src', 'components', 'about-webui', 'AboutWebUI.js'),
    path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'components', 'about-webui', 'AboutWebUI.js'),
  ],
  [
    path.resolve(upstreamWebUiRoot, 'src', 'components', 'connected', 'Connected.js'),
    path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'components', 'connected', 'Connected.js'),
  ],
  [
    path.resolve(upstreamWebUiRoot, 'src', 'components', 'is-connected', 'IsConnected.js'),
    path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'components', 'is-connected', 'IsConnected.js'),
  ],
  [
    path.resolve(upstreamWebUiRoot, 'src', 'navigation', 'NavBar.js'),
    path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'navigation', 'NavBar.js'),
  ],
  [
    path.resolve(upstreamWebUiRoot, 'src', 'status', 'NodeInfo.js'),
    path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'status', 'NodeInfo.js'),
  ],
  [
    path.resolve(upstreamWebUiRoot, 'src', 'status', 'NodeInfoAdvanced.js'),
    path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'status', 'NodeInfoAdvanced.js'),
  ],
  [
    path.resolve(upstreamWebUiRoot, 'src', 'status', 'StatusConnected.js'),
    path.resolve(sdnUpstreamWebUiRoot, 'overrides', 'status', 'StatusConnected.js'),
  ],
]);
const browserProcessShimBanner = [
  'var process = globalThis.process || (globalThis.process = {',
  'env: {},',
  'browser: true,',
  'versions: {},',
  'release: {},',
  'platform: "browser",',
  'arch: "browser",',
  'version: "",',
  'pid: 1,',
  '__nwjs: false,',
  'type: undefined,',
  'cwd: () => "/",',
  'nextTick: (fn, ...args) => queueMicrotask(() => fn(...args)),',
  'noDeprecation: false,',
  'throwDeprecation: false,',
  'traceDeprecation: false',
  '});',
  'var global = globalThis;',
].join('');

function stripReactVirtualizedWindowScrollerProptypeImport(code: string): string {
  return code.replace(reactVirtualizedWindowScrollerProptypeImportPattern, '');
}

export default defineConfig({
  root: __dirname,
  publicDir: path.resolve(upstreamWebUiRoot, 'public'),
  base: './',
  envPrefix: ['VITE_', 'SDN_UI_'],
  plugins: [
    svelte(),
    {
      name: 'sdn-react-virtualized-window-scroller-patch',
      transform(code, id) {
        if (path.resolve(id.split('?')[0]) === reactVirtualizedWindowScrollerOnScrollPath) {
          return {
            code: stripReactVirtualizedWindowScrollerProptypeImport(code),
            map: null,
          };
        }
        return null;
      },
    },
    {
      name: 'sdn-upstream-webui-jsx',
      async transform(code, id) {
        if (
          id.endsWith('.js') && (
            id.includes('/webui/src/') ||
            id.includes('/sdn-js/ui/src/upstream-webui/')
          )
        ) {
          return transformWithEsbuild(code, id, {
            loader: 'jsx',
            jsxFactory: 'React.createElement',
            jsxFragment: 'React.Fragment',
          });
        }
        return null;
      },
    },
    {
      name: 'sdn-upstream-webui-root-overrides',
      enforce: 'pre',
      resolveId(source, importer) {
        if (!importer || source.startsWith('/') || !source.startsWith('.')) {
          return null;
        }
        const sourcePath = path.resolve(path.dirname(importer), source);
        return rootBrandingOverrides.get(sourcePath) ?? null;
      },
    },
    {
      name: 'sdn-upstream-webui-extension-alias',
      resolveId(source, importer) {
        if (!importer || source.startsWith('/') || !source.startsWith('.')) {
          return null;
        }
        const sourcePath = path.resolve(path.dirname(importer), source);
        const candidates = source.endsWith('.jsx')
          ? [sourcePath.replace(/\.jsx$/, '.tsx'), sourcePath.replace(/\.jsx$/, '.ts')]
          : source.endsWith('.js')
            ? [sourcePath.replace(/\.js$/, '.tsx'), sourcePath.replace(/\.js$/, '.ts')]
            : [];
        for (const candidate of candidates) {
          if (fs.existsSync(candidate)) {
            return candidate;
          }
        }
        return null;
      },
    },
    {
      name: 'sdn-coi-service-worker',
      configureServer(server) {
        server.middlewares.use('/coi-serviceworker.js', async (_req, res) => {
          res.setHeader('Content-Type', 'text/javascript; charset=utf-8');
          res.end(await fs.promises.readFile(coiServiceWorkerPath, 'utf8'));
        });
      },
      generateBundle() {
        this.emitFile({
          type: 'asset',
          fileName: 'coi-serviceworker.js',
          source: fs.readFileSync(coiServiceWorkerPath, 'utf8'),
        });
      },
    },
  ],
  server: {
    host: '127.0.0.1',
    port: Number.parseInt(process.env.SDN_ADMIN_UI_PORT ?? '5173', 10),
    fs: {
      allow: [repoRoot, stackPackagesRoot],
    },
    ...(proxyTarget || kuboProxyTarget
      ? {
        proxy: {
          ...(kuboProxyTarget
            ? {
              '/kubo': {
                target: kuboProxyTarget,
                changeOrigin: true,
                secure: false,
                rewrite: (requestPath: string) => requestPath.replace(/^\/kubo(?=\/|$)/, '') || '/',
                configure: (proxy) => {
                  proxy.on('proxyReq', (proxyReq) => {
                    proxyReq.removeHeader('origin');
                    proxyReq.removeHeader('referer');
                    proxyReq.removeHeader('user-agent');
                  });
                },
              },
            }
            : {}),
          ...(proxyTarget
            ? {
              '/api': {
                target: proxyTarget,
                changeOrigin: true,
                secure: false,
              },
              '/login': {
                target: proxyTarget,
                changeOrigin: true,
                secure: false,
              },
              '/wallet-ui': {
                target: proxyTarget,
                changeOrigin: true,
                secure: false,
              },
              '/webui': {
                target: proxyTarget,
                changeOrigin: true,
                secure: false,
              },
              '/ipfs': {
                target: proxyTarget,
                changeOrigin: true,
                secure: false,
              },
            }
            : {}),
        },
      }
      : {}),
  },
  optimizeDeps: {
    esbuildOptions: {
      loader: {
        '.js': 'jsx',
      },
      plugins: [
        {
          name: 'sdn-react-virtualized-window-scroller-patch',
          setup(build) {
            build.onLoad({ filter: /react-virtualized\/dist\/es\/WindowScroller\/utils\/onScroll\.js$/ }, async (args) => {
              const code = await fs.promises.readFile(args.path, 'utf8');
              return {
                contents: stripReactVirtualizedWindowScrollerProptypeImport(code),
                loader: 'js',
              };
            });
          },
        },
      ],
    },
  },
  define: {
    'process.env.REACT_APP_VERSION': JSON.stringify(process.env.REACT_APP_VERSION ?? process.env.npm_package_version ?? 'dev'),
    'process.env.REACT_APP_GIT_REV': JSON.stringify(process.env.REACT_APP_GIT_REV ?? 'local'),
    'process.env.NODE_ENV': JSON.stringify(process.env.NODE_ENV ?? 'development'),
  },
  resolve: {
    alias: [
      {
        find: '@sds',
        replacement: path.resolve(packageRoot, 'node_modules/spacedatastandards.org'),
      },
      {
        find: /^react$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/react'),
      },
      {
        find: /^react-dom$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/react-dom'),
      },
      {
        find: /^redux-bundler-react$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/redux-bundler-react'),
      },
      {
        find: /^redux-bundler$/,
        replacement: path.resolve(__dirname, 'shims/redux-bundler-bound-timers.js'),
      },
      {
        find: /^ipld-explorer-components$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/ipld-explorer-components/dist/index.js'),
      },
      {
        find: /^ipld-explorer-components\/providers$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/ipld-explorer-components/dist/providers/index.js'),
      },
      {
        find: /^ipld-explorer-components\/pages$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/ipld-explorer-components/dist/pages.js'),
      },
      {
        find: /^ipld-explorer-components\/css$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/ipld-explorer-components/dist/style.css'),
      },
      {
        find: /^react-i18next$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/react-i18next'),
      },
      {
        find: /^react-dnd$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/react-dnd'),
      },
      {
        find: /^react-dnd-html5-backend$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/react-dnd-html5-backend'),
      },
      {
        find: /^react-joyride$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/react-joyride'),
      },
      {
        find: /^prop-types$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/prop-types'),
      },
      {
        find: /^internal-nav-helper$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/internal-nav-helper'),
      },
      {
        find: /^milliseconds$/,
        replacement: path.resolve(__dirname, 'shims/milliseconds.js'),
      },
      {
        find: /^classnames$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/classnames'),
      },
      {
        find: /^hd-wallet-wasm$/,
        replacement: path.resolve(packageRoot, 'node_modules/hd-wallet-wasm/src/index.mjs'),
      },
      {
        find: /^@noble\/curves\/(.*)$/,
        replacement: path.resolve(packageRoot, 'node_modules/@noble/curves') + '/$1',
      },
      {
        find: /^@noble\/hashes\/(.*)$/,
        replacement: path.resolve(packageRoot, 'node_modules/@noble/hashes') + '/$1',
      },
      {
        find: /^buffer$/,
        replacement: path.resolve(packageRoot, 'node_modules/buffer/index.js'),
      },
      {
        find: /^qrcode$/,
        replacement: path.resolve(packageRoot, 'node_modules/qrcode'),
      },
      {
        find: /^vcard-cryptoperson$/,
        replacement: path.resolve(packageRoot, 'node_modules/vcard-cryptoperson/dist/index.js'),
      },
      {
        find: /^flatbuffers$/,
        replacement: path.resolve(packageRoot, 'node_modules/flatbuffers/js/flatbuffers.js'),
      },
      {
        find: /^@scure\/base$/,
        replacement: path.resolve(packageRoot, 'node_modules/@scure/base/lib/index.js'),
      },
      {
        find: /^react-virtualized\/styles\.css$/,
        replacement: path.resolve(upstreamWebUiRoot, 'node_modules/react-virtualized/styles.css'),
      },
      {
        find: /^\.\/sdn-plugin\.mjs$/,
        replacement: path.resolve(__dirname, 'shims/hd-wallet-sdn-plugin.mjs'),
      },
      {
        find: /^\.\/sdn-plugin-manifest-source\.mjs$/,
        replacement: path.resolve(__dirname, 'shims/hd-wallet-sdn-plugin-manifest-source.mjs'),
      },
    ],
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        banner: browserProcessShimBanner,
        manualChunks(id) {
          if (!id.includes('node_modules')) {
            return undefined;
          }
          if (
            id.includes('/libp2p/') ||
            id.includes('/helia/') ||
            id.includes('/@libp2p/') ||
            id.includes('/@chainsafe/') ||
            id.includes('/multiformats/') ||
            id.includes('/@multiformats/')
          ) {
            return 'network';
          }
          return undefined;
        },
      },
    },
  },
  worker: {
    format: 'es',
  },
});
