import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import { clearLocalFlatSqlStore, createLocalFlatSqlStore, isReadOnlyFlatSqlQuery, stripSdnFlatBufferSizePrefix } from './local-flatsql';

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

  it('allows callers to clear persisted local FlatSQL data without IndexedDB support', async () => {
    await expect(clearLocalFlatSqlStore({
      persistenceKey: 'sdn-data:configured:space-data-network-02',
      standardIds: ['OMM', 'PNM'],
    })).resolves.toBeUndefined();
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
