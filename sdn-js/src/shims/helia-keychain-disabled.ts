/**
 * Helia's keychain, disabled for the published bundle.
 *
 * `@ipshipyard/keychain` stores keys encrypted with WebCrypto-derived material,
 * which this package does not ship (src/no-webcrypto-runtime.test.ts). sdn-js
 * never stores a key in Helia: the node identity is HD-wallet derived and handed
 * to libp2p directly, so `helia.keychain` has no caller here.
 *
 * helia's constructor calls `keychain()(components)` unconditionally, so this
 * returns an object whose methods refuse rather than throwing at construction.
 * The same boundary sdn-js 3.0.0 shipped for `@libp2p/keychain`.
 */

const message = 'The Helia keychain is disabled in the SDN bundle; node identity comes from the HD wallet.';

export function keychain(): (components?: unknown) => Record<string, unknown> {
  const refuse = async (): Promise<never> => {
    throw new Error(message);
  };
  return () => ({
    createKey: refuse,
    exportKey: refuse,
    importKey: refuse,
    listKeys: refuse,
    findKeyById: refuse,
    findKeyByName: refuse,
    removeKey: refuse,
    renameKey: refuse,
    rotateKeychainPass: refuse,
  });
}
