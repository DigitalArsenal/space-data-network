export const FLATSQL_SYNC_PROTOCOL_ID = '/space-data-network/flatsql-sync/1.0.0';

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

export interface FlatSqlSyncTransport {
  dialProtocol(
    targetPeerId: string,
    protocolId: string,
    payload: Uint8Array,
    candidateAddrs?: string[],
  ): Promise<Uint8Array>;
}

export interface FlatSqlSyncQuery {
  targetPeerId: string;
  candidateAddrs?: string[];
  op?: 'read_chunk' | 'scan' | 'open_snapshot' | 'open_manifest' | 'list_datastores';
  schema: string;
  datastoreKey?: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  producerPublicKey?: string;
  peerId?: string;
  cursor?: string;
  snapshotId?: string;
  head?: string;
  nextCursor?: string;
  totalCount?: number;
  highWaterMark?: string;
  scanHash?: string;
  chunkHash?: string;
  queryProfile?: string;
  limit?: number;
  offset?: number;
  records?: FlatSqlSyncRecordRef[];
}

export interface FlatSqlWireSpeedProbeQuery {
  targetPeerId: string;
  candidateAddrs?: string[];
  probeBytes?: number;
}

export interface FlatSqlWireSpeedProbeResult {
  requestedBytes: number;
  payloadBytes: number;
  elapsedMs: number;
  bytesPerSecond: number;
  syncProtocol: string;
}

export interface FlatSqlSyncDatastoreIdentity {
  schemaName: string;
  sourcePeerId?: string;
  sourcePublicKey?: string;
  providerId?: string;
  sourceName?: string;
  batchHead?: string;
  queryProfile?: string;
  canonicalParams?: Record<string, string>;
  snapshotId?: string;
  highWaterMark?: string;
  artifactHash?: string;
}

export interface FlatSqlSyncDatastoreEntry {
  key: string;
  identity: FlatSqlSyncDatastoreIdentity;
  updatedAt: number;
}

export interface FlatSqlSyncDatastoreList {
  count: number;
  results: FlatSqlSyncDatastoreEntry[];
}

export interface FlatSqlSyncHeader {
  schema: string;
  totalCount: number;
  count: number;
  limit: number;
  offset: number;
  cursor: string;
  nextCursor: string;
  snapshotId: string;
  head: string;
  highWaterMark: string;
  scanHash: string;
  chunkHash: string;
  queryProfile: string;
  syncProtocol: string;
  maxChunkSize: number;
  transports: string[];
  results: FlatSqlSyncRecordRef[];
}

export interface FlatSqlSyncManifest {
  manifestId: string;
  schema: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  producerPublicKey?: string;
  totalCount: number;
  totalBytes: number;
  snapshotId: string;
  head: string;
  highWaterMark: string;
  queryProfile: string;
  syncProtocol: string;
  maxChunkSize: number;
  transports: string[];
  segments: FlatSqlSyncManifestSegment[];
}

export interface FlatSqlSyncManifestSegment {
  index: number;
  cursor: string;
  nextCursor: string;
  rowCount: number;
  byteCount: number;
  chunkHash: string;
  cid?: string;
  indexCid?: string;
  manifestCid?: string;
  pnmCid?: string;
  shardSha256?: string;
  indexSha256?: string;
  querySha256?: string;
  feedSequence?: number;
  previousHead?: string;
  feedHead?: string;
}

export interface FlatSqlSyncRecordRef {
  schemaName: string;
  cid: string;
  peerId: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  producerPublicKey?: string;
  timestamp?: string;
  sizeBytes: number;
}

export interface FlatSqlSyncChunk {
  header: FlatSqlSyncHeader;
  records: Uint8Array[];
  recordStream: Uint8Array;
}

export async function requestFlatSqlSyncChunk(
  transport: FlatSqlSyncTransport,
  query: FlatSqlSyncQuery,
): Promise<FlatSqlSyncChunk> {
  const response = await transport.dialProtocol(
    query.targetPeerId,
    FLATSQL_SYNC_PROTOCOL_ID,
    encodeFlatSqlSyncRequest(query),
    query.candidateAddrs,
  );
  return decodeFlatSqlSyncChunk(response);
}

export async function requestFlatSqlSyncManifest(
  transport: FlatSqlSyncTransport,
  query: FlatSqlSyncQuery,
): Promise<FlatSqlSyncManifest> {
  const response = await transport.dialProtocol(
    query.targetPeerId,
    FLATSQL_SYNC_PROTOCOL_ID,
    encodeFlatSqlSyncRequest({ ...query, op: 'open_manifest' }),
    query.candidateAddrs,
  );
  return decodeFlatSqlSyncManifest(response);
}

export async function requestFlatSqlSyncDatastores(
  transport: FlatSqlSyncTransport,
  query: Pick<FlatSqlSyncQuery, 'targetPeerId' | 'candidateAddrs'>,
): Promise<FlatSqlSyncDatastoreList> {
  const response = await transport.dialProtocol(
    query.targetPeerId,
    FLATSQL_SYNC_PROTOCOL_ID,
    encodeFlatSqlSyncRequest({ ...query, op: 'list_datastores', schema: '*' }),
    query.candidateAddrs,
  );
  return decodeFlatSqlSyncDatastores(response);
}

export async function requestFlatSqlWireSpeedProbe(
  transport: FlatSqlSyncTransport,
  query: FlatSqlWireSpeedProbeQuery,
  now: () => number = monotonicNow,
): Promise<FlatSqlWireSpeedProbeResult> {
  const startedAtMs = now();
  const response = await transport.dialProtocol(
    query.targetPeerId,
    FLATSQL_SYNC_PROTOCOL_ID,
    encodeFlatSqlWireSpeedProbeRequest(query),
    query.candidateAddrs,
  );
  const elapsedMs = Math.max(1, now() - startedAtMs);
  return decodeFlatSqlWireSpeedProbe(response, elapsedMs);
}

export function encodeFlatSqlSyncRequest(query: FlatSqlSyncQuery): Uint8Array {
  return encodeJsonFrame({
    op: query.op ?? 'read_chunk',
    schema: query.schema,
    ...(query.datastoreKey ? { datastore_key: query.datastoreKey } : {}),
    ...(query.providerId ? { provider_id: query.providerId } : {}),
    ...(query.sourceName ? { source_name: query.sourceName } : {}),
    ...(query.batchId ? { batch_id: query.batchId } : {}),
    ...(query.producerPeerId ? { producer_peer_id: query.producerPeerId } : {}),
    ...(query.producerPublicKey ? { producer_public_key: query.producerPublicKey } : {}),
    ...(query.peerId ? { peer_id: query.peerId } : {}),
    ...(query.cursor ? { cursor: query.cursor } : {}),
    ...(query.snapshotId ? { snapshot_id: query.snapshotId } : {}),
    ...(query.head ? { head: query.head } : {}),
    ...(query.nextCursor ? { next_cursor: query.nextCursor } : {}),
    ...(typeof query.totalCount === 'number' ? { total_count: query.totalCount } : {}),
    ...(query.highWaterMark ? { high_water_mark: query.highWaterMark } : {}),
    ...(query.scanHash ? { scan_hash: query.scanHash } : {}),
    ...(query.chunkHash ? { chunk_hash: query.chunkHash } : {}),
    ...(query.queryProfile ? { query_profile: query.queryProfile } : {}),
    ...(typeof query.limit === 'number' ? { limit: query.limit } : {}),
    ...(typeof query.offset === 'number' ? { offset: query.offset } : {}),
    ...(query.records ? { records: query.records.map(flatSqlSyncRecordRefPayload) } : {}),
  });
}

export function encodeFlatSqlWireSpeedProbeRequest(query: FlatSqlWireSpeedProbeQuery): Uint8Array {
  return encodeJsonFrame({
    op: 'wire_speed_probe',
    ...(typeof query.probeBytes === 'number' ? { probe_bytes: Math.max(1, Math.floor(query.probeBytes)) } : {}),
  });
}

function flatSqlSyncRecordRefPayload(record: FlatSqlSyncRecordRef): Record<string, unknown> {
  return {
    schema_name: record.schemaName,
    cid: record.cid,
    peer_id: record.peerId,
    ...(record.providerId ? { provider_id: record.providerId } : {}),
    ...(record.sourceName ? { source_name: record.sourceName } : {}),
    ...(record.batchId ? { batch_id: record.batchId } : {}),
    ...(record.producerPeerId ? { producer_peer_id: record.producerPeerId } : {}),
    ...(record.producerPublicKey ? { producer_public_key: record.producerPublicKey } : {}),
    ...(record.timestamp ? { timestamp: record.timestamp } : {}),
    ...(typeof record.sizeBytes === 'number' ? { size_bytes: record.sizeBytes } : {}),
  };
}

export function decodeFlatSqlSyncChunk(bytes: Uint8Array): FlatSqlSyncChunk {
  const first = readFrame(bytes, 0);
  if (!first) throw new Error('missing FlatSQL sync header frame');
  const headerPayload = JSON.parse(textDecoder.decode(first.payload)) as unknown;
  const header = normalizeFlatSqlSyncHeader(headerPayload);
  if (header.syncProtocol && header.syncProtocol !== FLATSQL_SYNC_PROTOCOL_ID) {
    throw new Error(`unexpected FlatSQL sync protocol ${header.syncProtocol}`);
  }
  const status = readString(headerPayload, 'status');
  if (status === 'error') {
    const message = readNestedString(headerPayload, 'error', 'message') ?? 'FlatSQL sync request failed';
    throw new Error(message);
  }

  const recordStream = bytes.slice(first.nextOffset);
  const records = decodeFlatSqlSizePrefixedStream(recordStream);
  return { header, records, recordStream };
}

export function decodeFlatSqlSyncManifest(bytes: Uint8Array): FlatSqlSyncManifest {
  const first = readFrame(bytes, 0);
  if (!first) throw new Error('missing FlatSQL sync manifest frame');
  const payload = JSON.parse(textDecoder.decode(first.payload)) as unknown;
  throwIfProtocolError(payload);
  const manifest = normalizeFlatSqlSyncManifest(payload);
  if (manifest.syncProtocol && manifest.syncProtocol !== FLATSQL_SYNC_PROTOCOL_ID) {
    throw new Error(`unexpected FlatSQL sync protocol ${manifest.syncProtocol}`);
  }
  return manifest;
}

export function decodeFlatSqlSyncDatastores(bytes: Uint8Array): FlatSqlSyncDatastoreList {
  const first = readFrame(bytes, 0);
  if (!first) throw new Error('missing FlatSQL sync datastore list frame');
  const payload = JSON.parse(textDecoder.decode(first.payload)) as unknown;
  throwIfProtocolError(payload);
  return normalizeFlatSqlSyncDatastores(payload);
}

export function decodeFlatSqlWireSpeedProbe(bytes: Uint8Array, elapsedMs: number): FlatSqlWireSpeedProbeResult {
  const first = readFrame(bytes, 0);
  if (!first) throw new Error('missing FlatSQL sync wire-speed probe header frame');
  const payload = JSON.parse(textDecoder.decode(first.payload)) as unknown;
  throwIfProtocolError(payload);
  const syncProtocol = readString(payload, 'sync_protocol', 'syncProtocol') ?? '';
  if (syncProtocol && syncProtocol !== FLATSQL_SYNC_PROTOCOL_ID) {
    throw new Error(`unexpected FlatSQL sync protocol ${syncProtocol}`);
  }
  const payloadBytes = bytes.byteLength - first.nextOffset;
  const expectedPayloadBytes = readNumber(payload, 'payload_bytes', 'payloadBytes') ?? payloadBytes;
  if (expectedPayloadBytes !== payloadBytes) {
    throw new Error(`wire-speed probe returned ${payloadBytes}/${expectedPayloadBytes} bytes`);
  }
  const requestedBytes = readNumber(payload, 'probe_bytes', 'probeBytes') ?? payloadBytes;
  const normalizedElapsedMs = Math.max(1, elapsedMs);
  return {
    requestedBytes,
    payloadBytes,
    elapsedMs: normalizedElapsedMs,
    bytesPerSecond: Math.floor(payloadBytes / (normalizedElapsedMs / 1000)),
    syncProtocol,
  };
}

function encodeJsonFrame(payload: Record<string, unknown>): Uint8Array {
  const data = textEncoder.encode(JSON.stringify(payload));
  const frame = new Uint8Array(4 + data.byteLength);
  new DataView(frame.buffer).setUint32(0, data.byteLength, false);
  frame.set(data, 4);
  return frame;
}

function readFrame(bytes: Uint8Array, offset: number): { payload: Uint8Array; nextOffset: number } | null {
  if (offset === bytes.byteLength) return null;
  if (offset + 4 > bytes.byteLength) throw new Error('truncated FlatSQL sync frame header');
  const length = new DataView(bytes.buffer, bytes.byteOffset + offset, 4).getUint32(0, false);
  const payloadOffset = offset + 4;
  const nextOffset = payloadOffset + length;
  if (nextOffset > bytes.byteLength) throw new Error('truncated FlatSQL sync frame payload');
  return { payload: bytes.slice(payloadOffset, nextOffset), nextOffset };
}

function decodeFlatSqlSizePrefixedStream(streamBytes: Uint8Array): Uint8Array[] {
  const view = new DataView(streamBytes.buffer, streamBytes.byteOffset, streamBytes.byteLength);
  const records: Uint8Array[] = [];
  let offset = 0;
  let index = 0;
  while (offset < streamBytes.byteLength) {
    if (streamBytes.byteLength - offset < 4) {
      throw new Error(`truncated FlatSQL size-prefixed stream header at offset ${offset}`);
    }
    const length = view.getUint32(offset, true);
    offset += 4;
    const nextOffset = offset + length;
    if (nextOffset > streamBytes.byteLength) {
      throw new Error(`truncated FlatSQL size-prefixed stream payload at index ${index}`);
    }
    records.push(streamBytes.slice(offset, nextOffset));
    offset = nextOffset;
    index += 1;
  }
  return records;
}

function normalizeFlatSqlSyncHeader(payload: unknown): FlatSqlSyncHeader {
  const record = isRecord(payload) ? payload : {};
  const results = Array.isArray(record.results) ? record.results : [];
  return {
    schema: readString(record, 'schema') ?? 'unknown',
    totalCount: readNumber(record, 'total_count', 'totalCount') ?? 0,
    count: readNumber(record, 'count') ?? 0,
    limit: readNumber(record, 'limit') ?? 0,
    offset: readNumber(record, 'offset') ?? 0,
    cursor: readString(record, 'cursor') ?? '',
    nextCursor: readString(record, 'next_cursor', 'nextCursor') ?? '',
    snapshotId: readString(record, 'snapshot_id', 'snapshotId') ?? '',
    head: readString(record, 'head') ?? '',
    highWaterMark: readString(record, 'high_water_mark', 'highWaterMark') ?? '',
    scanHash: readString(record, 'scan_hash', 'scanHash') ?? '',
    chunkHash: readString(record, 'chunk_hash', 'chunkHash') ?? '',
    queryProfile: readString(record, 'query_profile', 'queryProfile') ?? '',
    syncProtocol: readString(record, 'sync_protocol', 'syncProtocol') ?? '',
    maxChunkSize: readNumber(record, 'max_chunk_size', 'maxChunkSize') ?? 0,
    transports: readStringArray(record.transports),
    results: results.filter(isRecord).map(normalizeFlatSqlSyncRecordRef),
  };
}

function normalizeFlatSqlSyncManifest(payload: unknown): FlatSqlSyncManifest {
  const record = isRecord(payload) ? payload : {};
  const segments = Array.isArray(record.segments) ? record.segments : [];
  return {
    manifestId: readString(record, 'manifest_id', 'manifestId') ?? '',
    schema: readString(record, 'schema') ?? 'unknown',
    providerId: readString(record, 'provider_id', 'providerId') ?? undefined,
    sourceName: readString(record, 'source_name', 'sourceName') ?? undefined,
    batchId: readString(record, 'batch_id', 'batchId') ?? undefined,
    producerPeerId: readString(record, 'producer_peer_id', 'producerPeerId') ?? undefined,
    producerPublicKey: readString(record, 'producer_public_key', 'producerPublicKey') ?? undefined,
    totalCount: readNumber(record, 'total_count', 'totalCount') ?? 0,
    totalBytes: readNumber(record, 'total_bytes', 'totalBytes') ?? 0,
    snapshotId: readString(record, 'snapshot_id', 'snapshotId') ?? '',
    head: readString(record, 'head') ?? '',
    highWaterMark: readString(record, 'high_water_mark', 'highWaterMark') ?? '',
    queryProfile: readString(record, 'query_profile', 'queryProfile') ?? '',
    syncProtocol: readString(record, 'sync_protocol', 'syncProtocol') ?? '',
    maxChunkSize: readNumber(record, 'max_chunk_size', 'maxChunkSize') ?? 0,
    transports: readStringArray(record.transports),
    segments: segments.filter(isRecord).map(normalizeFlatSqlSyncManifestSegment),
  };
}

function normalizeFlatSqlSyncDatastores(payload: unknown): FlatSqlSyncDatastoreList {
  const record = isRecord(payload) ? payload : {};
  const results = Array.isArray(record.results) ? record.results : [];
  return {
    count: readNumber(record, 'count') ?? results.length,
    results: results.filter(isRecord).map((entry): FlatSqlSyncDatastoreEntry => {
      const identity = isRecord(entry.identity) ? entry.identity : {};
      return {
        key: readString(entry, 'key') ?? '',
        updatedAt: readNumber(entry, 'updated_at', 'updatedAt') ?? 0,
        identity: {
          schemaName: readString(identity, 'schema_name', 'schemaName') ?? 'unknown',
          sourcePeerId: readString(identity, 'source_peer_id', 'sourcePeerId') ?? undefined,
          sourcePublicKey: readString(identity, 'source_public_key', 'sourcePublicKey') ?? undefined,
          providerId: readString(identity, 'provider_id', 'providerId') ?? undefined,
          sourceName: readString(identity, 'source_name', 'sourceName') ?? undefined,
          batchHead: readString(identity, 'batch_head', 'batchHead') ?? undefined,
          queryProfile: readString(identity, 'query_profile', 'queryProfile') ?? undefined,
          canonicalParams: normalizeStringRecord(identity.canonical_params ?? identity.canonicalParams),
          snapshotId: readString(identity, 'snapshot_id', 'snapshotId') ?? undefined,
          highWaterMark: readString(identity, 'high_water_mark', 'highWaterMark') ?? undefined,
          artifactHash: readString(identity, 'artifact_hash', 'artifactHash') ?? undefined,
        },
      };
    }).filter((entry) => entry.key && entry.identity.schemaName !== 'unknown'),
  };
}

function normalizeFlatSqlSyncManifestSegment(record: Record<string, unknown>): FlatSqlSyncManifestSegment {
  return {
    index: readNumber(record, 'index') ?? 0,
    cursor: readString(record, 'cursor') ?? '',
    nextCursor: readString(record, 'next_cursor', 'nextCursor') ?? '',
    rowCount: readNumber(record, 'row_count', 'rowCount') ?? 0,
    byteCount: readNumber(record, 'byte_count', 'byteCount') ?? 0,
    chunkHash: readString(record, 'chunk_hash', 'chunkHash') ?? '',
    cid: readString(record, 'cid') ?? undefined,
    indexCid: readString(record, 'index_cid', 'indexCid') ?? undefined,
    manifestCid: readString(record, 'manifest_cid', 'manifestCid') ?? undefined,
    pnmCid: readString(record, 'pnm_cid', 'pnmCid') ?? undefined,
    shardSha256: readString(record, 'shard_sha256', 'shardSha256') ?? undefined,
    indexSha256: readString(record, 'index_sha256', 'indexSha256') ?? undefined,
    querySha256: readString(record, 'query_sha256', 'querySha256') ?? undefined,
    feedSequence: readNumber(record, 'feed_sequence', 'feedSequence') ?? undefined,
    previousHead: readString(record, 'previous_head', 'previousHead') ?? undefined,
    feedHead: readString(record, 'feed_head', 'feedHead') ?? undefined,
  };
}

function throwIfProtocolError(payload: unknown): void {
  const status = readString(payload, 'status');
  if (status === 'error') {
    const message = readNestedString(payload, 'error', 'message') ?? 'FlatSQL sync request failed';
    throw new Error(message);
  }
}

function normalizeFlatSqlSyncRecordRef(record: Record<string, unknown>): FlatSqlSyncRecordRef {
  return {
    schemaName: readString(record, 'schema_name', 'schemaName') ?? 'unknown',
    cid: readString(record, 'cid', 'id') ?? '',
    peerId: readString(record, 'peer_id', 'peerId') ?? '',
    providerId: readString(record, 'provider_id', 'providerId') ?? undefined,
    sourceName: readString(record, 'source_name', 'sourceName') ?? undefined,
    batchId: readString(record, 'batch_id', 'batchId') ?? undefined,
    producerPeerId: readString(record, 'producer_peer_id', 'producerPeerId') ?? undefined,
    producerPublicKey: readString(record, 'producer_public_key', 'producerPublicKey') ?? undefined,
    timestamp: readString(record, 'timestamp') ?? undefined,
    sizeBytes: readNumber(record, 'size_bytes', 'sizeBytes') ?? 0,
  };
}

function readStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0);
}

function normalizeStringRecord(value: unknown): Record<string, string> | undefined {
  if (!isRecord(value)) return undefined;
  const out: Record<string, string> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (typeof entry === 'string') out[key] = entry;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function readNestedString(payload: unknown, parent: string, key: string): string | null {
  if (!isRecord(payload)) return null;
  const parentValue = payload[parent];
  return isRecord(parentValue) ? readString(parentValue, key) : null;
}

function readString(payload: unknown, ...keys: string[]): string | null {
  if (!isRecord(payload)) return null;
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === 'string' && value.trim().length > 0) return value.trim();
  }
  return null;
}

function readNumber(payload: unknown, ...keys: string[]): number | null {
  if (!isRecord(payload)) return null;
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string') {
      const parsed = Number.parseFloat(value);
      if (Number.isFinite(parsed)) return parsed;
    }
  }
  return null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function monotonicNow(): number {
  return globalThis.performance?.now?.() ?? Date.now();
}
