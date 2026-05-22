import { describe, expect, it } from 'vitest';

import {
  canonicalizeDataDirectorySourceIds,
  DEFAULT_DATA_FEED_RETENTION_POLICY,
  DATA_SOURCE_OWNERTRUST,
  DEFAULT_OWNERTRUST,
  isTrustedDirectoryOwnertrust,
  migrateSchemaSyncPreferencesToDataDirectory,
  normalizeOwnertrust,
  ownertrustForDataSourceSubscription,
  subscriptionKey,
  upsertDataFeedSubscription,
  updateDataFeedSubscription,
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
      queryProfile: 'dataset-publication-offset-v1',
    });

    expect(next.peerTrust['16Uiu2HAmCelestrak']).toBe('marginal');
    expect(next.subscriptions).toHaveLength(1);
    expect(next.subscriptions[0]).toMatchObject({
      id: subscriptionKey('configured:celestrak.eth', 'OMM'),
      peerId: '16Uiu2HAmCelestrak',
      standardId: 'OMM',
      syncFilter: 'EPOCH BETWEEN 2026-05-01 AND 2026-05-12',
      queryProfile: 'dataset-publication-offset-v1',
    });
  });

  it('stores the sync query profile on the subscription', () => {
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
      syncFilter: '',
      queryProfile: 'dataset-publication-offset-v1',
    });

    expect(next.subscriptions[0]).toMatchObject({
      queryProfile: 'dataset-publication-offset-v1',
    });
  });

  it('defaults CAT feeds to replace snapshots while other feeds append history', () => {
    const cat = upsertDataFeedSubscription({ peerTrust: {}, subscriptions: [] }, {
      dataSourceId: 'configured:celestrak.eth',
      peerId: '16Uiu2HAmCelestrak',
      datastoreKey: 'sdn-ds-v1-cat',
      standardId: 'CAT',
      providerName: 'CelesTrak SATCAT',
      providerPublicKey: 'ed25519:abc',
      remoteRows: 145902,
      storageCap: 1,
      storageUnit: 'GB',
      syncFilter: '',
    });
    const omm = upsertDataFeedSubscription({ peerTrust: {}, subscriptions: [] }, {
      dataSourceId: 'configured:celestrak.eth',
      peerId: '16Uiu2HAmCelestrak',
      datastoreKey: 'sdn-ds-v1-omm',
      standardId: 'OMM',
      providerName: 'CelesTrak OMM',
      providerPublicKey: 'ed25519:abc',
      remoteRows: 2000000,
      storageCap: 1,
      storageUnit: 'GB',
      syncFilter: '',
    });

    expect(DEFAULT_DATA_FEED_RETENTION_POLICY).toBe('append-only');
    expect(cat.subscriptions[0]).toMatchObject({ retentionPolicy: 'replace-snapshot' });
    expect(omm.subscriptions[0]).toMatchObject({ retentionPolicy: 'append-only' });
  });

  it('stores and updates explicit feed retention policy', () => {
    const initial = upsertDataFeedSubscription({ peerTrust: {}, subscriptions: [] }, {
      dataSourceId: 'configured:celestrak.eth',
      peerId: '16Uiu2HAmCelestrak',
      datastoreKey: 'sdn-ds-v1-cat',
      standardId: 'CAT',
      providerName: 'CelesTrak SATCAT',
      providerPublicKey: 'ed25519:abc',
      remoteRows: 145902,
      storageCap: 1,
      storageUnit: 'GB',
      syncFilter: '',
      retentionPolicy: 'append-only',
    });

    expect(initial.subscriptions[0]).toMatchObject({ retentionPolicy: 'append-only' });

    const updated = updateDataFeedSubscription(initial, initial.subscriptions[0].id, {
      retentionPolicy: 'replace-snapshot',
    });

    expect(updated.subscriptions[0]).toMatchObject({ retentionPolicy: 'replace-snapshot' });
  });

  it('preserves the SDN datastore namespace key on feed subscriptions', () => {
    const next = upsertDataFeedSubscription({ peerTrust: {}, subscriptions: [] }, {
      dataSourceId: 'configured:celestrak.eth',
      peerId: '16Uiu2HAmCelestrak',
      datastoreKey: 'sdn-ds-v1-history',
      standardId: 'OMM',
      providerName: 'CelesTrak',
      providerPublicKey: 'ed25519:abc',
      remoteRows: 44349135,
      storageCap: 2,
      storageUnit: 'TB',
      syncFilter: '',
    });

    expect(next.subscriptions[0]).toMatchObject({
      id: subscriptionKey('configured:celestrak.eth', 'OMM', 'sdn-ds-v1-history'),
      datastoreKey: 'sdn-ds-v1-history',
      remoteRows: 44349135,
    });
  });

  it('preserves provider and source identity on feed subscriptions', () => {
    const next = upsertDataFeedSubscription({ peerTrust: {}, subscriptions: [] }, {
      dataSourceId: 'configured:celestrak.eth',
      peerId: '16Uiu2HAmCelestrak',
      datastoreKey: 'sdn-ds-v1-cat',
      standardId: 'CAT',
      providerName: 'CelesTrak SATCAT',
      providerPublicKey: 'ed25519:abc',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-satcat-csv',
      remoteRows: 145902,
      storageCap: 1,
      storageUnit: 'GB',
      syncFilter: '',
    });

    expect(next.subscriptions[0]).toMatchObject({
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-satcat-csv',
    });
  });

  it('drops invalid CelesTrak source identities for the selected schema', () => {
    const next = upsertDataFeedSubscription({ peerTrust: {}, subscriptions: [] }, {
      dataSourceId: 'configured:space-data-network-02',
      peerId: '16Uiu2HAmCelestrak',
      standardId: 'CAT',
      providerName: 'CelesTrak CAT',
      providerPublicKey: 'ed25519:abc',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      remoteRows: 435257,
      storageCap: 1,
      storageUnit: 'GB',
      syncFilter: '',
    });

    expect(next.subscriptions[0]).toMatchObject({
      providerId: 'space-data-network-02',
      standardId: 'CAT',
      sourceName: null,
    });
  });

  it('does not treat the standard name as a provider source filter', () => {
    const next = upsertDataFeedSubscription({ peerTrust: {}, subscriptions: [] }, {
      dataSourceId: 'configured:space-data-network-02',
      peerId: '16Uiu2HAmCelestrak',
      standardId: 'OMM',
      providerName: 'CelesTrak OMM',
      providerPublicKey: 'ed25519:abc',
      providerId: 'space-data-network-02',
      sourceName: 'OMM',
      remoteRows: 2409549,
      storageCap: 10,
      storageUnit: 'GB',
      syncFilter: '',
    });

    expect(next.subscriptions[0]).toMatchObject({
      standardId: 'OMM',
      sourceName: null,
    });
  });

  it('keeps multiple datastore namespaces for the same source and schema as distinct subscriptions', () => {
    let state = upsertDataFeedSubscription({ peerTrust: {}, subscriptions: [] }, {
      dataSourceId: 'configured:celestrak.eth',
      peerId: '16Uiu2HAmCelestrak',
      datastoreKey: 'sdn-ds-v1-live',
      standardId: 'OMM',
      providerName: 'CelesTrak live',
      providerPublicKey: 'ed25519:abc',
      remoteRows: 2287018,
      storageCap: 1,
      storageUnit: 'GB',
      syncFilter: '',
    });

    state = upsertDataFeedSubscription(state, {
      dataSourceId: 'configured:celestrak.eth',
      peerId: '16Uiu2HAmCelestrak',
      datastoreKey: 'sdn-ds-v1-history',
      standardId: 'OMM',
      providerName: 'CelesTrak historical',
      providerPublicKey: 'ed25519:abc',
      remoteRows: 44349135,
      storageCap: 5,
      storageUnit: 'GB',
      syncFilter: '',
    });

    expect(state.subscriptions).toHaveLength(2);
    expect(state.subscriptions.map((subscription) => subscription.id).sort()).toEqual([
      subscriptionKey('configured:celestrak.eth', 'OMM', 'sdn-ds-v1-history'),
      subscriptionKey('configured:celestrak.eth', 'OMM', 'sdn-ds-v1-live'),
    ]);
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

  it('canonicalizes old configured node source IDs to the current configured source ID', () => {
    const next = canonicalizeDataDirectorySourceIds({
      peerTrust: {
        '16Uiu2HAmCelestrak': 'marginal',
      },
      subscriptions: [{
        id: 'space-data-network-02:OMM',
        dataSourceId: 'space-data-network-02',
        peerId: '16Uiu2HAmCelestrak',
        datastoreKey: null,
        standardId: 'OMM',
        providerName: 'CelesTrak Provider',
        providerId: 'space-data-network-02',
        providerPublicKey: 'ed25519:celestrak',
        sourceName: 'OMM',
        remoteRows: 2409549,
        storageCap: 10,
        storageUnit: 'GB',
        syncFilter: '',
        queryProfile: 'dataset-publication-offset-v1',
        createdAt: '2026-05-14T00:00:00.000Z',
        updatedAt: '2026-05-14T00:00:00.000Z',
      }],
    }, [{
      dataSourceId: 'configured:space-data-network-02',
      legacyDataSourceIds: ['space-data-network-02'],
      peerId: '16Uiu2HAmCelestrak',
      providerName: 'CelesTrak Provider',
      providerPublicKey: 'ed25519:celestrak',
    }]);

    expect(next.subscriptions).toHaveLength(1);
    expect(next.subscriptions[0]).toMatchObject({
      id: subscriptionKey('configured:space-data-network-02', 'OMM'),
      dataSourceId: 'configured:space-data-network-02',
      peerId: '16Uiu2HAmCelestrak',
      remoteRows: 2409549,
    });
  });
});
