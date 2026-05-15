import { describe, expect, it } from 'vitest';
import {
  type FlatSqlPublishedShardBatch,
  type FlatSqlPublishedShard,
  type FlatSqlSyncChunk,
  type FlatSqlSyncManifest,
  type FlatSqlSyncQuery,
  type FlatSqlWireSpeedProbeQuery,
  type FlatSqlWireSpeedProbeResult,
} from '../../flatsql-sync';
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

  it('lets workers stream published shard bytes through the cached libp2p client', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const shardBytes = new Uint8Array([3, 0, 0, 0, 1, 2, 3]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push(query);
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteCount: shardBytes.byteLength,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes: shardBytes,
        };
      },
    }));

    await expect(cache.readFlatSqlPublishedShard(remoteConfig(), {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkshard',
    })).resolves.toMatchObject({
      header: { cid: 'bafkshard', byteCount: shardBytes.byteLength },
      streamBytes: shardBytes,
    });

    expect(calls).toEqual([
      expect.objectContaining({
        targetPeerId: '16Uiu2HCelesTrak',
        candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
        schema: 'OMM.fbs',
        op: 'read_published_shard',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        queryProfile: 'dataset-publication-offset-v1',
        cid: 'bafkshard',
      }),
    ]);
  });

  it('passes published shard byte ranges through the cached libp2p client', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const shardBytes = new Uint8Array([1, 2, 3, 4]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push(query);
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteOffset: query.byteOffset ?? 0,
            byteLength: query.byteLength ?? shardBytes.byteLength,
            byteCount: shardBytes.byteLength,
            totalByteCount: 128,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes: shardBytes,
        };
      },
    }));

    await expect(cache.readFlatSqlPublishedShard(remoteConfig(), {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkrangedshard',
      byteOffset: 64,
      byteLength: 4,
    })).resolves.toMatchObject({
      header: {
        cid: 'bafkrangedshard',
        byteOffset: 64,
        byteLength: 4,
        totalByteCount: 128,
      },
    });

    expect(calls).toEqual([
      expect.objectContaining({
        targetPeerId: '16Uiu2HCelesTrak',
        schema: 'OMM.fbs',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        cid: 'bafkrangedshard',
        byteOffset: 64,
        byteLength: 4,
      }),
    ]);
  });

  it('can prefer a mirror source for published shard ranges', async () => {
    const calls: Array<{ peer: string; query: FlatSqlSyncQuery & { cid: string } }> = [];
    const shardBytes = new Uint8Array([1, 2, 3, 4]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async (options) => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push({ peer: options.targetPeerId, query });
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteOffset: query.byteOffset ?? 0,
            byteLength: query.byteLength ?? shardBytes.byteLength,
            byteCount: shardBytes.byteLength,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes: shardBytes,
        };
      },
    }));

    await expect(cache.readFlatSqlPublishedShardFromSources([
      mirrorConfig(),
      remoteConfig(),
    ], {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkrangedshard',
      byteOffset: 64,
      byteLength: 4,
    }, 0)).resolves.toMatchObject({
      header: {
        cid: 'bafkrangedshard',
        byteOffset: 64,
        byteLength: 4,
      },
    });

    expect(calls).toEqual([
      {
        peer: '16Uiu2HMirror',
        query: expect.objectContaining({
          targetPeerId: '16Uiu2HMirror',
          candidateAddrs: ['/dns4/mirror.spaceaware.io/tcp/443/wss/p2p/16Uiu2HMirror'],
          schema: 'OMM.fbs',
          providerId: 'space-data-network-02',
          sourceName: 'celestrak-gp',
          cid: 'bafkrangedshard',
          byteOffset: 64,
          byteLength: 4,
        }),
      },
    ]);
  });

  it('uses the preferred published shard source before throughput stats exist', async () => {
    const calls: string[] = [];
    const shardBytes = new Uint8Array([1, 2, 3, 4]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async (options) => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push(options.targetPeerId);
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteCount: shardBytes.byteLength,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes: shardBytes,
        };
      },
    }));

    await cache.readFlatSqlPublishedShardFromSources([
      mirrorConfig(),
      remoteConfig(),
    ], {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkordered-first-wave',
    }, 1);

    expect(calls).toEqual(['16Uiu2HCelesTrak']);
  });

  it('probes unmeasured configured sources before permanently favoring the first measured source', async () => {
    const calls: string[] = [];
    const shardBytes = new Uint8Array([1, 2, 3, 4]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async (options) => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push(options.targetPeerId);
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteCount: shardBytes.byteLength,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes: shardBytes,
        };
      },
    }));
    const sources = [mirrorConfig(), remoteConfig()];

    await cache.readFlatSqlPublishedShardFromSources(sources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkprime-mirror',
    }, 0);
    calls.length = 0;

    await cache.readFlatSqlPublishedShardFromSources(sources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkknown-source',
    }, 1);

    expect(calls).toEqual(['16Uiu2HCelesTrak']);
  });

  it('limits concurrent cold-start probing to one active request per unmeasured source', async () => {
    const calls: string[] = [];
    const releaseReads: Array<() => void> = [];
    const shardBytes = new Uint8Array([1, 2, 3, 4]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async (options) => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push(options.targetPeerId);
        await new Promise<void>((resolve) => releaseReads.push(resolve));
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteCount: shardBytes.byteLength,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes: shardBytes,
        };
      },
    }));
    const sources = [mirrorConfig(), remoteConfig()];

    const reads = Promise.all(Array.from({ length: 4 }, (_value, index) => cache.readFlatSqlPublishedShardFromSources(sources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: `bafkcold-${index}`,
    }, index)));
    await withTimeout(waitForCallCount(calls, 4), 100, 'cold source calls did not start');

    expect(calls).toHaveLength(4);
    expect(calls.filter((peer) => peer === '16Uiu2HCelesTrak')).toHaveLength(1);
    expect(calls.filter((peer) => peer === '16Uiu2HMirror')).toHaveLength(3);

    for (const release of releaseReads) release();
    await reads;
  });

  it('falls back to the primary source when a preferred mirror lacks the shard', async () => {
    const calls: string[] = [];
    const shardBytes = new Uint8Array([5, 6, 7, 8]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async (options) => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push(options.targetPeerId);
        if (options.targetPeerId === '16Uiu2HMirror') {
          throw new Error('published shard was not found for bafkmissing');
        }
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteOffset: query.byteOffset ?? 0,
            byteLength: query.byteLength ?? shardBytes.byteLength,
            byteCount: shardBytes.byteLength,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes: shardBytes,
        };
      },
    }));

    await expect(cache.readFlatSqlPublishedShardFromSources([
      mirrorConfig(),
      remoteConfig(),
    ], {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkmissing',
      byteOffset: 0,
      byteLength: 4,
    }, 0)).resolves.toMatchObject({
      header: {
        cid: 'bafkmissing',
      },
      streamBytes: shardBytes,
    });

    expect(calls).toEqual(['16Uiu2HMirror', '16Uiu2HCelesTrak']);
  });

  it('routes later published shard reads away from a measured slow mirror', async () => {
    const calls: string[] = [];
    const shardBytes = new Uint8Array([9, 8, 7, 6]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async (options) => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push(options.targetPeerId);
        if (options.targetPeerId === '16Uiu2HMirror') await sleep(30);
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteCount: shardBytes.byteLength,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes: shardBytes,
        };
      },
    }));

    const mirrorFirstSources = [mirrorConfig(), remoteConfig()];
    const remoteFirstSources = [remoteConfig(), mirrorConfig()];
    await cache.readFlatSqlPublishedShardFromSources(mirrorFirstSources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkslow-mirror-first',
    }, 0);
    await cache.readFlatSqlPublishedShardFromSources([remoteConfig()], {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkprime-primary',
    }, 0);
    calls.length = 0;

    await cache.readFlatSqlPublishedShardFromSources(mirrorFirstSources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkslow-mirror-second',
    }, 0);

    expect(calls).toEqual(['16Uiu2HCelesTrak']);
  });

  it('spills concurrent published shard reads to a slower source when the fastest source is busy', async () => {
    const calls: string[] = [];
    const releasePrimaryReads: Array<() => void> = [];
    const primaryBytes = new Uint8Array(100);
    const mirrorBytes = new Uint8Array(100);
    const cache = new Libp2pFlatSqlSyncBackendCache(async (options) => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push(options.targetPeerId);
        const streamBytes = options.targetPeerId === '16Uiu2HMirror' ? mirrorBytes : primaryBytes;
        if (query.cid.startsWith('bafkparallel-') && options.targetPeerId === '16Uiu2HCelesTrak') {
          await new Promise<void>((resolve) => releasePrimaryReads.push(resolve));
        }
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteCount: streamBytes.byteLength,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes,
        };
      },
    }));
    const remoteFirstSources = [remoteConfig(), mirrorConfig()];
    const mirrorFirstSources = [mirrorConfig(), remoteConfig()];

    await cache.readFlatSqlPublishedShardFromSources(remoteFirstSources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkprime-primary',
    }, 0);
    await cache.readFlatSqlPublishedShardFromSources([mirrorConfig()], {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'bafkprime-mirror',
    }, 0);

    calls.length = 0;
    const reads = Promise.all(Array.from({ length: 4 }, (_value, index) => cache.readFlatSqlPublishedShardFromSources(remoteFirstSources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: `bafkparallel-${index}`,
    }, 0)));
    await Promise.resolve();

    expect(calls).toContain('16Uiu2HCelesTrak');
    expect(calls).toContain('16Uiu2HMirror');

    for (const release of releasePrimaryReads) release();
    await reads;
  });

  it('does not over-rotate moderate concurrent reads to a much slower source', async () => {
    const calls: string[] = [];
    const releaseLoadReads: Array<() => void> = [];
    const cache = new Libp2pFlatSqlSyncBackendCache(async (options) => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
        calls.push(options.targetPeerId);
        const streamBytes = options.targetPeerId === '16Uiu2HCelesTrak'
          ? new Uint8Array(300)
          : new Uint8Array(100);
        if (query.cid.startsWith('load-')) {
          await new Promise<void>((resolve) => releaseLoadReads.push(resolve));
        }
        return {
          header: {
            op: 'read_published_shard',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            cid: query.cid,
            rowCount: 1,
            byteCount: streamBytes.byteLength,
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
          },
          streamBytes,
        };
      },
    }));
    const sources = [remoteConfig(), mirrorConfig()];

    await cache.readFlatSqlPublishedShardFromSources(sources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'prime-primary',
    }, 0);
    await cache.readFlatSqlPublishedShardFromSources(sources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: 'prime-mirror',
    }, 1);

    calls.length = 0;
    const reads = Array.from({ length: 4 }, (_value, index) => cache.readFlatSqlPublishedShardFromSources(sources, {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: `load-${index}`,
    }, 0));
    await Promise.resolve();

    expect(calls).toEqual([
      '16Uiu2HCelesTrak',
      '16Uiu2HCelesTrak',
      '16Uiu2HCelesTrak',
      '16Uiu2HCelesTrak',
    ]);

    for (const release of releaseLoadReads) release();
    await Promise.all(reads);
  });

  it('lets workers stream published shard batches through the cached libp2p client', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const shardBytes = new Uint8Array([3, 0, 0, 0, 1, 2, 3]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async readFlatSqlPublishedShardBatch(query: FlatSqlSyncQuery & { cids: string[] }): Promise<FlatSqlPublishedShardBatch> {
        calls.push(query);
        return {
          header: {
            op: 'read_published_shard_batch',
            status: 'ok',
            schema: query.schema,
            queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
            syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
            transports: ['libp2p-websocket'],
            payloadFormat: 'concatenated-flatsql-size-prefixed-flatbuffers',
            shards: query.cids.map((cid) => ({
              op: 'read_published_shard',
              status: 'ok',
              schema: query.schema,
              queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
              cid,
              rowCount: 1,
              byteCount: shardBytes.byteLength,
              syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
              transports: ['libp2p-websocket'],
            })),
          },
          shards: query.cids.map((cid) => ({
            header: {
              op: 'read_published_shard',
              status: 'ok',
              schema: query.schema,
              queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
              cid,
              rowCount: 1,
              byteCount: shardBytes.byteLength,
              syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
              transports: ['libp2p-websocket'],
            },
            streamBytes: shardBytes,
          })),
        };
      },
    }));

    await expect(cache.readFlatSqlPublishedShardBatch(remoteConfig(), {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard_batch',
      queryProfile: 'dataset-publication-offset-v1',
      cids: ['bafkfirst', 'bafksecond'],
    })).resolves.toMatchObject({
      shards: [
        { header: { cid: 'bafkfirst', byteCount: shardBytes.byteLength } },
        { header: { cid: 'bafksecond', byteCount: shardBytes.byteLength } },
      ],
    });

    expect(calls).toEqual([
      expect.objectContaining({
        targetPeerId: '16Uiu2HCelesTrak',
        candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
        schema: 'OMM.fbs',
        op: 'read_published_shard_batch',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        queryProfile: 'dataset-publication-offset-v1',
        cids: ['bafkfirst', 'bafksecond'],
      }),
    ]);
  });

  it('releases the control client before opening eight dedicated published shard clients', async () => {
    const createdClients: Array<Libp2pFlatSqlSyncClient & { id: number; stop(): Promise<void> }> = [];
    const shardClientIds: number[] = [];
    const stoppedClientIds: number[] = [];
    const shardBytes = new Uint8Array([3, 0, 0, 0, 1, 2, 3]);
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => {
      const id = createdClients.length + 1;
      const client = {
        id,
        async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
          return headerOnlyChunk(query.schema);
        },
        async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
          return manifestFor(query.schema);
        },
        async readFlatSqlPublishedShard(query: FlatSqlSyncQuery & { cid: string }): Promise<FlatSqlPublishedShard> {
          shardClientIds.push(id);
          return {
            header: {
              op: 'read_published_shard',
              status: 'ok',
              schema: query.schema,
              queryProfile: query.queryProfile ?? 'dataset-publication-offset-v1',
              cid: query.cid,
              rowCount: 1,
              byteCount: shardBytes.byteLength,
              syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
              transports: ['libp2p-websocket'],
            },
            streamBytes: shardBytes,
          };
        },
        async stop() {
          stoppedClientIds.push(id);
        },
      };
      createdClients.push(client);
      return client;
    });

    await cache.openFlatSqlSyncManifest(remoteConfig(), {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'open_manifest',
      queryProfile: 'dataset-publication-offset-v1',
      limit: 50_000,
    });
    await cache.releaseControlClient(remoteConfig());

    await Promise.all(Array.from({ length: 8 }, (_, index) => cache.readFlatSqlPublishedShard(remoteConfig(), {
      targetPeerId: '',
      schema: 'OMM.fbs',
      op: 'read_published_shard',
      queryProfile: 'dataset-publication-offset-v1',
      cid: `bafkshard-${index}`,
    })));

    expect(stoppedClientIds).toEqual([1]);
    expect(createdClients).toHaveLength(9);
    expect(new Set(shardClientIds)).toEqual(new Set([2, 3, 4, 5, 6, 7, 8, 9]));
  });

  it('lets workers measure wire speed through the cached libp2p client', async () => {
    const calls: FlatSqlWireSpeedProbeQuery[] = [];
    const cache = new Libp2pFlatSqlSyncBackendCache(async () => ({
      async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
        return headerOnlyChunk(query.schema);
      },
      async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
        return manifestFor(query.schema);
      },
      async measureWireSpeed(query: FlatSqlWireSpeedProbeQuery): Promise<FlatSqlWireSpeedProbeResult> {
        calls.push(query);
        return {
          requestedBytes: query.probeBytes ?? 0,
          payloadBytes: query.probeBytes ?? 0,
          elapsedMs: 250,
          bytesPerSecond: 4096,
          syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
        };
      },
    }));

    await expect(cache.measureWireSpeed(remoteConfig(), { probeBytes: 1024 })).resolves.toMatchObject({
      requestedBytes: 1024,
      payloadBytes: 1024,
      bytesPerSecond: 4096,
    });

    expect(calls).toEqual([
      expect.objectContaining({
        targetPeerId: '16Uiu2HCelesTrak',
        candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
        probeBytes: 1024,
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

function mirrorConfig() {
  return {
    targetPeerId: '16Uiu2HMirror',
    candidateAddrs: ['/dns4/mirror.spaceaware.io/tcp/443/wss/p2p/16Uiu2HMirror'],
    providerId: 'space-data-network-02',
    sourceName: 'celestrak-gp',
  };
}

async function sleep(milliseconds: number): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitForCallCount(calls: unknown[], count: number): Promise<void> {
  while (calls.length < count) {
    await sleep(0);
  }
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
    recordStream: new Uint8Array(),
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
