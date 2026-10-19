/**
 * $QRP requests and table pages for the node's /api/v1/data routes
 * (browser-safe; the scan stream and the error frame live in qrp-frames.ts).
 *
 * POST /api/v1/data/query
 *   request:  one size-prefixed $QRP{KIND=Request} frame ({@link encodeQrpRequest})
 *   response: runs of [$QRP{KIND=Page} header][RECORD_COUNT verbatim record frames]
 *             ({@link decodeQrpRecordRuns}, or qrp-frames decodeQrpScanStream
 *             when every record needs its CID)
 * GET /api/v1/data/{health,summary,datastores,local-replica-stats,sources}
 *   response: one $QRP{KIND=Page} frame, COLUMNS + ROWS of typed cells
 *             ({@link decodeQrpTablePage}); cells are read by column NAME.
 * Errors: one $QRP{KIND=Error} frame (qrp-frames QrpError).
 */
import * as flatbuffers from 'flatbuffers';
import { QRP } from 'spacedatastandards.org/lib/js/QRP/QRP.js';
import { QRPColumn } from 'spacedatastandards.org/lib/js/QRP/QRPColumn.js';
import { QRPFilter } from 'spacedatastandards.org/lib/js/QRP/QRPFilter.js';
import { QRPRow } from 'spacedatastandards.org/lib/js/QRP/QRPRow.js';
import { QRPValue } from 'spacedatastandards.org/lib/js/QRP/QRPValue.js';
import { qrpFilterOp } from 'spacedatastandards.org/lib/js/QRP/qrpFilterOp.js';
import { qrpKind } from 'spacedatastandards.org/lib/js/QRP/qrpKind.js';
import { qrpStatus } from 'spacedatastandards.org/lib/js/QRP/qrpStatus.js';
import { qrpTimeProfile } from 'spacedatastandards.org/lib/js/QRP/qrpTimeProfile.js';
import { qrpValueKind } from 'spacedatastandards.org/lib/js/QRP/qrpValueKind.js';
import { FLATBUFFER_STREAM_TYPE, QrpError, splitFrames } from './qrp-frames';
import type { RawDataQuery } from './sdn-backend';

export { qrpFilterOp, qrpStatus, qrpTimeProfile };

/** A typed QRPValue cell: Null, Bool, Int (number), Float, Text, Bytes. */
export type QrpCell = null | boolean | number | string | Uint8Array;

export interface QrpFilterSpec {
  field: string;
  op?: qrpFilterOp;
  value: string;
}

export interface QrpRequest {
  schema: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  /** Storing peer id. */
  originId?: string;
  archiveId?: string;
  /** sync_filter grammar (`EPOCH >= 2024-01-01 AND ...`). */
  sql?: string;
  search?: string;
  filters?: QrpFilterSpec[];
  fromEpochMs?: number;
  toEpochMs?: number;
  timeProfile?: qrpTimeProfile;
  profileEpochMs?: number;
  /** 1-based; offset = (page - 1) * limit. */
  page?: number;
  limit?: number;
  parameters?: QrpCell[];
}

export interface QrpColumnInfo {
  name: string;
  type: string;
  unit: string;
}

export interface QrpTablePage {
  status: qrpStatus;
  schemaName: string;
  generatedAtMs: number;
  asOfMs: number;
  stale: boolean;
  totalCount: number;
  page: number;
  limit: number;
  columns: QrpColumnInfo[];
  rows: QrpCell[][];
}

export interface QrpRunHeader {
  schemaName: string;
  fileIdentifier: string;
  page: number;
  limit: number;
  totalCount: number;
  search: string;
  generatedAtMs: number;
  providerId: string;
  sourceName: string;
  batchId: string;
  producerPeerId: string;
  originId: string;
}

export interface QrpRecordRun extends QrpRunHeader {
  count: number;
  /** The run's record frames as one aligned size-prefixed stream (a view into the body). */
  stream: Uint8Array;
}

/** Size-prefixed $QRP{KIND=Request} frame. */
export function encodeQrpRequest(request: QrpRequest): Uint8Array {
  const b = new flatbuffers.Builder(512);
  const str = (value?: string) => (value ? b.createString(value) : 0);
  const schema = str(request.schema);
  const providerId = str(request.providerId);
  const sourceName = str(request.sourceName);
  const batchId = str(request.batchId);
  const producerPeerId = str(request.producerPeerId);
  const originId = str(request.originId);
  const archiveId = str(request.archiveId);
  const sql = str(request.sql);
  const search = str(request.search);
  const filters = request.filters?.length
    ? QRP.createFiltersVector(b, request.filters.map((f) => QRPFilter.createQRPFilter(b, b.createString(f.field), f.op ?? qrpFilterOp.Eq, b.createString(f.value))))
    : 0;
  const parameters = request.parameters?.length
    ? QRP.createParametersVector(b, request.parameters.map((cell) => encodeCell(b, cell)))
    : 0;
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Request);
  if (schema) QRP.addSchemaName(b, schema);
  if (providerId) QRP.addProviderId(b, providerId);
  if (sourceName) QRP.addSourceName(b, sourceName);
  if (batchId) QRP.addBatchId(b, batchId);
  if (producerPeerId) QRP.addProducerPeerId(b, producerPeerId);
  if (originId) QRP.addOriginId(b, originId);
  if (archiveId) QRP.addArchiveId(b, archiveId);
  if (sql) QRP.addSql(b, sql);
  if (search) QRP.addSearch(b, search);
  if (filters) QRP.addFilters(b, filters);
  if (parameters) QRP.addParameters(b, parameters);
  if (request.fromEpochMs != null) QRP.addFromEpochMs(b, BigInt(Math.trunc(request.fromEpochMs)));
  if (request.toEpochMs != null) QRP.addToEpochMs(b, BigInt(Math.trunc(request.toEpochMs)));
  if (request.timeProfile) QRP.addTimeProfile(b, request.timeProfile);
  if (request.profileEpochMs != null) QRP.addProfileEpochMs(b, BigInt(Math.trunc(request.profileEpochMs)));
  if (request.page) QRP.addPage(b, request.page);
  if (request.limit) QRP.addLimit(b, request.limit);
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return b.asUint8Array();
}

function encodeCell(b: flatbuffers.Builder, cell: QrpCell): flatbuffers.Offset {
  if (cell === null) return QRPValue.createQRPValue(b, qrpValueKind.Null, false, 0n, 0, 0, 0);
  if (typeof cell === 'boolean') return QRPValue.createQRPValue(b, qrpValueKind.Bool, cell, 0n, 0, 0, 0);
  if (typeof cell === 'number') {
    return Number.isInteger(cell)
      ? QRPValue.createQRPValue(b, qrpValueKind.Int, false, BigInt(cell), 0, 0, 0)
      : QRPValue.createQRPValue(b, qrpValueKind.Float, false, 0n, cell, 0, 0);
  }
  if (typeof cell === 'string') return QRPValue.createQRPValue(b, qrpValueKind.Text, false, 0n, 0, b.createString(cell), 0);
  return QRPValue.createQRPValue(b, qrpValueKind.Bytes, false, 0n, 0, 0, QRPValue.createBytesValueVector(b, cell));
}

/**
 * The /data/query request for a raw-lane query. The page cursor is PAGE:
 * an offset that is not a whole number of pages cannot be asked for.
 */
export function qrpRequestFromRawQuery(query: RawDataQuery): QrpRequest {
  const limit = query.limit && query.limit > 0 ? Math.floor(query.limit) : undefined;
  const offset = Math.max(0, Math.floor(query.offset ?? 0));
  if (offset && (!limit || offset % limit !== 0)) throw new Error(`offset ${offset} is not a whole page of ${limit ?? 0} records`);
  return {
    schema: query.schema,
    ...(query.datastoreKey ? { archiveId: query.datastoreKey } : {}),
    ...(query.providerId ? { providerId: query.providerId } : {}),
    ...(query.sourceName ? { sourceName: query.sourceName } : {}),
    ...(query.batchId ? { batchId: query.batchId } : {}),
    ...(query.peerId ? { originId: query.peerId } : {}),
    ...(query.syncFilter ? { sql: query.syncFilter } : {}),
    ...(query.search ? { search: query.search } : {}),
    ...(limit ? { limit, page: offset / limit + 1 } : {}),
  };
}

/** Authenticated POST of one $QRP request frame. */
export function qrpPostInit(request: QrpRequest, signal?: AbortSignal): RequestInit {
  return {
    method: 'POST',
    credentials: 'include',
    headers: { 'content-type': FLATBUFFER_STREAM_TYPE, accept: FLATBUFFER_STREAM_TYPE, 'x-requested-with': 'sdn-ui' },
    body: encodeQrpRequest(request) as BodyInit,
    ...(signal ? { signal } : {}),
  };
}

function readQrp(payload: Uint8Array): QRP {
  if (!(payload.byteLength >= 8 && payload[4] === 0x24 && payload[5] === 0x51 && payload[6] === 0x52 && payload[7] === 0x50)) {
    throw new Error('frame is not a $QRP buffer');
  }
  return QRP.getRootAsQRP(new flatbuffers.ByteBuffer(payload));
}

function throwIfError(q: QRP): void {
  if (q.KIND() === qrpKind.Error) {
    throw new QrpError(q.ERROR_CODE() ?? '', q.MESSAGE() ?? 'node answered an error', q.STATUS(), q.RETRY_AFTER_MS());
  }
}

function decodeCell(cell: QRPValue): QrpCell {
  switch (cell.KIND()) {
    case qrpValueKind.Bool: return cell.BOOL_VALUE();
    case qrpValueKind.Int: return Number(cell.INT_VALUE());
    case qrpValueKind.Float: return cell.FLOAT_VALUE();
    case qrpValueKind.Text: return cell.TEXT_VALUE() ?? '';
    case qrpValueKind.Bytes: return cell.bytesValueArray() ?? new Uint8Array(0);
    default: return null;
  }
}

/** Decodes a body that is exactly one $QRP Page of COLUMNS + ROWS. */
export function decodeQrpTablePage(body: Uint8Array): QrpTablePage {
  const frames = splitFrames(body);
  if (frames.length !== 1) throw new Error(`table page answered ${frames.length} frames, want 1`);
  const q = readQrp(frames[0]);
  throwIfError(q);
  if (q.KIND() !== qrpKind.Page) throw new Error('table answer is not a $QRP page');
  const column = new QRPColumn();
  const columns: QrpColumnInfo[] = [];
  for (let j = 0; j < q.columnsLength(); j++) {
    q.COLUMNS(j, column);
    columns.push({ name: column.NAME() ?? '', type: column.TYPE() ?? '', unit: column.UNIT() ?? '' });
  }
  const row = new QRPRow();
  const cell = new QRPValue();
  const rows: QrpCell[][] = [];
  for (let i = 0; i < q.rowsLength(); i++) {
    q.ROWS(i, row);
    const cells: QrpCell[] = [];
    for (let j = 0; j < row.cellsLength(); j++) {
      row.CELLS(j, cell);
      cells.push(decodeCell(cell));
    }
    rows.push(cells);
  }
  return {
    status: q.STATUS(),
    schemaName: q.SCHEMA_NAME() ?? '',
    generatedAtMs: Number(q.GENERATED_AT_MS()),
    asOfMs: Number(q.AS_OF_MS()),
    stale: q.STALE(),
    totalCount: Number(q.TOTAL_COUNT()),
    page: q.PAGE(),
    limit: q.LIMIT(),
    columns,
    rows,
  };
}

/** Rows keyed by column NAME (a column the page lacks reads as undefined). */
export function qrpRowObjects(page: QrpTablePage): Array<Record<string, QrpCell>> {
  return page.rows.map((cells) => {
    const out: Record<string, QrpCell> = {};
    page.columns.forEach((column, j) => { out[column.name] = j < cells.length ? cells[j] : null; });
    return out;
  });
}

export function qrpText(row: Record<string, QrpCell>, name: string): string {
  const value = row[name];
  return value == null || value instanceof Uint8Array ? '' : String(value);
}

export function qrpNumber(row: Record<string, QrpCell>, name: string): number | undefined {
  const value = row[name];
  return typeof value === 'number' ? value : undefined;
}

export function qrpBool(row: Record<string, QrpCell>, name: string): boolean | undefined {
  const value = row[name];
  return typeof value === 'boolean' ? value : undefined;
}

/**
 * Decodes a /data/query answer into its runs without hashing records: each
 * run keeps its lane and its record frames as one aligned size-prefixed view
 * of the body (zero copy), ready for a FlatSQL stream ingest.
 */
export function decodeQrpRecordRuns(body: Uint8Array): QrpRecordRun[] {
  const frames = splitFrames(body);
  if (frames.length === 0) throw new Error('query answered an empty body');
  const runs: QrpRecordRun[] = [];
  for (let k = 0; k < frames.length; ) {
    const header = readQrp(frames[k++]);
    throwIfError(header);
    if (header.KIND() !== qrpKind.Page) throw new Error('query run does not start with a $QRP page header');
    const count = header.RECORD_COUNT();
    if (k + count > frames.length) throw new Error(`query run declares ${count} records, ${frames.length - k} frames remain`);
    const start = count ? frames[k].byteOffset - 4 : 0;
    const end = count ? frames[k + count - 1].byteOffset + frames[k + count - 1].byteLength : 0;
    runs.push({
      schemaName: header.SCHEMA_NAME() ?? '',
      fileIdentifier: header.FILE_IDENTIFIER() ?? '',
      page: header.PAGE(),
      limit: header.LIMIT(),
      totalCount: Number(header.TOTAL_COUNT()),
      search: header.SEARCH() ?? '',
      generatedAtMs: Number(header.GENERATED_AT_MS()),
      providerId: header.PROVIDER_ID() ?? '',
      sourceName: header.SOURCE_NAME() ?? '',
      batchId: header.BATCH_ID() ?? '',
      producerPeerId: header.PRODUCER_PEER_ID() ?? '',
      originId: header.ORIGIN_ID() ?? '',
      count,
      stream: count ? new Uint8Array(body.buffer, start, end - start) : new Uint8Array(0),
    });
    k += count;
  }
  return runs;
}

export interface QrpRunSpec {
  schemaName: string;
  fileIdentifier?: string;
  page?: number;
  limit?: number;
  totalCount?: number;
  search?: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  originId?: string;
  /** Size-prefixed record frames of the run. */
  records: Uint8Array[];
}

/** The node side of /data/query: runs of [$QRP page header][record frames]. */
export function encodeQrpRunAnswer(runs: QrpRunSpec[]): Uint8Array {
  const parts: Uint8Array[] = [];
  for (const run of runs) {
    const b = new flatbuffers.Builder(256);
    const str = (value?: string) => (value ? b.createString(value) : 0);
    const offsets = [run.schemaName, run.fileIdentifier, run.search, run.providerId, run.sourceName, run.batchId, run.producerPeerId, run.originId].map(str);
    QRP.startQRP(b);
    QRP.addKind(b, qrpKind.Page);
    if (offsets[0]) QRP.addSchemaName(b, offsets[0]);
    if (offsets[1]) QRP.addFileIdentifier(b, offsets[1]);
    if (offsets[2]) QRP.addSearch(b, offsets[2]);
    if (offsets[3]) QRP.addProviderId(b, offsets[3]);
    if (offsets[4]) QRP.addSourceName(b, offsets[4]);
    if (offsets[5]) QRP.addBatchId(b, offsets[5]);
    if (offsets[6]) QRP.addProducerPeerId(b, offsets[6]);
    if (offsets[7]) QRP.addOriginId(b, offsets[7]);
    if (run.page) QRP.addPage(b, run.page);
    if (run.limit) QRP.addLimit(b, run.limit);
    QRP.addTotalCount(b, BigInt(run.totalCount ?? run.records.length));
    QRP.addRecordCount(b, run.records.length);
    QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
    parts.push(b.asUint8Array(), ...run.records);
  }
  return concatBytes(parts);
}

/** The node side of a COLUMNS + ROWS route: one $QRP page. */
export function encodeQrpTablePage(page: { status?: qrpStatus; generatedAtMs?: number; columns: Array<Partial<QrpColumnInfo> & { name: string }>; rows: QrpCell[][] }): Uint8Array {
  const b = new flatbuffers.Builder(512);
  const columns = QRP.createColumnsVector(b, page.columns.map((column) => QRPColumn.createQRPColumn(
    b, b.createString(column.name), b.createString(column.type ?? ''), b.createString(column.unit ?? ''), false, false)));
  const rows = QRP.createRowsVector(b, page.rows.map((cells) => QRPRow.createQRPRow(b, QRPRow.createCellsVector(b, cells.map((cell) => encodeCell(b, cell))))));
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Page);
  QRP.addStatus(b, page.status ?? qrpStatus.Ok);
  if (page.generatedAtMs) QRP.addGeneratedAtMs(b, BigInt(page.generatedAtMs));
  QRP.addColumns(b, columns);
  QRP.addRows(b, rows);
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return b.asUint8Array().slice();
}

/** The node side of every error: one $QRP{KIND=Error} frame. */
export function encodeQrpErrorFrame(code: string, message: string, status: qrpStatus = qrpStatus.Error, retryAfterMs = 0): Uint8Array {
  const b = new flatbuffers.Builder(128);
  const codeOffset = b.createString(code);
  const messageOffset = b.createString(message);
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Error);
  QRP.addStatus(b, status);
  QRP.addErrorCode(b, codeOffset);
  QRP.addMessage(b, messageOffset);
  if (retryAfterMs) QRP.addRetryAfterMs(b, retryAfterMs);
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return b.asUint8Array().slice();
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

/** Decodes one $QRP request frame (the node side of {@link encodeQrpRequest}). */
export function decodeQrpRequest(frame: Uint8Array): QrpRequest {
  const frames = splitFrames(frame);
  if (frames.length !== 1) throw new Error(`request carries ${frames.length} frames, want 1`);
  const q = readQrp(frames[0]);
  if (q.KIND() !== qrpKind.Request) throw new Error('frame is not a $QRP request');
  const filter = new QRPFilter();
  const filters: QrpFilterSpec[] = [];
  for (let i = 0; i < q.filtersLength(); i++) {
    q.FILTERS(i, filter);
    filters.push({ field: filter.FIELD() ?? '', op: filter.OP(), value: filter.VALUE() ?? '' });
  }
  const opt = (value: string | null) => (value ? value : undefined);
  const ms = (value: bigint) => (value ? Number(value) : undefined);
  const out: QrpRequest = { schema: q.SCHEMA_NAME() ?? '' };
  const fields: Partial<QrpRequest> = {
    providerId: opt(q.PROVIDER_ID()), sourceName: opt(q.SOURCE_NAME()), batchId: opt(q.BATCH_ID()),
    producerPeerId: opt(q.PRODUCER_PEER_ID()), originId: opt(q.ORIGIN_ID()), archiveId: opt(q.ARCHIVE_ID()),
    sql: opt(q.SQL()), search: opt(q.SEARCH()), filters: filters.length ? filters : undefined,
    fromEpochMs: ms(q.FROM_EPOCH_MS()), toEpochMs: ms(q.TO_EPOCH_MS()),
    timeProfile: q.TIME_PROFILE() || undefined, profileEpochMs: ms(q.PROFILE_EPOCH_MS()),
    page: q.PAGE() || undefined, limit: q.LIMIT() || undefined,
  };
  for (const [key, value] of Object.entries(fields)) if (value !== undefined) (out as unknown as Record<string, unknown>)[key] = value;
  return out;
}
