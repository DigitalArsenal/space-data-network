/**
 * ECDSA key operations, disabled for the published browser bundle.
 *
 * WHY. `@libp2p/crypto`'s ECDSA implementation is the only part of that package
 * that reaches for the WebCrypto SubtleCrypto interface, and this package forbids
 * browser WebCrypto in a shipped bundle — every SDN digest, signature and key
 * derivation goes through the hd-wallet-wasm native crypto boundary instead. See
 * src/no-webcrypto-runtime.test.ts, which fails when a WebCrypto call reaches
 * `dist/`.
 *
 * WHAT IT CHANGES. Nothing, versus what sdn-js 3.0.0 already shipped: that
 * bundle replaced `@libp2p/crypto/keys` wholesale with a hand-written module
 * that implemented Ed25519 and secp256k1 only, so ECDSA identities have never
 * worked in this package. SDN identities are secp256k1 (HD wallet) and
 * go-libp2p peers are Ed25519, so nothing on the wire uses ECDSA.
 *
 * The OID constants keep their real values so upstream's DER sniffing still
 * RECOGNISES an ECDSA key and routes it here to fail with a clear message,
 * rather than misparsing it as something else.
 *
 * This substitutes ONE leaf module. Everything else in `@libp2p/crypto` —
 * protobuf encoding, key classes, Ed25519, secp256k1, the multihash/CID
 * plumbing the noise handshake depends on — is upstream's own code, which is
 * also the code the test suite exercises.
 */

export const ECDSA_P_256_OID = '1.2.840.10045.3.1.7';
export const ECDSA_P_384_OID = '1.3.132.0.34';
export const ECDSA_P_521_OID = '1.3.132.0.35';

const message =
  'ECDSA keys are disabled in the SDN browser bundle (WebCrypto is not available at this boundary); SDN identities are secp256k1 and network peers are Ed25519.';

export async function generateECDSAKey(): Promise<never> {
  throw new Error(message);
}

export async function hashAndSign(): Promise<never> {
  throw new Error(message);
}

export async function hashAndVerify(): Promise<never> {
  throw new Error(message);
}
