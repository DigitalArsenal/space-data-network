import { describe, expect, it, vi } from 'vitest';

vi.mock('./crypto/hd-wallet', async () => {
  const actual = await vi.importActual<typeof import('./crypto/hd-wallet')>('./crypto/hd-wallet');
  return {
    ...actual,
    derivePeerIdFromPublicKey: vi.fn(async () => 'provider-peer-id'),
  };
});

import {
  MODULE_DELIVERY_DISCOVERY_NAMESPACE,
  computeProviderDiscoveryCID,
  discoverProvider,
} from './discovery';

describe('discovery', () => {
  it('computes a deterministic CID from the provider public key', async () => {
    const cid = await computeProviderDiscoveryCID(hexToBytes('02'.padEnd(66, '1')));
    expect(cid.startsWith('b')).toBe(true);
  });

  it('derives the provider peer id and discovery cid from the trust-root key', async () => {
    const discovery = await discoverProvider(hexToBytes('03'.padEnd(66, '2')));

    expect(discovery).toEqual({
      peerId: 'provider-peer-id',
      discoveryCID: expect.any(String),
      discoveryNamespace: MODULE_DELIVERY_DISCOVERY_NAMESPACE,
    });
  });

  it('rejects non-compressed provider keys', async () => {
    await expect(computeProviderDiscoveryCID(new Uint8Array(32))).rejects.toThrow(/33-byte/i);
  });
});

function hexToBytes(hex: string): Uint8Array {
  const normalized = hex.trim().toLowerCase();
  const bytes = new Uint8Array(normalized.length / 2);
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number.parseInt(normalized.slice(index * 2, index * 2 + 2), 16);
  }
  return bytes;
}
