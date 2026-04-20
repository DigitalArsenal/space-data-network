import { describe, expect, it, vi } from 'vitest';

import { createRuntimeModuleLoader } from '../../ui/src/runtime-modules';

describe('createRuntimeModuleLoader', () => {
  it('memoizes the dynamic runtime imports', async () => {
    const loadCrypto = vi.fn(async () => ({
      initHDWallet: async () => true,
      deriveIdentity: async () => ({ encryptionKey: { privateKey: new Uint8Array() } }),
      randomBytes: (length: number) => new Uint8Array(length),
    }));
    const loadDiscovery = vi.fn(async () => ({
      discoverProvider: async () => ({ discoveryCID: 'cid' }),
    }));

    const loader = createRuntimeModuleLoader({
      loadCrypto,
      loadDiscovery,
      loadModuleDelivery: async () => ({
        fetchEncryptedModuleBundle: async () => ({
          grant: { wrappedContentKey: null, bundleDescriptor: { cid: 'cid' } },
          encryptedBundleBytes: new Uint8Array(),
        }),
      }),
      loadNode: async () => ({ SDNNode: { create: async () => ({}) } }),
      loadAddressLookup: async () => ({
        normalizeAddressLookupKey: async () => ({
          normalizedValue: 'x',
          discoveryCID: 'cid',
        }),
      }),
      loadLiveDelivery: async () => ({
        decryptEncryptedModuleBundle: async () => new Uint8Array(),
        invokeLoadedModule: async () => ({}),
        loadDecryptedModule: async () => ({}),
        unwrapGrantContentKey: async () => new Uint8Array(),
      }),
    });

    await loader.load();
    await loader.load();

    expect(loadCrypto).toHaveBeenCalledTimes(1);
    expect(loadDiscovery).toHaveBeenCalledTimes(1);
  });
});
