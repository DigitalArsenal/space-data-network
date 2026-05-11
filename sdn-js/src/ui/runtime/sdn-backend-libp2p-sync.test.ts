import { describe, expect, it } from 'vitest';
import { FLATSQL_SYNC_PROTOCOL_ID, type FlatSqlSyncChunk, type FlatSqlSyncQuery } from '../../flatsql-sync';
import { createLibp2pFlatSqlSyncBackend } from './sdn-backend-libp2p-sync';

describe('libp2p FlatSQL sync backend', () => {
  it('builds data summaries by scanning configured schemas over libp2p sync', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      syncClient: {
        async readFlatSqlSyncChunk(query) {
          calls.push(query);
          return headerOnlyChunk(query.schema, query.schema === 'OMM.fbs' ? 1_999_559 : 0);
        },
      },
    });

    const result = await backend.getDataSummary();

    expect(result).toMatchObject({
      ok: true,
      data: {
        totalRecords: 1_999_559,
        schemas: [{ schemaName: 'OMM.fbs', count: 1_999_559 }],
        sources: [{
          schemaName: 'OMM.fbs',
          providerId: 'space-data-network-02',
          sourceName: 'celestrak-gp',
          count: 1_999_559,
        }],
      },
    });
    expect(calls[0]).toMatchObject({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      op: 'scan',
      schema: 'CAT.fbs',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      limit: 1,
      offset: 0,
    });
  });

  it('keeps summary loading usable when one schema is absent on the provider', async () => {
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      providerId: 'space-data-network-02',
      schemas: ['OMM.fbs', 'PNM.fbs'],
      syncClient: {
        async readFlatSqlSyncChunk(query) {
          if (query.schema === 'PNM.fbs') throw new Error('no flatsql schema found for PNM');
          return headerOnlyChunk(query.schema, 1_999_559);
        },
      },
    });

    await expect(backend.getDataSummary()).resolves.toMatchObject({
      ok: true,
      data: {
        totalRecords: 1_999_559,
        schemas: [{ schemaName: 'OMM.fbs', count: 1_999_559 }],
      },
    });
  });

  it('fails visibly when libp2p sync cannot connect', async () => {
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      schemas: ['OMM.fbs'],
      syncClient: {
        async readFlatSqlSyncChunk() {
          throw new Error('failed to dial /space-data-network/flatsql-sync/1.0.0');
        },
      },
    });

    await expect(backend.getDataSummary()).resolves.toMatchObject({
      ok: false,
      capability: {
        id: 'getDataSummary',
        state: 'unavailable',
        reason: 'failed to dial /space-data-network/flatsql-sync/1.0.0',
      },
    });
  });

  it('scans and streams raw FlatBuffers through the sync protocol without HTTP', async () => {
    const rawRecord = new Uint8Array([9, 8, 7, 6]);
    const calls: FlatSqlSyncQuery[] = [];
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      providerId: 'space-data-network-02',
      syncClient: {
        async readFlatSqlSyncChunk(query) {
          calls.push(query);
          if (query.op === 'read_chunk') {
            return {
              header: headerOnlyChunk('OMM.fbs', 1, {
                count: 1,
                results: [recordRef()],
                chunkHash: 'chunk-hash',
                scanHash: 'scan-hash',
              }).header,
              records: [rawRecord],
            };
          }
          return {
            header: headerOnlyChunk('OMM.fbs', 1, {
              count: 1,
              results: [recordRef()],
              chunkHash: 'chunk-hash',
              scanHash: 'scan-hash',
              nextCursor: 'cursor-2',
            }).header,
            records: [],
          };
        },
      },
    });

    const scan = await backend.scanRawData({ schema: 'OMM.fbs', limit: 1, offset: 0 });
    expect(scan).toMatchObject({
      ok: true,
      data: {
        schema: 'OMM.fbs',
        totalCount: 1,
        scanHash: 'scan-hash',
        chunkHash: 'chunk-hash',
        results: [{ cid: 'omm-cid-1', providerId: 'space-data-network-02' }],
      },
    });

    const stream = await backend.streamRawData({
      schema: 'OMM.fbs',
      scanHash: 'scan-hash',
      chunkHash: 'chunk-hash',
      records: scan.data?.results ?? [],
    });

    expect(stream).toMatchObject({
      ok: true,
      data: [{ cid: 'omm-cid-1', dataBytes: rawRecord }],
    });
    expect(calls).toEqual([
      expect.objectContaining({
        op: 'scan',
        targetPeerId: '16Uiu2HCelesTrak',
        candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
        schema: 'OMM.fbs',
        providerId: 'space-data-network-02',
      }),
      expect.objectContaining({
        op: 'read_chunk',
        targetPeerId: '16Uiu2HCelesTrak',
        candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
        schema: 'OMM.fbs',
        scanHash: 'scan-hash',
        chunkHash: 'chunk-hash',
        records: [expect.objectContaining({ cid: 'omm-cid-1' })],
      }),
    ]);
  });
});

function headerOnlyChunk(schema: string, totalCount: number, patch: Partial<FlatSqlSyncChunk['header']> = {}): FlatSqlSyncChunk {
  return {
    header: {
      schema,
      totalCount,
      count: 0,
      limit: 1,
      offset: 0,
      cursor: 'MA',
      nextCursor: '',
      snapshotId: 'snapshot-1',
      head: 'snapshot-1',
      highWaterMark: `snapshot-1:${totalCount}`,
      scanHash: `${schema}:scan`,
      chunkHash: `${schema}:chunk`,
      queryProfile: 'ordered-offset-v1',
      syncProtocol: FLATSQL_SYNC_PROTOCOL_ID,
      maxChunkSize: 25_000,
      transports: ['libp2p-websocket', 'libp2p-webrtc'],
      results: [],
      ...patch,
    },
    records: [],
  };
}

function recordRef() {
  return {
    schemaName: 'OMM.fbs',
    cid: 'omm-cid-1',
    peerId: 'source:celestrak',
    providerId: 'space-data-network-02',
    sourceName: 'celestrak-gp',
    sizeBytes: 4,
  };
}
