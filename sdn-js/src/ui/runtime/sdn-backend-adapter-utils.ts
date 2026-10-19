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
  type SearchResult,
  type SharedSearchRequest,
  type DataSearchRow,
  type ProviderSearchRow,
  type SdnBackendMode,
  type StorageSummary,
} from './sdn-backend';
import { normalizeIpfsArtifactPeerAddrs } from './ipfs-artifact-peers';
import { FLATBUFFER_STREAM_TYPE, decodeQrpError, decodeQrpScanStream, encodeQrpScanRequest } from './qrp-frames';
import {
  decodeQrpTablePage,
  qrpNumber,
  qrpPostInit,
  qrpRequestFromRawQuery,
  qrpRowObjects,
  qrpStatus,
  qrpText,
  type QrpTablePage,
  type QrpCell,
} from './qrp-request';

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

/**
 * GET of a route that answers one $QRP Page of COLUMNS + ROWS
 * (/api/v1/data/{health,summary,datastores,local-replica-stats,sources}).
 */
export async function getQrpTable(
  fetchLike: FetchLike,
  url: string,
  capabilityId: string,
  init?: RequestInit,
): Promise<BackendResult<QrpTablePage>> {
  try {
    const headers = new Headers(init?.headers);
    headers.set('accept', FLATBUFFER_STREAM_TYPE);
    const response = await fetchLike(url, { ...init, headers });
    const body = new Uint8Array(await response.arrayBuffer());
    if (!response.ok) {
      const error = decodeQrpError(body);
      return createDegradedResult(capabilityId, `${url} returned HTTP ${response.status}${error ? `: ${error.code} ${error.message}` : ''}`);
    }
    return createAvailableResult(capabilityId, decodeQrpTablePage(body));
  } catch (error) {
    return createDegradedResult(capabilityId, error instanceof Error ? error.message : String(error));
  }
}

/**
 * /api/v1/data/summary: one row per (schema, lane), then one total row per
 * schema (every lane field ''), then the store total (schema_name '' too).
 * Columns: schema_name, provider_id, source_name, batch_id,
 * producer_peer_id, producer_public_key, count, total_bytes.
 */
export function dataSummaryFromQrp(page: QrpTablePage): DataSummary {
  const rows = qrpRowObjects(page);
  const laneless = (row: Record<string, QrpCell>) =>
    ['provider_id', 'source_name', 'batch_id', 'producer_peer_id', 'producer_public_key'].every((name) => qrpText(row, name) === '');
  const store = rows.find((row) => laneless(row) && qrpText(row, 'schema_name') === '');
  const totals = rows.filter((row) => laneless(row) && qrpText(row, 'schema_name') !== '');
  const lanes = rows.filter((row) => !laneless(row));
  const schemas = totals.map((row) => ({
    schemaName: qrpText(row, 'schema_name'),
    count: qrpNumber(row, 'count') ?? 0,
    totalBytes: qrpNumber(row, 'total_bytes') ?? 0,
  }));
  const sources = lanes.map((row) => ({
    schemaName: qrpText(row, 'schema_name') || 'unknown',
    providerId: qrpText(row, 'provider_id'),
    sourceName: qrpText(row, 'source_name'),
    batchId: qrpText(row, 'batch_id'),
    producerPeerId: qrpText(row, 'producer_peer_id'),
    producerPublicKey: qrpText(row, 'producer_public_key'),
    count: qrpNumber(row, 'count') ?? 0,
    totalBytes: qrpNumber(row, 'total_bytes') ?? 0,
  }));
  const counted = schemas.length ? schemas : sources;
  return {
    totalRecords: store ? qrpNumber(store, 'count') ?? 0 : counted.reduce((total, entry) => total + entry.count, 0),
    totalBytes: store ? qrpNumber(store, 'total_bytes') ?? 0 : counted.reduce((total, entry) => total + entry.totalBytes, 0),
    schemas,
    sources,
  };
}

/** /api/v1/data/health: STATUS Ok (or Cold) plus rows [component, status]. */
export function healthFromQrp(page: QrpTablePage): { healthy: boolean; details: Record<string, unknown> } {
  const details: Record<string, unknown> = {
    status: page.status === qrpStatus.Ok ? 'ok' : page.status === qrpStatus.Cold ? 'cold' : String(page.status),
    generated_at_ms: page.generatedAtMs,
  };
  for (const row of qrpRowObjects(page)) {
    const component = qrpText(row, 'component');
    if (component) details[component] = qrpText(row, 'status');
  }
  return { healthy: page.status === qrpStatus.Ok, details };
}

/**
 * One page of the FlatBuffer-only scan (sdn-server data_scan.go): POST one
 * $QRP request frame, decode the $QRP page runs and their verbatim record
 * frames. Each record carries its bytes, its store CID and its lane.
 */
export async function scanRawDataViaQrp(
  fetchLike: FetchLike,
  baseUrl: string | null | undefined,
  capabilityId: string,
  query: RawDataQuery,
): Promise<BackendResult<DataScanResult>> {
  const url = joinUrl(baseUrl, '/api/v1/data/scan');
  try {
    const response = await fetchLike(url, {
      method: 'POST',
      credentials: 'include',
      headers: { 'content-type': FLATBUFFER_STREAM_TYPE, accept: FLATBUFFER_STREAM_TYPE, 'x-requested-with': 'sdn-ui' },
      body: encodeQrpScanRequest({
        schema: query.schema,
        providerId: query.providerId,
        sourceName: query.sourceName,
        batchId: query.batchId,
        originId: query.peerId,
        search: query.search,
        cursor: query.cursor,
        limit: query.limit,
      }) as BodyInit,
    });
    const body = new Uint8Array(await response.arrayBuffer());
    if (!response.ok) {
      const error = decodeQrpError(body);
      return createDegradedResult(capabilityId, `${url} returned HTTP ${response.status}${error ? `: ${error.code} ${error.message}` : ''}`);
    }
    const { page, records } = await decodeQrpScanStream(body);
    return createAvailableResult(capabilityId, {
      schema: page.schemaName,
      totalCount: page.totalCount,
      count: records.length,
      limit: page.limit,
      offset: 0,
      cursor: page.cursor,
      nextCursor: page.nextCursor,
      snapshotId: '',
      head: '',
      highWaterMark: '',
      scanHash: page.etag,
      chunkHash: page.etag,
      queryProfile: '',
      syncProtocol: 'http:/api/v1/data/scan',
      maxChunkSize: page.limit,
      transports: ['http'],
      results: records.map((record): RawDataRecord => ({
        schemaName: page.schemaName,
        cid: record.cid,
        peerId: record.originId,
        ...(record.providerId ? { providerId: record.providerId } : {}),
        ...(record.sourceName ? { sourceName: record.sourceName } : {}),
        ...(record.batchId ? { batchId: record.batchId } : {}),
        ...(record.producerPeerId ? { producerPeerId: record.producerPeerId } : {}),
        sizeBytes: record.bytes.byteLength,
        dataBytes: record.bytes,
      })),
    });
  } catch (error) {
    return createDegradedResult(capabilityId, error instanceof Error ? error.message : String(error));
  }
}

/**
 * Raw data query in ONE $QRP request (sdn-server /api/v1/data/query): POST
 * one $QRP{KIND=Request} frame, decode the page runs and their verbatim
 * record frames. Each record carries its store CID (CIDv1 raw, sha2-256 of
 * the frame bytes) and the lane its run header names.
 */
export async function queryRawDataViaStream(
  fetchLike: FetchLike,
  baseUrl: string | null | undefined,
  capabilityId: string,
  query: RawDataQuery,
): Promise<BackendResult<RawDataRecord[]>> {
  const url = joinUrl(baseUrl, '/api/v1/data/query');
  try {
    const response = await fetchLike(url, qrpPostInit(qrpRequestFromRawQuery(query)));
    const body = new Uint8Array(await response.arrayBuffer());
    if (!response.ok) {
      const error = decodeQrpError(body);
      return createDegradedResult(capabilityId, `${url} returned HTTP ${response.status}${error ? `: ${error.code} ${error.message}` : ''}`);
    }
    const { page, records } = await decodeQrpScanStream(body);
    return createAvailableResult(capabilityId, records.map((record): RawDataRecord => ({
      schemaName: page.schemaName,
      cid: record.cid,
      peerId: record.originId,
      ...(record.providerId ? { providerId: record.providerId } : {}),
      ...(record.sourceName ? { sourceName: record.sourceName } : {}),
      ...(record.batchId ? { batchId: record.batchId } : {}),
      ...(record.producerPeerId ? { producerPeerId: record.producerPeerId } : {}),
      sizeBytes: record.bytes.byteLength,
      dataBytes: record.bytes,
    })));
  } catch (error) {
    return createDegradedResult(capabilityId, error instanceof Error ? error.message : String(error));
  }
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
