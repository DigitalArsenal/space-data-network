import { sha256 } from '../../crypto/hd-wallet';
import type { RawDataRecord } from './sdn-backend';

export interface PublishedFlatSqlSegmentInput {
  schema: string;
  providerPeerId: string;
  cid: string;
  indexCid?: string;
  shardSha256?: string;
  fetchCidBytes(cid: string): Promise<Uint8Array>;
}

export interface TimedPublishedFlatSqlSegment {
  streamBytes: Uint8Array;
  networkTransferMs: number;
  verificationMs: number;
}

export interface PublishedFlatSqlShardCarImportInput {
  cid: string;
  sha256?: string;
  fetchCidBytes(cid: string): Promise<Uint8Array>;
  importCarBytes(bytes: Uint8Array): Promise<void>;
}

export interface TimedPublishedFlatSqlShardCarImport {
  cid: string;
  byteLength: number;
  networkTransferMs: number;
  verificationMs: number;
  importMs: number;
}

interface DatasetExportIndex {
  version?: number;
  schemaName?: string;
  shardSha256?: string;
  shardCid?: string;
  recordCount?: number;
  records?: DatasetExportIndexRecord[];
}

interface DatasetExportIndexRecord {
  cid?: string;
  offset?: number;
  length?: number;
  sourceTags?: {
    providerId?: string;
    sourceName?: string;
    batchId?: string;
    producerPeerId?: string;
    producerPublicKey?: string;
  };
}

const textDecoder = new TextDecoder();

export async function flatBufferStreamFromPublishedFlatSqlSegment(input: PublishedFlatSqlSegmentInput): Promise<Uint8Array> {
  return (await timedFlatBufferStreamFromPublishedFlatSqlSegment(input)).streamBytes;
}

export async function timedFlatBufferStreamFromPublishedFlatSqlSegment(input: PublishedFlatSqlSegmentInput): Promise<TimedPublishedFlatSqlSegment> {
  const networkStartedAt = Date.now();
  const shardBytes = await input.fetchCidBytes(input.cid);
  const networkTransferMs = Math.max(0, Date.now() - networkStartedAt);
  const verificationStartedAt = Date.now();
  const expectedSha = input.shardSha256?.trim();
  if (expectedSha) {
    const actualSha = await sha256Hex(shardBytes);
    if (actualSha !== expectedSha) {
      throw new Error(`shard SHA-256 mismatch for ${input.cid}`);
    }
  }
  assertFlatSqlSizePrefixedStream(shardBytes);
  const verificationMs = Math.max(0, Date.now() - verificationStartedAt);
  return { streamBytes: shardBytes, networkTransferMs, verificationMs };
}

export async function importPublishedFlatSqlShardCar(input: PublishedFlatSqlShardCarImportInput): Promise<TimedPublishedFlatSqlShardCarImport> {
  const cid = input.cid.trim();
  if (!cid) throw new Error('published shard CAR CID is required');
  const networkStartedAt = Date.now();
  const carBytes = await input.fetchCidBytes(cid);
  const networkTransferMs = Math.max(0, Date.now() - networkStartedAt);
  const verificationStartedAt = Date.now();
  const expectedSha = input.sha256?.trim();
  if (expectedSha) {
    const actualSha = await sha256Hex(carBytes);
    if (actualSha !== expectedSha) {
      throw new Error(`shard group CAR SHA-256 mismatch for ${cid}`);
    }
  }
  const verificationMs = Math.max(0, Date.now() - verificationStartedAt);
  const importStartedAt = Date.now();
  await input.importCarBytes(carBytes);
  const importMs = Math.max(0, Date.now() - importStartedAt);
  return {
    cid,
    byteLength: carBytes.byteLength,
    networkTransferMs,
    verificationMs,
    importMs,
  };
}

export async function importCarBytesToKubo(ipfsApiUrl: string, carBytes: Uint8Array): Promise<void> {
  const base = ipfsApiUrl.trim().replace(/\/+$/, '');
  if (!base) throw new Error('local IPFS API URL is required for CAR import');
  if (typeof FormData === 'undefined' || typeof Blob === 'undefined') {
    throw new Error('CAR import requires FormData and Blob support');
  }
  const form = new FormData();
  form.append('file', new Blob([arrayBufferBlobPart(carBytes)], { type: 'application/vnd.ipld.car' }), 'sdn-shard-group.car');
  const response = await fetch(`${base}/api/v0/dag/import?pin-roots=true&stats=false`, {
    method: 'POST',
    body: form,
  });
  if (!response.ok) {
    const text = await response.text().catch(() => '');
    throw new Error(`IPFS CAR import failed with HTTP ${response.status}${text ? `: ${text.trim()}` : ''}`);
  }
}

function arrayBufferBlobPart(bytes: Uint8Array): ArrayBuffer {
  if (bytes.buffer instanceof ArrayBuffer) {
    if (bytes.byteOffset === 0 && bytes.byteLength === bytes.buffer.byteLength) {
      return bytes.buffer;
    }
    return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
  }
  return new Uint8Array(bytes).buffer;
}

export async function rawRecordsFromPublishedFlatSqlSegment(input: PublishedFlatSqlSegmentInput): Promise<RawDataRecord[]> {
  const shardBytes = await input.fetchCidBytes(input.cid);
  if (!input.indexCid) throw new Error('published shard materialized index CID is required for raw record hydration');
  const indexBytes = await input.fetchCidBytes(input.indexCid);
  const expectedSha = input.shardSha256?.trim();
  if (expectedSha) {
    const actualSha = await sha256Hex(shardBytes);
    if (actualSha !== expectedSha) {
      throw new Error(`shard SHA-256 mismatch for ${input.cid}`);
    }
  }

  const index = parseDatasetExportIndex(indexBytes);
  const schemaName = index.schemaName?.trim() || input.schema;
  if (schemaName !== input.schema) {
    throw new Error(`published shard schema ${schemaName} does not match requested ${input.schema}`);
  }
  if (typeof index.recordCount === 'number' && Array.isArray(index.records) && index.recordCount !== index.records.length) {
    throw new Error(`published shard index count ${index.recordCount} does not match ${index.records.length} records`);
  }
  if (index.shardSha256 && expectedSha && index.shardSha256 !== expectedSha) {
    throw new Error(`published shard index SHA-256 mismatch for ${input.cid}`);
  }

  const records = Array.isArray(index.records) ? index.records : [];
  return records.map((record) => rawRecordFromIndexRecord(input, schemaName, shardBytes, record));
}

export async function fetchCidBytesFromGateway(gatewayUrl: string, cid: string): Promise<Uint8Array> {
  const base = gatewayUrl.trim().replace(/\/+$/, '');
  if (!base) throw new Error('local IPFS gateway URL is required');
  const response = await fetch(`${base}/ipfs/${encodeURIComponent(cid)}`);
  if (!response.ok) throw new Error(`fetch CID ${cid} failed with HTTP ${response.status}`);
  return new Uint8Array(await response.arrayBuffer());
}

function rawRecordFromIndexRecord(
  input: PublishedFlatSqlSegmentInput,
  schemaName: string,
  shardBytes: Uint8Array,
  record: DatasetExportIndexRecord,
): RawDataRecord {
  const cid = record.cid?.trim();
  if (!cid) throw new Error('published shard index record is missing CID');
  const offset = integerField(record.offset, `record ${cid} offset`);
  const length = integerField(record.length, `record ${cid} length`);
  if (offset < 0 || length < 0 || offset + 4 + length > shardBytes.byteLength) {
    throw new Error(`record ${cid} offset/length outside shard`);
  }
  const view = new DataView(shardBytes.buffer, shardBytes.byteOffset + offset, 4);
  const frameLength = view.getUint32(0, true);
  if (frameLength !== length) {
    throw new Error(`record ${cid} frame length ${frameLength} does not match index length ${length}`);
  }
  const dataBytes = shardBytes.slice(offset + 4, offset + 4 + length);
  const tags = record.sourceTags ?? {};
  return {
    schemaName,
    cid,
    peerId: input.providerPeerId,
    providerId: tags.providerId,
    sourceName: tags.sourceName,
    batchId: tags.batchId,
    producerPeerId: tags.producerPeerId,
    producerPublicKey: tags.producerPublicKey,
    sizeBytes: dataBytes.byteLength,
    dataBytes,
  };
}

function assertFlatSqlSizePrefixedStream(streamBytes: Uint8Array): void {
  const view = new DataView(streamBytes.buffer, streamBytes.byteOffset, streamBytes.byteLength);
  let offset = 0;
  let index = 0;
  while (offset < streamBytes.byteLength) {
    if (streamBytes.byteLength - offset < 4) {
      throw new Error(`Invalid FlatSQL shard stream: truncated frame header at offset ${offset}`);
    }
    const length = view.getUint32(offset, true);
    offset += 4;
    if (offset + length > streamBytes.byteLength) {
      throw new Error(`Invalid FlatSQL shard stream: truncated frame at index ${index}`);
    }
    offset += length;
    index += 1;
  }
}

function parseDatasetExportIndex(indexBytes: Uint8Array): DatasetExportIndex {
  const parsed = JSON.parse(textDecoder.decode(indexBytes)) as unknown;
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('published shard index is not a JSON object');
  }
  const index = parsed as DatasetExportIndex;
  if (index.version !== 1) throw new Error(`published shard index version ${index.version ?? 'missing'} is unsupported`);
  return index;
}

function integerField(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

async function sha256Hex(bytes: Uint8Array): Promise<string> {
  return bytesToHex(await sha256(bytes));
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes).map((byte) => byte.toString(16).padStart(2, '0')).join('');
}
