import { describe, expect, it } from 'vitest';
import { buildPeerDataFeeds, dataSummaryListingsForSource } from './peer-data-feeds';
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
      standardId: 'OMM',
      remoteRows: 1_999_559,
      syncAddrs: ['/dns4/celestrak.eth/tcp/443/wss/p2p/16Uiu2HCelesTrak'],
    })]);
  });
});
