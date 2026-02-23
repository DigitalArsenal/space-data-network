/**
 * DHT Discovery for SDN services.
 *
 * Enables clients to find servers by their baked-in secp256k1 public key:
 * 1. Client has the server's secp256k1 compressed public key (baked at build time)
 * 2. Derives the server's PeerID from the public key via hd-wallet-wasm
 * 3. Computes a CID from SHA-256(namespace + pubkey)
 * 4. Calls DHT FindProviders(CID) to discover the server's multiaddrs
 * 5. Opens a libp2p stream to the server's PeerID
 *
 * This mirrors the server-side pattern in streambridge.go.
 */

import { sha256, derivePeerIdFromPublicKey, derivePeerIdFromXpub } from './crypto/index';

/** Namespace used for computing the DHT CID. Must match server-side. */
const KEY_BROKER_CID_NAMESPACE = 'sdn-key-broker-pubkey';

/**
 * Derive a libp2p PeerID from a secp256k1 compressed public key (33 bytes)
 * or from an xpub string.
 */
export async function deriveServerPeerID(keyOrXpub: Uint8Array | string): Promise<string> {
  if (typeof keyOrXpub === 'string') {
    return derivePeerIdFromXpub(keyOrXpub);
  }
  if (keyOrXpub.length !== 33) {
    throw new Error(`Expected 33-byte secp256k1 compressed public key, got ${keyOrXpub.length} bytes`);
  }
  return derivePeerIdFromPublicKey(keyOrXpub);
}

/**
 * Compute the CID that the server announces to the DHT.
 *
 * This is SHA-256(namespace + pubkey), encoded as a CIDv1 with raw codec.
 * Clients use this CID with FindProviders to locate the server.
 *
 * Returns the raw multihash bytes (SHA-256). The caller is responsible
 * for constructing the full CID if needed for their DHT implementation.
 */
export async function computeServerCIDHash(
  pubKey: Uint8Array,
  namespace: string = KEY_BROKER_CID_NAMESPACE
): Promise<Uint8Array> {
  // Concatenate namespace + pubkey, then SHA-256
  const nsBytes = new TextEncoder().encode(namespace);
  const input = new Uint8Array(nsBytes.length + pubKey.length);
  input.set(nsBytes, 0);
  input.set(pubKey, nsBytes.length);

  return sha256(input);
}

/**
 * Full discovery flow: derive PeerID and CID hash from a baked public key.
 *
 * Returns both the PeerID string and the SHA-256 hash used for DHT lookup.
 */
export async function discoverServer(pubKey: Uint8Array): Promise<{
  peerId: string;
  cidHash: Uint8Array;
}> {
  const [peerId, cidHash] = await Promise.all([
    deriveServerPeerID(pubKey),
    computeServerCIDHash(pubKey),
  ]);
  return { peerId, cidHash };
}
