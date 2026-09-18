/**
 * RSA key operations, disabled for the published browser bundle.
 *
 * WHY. `@libp2p/crypto`'s RSA implementation reaches for the WebCrypto
 * SubtleCrypto interface (importKey, generateKey, sign, verify, exportKey), and
 * this package forbids browser WebCrypto in a shipped bundle — every SDN
 * digest, signature and key derivation goes through the hd-wallet-wasm native
 * crypto boundary instead. See src/no-webcrypto-runtime.test.ts.
 *
 * WHAT IT CHANGES. Nothing, versus what sdn-js 3.0.0 shipped: that bundle
 * replaced `@libp2p/crypto/keys` wholesale with a module implementing Ed25519
 * and secp256k1 only, so RSA identities have never worked here. SDN identities
 * are secp256k1 (HD wallet) and go-libp2p peers are Ed25519. Nothing on the
 * wire is RSA.
 *
 * The OID keeps its real value so upstream's DER sniffing still RECOGNISES an
 * RSA key and routes it here to fail by name, rather than misparsing it.
 */

/** Real value: upstream DER sniffing matches on it before dispatching here. */
export const RSAES_PKCS1_V1_5_OID = '1.2.840.113549.1.1.1';

const DISABLED =
  'RSA is not available in @spacedatanetwork/sdn-js: the published bundle ' +
  'carries no WebCrypto, and SDN identities are secp256k1 while libp2p peers ' +
  'are Ed25519. If you are seeing this, something handed this node an RSA key.';

export async function generateRSAKey(_bits?: number): Promise<never> {
  throw new Error(DISABLED);
}

export async function hashAndSign(_key: unknown, _msg: unknown): Promise<never> {
  throw new Error(DISABLED);
}

export async function hashAndVerify(_key: unknown, _sig: unknown, _msg: unknown): Promise<never> {
  throw new Error(DISABLED);
}

export function rsaKeySize(_key: unknown): number {
  throw new Error(DISABLED);
}

/**
 * `randomBytes` re-exported as `getRandomValues` upstream. Kept real — it is
 * not WebCrypto, and removing it would break importers that only want bytes.
 */
export { randomBytes as getRandomValues } from '@libp2p/crypto';

/**
 * rsa.js reaches for `utils.jwkToPkix` / `utils.jwkToPkcs1` when materialising a
 * key's DER form. Those paths are only reachable from an RSA key, which cannot
 * be constructed here, so they throw by the same name rather than silently
 * returning empty bytes.
 */
export const utils = {
  jwkToPkix(_jwk: unknown): never {
    throw new Error(DISABLED);
  },
  jwkToPkcs1(_jwk: unknown): never {
    throw new Error(DISABLED);
  },
  pkixToJwk(_bytes: unknown): never {
    throw new Error(DISABLED);
  },
  pkcs1ToJwk(_bytes: unknown): never {
    throw new Error(DISABLED);
  },
};
