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
  format: 'esm',
  platform: 'browser',
  target: 'es2022',
  banner: {
    js: 'var WebSocket = globalThis.WebSocket;',
  },
  sourcemap: false,
  logLevel: 'info',
  mainFields: ['browser', 'module', 'main'],
  conditions: ['browser', 'import', 'module'],
  alias: {
    '@sds': path.join(packageRoot, 'node_modules/spacedatastandards.org'),
    '@libp2p/crypto/ciphers': path.join(packageRoot, 'src/shims/libp2p-crypto-ciphers.ts'),
    '@libp2p/crypto/webcrypto': path.join(packageRoot, 'src/shims/libp2p-crypto-webcrypto.ts'),
    '@libp2p/crypto/keys': path.join(packageRoot, 'src/shims/libp2p-crypto-keys.ts'),
    '@libp2p/crypto': path.join(packageRoot, 'src/shims/libp2p-crypto.ts'),
    '@libp2p/keychain': path.join(packageRoot, 'src/shims/libp2p-keychain.ts'),
    'multiformats/basics': path.join(packageRoot, 'src/shims/multiformats-basics-native.ts'),
    'multiformats/hashes/sha1': path.join(packageRoot, 'src/shims/multiformats-sha1-disabled.ts'),
    'multiformats/hashes/sha2': path.join(packageRoot, 'src/shims/multiformats-sha2-native.ts'),
  },
  plugins: [
    {
      name: 'satellite-js-wasm-disabled',
      setup(pluginBuild) {
        pluginBuild.onResolve({ filter: /^\.\/wasm\/index\.js$/ }, (args) => {
          if (!args.importer.includes(`node_modules${path.sep}satellite.js${path.sep}`)) {
            return null;
          }
          return { path: path.join(packageRoot, 'src/shims/satellite-wasm-disabled.ts') };
        });
      },
    },
    {
      // Loop D.1: the FlatSQL-WASM engine (THE SDNNode store) is bundled
      // into the package entries. Its emscripten glue / wasm loader import
      // the Node builtins `module` and `node:crypto` on Node-only code
      // paths; for the browser-platform bundle those imports are mapped to
      // runtime-conditional shims (real builtins in Node, inert in
      // browsers). Scoped to importers inside node_modules/flatsql so no
      // other dependency resolution changes.
      name: 'flatsql-node-builtin-shims',
      setup(pluginBuild) {
        const flatsqlDir = `node_modules${path.sep}flatsql${path.sep}`;
        const shimByBuiltin = new Map([
          ['module', 'src/shims/flatsql-node-module.ts'],
          ['node:crypto', 'src/shims/flatsql-node-crypto.ts'],
          ['fs', 'src/shims/flatsql-node-builtins.ts'],
          ['path', 'src/shims/flatsql-node-builtins.ts'],
          ['url', 'src/shims/flatsql-node-builtins.ts'],
        ]);
        pluginBuild.onResolve({ filter: /^(module|node:crypto|fs|path|url)$/ }, (args) => {
          if (!args.importer.includes(flatsqlDir)) {
            return null;
          }
          const shim = shimByBuiltin.get(args.path);
          return shim ? { path: path.join(packageRoot, shim) } : null;
        });
      },
    },
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
    {
      name: 'libp2p-peer-store-metadata-byte-views',
      setup(pluginBuild) {
        pluginBuild.onLoad(
          { filter: /node_modules[\\/]@libp2p[\\/]peer-store[\\/](?:src[\\/]utils[\\/]to-peer-pb\.ts|dist[\\/]src[\\/]utils[\\/]to-peer-pb\.js)$/ },
          async (args) => {
            let contents = await fs.readFile(args.path, 'utf8');
            const normalizerTs = `function normalizePeerMetadataValue (value: any): Uint8Array | null {
  if (value instanceof Uint8Array) {
    return value
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength).slice()
  }
  if (value instanceof ArrayBuffer || (value?.constructor?.name === 'ArrayBuffer' && typeof value?.byteLength === 'number')) {
    return new Uint8Array(value).slice()
  }
  if (typeof value?.subarray === 'function' && typeof value?.byteLength === 'number') {
    const view = value.subarray()
    if (view instanceof Uint8Array) {
      return view.slice()
    }
    if (ArrayBuffer.isView(view)) {
      return new Uint8Array(view.buffer, view.byteOffset, view.byteLength).slice()
    }
  }
  return null
}

`;
            const normalizerJs = `function normalizePeerMetadataValue(value) {
    if (value instanceof Uint8Array) {
        return value;
    }
    if (ArrayBuffer.isView(value)) {
        return new Uint8Array(value.buffer, value.byteOffset, value.byteLength).slice();
    }
    if (value instanceof ArrayBuffer || (value?.constructor?.name === 'ArrayBuffer' && typeof value?.byteLength === 'number')) {
        return new Uint8Array(value).slice();
    }
    if (typeof value?.subarray === 'function' && typeof value?.byteLength === 'number') {
        const view = value.subarray();
        if (view instanceof Uint8Array) {
            return view.slice();
        }
        if (ArrayBuffer.isView(view)) {
            return new Uint8Array(view.buffer, view.byteOffset, view.byteLength).slice();
        }
    }
    return null;
}
`;
            contents = contents.replace(
              'function validateMetadata (key: string, value: Uint8Array): void {\n',
              `${normalizerTs}function validateMetadata (key: string, value: Uint8Array): void {\n`,
            );
            contents = contents.replace(
              'function validateMetadata(key, value) {\n',
              `${normalizerJs}function validateMetadata(key, value) {\n`,
            );
            contents = contents.replace(
              "  if (!(value instanceof Uint8Array)) {\n    throw new CodeError('Metadata value must be a Uint8Array', codes.ERR_INVALID_PARAMETERS)\n  }\n",
              "  if (normalizePeerMetadataValue(value) == null) {\n    throw new CodeError('Metadata value must be a Uint8Array', codes.ERR_INVALID_PARAMETERS)\n  }\n",
            );
            contents = contents.replace(
              "    if (!(value instanceof Uint8Array)) {\n        throw new CodeError('Metadata value must be a Uint8Array', codes.ERR_INVALID_PARAMETERS);\n    }\n",
              "    if (normalizePeerMetadataValue(value) == null) {\n        throw new CodeError('Metadata value must be a Uint8Array', codes.ERR_INVALID_PARAMETERS);\n    }\n",
            );
            contents = contents.replace(
              "    if (!(value instanceof Uint8Array)) {\n        throw new InvalidParametersError('Metadata value must be a Uint8Array');\n    }\n",
              "    if (normalizePeerMetadataValue(value) == null) {\n        throw new InvalidParametersError('Metadata value must be a Uint8Array');\n    }\n",
            );
            contents = contents.replaceAll(
              "      metadata = createSortedMap(metadataEntries, {\n        validate: validateMetadata\n      })",
              "      metadata = createSortedMap(metadataEntries, {\n        validate: validateMetadata,\n        map: (_key, value) => normalizePeerMetadataValue(value) ?? value\n      })",
            );
            contents = contents.replaceAll(
              "            metadata = createSortedMap(metadataEntries, {\n                validate: validateMetadata\n            });",
              "            metadata = createSortedMap(metadataEntries, {\n                validate: validateMetadata,\n                map: (_key, value) => normalizePeerMetadataValue(value) ?? value\n            });",
            );
            contents = contents.replaceAll(
              "      metadata = createSortedMap([...metadata.entries()], {\n        validate: validateMetadata\n      })",
              "      metadata = createSortedMap([...metadata.entries()], {\n        validate: validateMetadata,\n        map: (_key, value) => normalizePeerMetadataValue(value) ?? value\n      })",
            );
            contents = contents.replaceAll(
              "            metadata = createSortedMap([...metadata.entries()], {\n                validate: validateMetadata\n            });",
              "            metadata = createSortedMap([...metadata.entries()], {\n                validate: validateMetadata,\n                map: (_key, value) => normalizePeerMetadataValue(value) ?? value\n            });",
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
    path.join(packageRoot, 'src/astro/index.ts'),
  ],
  outdir: path.join(packageRoot, 'dist'),
  outbase: path.join(packageRoot, 'src'),
  splitting: false,
  entryNames: '[dir]/[name]',
  outExtension: {
    '.js': '.mjs',
  },
});

// The bundled flatsql emscripten glue locates its wasm binary relative to
// the importing module's URL (new URL('flatsql.wasm', import.meta.url)).
// Ship the engine binary beside every dist entry that may initialize it so
// the lookup works from the published package in both Node and browsers.
const flatsqlWasm = path.join(packageRoot, 'node_modules/flatsql/wasm/flatsql.wasm');
for (const target of ['dist/flatsql.wasm', 'dist/ui/flatsql.wasm']) {
  await fs.copyFile(flatsqlWasm, path.join(packageRoot, target));
}
