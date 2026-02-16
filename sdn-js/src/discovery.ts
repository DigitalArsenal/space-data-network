/**
 * DHT Discovery for SDN services.
 *
 * Enables clients to find servers by their baked-in Ed25519 public key:
 * 1. Client has the server's Ed25519 public key (baked at build time)
 * 2. Derives the server's PeerID from the public key
 * 3. Computes a CID from SHA-256(namespace + pubkey)
 * 4. Calls DHT FindProviders(CID) to discover the server's multiaddrs
 * 5. Opens a libp2p stream to the server's PeerID
 *
 * This mirrors the server-side pattern in streambridge.go.
 */

import { keys } from '@libp2p/crypto';
import { createFromPubKey } from '@libp2p/peer-id-factory';
import { sha256 } from './crypto/index';

/** Namespace used for computing the DHT CID. Must match server-side. */
const KEY_BROKER_CID_NAMESPACE = 'sdn-key-broker-pubkey';

/**
 * Derive a libp2p PeerID from an Ed25519 public key.
 *
 * The PeerID is the identity-multihash of the protobuf-encoded public key,
 * matching how libp2p identifies Ed25519 peers.
 */
export async function deriveServerPeerID(ed25519PubKey: Uint8Array): Promise<string> {
  if (ed25519PubKey.length !== 32) {
    throw new Error(`Expected 32-byte Ed25519 public key, got ${ed25519PubKey.length} bytes`);
  }
  const libp2pPub = keys.unmarshalPublicKey(
    marshalEd25519PublicKey(ed25519PubKey)
  );
  const peerId = await createFromPubKey(libp2pPub);
  return peerId.toString();
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
  ed25519PubKey: Uint8Array,
  namespace: string = KEY_BROKER_CID_NAMESPACE
): Promise<Uint8Array> {
  // Concatenate namespace + pubkey, then SHA-256
  const input = new Uint8Array(
    new TextEncoder().encode(namespace).length + ed25519PubKey.length
  );
  const nsBytes = new TextEncoder().encode(namespace);
  input.set(nsBytes, 0);
  input.set(ed25519PubKey, nsBytes.length);

  return sha256(input);
}

/**
 * Full discovery flow: derive PeerID and CID hash from a baked public key.
 *
 * Returns both the PeerID string and the SHA-256 hash used for DHT lookup.
 */
export async function discoverServer(ed25519PubKey: Uint8Array): Promise<{
  peerId: string;
  cidHash: Uint8Array;
}> {
  const [peerId, cidHash] = await Promise.all([
    deriveServerPeerID(ed25519PubKey),
    computeServerCIDHash(ed25519PubKey),
  ]);
  return { peerId, cidHash };
}

/**
 * Marshal an Ed25519 public key into the libp2p protobuf format.
 *
 * Format: varint(1) = Ed25519 key type, then the 32-byte key.
 * This is the protobuf encoding that libp2p expects:
 *   message PublicKey { KeyType Type = 1; bytes Data = 2; }
 * where Type=1 (Ed25519).
 */
function marshalEd25519PublicKey(pubKey: Uint8Array): Uint8Array {
  // Protobuf: field 1 (KeyType) = varint 1, field 2 (Data) = bytes
  // field 1: tag=0x08, value=0x01
  // field 2: tag=0x12, length=0x20 (32 bytes), data
  const buf = new Uint8Array(2 + 2 + 32);
  buf[0] = 0x08; // field 1 tag
  buf[1] = 0x01; // KeyType.Ed25519
  buf[2] = 0x12; // field 2 tag
  buf[3] = 0x20; // length = 32
  buf.set(pubKey, 4);
  return buf;
}
