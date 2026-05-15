import { readFileSync } from 'node:fs';
import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';

import { DSS } from '../../../../../spacedatastandards.org/lib/js/DSS/DSS.js';
import {
  decodeWorkerSchemaSyncProgressFlatBuffer,
  encodeWorkerSchemaSyncProgressFlatBuffer,
} from './worker-sync-status-flatbuffer';
import type { WorkerSchemaSyncProgress } from './local-flatsql-worker-client';

const workerClientSource = readFileSync(new URL('./local-flatsql-worker-client.ts', import.meta.url), 'utf8');
const workerSource = readFileSync(new URL('./local-flatsql.worker.ts', import.meta.url), 'utf8');
const dssSchema = readFileSync(new URL('../../../../../spacedatastandards.org/schema/DSS/main.fbs', import.meta.url), 'utf8');

describe('FlatSQL worker sync status transport', () => {
  it('sends sync status over the worker boundary as FlatBuffer bytes', () => {
    const codecSource = readFileSync(new URL('./worker-sync-status-flatbuffer.ts', import.meta.url), 'utf8');
    expect(codecSource).toContain("spacedatastandards.org/lib/js/DSS/DSS.js");
    expect(codecSource).toContain("spacedatastandards.org/lib/js/DSS/dssSyncState.js");
    expect(codecSource).toContain('DSS.finishDSSBuffer');
    expect(codecSource).toContain('DSS.getRootAsDSS');
    expect(codecSource).not.toContain('DSS_FIELD_COUNT');
    expect(codecSource).not.toContain('function readOffset');

    expect(workerSource).toContain('encodeWorkerSchemaSyncProgressFlatBuffer');
    expect(workerSource).toContain('progressBytes');
    expect(workerSource).not.toContain("type: 'syncProgress', progress, stats");

    expect(workerClientSource).toContain('decodeWorkerSchemaSyncProgressFlatBuffer');
    expect(workerClientSource).toContain('progressBytes: Uint8Array');
    expect(workerClientSource).not.toContain("progress: WorkerSchemaSyncProgress; stats");
    expect(workerClientSource).not.toContain('progress: response.progress');
  });

  it('round-trips worker progress through the SDS DSS FlatBuffer envelope', () => {
    expect(dssSchema).toContain('table DSS');
    expect(dssSchema).toContain('file_identifier "$DSS"');

    const progress: WorkerSchemaSyncProgress = {
      status: 'syncing',
      syncedRows: 42,
      totalRows: 100,
      localRows: 40,
      pinnedRows: 40,
      missingRows: 60,
      cachedBytes: 1024,
      pinnedBytes: 900,
      downloadedBytes: 4096,
      downloadSpeedBytesPerSecond: 2048,
      measuredWireSpeedBytesPerSecond: 8192,
      wireSpeedUtilization: 0.25,
      wireSpeedTarget: 0.8,
      wireSpeedTargetMet: false,
      manifestDiscoveryMs: 12,
      networkTransferMs: 34,
      verificationMs: 56,
      flatSqlMaterializationMs: 78,
      providerPeerId: '16Uiu2HProvider',
      providerPublicKey: 'provider-public-key',
      snapshotId: 'snapshot-1',
      head: 'head-1',
      cursor: 'cursor-1',
      nextCursor: 'cursor-2',
      highWaterMark: '2026-05-15T00:00:00Z',
      queryProfile: 'dataset-publication-offset-v1',
      chunkHash: 'chunk-hash',
      syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
      syncFilter: 'EPOCH >= 2026-05-15',
      verifiedChunks: ['cid-a', 'cid-b'],
      lastSyncedAt: '2026-05-15T01:02:03Z',
      error: null,
    };

    const bytes = encodeWorkerSchemaSyncProgressFlatBuffer(progress);

    expect(String.fromCharCode(...bytes.slice(4, 8))).toBe('$DSS');
    const generatedDss = DSS.getRootAsDSS(new flatbuffers.ByteBuffer(bytes));
    expect(Number(generatedDss.SYNCED_ROWS())).toBe(42);
    expect(generatedDss.PROVIDER_PEER_ID()).toBe('16Uiu2HProvider');
    expect(generatedDss.verifiedChunksLength()).toBe(2);
    expect(generatedDss.VERIFIED_CHUNKS(1)).toBe('cid-b');
    expect(decodeWorkerSchemaSyncProgressFlatBuffer(bytes)).toEqual(progress);
  });
});
