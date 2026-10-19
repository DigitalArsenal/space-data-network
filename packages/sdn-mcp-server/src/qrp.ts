/**
 * $QRP wire for the node's /api/v1/data routes (sdn-server internal/api/wire.go).
 *
 * Every body, errors included, is size-prefixed FlatBuffers
 * (application/vnd.sdn.flatbuffers.stream):
 *   POST /api/v1/data/query  one $QRP{KIND=Request} frame in; runs of
 *                            [$QRP{KIND=Page} header][RECORD_COUNT record frames] out
 *   GET  /api/v1/data/{health,summary,...}  one $QRP{KIND=Page} of COLUMNS + ROWS
 *   any error                one $QRP{KIND=Error} frame
 */
import { createHash } from "node:crypto";
import * as flatbuffers from "flatbuffers";
import { QRP } from "spacedatastandards.org/lib/js/QRP/QRP.js";
import { QRPColumn } from "spacedatastandards.org/lib/js/QRP/QRPColumn.js";
import { QRPFilter } from "spacedatastandards.org/lib/js/QRP/QRPFilter.js";
import { QRPRow } from "spacedatastandards.org/lib/js/QRP/QRPRow.js";
import { QRPValue } from "spacedatastandards.org/lib/js/QRP/QRPValue.js";
import { qrpFilterOp } from "spacedatastandards.org/lib/js/QRP/qrpFilterOp.js";
import { qrpKind } from "spacedatastandards.org/lib/js/QRP/qrpKind.js";
import { qrpStatus } from "spacedatastandards.org/lib/js/QRP/qrpStatus.js";
import { qrpTimeProfile } from "spacedatastandards.org/lib/js/QRP/qrpTimeProfile.js";
import { qrpValueKind } from "spacedatastandards.org/lib/js/QRP/qrpValueKind.js";

export { qrpFilterOp, qrpStatus, qrpTimeProfile };

export const FLATBUFFER_STREAM_TYPE = "application/vnd.sdn.flatbuffers.stream";

export type QrpCell = null | boolean | number | string | Uint8Array;

export interface QrpRequest {
  schema: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  originId?: string;
  filters?: Array<{ field: string; op?: qrpFilterOp; value: string }>;
  fromEpochMs?: number;
  toEpochMs?: number;
  timeProfile?: qrpTimeProfile;
  profileEpochMs?: number;
  page?: number;
  limit?: number;
}

export interface QrpRecord {
  cid: string;
  size_bytes: number;
  file_identifier: string;
  provider_id: string;
  source_name: string;
  batch_id: string;
  producer_peer_id: string;
  origin_id: string;
}

export interface QrpQueryAnswer {
  schema: string;
  page: number;
  limit: number;
  total_count: number;
  count: number;
  generated_at_ms: number;
  records: QrpRecord[];
}

export interface QrpTable {
  status: qrpStatus;
  generated_at_ms: number;
  as_of_ms: number;
  rows: Array<Record<string, QrpCell>>;
}

/** Size-prefixed $QRP{KIND=Request} frame. */
export function encodeQrpRequest(request: QrpRequest): Uint8Array {
  const b = new flatbuffers.Builder(256);
  const str = (value?: string) => (value ? b.createString(value) : 0);
  const schema = str(request.schema);
  const providerId = str(request.providerId);
  const sourceName = str(request.sourceName);
  const batchId = str(request.batchId);
  const producerPeerId = str(request.producerPeerId);
  const originId = str(request.originId);
  const filters = request.filters?.length
    ? QRP.createFiltersVector(
        b,
        request.filters.map((f) => QRPFilter.createQRPFilter(b, b.createString(f.field), f.op ?? qrpFilterOp.Eq, b.createString(f.value)))
      )
    : 0;
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Request);
  if (schema) QRP.addSchemaName(b, schema);
  if (providerId) QRP.addProviderId(b, providerId);
  if (sourceName) QRP.addSourceName(b, sourceName);
  if (batchId) QRP.addBatchId(b, batchId);
  if (producerPeerId) QRP.addProducerPeerId(b, producerPeerId);
  if (originId) QRP.addOriginId(b, originId);
  if (filters) QRP.addFilters(b, filters);
  if (request.fromEpochMs != null) QRP.addFromEpochMs(b, BigInt(Math.trunc(request.fromEpochMs)));
  if (request.toEpochMs != null) QRP.addToEpochMs(b, BigInt(Math.trunc(request.toEpochMs)));
  if (request.timeProfile) QRP.addTimeProfile(b, request.timeProfile);
  if (request.profileEpochMs != null) QRP.addProfileEpochMs(b, BigInt(Math.trunc(request.profileEpochMs)));
  if (request.page) QRP.addPage(b, request.page);
  if (request.limit) QRP.addLimit(b, request.limit);
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return b.asUint8Array();
}

/** Splits a size-prefixed stream into frame payloads (no copies). */
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

function readQrp(payload: Uint8Array): QRP {
  if (!(payload.byteLength >= 8 && payload[4] === 0x24 && payload[5] === 0x51 && payload[6] === 0x52 && payload[7] === 0x50)) {
    throw new Error("frame is not a $QRP buffer");
  }
  return QRP.getRootAsQRP(new flatbuffers.ByteBuffer(payload));
}

function errorText(q: QRP): string {
  return [q.ERROR_CODE(), q.MESSAGE()].filter(Boolean).join(": ") || "node answered an error";
}

/** "code: message" of a body that is exactly one $QRP Error frame, else null. */
export function decodeQrpErrorText(body: Uint8Array): string | null {
  try {
    const frames = splitFrames(body);
    if (frames.length !== 1) return null;
    const q = readQrp(frames[0]);
    return q.KIND() === qrpKind.Error ? errorText(q) : null;
  } catch {
    return null;
  }
}

function assertNotError(q: QRP): void {
  if (q.KIND() === qrpKind.Error) throw new Error(errorText(q));
}

/** CIDv1 (raw codec, sha2-256) of the record bytes, base32 lower ("b..."): the store's id. */
export function recordCid(bytes: Uint8Array): string {
  const digest = createHash("sha256").update(bytes).digest();
  const cid = new Uint8Array(4 + digest.length);
  cid.set([0x01, 0x55, 0x12, 0x20]);
  cid.set(digest, 4);
  const alphabet = "abcdefghijklmnopqrstuvwxyz234567";
  let out = "b";
  let bits = 0;
  let value = 0;
  for (const byte of cid) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += alphabet[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) out += alphabet[(value << (5 - bits)) & 31];
  return out;
}

/** Decodes a /data/query answer: its cursors and every record as CID + size + file identifier. */
export function decodeQrpQueryAnswer(body: Uint8Array): QrpQueryAnswer {
  const frames = splitFrames(body);
  if (frames.length === 0) throw new Error("query answered an empty body");
  let answer: QrpQueryAnswer | null = null;
  for (let k = 0; k < frames.length; ) {
    const header = readQrp(frames[k++]);
    assertNotError(header);
    if (header.KIND() !== qrpKind.Page) throw new Error("query run does not start with a $QRP page header");
    answer ??= {
      schema: header.SCHEMA_NAME() ?? "",
      page: header.PAGE(),
      limit: header.LIMIT(),
      total_count: Number(header.TOTAL_COUNT()),
      count: 0,
      generated_at_ms: Number(header.GENERATED_AT_MS()),
      records: [],
    };
    const count = header.RECORD_COUNT();
    if (k + count > frames.length) throw new Error(`query run declares ${count} records, ${frames.length - k} frames remain`);
    const lane = {
      provider_id: header.PROVIDER_ID() ?? "",
      source_name: header.SOURCE_NAME() ?? "",
      batch_id: header.BATCH_ID() ?? "",
      producer_peer_id: header.PRODUCER_PEER_ID() ?? "",
      origin_id: header.ORIGIN_ID() ?? "",
    };
    for (const bytes of frames.slice(k, k + count)) {
      const fileIdentifier = bytes.byteLength >= 8 ? Buffer.from(bytes.subarray(4, 8)).toString("latin1") : "";
      answer.records.push({ cid: recordCid(bytes), size_bytes: bytes.byteLength, file_identifier: fileIdentifier, ...lane });
    }
    k += count;
  }
  answer!.count = answer!.records.length;
  return answer!;
}

function decodeCell(cell: QRPValue): QrpCell {
  switch (cell.KIND()) {
    case qrpValueKind.Bool:
      return cell.BOOL_VALUE();
    case qrpValueKind.Int:
      return Number(cell.INT_VALUE());
    case qrpValueKind.Float:
      return cell.FLOAT_VALUE();
    case qrpValueKind.Text:
      return cell.TEXT_VALUE() ?? "";
    case qrpValueKind.Bytes:
      return cell.bytesValueArray() ?? new Uint8Array(0);
    default:
      return null;
  }
}

/** Decodes a COLUMNS + ROWS page into rows keyed by column NAME. */
export function decodeQrpTable(body: Uint8Array): QrpTable {
  const frames = splitFrames(body);
  if (frames.length !== 1) throw new Error(`table page answered ${frames.length} frames, want 1`);
  const q = readQrp(frames[0]);
  assertNotError(q);
  if (q.KIND() !== qrpKind.Page) throw new Error("table answer is not a $QRP page");
  const column = new QRPColumn();
  const names: string[] = [];
  for (let j = 0; j < q.columnsLength(); j++) {
    q.COLUMNS(j, column);
    names.push(column.NAME() ?? "");
  }
  const row = new QRPRow();
  const cell = new QRPValue();
  const rows: Array<Record<string, QrpCell>> = [];
  for (let i = 0; i < q.rowsLength(); i++) {
    q.ROWS(i, row);
    const out: Record<string, QrpCell> = {};
    names.forEach((name, j) => {
      out[name] = j < row.cellsLength() && row.CELLS(j, cell) ? decodeCell(cell) : null;
    });
    rows.push(out);
  }
  return { status: q.STATUS(), generated_at_ms: Number(q.GENERATED_AT_MS()), as_of_ms: Number(q.AS_OF_MS()), rows };
}
