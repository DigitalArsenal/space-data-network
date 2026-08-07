/**
 * sdn-js external HD wallet runtime adapter.
 *
 * This module is compiled into the package bundles IN PLACE OF the
 * `hd-wallet-wasm` package whenever sdn-js is built with the external wallet
 * mode (`npm run build:browser-external-wallet`). Nothing here pulls
 * `hd-wallet-wasm`'s emscripten runtime into the bundle: the ~5 MB runtime is
 * obtained at RUNTIME from a provider the host installs, which is why the
 * externalised `dist/index.mjs` is roughly half the size of the default one.
 *
 * Where the runtime comes from
 * ----------------------------
 * 1. A provider the host registers before use, via `registerHdWalletProvider()`
 *    or by assigning `globalThis['sdn.hd-wallet-wasm.provider.v1']`. This is
 *    what an embedding engine does when it already ships its own reviewed copy
 *    of the runtime and must not have a second one on the page.
 * 2. Otherwise the SAME-ORIGIN staged runtime the node serves at
 *    `/wallet-wasm/runtime/index.mjs` (see the node's /wallet-wasm handler and
 *    deployment/wallet-wasm/stage-wallet-wasm.sh). Same-origin only — a node UI
 *    never loads external-origin bytes.
 *
 * Fail-closed: if neither is available, every wallet entry point rejects or
 * throws with the staging path named. There is no silent degraded mode.
 */

/** Global key a host assigns to install its own runtime provider. */
export const HD_WALLET_PROVIDER_GLOBAL = 'sdn.hd-wallet-wasm.provider.v1';

/** Global key a host assigns to override the staged runtime URL. */
export const HD_WALLET_RUNTIME_URL_GLOBAL = 'sdn.hd-wallet-wasm.runtime-url.v1';

/** The node's staged runtime entry, served same-origin. */
export const DEFAULT_STAGED_RUNTIME_URL = '/wallet-wasm/runtime/index.mjs';

const CURVE_KEYS = Object.freeze([
  'SECP256K1',
  'ED25519',
  'P256',
  'P384',
  'X25519',
]);

const LANGUAGE_KEYS = Object.freeze([
  'ENGLISH',
  'JAPANESE',
  'KOREAN',
  'SPANISH',
  'CHINESE_SIMPLIFIED',
  'CHINESE_TRADITIONAL',
  'FRENCH',
  'ITALIAN',
  'CZECH',
  'PORTUGUESE',
]);

let stagedRuntimePromise = null;

function providerInitializer(provider) {
  if (provider === null || typeof provider !== 'object') {
    return null;
  }
  if (typeof provider.createHDWallet === 'function') {
    return provider.createHDWallet;
  }
  if (typeof provider.default === 'function') {
    return provider.default;
  }
  return null;
}

function installedProvider() {
  const provider = globalThis[HD_WALLET_PROVIDER_GLOBAL] ?? null;
  return providerInitializer(provider) === null ? null : provider;
}

/**
 * Install the HD wallet runtime provider for this realm.
 * @param {object} provider a module-shaped object exposing `createHDWallet()`
 *   (or a default export of the same shape) plus the runtime's metadata.
 * @returns {object} the installed provider
 */
export function registerHdWalletProvider(provider) {
  if (providerInitializer(provider) === null) {
    throw new TypeError(
      'HD wallet provider must expose createHDWallet() or a default initializer.',
    );
  }
  globalThis[HD_WALLET_PROVIDER_GLOBAL] = provider;
  stagedRuntimePromise = null;
  return provider;
}

/** Forget the installed provider. Intended for tests and host teardown. */
export function unregisterHdWalletProvider() {
  delete globalThis[HD_WALLET_PROVIDER_GLOBAL];
  stagedRuntimePromise = null;
}

function stagedRuntimeUrl() {
  const configured = globalThis[HD_WALLET_RUNTIME_URL_GLOBAL];
  return typeof configured === 'string' && configured !== ''
    ? configured
    : DEFAULT_STAGED_RUNTIME_URL;
}

async function loadStagedRuntime() {
  const specifier = stagedRuntimeUrl();
  let runtime;
  try {
    runtime = await import(/* @vite-ignore */ /* webpackIgnore: true */ specifier);
  } catch (cause) {
    throw new Error(
      `sdn-js was built with the external HD wallet runtime and no provider was installed, ` +
        `so it tried the staged runtime at ${specifier} and could not load it. ` +
        `Stage it on the node (deployment/wallet-wasm/stage-wallet-wasm.sh) or install a ` +
        `provider with registerHdWalletProvider().`,
      { cause },
    );
  }
  if (providerInitializer(runtime) === null) {
    throw new Error(
      `The staged HD wallet runtime at ${specifier} does not expose createHDWallet().`,
    );
  }
  return runtime;
}

async function resolveProvider() {
  const installed = installedProvider();
  if (installed !== null) {
    return installed;
  }
  if (stagedRuntimePromise === null) {
    stagedRuntimePromise = loadStagedRuntime().catch((error) => {
      stagedRuntimePromise = null;
      throw error;
    });
  }
  return registerHdWalletProvider(await stagedRuntimePromise);
}

function requireInstalledProvider(what) {
  const installed = installedProvider();
  if (installed === null) {
    throw new Error(
      `sdn-js was built with the external HD wallet runtime, and ${what} was read before the ` +
        `runtime was available. Await initHDWallet()/createHDWallet() first, or install a provider ` +
        `with registerHdWalletProvider() (a node stages one at ${DEFAULT_STAGED_RUNTIME_URL}).`,
    );
  }
  return installed;
}

function metadataView(name, keys) {
  const view = {};
  for (const key of keys) {
    Object.defineProperty(view, key, {
      configurable: false,
      enumerable: true,
      get() {
        return requireInstalledProvider(`${name}.${key}`)[name][key];
      },
    });
  }
  return Object.freeze(view);
}

/** SLIP-10 curve identifiers, read through the installed provider. */
export const Curve = metadataView('Curve', CURVE_KEYS);

/** BIP-39 wordlist languages, read through the installed provider. */
export const Language = metadataView('Language', LANGUAGE_KEYS);

/** Capability descriptor for a wallet instance, read through the provider. */
export function getWalletOriginCapabilities(wallet) {
  return requireInstalledProvider('getWalletOriginCapabilities').getWalletOriginCapabilities(
    wallet,
  );
}

/**
 * Initialize the HD wallet runtime.
 * @returns {Promise<object>} the runtime module
 */
export async function createHDWallet() {
  const provider = await resolveProvider();
  return providerInitializer(provider)();
}

export default createHDWallet;

// EPM attestation is pure JavaScript over already-derived key material: it does
// not touch the runtime, so it stays bundled and keeps working with no provider.
export {
  buildCanonicalPayload,
  buildEPMSigningContent,
  signEPMContent,
  verifyEPMSignature,
  buildBitcoinChainProof,
  buildEthereumChainProof,
  buildSolanaChainProof,
  buildAllChainProofs,
  verifyChainProof,
  verifyAllChainProofs,
} from 'hd-wallet-wasm/attestation';
