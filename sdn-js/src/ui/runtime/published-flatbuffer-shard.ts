import { sha256 } from '../../crypto/hd-wallet';
import type { RawDataRecord } from './sdn-backend';

export interface PublishedFlatSqlSegmentInput {
  schema: string;
  providerPeerId: string;
  cid: string;
  indexCid: string;
  shardSha256?: string;
  fetchCidBytes(cid: string): Promise<Uint8Array>;
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

export async function rawRecordsFromPublishedFlatSqlSegment(input: PublishedFlatSqlSegmentInput): Promise<RawDataRecord[]> {
  const shardBytes = await input.fetchCidBytes(input.cid);
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
  const frameLength = view.getUint32(0, false);
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
