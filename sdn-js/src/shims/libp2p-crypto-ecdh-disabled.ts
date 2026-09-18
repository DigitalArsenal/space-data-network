/**
 * ECDH ephemeral key exchange, disabled for the published browser bundle.
 *
 * WHY. `@libp2p/crypto`'s ECDH implementation reaches for WebCrypto, which this
 * package forbids in a shipped bundle (src/no-webcrypto-runtime.test.ts).
 *
 * WHAT IT CHANGES. Nothing. This entry point existed for libp2p's retired SECIO
 * handshake; the encryption SDN actually negotiates is noise, whose key
 * agreement is X25519 and does not come through here. SDN's own ECIES path has
 * its own ECDH over secp256k1 in src/ecies.ts and never imports this one.
 */

const DISABLED =
  'ECDH via @libp2p/crypto is not available in @spacedatanetwork/sdn-js: the ' +
  'published bundle carries no WebCrypto. Noise (X25519) is the negotiated ' +
  'encryption, and SDN ECIES uses its own secp256k1 ECDH in src/ecies.ts.';

export async function generateEphemeralKeyPair(_curve?: string): Promise<never> {
  throw new Error(DISABLED);
}
