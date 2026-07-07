import {
  createAvailableResult,
  createDegradedResult,
  type BackendResult,
  type ConjunctionEvent,
  type ConjunctionScreenRequest,
  type ConjunctionScreenResult,
  type DataScanResult,
  type DataSummary,
  type LocalObjectSummary,
  type NodeAccessUser,
  type NodeSummary,
  type ObservedSdnPeer,
  type RawDataQuery,
  type RawDataRecord,
  type RawDataStreamRequest,
  type SearchResult,
  type SharedSearchRequest,
  type DataSearchRow,
  type ProviderSearchRow,
  type SdnBackendMode,
  type StorageSummary,
} from './sdn-backend';
import { normalizeIpfsArtifactPeerAddrs } from './ipfs-artifact-peers';
import { sha256 } from '../../crypto/hd-wallet';

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
    const id = readString(record, 'id', 'OBJECT_ID', 'object_id', 'objectId') ?? cid ?? `object-${index + 1}`;
    return {
      id,
      label: readString(record, 'label', 'name', 'OBJECT_NAME', 'object_name', 'objectName') ?? id,
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
    };
  }).filter((record) => record.schemaName !== 'unknown' && record.cid !== '');
}

/**
 * Parse an aligned size-prefixed FlatBuffer record stream. The wire format
 * is u32 LITTLE-ENDIAN length prefixes — the FlatSQL engine framing the
 * server advertises as `X-SDN-Stream-Format: flatsql-size-prefixed-le-u32`
 * (sdn-server writeFlatBufferPayloadStream / WriteRawRecordFrames). Frames
 * are zero-copy subarray views into `bytes`.
 */
export function parseRawFlatbufferStream(bytes: Uint8Array): Uint8Array[] {
  const records: Uint8Array[] = [];
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let offset = 0;
  while (offset < bytes.byteLength) {
    if (offset + 4 > bytes.byteLength) throw new Error('truncated raw FlatBuffer stream frame header');
    const length = view.getUint32(offset, true);
    offset += 4;
    if (offset + length > bytes.byteLength) throw new Error('truncated raw FlatBuffer stream frame payload');
    records.push(bytes.subarray(offset, offset + length));
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

/** Datasync raw-query JSON payload (shared by the remote + desktop adapters). */
export function rawDataQueryPayload(query: RawDataQuery): Record<string, unknown> {
  return {
    schema: query.schema,
    include_data: false,
    ...(query.datastoreKey ? { datastore_key: query.datastoreKey } : {}),
    ...(query.providerId ? { provider_id: query.providerId } : {}),
    ...(query.sourceName ? { source_name: query.sourceName } : {}),
    ...(query.batchId ? { batch_id: query.batchId } : {}),
    ...(query.peerId ? { peer_id: query.peerId } : {}),
    ...(query.cursor ? { cursor: query.cursor } : {}),
    ...(query.snapshotId ? { snapshot_id: query.snapshotId } : {}),
    ...(query.head ? { head: query.head } : {}),
    ...(query.queryProfile ? { query_profile: query.queryProfile } : {}),
    ...(query.syncFilter ? { sync_filter: query.syncFilter } : {}),
    ...(typeof query.limit === 'number' ? { limit: query.limit } : {}),
    ...(typeof query.offset === 'number' ? { offset: query.offset } : {}),
  };
}

/** Authenticated POST that asks for the aligned FlatBuffer record stream. */
export function authRawFlatbufferStreamRequest(body: Record<string, unknown>): RequestInit {
  return {
    method: 'POST',
    credentials: 'include',
    headers: {
      'content-type': 'application/json',
      'x-requested-with': 'sdn-ui',
      accept: RAW_FLATBUFFER_STREAM_CONTENT_TYPE,
    },
    body: JSON.stringify(body),
  };
}

/**
 * Raw data query in ONE stream request (loop D.3 — the base64-era double
 * round-trip is gone): POST `/api/v1/data/query` with the FlatBuffer-stream
 * Accept header; the response headers carry the schema
 * (`X-SDN-Schema`) and record count (`X-SDN-Record-Count`), the body is the
 * aligned size-prefixed record stream. Record identity is reconstructed
 * client-side: cid = sha256 hex of the payload bytes — exactly the server's
 * computeCID (sdn-server internal/storage/flatsql.go) — and the query's own
 * provider/source/batch filters stamp the provenance columns they pinned.
 */
export async function queryRawDataViaStream(
  fetchLike: FetchLike,
  baseUrl: string | null | undefined,
  capabilityId: string,
  query: RawDataQuery,
): Promise<BackendResult<RawDataRecord[]>> {
  const url = joinUrl(baseUrl, '/api/v1/data/query');
  try {
    const response = await fetchLike(url, authRawFlatbufferStreamRequest(rawDataQueryPayload(query)));
    if (!response.ok) {
      return createDegradedResult(capabilityId, `${url} returned HTTP ${response.status}`);
    }
    const streamBytes = new Uint8Array(await response.arrayBuffer());
    const schemaName = response.headers?.get?.('x-sdn-schema') ?? query.schema;
    const frames = parseRawFlatbufferStream(streamBytes);
    const records = await Promise.all(frames.map(async (frame): Promise<RawDataRecord> => ({
      schemaName,
      cid: await sha256Hex(frame),
      peerId: query.peerId ?? '',
      ...(query.providerId ? { providerId: query.providerId } : {}),
      ...(query.sourceName ? { sourceName: query.sourceName } : {}),
      ...(query.batchId ? { batchId: query.batchId } : {}),
      sizeBytes: frame.byteLength,
      dataBytes: frame,
    })));
    return createAvailableResult(capabilityId, records);
  } catch (error) {
    return createDegradedResult(capabilityId, error instanceof Error ? error.message : String(error));
  }
}

async function sha256Hex(data: Uint8Array): Promise<string> {
  const digest = await sha256(new Uint8Array(data));
  let hex = '';
  for (const byte of digest) hex += byte.toString(16).padStart(2, '0');
  return hex;
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
    ...(request.syncFilter ? { sync_filter: request.syncFilter } : {}),
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

export function conjunctionScreenPayload(request: ConjunctionScreenRequest): Record<string, unknown> {
  const primarySchema = normalizeSchemaName(request.primarySchema, 'MPE.fbs');
  const secondarySchema = normalizeSchemaName(request.secondarySchema, 'OMM.fbs');
  return {
    primary_schema: primarySchema,
    secondary_schema: secondarySchema,
    encrypted: request.encrypted !== false,
    include_provenance: request.includeProvenance !== false,
    sources: [
      conjunctionSourcePayload('primary', primarySchema, request.primaryProviderId, request.primarySourceName, request.primaryPnmCid, request.primaryQuery, request.encrypted !== false),
      conjunctionSourcePayload('secondary', secondarySchema, request.secondaryProviderId, request.secondarySourceName, request.secondaryPnmCid, request.secondaryQuery, false),
    ],
    module: {
      id: request.moduleId || 'com.space-data-network.conjunction-assessment',
      version: request.moduleVersion || 'latest',
    },
    ...(request.grantId ? { grant_id: request.grantId } : {}),
    ...(request.channelId ? { channel_id: request.channelId } : {}),
    ...(request.resultChannelId ? { result_channel_id: request.resultChannelId } : {}),
    ...(request.assessorPeerId ? { assessor_peer_id: request.assessorPeerId } : {}),
    ...(typeof request.limit === 'number' && Number.isFinite(request.limit) ? { limit: request.limit } : {}),
  };
}

function conjunctionSourcePayload(
  role: 'primary' | 'secondary',
  schema: string,
  providerId: string | undefined,
  sourceName: string | undefined,
  pnmCid: string | undefined,
  query: string | undefined,
  encrypted: boolean,
): Record<string, unknown> {
  return {
    role,
    schema,
    ...(providerId ? { provider_id: providerId } : {}),
    ...(sourceName ? { source_name: sourceName } : {}),
    ...(pnmCid ? { pnm_cid: pnmCid } : {}),
    ...(query ? { query } : {}),
    encrypted,
  };
}

export function sharedSearchPayload(request: SharedSearchRequest): Record<string, unknown> {
  return {
    ...(request.query ? { query: request.query } : {}),
    ...(request.schema ? { schema: request.schema } : {}),
    ...(request.providerId ? { provider_id: request.providerId } : {}),
    ...(request.providerPeerId ? { provider_peer_id: request.providerPeerId } : {}),
    ...(request.sourceName ? { source_name: request.sourceName } : {}),
    ...(request.batchId ? { batch_id: request.batchId } : {}),
    ...(request.queryProfile ? { query_profile: request.queryProfile } : {}),
    ...(request.mode ? { mode: request.mode } : {}),
    ...(typeof request.limit === 'number' ? { limit: request.limit } : {}),
  };
}

export function normalizeProviderSearchResult(payload: unknown): SearchResult<ProviderSearchRow> {
  const record = isRecord(payload) ? payload : {};
  const results = recordsFromPayload(record).map(normalizeProviderSearchRow);
  return {
    count: readNumber(record, 'count') ?? results.length,
    results,
  };
}

export function normalizeDataSearchResult(payload: unknown): SearchResult<DataSearchRow> {
  const record = isRecord(payload) ? payload : {};
  const results = recordsFromPayload(record).map(normalizeDataSearchRow);
  return {
    count: readNumber(record, 'count') ?? results.length,
    results,
  };
}

function normalizeProviderSearchRow(record: Record<string, unknown>): ProviderSearchRow {
  return {
    ...record,
    peerId: readString(record, 'peer_id', 'peerId') ?? undefined,
    displayName: readString(record, 'dn', 'display_name', 'displayName', 'name') ?? undefined,
    legalName: readString(record, 'legal_name', 'legalName') ?? undefined,
    bitcoinAddress: readString(record, 'bitcoin_address', 'bitcoinAddress') ?? undefined,
    epmCid: readString(record, 'epm_cid', 'epmCid') ?? undefined,
    source: readString(record, 'source') ?? undefined,
    updatedAt: readString(record, 'updated_at', 'updatedAt') ?? undefined,
    schemaName: readString(record, 'schema_name', 'schemaName') ?? undefined,
    providerPeerId: readString(record, 'provider_peer_id', 'providerPeerId') ?? undefined,
    providerPublicKey: readString(record, 'provider_public_key', 'providerPublicKey') ?? undefined,
    providerId: readString(record, 'provider_id', 'providerId') ?? undefined,
    sourceName: readString(record, 'source_name', 'sourceName') ?? undefined,
    batchId: readString(record, 'batch_id', 'batchId') ?? undefined,
    queryProfile: readString(record, 'query_profile', 'queryProfile') ?? undefined,
    localRows: readNumber(record, 'local_rows', 'localRows') ?? undefined,
    pinnedRows: readNumber(record, 'pinned_rows', 'pinnedRows') ?? undefined,
    cachedBytes: readNumber(record, 'cached_bytes', 'cachedBytes') ?? undefined,
    pinnedBytes: readNumber(record, 'pinned_bytes', 'pinnedBytes') ?? undefined,
    snapshotId: readString(record, 'snapshot_id', 'snapshotId') ?? undefined,
    head: readString(record, 'head') ?? undefined,
    highWaterMark: readString(record, 'high_water_mark', 'highWaterMark') ?? undefined,
    lastSyncedAt: readString(record, 'last_synced_at', 'lastSyncedAt') ?? undefined,
  };
}

function normalizeDataSearchRow(record: Record<string, unknown>): DataSearchRow {
  return {
    ...record,
    schemaName: readString(record, 'schema_name', 'schemaName') ?? undefined,
    providerId: readString(record, 'provider_id', 'providerId') ?? undefined,
    sourceName: readString(record, 'source_name', 'sourceName') ?? undefined,
    batchId: readString(record, 'batch_id', 'batchId') ?? undefined,
    queryProfile: readString(record, 'query_profile', 'queryProfile') ?? undefined,
    providerPeerId: readString(record, 'provider_peer_id', 'providerPeerId') ?? undefined,
    providerPublicKey: readString(record, 'provider_public_key', 'providerPublicKey') ?? undefined,
    localRows: readNumber(record, 'local_rows', 'localRows') ?? undefined,
    pinnedRows: readNumber(record, 'pinned_rows', 'pinnedRows') ?? undefined,
    cachedBytes: readNumber(record, 'cached_bytes', 'cachedBytes') ?? undefined,
    pinnedBytes: readNumber(record, 'pinned_bytes', 'pinnedBytes') ?? undefined,
    snapshotId: readString(record, 'snapshot_id', 'snapshotId') ?? undefined,
    head: readString(record, 'head') ?? undefined,
    highWaterMark: readString(record, 'high_water_mark', 'highWaterMark') ?? undefined,
    lastSyncedAt: readString(record, 'last_synced_at', 'lastSyncedAt') ?? undefined,
  };
}

export function normalizeConjunctionScreenResult(payload: unknown): ConjunctionScreenResult {
  const record = isRecord(payload) ? payload : {};
  const events = recordsFromValue(record.events).map(normalizeConjunctionEvent);
  return {
    workflow: readString(record, 'workflow') ?? 'encrypted-conjunction-assessment',
    mode: readString(record, 'mode') ?? 'private-maneuver-ephemeris',
    status: readString(record, 'status') ?? undefined,
    primarySchema: readString(record, 'primary_schema', 'primarySchema') ?? 'MPE.fbs',
    secondarySchema: readString(record, 'secondary_schema', 'secondarySchema') ?? 'OMM.fbs',
    encrypted: readBoolean(record, 'encrypted') ?? false,
    grantId: readString(record, 'grant_id', 'grantId') ?? undefined,
    channelId: readString(record, 'channel_id', 'channelId') ?? undefined,
    resultChannelId: readString(record, 'result_channel_id', 'resultChannelId') ?? undefined,
    assessorPeerId: readString(record, 'assessor_peer_id', 'assessorPeerId') ?? undefined,
    count: readNumber(record, 'count') ?? events.length,
    events,
    provenance: isRecord(record.provenance) ? record.provenance : undefined,
    sources: recordsFromValue(record.sources),
  };
}

function normalizeConjunctionEvent(record: Record<string, unknown>): ConjunctionEvent {
  return {
    ...record,
    primaryObject: readString(record, 'primary_object', 'primaryObject') ?? undefined,
    secondaryObject: readString(record, 'secondary_object', 'secondaryObject') ?? undefined,
    tca: readString(record, 'tca', 'tca_time', 'tcaTime') ?? undefined,
    missDistanceKm: readNumber(record, 'miss_distance_km', 'missDistanceKm') ?? undefined,
    probability: readNumber(record, 'probability', 'pc', 'collision_probability') ?? undefined,
    providerId: readString(record, 'provider_id', 'providerId') ?? undefined,
    sourceName: readString(record, 'source_name', 'sourceName') ?? undefined,
    status: readString(record, 'status') ?? undefined,
  };
}

function normalizePeerRecord(record: Record<string, unknown>): ObservedSdnPeer | null {
  const metadata = isRecord(record.metadata) ? record.metadata : {};
  const id = readString(record, 'id', 'peer_id', 'peerId', 'PeerID');
  if (!id) return null;
  const protocols = normalizeProtocols(record.protocols ?? metadata.protocols);
  const agentVersion = readString(record, 'agent_version', 'agentVersion') ?? readString(metadata, 'agent_version', 'agentVersion');
  const artifactPeerAddrs = normalizeIpfsArtifactPeerAddrs(
    record.ipfs_artifact_addrs
      ?? record.ipfsArtifactAddrs
      ?? record.artifact_peer_addrs
      ?? record.artifactPeerAddrs
      ?? metadata.ipfs_artifact_addrs
      ?? metadata.ipfsArtifactAddrs
      ?? metadata.artifact_peer_addrs
      ?? metadata.artifactPeerAddrs,
  );
  if (!isLikelyLibp2pPeerId(id) || !hasSdnIdentityEvidence(record, metadata, protocols, agentVersion)) return null;
  return {
    id,
    name: readString(record, 'name', 'display_name', 'displayName', 'dn') ?? id,
    addrs: readStringArray(record.addrs ?? record.multiaddrs ?? record.addresses),
    trustLevel: readString(record, 'trust_level', 'trustLevel', 'trust', 'state') ?? 'observed',
    ...(agentVersion ? { agentVersion } : {}),
    ...(protocols.length > 0 ? { protocols } : {}),
    ...(artifactPeerAddrs.length > 0 ? { artifactPeerAddrs } : {}),
    ...(Object.keys(metadata).length > 0 ? { metadata } : {}),
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

function readBoolean(record: Record<string, unknown>, ...keys: string[]): boolean | null {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'boolean') return value;
    if (typeof value === 'string') {
      const normalized = value.trim().toLowerCase();
      if (normalized === 'true') return true;
      if (normalized === 'false') return false;
    }
  }
  return null;
}

function normalizeSchemaName(value: string, fallback: string): string {
  const trimmed = value.trim();
  if (!trimmed) return fallback;
  const withoutSuffix = trimmed.replace(/\.fbs$/i, '');
  return `${withoutSuffix.toUpperCase()}.fbs`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}
