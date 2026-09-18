/**
 * Ed25519 without WebCrypto.
 *
 * WHY. `@libp2p/crypto`'s ed25519 browser leaf tries SubtleCrypto first and
 * falls back to @noble/curves when it is unavailable — so its WebCrypto code is
 * unreachable in this bundle but its BYTES still ship, and this package forbids
 * WebCrypto in a published bundle (src/no-webcrypto-runtime.test.ts). The node
 * leaf has no WebCrypto but imports node's `crypto`, so it cannot be substituted
 * in a browser build either.
 *
 * Unlike the ecdsa/rsa/ecdh leaves next to it, this one CANNOT be disabled:
 * go-libp2p peers are Ed25519, so signing and verification here are on the live
 * wire path. It is re-implemented over @noble/curves — the same library, the
 * same curve, the same bytes — which is exactly the fallback upstream would
 * have taken, made unconditional.
 *
 * hashAndSign / hashAndVerify stay ASYNC to match the browser leaf's signature,
 * because that is what callers in @libp2p/crypto await.
 */
import { ed25519 as ed } from '@noble/curves/ed25519.js';

const KEYS_BYTE_LENGTH = 32;

/** Same constants the leaf this replaces exports; ed25519.js consumes them. */
export const publicKeyLength = KEYS_BYTE_LENGTH;
export const privateKeyLength = KEYS_BYTE_LENGTH * 2;

function concatKeys(privateKeyRaw: Uint8Array, publicKey: Uint8Array): Uint8Array {
  const privateKey = new Uint8Array(KEYS_BYTE_LENGTH * 2);
  privateKey.set(privateKeyRaw, 0);
  privateKey.set(publicKey, KEYS_BYTE_LENGTH);
  return privateKey;
}

function bytes(msg: Uint8Array | { subarray(): Uint8Array }): Uint8Array {
  return msg instanceof Uint8Array ? msg : msg.subarray();
}

export function generateKey(): { privateKey: Uint8Array; publicKey: Uint8Array } {
  const privateKeyRaw = ed.utils.randomSecretKey();
  const publicKey = ed.getPublicKey(privateKeyRaw);
  return { privateKey: concatKeys(privateKeyRaw, publicKey), publicKey };
}

export function generateKeyFromSeed(seed: Uint8Array): { privateKey: Uint8Array; publicKey: Uint8Array } {
  if (!(seed instanceof Uint8Array)) {
    throw new TypeError('"seed" must be a Uint8Array.');
  }
  if (seed.length !== KEYS_BYTE_LENGTH) {
    throw new TypeError('"seed" must be 32 bytes in length.');
  }
  const publicKey = ed.getPublicKey(seed);
  return { privateKey: concatKeys(seed, publicKey), publicKey };
}

export function hashAndSignNoble(privateKey: Uint8Array, msg: Uint8Array | { subarray(): Uint8Array }): Uint8Array {
  return ed.sign(bytes(msg), privateKey.subarray(0, KEYS_BYTE_LENGTH));
}

export function hashAndVerifyNoble(publicKey: Uint8Array, sig: Uint8Array, msg: Uint8Array | { subarray(): Uint8Array }): boolean {
  return ed.verify(sig, bytes(msg), publicKey);
}

export async function hashAndSign(privateKey: Uint8Array, msg: Uint8Array | { subarray(): Uint8Array }): Promise<Uint8Array> {
  return hashAndSignNoble(privateKey, msg);
}

export async function hashAndVerify(publicKey: Uint8Array, sig: Uint8Array, msg: Uint8Array | { subarray(): Uint8Array }): Promise<boolean> {
  return hashAndVerifyNoble(publicKey, sig, msg);
}
