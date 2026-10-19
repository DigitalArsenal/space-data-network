/**
 * $QRP wire for the node's FlatBuffer-only record reads (browser-safe: no
 * Node APIs; sha-256 through WebCrypto via multiformats).
 *
 * POST /api/v1/data/scan
 *   request:  one size-prefixed $QRP{KIND=Request} frame,
 *             Content-Type application/vnd.sdn.flatbuffers.stream
 *   response: runs of [$QRP{KIND=Page} header][RECORD_COUNT record frames];
 *             each header names the run's lane and repeats the page cursors
 * GET /api/v1/data/index?schema=&source_name=&provider_id=&batch_id=&norad=&page=&limit=
 *   response: one $QRP{KIND=Page} frame, COLUMNS [cid, norad, epoch_ms], ROWS
 * Errors: one $QRP{KIND=Error} frame (STATUS Busy/Cold carry RETRY_AFTER_MS).
 *
 * Record frames are the stored bytes; cid is the store's CIDv1 (raw codec,
 * sha2-256), the same id /api/v1/data/records/{schema}/{cid} serves.
 */
import * as flatbuffers from 'flatbuffers';
import { CID } from 'multiformats/cid';
import * as raw from 'multiformats/codecs/raw';
import { sha256 } from 'multiformats/hashes/sha2';
import { QRP } from 'spacedatastandards.org/lib/js/QRP/QRP.js';
import { QRPRow } from 'spacedatastandards.org/lib/js/QRP/QRPRow.js';
import { QRPValue } from 'spacedatastandards.org/lib/js/QRP/QRPValue.js';
import { qrpKind } from 'spacedatastandards.org/lib/js/QRP/qrpKind.js';
import { qrpStatus } from 'spacedatastandards.org/lib/js/QRP/qrpStatus.js';
import { qrpValueKind } from 'spacedatastandards.org/lib/js/QRP/qrpValueKind.js';

export const FLATBUFFER_STREAM_TYPE = 'application/vnd.sdn.flatbuffers.stream';

export interface QrpScanRequest {
  schema: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  /** Storing peer id. */
  originId?: string;
  search?: string;
  limit?: number;
  cursor?: string;
  /**
   * The RESUME_CURSOR of an earlier complete scan of this same query: the
   * scan returns only records stored since. Never sent with `cursor`.
   */
  resumeCursor?: string;
}

export interface QrpScanPage {
  schemaName: string;
  fileIdentifier: string;
  limit: number;
  cursor: string;
  nextCursor: string;
  /**
   * Set on the last page (nextCursor empty): resumes this query after this
   * scan. Opaque; a 410 resume_cursor_expired answer means rescan.
   */
  resumeCursor: string;
  totalCount: number;
  /** Page chunk hash. */
  etag: string;
}

export interface QrpScanRecord {
  /** The stored record bytes (a view into the response body). */
  bytes: Uint8Array;
  cid: string;
  providerId: string;
  sourceName: string;
  batchId: string;
  producerPeerId: string;
  originId: string;
}

export interface QrpIndexPage {
  schemaName: string;
  total: number;
  page: number;
  limit: number;
  stale: boolean;
  asOfMs: number;
  rows: Array<{ cid: string; norad: number | null; epochMs: number | null }>;
}

export class QrpError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly status: qrpStatus,
    readonly retryAfterMs: number,
  ) {
    super(message);
    this.name = 'QrpError';
  }
}

/** Size-prefixed $QRP{KIND=Request} frame for a scan. */
export function encodeQrpScanRequest(request: QrpScanRequest): Uint8Array {
  const b = new flatbuffers.Builder(256);
  const str = (value?: string) => (value ? b.createString(value) : 0);
  const schema = str(request.schema);
  const providerId = str(request.providerId);
  const sourceName = str(request.sourceName);
  const batchId = str(request.batchId);
  const producerPeerId = str(request.producerPeerId);
  const originId = str(request.originId);
  const search = str(request.search);
  const cursor = str(request.cursor);
  const resumeCursor = str(request.resumeCursor);
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Request);
  QRP.addSchemaName(b, schema);
  if (providerId) QRP.addProviderId(b, providerId);
  if (sourceName) QRP.addSourceName(b, sourceName);
  if (batchId) QRP.addBatchId(b, batchId);
  if (producerPeerId) QRP.addProducerPeerId(b, producerPeerId);
  if (originId) QRP.addOriginId(b, originId);
  if (search) QRP.addSearch(b, search);
  if (cursor) QRP.addCursor(b, cursor);
  if (resumeCursor) QRP.addResumeCursor(b, resumeCursor);
  if (request.limit) QRP.addLimit(b, request.limit);
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return b.asUint8Array();
}

/** Splits an aligned size-prefixed stream into frame payloads (no copies). */
export function splitFrames(body: Uint8Array): Uint8Array[] {
  const view = new DataView(body.buffer, body.byteOffset, body.byteLength);
  const frames: Uint8Array[] = [];
  for (let off = 0; off < body.byteLength; ) {
    if (body.byteLength - off < 4) throw new Error(`truncated frame prefix at byte ${off}`);
    const length = view.getUint32(off, true);
    if (off + 4 + length > body.byteLength) throw new Error(`frame at byte ${off} overruns the body`);
    frames.push(body.subarray(off + 4, off + 4 + length));
    off += 4 + length;
  }
  return frames;
}

function isQrp(payload: Uint8Array): boolean {
  return payload.byteLength >= 8 && payload[4] === 0x24 && payload[5] === 0x51 && payload[6] === 0x52 && payload[7] === 0x50;
}

function readQrp(payload: Uint8Array): QRP {
  if (!isQrp(payload)) throw new Error('frame is not a $QRP buffer');
  return QRP.getRootAsQRP(new flatbuffers.ByteBuffer(payload));
}

function throwIfError(q: QRP): void {
  if (q.KIND() === qrpKind.Error) {
    throw new QrpError(q.ERROR_CODE() ?? '', q.MESSAGE() ?? 'node answered an error', q.STATUS(), q.RETRY_AFTER_MS());
  }
}

/** Decodes a body that should be exactly one $QRP Error frame, or null. */
export function decodeQrpError(body: Uint8Array): QrpError | null {
  try {
    const frames = splitFrames(body);
    if (frames.length !== 1) return null;
    const q = readQrp(frames[0]);
    if (q.KIND() !== qrpKind.Error) return null;
    return new QrpError(q.ERROR_CODE() ?? '', q.MESSAGE() ?? '', q.STATUS(), q.RETRY_AFTER_MS());
  } catch {
    return null;
  }
}

export async function recordCid(bytes: Uint8Array): Promise<string> {
  return CID.create(1, raw.code, await sha256.digest(bytes)).toString();
}

/** Decodes one scan page: its cursors and every record with its lane. */
export async function decodeQrpScanStream(body: Uint8Array): Promise<{ page: QrpScanPage; records: QrpScanRecord[] }> {
  const frames = splitFrames(body);
  if (frames.length === 0) throw new Error('scan answered an empty body');
  let page: QrpScanPage | null = null;
  const records: QrpScanRecord[] = [];
  for (let k = 0; k < frames.length; ) {
    const header = readQrp(frames[k++]);
    throwIfError(header);
    if (header.KIND() !== qrpKind.Page) throw new Error('scan run does not start with a $QRP page header');
    page ??= {
      schemaName: header.SCHEMA_NAME() ?? '',
      fileIdentifier: header.FILE_IDENTIFIER() ?? '',
      limit: header.LIMIT(),
      cursor: header.CURSOR() ?? '',
      nextCursor: header.NEXT_CURSOR() ?? '',
      resumeCursor: header.RESUME_CURSOR() ?? '',
      totalCount: Number(header.TOTAL_COUNT()),
      etag: header.ETAG() ?? '',
    };
    const count = header.RECORD_COUNT();
    if (k + count > frames.length) throw new Error(`scan run declares ${count} records, ${frames.length - k} frames remain`);
    const lane = {
      providerId: header.PROVIDER_ID() ?? '',
      sourceName: header.SOURCE_NAME() ?? '',
      batchId: header.BATCH_ID() ?? '',
      producerPeerId: header.PRODUCER_PEER_ID() ?? '',
      originId: header.ORIGIN_ID() ?? '',
    };
    for (const bytes of frames.slice(k, k + count)) {
      records.push({ bytes, cid: await recordCid(bytes), ...lane });
    }
    k += count;
  }
  return { page: page!, records };
}

/** Decodes the index route's one $QRP projection page. */
export function decodeQrpIndexPage(body: Uint8Array): QrpIndexPage {
  const frames = splitFrames(body);
  if (frames.length !== 1) throw new Error(`index answered ${frames.length} frames, want 1`);
  const q = readQrp(frames[0]);
  throwIfError(q);
  const row = new QRPRow();
  const cell = new QRPValue();
  const int = (j: number): number | null => {
    row.CELLS(j, cell);
    return cell.KIND() === qrpValueKind.Int ? Number(cell.INT_VALUE()) : null;
  };
  const rows: QrpIndexPage['rows'] = [];
  for (let i = 0; i < q.rowsLength(); i++) {
    q.ROWS(i, row);
    row.CELLS(0, cell);
    const cid = cell.TEXT_VALUE() ?? '';
    rows.push({ cid, norad: int(1), epochMs: int(2) });
  }
  return {
    schemaName: q.SCHEMA_NAME() ?? '',
    total: Number(q.TOTAL_COUNT()),
    page: q.PAGE(),
    limit: q.LIMIT(),
    stale: q.STALE(),
    asOfMs: Number(q.AS_OF_MS()),
    rows,
  };
}
