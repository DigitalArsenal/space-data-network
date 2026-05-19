import * as flatbuffers from 'flatbuffers';
import { DSS, DSST } from 'spacedatastandards.org/lib/js/DSS/DSS.js';
import { dssSyncState } from 'spacedatastandards.org/lib/js/DSS/dssSyncState.js';

import type { WorkerSchemaSyncProgress } from './local-flatsql-worker-client';

const dssStatusToWire = {
  idle: dssSyncState.IDLE,
  syncing: dssSyncState.SYNCING,
  synced: dssSyncState.SYNCED,
  capped: dssSyncState.CAPPED,
  error: dssSyncState.ERROR,
} as const satisfies Record<WorkerSchemaSyncProgress['status'], number>;

const dssStatusFromWire = new Map<number, WorkerSchemaSyncProgress['status']>([
  [dssSyncState.IDLE, 'idle'],
  [dssSyncState.SYNCING, 'syncing'],
  [dssSyncState.SYNCED, 'synced'],
  [dssSyncState.CAPPED, 'capped'],
  [dssSyncState.ERROR, 'error'],
]);

export function encodeWorkerSchemaSyncProgressFlatBuffer(progress: WorkerSchemaSyncProgress): Uint8Array {
  const builder = new flatbuffers.Builder(1024 + progress.verifiedChunks.length * 96);

  const offset = new DSST(
    dssStatusToWire[progress.status],
    uint64(progress.syncedRows),
    uint64(progress.totalRows),
    uint64(progress.localRows),
    uint64(progress.pinnedRows),
    uint64(progress.missingRows),
    uint64(progress.cachedBytes),
    uint64(progress.pinnedBytes),
    uint64(progress.downloadedBytes),
    uint64(progress.downloadSpeedBytesPerSecond),
    uint64(progress.measuredWireSpeedBytesPerSecond),
    progress.wireSpeedUtilization != null,
    finiteNumber(progress.wireSpeedUtilization),
    finiteNumber(progress.wireSpeedTarget),
    progress.wireSpeedTargetMet != null,
    progress.wireSpeedTargetMet === true,
    uint64(progress.manifestDiscoveryMs),
    uint64(progress.networkTransferMs),
    uint64(progress.verificationMs),
    uint64(progress.flatSqlMaterializationMs),
    stringValue(progress.providerPeerId),
    stringValue(progress.providerPublicKey),
    stringValue(progress.snapshotId),
    stringValue(progress.head),
    stringValue(progress.cursor),
    stringValue(progress.nextCursor),
    stringValue(progress.highWaterMark),
    stringValue(progress.queryProfile),
    stringValue(progress.chunkHash),
    stringValue(progress.syncProtocol),
    stringValue(progress.syncFilter),
    stringVector(progress.verifiedChunks),
    stringValue(progress.lastSyncedAt),
    stringValue(progress.error),
  ).pack(builder);

  DSS.finishDSSBuffer(builder, offset);
  return builder.asUint8Array();
}

export function decodeWorkerSchemaSyncProgressFlatBuffer(bytes: Uint8Array): WorkerSchemaSyncProgress {
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!DSS.bufferHasIdentifier(bb)) {
    throw new Error('sync progress FlatBuffer is not a DSS envelope');
  }
  const dss = DSS.getRootAsDSS(bb);
  return {
    status: statusFromWire(dss.STATUS()),
    syncedRows: uint64Number(dss.SYNCED_ROWS()),
    totalRows: uint64Number(dss.TOTAL_ROWS()),
    localRows: uint64Number(dss.LOCAL_ROWS()),
    pinnedRows: uint64Number(dss.PINNED_ROWS()),
    missingRows: uint64Number(dss.MISSING_ROWS()),
    cachedBytes: uint64Number(dss.CACHED_BYTES()),
    pinnedBytes: uint64Number(dss.PINNED_BYTES()),
    downloadedBytes: uint64Number(dss.DOWNLOADED_BYTES()),
    downloadSpeedBytesPerSecond: uint64Number(dss.DOWNLOAD_SPEED_BYTES_PER_SECOND()),
    measuredWireSpeedBytesPerSecond: uint64Number(dss.MEASURED_WIRE_SPEED_BYTES_PER_SECOND()),
    wireSpeedUtilization: dss.HAS_WIRE_SPEED_UTILIZATION() ? dss.WIRE_SPEED_UTILIZATION() : null,
    wireSpeedTarget: dss.WIRE_SPEED_TARGET(),
    wireSpeedTargetMet: dss.HAS_WIRE_SPEED_TARGET_MET() ? dss.WIRE_SPEED_TARGET_MET() : null,
    manifestDiscoveryMs: uint64Number(dss.MANIFEST_DISCOVERY_MS()),
    networkTransferMs: uint64Number(dss.NETWORK_TRANSFER_MS()),
    verificationMs: uint64Number(dss.VERIFICATION_MS()),
    flatSqlMaterializationMs: uint64Number(dss.FLATSQL_MATERIALIZATION_MS()),
    providerPeerId: stringFromFlatBufferValue(dss.PROVIDER_PEER_ID()),
    providerPublicKey: stringFromFlatBufferValue(dss.PROVIDER_PUBLIC_KEY()),
    snapshotId: stringFromFlatBufferValue(dss.SNAPSHOT_ID()),
    head: stringFromFlatBufferValue(dss.HEAD()),
    cursor: stringFromFlatBufferValue(dss.CURSOR()),
    nextCursor: stringFromFlatBufferValue(dss.NEXT_CURSOR()),
    highWaterMark: stringFromFlatBufferValue(dss.HIGH_WATER_MARK()),
    queryProfile: stringFromFlatBufferValue(dss.QUERY_PROFILE()),
    chunkHash: stringFromFlatBufferValue(dss.CHUNK_HASH()),
    syncProtocol: stringFromFlatBufferValue(dss.SYNC_PROTOCOL()),
    syncFilter: stringFromFlatBufferValue(dss.SYNC_FILTER()),
    verifiedChunks: stringVectorFromDss(dss),
    lastSyncedAt: stringFromFlatBufferValue(dss.LAST_SYNCED_AT()),
    error: stringFromFlatBufferValue(dss.ERROR()),
  };
}

function stringValue(value: string | null | undefined): string | null {
  const text = value?.trim();
  return text ? text : null;
}

function stringVector(values: string[]): string[] {
  return values.map((value) => value.trim()).filter(Boolean);
}

function uint64Number(value: bigint): number {
  if (value > BigInt(Number.MAX_SAFE_INTEGER)) return Number.MAX_SAFE_INTEGER;
  return Number(value);
}

function stringFromFlatBufferValue(value: string | Uint8Array | null): string | null {
  if (typeof value === 'string') return value.trim() || null;
  if (value instanceof Uint8Array) {
    const decoded = new TextDecoder().decode(value).trim();
    return decoded || null;
  }
  return null;
}

function statusFromWire(value: number): WorkerSchemaSyncProgress['status'] {
  return dssStatusFromWire.get(value) ?? 'idle';
}

function finiteNumber(value: number | null | undefined): number {
  const numeric = Number(value);
  return Number.isFinite(numeric) ? numeric : 0;
}

function uint64(value: number | null | undefined): bigint {
  const numeric = Math.floor(Number(value));
  return BigInt(Number.isFinite(numeric) && numeric > 0 ? numeric : 0);
}

function stringVectorFromDss(dss: DSS): string[] {
  const values: string[] = [];
  for (let index = 0; index < dss.verifiedChunksLength(); index += 1) {
    const value = stringFromFlatBufferValue(dss.VERIFIED_CHUNKS(index));
    if (value) values.push(value);
  }
  return values;
}
