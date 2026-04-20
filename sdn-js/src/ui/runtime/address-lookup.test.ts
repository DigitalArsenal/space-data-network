import { describe, expect, it } from 'vitest';

import {
  ADDRESS_LOOKUP_NAMESPACE_PREFIX,
  addressLookupNamespace,
  normalizeAddressLookupKey,
} from './address-lookup';

describe('addressLookupNamespace', () => {
  it('builds the deterministic namespace for a blockchain address type', () => {
    expect(addressLookupNamespace('bitcoin')).toBe(
      `${ADDRESS_LOOKUP_NAMESPACE_PREFIX}/bitcoin`,
    );
  });
});

describe('normalizeAddressLookupKey', () => {
  it('normalizes bitcoin addresses and returns a deterministic discovery cid', async () => {
    const key = await normalizeAddressLookupKey('bitcoin', ' BC1QTESTADDRESS ');

    expect(key.namespace).toBe(`${ADDRESS_LOOKUP_NAMESPACE_PREFIX}/bitcoin`);
    expect(key.normalizedValue).toBe('bc1qtestaddress');
    expect(key.discoveryCID.startsWith('b')).toBe(true);
  });

  it('rejects empty lookup values', async () => {
    await expect(normalizeAddressLookupKey('bitcoin', '   ')).rejects.toThrow(
      /address lookup value is required/i,
    );
  });
});
