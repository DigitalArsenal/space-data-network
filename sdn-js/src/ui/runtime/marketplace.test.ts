import { describe, expect, it } from 'vitest';

import {
  canonicalListingKey,
  createMarketplaceIndex,
  type CanonicalListing,
} from './marketplace';

describe('canonicalListingKey', () => {
  it('dedupes listings by plugin id and version', () => {
    expect(
      canonicalListingKey({
        pluginId: 'orbital-demo',
        version: '1.2.3',
      }),
    ).toBe('orbital-demo@1.2.3');
  });
});

describe('createMarketplaceIndex', () => {
  it('keeps the newest listing snapshot for the same canonical key', () => {
    const olderListing: CanonicalListing = {
      pluginId: 'orbital-demo',
      version: '1.2.3',
      name: 'Orbital Demo',
      publisherName: 'A',
      observedAt: 100,
    };
    const newerListing: CanonicalListing = {
      ...olderListing,
      publisherName: 'B',
      observedAt: 200,
    };

    const index = createMarketplaceIndex([olderListing, newerListing]);

    expect(index.count()).toBe(1);
    expect(index.get('orbital-demo', '1.2.3')).toEqual(newerListing);
    expect(index.values()).toEqual([newerListing]);
  });

  it('returns listings in descending observed order for dynamic rendering', () => {
    const index = createMarketplaceIndex([
      {
        pluginId: 'plugin-a',
        version: '1.0.0',
        observedAt: 10,
      },
      {
        pluginId: 'plugin-b',
        version: '2.0.0',
        observedAt: 30,
      },
      {
        pluginId: 'plugin-c',
        version: '3.0.0',
        observedAt: 20,
      },
    ]);

    expect(index.values().map((listing) => listing.pluginId)).toEqual([
      'plugin-b',
      'plugin-c',
      'plugin-a',
    ]);
  });
});
