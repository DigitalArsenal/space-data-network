import { describe, it, expect } from 'vitest';

import {
  FlatSQLStorage,
  MemorySnapshotPersistence,
  HeliaSnapshotPersistence,
} from './flatsql-storage';

const enc = (text: string) => new TextEncoder().encode(text);

describe('FlatSQLStorage', () => {
  it('stores, gets, queries, and content-address dedupes records', async () => {
    const storage = await FlatSQLStorage.open();
    const cid = await storage.store('OMM', enc('record-1'), 'peer-a', enc('sig'));
    expect(cid).toMatch(/^[0-9a-f]{64}$/);
    expect(await storage.store('OMM', enc('record-1'), 'peer-a', enc('sig'))).toBe(cid);
    expect(await storage.count()).toBe(1);

    const record = await storage.get('OMM', cid);
    expect(record?.peerId).toBe('peer-a');
    expect([...(record?.data ?? [])]).toEqual([...enc('record-1')]);
    expect(await storage.get('CDM', cid)).toBeNull();

    await storage.store('OMM', enc('record-2'), 'peer-b', enc('sig'));
    await storage.store('CDM', enc('record-3'), 'peer-a', enc('sig'));
    expect((await storage.query('OMM')).length).toBe(2);
    expect((await storage.query('OMM', { peerId: 'peer-b' })).length).toBe(1);
    expect((await storage.query('OMM', { limit: 1 })).length).toBe(1);
  });

  it('exposes SQL over the engine control table with schema provenance', async () => {
    // D.1: the row index lives in the FlatSQL-WASM engine's control table,
    // named after the server layout (sdn_record_index, flatsql.go).
    const storage = await FlatSQLStorage.open();
    await storage.store('OMM', enc('a'), 'p', enc(''));
    await storage.store('OMM', enc('b'), 'p', enc(''));
    await storage.store('CDM', enc('c'), 'p', enc(''));
    const result = storage.sql(
      "SELECT schema_name, cid FROM sdn_record_index WHERE schema_name = 'OMM' ORDER BY rowid",
    );
    expect(result.rowCount).toBe(2);
    expect(result.rows.map((r) => r[0])).toEqual(['OMM', 'OMM']);
  });

  it('delete tombstones a record out of gets, queries, and snapshots', async () => {
    const persistence = new MemorySnapshotPersistence();
    const storage = await FlatSQLStorage.open({ persistence });
    const cid = await storage.store('OMM', enc('doomed'), 'p', enc(''));
    await storage.delete(cid);
    expect(await storage.get('OMM', cid)).toBeNull();
    expect(await storage.query('OMM')).toEqual([]);

    const reopened = await FlatSQLStorage.open({ persistence });
    expect(await reopened.count()).toBe(0);
  });

  it('persists a snapshot and reloads the store of record across open()', async () => {
    const persistence = new MemorySnapshotPersistence();
    const first = await FlatSQLStorage.open({ persistence });
    const cid1 = await first.store('OMM', enc('alpha'), 'peer-a', enc('sig-a'));
    const cid2 = await first.store('CDM', enc('beta'), 'peer-b', enc('sig-b'));
    await first.close();

    const second = await FlatSQLStorage.open({ persistence });
    expect(await second.count()).toBe(2);
    const alpha = await second.get('OMM', cid1);
    expect([...(alpha?.data ?? [])]).toEqual([...enc('alpha')]);
    expect(alpha?.peerId).toBe('peer-a');
    const beta = await second.get('CDM', cid2);
    expect([...(beta?.signature ?? [])]).toEqual([...enc('sig-b')]);
    // Engine control-table rows rebuilt from the snapshot
    expect(second.sql('SELECT cid FROM sdn_record_index').rowCount).toBe(2);
  });

  it('HeliaSnapshotPersistence round-trips through an addBytes/catBytes surface', async () => {
    const blocks = new Map<string, Uint8Array>();
    let counter = 0;
    const helia = {
      async addBytes(bytes: Uint8Array) {
        const cid = `bafy-fake-${++counter}`;
        blocks.set(cid, new Uint8Array(bytes));
        return { toString: () => cid };
      },
      async catBytes(cid: string) {
        const found = blocks.get(cid);
        if (!found) throw new Error(`unknown cid ${cid}`);
        return new Uint8Array(found);
      },
    };
    const refs = new Map<string, string>();
    const refStore = {
      getItem: (key: string) => refs.get(key) ?? null,
      setItem: (key: string, value: string) => void refs.set(key, value),
    };
    const persistence = new HeliaSnapshotPersistence(helia, refStore);

    const first = await FlatSQLStorage.open({ persistence });
    await first.store('OMM', enc('helia-record'), 'peer', enc(''));
    await first.close();
    expect(persistence.rootCid()).toMatch(/^bafy-fake-/); // store() auto-flush + close() flush

    const second = await FlatSQLStorage.open({ persistence });
    expect(await second.count()).toBe(1);
    expect((await second.query('OMM'))[0].peerId).toBe('peer');
  });
});
