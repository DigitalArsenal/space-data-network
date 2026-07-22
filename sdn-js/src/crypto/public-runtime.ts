import { gcm } from '@noble/ciphers/aes';
import { ed25519 } from '@noble/curves/ed25519';
import { sha256 as nobleSha256 } from '@noble/hashes/sha256';

/**
 * Public, non-wallet cryptographic primitives used by the read-only web UI.
 *
 * This module deliberately has no dependency on the HD wallet runtime: it can
 * verify public signatures, hash bytes, and decrypt an already-authorized
 * symmetric payload without exposing signing or key-derivation capabilities.
 */
export function publicSha256(data: Uint8Array): Uint8Array {
  return nobleSha256(data);
}

export function verifyPublicEd25519Signature(
  publicKey: Uint8Array,
  message: Uint8Array,
  signature: Uint8Array,
): boolean {
  return ed25519.verify(signature, message, publicKey);
}

export async function decryptPublicAesGcm(
  key: Uint8Array,
  ciphertextAndTag: Uint8Array,
  iv: Uint8Array,
  aad = new Uint8Array(0),
): Promise<Uint8Array> {
  return gcm(cloneBytes(key), cloneBytes(iv), cloneBytes(aad))
    .decrypt(cloneBytes(ciphertextAndTag));
}

function cloneBytes(bytes: Uint8Array): Uint8Array<ArrayBuffer> {
  return new Uint8Array(bytes);
}
