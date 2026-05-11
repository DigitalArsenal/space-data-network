import { describe, expect, it } from 'vitest';
import { type FlatSqlSyncChunk, type FlatSqlSyncQuery } from '../../flatsql-sync';
import { withTimeout } from './async-timeout';
import { Libp2pFlatSqlSyncBackendCache } from './libp2p-sync-backend-cache';
import type { Libp2pFlatSqlSyncClient } from './sdn-backend-libp2p-sync';

describe('libp2p FlatSQL sync backend cache', () => {
  it('reuses one libp2p sync client across backend instances for the same remote peer', async () => {
    const createdClients: Array<Libp2pFlatSqlSyncClient & { stop(): Promise<void> }> = [];
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => {
      const client = {
        async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
          return headerOnlyChunk(query.schema);
        },
        async stop() {},
      };
      createdClients.push(client);
      return client;
    });

    const first = cache.backendFor(remoteConfig());
    const second = cache.backendFor(remoteConfig());

    await expect(first.scanRawData({ schema: 'OMM.fbs', limit: 1, offset: 0 })).resolves.toMatchObject({ ok: true });
    await expect(second.scanRawData({ schema: 'OMM.fbs', limit: 1, offset: 0 })).resolves.toMatchObject({ ok: true });

    expect(createdClients).toHaveLength(1);
  });

  it('stops cached libp2p clients when destroyed', async () => {
    let stopCount = 0;
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async stop() {
        stopCount += 1;
      },
    }));

    await cache.backendFor(remoteConfig()).scanRawData({ schema: 'OMM.fbs', limit: 1, offset: 0 });
    await cache.backendFor({ ...remoteConfig(), targetPeerId: '16Uiu2HOther' }).scanRawData({ schema: 'OMM.fbs', limit: 1, offset: 0 });
    await cache.destroy();
    await cache.destroy();

    expect(stopCount).toBe(2);
  });

  it('does not hang destroy when a libp2p client is still being created', async () => {
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => new Promise(() => undefined));

    void cache.backendFor(remoteConfig()).scanRawData({ schema: 'OMM.fbs', limit: 1, offset: 0 });
    await Promise.resolve();

    await expect(withTimeout(cache.destroy(), 100, 'cache destroy timed out')).resolves.toBeUndefined();
  });
});

function remoteConfig() {
  return {
    targetPeerId: '16Uiu2HCelesTrak',
    candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
    providerId: 'space-data-network-02',
    sourceName: 'celestrak-gp',
  };
}

function headerOnlyChunk(schema: string): FlatSqlSyncChunk {
  return {
    header: {
      schema,
      totalCount: 1,
      count: 0,
      limit: 1,
      offset: 0,
      cursor: '',
      nextCursor: '',
      snapshotId: 'snapshot-1',
      head: 'snapshot-1',
      highWaterMark: 'snapshot-1:1',
      scanHash: 'scan-1',
      chunkHash: 'chunk-1',
      queryProfile: 'ordered-offset-v1',
      syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
      maxChunkSize: 25_000,
      transports: ['libp2p-websocket'],
      results: [],
    },
    records: [],
  };
}
