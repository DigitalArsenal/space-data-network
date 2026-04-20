import { describe, expect, it } from 'vitest';

import type { CanonicalListing } from './types';
import { buildStoreFeed, searchStoreListings } from './store-search';

describe('searchStoreListings', () => {
  it('groups marketplace results into authors, plugins, and standards-linked data', () => {
    const listings: CanonicalListing[] = [
      {
        pluginId: 'com.space-data-network.orbital-demo',
        version: '1.2.3',
        name: 'Orbital Demo',
        publisherName: 'Space Ops',
        publisherHandle: '@spaceops',
        standardsUsed: ['OMM', 'OEM'],
        observedAt: 20,
      },
      {
        pluginId: 'com.space-data-network.reentry-watch',
        version: '4.5.6',
        name: 'Reentry Watch',
        publisherName: 'Mission Control',
        publisherHandle: '@missioncontrol',
        standardsUsed: ['CDM'],
        observedAt: 10,
      },
    ];

    const results = searchStoreListings(listings);

    expect(results.authors).toEqual([
      expect.objectContaining({
        key: 'mission control',
        name: 'Mission Control',
        moduleCount: 1,
        standardsUsed: ['CDM'],
      }),
      expect.objectContaining({
        key: 'space ops',
        name: 'Space Ops',
        moduleCount: 1,
        standardsUsed: ['OEM', 'OMM'],
      }),
    ]);
    expect(results.plugins.map((entry) => entry.listing.pluginId)).toEqual([
      'com.space-data-network.orbital-demo',
      'com.space-data-network.reentry-watch',
    ]);
    expect(results.data).toEqual([
      expect.objectContaining({
        key: 'CDM',
        standard: 'CDM',
        moduleCount: 1,
      }),
      expect.objectContaining({
        key: 'OEM',
        standard: 'OEM',
        moduleCount: 1,
      }),
      expect.objectContaining({
        key: 'OMM',
        standard: 'OMM',
        moduleCount: 1,
      }),
    ]);
  });

  it('filters across author, plugin, and standards metadata from one search box', () => {
    const listings: CanonicalListing[] = [
      {
        pluginId: 'com.space-data-network.orbital-demo',
        version: '1.2.3',
        name: 'Orbital Demo',
        publisherName: 'Space Ops',
        publisherHandle: '@spaceops',
        standardsUsed: ['OMM'],
        tags: ['orbit', 'OMM'],
      },
      {
        pluginId: 'com.space-data-network.telemetry-lab',
        version: '4.5.6',
        name: 'Telemetry Lab',
        publisherName: 'Telemetry Works',
        publisherHandle: '@telemetry',
        standardsUsed: ['TDM'],
        tags: ['tracking', 'TDM'],
      },
    ];

    expect(searchStoreListings(listings, 'space ops').authors).toEqual([
      expect.objectContaining({ name: 'Space Ops' }),
    ]);
    expect(searchStoreListings(listings, 'orbital').plugins).toEqual([
      expect.objectContaining({
        listing: expect.objectContaining({ pluginId: 'com.space-data-network.orbital-demo' }),
      }),
    ]);
    expect(searchStoreListings(listings, 'tracking').data).toEqual([
      expect.objectContaining({ standard: 'TDM' }),
    ]);
  });

  it('builds a single popular feed for blank searches without empty placeholder sections', () => {
    const listings: CanonicalListing[] = [
      {
        pluginId: 'com.space-data-network.orbital-demo',
        version: '1.2.3',
        name: 'Orbital Demo',
        publisherName: 'Space Ops',
        standardsUsed: ['OMM', 'OEM'],
        observedAt: 20,
      },
      {
        pluginId: 'com.space-data-network.telemetry-lab',
        version: '4.5.6',
        name: 'Telemetry Lab',
        publisherName: 'Telemetry Works',
        standardsUsed: ['TDM'],
        observedAt: 10,
      },
    ];

    const feed = buildStoreFeed(searchStoreListings(listings));

    expect(feed.mode).toBe('popular');
    expect(feed.entries).toEqual([
      expect.objectContaining({
        kind: 'plugin',
        key: 'com.space-data-network.orbital-demo@1.2.3',
        standardsUsed: ['OEM', 'OMM'],
      }),
      expect.objectContaining({
        kind: 'plugin',
        key: 'com.space-data-network.telemetry-lab@4.5.6',
        standardsUsed: ['TDM'],
      }),
      expect.objectContaining({
        kind: 'data',
        key: 'OEM',
      }),
      expect.objectContaining({
        kind: 'data',
        key: 'OMM',
      }),
      expect.objectContaining({
        kind: 'data',
        key: 'TDM',
      }),
    ]);
  });

  it('builds one mixed search feed across authors, plugins, and data', () => {
    const listings: CanonicalListing[] = [
      {
        pluginId: 'com.space-data-network.orbital-demo',
        version: '1.2.3',
        name: 'Orbital Demo',
        publisherName: 'Space Ops',
        publisherHandle: '@spaceops',
        standardsUsed: ['OMM'],
      },
      {
        pluginId: 'com.space-data-network.telemetry-lab',
        version: '4.5.6',
        name: 'Telemetry Lab',
        publisherName: 'Telemetry Works',
        standardsUsed: ['TDM'],
      },
    ];

    const feed = buildStoreFeed(searchStoreListings(listings, 'space'), 'space');

    expect(feed.mode).toBe('search');
    expect(feed.entries).toEqual(expect.arrayContaining([
      expect.objectContaining({ kind: 'plugin', key: 'com.space-data-network.orbital-demo@1.2.3' }),
      expect.objectContaining({ kind: 'author', key: 'space ops' }),
    ]));
  });
});
