import type { RuntimeModules } from './state/types';

interface RuntimeModuleDeps {
  loadCrypto: () => Promise<typeof import('../../src/crypto')>;
  loadDiscovery: () => Promise<typeof import('../../src/discovery')>;
  loadModuleDelivery: () => Promise<typeof import('../../src/module-delivery')>;
  loadNode: () => Promise<typeof import('../../src/node')>;
  loadAddressLookup: () => Promise<typeof import('../../src/ui/runtime/address-lookup')>;
  loadLiveDelivery: () => Promise<typeof import('../../src/ui/runtime/live-delivery')>;
}

const defaultDeps: RuntimeModuleDeps = {
  loadCrypto: () => import('../../src/crypto'),
  loadDiscovery: () => import('../../src/discovery'),
  loadModuleDelivery: () => import('../../src/module-delivery'),
  loadNode: () => import('../../src/node'),
  loadAddressLookup: () => import('../../src/ui/runtime/address-lookup'),
  loadLiveDelivery: () => import('../../src/ui/runtime/live-delivery'),
};

export function createRuntimeModuleLoader(
  deps: RuntimeModuleDeps = defaultDeps,
): { load: () => Promise<RuntimeModules> } {
  let promise: Promise<RuntimeModules> | null = null;

  return {
    async load(): Promise<RuntimeModules> {
      if (!promise) {
        promise = Promise.all([
          deps.loadCrypto(),
          deps.loadDiscovery(),
          deps.loadModuleDelivery(),
          deps.loadNode(),
          deps.loadAddressLookup(),
          deps.loadLiveDelivery(),
        ]).then(([crypto, discovery, moduleDelivery, node, addressLookup, liveDelivery]) => ({
          initHDWallet: crypto.initHDWallet,
          deriveIdentity: crypto.deriveIdentity,
          randomBytes: crypto.randomBytes,
          discoverProvider: discovery.discoverProvider,
          fetchEncryptedModuleBundle: moduleDelivery.fetchEncryptedModuleBundle,
          SDNNode: node.SDNNode,
          normalizeAddressLookupKey: addressLookup.normalizeAddressLookupKey,
          decryptEncryptedModuleBundle: liveDelivery.decryptEncryptedModuleBundle,
          invokeLoadedModule: liveDelivery.invokeLoadedModule,
          loadDecryptedModule: liveDelivery.loadDecryptedModule,
          unwrapGrantContentKey: liveDelivery.unwrapGrantContentKey,
        }));
      }
      return promise;
    },
  };
}
