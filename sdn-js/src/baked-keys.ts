/**
 * Baked-in server public keys for DHT discovery.
 *
 * These Ed25519 public keys are compiled into the client SDK so that
 * clients can derive the server's PeerID and discover it via DHT
 * without needing to know its IP address.
 *
 * To update: replace the hex string with the server's Ed25519 signing
 * public key (32 bytes, hex-encoded). The key can be obtained from
 * the server's EPM or from the HD wallet signing derivation path
 * m/44'/0'/0'/0'/0'.
 *
 * At build time, these can be overridden via environment variables:
 *   SDN_LICENSE_SERVER_PUBKEY_HEX
 */

/** Hex-encoded Ed25519 public key of the OrbPro license server. */
export const LICENSE_SERVER_PUBKEY_HEX: string =
  process.env.SDN_LICENSE_SERVER_PUBKEY_HEX || '';

/** Convert hex string to Uint8Array. */
export function hexToBytes(hex: string): Uint8Array {
  if (!hex || hex.length % 2 !== 0) return new Uint8Array(0);
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < hex.length; i += 2) {
    bytes[i / 2] = parseInt(hex.substr(i, 2), 16);
  }
  return bytes;
}

/** Get the license server Ed25519 public key as bytes. */
export function getLicenseServerPubkey(): Uint8Array {
  return hexToBytes(LICENSE_SERVER_PUBKEY_HEX);
}
