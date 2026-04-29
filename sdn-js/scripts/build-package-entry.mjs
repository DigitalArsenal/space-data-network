import path from 'node:path';
import fs from 'node:fs/promises';
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
  packages: 'external',
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
    {
      name: 'uint8arraylist-cross-realm-views',
      setup(pluginBuild) {
        pluginBuild.onLoad(
          { filter: /node_modules[\\/]uint8arraylist[\\/](?:src[\\/]index\.ts|dist[\\/]src[\\/]index\.js)$/ },
          async (args) => {
            let contents = await fs.readFile(args.path, 'utf8');
            contents = contents.replace(
              "export function isUint8ArrayList (value: any): value is Uint8ArrayList {\n  return Boolean(value?.[symbol])\n}\n\n",
              `export function isUint8ArrayList (value: any): value is Uint8ArrayList {\n  return Boolean(value?.[symbol])\n}\n\nfunction normalizeAppendableByteView (value: any): Uint8Array | null {\n  if (value instanceof Uint8Array) {\n    return value\n  }\n  if (ArrayBuffer.isView(value)) {\n    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength).slice()\n  }\n  if (value instanceof ArrayBuffer || (value?.constructor?.name === 'ArrayBuffer' && typeof value?.byteLength === 'number')) {\n    return new Uint8Array(value).slice()\n  }\n  return null\n}\n\n`,
            );
            contents = contents.replace(
              "export function isUint8ArrayList(value) {\n    return Boolean(value?.[symbol]);\n}\n",
              `export function isUint8ArrayList(value) {\n    return Boolean(value?.[symbol]);\n}\nfunction normalizeAppendableByteView(value) {\n    if (value instanceof Uint8Array) {\n        return value;\n    }\n    if (ArrayBuffer.isView(value)) {\n        return new Uint8Array(value.buffer, value.byteOffset, value.byteLength).slice();\n    }\n    if (value instanceof ArrayBuffer || (value?.constructor?.name === 'ArrayBuffer' && typeof value?.byteLength === 'number')) {\n        return new Uint8Array(value).slice();\n    }\n    return null;\n}\n`,
            );
            contents = contents.replace(
              "      if (buf instanceof Uint8Array) {\n        length += buf.byteLength\n        this.bufs.push(buf)\n      } else if (isUint8ArrayList(buf)) {",
              "      const byteView = normalizeAppendableByteView(buf)\n      if (byteView != null) {\n        length += byteView.byteLength\n        this.bufs.push(byteView)\n      } else if (isUint8ArrayList(buf)) {",
            );
            contents = contents.replace(
              "            if (buf instanceof Uint8Array) {\n                length += buf.byteLength;\n                this.bufs.push(buf);\n            }\n            else if (isUint8ArrayList(buf)) {",
              "            const byteView = normalizeAppendableByteView(buf);\n            if (byteView != null) {\n                length += byteView.byteLength;\n                this.bufs.push(byteView);\n            }\n            else if (isUint8ArrayList(buf)) {",
            );
            return {
              contents,
              loader: args.path.endsWith('.ts') ? 'ts' : 'js',
            };
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
