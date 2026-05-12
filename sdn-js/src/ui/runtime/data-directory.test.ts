import { describe, expect, it } from 'vitest';

import {
  DATA_SOURCE_OWNERTRUST,
  DEFAULT_OWNERTRUST,
  isTrustedDirectoryOwnertrust,
  migrateSchemaSyncPreferencesToDataDirectory,
  normalizeOwnertrust,
  ownertrustForDataSourceSubscription,
  subscriptionKey,
  upsertDataFeedSubscription,
  updatePeerOwnertrust,
  type DataDirectoryState,
} from './data-directory';

describe('PGP data directory ownertrust', () => {
  it('defaults observed peers to unknown ownertrust', () => {
    expect(DEFAULT_OWNERTRUST).toBe('unknown');
    expect(normalizeOwnertrust(undefined)).toBe('unknown');
    expect(isTrustedDirectoryOwnertrust('unknown')).toBe(false);
    expect(isTrustedDirectoryOwnertrust('never')).toBe(false);
  });

  it('uses marginal ownertrust as the lowest usable trust when subscribing to a data source', () => {
    expect(DATA_SOURCE_OWNERTRUST).toBe('marginal');
    expect(ownertrustForDataSourceSubscription('unknown')).toBe('marginal');
    expect(ownertrustForDataSourceSubscription('never')).toBe('marginal');
    expect(ownertrustForDataSourceSubscription('full')).toBe('full');
    expect(isTrustedDirectoryOwnertrust('marginal')).toBe(true);
  });

  it('promotes a peer to marginal ownertrust when a feed subscription is added', () => {
    const initial: DataDirectoryState = { peerTrust: {}, subscriptions: [] };
    const next = upsertDataFeedSubscription(initial, {
      dataSourceId: 'configured:celestrak.eth',
      peerId: '16Uiu2HAmCelestrak',
      standardId: 'OMM',
      providerName: 'CelesTrak',
      providerPublicKey: 'ed25519:abc',
      remoteRows: 2000000,
      storageCap: 1,
      storageUnit: 'GB',
      syncFilter: 'EPOCH BETWEEN 2026-05-01 AND 2026-05-12',
    });

    expect(next.peerTrust['16Uiu2HAmCelestrak']).toBe('marginal');
    expect(next.subscriptions).toHaveLength(1);
    expect(next.subscriptions[0]).toMatchObject({
      id: subscriptionKey('configured:celestrak.eth', 'OMM'),
      peerId: '16Uiu2HAmCelestrak',
      standardId: 'OMM',
      syncFilter: 'EPOCH BETWEEN 2026-05-01 AND 2026-05-12',
    });
  });

  it('keeps explicit full or ultimate trust when subscribing to a feed', () => {
    const state = updatePeerOwnertrust({ peerTrust: {}, subscriptions: [] }, '16Uiu2HAmProvider', 'full');
    const next = upsertDataFeedSubscription(state, {
      dataSourceId: 'configured:provider',
      peerId: '16Uiu2HAmProvider',
      standardId: 'PNM',
      providerName: 'Provider',
      providerPublicKey: null,
      remoteRows: 10,
      storageCap: 512,
      storageUnit: 'MB',
      syncFilter: '',
    });

    expect(next.peerTrust['16Uiu2HAmProvider']).toBe('full');
  });

  it('migrates legacy schema sync preferences into marginal data-directory subscriptions', () => {
    const next = migrateSchemaSyncPreferencesToDataDirectory({
      peerTrust: {},
      subscriptions: [],
    }, {
      'configured:celestrak:OMM': { mode: 'sync', storageCap: 2, storageUnit: 'GB' },
      'configured:celestrak:PNM': { mode: 'preview', storageCap: 1, storageUnit: 'GB' },
    }, [{
      dataSourceId: 'configured:celestrak',
      peerId: '16Uiu2HAmCelestrak',
      providerName: 'CelesTrak',
      providerPublicKey: 'ed25519:celestrak',
      remoteRowsByStandard: { OMM: 2005702 },
    }], {
      'configured:celestrak:OMM': { totalRows: 1999559 },
    });

    expect(next.peerTrust['16Uiu2HAmCelestrak']).toBe('marginal');
    expect(next.subscriptions).toHaveLength(1);
    expect(next.subscriptions[0]).toMatchObject({
      id: 'configured:celestrak:OMM',
      standardId: 'OMM',
      remoteRows: 2005702,
      storageCap: 2,
      storageUnit: 'GB',
    });
  });
});
