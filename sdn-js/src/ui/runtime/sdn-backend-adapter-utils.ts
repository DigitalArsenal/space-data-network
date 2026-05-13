import {
  createAvailableResult,
  createDegradedResult,
  type BackendResult,
  type DataScanResult,
  type DataSummary,
  type LocalObjectSummary,
  type NodeAccessUser,
  type NodeSummary,
  type ObservedSdnPeer,
  type RawDataRecord,
  type RawDataStreamRequest,
  type SdnBackendMode,
  type StorageSummary,
} from './sdn-backend';

export type FetchLike = (url: string, init?: RequestInit) => Promise<Response>;
export const RAW_FLATBUFFER_STREAM_CONTENT_TYPE = 'application/vnd.sdn.flatbuffers.stream';

export interface BackendDeps {
  fetch?: FetchLike;
}

export function resolveFetch(fetchLike?: FetchLike): FetchLike {
  if (fetchLike) return fetchLike;
  if (typeof globalThis.fetch === 'function') {
    return globalThis.fetch.bind(globalThis) as FetchLike;
  }
  throw new Error('fetch is unavailable in this runtime');
}

export function joinUrl(base: string | null | undefined, path: string): string {
  const normalizedPath = path.startsWith('/') ? path : `/${path}`;
  if (!base) return normalizedPath;
  return `${base.replace(/\/+$/, '')}${normalizedPath}`;
}

export async function getJson<T>(
  fetchLike: FetchLike,
  url: string,
  capabilityId: string,
  init?: RequestInit,
): Promise<BackendResult<T>> {
  try {
    const response = await fetchLike(url, init);
    if (!response.ok) {
      return createDegradedResult(capabilityId, `${url} returned HTTP ${response.status}`);
    }
    return createAvailableResult(capabilityId, await response.json() as T);
  } catch (error) {
    return createDegradedResult(capabilityId, error instanceof Error ? error.message : String(error));
  }
}

export async function getBytes(
  fetchLike: FetchLike,
  url: string,
  capabilityId: string,
  init?: RequestInit,
): Promise<BackendResult<Uint8Array>> {
  try {
    const response = await fetchLike(url, init);
    if (!response.ok) {
      return createDegradedResult(capabilityId, `${url} returned HTTP ${response.status}`);
    }
    return createAvailableResult(capabilityId, new Uint8Array(await response.arrayBuffer()));
  } catch (error) {
    return createDegradedResult(capabilityId, error instanceof Error ? error.message : String(error));
  }
}

export function nodeSummaryFromProfile(
  profile: Record<string, unknown>,
  runtime: SdnBackendMode,
): NodeSummary {
  const peerId = readString(profile, 'peer_id', 'peerId', 'PeerID', 'ID');
  return {
    displayName: readString(profile, 'dn', 'display_name', 'displayName', 'name') ?? 'Space Data Network',
    peerId,
    agentVersion: readString(profile, 'agent_version', 'agentVersion', 'AgentVersion') ?? null,
    online: Boolean(peerId),
    runtime,
  };
}

export function normalizePeerPayload(payload: unknown): ObservedSdnPeer[] {
  const records = recordsFromPayload(payload);
  return records.map(normalizePeerRecord).filter((peer): peer is ObservedSdnPeer => peer !== null);
}

export function normalizeObjectPayload(payload: unknown): LocalObjectSummary[] {
  return recordsFromPayload(payload).map((record, index) => {
    const cid = readString(record, 'cid', 'CID');
    const id = readString(record, 'id', 'object_id', 'objectId') ?? cid ?? `object-${index + 1}`;
    return {
      id,
      label: readString(record, 'label', 'name', 'object_name', 'objectName') ?? id,
      schema: readString(record, 'schema', 'schema_name', 'schemaName') ?? null,
      source: readString(record, 'source', 'source_name', 'sourceName', 'source_provider_id') ?? null,
      sizeBytes: readNumber(record, 'size_bytes', 'sizeBytes', 'size') ?? null,
      state: readString(record, 'state', 'status') ?? 'stored',
      ...(cid ? { cid } : {}),
    };
  });
}

export function recordsFromPayload(payload: unknown): Array<Record<string, unknown>> {
  if (Array.isArray(payload)) return payload.filter(isRecord);
  if (!isRecord(payload)) return [];
  for (const key of ['peers', 'results', 'items', 'objects', 'records', 'wallets', 'epms', 'rulesets', 'files']) {
    const value = payload[key];
    if (Array.isArray(value)) return value.filter(isRecord);
  }
  return [payload];
}

export function normalizeNodeAccessPayload(payload: unknown): NodeAccessUser[] {
  return recordsFromPayload(payload).map(normalizeNodeAccessUser).filter((user): user is NodeAccessUser => user !== null);
}

export function normalizeStorageSummary(payload: unknown): StorageSummary {
  const record = isRecord(payload) ? payload : {};
  return {
    usedBytes: readNumber(record, 'used_bytes', 'usedBytes', 'repo_size', 'RepoSize') ?? null,
    pinnedBytes: readNumber(record, 'pinned_bytes', 'pinnedBytes') ?? null,
    cacheBytes: readNumber(record, 'cache_bytes', 'cacheBytes') ?? null,
    quotaBytes: readNumber(record, 'quota_bytes', 'quotaBytes', 'storage_max', 'StorageMax') ?? null,
  };
}

export function normalizeDataSummary(payload: unknown): DataSummary {
  const record = isRecord(payload) ? payload : {};
  return {
    totalRecords: readNumber(record, 'total_records', 'totalRecords') ?? 0,
    totalBytes: readNumber(record, 'total_bytes', 'totalBytes') ?? 0,
    schemas: recordsFromValue(record.schemas).map((entry) => ({
      schemaName: readString(entry, 'schema_name', 'schemaName') ?? 'unknown',
      count: readNumber(entry, 'count') ?? 0,
      totalBytes: readNumber(entry, 'total_bytes', 'totalBytes') ?? 0,
    })),
    sources: recordsFromValue(record.sources).map((entry) => ({
      datastoreKey: readString(entry, 'datastore_key', 'datastoreKey') ?? undefined,
      schemaName: readString(entry, 'schema_name', 'schemaName') ?? 'unknown',
      providerId: readString(entry, 'provider_id', 'providerId') ?? '',
      sourceName: readString(entry, 'source_name', 'sourceName') ?? '',
      batchId: readString(entry, 'batch_id', 'batchId') ?? '',
      producerPeerId: readString(entry, 'producer_peer_id', 'producerPeerId') ?? '',
      producerPublicKey: readString(entry, 'producer_public_key', 'producerPublicKey') ?? '',
      count: readNumber(entry, 'count') ?? 0,
      totalBytes: readNumber(entry, 'total_bytes', 'totalBytes') ?? 0,
    })),
  };
}

export function normalizeDataScanResult(payload: unknown): DataScanResult {
  const record = isRecord(payload) ? payload : {};
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
    results: normalizeRawDataRecords(record),
  };
}

export function normalizeRawDataRecords(payload: unknown): RawDataRecord[] {
  return recordsFromPayload(payload).map((record): RawDataRecord => {
    const dataBase64 = readString(record, 'data_base64', 'dataBase64') ?? undefined;
    return {
      schemaName: readString(record, 'schema_name', 'schemaName') ?? 'unknown',
      cid: readString(record, 'cid', 'CID', 'id') ?? '',
      peerId: readString(record, 'peer_id', 'peerId') ?? '',
      providerId: readString(record, 'provider_id', 'providerId') ?? undefined,
      sourceName: readString(record, 'source_name', 'sourceName') ?? undefined,
      batchId: readString(record, 'batch_id', 'batchId') ?? undefined,
      producerPeerId: readString(record, 'producer_peer_id', 'producerPeerId') ?? undefined,
      producerPublicKey: readString(record, 'producer_public_key', 'producerPublicKey') ?? undefined,
      timestamp: readString(record, 'timestamp') ?? undefined,
      sizeBytes: readNumber(record, 'size_bytes', 'sizeBytes') ?? 0,
      ...(dataBase64 ? { dataBase64 } : {}),
    };
  }).filter((record) => record.schemaName !== 'unknown' && record.cid !== '');
}

export function parseRawFlatbufferStream(bytes: Uint8Array): Uint8Array[] {
  const records: Uint8Array[] = [];
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let offset = 0;
  while (offset < bytes.byteLength) {
    if (offset + 4 > bytes.byteLength) throw new Error('truncated raw FlatBuffer stream frame header');
    const length = view.getUint32(offset, false);
    offset += 4;
    if (offset + length > bytes.byteLength) throw new Error('truncated raw FlatBuffer stream frame payload');
    records.push(bytes.slice(offset, offset + length));
    offset += length;
  }
  return records;
}

export function attachRawFlatbufferStream(records: RawDataRecord[], streamBytes: Uint8Array): RawDataRecord[] {
  const payloads = parseRawFlatbufferStream(streamBytes);
  return records.map((record, index) => ({
    ...record,
    ...(payloads[index] ? { dataBytes: payloads[index] } : {}),
  }));
}

export function rawDataStreamPayload(request: RawDataStreamRequest): Record<string, unknown> {
  return {
    schema: request.schema,
    ...(request.datastoreKey ? { datastore_key: request.datastoreKey } : {}),
    ...(request.scanHash ? { scan_hash: request.scanHash } : {}),
    ...(request.chunkHash ? { chunk_hash: request.chunkHash } : {}),
    ...(request.snapshotId ? { snapshot_id: request.snapshotId } : {}),
    ...(request.head ? { head: request.head } : {}),
    ...(request.cursor ? { cursor: request.cursor } : {}),
    ...(request.nextCursor ? { next_cursor: request.nextCursor } : {}),
    ...(typeof request.totalCount === 'number' ? { total_count: request.totalCount } : {}),
    ...(request.highWaterMark ? { high_water_mark: request.highWaterMark } : {}),
    ...(request.queryProfile ? { query_profile: request.queryProfile } : {}),
    records: request.records.map((record) => ({
      schema_name: record.schemaName,
      cid: record.cid,
      peer_id: record.peerId,
      ...(record.providerId ? { provider_id: record.providerId } : {}),
      ...(record.sourceName ? { source_name: record.sourceName } : {}),
      ...(record.batchId ? { batch_id: record.batchId } : {}),
    })),
  };
}

function normalizePeerRecord(record: Record<string, unknown>): ObservedSdnPeer | null {
  const metadata = isRecord(record.metadata) ? record.metadata : {};
  const id = readString(record, 'id', 'peer_id', 'peerId', 'PeerID');
  if (!id) return null;
  const protocols = normalizeProtocols(record.protocols ?? metadata.protocols);
  const agentVersion = readString(record, 'agent_version', 'agentVersion') ?? readString(metadata, 'agent_version', 'agentVersion');
  if (!isLikelyLibp2pPeerId(id) || !hasSdnIdentityEvidence(record, metadata, protocols, agentVersion)) return null;
  return {
    id,
    name: readString(record, 'name', 'display_name', 'displayName', 'dn') ?? id,
    addrs: readStringArray(record.addrs ?? record.multiaddrs ?? record.addresses),
    trustLevel: readString(record, 'trust_level', 'trustLevel', 'trust', 'state') ?? 'observed',
    ...(agentVersion ? { agentVersion } : {}),
    ...(protocols.length > 0 ? { protocols } : {}),
  };
}

function recordsFromValue(value: unknown): Array<Record<string, unknown>> {
  if (!Array.isArray(value)) return [];
  return value.filter(isRecord);
}

function normalizeProtocols(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0);
  }
  if (typeof value === 'string') {
    return value.split(/[,\s]+/).map((entry) => entry.trim()).filter(Boolean);
  }
  return [];
}

function isLikelyLibp2pPeerId(value: string): boolean {
  return /^(12D3Koo|16Uiu2H|Qm)[1-9A-HJ-NP-Za-km-z]{20,}$/.test(value);
}

function hasSdnIdentityEvidence(
  record: Record<string, unknown>,
  metadata: Record<string, unknown>,
  protocols: string[],
  agentVersion: string | null,
): boolean {
  const evidence = [
    agentVersion,
    readString(record, 'source', 'kind', 'type'),
    readString(metadata, 'source', 'kind', 'type'),
    ...protocols,
  ].filter((entry): entry is string => Boolean(entry));
  return evidence.some((entry) => /space-data-network|spacedatanetwork|sdn/i.test(entry));
}

function readStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0);
}

function readString(record: Record<string, unknown>, ...keys: string[]): string | null {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim().length > 0) return value.trim();
  }
  return null;
}

function normalizeNodeAccessUser(record: Record<string, unknown>): NodeAccessUser | null {
  const xpub = readString(record, 'xpub', 'XPub');
  if (!xpub) return null;
  const source = readString(record, 'source', 'Source') ?? 'database';
  return {
    xpub,
    name: readString(record, 'name', 'Name') ?? '',
    trustLevel: readString(record, 'trust_level', 'trustLevel', 'TrustLevel') ?? 'untrusted',
    signingPubKeyHex: readString(record, 'signing_pubkey_hex', 'signingPubKeyHex', 'SigningPubKeyHex') ?? '',
    source,
    configManaged: source === 'config',
    ...(readString(record, 'created_at', 'createdAt', 'CreatedAt') ? { createdAt: readString(record, 'created_at', 'createdAt', 'CreatedAt') ?? undefined } : {}),
    ...(readString(record, 'last_login', 'lastLogin', 'LastLogin') ? { lastLogin: readString(record, 'last_login', 'lastLogin', 'LastLogin') ?? undefined } : {}),
  };
}

function readNumber(record: Record<string, unknown>, ...keys: string[]): number | null {
  for (const key of keys) {
    const value = record[key];
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
