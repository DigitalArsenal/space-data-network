/**
 * flatsql-sync 2.0.0 client: chunked, resumable FlatSQL record and dataset
 * publication reads over a libp2p stream (sdn-server internal/syncwire and
 * internal/protocol/flatsql_sync.go are the wire's source of truth).
 *
 * One libp2p protocol ID per op ({@link flatSqlSyncProtocolId}); multistream
 * select is the op selector. Every frame is an SDS size-prefixed FlatBuffer
 * (u32 LITTLE-endian length + root). The request is exactly one
 * $QRP{KIND=Request} frame; every reply begins with a $QRP Page or a $QRP
 * Error frame (ERROR_CODE, MESSAGE, RETRY_AFTER_MS, HTTP_STATUS).
 *
 *   read-chunk        runs of [$QRP Page][RECORD_COUNT record frames] (== POST /api/v1/data/scan)
 *   open-manifest     one $QRP Page of SEGMENT_COLUMNS rows (ROLE segment | shard-group-car)
 *   list-published    one $QRP Page of SEGMENT_COLUMNS rows (one per publication)
 *   read-shard        one $QRP Page (one row: BYTE_OFFSET, BYTE_LENGTH, BYTE_COUNT) + BYTE_LENGTH raw bytes
 *   read-shard-batch  one $QRP Page (one row per shard, in order) + the shards' bytes concatenated
 *   wire-speed-probe  one $QRP Page (MAX_BYTES = payload) + that many filler bytes
 *   list-datastores   one $QRP Page of DATASTORE_COLUMNS rows
 *
 * Record frames are returned byte-identical: {@link FlatSqlSyncRun.stream}
 * is a zero-copy view of the run's size-prefixed frames.
 */
import * as flatbuffers from 'flatbuffers';
import { CID } from 'multiformats/cid';
import * as raw from 'multiformats/codecs/raw';
import { sha256 } from 'multiformats/hashes/sha2';
import { QRP } from 'spacedatastandards.org/lib/js/QRP/QRP.js';
import { QRPColumn } from 'spacedatastandards.org/lib/js/QRP/QRPColumn.js';
import { QRPFilter } from 'spacedatastandards.org/lib/js/QRP/QRPFilter.js';
import { QRPRow } from 'spacedatastandards.org/lib/js/QRP/QRPRow.js';
import { QRPValue } from 'spacedatastandards.org/lib/js/QRP/QRPValue.js';
import { qrpFilterOp } from 'spacedatastandards.org/lib/js/QRP/qrpFilterOp.js';
import { qrpKind } from 'spacedatastandards.org/lib/js/QRP/qrpKind.js';
import { qrpStatus } from 'spacedatastandards.org/lib/js/QRP/qrpStatus.js';
import { qrpValueKind } from 'spacedatastandards.org/lib/js/QRP/qrpValueKind.js';

/** Every op's protocol ID is this prefix + the op. 1.0.0 is registered nowhere. */
export const FLATSQL_SYNC_PROTOCOL_PREFIX = '/space-data-network/flatsql-sync/2.0.0/';

export const FLATSQL_SYNC_OPS = [
  'read-chunk',
  'open-manifest',
  'list-published',
  'read-shard',
  'read-index',
  'read-shard-batch',
  'wire-speed-probe',
  'list-datastores',
] as const;

export type FlatSqlSyncOp = (typeof FLATSQL_SYNC_OPS)[number];

/** The libp2p protocol ID of one op. */
export function flatSqlSyncProtocolId(op: FlatSqlSyncOp): string {
  return FLATSQL_SYNC_PROTOCOL_PREFIX + op;
}

export const FLATSQL_SYNC_READ_CHUNK_PROTOCOL_ID = flatSqlSyncProtocolId('read-chunk');
export const FLATSQL_SYNC_OPEN_MANIFEST_PROTOCOL_ID = flatSqlSyncProtocolId('open-manifest');
export const FLATSQL_SYNC_LIST_PUBLISHED_PROTOCOL_ID = flatSqlSyncProtocolId('list-published');
export const FLATSQL_SYNC_READ_SHARD_PROTOCOL_ID = flatSqlSyncProtocolId('read-shard');
export const FLATSQL_SYNC_READ_INDEX_PROTOCOL_ID = flatSqlSyncProtocolId('read-index');
export const FLATSQL_SYNC_READ_SHARD_BATCH_PROTOCOL_ID = flatSqlSyncProtocolId('read-shard-batch');
export const FLATSQL_SYNC_WIRE_SPEED_PROBE_PROTOCOL_ID = flatSqlSyncProtocolId('wire-speed-probe');
export const FLATSQL_SYNC_LIST_DATASTORES_PROTOCOL_ID = flatSqlSyncProtocolId('list-datastores');

/** Every op's protocol ID, in registration order. */
export const FLATSQL_SYNC_PROTOCOL_IDS: readonly string[] = FLATSQL_SYNC_OPS.map(flatSqlSyncProtocolId);

/** FILTERS fields a request may carry (Eq, except SHARD_CID which is In "a,b,c"). */
export const FLATSQL_SYNC_FILTER_QUERY_PROFILE = 'QUERY_PROFILE';
export const FLATSQL_SYNC_FILTER_DATASTORE_KEY = 'DATASTORE_KEY';
export const FLATSQL_SYNC_FILTER_SHARD_CID = 'SHARD_CID';

/**
 * SEGMENT_COLUMNS: THE column set of every segment page (Go: syncwire
 * segmentColumns), in wire order. Readers look cells up by NAME.
 */
export const FLATSQL_SYNC_SEGMENT_COLUMNS = [
  ['SCHEMA_NAME', 'string'], ['PROVIDER_ID', 'string'], ['SOURCE_NAME', 'string'], ['BATCH_ID', 'string'],
  ['QUERY_PROFILE', 'string'], ['ROLE', 'string'], ['SEGMENT_INDEX', 'int64'], ['OFFSET', 'int64'],
  ['LIMIT', 'int64'], ['CURSOR', 'string'], ['NEXT_CURSOR', 'string'], ['RECORD_COUNT', 'int64'],
  ['BYTE_COUNT', 'int64'], ['BYTE_OFFSET', 'int64'], ['BYTE_LENGTH', 'int64'], ['CHUNK_HASH', 'string'],
  ['SHARD_CID', 'string'], ['INDEX_CID', 'string'], ['MANIFEST_CID', 'string'], ['PNM_CID', 'string'],
  ['SHARD_SHA256', 'string'], ['INDEX_SHA256', 'string'], ['QUERY_SHA256', 'string'], ['RESULT_SHA256', 'string'],
  ['FEED_SEQUENCE', 'int64'], ['PREVIOUS_HEAD', 'string'], ['FEED_HEAD', 'string'], ['PUBLISHED_AT_MS', 'int64'],
  ['SIGNATURE', 'bytes'], ['SIGNATURE_TYPE', 'string'], ['SIGNING_PUBLIC_KEY', 'bytes'],
] as const;

export interface FlatSqlSyncTransport {
  dialProtocol(
    targetPeerId: string,
    protocolId: string,
    payload: Uint8Array,
    candidateAddrs?: string[],
  ): Promise<Uint8Array>;
}

/** A request's target peer and lane. Which fields an op reads is listed per op. */
export interface FlatSqlSyncQuery {
  targetPeerId: string;
  candidateAddrs?: string[];
  schema: string;
  /** A registered datastore other than the node's own (private-read authority). */
  datastoreKey?: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  /** Storing peer id ($QRP ORIGIN_ID). */
  originId?: string;
  queryProfile?: string;
  search?: string;
  /** read-chunk: node-issued page cursor; list-published: node-issued offset. */
  cursor?: string;
  /** read-chunk: the RESUME_CURSOR of an earlier complete walk of this query. */
  resumeCursor?: string;
  limit?: number;
  /** read-shard: the shard CID. */
  cid?: string;
  /** read-shard-batch: the shard CIDs, answered in this order. */
  cids?: string[];
  /** read-shard: byte range start. */
  byteOffset?: number;
  /** read-shard: byte range length (0 = to the end). */
  byteLength?: number;
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
}

/** A $QRP{KIND=Error} answer. */
export class FlatSqlSyncError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly status: qrpStatus,
    readonly retryAfterMs: number,
    readonly httpStatus: number,
  ) {
    super(message);
    this.name = 'FlatSqlSyncError';
  }
}

/** One record's lane, from its run header, and its store CID. */
export interface FlatSqlSyncRecordRef {
  schemaName: string;
  /** CIDv1 (raw, sha2-256) of the record bytes: the store's record id. */
  cid: string;
  /** Storing peer id (the run's ORIGIN_ID). */
  peerId: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  sizeBytes: number;
}

/** One read-chunk page, from its first run header. */
export interface FlatSqlSyncHeader {
  schema: string;
  fileIdentifier: string;
  totalCount: number;
  /** Records in this page (all runs). */
  count: number;
  limit: number;
  cursor: string;
  /** Empty on the last page. */
  nextCursor: string;
  /** Set on the last page: resumes this query after this walk. */
  resumeCursor: string;
  /** Page chunk hash ($QRP ETAG). */
  chunkHash: string;
  /** One ref per record, in page order. */
  results: FlatSqlSyncRecordRef[];
}

/** One run: consecutive records of one lane. */
export interface FlatSqlSyncRun {
  providerId: string;
  sourceName: string;
  batchId: string;
  producerPeerId: string;
  originId: string;
  /** The run's record frames, size-prefixed, byte-identical (a view into the reply). */
  stream: Uint8Array;
  /** Each record's bytes without its prefix (views into the reply). */
  records: Uint8Array[];
}

export interface FlatSqlSyncChunk {
  header: FlatSqlSyncHeader;
  runs: FlatSqlSyncRun[];
  /** Every record's bytes without its prefix, in page order. */
  records: Uint8Array[];
  /** Every record frame, size-prefixed, in page order (a view when the page is one run). */
  recordStream: Uint8Array;
}

/** One SEGMENT_COLUMNS row, every column decoded (Null strings are '', Null bytes null). */
export interface FlatSqlSegmentRow {
  schemaName: string;
  providerId: string;
  sourceName: string;
  batchId: string;
  queryProfile: string;
  role: string;
  segmentIndex: number;
  offset: number;
  limit: number;
  cursor: string;
  nextCursor: string;
  recordCount: number;
  byteCount: number;
  byteOffset: number;
  byteLength: number;
  chunkHash: string;
  shardCid: string;
  indexCid: string;
  manifestCid: string;
  pnmCid: string;
  shardSha256: string;
  indexSha256: string;
  querySha256: string;
  resultSha256: string;
  feedSequence: number;
  previousHead: string;
  feedHead: string;
  publishedAtMs: number;
  signature: Uint8Array | null;
  signatureType: string;
  signingPublicKey: Uint8Array | null;
}

export interface FlatSqlSyncManifest {
  /** $QRP ETAG. */
  manifestId: string;
  schema: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  queryProfile: string;
  totalCount: number;
  /** Sum of the segments' BYTE_COUNT. */
  totalBytes: number;
  /** $QRP CURSOR: the snapshot every segment cursor belongs to. */
  snapshotId: string;
  artifactBundles: FlatSqlSyncManifestArtifactBundle[];
  segments: FlatSqlSyncManifestSegment[];
}

export interface FlatSqlSyncManifestArtifactBundle {
  role: 'shard-group-car' | string;
  cid: string;
  byteCount: number;
  sha256?: string;
  segmentStart: number;
  segmentCount: number;
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

/** list-published: one page of the node's servable dataset publications. */
export interface FlatSqlPublishedList {
  schema: string;
  totalCount: number;
  limit: number;
  /** Node-issued offset of this page. */
  cursor: string;
  /** Empty on the last page. */
  nextCursor: string;
  publications: FlatSqlSegmentRow[];
}

export interface FlatSqlPublishedShardHeader {
  role: string;
  schema: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  queryProfile: string;
  cid: string;
  indexCid?: string;
  manifestCid?: string;
  pnmCid?: string;
  rowCount: number;
  byteOffset: number;
  /** Bytes that follow the header for this shard. */
  byteLength: number;
  /** The whole file's size. */
  byteCount: number;
  shardSha256?: string;
  indexSha256?: string;
  querySha256?: string;
  resultSha256?: string;
  feedSequence?: number;
  previousHead?: string;
  feedHead?: string;
}

export interface FlatSqlPublishedShard {
  header: FlatSqlPublishedShardHeader;
  streamBytes: Uint8Array;
}

export interface FlatSqlPublishedShardBatchHeader {
  schema: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  /** Sum of the shards' bytes ($QRP MAX_BYTES). */
  totalBytes: number;
  shards: FlatSqlPublishedShardHeader[];
}

export interface FlatSqlPublishedShardBatch {
  header: FlatSqlPublishedShardBatchHeader;
  shards: FlatSqlPublishedShard[];
}

export interface FlatSqlSyncDatastoreIdentity {
  schemaName: string;
  sourcePeerId?: string;
  sourcePublicKey?: string;
  providerId?: string;
  sourceName?: string;
  batchHead?: string;
  queryProfile?: string;
}

export interface FlatSqlSyncDatastoreEntry {
  key: string;
  identity: FlatSqlSyncDatastoreIdentity;
  updatedAtMs: number;
}

export interface FlatSqlSyncDatastoreList {
  count: number;
  results: FlatSqlSyncDatastoreEntry[];
}

// ---------------------------------------------------------------- requests

export async function requestFlatSqlSyncChunk(
  transport: FlatSqlSyncTransport,
  query: FlatSqlSyncQuery,
): Promise<FlatSqlSyncChunk> {
  const response = await transport.dialProtocol(
    query.targetPeerId, FLATSQL_SYNC_READ_CHUNK_PROTOCOL_ID, encodeFlatSqlReadChunkRequest(query), query.candidateAddrs);
  return decodeFlatSqlSyncChunk(response);
}

export async function requestFlatSqlSyncManifest(
  transport: FlatSqlSyncTransport,
  query: FlatSqlSyncQuery,
): Promise<FlatSqlSyncManifest> {
  const response = await transport.dialProtocol(
    query.targetPeerId, FLATSQL_SYNC_OPEN_MANIFEST_PROTOCOL_ID, encodeFlatSqlManifestRequest(query), query.candidateAddrs);
  return decodeFlatSqlSyncManifest(response);
}

export async function requestFlatSqlPublishedList(
  transport: FlatSqlSyncTransport,
  query: FlatSqlSyncQuery,
): Promise<FlatSqlPublishedList> {
  const response = await transport.dialProtocol(
    query.targetPeerId, FLATSQL_SYNC_LIST_PUBLISHED_PROTOCOL_ID, encodeFlatSqlPublishedListRequest(query), query.candidateAddrs);
  return decodeFlatSqlPublishedList(response);
}

export async function requestFlatSqlPublishedShard(
  transport: FlatSqlSyncTransport,
  query: FlatSqlSyncQuery & { cid: string },
): Promise<FlatSqlPublishedShard> {
  const response = await transport.dialProtocol(
    query.targetPeerId, FLATSQL_SYNC_READ_SHARD_PROTOCOL_ID, encodeFlatSqlPublishedShardRequest(query), query.candidateAddrs);
  return decodeFlatSqlPublishedShard(response);
}

export async function requestFlatSqlPublishedShardBatch(
  transport: FlatSqlSyncTransport,
  query: FlatSqlSyncQuery & { cids: string[] },
): Promise<FlatSqlPublishedShardBatch> {
  const response = await transport.dialProtocol(
    query.targetPeerId, FLATSQL_SYNC_READ_SHARD_BATCH_PROTOCOL_ID, encodeFlatSqlPublishedShardBatchRequest(query), query.candidateAddrs);
  return decodeFlatSqlPublishedShardBatch(response);
}

export async function requestFlatSqlSyncDatastores(
  transport: FlatSqlSyncTransport,
  query: Pick<FlatSqlSyncQuery, 'targetPeerId' | 'candidateAddrs'>,
): Promise<FlatSqlSyncDatastoreList> {
  const response = await transport.dialProtocol(
    query.targetPeerId, FLATSQL_SYNC_LIST_DATASTORES_PROTOCOL_ID, encodeQrpRequest({}), query.candidateAddrs);
  return decodeFlatSqlSyncDatastores(response);
}

export async function requestFlatSqlWireSpeedProbe(
  transport: FlatSqlSyncTransport,
  query: FlatSqlWireSpeedProbeQuery,
  now: () => number = monotonicNow,
): Promise<FlatSqlWireSpeedProbeResult> {
  const startedAtMs = now();
  const response = await transport.dialProtocol(
    query.targetPeerId, FLATSQL_SYNC_WIRE_SPEED_PROBE_PROTOCOL_ID, encodeFlatSqlWireSpeedProbeRequest(query), query.candidateAddrs);
  const elapsedMs = Math.max(1, now() - startedAtMs);
  return decodeFlatSqlWireSpeedProbe(response, elapsedMs, query.probeBytes);
}

// ---------------------------------------------------------------- encoders

interface QrpRequestFields {
  schema?: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  originId?: string;
  search?: string;
  cursor?: string;
  resumeCursor?: string;
  limit?: number;
  cid?: string;
  maxBytes?: number;
  filters?: Array<{ field: string; op: qrpFilterOp; value: string }>;
}

/** One size-prefixed $QRP{KIND=Request} frame. */
function encodeQrpRequest(fields: QrpRequestFields): Uint8Array {
  const b = new flatbuffers.Builder(256);
  const str = (value?: string) => (value && value.trim() ? b.createString(value.trim()) : 0);
  const schema = str(fields.schema);
  const providerId = str(fields.providerId);
  const sourceName = str(fields.sourceName);
  const batchId = str(fields.batchId);
  const producerPeerId = str(fields.producerPeerId);
  const originId = str(fields.originId);
  const search = str(fields.search);
  const cursor = str(fields.cursor);
  const resumeCursor = str(fields.resumeCursor);
  const cid = str(fields.cid);
  const filters = fields.filters?.length
    ? QRP.createFiltersVector(b, fields.filters.map((filter) =>
      QRPFilter.createQRPFilter(b, b.createString(filter.field), filter.op, b.createString(filter.value))))
    : 0;
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Request);
  if (schema) QRP.addSchemaName(b, schema);
  if (providerId) QRP.addProviderId(b, providerId);
  if (sourceName) QRP.addSourceName(b, sourceName);
  if (batchId) QRP.addBatchId(b, batchId);
  if (producerPeerId) QRP.addProducerPeerId(b, producerPeerId);
  if (originId) QRP.addOriginId(b, originId);
  if (search) QRP.addSearch(b, search);
  if (filters) QRP.addFilters(b, filters);
  if (cursor) QRP.addCursor(b, cursor);
  if (resumeCursor) QRP.addResumeCursor(b, resumeCursor);
  if (cid) QRP.addCid(b, cid);
  const limit = wholeNumber(fields.limit);
  if (limit) QRP.addLimit(b, Math.min(limit, 0xffffffff));
  const maxBytes = wholeNumber(fields.maxBytes);
  if (maxBytes) QRP.addMaxBytes(b, BigInt(maxBytes));
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return b.asUint8Array().slice();
}

function laneFilters(query: Pick<FlatSqlSyncQuery, 'queryProfile' | 'datastoreKey'>): QrpRequestFields['filters'] {
  const filters: NonNullable<QrpRequestFields['filters']> = [];
  if (query.queryProfile?.trim()) filters.push({ field: FLATSQL_SYNC_FILTER_QUERY_PROFILE, op: qrpFilterOp.Eq, value: query.queryProfile.trim() });
  if (query.datastoreKey?.trim()) filters.push({ field: FLATSQL_SYNC_FILTER_DATASTORE_KEY, op: qrpFilterOp.Eq, value: query.datastoreKey.trim() });
  return filters;
}

function laneFields(query: FlatSqlSyncQuery): QrpRequestFields {
  return {
    schema: query.schema,
    providerId: query.providerId,
    sourceName: query.sourceName,
    batchId: query.batchId,
    filters: laneFilters(query),
  };
}

/** read-chunk request: lane, SEARCH, CURSOR | RESUME_CURSOR, LIMIT. */
export function encodeFlatSqlReadChunkRequest(query: Omit<FlatSqlSyncQuery, 'targetPeerId'>): Uint8Array {
  return encodeQrpRequest({
    ...laneFields(query as FlatSqlSyncQuery),
    producerPeerId: query.producerPeerId,
    originId: query.originId,
    search: query.search,
    ...(query.cursor ? { cursor: query.cursor } : { resumeCursor: query.resumeCursor }),
    limit: query.limit,
  });
}

/** open-manifest request: lane, LIMIT (records per segment). */
export function encodeFlatSqlManifestRequest(query: Omit<FlatSqlSyncQuery, 'targetPeerId'>): Uint8Array {
  return encodeQrpRequest({
    ...laneFields(query as FlatSqlSyncQuery),
    producerPeerId: query.producerPeerId,
    originId: query.originId,
    search: query.search,
    limit: query.limit,
  });
}

/** list-published request: lane, FILTERS [QUERY_PROFILE Eq], CURSOR (node-issued offset), LIMIT. */
export function encodeFlatSqlPublishedListRequest(
  query: Pick<FlatSqlSyncQuery, 'schema' | 'providerId' | 'sourceName' | 'batchId' | 'queryProfile' | 'datastoreKey' | 'cursor' | 'limit'>,
): Uint8Array {
  return encodeQrpRequest({ ...laneFields(query as FlatSqlSyncQuery), cursor: query.cursor, limit: query.limit });
}

/** read-shard request: lane, CID, CURSOR = byte offset, MAX_BYTES = byte length (0 = rest). */
export function encodeFlatSqlPublishedShardRequest(query: Omit<FlatSqlSyncQuery, 'targetPeerId'> & { cid: string }): Uint8Array {
  const byteOffset = wholeNumber(query.byteOffset);
  return encodeQrpRequest({
    ...laneFields(query as FlatSqlSyncQuery),
    cid: query.cid,
    ...(byteOffset ? { cursor: String(byteOffset) } : {}),
    maxBytes: query.byteLength,
  });
}

/** read-shard-batch request: lane, FILTERS [SHARD_CID In "a,b,c"]. */
export function encodeFlatSqlPublishedShardBatchRequest(query: Omit<FlatSqlSyncQuery, 'targetPeerId'> & { cids: string[] }): Uint8Array {
  const fields = laneFields(query as FlatSqlSyncQuery);
  const cids = query.cids.map((cid) => cid.trim()).filter(Boolean);
  fields.filters = [...(fields.filters ?? []), { field: FLATSQL_SYNC_FILTER_SHARD_CID, op: qrpFilterOp.In, value: cids.join(',') }];
  return encodeQrpRequest(fields);
}

/** wire-speed-probe request: MAX_BYTES (0 = the node's default). */
export function encodeFlatSqlWireSpeedProbeRequest(query: Pick<FlatSqlWireSpeedProbeQuery, 'probeBytes'>): Uint8Array {
  return encodeQrpRequest({ maxBytes: query.probeBytes });
}

// ---------------------------------------------------------------- decoders

interface ReplyHead {
  q: QRP;
  /** Byte offset just past the header frame. */
  end: number;
}

function frameAt(bytes: Uint8Array, offset: number): { payload: Uint8Array; end: number } {
  if (bytes.byteLength - offset < 4) throw new Error(`truncated FlatSQL sync frame prefix at byte ${offset}`);
  const length = new DataView(bytes.buffer, bytes.byteOffset + offset, 4).getUint32(0, true);
  const end = offset + 4 + length;
  if (length === 0 || end > bytes.byteLength) throw new Error(`FlatSQL sync frame at byte ${offset} declares ${length} bytes, ${bytes.byteLength - offset - 4} remain`);
  return { payload: bytes.subarray(offset + 4, end), end };
}

/** Reads the $QRP frame at offset: a Page is returned, an Error frame is thrown. */
function readPage(bytes: Uint8Array, offset: number): ReplyHead {
  const { payload, end } = frameAt(bytes, offset);
  if (!(payload.byteLength >= 8 && payload[4] === 0x24 && payload[5] === 0x51 && payload[6] === 0x52 && payload[7] === 0x50)) {
    throw new Error('FlatSQL sync reply frame is not a $QRP buffer');
  }
  const q = QRP.getRootAsQRP(new flatbuffers.ByteBuffer(payload));
  if (q.KIND() === qrpKind.Error) {
    throw new FlatSqlSyncError(q.ERROR_CODE() ?? '', q.MESSAGE() || 'FlatSQL sync request failed', q.STATUS(), q.RETRY_AFTER_MS(), q.HTTP_STATUS());
  }
  if (q.KIND() !== qrpKind.Page) throw new Error(`unexpected $QRP KIND ${q.KIND()} in a FlatSQL sync reply`);
  return { q, end };
}

/** A body that must be exactly one $QRP Page. */
function readOnlyPage(bytes: Uint8Array, what: string): QRP {
  if (bytes.byteLength === 0) throw new Error(`missing FlatSQL sync ${what} page`);
  const head = readPage(bytes, 0);
  if (head.end !== bytes.byteLength) throw new Error(`FlatSQL sync ${what} page has ${bytes.byteLength - head.end} trailing bytes`);
  return head.q;
}

type Cell = null | number | string | Uint8Array;

/** Rows of a projection page keyed by column NAME (a missing column reads undefined). */
function rowObjects(q: QRP): Array<Record<string, Cell>> {
  const names: string[] = [];
  const column = new QRPColumn();
  for (let j = 0; j < q.columnsLength(); j++) names.push(q.COLUMNS(j, column)?.NAME() ?? '');
  const row = new QRPRow();
  const value = new QRPValue();
  const out: Array<Record<string, Cell>> = [];
  for (let i = 0; i < q.rowsLength(); i++) {
    q.ROWS(i, row);
    const cells: Record<string, Cell> = {};
    for (let j = 0; j < names.length && j < row.cellsLength(); j++) {
      row.CELLS(j, value);
      switch (value.KIND()) {
        case qrpValueKind.Int: cells[names[j]] = Number(value.INT_VALUE()); break;
        case qrpValueKind.Text: cells[names[j]] = value.TEXT_VALUE() ?? ''; break;
        case qrpValueKind.Bytes: cells[names[j]] = value.bytesValueArray() ?? new Uint8Array(0); break;
        default: cells[names[j]] = null;
      }
    }
    out.push(cells);
  }
  return out;
}

function text(row: Record<string, Cell>, name: string): string {
  const value = row[name];
  if (value == null) return '';
  if (typeof value !== 'string') throw new Error(`FlatSQL sync column ${name} is not text`);
  return value;
}

function int(row: Record<string, Cell>, name: string): number {
  const value = row[name];
  if (value == null) return 0;
  if (typeof value !== 'number') throw new Error(`FlatSQL sync column ${name} is not an integer`);
  return value;
}

function bytesCell(row: Record<string, Cell>, name: string): Uint8Array | null {
  const value = row[name];
  if (value == null) return null;
  if (!(value instanceof Uint8Array)) throw new Error(`FlatSQL sync column ${name} is not bytes`);
  return value;
}

function opt(value: string): string | undefined {
  return value ? value : undefined;
}

/** Decodes the SEGMENT_COLUMNS rows of a $QRP page by column NAME. */
export function decodeFlatSqlSegmentRows(q: QRP): FlatSqlSegmentRow[] {
  return rowObjects(q).map((row) => ({
    schemaName: text(row, 'SCHEMA_NAME'),
    providerId: text(row, 'PROVIDER_ID'),
    sourceName: text(row, 'SOURCE_NAME'),
    batchId: text(row, 'BATCH_ID'),
    queryProfile: text(row, 'QUERY_PROFILE'),
    role: text(row, 'ROLE'),
    segmentIndex: int(row, 'SEGMENT_INDEX'),
    offset: int(row, 'OFFSET'),
    limit: int(row, 'LIMIT'),
    cursor: text(row, 'CURSOR'),
    nextCursor: text(row, 'NEXT_CURSOR'),
    recordCount: int(row, 'RECORD_COUNT'),
    byteCount: int(row, 'BYTE_COUNT'),
    byteOffset: int(row, 'BYTE_OFFSET'),
    byteLength: int(row, 'BYTE_LENGTH'),
    chunkHash: text(row, 'CHUNK_HASH'),
    shardCid: text(row, 'SHARD_CID'),
    indexCid: text(row, 'INDEX_CID'),
    manifestCid: text(row, 'MANIFEST_CID'),
    pnmCid: text(row, 'PNM_CID'),
    shardSha256: text(row, 'SHARD_SHA256'),
    indexSha256: text(row, 'INDEX_SHA256'),
    querySha256: text(row, 'QUERY_SHA256'),
    resultSha256: text(row, 'RESULT_SHA256'),
    feedSequence: int(row, 'FEED_SEQUENCE'),
    previousHead: text(row, 'PREVIOUS_HEAD'),
    feedHead: text(row, 'FEED_HEAD'),
    publishedAtMs: int(row, 'PUBLISHED_AT_MS'),
    signature: bytesCell(row, 'SIGNATURE'),
    signatureType: text(row, 'SIGNATURE_TYPE'),
    signingPublicKey: bytesCell(row, 'SIGNING_PUBLIC_KEY'),
  }));
}

async function recordCid(bytes: Uint8Array): Promise<string> {
  return CID.create(1, raw.code, await sha256.digest(bytes)).toString();
}

/** Decodes a read-chunk reply: runs of [$QRP Page][RECORD_COUNT record frames]. */
export async function decodeFlatSqlSyncChunk(bytes: Uint8Array): Promise<FlatSqlSyncChunk> {
  if (bytes.byteLength === 0) throw new Error('missing FlatSQL sync page header');
  let header: FlatSqlSyncHeader | null = null;
  const runs: FlatSqlSyncRun[] = [];
  const records: Uint8Array[] = [];
  for (let offset = 0; offset < bytes.byteLength;) {
    const { q, end } = readPage(bytes, offset);
    header ??= {
      schema: q.SCHEMA_NAME() ?? '',
      fileIdentifier: q.FILE_IDENTIFIER() ?? '',
      totalCount: Number(q.TOTAL_COUNT()),
      count: 0,
      limit: q.LIMIT(),
      cursor: q.CURSOR() ?? '',
      nextCursor: q.NEXT_CURSOR() ?? '',
      resumeCursor: q.RESUME_CURSOR() ?? '',
      chunkHash: q.ETAG() ?? '',
      results: [],
    };
    const run: FlatSqlSyncRun = {
      providerId: q.PROVIDER_ID() ?? '',
      sourceName: q.SOURCE_NAME() ?? '',
      batchId: q.BATCH_ID() ?? '',
      producerPeerId: q.PRODUCER_PEER_ID() ?? '',
      originId: q.ORIGIN_ID() ?? '',
      stream: new Uint8Array(0),
      records: [],
    };
    let cursor = end;
    for (let k = 0; k < q.RECORD_COUNT(); k++) {
      if (cursor >= bytes.byteLength) throw new Error(`FlatSQL sync run declares ${q.RECORD_COUNT()} records, ${k} arrived`);
      const frame = frameAt(bytes, cursor);
      run.records.push(frame.payload);
      cursor = frame.end;
    }
    run.stream = bytes.subarray(end, cursor);
    for (const record of run.records) {
      records.push(record);
      header.results.push({
        schemaName: header.schema,
        cid: await recordCid(record),
        peerId: run.originId,
        ...(run.providerId ? { providerId: run.providerId } : {}),
        ...(run.sourceName ? { sourceName: run.sourceName } : {}),
        ...(run.batchId ? { batchId: run.batchId } : {}),
        ...(run.producerPeerId ? { producerPeerId: run.producerPeerId } : {}),
        sizeBytes: record.byteLength,
      });
    }
    header.count += run.records.length;
    if (run.records.length > 0) runs.push(run);
    offset = cursor;
  }
  const recordStream = runs.length === 1 ? runs[0].stream : concatBytes(runs.map((run) => run.stream));
  return { header: header!, runs, records, recordStream };
}

/** Decodes an open-manifest reply. */
export function decodeFlatSqlSyncManifest(bytes: Uint8Array): FlatSqlSyncManifest {
  const q = readOnlyPage(bytes, 'manifest');
  const rows = decodeFlatSqlSegmentRows(q);
  const segments: FlatSqlSyncManifestSegment[] = [];
  const artifactBundles: FlatSqlSyncManifestArtifactBundle[] = [];
  for (const row of rows) {
    if (row.role === 'segment') {
      segments.push({
        index: row.segmentIndex,
        cursor: row.cursor,
        nextCursor: row.nextCursor,
        rowCount: row.recordCount,
        byteCount: row.byteCount,
        chunkHash: row.chunkHash,
        cid: opt(row.shardCid),
        indexCid: opt(row.indexCid),
        manifestCid: opt(row.manifestCid),
        pnmCid: opt(row.pnmCid),
        shardSha256: opt(row.shardSha256),
        indexSha256: opt(row.indexSha256),
        querySha256: opt(row.querySha256),
        ...(row.feedSequence ? { feedSequence: row.feedSequence } : {}),
        previousHead: opt(row.previousHead),
        feedHead: opt(row.feedHead),
      });
    } else if (row.role) {
      artifactBundles.push({
        role: row.role,
        cid: row.shardCid,
        byteCount: row.byteCount,
        sha256: opt(row.shardSha256),
        segmentStart: row.segmentIndex,
        segmentCount: row.limit,
      });
    }
  }
  return {
    manifestId: q.ETAG() ?? '',
    schema: q.SCHEMA_NAME() ?? '',
    providerId: opt(q.PROVIDER_ID() ?? ''),
    sourceName: opt(q.SOURCE_NAME() ?? ''),
    batchId: opt(q.BATCH_ID() ?? ''),
    producerPeerId: opt(q.PRODUCER_PEER_ID() ?? ''),
    queryProfile: rows[0]?.queryProfile ?? '',
    totalCount: Number(q.TOTAL_COUNT()),
    totalBytes: segments.reduce((sum, segment) => sum + segment.byteCount, 0),
    snapshotId: q.CURSOR() ?? '',
    artifactBundles,
    segments,
  };
}

/** Decodes a list-published reply (one $QRP Page of SEGMENT_COLUMNS rows). */
export function decodeFlatSqlPublishedList(bytes: Uint8Array): FlatSqlPublishedList {
  const q = readOnlyPage(bytes, 'publication list');
  return {
    schema: q.SCHEMA_NAME() ?? '',
    totalCount: Number(q.TOTAL_COUNT()),
    limit: q.LIMIT(),
    cursor: q.CURSOR() ?? '',
    nextCursor: q.NEXT_CURSOR() ?? '',
    publications: decodeFlatSqlSegmentRows(q),
  };
}

function shardHeaderFromRow(row: FlatSqlSegmentRow): FlatSqlPublishedShardHeader {
  return {
    role: row.role,
    schema: row.schemaName,
    providerId: opt(row.providerId),
    sourceName: opt(row.sourceName),
    batchId: opt(row.batchId),
    queryProfile: row.queryProfile,
    cid: row.shardCid,
    indexCid: opt(row.indexCid),
    manifestCid: opt(row.manifestCid),
    pnmCid: opt(row.pnmCid),
    rowCount: row.recordCount,
    byteOffset: row.byteOffset,
    byteLength: row.byteLength,
    byteCount: row.byteCount,
    shardSha256: opt(row.shardSha256),
    indexSha256: opt(row.indexSha256),
    querySha256: opt(row.querySha256),
    resultSha256: opt(row.resultSha256),
    ...(row.feedSequence ? { feedSequence: row.feedSequence } : {}),
    previousHead: opt(row.previousHead),
    feedHead: opt(row.feedHead),
  };
}

/** Decodes a read-shard reply: one $QRP Page (one row) then BYTE_LENGTH raw bytes. */
export function decodeFlatSqlPublishedShard(bytes: Uint8Array): FlatSqlPublishedShard {
  if (bytes.byteLength === 0) throw new Error('missing FlatSQL published shard page');
  const { q, end } = readPage(bytes, 0);
  const rows = decodeFlatSqlSegmentRows(q);
  if (rows.length !== 1) throw new Error(`published shard page carries ${rows.length} rows, want 1`);
  const header = shardHeaderFromRow(rows[0]);
  const streamBytes = bytes.subarray(end);
  if (streamBytes.byteLength !== header.byteLength) {
    throw new Error(`published shard returned ${streamBytes.byteLength}/${header.byteLength} bytes`);
  }
  return { header, streamBytes };
}

/** Decodes a read-shard-batch reply: one $QRP Page (a row per shard) then the shards concatenated. */
export function decodeFlatSqlPublishedShardBatch(bytes: Uint8Array): FlatSqlPublishedShardBatch {
  if (bytes.byteLength === 0) throw new Error('missing FlatSQL published shard batch page');
  const { q, end } = readPage(bytes, 0);
  const shardHeaders = decodeFlatSqlSegmentRows(q).map(shardHeaderFromRow);
  const shards: FlatSqlPublishedShard[] = [];
  let offset = end;
  for (const header of shardHeaders) {
    const next = offset + Math.max(0, header.byteLength);
    if (next > bytes.byteLength) {
      throw new Error(`published shard batch truncated ${header.cid} at ${bytes.byteLength - offset}/${header.byteLength} bytes`);
    }
    shards.push({ header, streamBytes: bytes.subarray(offset, next) });
    offset = next;
  }
  if (offset !== bytes.byteLength) throw new Error(`published shard batch has ${bytes.byteLength - offset} trailing bytes`);
  return {
    header: {
      schema: q.SCHEMA_NAME() ?? '',
      providerId: opt(q.PROVIDER_ID() ?? ''),
      sourceName: opt(q.SOURCE_NAME() ?? ''),
      batchId: opt(q.BATCH_ID() ?? ''),
      totalBytes: Number(q.MAX_BYTES()),
      shards: shardHeaders,
    },
    shards,
  };
}

/** Decodes a list-datastores reply (DATASTORE_KEY, SCHEMA_NAME, ... UPDATED_AT_MS rows). */
export function decodeFlatSqlSyncDatastores(bytes: Uint8Array): FlatSqlSyncDatastoreList {
  const q = readOnlyPage(bytes, 'datastore list');
  const results = rowObjects(q).map((row): FlatSqlSyncDatastoreEntry => ({
    key: text(row, 'DATASTORE_KEY'),
    updatedAtMs: int(row, 'UPDATED_AT_MS'),
    identity: {
      schemaName: text(row, 'SCHEMA_NAME'),
      providerId: opt(text(row, 'PROVIDER_ID')),
      sourceName: opt(text(row, 'SOURCE_NAME')),
      batchHead: opt(text(row, 'BATCH_ID')),
      queryProfile: opt(text(row, 'QUERY_PROFILE')),
      sourcePeerId: opt(text(row, 'PRODUCER_PEER_ID')),
      sourcePublicKey: opt(text(row, 'PRODUCER_PUBLIC_KEY')),
    },
  })).filter((entry) => entry.key && entry.identity.schemaName);
  return { count: Number(q.TOTAL_COUNT()) || results.length, results };
}

/** Decodes a wire-speed-probe reply: one $QRP Page (MAX_BYTES) then that many bytes. */
export function decodeFlatSqlWireSpeedProbe(bytes: Uint8Array, elapsedMs: number, requestedBytes?: number): FlatSqlWireSpeedProbeResult {
  if (bytes.byteLength === 0) throw new Error('missing FlatSQL sync wire-speed probe page');
  const { q, end } = readPage(bytes, 0);
  const payloadBytes = bytes.byteLength - end;
  const expected = Number(q.MAX_BYTES());
  if (expected !== payloadBytes) throw new Error(`wire-speed probe returned ${payloadBytes}/${expected} bytes`);
  const normalizedElapsedMs = Math.max(1, elapsedMs);
  return {
    requestedBytes: wholeNumber(requestedBytes) || payloadBytes,
    payloadBytes,
    elapsedMs: normalizedElapsedMs,
    bytesPerSecond: Math.floor(payloadBytes / (normalizedElapsedMs / 1000)),
  };
}

function wholeNumber(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}

function concatBytes(parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((total, part) => total + part.byteLength, 0));
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.byteLength;
  }
  return out;
}

function monotonicNow(): number {
  return globalThis.performance?.now?.() ?? Date.now();
}
