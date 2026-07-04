/**
 * Loop D.5: the webUI FlatSQL worker protocol proxies the engine query
 * surface — `listSources`, `queryEpochRawStream` (the D.2 PRIMARY query
 * path) and `queryRawFlatBufferStream` — so the data explorer runs over the
 * unified per-provider store even when the store lives in a Web Worker.
 *
 * The REAL worker module (`local-flatsql.worker.ts`) is imported with a
 * stubbed `self`, and the REAL client (`createWorkerLocalFlatSqlStore`) is
 * pointed at it through a fake `Worker` bridge, so this exercises the exact
 * message protocol both sides speak in the browser.
 */
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { createLocalFlatSqlStore } from './local-flatsql';
import type { WorkerLocalFlatSqlStore } from './local-flatsql-worker-client';

const require = createRequire(import.meta.url);
const OMM_SCHEMA = readFileSync(require.resolve('spacedatastandards.org/schema/OMM/main.fbs'), 'utf8');
const STARLINK_6292_OMM_BYTES = Buffer.from('HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA', 'base64');

const OMM_SCHEMAS = [{
  standardId: 'OMM',
  tableName: 'OMM',
  fileId: '$OMM',
  schema: OMM_SCHEMA,
}];

const OMM_RECORD = {
  cid: 'celestrak-omm-1',
  schemaName: 'OMM.fbs',
  peerId: 'source:celestrak',
  providerId: 'space-data-network-02',
  sourceName: 'celestrak-gp',
  batchId: 'fixture-batch',
  timestamp: '2026-05-11T04:02:25Z',
  dataBytes: new Uint8Array(STARLINK_6292_OMM_BYTES),
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
const originalWorker = (globalThis as { Worker?: unknown }).Worker;

beforeAll(async () => {
  (globalThis as unknown as { self: WorkerScope }).self = {
    onmessage: null,
    postMessage(response: unknown) {
      queueMicrotask(() => activeBridge?.onmessage?.({ data: response }));
    },
  };
  await import('./local-flatsql.worker');
  (globalThis as { Worker?: unknown }).Worker = FakeWorkerBridge;
});

afterAll(() => {
  (globalThis as { self?: unknown }).self = originalSelf;
  (globalThis as { Worker?: unknown }).Worker = originalWorker;
});

async function openWorkerStore(): Promise<WorkerLocalFlatSqlStore> {
  const { createWorkerLocalFlatSqlStore } = await import('./local-flatsql-worker-client');
  return await createWorkerLocalFlatSqlStore({ schemas: OMM_SCHEMAS });
}

describe('FlatSQL worker protocol — engine query surface (loop D.5)', () => {
  it('proxies listSources, queryEpochRawStream and queryRawFlatBufferStream through the worker', async () => {
    const store = await openWorkerStore();
    expect(activeBridge).not.toBeNull();

    await store.ingestRecords('OMM', [OMM_RECORD], { source: 'celestrak-gp', persist: false });

    // Provider partitions visible through the proxy.
    await expect(Promise.resolve(store.listSources('OMM'))).resolves.toEqual(['celestrak-gp']);

    // PRIMARY query path (D.2) through the proxy: byte-identical to the
    // direct in-process engine store over the same contents.
    const direct = await createLocalFlatSqlStore({ schemas: OMM_SCHEMAS });
    await direct.ingestRecords('OMM', [OMM_RECORD], { source: 'celestrak-gp', persist: false });
    const queryEpochSeconds = Date.UTC(2026, 4, 12) / 1000;
    const expected = direct.queryEpochRawStream?.('OMM', { epoch: queryEpochSeconds });
    const proxied = await store.queryEpochRawStream('OMM', { epoch: queryEpochSeconds });
    expect(proxied).toBeInstanceOf(Uint8Array);
    expect(proxied.byteLength).toBeGreaterThan(0);
    expect(Buffer.from(proxied).equals(Buffer.from(expected as Uint8Array))).toBe(true);

    // Generic raw-stream mirror over a shadow partition.
    const rawStream = await store.queryRawFlatBufferStream(
      'OMM',
      'SELECT _data FROM "OMM@celestrak-gp" ORDER BY _rowid ASC',
    );
    const expectedRaw = direct.queryRawFlatBufferStream?.(
      'OMM',
      'SELECT _data FROM "OMM@celestrak-gp" ORDER BY _rowid ASC',
    );
    expect(Buffer.from(rawStream).equals(Buffer.from(expectedRaw as Uint8Array))).toBe(true);

    direct.destroy();
  });

  it('surfaces engine errors for raw-stream queries instead of falling back', async () => {
    const store = await openWorkerStore();
    await expect(
      store.queryRawFlatBufferStream('OMM', 'DELETE FROM OMM'),
    ).rejects.toThrow(/read-only/i);
  });
});
