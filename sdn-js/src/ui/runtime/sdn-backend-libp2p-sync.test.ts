import { describe, expect, it } from 'vitest';
import { FLATSQL_SYNC_PROTOCOL_ID, type FlatSqlSyncChunk, type FlatSqlSyncQuery } from '../../flatsql-sync';
import {
  LIBP2P_FLATSQL_SYNC_YAMUX_OPTIONS,
  createLibp2pFlatSqlSyncBackend,
  exchangeFlatSqlSyncStream,
  isLibp2pFlatSqlSyncAddrDialable,
  orderLibp2pFlatSqlSyncDialAddrs,
  selectLibp2pFlatSqlSyncTransports,
} from './sdn-backend-libp2p-sync';

describe('libp2p FlatSQL sync backend', () => {
  it('advertises a high-throughput yamux receive window for bulk FlatSQL shard streams', () => {
    expect(LIBP2P_FLATSQL_SYNC_YAMUX_OPTIONS).toMatchObject({
      initialStreamWindowSize: 16 * 1024 * 1024,
      maxStreamWindowSize: 128 * 1024 * 1024,
    });
  });

  it('raises the outbound protocol stream ceiling for bulk FlatSQL shard ranges', async () => {
    const source = await import('node:fs').then((fs) => fs.readFileSync(new URL('./sdn-backend-libp2p-sync.ts', import.meta.url), 'utf8'));

    expect(source).toContain('export const LIBP2P_FLATSQL_SYNC_MAX_OUTBOUND_STREAMS = 512');
    expect(source).toContain('maxOutboundStreams: LIBP2P_FLATSQL_SYNC_MAX_OUTBOUND_STREAMS');
  });

  it('selects WebRTC direct transport for direct WebRTC multiaddrs', () => {
    expect(selectLibp2pFlatSqlSyncTransports([
      '/ip4/167.172.219.213/udp/4003/webrtc-direct/certhash/uEiDkQCtOX-kkIk6CsI0pdCvIzTQ4IkRF1ujnZ6CSvED3cw/p2p/16Uiu2HCelesTrak',
    ])).toEqual({
      tcp: false,
      webSockets: false,
      webTransport: false,
      webRtcRelay: false,
      webRtcDirect: true,
    });
  });

  it('selects relay WebRTC separately from WebRTC direct', () => {
    expect(selectLibp2pFlatSqlSyncTransports([
      '/ip4/159.203.150.8/tcp/8080/ws/p2p/16Uiu2HRelay/p2p-circuit/webrtc/p2p/16Uiu2HCelesTrak',
    ])).toEqual({
      tcp: false,
      webSockets: true,
      webTransport: false,
      webRtcRelay: true,
      webRtcDirect: false,
    });
  });

  it('selects native TCP for desktop/node libp2p multiaddrs', () => {
    expect(selectLibp2pFlatSqlSyncTransports([
      '/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HCelesTrak',
    ])).toEqual({
      tcp: true,
      webSockets: false,
      webTransport: false,
      webRtcRelay: false,
      webRtcDirect: false,
    });
  });

  it('orders native TCP before websocket for node/desktop sync clients', () => {
    expect(orderLibp2pFlatSqlSyncDialAddrs([
      '/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak',
      '/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HCelesTrak',
    ])).toEqual([
      '/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HCelesTrak',
      '/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak',
    ]);
  });

  it('does not dial WebRTC direct from node when the runtime has no browser WebRTC certificate API', () => {
    expect(isLibp2pFlatSqlSyncAddrDialable(
      '/ip4/167.172.219.213/udp/4003/webrtc-direct/certhash/uEiDkQCtOX-kkIk6CsI0pdCvIzTQ4IkRF1ujnZ6CSvED3cw/p2p/16Uiu2HCelesTrak',
      {
        tcp: false,
        webSockets: false,
        webTransport: false,
        webRtcRelay: false,
        webRtcDirect: true,
      },
    )).toBe(false);
  });

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
      limit: 1,
      offset: 0,
    });
  });

  it('uses the raw CelesTrak SATCAT CSV source for CAT fallback summary scans', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      schemas: ['CAT.fbs'],
      syncClient: {
        async readFlatSqlSyncChunk(query) {
          calls.push(query);
          return headerOnlyChunk(query.schema, query.sourceName === 'celestrak-satcat-csv' ? 98_123 : 972_737);
        },
      },
    });

    await expect(backend.getDataSummary()).resolves.toMatchObject({
      ok: true,
      data: {
        totalRecords: 98_123,
        schemas: [{ schemaName: 'CAT.fbs', count: 98_123 }],
        sources: [{
          schemaName: 'CAT.fbs',
          providerId: 'space-data-network-02',
          sourceName: 'celestrak-satcat-csv',
          count: 98_123,
        }],
      },
    });
    expect(calls).toEqual([
      expect.objectContaining({
        op: 'scan',
        schema: 'CAT.fbs',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-satcat-csv',
        limit: 1,
        offset: 0,
      }),
    ]);
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

  it('builds data summaries from registered datastore namespaces when advertised over libp2p', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      schemas: ['OMM.fbs'],
      syncClient: {
        async listFlatSqlSyncDatastores() {
          return {
            count: 1,
            results: [{
              key: 'sdn-ds-v1-history',
              updatedAt: 1778712000,
              identity: {
                schemaName: 'OMM.fbs',
                sourcePeerId: 'source:history',
                sourcePublicKey: 'history-public-key',
                providerId: 'space-data-network-02',
                sourceName: 'celestrak-gp-historical',
              },
            }],
          };
        },
        async readFlatSqlSyncChunk(query) {
          calls.push(query);
          return headerOnlyChunk(query.schema, 44_349_135);
        },
      },
    });

    await expect(backend.getDataSummary()).resolves.toMatchObject({
      ok: true,
      data: {
        totalRecords: 44_349_135,
        schemas: [{ schemaName: 'OMM.fbs', count: 44_349_135 }],
        sources: [{
          datastoreKey: 'sdn-ds-v1-history',
          schemaName: 'OMM.fbs',
          providerId: 'space-data-network-02',
          sourceName: 'celestrak-gp-historical',
          producerPeerId: 'source:history',
          producerPublicKey: 'history-public-key',
          count: 44_349_135,
        }],
      },
    });
    expect(calls).toEqual([
      expect.objectContaining({
        op: 'scan',
        schema: 'OMM.fbs',
        datastoreKey: 'sdn-ds-v1-history',
        limit: 1,
        offset: 0,
      }),
    ]);
  });

  it('merges schema scans into partial datastore summaries with schema-specific CelesTrak source names', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      schemas: ['CAT.fbs', 'OMM.fbs'],
      syncClient: {
        async listFlatSqlSyncDatastores() {
          return {
            count: 1,
            results: [{
              key: 'sdn-ds-v1-omm-history',
              updatedAt: 1778712000,
              identity: {
                schemaName: 'OMM.fbs',
                sourcePeerId: 'source:history',
                sourcePublicKey: 'history-public-key',
                providerId: 'space-data-network-02',
                sourceName: 'celestrak-gp-historical',
              },
            }],
          };
        },
        async readFlatSqlSyncChunk(query) {
          calls.push(query);
          if (query.datastoreKey) return headerOnlyChunk(query.schema, 44_349_135);
          return headerOnlyChunk(query.schema, query.schema === 'CAT.fbs' ? 145_902 : 0);
        },
      },
    });

    await expect(backend.getDataSummary()).resolves.toMatchObject({
      ok: true,
      data: {
        totalRecords: 44_495_037,
        schemas: expect.arrayContaining([
          { schemaName: 'OMM.fbs', count: 44_349_135, totalBytes: 0 },
          { schemaName: 'CAT.fbs', count: 145_902, totalBytes: 0 },
        ]),
        sources: expect.arrayContaining([
          expect.objectContaining({
            datastoreKey: 'sdn-ds-v1-omm-history',
            schemaName: 'OMM.fbs',
            sourceName: 'celestrak-gp-historical',
          }),
          expect.objectContaining({
            schemaName: 'CAT.fbs',
            providerId: 'space-data-network-02',
            sourceName: 'celestrak-satcat-csv',
            count: 145_902,
          }),
        ]),
      },
    });
    const catScan = calls.find((call) => call.schema === 'CAT.fbs');
    expect(catScan).toMatchObject({
      op: 'scan',
      schema: 'CAT.fbs',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-satcat-csv',
      limit: 1,
      offset: 0,
    });
  });

  it('falls back to schema scans when a provider does not support datastore discovery yet', async () => {
    const calls: FlatSqlSyncQuery[] = [];
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      schemas: ['OMM.fbs'],
      syncClient: {
        async listFlatSqlSyncDatastores() {
          throw new Error('unsupported sync op "list_datastores"');
        },
        async readFlatSqlSyncChunk(query) {
          calls.push(query);
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
    expect(calls).toEqual([
      expect.objectContaining({
        op: 'scan',
        schema: 'OMM.fbs',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
      }),
    ]);
  });

  it('loads summary schemas sequentially instead of parallel dialing every schema', async () => {
    let inFlight = 0;
    let maxInFlight = 0;
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      schemas: ['CAT.fbs', 'OMM.fbs', 'PNM.fbs'],
      syncClient: {
        async readFlatSqlSyncChunk(query) {
          inFlight += 1;
          maxInFlight = Math.max(maxInFlight, inFlight);
          await Promise.resolve();
          inFlight -= 1;
          return headerOnlyChunk(query.schema, query.schema === 'OMM.fbs' ? 10 : 0);
        },
      },
    });

    await expect(backend.getDataSummary()).resolves.toMatchObject({ ok: true });

    expect(maxInFlight).toBe(1);
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
              recordStream: flatSqlSizePrefixedStream([rawRecord]),
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
            recordStream: new Uint8Array(),
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

  it('carries datastore namespaces through libp2p scan and stream requests', async () => {
    const rawRecord = new Uint8Array([1, 3, 3, 7]);
    const calls: FlatSqlSyncQuery[] = [];
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      datastoreKey: 'sdn-ds-v1-history',
      syncClient: {
        async readFlatSqlSyncChunk(query) {
          calls.push(query);
          if (query.op === 'read_chunk') {
            return {
              header: headerOnlyChunk('OMM.fbs', 1, {
                count: 1,
                results: [recordRef()],
                scanHash: 'namespace-scan-hash',
                chunkHash: 'namespace-scan-hash',
              }).header,
              records: [rawRecord],
              recordStream: flatSqlSizePrefixedStream([rawRecord]),
            };
          }
          return headerOnlyChunk('OMM.fbs', 1, {
            count: 1,
            results: [recordRef()],
            scanHash: 'namespace-scan-hash',
            chunkHash: 'namespace-scan-hash',
          });
        },
      },
    });

    const scan = await backend.scanRawData({ schema: 'OMM.fbs', limit: 1 });
    await expect(backend.streamRawData({
      schema: 'OMM.fbs',
      scanHash: scan.data?.scanHash,
      records: scan.data?.results ?? [],
    })).resolves.toMatchObject({
      ok: true,
      data: [{ cid: 'omm-cid-1', dataBytes: rawRecord }],
    });

    expect(calls).toEqual([
      expect.objectContaining({ op: 'scan', datastoreKey: 'sdn-ds-v1-history' }),
      expect.objectContaining({ op: 'read_chunk', datastoreKey: 'sdn-ds-v1-history' }),
    ]);
  });

  it('queries raw data with one direct read_chunk stream instead of scan plus row-ref stream', async () => {
    const rawRecord = new Uint8Array([9, 8, 7, 6]);
    const calls: FlatSqlSyncQuery[] = [];
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      syncClient: {
        async readFlatSqlSyncChunk(query) {
          calls.push(query);
          return {
            header: headerOnlyChunk('OMM.fbs', 2_005_702, {
              count: 1,
              limit: 50_000,
              nextCursor: 'cursor-50000',
              results: [recordRef()],
              chunkHash: 'chunk-hash',
              scanHash: 'scan-hash',
            }).header,
            records: [rawRecord],
            recordStream: flatSqlSizePrefixedStream([rawRecord]),
          };
        },
      },
    });

    await expect(backend.queryRawData({
      schema: 'OMM.fbs',
      syncFilter: "EPOCH >= '2026-05-01T00:00:00Z'",
      limit: 50_000,
      offset: 10_000,
    })).resolves.toMatchObject({
      ok: true,
      data: [{ cid: 'omm-cid-1', dataBytes: rawRecord }],
    });

    expect(calls).toEqual([
      expect.objectContaining({
        op: 'read_chunk',
        targetPeerId: '16Uiu2HCelesTrak',
        candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
        schema: 'OMM.fbs',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        syncFilter: "EPOCH >= '2026-05-01T00:00:00Z'",
        limit: 50_000,
        offset: 10_000,
      }),
    ]);
  });

  it('fails stream requests that return refs without matching FlatBuffer frames', async () => {
    const backend = createLibp2pFlatSqlSyncBackend({
      targetPeerId: '16Uiu2HCelesTrak',
      candidateAddrs: ['/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HCelesTrak'],
      syncClient: {
        async readFlatSqlSyncChunk(query) {
          return {
            header: headerOnlyChunk('OMM.fbs', 1, {
              count: 1,
              results: [recordRef()],
              chunkHash: query.chunkHash ?? 'chunk-hash',
              scanHash: query.scanHash ?? 'scan-hash',
            }).header,
            records: [],
            recordStream: new Uint8Array(),
          };
        },
      },
    });

    await expect(backend.streamRawData({
      schema: 'OMM.fbs',
      scanHash: 'scan-hash',
      chunkHash: 'chunk-hash',
      records: [recordRef()],
    })).resolves.toMatchObject({
      ok: false,
      capability: {
        id: 'streamRawData',
        state: 'unavailable',
        reason: 'remote FlatSQL sync returned 0/1 FlatBuffer frames',
      },
    });
  });

  it('reads response bytes while the outbound request sink is still settling', async () => {
    let releaseSink: (() => void) | undefined;
    const sourceStarted = new Promise<void>((resolve) => {
      releaseSink = resolve;
    });
    const stream = {
      async sink(source: AsyncIterable<Uint8Array>) {
        const chunks: Uint8Array[] = [];
        for await (const chunk of source) chunks.push(chunk);
        expect(chunks.map((chunk) => Array.from(chunk))).toEqual([[1, 2, 3, 4]]);
        await sourceStarted;
      },
      source: (async function* source() {
        releaseSink?.();
        yield new Uint8Array([9, 8, 7, 6]);
      })(),
      async close() {},
    };

    await expect(Promise.race([
      exchangeFlatSqlSyncStream(stream, new Uint8Array([1, 2, 3, 4])),
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('exchange deadlocked')), 25)),
    ])).resolves.toEqual(new Uint8Array([9, 8, 7, 6]));
  });

  it('does not clone every inbound response chunk before final concatenation', async () => {
    const source = await import('node:fs').then((fs) => fs.readFileSync(new URL('./sdn-backend-libp2p-sync.ts', import.meta.url), 'utf8'));

    expect(source).toContain('chunks.push(streamChunkBytes(chunk))');
    expect(source).not.toContain('chunks.push(cloneStreamBytes(chunk))');
  });

  it('bounds stream exchanges that never close', async () => {
    const stream = {
      async sink(source: AsyncIterable<Uint8Array>) {
        for await (const _chunk of source) {
          // Drain the request payload and then leave the remote response open.
        }
      },
      source: (async function* source() {
        yield new Uint8Array([9, 8, 7, 6]);
        await new Promise(() => undefined);
      })(),
      async close() {},
    };

    await expect(Promise.race([
      exchangeFlatSqlSyncStream(stream, new Uint8Array([1, 2, 3, 4]), { timeoutMs: 1, label: 'FlatSQL sync probe' }),
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('exchange did not time out')), 25)),
    ])).rejects.toThrow('FlatSQL sync probe timed out after 1 ms');
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
    recordStream: new Uint8Array(),
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

function flatSqlSizePrefixedStream(records: Uint8Array[]): Uint8Array {
  const totalLength = records.reduce((sum, frame) => sum + 4 + frame.byteLength, 0);
  const out = new Uint8Array(totalLength);
  const view = new DataView(out.buffer);
  let offset = 0;
  for (const frame of records) {
    view.setUint32(offset, frame.byteLength, true);
    offset += 4;
    out.set(frame, offset);
    offset += frame.byteLength;
  }
  return out;
}
