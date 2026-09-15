import initHDWallet from 'hd-wallet-wasm';
import { createWalletOriginApp } from '../wallet-origin/index.js';

export function normalizeTabHash(value) {
  if (typeof value !== 'string') return null;
  const normalized = value.replace(/^#/, '').trim().toLowerCase();
  return /^[a-z][a-z0-9-]{0,63}$/u.test(normalized) ? normalized : null;
}

export function normalizeCreateWalletUIArguments(rootElementOrOptions, options = {}) {
  const NodeConstructor = globalThis.Node;
  const isNode = typeof NodeConstructor === 'function'
    && rootElementOrOptions instanceof NodeConstructor;
  if (isNode || rootElementOrOptions === null || rootElementOrOptions === undefined) {
    return { element: rootElementOrOptions ?? null, options: options ?? {} };
  }
  if (typeof rootElementOrOptions !== 'object' || Array.isArray(rootElementOrOptions)) {
    throw new TypeError('createWalletUI expects an element or an options object');
  }
  const { element = null, ...objectOptions } = rootElementOrOptions;
  if (element !== null
      && !(typeof NodeConstructor === 'function' && element instanceof NodeConstructor)) {
    throw new TypeError('createWalletUI element must be a DOM Node');
  }
  return { element, options: objectOptions };
}

export async function createWalletUI(rootElementOrOptions, options = {}) {
  const normalized = normalizeCreateWalletUIArguments(rootElementOrOptions, options);
  const configuration = normalized.options ?? {};
  const documentObject = configuration.document
    ?? normalized.element?.ownerDocument
    ?? globalThis.document;
  const windowObject = configuration.window
    ?? documentObject?.defaultView
    ?? globalThis.window;
  const wasm = await (configuration.wasm ?? initHDWallet());
  const app = createWalletOriginApp({
    clipboard: configuration.clipboard,
    credentialPrompt: configuration.credentialPrompt,
    document: documentObject,
    fetch: configuration.fetch,
    location: configuration.location,
    mount: normalized.element ?? documentObject?.body,
    registry: configuration.registry,
    relay: configuration.relay,
    rng: configuration.rng,
    wasm,
    window: windowObject,
  });
  const open = () => app.start();
  return Object.freeze({
    openLogin: open,
    openAccount: open,
    logout: () => app.logout(),
    destroy: () => app.stop('destroy'),
  });
}

export async function init(rootElementOrOptions, options = {}) {
  return createWalletUI(rootElementOrOptions, options);
}
