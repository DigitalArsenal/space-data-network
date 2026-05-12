import { describe, expect, it } from 'vitest';
import { type FlatSqlSyncChunk, type FlatSqlSyncManifest, type FlatSqlSyncQuery } from '../../flatsql-sync';
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
        async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
          return manifestFor(query.schema);
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
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
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

  it('lets workers fetch a direct read_chunk through the cached libp2p client', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        calls.push(query);
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
    }));

    await expect(cache.readFlatSqlSyncChunk(remoteConfig(), {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_chunk',
      limit: 50_000,
      offset: 10_000,
    })).resolves.toMatchObject({
      header: {
        schema: 'OMM.fbs',
      },
    });

    expect(calls).toEqual([
      expect.objectContaining({
        targetPeerId: '16Uiu2HCelesTrak',
        candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
        schema: 'OMM.fbs',
        op: 'read_chunk',
        limit: 50_000,
        offset: 10_000,
      }),
    ]);
  });

  it('lets workers open a published shard manifest through the cached libp2p client', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        calls.push(query);
        return manifestFor(query.schema);
      },
    }));

    await expect(cache.openFlatSqlSyncManifest(remoteConfig(), {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'open_manifest',
      queryProfile: 'dataset-publication-offset-v1',
      limit: 50_000,
    })).resolves.toMatchObject({
      schema: 'OMM.fbs',
      segments: [expect.objectContaining({ cid: 'bafkshard', indexCid: 'bafkindex' })],
    });

    expect(calls).toEqual([
      expect.objectContaining({
        targetPeerId: '16Uiu2HCelesTrak',
        candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
        schema: 'OMM.fbs',
        op: 'open_manifest',
        queryProfile: 'dataset-publication-offset-v1',
        limit: 50_000,
      }),
    ]);
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

function manifestFor(schema: string): FlatSqlSyncManifest {
  return {
    manifestId: 'manifest-1',
    schema,
    totalCount: 50_000,
    totalBytes: 8_000_000,
    snapshotId: 'snapshot-1',
    head: 'snapshot-1',
    highWaterMark: '1:2:3:50000',
    queryProfile: 'dataset-publication-offset-v1',
    syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
    maxChunkSize: 50_000,
    transports: ['libp2p-websocket', 'libp2p-webrtc'],
    segments: [{
      index: 0,
      cursor: 'MA',
      nextCursor: '',
      rowCount: 50_000,
      byteCount: 8_000_000,
      chunkHash: 'result-sha',
      cid: 'bafkshard',
      indexCid: 'bafkindex',
      manifestCid: 'bafkmanifest',
      shardSha256: 'shard-sha',
    }],
  };
}
