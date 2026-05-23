import { describe, expect, it } from 'vitest';
import { buildPeerDataFeeds, dataSummaryListingsForSource, preferredDataSummarySource } from './peer-data-feeds';
import type { DataDirectoryState } from './data-directory';
import type { DataSummary } from './sdn-backend';

describe('peer data feeds', () => {
  const source = {
    id: 'configured:space-data-network-02',
    label: 'CelesTrak Provider',
    peerId: '16Uiu2HCelesTrak',
    publicKey: 'celestrak-public-key',
    syncAddrs: ['/dns4/celestrak.eth/tcp/443/wss/p2p/16Uiu2HCelesTrak'],
  };

  const emptyDirectory: DataDirectoryState = { peerTrust: {}, subscriptions: [] };

  it('turns discovered datastore namespaces into subscribe-ready peer feed rows', () => {
    const summary: DataSummary = {
      totalRecords: 1_999_559,
      totalBytes: 668_819_776,
      schemas: [{ schemaName: 'OMM.fbs', count: 1_999_559, totalBytes: 668_819_776 }],
      sources: [{
        datastoreKey: 'sdn-ds-v1-celestrak-omm',
        schemaName: 'OMM.fbs',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'plhd-2026-05-13',
        producerPeerId: '16Uiu2HCelesTrak',
        producerPublicKey: 'producer-public-key',
        count: 1_999_559,
        totalBytes: 668_819_776,
      }],
    };

    const listings = dataSummaryListingsForSource(source, summary);
    const feeds = buildPeerDataFeeds([source], listings, emptyDirectory);

    expect(feeds).toEqual([expect.objectContaining({
      dataSourceId: 'configured:space-data-network-02',
      peerId: '16Uiu2HCelesTrak',
      datastoreKey: 'sdn-ds-v1-celestrak-omm',
      providerName: 'CelesTrak Provider',
      providerPublicKey: 'producer-public-key',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      standardId: 'OMM',
      remoteRows: 1_999_559,
      syncAddrs: ['/dns4/celestrak.eth/tcp/443/wss/p2p/16Uiu2HCelesTrak'],
    })]);
  });

  it('prefers the active CelesTrak SATCAT CSV source when multiple CAT sources are advertised without datastore keys', () => {
    const summary: DataSummary = {
      totalRecords: 1_506_117,
      totalBytes: 123_456,
      schemas: [{ schemaName: 'CAT.fbs', count: 1_506_117, totalBytes: 123_456 }],
      sources: [
        {
          schemaName: 'CAT.fbs',
          providerId: 'space-data-network-02',
          sourceName: '',
          batchId: '',
          producerPeerId: '16Uiu2HCelesTrak',
          producerPublicKey: 'producer-public-key',
          count: 972_737,
          totalBytes: 219_942_748,
        },
        {
          schemaName: 'CAT.fbs',
          providerId: 'space-data-network-02',
          sourceName: 'celestrak-satcat-csv',
          batchId: 'csv',
          producerPeerId: '16Uiu2HCelesTrak',
          producerPublicKey: 'producer-public-key',
          count: 98_123,
          totalBytes: 10_000,
        },
        {
          schemaName: 'CAT.fbs',
          providerId: 'space-data-network-02',
          sourceName: 'celestrak-satcat',
          batchId: 'legacy',
          producerPeerId: '16Uiu2HCelesTrak',
          producerPublicKey: 'producer-public-key',
          count: 435_257,
          totalBytes: 113_456,
        },
      ],
    };

    const listings = dataSummaryListingsForSource(source, summary);
    const feeds = buildPeerDataFeeds([source], listings, emptyDirectory);
    const cat = feeds.find((feed) => feed.standardId === 'CAT');

    expect(cat).toMatchObject({
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-satcat-csv',
      remoteRows: 98_123,
    });
  });

  it('uses the active CelesTrak source when a stale source filter is stored for a schema', () => {
    const sourceRow = preferredDataSummarySource([
      {
        schemaName: 'CAT.fbs',
        providerId: 'space-data-network-02',
        sourceName: '',
        batchId: '',
        producerPeerId: '16Uiu2HCelesTrak',
        producerPublicKey: 'producer-public-key',
        count: 972_737,
        totalBytes: 219_942_748,
      },
      {
        schemaName: 'CAT.fbs',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-satcat-csv',
        batchId: 'csv',
        producerPeerId: '16Uiu2HCelesTrak',
        producerPublicKey: 'producer-public-key',
        count: 98_123,
        totalBytes: 10_000,
      },
      {
        schemaName: 'CAT.fbs',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-satcat',
        batchId: 'legacy',
        producerPeerId: '16Uiu2HCelesTrak',
        producerPublicKey: 'producer-public-key',
        count: 435_257,
        totalBytes: 113_456,
      },
    ], {
      standardId: 'CAT',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
    });

    expect(sourceRow).toMatchObject({
      sourceName: 'celestrak-satcat-csv',
      count: 98_123,
    });
  });
});
