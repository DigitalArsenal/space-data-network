import { derivePeerIdFromPublicKey } from './crypto/hd-wallet';

const BASE32_ALPHABET = 'abcdefghijklmnopqrstuvwxyz234567';
const encoder = new TextEncoder();

export const MODULE_DELIVERY_DISCOVERY_NAMESPACE = 'space-data-network/module-delivery/provider-pubkey';

export async function deriveProviderPeerId(publicKey: Uint8Array): Promise<string> {
  assertCompressedPublicKey(publicKey);
  return derivePeerIdFromPublicKey(publicKey);
}

export async function computeProviderDiscoveryCID(
  publicKey: Uint8Array,
  namespace: string = MODULE_DELIVERY_DISCOVERY_NAMESPACE,
): Promise<string> {
  assertCompressedPublicKey(publicKey);
  const namespaceBytes = encoder.encode(namespace);
  const input = new Uint8Array(namespaceBytes.length + publicKey.length);
  input.set(namespaceBytes, 0);
  input.set(publicKey, namespaceBytes.length);

  const hash = await sha256(input);
  return encodeCIDv1Raw(hash);
}

export async function discoverProvider(publicKey: Uint8Array): Promise<{
  peerId: string;
  discoveryCID: string;
  discoveryNamespace: string;
}> {
  const [peerId, discoveryCID] = await Promise.all([
    deriveProviderPeerId(publicKey),
    computeProviderDiscoveryCID(publicKey),
  ]);

  return {
    peerId,
    discoveryCID,
    discoveryNamespace: MODULE_DELIVERY_DISCOVERY_NAMESPACE,
  };
}

function assertCompressedPublicKey(publicKey: Uint8Array): void {
  if (publicKey.length !== 33) {
    throw new Error(`Expected 33-byte compressed secp256k1 public key, got ${publicKey.length} bytes`);
  }
  if (publicKey[0] !== 0x02 && publicKey[0] !== 0x03) {
    throw new Error('Expected compressed secp256k1 public key prefix (0x02/0x03)');
  }
}

async function sha256(value: Uint8Array): Promise<Uint8Array> {
  if (globalThis.crypto?.subtle) {
    const digest = await globalThis.crypto.subtle.digest('SHA-256', Uint8Array.from(value));
    return new Uint8Array(digest);
  }

  const cryptoModule = await import('node:crypto');
  return new Uint8Array(cryptoModule.createHash('sha256').update(Buffer.from(value)).digest());
}

function encodeCIDv1Raw(hash: Uint8Array): string {
  const cidBytes = new Uint8Array(4 + hash.length);
  cidBytes[0] = 0x01;
  cidBytes[1] = 0x55;
  cidBytes[2] = 0x12;
  cidBytes[3] = 0x20;
  cidBytes.set(hash, 4);
  return `b${base32Encode(cidBytes)}`;
}

function base32Encode(value: Uint8Array): string {
  let output = '';
  let bits = 0;
  let current = 0;

  for (const byte of value) {
    current = (current << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      output += BASE32_ALPHABET[(current >>> (bits - 5)) & 0x1f];
      bits -= 5;
    }
  }

  if (bits > 0) {
    output += BASE32_ALPHABET[(current << (5 - bits)) & 0x1f];
  }

  return output;
}
