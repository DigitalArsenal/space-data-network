/**
 * The store-only FlatSQL worker (`local-flatsql-store.worker.ts`) speaks the
 * SAME protocol as the sync worker, minus the sync lanes — it is the engine
 * the dashboard window model runs in (owner ruling 2026-09-03).
 *
 * The REAL worker module is imported with a stubbed `self`, and the REAL
 * client (`createWorkerLocalFlatSqlStore`) is pointed at it through the
 * injected `createWorker` hook — the exact seam the single-file dashboard
 * uses to hand the client a bundler-inlined worker.
 */
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import type { WorkerLocalFlatSqlStore } from './local-flatsql-worker-client';

const require = createRequire(import.meta.url);
const OMM_SCHEMA = readFileSync(require.resolve('spacedatastandards.org/schema/OMM/main.fbs'), 'utf8');
// One aligned size-prefixed $OMM frame (STARLINK-6292; 4-byte u32 LE prefix + 284-byte table).
const STARLINK_6292_OMM_FRAME = Buffer.from('HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA', 'base64');

const OMM_SCHEMA_ENTRY = {
  standardId: 'OMM',
  tableName: 'OMM',
  fileId: '$OMM',
  schema: OMM_SCHEMA,
};

type WorkerScope = {
  onmessage: ((event: { data: unknown }) => void) | null;
  postMessage: (response: unknown, transferables?: unknown[]) => void;
};

let activeBridge: FakeWorkerBridge | null = null;

class FakeWorkerBridge {
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onerror: ((event: { message?: string }) => void) | null = null;
  onmessageerror: (() => void) | null = null;

  constructor() {
    activeBridge = this;
  }

  postMessage(message: unknown): void {
    const scope = (globalThis as unknown as { self: WorkerScope }).self;
    queueMicrotask(() => scope.onmessage?.({ data: message }));
  }

  terminate(): void {
    // The worker module is a singleton in this process; leave it running.
  }
}

const originalSelf = (globalThis as { self?: unknown }).self;

beforeAll(async () => {
  (globalThis as unknown as { self: WorkerScope }).self = {
    onmessage: null,
    postMessage(response: unknown) {
      queueMicrotask(() => activeBridge?.onmessage?.({ data: response }));
    },
  };
  await import('./local-flatsql-store.worker');
});

afterAll(() => {
  (globalThis as { self?: unknown }).self = originalSelf;
});

async function openWorkerStore(): Promise<WorkerLocalFlatSqlStore> {
  const { createWorkerLocalFlatSqlStore } = await import('./local-flatsql-worker-client');
  // No `Worker` global in this lane: the injected hook is the only way in.
  return await createWorkerLocalFlatSqlStore(
    { schemas: [] },
    { createWorker: () => new FakeWorkerBridge() as unknown as Worker },
  );
}

function twoFrameStream(): Uint8Array {
  const stream = new Uint8Array(STARLINK_6292_OMM_FRAME.byteLength * 2);
  stream.set(STARLINK_6292_OMM_FRAME, 0);
  stream.set(STARLINK_6292_OMM_FRAME, STARLINK_6292_OMM_FRAME.byteLength);
  return stream;
}

describe('store-only FlatSQL worker protocol (dashboard window model)', () => {
  it('opens a standard on demand, ingests a raw frame stream, projects it locally and clears the window', async () => {
    const store = await openWorkerStore();
    expect(activeBridge).not.toBeNull();
    expect(await store.getStats()).toEqual([]);

    await store.addStandard(OMM_SCHEMA_ENTRY);
    // Idempotent: a second add of the same standard is a no-op.
    await store.addStandard(OMM_SCHEMA_ENTRY);

    const ingested = await store.ingestFlatBufferStream('OMM', twoFrameStream(), {
      source: 'celestrak-gp',
      persist: false,
      recordKeyPrefix: 't:',
    });
    expect(ingested).toBe(2);

    const stats = await store.getStats();
    expect(stats).toHaveLength(1);
    expect(stats[0]).toMatchObject({ standardId: 'OMM', tableName: 'OMM', recordCount: 2 });
    await expect(Promise.resolve(store.listSources('OMM'))).resolves.toEqual(['celestrak-gp']);

    const projected = await store.query('SELECT OBJECT_NAME, _source FROM OMM ORDER BY _rowid ASC', 'OMM');
    expect(projected.columns).toEqual(['OBJECT_NAME', '_source']);
    expect(projected.records).toEqual([
      { OBJECT_NAME: 'STARLINK-6292', _source: 'OMM@celestrak-gp' },
      { OBJECT_NAME: 'STARLINK-6292', _source: 'OMM@celestrak-gp' },
    ]);

    await store.clearStandard('OMM', { persist: false });
    const cleared = await store.getStats();
    expect(cleared).toHaveLength(1);
    expect(cleared[0].recordCount).toBe(0);
  });

  it('refuses the sync lanes instead of pretending to serve them', async () => {
    const store = await openWorkerStore();
    await expect(
      store.syncSchema({
        standardId: 'OMM',
        schema: OMM_SCHEMA,
        backendConfig: { targetPeerId: 'peer', candidateAddrs: [] },
        initialProgress: {} as never,
        totalRows: 0,
        capBytes: 0,
        pageSize: 0,
        persistRecordInterval: 0,
        source: null,
      }),
    ).rejects.toThrow(/not available in the store-only FlatSQL worker/);
    await expect(
      store.getRemoteDataSummary({ targetPeerId: 'peer', candidateAddrs: [] }),
    ).rejects.toThrow(/not available in the store-only FlatSQL worker/);
  });
});
