import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import { buildEpochProfileSql } from './epoch-query-sql';
import { clearLocalFlatSqlStore, createLocalFlatSqlStore, decodeFlatSqlSizePrefixedStream, flatSqlSizePrefixedStreamInfo, isReadOnlyFlatSqlQuery, stripSdnFlatBufferSizePrefix } from './local-flatsql';

const OMM_SCHEMA = readFileSync(
  new URL('../../../../../spacedatastandards.org/schema/OMM/main.fbs', import.meta.url),
  'utf8',
);
const PNM_SCHEMA = readFileSync(
  new URL('../../../../../spacedatastandards.org/schema/PNM/main.fbs', import.meta.url),
  'utf8',
);
const STARLINK_6292_OMM_BYTES = Buffer.from('HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA', 'base64');

describe('local FlatSQL datastore', () => {
  it('strips the SDN size prefix before ingesting standard FlatBuffers', () => {
    const raw = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);

    expect(String.fromCharCode(...raw.slice(4, 8))).toBe('$OMM');
    expect(raw.byteLength).toBe(STARLINK_6292_OMM_BYTES.byteLength - 4);
  });

  it('ingests downloaded OMM FlatBuffers and answers SQL over the local store', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-1',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], 'space-data-network-02');
    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-1',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], 'space-data-network-02');

    const result = await store.query('SELECT OBJECT_NAME, NORAD_CAT_ID FROM OMM WHERE NORAD_CAT_ID = 56775 LIMIT 10');

    expect(result.columns).toEqual(['OBJECT_NAME', 'NORAD_CAT_ID']);
    expect(result.records).toEqual([{ OBJECT_NAME: 'STARLINK-6292', NORAD_CAT_ID: 56775 }]);
    expect(store.getStats()).toEqual([expect.objectContaining({
      standardId: 'OMM',
      tableName: 'OMM',
      recordCount: 1,
      ingestedRecordCount: 1,
    })]);
    expect(store.getStats()[0]?.cachedBytes).toBeGreaterThan(0);
  });

  it('runs generated OMM epoch profile SQL against the local FlatSQL store', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-epoch-sql',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], { persist: false });

    const daySql = buildEpochProfileSql({
      standardId: 'OMM',
      profile: 'epoch.day',
      day: '2026-05-10',
      limit: 10,
    });
    const nearestSql = buildEpochProfileSql({
      standardId: 'OMM',
      profile: 'epoch.nearest',
      at: '2026-05-11T00:00',
      maxDeltaSeconds: 86400,
      limit: 10,
    });

    expect(store.query(daySql, 'OMM').records).toEqual([expect.objectContaining({ NORAD_CAT_ID: 56775 })]);
    expect(store.query(nearestSql, 'OMM').records).toEqual([expect.objectContaining({ OBJECT_NAME: 'STARLINK-6292' })]);
  });

  it('supports deferred persistence for bulk ingest batches', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-deferred',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], { source: 'space-data-network-02', persist: false });

    expect(store.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 1', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
    expect(store.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
      recordCount: 1,
      cachedBytes: 0,
    }));

    await store.flush('OMM');

    expect(store.getStats({ includeCachedBytes: false })[0]?.cachedBytes).toBeGreaterThan(0);
  });

  it('ingests native FlatSQL size-prefixed streams without using JSON row objects as the sync boundary', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    const stream = flatSqlSizePrefixedStream([stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES)]);
    const ingested = await store.ingestFlatBufferStream('OMM', stream, {
      recordKeys: ['celestrak-omm-stream'],
      persist: false,
    });

    expect(ingested).toBe(1);
    expect(store.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 1', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
    expect(store.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
      recordCount: 1,
      ingestedRecordCount: 1,
    }));
  });

  it('resumes native FlatSQL stream ingest from a skipped frame with a zero-offset buffer', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    const frame = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    const stream = flatSqlSizePrefixedStream([frame, frame]);
    const ingested = await store.ingestFlatBufferStream('OMM', stream, {
      persist: false,
      skipRecords: 1,
      recordKeyPrefix: 'published:bafkshard',
      recordKeyOffset: 1,
    });

    expect(ingested).toBe(1);
    expect(store.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 1', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
    expect(store.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
      recordCount: 1,
      ingestedRecordCount: 1,
    }));
  });

  it('trusts ordered published shard offsets when stale record keys claim missing rows', async () => {
    const restoreIndexedDb = installMemoryIndexedDb();
    try {
      await clearLocalFlatSqlStore({
        persistenceKey: 'stale-published-keys',
        standardIds: ['OMM'],
      });

      const first = await createLocalFlatSqlStore({
        persistenceKey: 'stale-published-keys',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      const frame = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
      await first.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([frame]), {
        persist: true,
        recordKeyPrefix: 'published:already-materialized',
      });
      await first.flush('OMM');
      first.destroy();

      await putLocalFlatSqlTestValue('stale-published-keys:OMM:record-keys', [
        'OMM|published:stale-shard|1',
      ]);

      const resumed = await createLocalFlatSqlStore({
        persistenceKey: 'stale-published-keys',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      const ingested = await resumed.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([frame, frame]), {
        persist: false,
        skipRecords: 1,
        recordKeyPrefix: 'published:stale-shard',
        recordKeyOffset: 1,
      });

      expect(ingested).toBe(1);
      expect(resumed.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 2,
        ingestedRecordCount: 1,
      }));
      resumed.destroy();
    } finally {
      restoreIndexedDb();
    }
  });

  it('records pin ledger entries for verified published FlatSQL shards', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    const stream = flatSqlSizePrefixedStream([stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES)]);
    await store.ingestFlatBufferStream('OMM', stream, {
      persist: false,
      recordKeyPrefix: 'published:bafkshard',
      pinLedgerEntries: [{
        cid: 'bafkshard',
        standardId: 'OMM',
        schemaName: 'OMM.fbs',
        providerPeerId: '16Uiu2HCelesTrak',
        providerPublicKey: 'provider-public-key',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'source-sha-001',
        queryProfile: 'dataset-publication-offset-v1',
        snapshotId: 'head-1',
        head: 'head-1',
        highWaterMark: 'head-1:1',
        byteHash: 'shard-sha',
        role: 'shard',
        rowCount: 1,
        byteCount: stream.byteLength,
        verificationState: 'verified',
        materializedAt: '2026-05-12T00:00:00.000Z',
        verifiedAt: '2026-05-12T00:00:00.000Z',
      }],
    });

    await expect(store.listPinLedgerEntries({
      standardId: 'OMM',
      providerPeerId: '16Uiu2HCelesTrak',
      queryProfile: 'dataset-publication-offset-v1',
      role: 'shard',
    })).resolves.toEqual([
      expect.objectContaining({
        cid: 'bafkshard',
        schemaName: 'OMM.fbs',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'source-sha-001',
        head: 'head-1',
        highWaterMark: 'head-1:1',
        byteHash: 'shard-sha',
        verificationState: 'verified',
      }),
    ]);
    await expect(Promise.resolve(store.getStats({ includeCachedBytes: false }))).resolves.toEqual([
      expect.objectContaining({
        standardId: 'OMM',
        pinnedRows: 1,
        pinnedBytes: stream.byteLength,
        snapshotId: 'head-1',
        head: 'head-1',
        highWaterMark: 'head-1:1',
        lastSyncedAt: '2026-05-12T00:00:00.000Z',
      }),
    ]);
  });

  it('decodes FlatSQL sync streams with zero-copy record views', () => {
    const first = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    const stream = flatSqlSizePrefixedStream([first]);

    const records = decodeFlatSqlSizePrefixedStream(stream);

    expect(records).toHaveLength(1);
    expect(records[0]?.buffer).toBe(stream.buffer);
  });

  it('scans native FlatSQL shard streams without materializing record views', () => {
    const direct = stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES);
    const stream = flatSqlSizePrefixedStream([direct, direct]);
    const firstFrameLength = 4 + direct.byteLength;

    expect(flatSqlSizePrefixedStreamInfo(stream)).toEqual({
      totalRecordCount: 2,
      ingestRecordCount: 2,
      ingestStartOffset: 0,
      allFramesHaveDirectFileIdentifier: true,
    });
    expect(flatSqlSizePrefixedStreamInfo(stream, 1)).toEqual({
      totalRecordCount: 2,
      ingestRecordCount: 1,
      ingestStartOffset: firstFrameLength,
      allFramesHaveDirectFileIdentifier: true,
    });
    expect(flatSqlSizePrefixedStreamInfo(flatSqlSizePrefixedStream([STARLINK_6292_OMM_BYTES]))).toEqual({
      totalRecordCount: 1,
      ingestRecordCount: 1,
      ingestStartOffset: 0,
      allFramesHaveDirectFileIdentifier: false,
    });
  });

  it('tracks downloaded FlatBuffer frames separately from materialized SQL rows', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [
      {
        cid: 'celestrak-omm-history',
        schemaName: 'OMM.fbs',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'fixture-batch',
        timestamp: '2026-05-11T04:02:25Z',
        dataBytes: STARLINK_6292_OMM_BYTES,
      },
      {
        cid: 'celestrak-omm-history',
        schemaName: 'OMM.fbs',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'fixture-batch',
        timestamp: '2026-05-11T07:02:25Z',
        dataBytes: STARLINK_6292_OMM_BYTES,
      },
    ], { source: 'space-data-network-02', persist: false });

    expect(store.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
      recordCount: 2,
      ingestedRecordCount: 2,
    }));
  });

  it('keeps query results fresh across ingest, persisted load, query template changes, and namespace changes', async () => {
    const restoreIndexedDb = installMemoryIndexedDb();
    try {
      await clearLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-a',
        standardIds: ['OMM', 'PNM'],
      });
      await clearLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-b',
        standardIds: ['OMM'],
      });

      const first = await createLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-a',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });

      expect(first.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([]);
      await first.ingestRecords('OMM', [{
        cid: 'cache-omm-1',
        schemaName: 'OMM.fbs',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'fixture-batch',
        timestamp: '2026-05-11T04:02:25Z',
        dataBytes: STARLINK_6292_OMM_BYTES,
      }]);

      expect(first.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
      expect(first.query('SELECT OBJECT_NAME FROM OMM WHERE NORAD_CAT_ID = 56775 LIMIT 10', 'OMM').records).toEqual([{ OBJECT_NAME: 'STARLINK-6292' }]);

      await first.ingestRecords('OMM', [{
        cid: 'cache-omm-2',
        schemaName: 'OMM.fbs',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'fixture-batch',
        timestamp: '2026-05-11T07:02:25Z',
        dataBytes: STARLINK_6292_OMM_BYTES,
      }]);
      expect(first.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }, { NORAD_CAT_ID: 56775 }]);
      await first.flush('OMM');
      first.destroy();

      const loaded = await createLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-a',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      expect(loaded.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }, { NORAD_CAT_ID: 56775 }]);
      expect(loaded.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 2,
        ingestedRecordCount: 2,
      }));

      const otherNamespace = await createLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-b',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      expect(otherNamespace.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([]);

      const otherTemplate = await createLocalFlatSqlStore({
        persistenceKey: 'cache-invalidation-a',
        schemas: [{
          standardId: 'PNM',
          tableName: 'PNM',
          fileId: '$PNM',
          schema: PNM_SCHEMA,
        }],
      });
      expect(otherTemplate.query('SELECT * FROM PNM LIMIT 0', 'PNM').columns).not.toContain('NORAD_CAT_ID');
      expect(otherTemplate.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        standardId: 'PNM',
        recordCount: 0,
      }));
    } finally {
      restoreIndexedDb();
    }
  });

  it('persists local FlatSQL bytes through the desktop FlatBuffer storage API when configured', async () => {
    const persisted = new Map<string, Uint8Array>();
    const calls: Array<{ method: string; url: string }> = [];
    const fetchMock = async (url: string | URL | Request, init?: RequestInit): Promise<Response> => {
      const requestUrl = String(url);
      const method = String(init?.method ?? 'GET').toUpperCase();
      calls.push({ method, url: requestUrl });
      const key = decodeURIComponent(new URL(requestUrl).pathname.split('/').pop() ?? '');
      if (method === 'GET') {
        const bytes = persisted.get(key);
        return bytes
          ? new Response(bytes.slice(), { status: 200 })
          : new Response('missing', { status: 404 });
      }
      if (method === 'PUT') {
        const body = init?.body;
        const bytes = body instanceof Uint8Array
          ? body
          : body instanceof ArrayBuffer
            ? new Uint8Array(body)
            : new Uint8Array(await new Response(body as BodyInit).arrayBuffer());
        persisted.set(key, bytes.slice());
        return new Response(null, { status: 204 });
      }
      if (method === 'DELETE') {
        persisted.delete(key);
        return new Response(null, { status: 204 });
      }
      return new Response('bad method', { status: 405 });
    };

    const first = await createLocalFlatSqlStore({
      persistenceKey: 'desktop-flatbuffer-cache',
      desktopPersistenceBaseUrl: 'http://desktop.local',
      fetch: fetchMock,
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });
    await first.ingestRecords('OMM', [{
      cid: 'desktop-cache-omm',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }]);
    await first.flush('OMM');
    first.destroy();

    const loaded = await createLocalFlatSqlStore({
      persistenceKey: 'desktop-flatbuffer-cache',
      desktopPersistenceBaseUrl: 'http://desktop.local',
      fetch: fetchMock,
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    expect(loaded.query('SELECT NORAD_CAT_ID FROM OMM LIMIT 10', 'OMM').records).toEqual([{ NORAD_CAT_ID: 56775 }]);
    expect(persisted.has('desktop-flatbuffer-cache:OMM')).toBe(true);
    expect(persisted.has('desktop-flatbuffer-cache:OMM:record-keys')).toBe(true);
    expect(calls).toEqual(expect.arrayContaining([
      expect.objectContaining({ method: 'PUT', url: 'http://desktop.local/api/flatsql/persistence/desktop-flatbuffer-cache%3AOMM' }),
      expect.objectContaining({ method: 'GET', url: 'http://desktop.local/api/flatsql/persistence/desktop-flatbuffer-cache%3AOMM' }),
    ]));
    loaded.destroy();
  });

  it('can defer published-shard pin ledger persistence until the FlatSQL checkpoint flush', async () => {
    const persisted = new Map<string, Uint8Array>();
    const calls: Array<{ method: string; key: string }> = [];
    const fetchMock = async (url: string | URL | Request, init?: RequestInit): Promise<Response> => {
      const method = String(init?.method ?? 'GET').toUpperCase();
      const key = decodeURIComponent(new URL(String(url)).pathname.split('/').pop() ?? '');
      calls.push({ method, key });
      if (method === 'GET') {
        const bytes = persisted.get(key);
        return bytes
          ? new Response(bytes.slice(), { status: 200 })
          : new Response('missing', { status: 404 });
      }
      if (method === 'PUT') {
        const bytes = init?.body instanceof Uint8Array
          ? init.body
          : new Uint8Array(await new Response(init?.body as BodyInit).arrayBuffer());
        persisted.set(key, bytes.slice());
        return new Response(null, { status: 204 });
      }
      return new Response(null, { status: method === 'DELETE' ? 204 : 405 });
    };

    const store = await createLocalFlatSqlStore({
      persistenceKey: 'deferred-pin-ledger',
      desktopPersistenceBaseUrl: 'http://desktop.local',
      fetch: fetchMock,
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });
    await store.ingestFlatBufferStream('OMM', flatSqlSizePrefixedStream([stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES)]), {
      persist: false,
      recordKeyPrefix: 'published:bafycheckpoint',
    });
    await store.recordPinLedgerEntries([{
      cid: 'bafycheckpoint',
      standardId: 'OMM',
      schemaName: 'OMM.fbs',
      role: 'shard',
      rowCount: 1,
      byteCount: STARLINK_6292_OMM_BYTES.byteLength,
      verificationState: 'verified',
      materializedAt: '2026-05-15T00:00:00.000Z',
      verifiedAt: '2026-05-15T00:00:00.000Z',
      updatedAt: '2026-05-15T00:00:00.000Z',
    }], { persist: false });

    await expect(store.listPinLedgerEntries({ standardId: 'OMM' })).resolves.toHaveLength(1);
    expect(calls.some((call) => call.method === 'PUT' && call.key === 'deferred-pin-ledger:pin-ledger')).toBe(false);

    await store.flush('OMM');

    expect(calls.some((call) => call.method === 'PUT' && call.key === 'deferred-pin-ledger:pin-ledger')).toBe(true);
    store.destroy();
  });

  it('allows callers to clear persisted local FlatSQL data without IndexedDB support', async () => {
    await expect(clearLocalFlatSqlStore({
      persistenceKey: 'sdn-data:configured:space-data-network-02',
      standardIds: ['OMM', 'PNM'],
    })).resolves.toBeUndefined();
  });

  it('repairs persisted record-key counts that outrun rebuilt FlatSQL rows', async () => {
    const restoreIndexedDb = installMemoryIndexedDb();
    try {
      await clearLocalFlatSqlStore({
        persistenceKey: 'repair-record-keys',
        standardIds: ['OMM'],
      });

      const first = await createLocalFlatSqlStore({
        persistenceKey: 'repair-record-keys',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });
      const stream = flatSqlSizePrefixedStream([stripSdnFlatBufferSizePrefix(STARLINK_6292_OMM_BYTES)]);
      await first.ingestFlatBufferStream('OMM', stream, {
        persist: true,
        recordKeyPrefix: 'published:bafkshard',
      });
      await first.flush('OMM');
      first.destroy();

      await putLocalFlatSqlTestValue('repair-record-keys:OMM:record-keys', [
        'OMM|published:bafkshard|0',
        'OMM|published:bafkshard|1',
      ]);

      const repaired = await createLocalFlatSqlStore({
        persistenceKey: 'repair-record-keys',
        schemas: [{
          standardId: 'OMM',
          tableName: 'OMM',
          fileId: '$OMM',
          schema: OMM_SCHEMA,
        }],
      });

      expect(repaired.getStats({ includeCachedBytes: false })[0]).toEqual(expect.objectContaining({
        recordCount: 1,
        ingestedRecordCount: 0,
      }));
      repaired.destroy();
    } finally {
      restoreIndexedDb();
    }
  });

  it('rejects non-read-only SQL before it reaches FlatSQL', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    expect(isReadOnlyFlatSqlQuery('SELECT * FROM OMM LIMIT 1')).toBe(true);
    expect(isReadOnlyFlatSqlQuery('WITH latest AS (SELECT * FROM OMM) SELECT * FROM latest')).toBe(true);
    expect(isReadOnlyFlatSqlQuery('DELETE FROM OMM')).toBe(false);
    expect(isReadOnlyFlatSqlQuery('SELECT * FROM OMM; DELETE FROM OMM')).toBe(false);
    expect(isReadOnlyFlatSqlQuery('PRAGMA table_info(OMM)')).toBe(false);

    expect(() => store.query('DELETE FROM OMM')).toThrow(/read-only SELECT/);
  });

  it('enforces local SQL byte limits after FlatSQL execution', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'OMM',
        tableName: 'OMM',
        fileId: '$OMM',
        schema: OMM_SCHEMA,
      }],
    });

    await store.ingestRecords('OMM', [{
      cid: 'celestrak-omm-byte-limit',
      schemaName: 'OMM.fbs',
      peerId: 'source:celestrak',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      batchId: 'fixture-batch',
      timestamp: '2026-05-11T04:02:25Z',
      dataBytes: STARLINK_6292_OMM_BYTES,
    }], { persist: false });

    expect(() => store.query('SELECT OBJECT_NAME FROM OMM LIMIT 10', 'OMM', {
      maxBytes: 4,
      maxLimit: 10,
      timeoutMs: 5000,
    })).toThrow(/byte limit/i);
  });

  it('registers SDS schemas whose comments contain URLs without exposing comment tokens as columns', async () => {
    const store = await createLocalFlatSqlStore({
      schemas: [{
        standardId: 'PNM',
        tableName: 'PNM',
        fileId: '$PNM',
        schema: PNM_SCHEMA,
      }],
    });

    expect(store.getStats()).toEqual([expect.objectContaining({
      standardId: 'PNM',
      tableName: 'PNM',
      recordCount: 0,
    })]);
    expect(store.query('SELECT * FROM PNM LIMIT 0', 'PNM').columns).not.toContain('https');
  });
});

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

function putLocalFlatSqlTestValue(key: string, value: unknown): Promise<void> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open('sdn-local-flatsql', 1);
    request.onerror = () => reject(request.error ?? new Error('IndexedDB open failed'));
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains('datastores')) db.createObjectStore('datastores');
    };
    request.onsuccess = () => {
      const db = request.result;
      const transaction = db.transaction('datastores', 'readwrite');
      transaction.objectStore('datastores').put(value, key);
      transaction.oncomplete = () => {
        db.close();
        resolve();
      };
      transaction.onerror = () => {
        db.close();
        reject(transaction.error ?? new Error('IndexedDB write failed'));
      };
    };
  });
}

function installMemoryIndexedDb(): () => void {
  const hadIndexedDb = 'indexedDB' in globalThis;
  const previous = globalThis.indexedDB;
  const databases = new Map<string, Map<string, unknown>>();
  const factory = {
    open(name: string): MemoryIDBRequest {
      const request = new MemoryIDBRequest();
      setTimeout(() => {
        let stores = databases.get(name);
        const needsUpgrade = !stores;
        if (!stores) {
          stores = new Map<string, unknown>();
          databases.set(name, stores);
        }
        request.result = new MemoryIDBDatabase(stores);
        if (needsUpgrade) request.onupgradeneeded?.({ target: request });
        request.onsuccess?.({ target: request });
      }, 0);
      return request;
    },
    deleteDatabase(name: string): MemoryIDBRequest {
      const request = new MemoryIDBRequest();
      setTimeout(() => {
        databases.delete(name);
        request.onsuccess?.({ target: request });
      }, 0);
      return request;
    },
  };
  Object.defineProperty(globalThis, 'indexedDB', {
    configurable: true,
    writable: true,
    value: factory,
  });
  return () => {
    if (hadIndexedDb) {
      Object.defineProperty(globalThis, 'indexedDB', {
        configurable: true,
        writable: true,
        value: previous,
      });
      return;
    }
    delete (globalThis as { indexedDB?: IDBFactory }).indexedDB;
  };
}

class MemoryIDBRequest {
  result: unknown;
  error: Error | null = null;
  onsuccess: ((event: { target: MemoryIDBRequest }) => void) | null = null;
  onerror: ((event: { target: MemoryIDBRequest }) => void) | null = null;
  onupgradeneeded: ((event: { target: MemoryIDBRequest }) => void) | null = null;
}

class MemoryIDBDatabase {
  readonly objectStoreNames: { contains: (name: string) => boolean };

  constructor(private readonly stores: Map<string, unknown>) {
    this.objectStoreNames = {
      contains: (name: string) => this.stores.has(name),
    };
  }

  createObjectStore(name: string): void {
    if (!this.stores.has(name)) this.stores.set(name, new Map<string, unknown>());
  }

  transaction(_storeName: string, _mode: IDBTransactionMode): MemoryIDBTransaction {
    return new MemoryIDBTransaction(this.stores);
  }

  close(): void {}
}

class MemoryIDBTransaction {
  oncomplete: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(private readonly stores: Map<string, unknown>) {}

  objectStore(name: string): MemoryIDBObjectStore {
    let store = this.stores.get(name);
    if (!(store instanceof Map)) {
      store = new Map<string, unknown>();
      this.stores.set(name, store);
    }
    return new MemoryIDBObjectStore(store, this);
  }

  complete(): void {
    setTimeout(() => this.oncomplete?.(), 0);
  }
}

class MemoryIDBObjectStore {
  constructor(
    private readonly values: Map<string, unknown>,
    private readonly transaction: MemoryIDBTransaction,
  ) {}

  get(key: string): MemoryIDBRequest {
    const request = new MemoryIDBRequest();
    setTimeout(() => {
      request.result = this.values.get(key);
      request.onsuccess?.({ target: request });
      this.transaction.complete();
    }, 0);
    return request;
  }

  put(value: unknown, key: string): MemoryIDBRequest {
    const request = new MemoryIDBRequest();
    setTimeout(() => {
      this.values.set(key, value);
      request.result = key;
      request.onsuccess?.({ target: request });
      this.transaction.complete();
    }, 0);
    return request;
  }

  delete(key: string): MemoryIDBRequest {
    const request = new MemoryIDBRequest();
    setTimeout(() => {
      this.values.delete(key);
      request.onsuccess?.({ target: request });
      this.transaction.complete();
    }, 0);
    return request;
  }
}
