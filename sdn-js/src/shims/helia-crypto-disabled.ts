/**
 * Helia's WebCrypto key implementations, disabled for the published bundle.
 *
 * helia 7 moved its key handling to `@ipshipyard/crypto`, whose ECDSA, Ed25519
 * and RSA implementations are all built on WebCrypto's SubtleCrypto interface.
 * sdn-js forbids browser WebCrypto in a shipped bundle
 * (src/no-webcrypto-runtime.test.ts) — all SDN crypto goes through the
 * hd-wallet-wasm native boundary.
 *
 * These implementations back `helia.getCrypto()` and `helia.keychain`, neither of
 * which sdn-js uses: this package's identities come from the HD wallet, blocks
 * are verified by hash, and nothing here publishes or resolves IPNS. helia
 * constructs both eagerly, so the substitution has to be a module, not a
 * config flag.
 *
 * Same product boundary sdn-js 3.0.0 shipped, where `@libp2p/keychain` was
 * likewise replaced by a module that refused every call.
 */

const message =
  'Helia WebCrypto key operations are disabled in the SDN bundle; SDN keys come from the hd-wallet-wasm native crypto boundary.';

interface DisabledCrypto {
  type: string;
  code: number;
  generatePrivateKey(): Promise<never>;
  publicKeyFromProtobuf(): Promise<never>;
  privateKeyFromProtobuf(): Promise<never>;
  publicKeyFromRaw?(): Promise<never>;
  privateKeyFromRaw?(): Promise<never>;
}

function disabledCrypto(type: string, code: number): DisabledCrypto {
  const refuse = async (): Promise<never> => {
    throw new Error(`${message} (${type})`);
  };
  return {
    type,
    code,
    generatePrivateKey: refuse,
    publicKeyFromProtobuf: refuse,
    privateKeyFromProtobuf: refuse,
    publicKeyFromRaw: refuse,
    privateKeyFromRaw: refuse,
  };
}

export function rsaCrypto(): DisabledCrypto {
  return disabledCrypto('RSA', 0);
}

export function ed25519Crypto(): DisabledCrypto {
  return disabledCrypto('Ed25519', 1);
}

export function ecdsaCrypto(): DisabledCrypto {
  return disabledCrypto('ECDSA', 3);
}

export function isPublicKey(obj?: unknown): boolean {
  return (obj as { type?: unknown })?.type != null && typeof (obj as { verify?: unknown })?.verify === 'function';
}

export function isPrivateKey(obj?: unknown): boolean {
  return (obj as { type?: unknown })?.type != null && typeof (obj as { sign?: unknown })?.sign === 'function';
}
