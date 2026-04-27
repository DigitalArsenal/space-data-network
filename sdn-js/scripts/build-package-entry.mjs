import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '..');
const hdWalletShims = new Map([
  ['./sdn-plugin.mjs', path.join(packageRoot, 'ui/shims/hd-wallet-sdn-plugin.mjs')],
  [
    './sdn-plugin-manifest-source.mjs',
    path.join(packageRoot, 'ui/shims/hd-wallet-sdn-plugin-manifest-source.mjs'),
  ],
]);

const sharedBuildOptions = {
  absWorkingDir: packageRoot,
  bundle: true,
  format: 'esm',
  platform: 'browser',
  target: 'es2022',
  sourcemap: false,
  logLevel: 'info',
  mainFields: ['browser', 'module', 'main'],
  conditions: ['browser', 'import', 'module'],
  alias: {
    '@sds': path.join(packageRoot, 'node_modules/spacedatastandards.org'),
  },
  plugins: [
    {
      name: 'hd-wallet-sdn-shims',
      setup(pluginBuild) {
        pluginBuild.onResolve(
          { filter: /^\.\/sdn-plugin(?:-manifest-source)?\.mjs$/ },
          (args) => {
            const shimPath = hdWalletShims.get(args.path);
            return shimPath ? { path: shimPath } : null;
          },
        );
      },
    },
  ],
  loader: {
    '.wasm': 'file',
  },
};

await build({
  ...sharedBuildOptions,
  entryPoints: [
    path.join(packageRoot, 'src/index.ts'),
    path.join(packageRoot, 'src/ui/index.ts'),
    path.join(packageRoot, 'src/storefront/index.ts'),
  ],
  outdir: path.join(packageRoot, 'dist'),
  outbase: path.join(packageRoot, 'src'),
  splitting: true,
  entryNames: '[dir]/[name]',
  chunkNames: 'chunks/[name]-[hash]',
  outExtension: {
    '.js': '.mjs',
  },
});
