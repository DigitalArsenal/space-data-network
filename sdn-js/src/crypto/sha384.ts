/**
 * SHA-384 for flatsql's wasm integrity manifest.
 *
 * flatsql pins SHA-384 for `integrity.json` (an SRI-style `sha384-…` digest of
 * `flatsql.wasm`) and the verifier runs inside the engine worker, where no
 * function from the page survives structured clone. The node's crypto runtime
 * (hd-wallet-wasm) carries SHA-256 and SHA-512 but no SHA-384, so this is a
 * pure-JS digest from the audited @noble/hashes implementation — a checksum of
 * a public artifact, never key material, and never browser WebCrypto (the
 * runtime boundary, src/no-webcrypto-runtime.test.ts).
 */
import { sha384 } from '@noble/hashes/sha2';

export function sha384Digest(data: ArrayBuffer | Uint8Array): Uint8Array {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
  return sha384(bytes);
}
