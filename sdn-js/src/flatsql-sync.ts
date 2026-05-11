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
  op?: 'read_chunk' | 'scan' | 'open_snapshot';
  schema: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  producerPublicKey?: string;
  peerId?: string;
  cursor?: string;
  snapshotId?: string;
  head?: string;
  queryProfile?: string;
  limit?: number;
  offset?: number;
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

export function encodeFlatSqlSyncRequest(query: FlatSqlSyncQuery): Uint8Array {
  return encodeJsonFrame({
    op: query.op ?? 'read_chunk',
    schema: query.schema,
    ...(query.providerId ? { provider_id: query.providerId } : {}),
    ...(query.sourceName ? { source_name: query.sourceName } : {}),
    ...(query.batchId ? { batch_id: query.batchId } : {}),
    ...(query.producerPeerId ? { producer_peer_id: query.producerPeerId } : {}),
    ...(query.producerPublicKey ? { producer_public_key: query.producerPublicKey } : {}),
    ...(query.peerId ? { peer_id: query.peerId } : {}),
    ...(query.cursor ? { cursor: query.cursor } : {}),
    ...(query.snapshotId ? { snapshot_id: query.snapshotId } : {}),
    ...(query.head ? { head: query.head } : {}),
    ...(query.queryProfile ? { query_profile: query.queryProfile } : {}),
    ...(typeof query.limit === 'number' ? { limit: query.limit } : {}),
    ...(typeof query.offset === 'number' ? { offset: query.offset } : {}),
  });
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

  const records: Uint8Array[] = [];
  let offset = first.nextOffset;
  while (offset < bytes.byteLength) {
    const frame = readFrame(bytes, offset);
    if (!frame) break;
    records.push(frame.payload);
    offset = frame.nextOffset;
  }
  return { header, records };
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
