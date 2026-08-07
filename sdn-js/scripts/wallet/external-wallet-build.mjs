/**
 * The committed, named way to build sdn-js with the HD wallet runtime
 * EXTERNALISED instead of inlined.
 *
 * Why this exists
 * ---------------
 * Embedders that ship their own reviewed copy of hd-wallet-wasm cannot have a
 * second full runtime inlined in sdn-js's bundles, and they enforce that with a
 * publish-time guard. Before this module the only way to get such a build was
 * for the embedder to REWRITE `scripts/build-package-entry.mjs` on disk around
 * the build and restore it afterwards, which meant the artifact was not
 * reproducible from any committed script in this package: `npm run build:core`
 * and the published npm tarball both produce the inlined form.
 *
 * The mode is now first class:
 *
 *   npm run build:browser-external-wallet
 *
 * and the substitution point is a plain esbuild plugin over an adapter module,
 * so an embedder supplies its own adapter with an environment variable instead
 * of patching this package's source.
 */

import path from 'node:path';
import { pathToFileURL } from 'node:url';

/** Set to `1`/`true` to build with the wallet runtime externalised. */
export const EXTERNAL_WALLET_ENV = 'SDN_JS_EXTERNAL_WALLET';

/** Absolute (or package-relative) path to the module that replaces `hd-wallet-wasm`. */
export const EXTERNAL_WALLET_ADAPTER_ENV = 'SDN_JS_HD_WALLET_ADAPTER';

/** Comma/`os.delimiter`-separated modules contributing extra esbuild plugins. */
export const EXTRA_PLUGIN_MODULES_ENV = 'SDN_JS_ESBUILD_PLUGIN_MODULES';

const WALLET_PACKAGE_FILTER = /^hd-wallet-wasm$/;

/** The adapter committed to this package; the default when none is supplied. */
export function defaultExternalWalletAdapterPath(packageRoot) {
  return path.join(packageRoot, 'scripts', 'wallet', 'external-hd-wallet-adapter.mjs');
}

/**
 * Whether this build externalises the wallet runtime. CLI flag wins over env so
 * `npm run build:core -- --external-wallet` behaves the same as the named script.
 */
export function isExternalWalletBuild({ argv = [], env = process.env } = {}) {
  if (argv.includes('--external-wallet')) {
    return true;
  }
  const configured = env[EXTERNAL_WALLET_ENV];
  return configured === '1' || configured === 'true';
}

export function resolveExternalWalletAdapterPath({ packageRoot, env = process.env } = {}) {
  const configured = env[EXTERNAL_WALLET_ADAPTER_ENV];
  if (typeof configured === 'string' && configured.trim() !== '') {
    return path.resolve(packageRoot, configured.trim());
  }
  return defaultExternalWalletAdapterPath(packageRoot);
}

/**
 * Redirect every `hd-wallet-wasm` import to `adapterPath`. Subpath imports
 * (`hd-wallet-wasm/attestation`) are deliberately NOT redirected: they carry no
 * runtime and the adapter re-exports them.
 */
export function createExternalHdWalletPlugin({ adapterPath }) {
  if (typeof adapterPath !== 'string' || !path.isAbsolute(adapterPath)) {
    throw new TypeError('adapterPath must be an absolute path.');
  }
  const normalizedAdapterPath = path.normalize(adapterPath);
  return {
    name: 'sdn-js-external-hd-wallet',
    setup(build) {
      build.onResolve({ filter: WALLET_PACKAGE_FILTER }, () => ({
        path: normalizedAdapterPath,
      }));
    },
  };
}

function splitPluginModuleList(value) {
  return value
    .split(/[,:;]/u)
    .map((entry) => entry.trim())
    .filter((entry) => entry !== '');
}

/**
 * Load extra esbuild plugins an embedder contributes. Each listed module must
 * export `createEsbuildPlugin({ packageRoot })` (or a default of that shape)
 * returning one plugin or an array of them. This is the supported way to add
 * embedder-specific rewrites without editing this package.
 */
export async function loadExtraEsbuildPlugins({ packageRoot, env = process.env } = {}) {
  const configured = env[EXTRA_PLUGIN_MODULES_ENV];
  if (typeof configured !== 'string' || configured.trim() === '') {
    return [];
  }
  const plugins = [];
  for (const entry of splitPluginModuleList(configured)) {
    const modulePath = path.resolve(packageRoot, entry);
    const loaded = await import(/* @vite-ignore */ pathToFileURL(modulePath).href);
    const factory = loaded.createEsbuildPlugin ?? loaded.default;
    if (typeof factory !== 'function') {
      throw new TypeError(
        `${modulePath} must export createEsbuildPlugin({ packageRoot }) or a default of that shape.`,
      );
    }
    const produced = await factory({ packageRoot });
    for (const plugin of Array.isArray(produced) ? produced : [produced]) {
      if (plugin === null || typeof plugin !== 'object' || typeof plugin.setup !== 'function') {
        throw new TypeError(`${modulePath} did not return an esbuild plugin.`);
      }
      plugins.push(plugin);
    }
  }
  return plugins;
}

/**
 * Everything `scripts/build-package-entry.mjs` needs to know about this build's
 * wallet mode, resolved once so it can be logged.
 */
export async function resolveWalletBuildMode({ packageRoot, argv = [], env = process.env } = {}) {
  const external = isExternalWalletBuild({ argv, env });
  const extraPlugins = await loadExtraEsbuildPlugins({ packageRoot, env });
  if (!external) {
    return { external, adapterPath: null, plugins: extraPlugins };
  }
  const adapterPath = resolveExternalWalletAdapterPath({ packageRoot, env });
  return {
    external,
    adapterPath,
    plugins: [createExternalHdWalletPlugin({ adapterPath }), ...extraPlugins],
  };
}
