/**
 * Baked-in server public keys for DHT discovery.
 *
 * These secp256k1 compressed public keys (33 bytes) are compiled into the
 * client SDK so that clients can derive the server's PeerID and discover
 * it via DHT without needing to know its IP address.
 *
 * To update: replace the hex string with the server's secp256k1 compressed
 * public key (33 bytes, hex-encoded). The key can be obtained from the
 * server's xpub at the BIP-44 identity path m/44'/0'/0'.
 *
 * At build time, these can be overridden via environment variables:
 *   SDN_LICENSE_SERVER_PUBKEY_HEX
 *   SDN_LICENSE_SERVER_XPUB
 */

/** Hex-encoded secp256k1 compressed public key of the OrbPro license server (33 bytes). */
export const LICENSE_SERVER_PUBKEY_HEX: string =
  process.env.SDN_LICENSE_SERVER_PUBKEY_HEX || '';

/** xpub of the OrbPro license server (Base58Check). PeerID is derivable from this. */
export const LICENSE_SERVER_XPUB: string =
  process.env.SDN_LICENSE_SERVER_XPUB || '';

/** Convert hex string to Uint8Array. */
export function hexToBytes(hex: string): Uint8Array {
  if (!hex || hex.length % 2 !== 0) return new Uint8Array(0);
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < hex.length; i += 2) {
    bytes[i / 2] = parseInt(hex.substr(i, 2), 16);
  }
  return bytes;
}

/** Get the license server secp256k1 public key as bytes. */
export function getLicenseServerPubkey(): Uint8Array {
  return hexToBytes(LICENSE_SERVER_PUBKEY_HEX);
}
