import type { AddressLookupChain, AddressLookupKey } from './types';

const BASE32_ALPHABET = 'abcdefghijklmnopqrstuvwxyz234567';
const encoder = new TextEncoder();
const CASE_INSENSITIVE_CHAINS = new Set(['bitcoin', 'ethereum']);

export const ADDRESS_LOOKUP_NAMESPACE_PREFIX = 'space-data-network/identity/address';

export function addressLookupNamespace(chain: AddressLookupChain): string {
  const normalizedChain = normalizeChain(chain);
  return `${ADDRESS_LOOKUP_NAMESPACE_PREFIX}/${normalizedChain}`;
}

export async function normalizeAddressLookupKey(
  chain: AddressLookupChain,
  value: string,
): Promise<AddressLookupKey> {
  const namespace = addressLookupNamespace(chain);
  const normalizedValue = normalizeLookupValue(chain, value);
  const discoveryCID = await encodeDiscoveryCID(`${namespace}:${normalizedValue}`);

  return {
    chain: normalizeChain(chain),
    namespace,
    normalizedValue,
    discoveryCID,
  };
}

function normalizeChain(chain: AddressLookupChain): string {
  const normalized = String(chain).trim().toLowerCase();
  if (!normalized) {
    throw new Error('address lookup chain is required');
  }
  return normalized;
}

function normalizeLookupValue(chain: AddressLookupChain, value: string): string {
  const trimmed = value.trim();
  if (!trimmed) {
    throw new Error('address lookup value is required');
  }

  if (CASE_INSENSITIVE_CHAINS.has(normalizeChain(chain))) {
    return trimmed.toLowerCase();
  }

  return trimmed;
}

async function encodeDiscoveryCID(input: string): Promise<string> {
  const hash = await sha256(encoder.encode(input));
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

async function sha256(data: Uint8Array): Promise<Uint8Array> {
  const copy = new Uint8Array(data.byteLength);
  copy.set(data);
  const digest = await crypto.subtle.digest('SHA-256', copy);
  return new Uint8Array(digest);
}
