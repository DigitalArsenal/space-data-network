import {
  createAvailableResult,
  createDegradedResult,
  type BackendResult,
  type LocalObjectSummary,
  type NodeSummary,
  type ObservedSdnPeer,
  type SdnBackendMode,
  type StorageSummary,
} from './sdn-backend';

export type FetchLike = (url: string, init?: RequestInit) => Promise<Response>;

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

export function normalizeStorageSummary(payload: unknown): StorageSummary {
  const record = isRecord(payload) ? payload : {};
  return {
    usedBytes: readNumber(record, 'used_bytes', 'usedBytes', 'repo_size', 'RepoSize') ?? null,
    pinnedBytes: readNumber(record, 'pinned_bytes', 'pinnedBytes') ?? null,
    cacheBytes: readNumber(record, 'cache_bytes', 'cacheBytes') ?? null,
    quotaBytes: readNumber(record, 'quota_bytes', 'quotaBytes', 'storage_max', 'StorageMax') ?? null,
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
