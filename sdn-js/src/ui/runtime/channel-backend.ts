import {
  createCapabilityResult,
  createAvailableResult,
  type BackendResult,
  type ChannelActionOptions,
  type ChannelBackend,
  type ChannelListOptions,
  type ChannelMonitor,
  type ChannelMonitorTimings,
  type ChannelSummary,
} from './sdn-backend';
import { getBytes, getJson, joinUrl, recordsFromPayload, type FetchLike } from './sdn-backend-adapter-utils';

export function createHttpChannelBackend(fetchLike: FetchLike, baseUrl: string | null | undefined): ChannelBackend {
  return {
    async list(options: ChannelListOptions = {}): Promise<BackendResult<ChannelSummary[]>> {
      const params = new URLSearchParams();
      if (options.standardCode) params.set('standardCode', options.standardCode);
      if (options.visibility) params.set('visibility', options.visibility);
      if (options.subject) params.set('subject', options.subject);
      if (options.grantId) params.set('grantId', options.grantId);
      const suffix = params.size > 0 ? `?${params.toString()}` : '';
      const result = await getJson<unknown>(fetchLike, joinUrl(baseUrl, `/api/v1/channels${suffix}`), 'channels.list');
      if (!result.ok) return result as BackendResult<ChannelSummary[]>;
      return createAvailableResult('channels.list', recordsFromPayload(result.data).map(normalizeChannelSummary));
    },
    async get(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<ChannelSummary>> {
      const result = await getJson<unknown>(fetchLike, channelDetailUrl(baseUrl, channelId, options), 'channels.get');
      if (!result.ok) return result as BackendResult<ChannelSummary>;
      return createAvailableResult('channels.get', normalizeChannelSummary(result.data));
    },
    subscribe(channelId: string, options?: ChannelActionOptions) {
      return postChannelAction(fetchLike, baseUrl, channelId, 'subscribe', undefined, undefined, options);
    },
    unsubscribe(channelId: string, options?: ChannelActionOptions) {
      return postChannelAction(fetchLike, baseUrl, channelId, 'unsubscribe', undefined, undefined, options);
    },
    publish(channelId: string, body?: BodyInit | null, options?: ChannelActionOptions) {
      return postChannelAction(fetchLike, baseUrl, channelId, 'publish', body, nativeStreamHeaders(body), options);
    },
    issueGrant(channelId: string, body: Record<string, unknown> = {}, options?: ChannelActionOptions) {
      return postChannelAction(fetchLike, baseUrl, channelId, 'grants', JSON.stringify(body), {
        'content-type': 'application/json',
      }, options);
    },
    openStream(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<Uint8Array>> {
      return getBytes(fetchLike, channelActionUrl(baseUrl, channelId, 'stream', options), 'channels.openStream', {
        headers: { accept: 'application/vnd.sdn.flatbuffers.stream' },
      });
    },
    async monitor(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<ChannelMonitor>> {
      const result = await getJson<unknown>(fetchLike, channelActionUrl(baseUrl, channelId, 'monitor', options), 'channels.monitor');
      if (!result.ok) return result as BackendResult<ChannelMonitor>;
      return createAvailableResult('channels.monitor', normalizeChannelMonitor(result.data));
    },
  };
}

export function createUnavailableChannelBackend(reason: string): ChannelBackend {
  return {
    list: () => Promise.resolve(createCapabilityResult('channels.list', 'unavailable', reason, [])),
    get: () => Promise.resolve(createCapabilityResult('channels.get', 'unavailable', reason)),
    subscribe: () => Promise.resolve(createCapabilityResult('channels.subscribe', 'unavailable', reason)),
    unsubscribe: () => Promise.resolve(createCapabilityResult('channels.unsubscribe', 'unavailable', reason)),
    publish: () => Promise.resolve(createCapabilityResult('channels.publish', 'unavailable', reason)),
    issueGrant: () => Promise.resolve(createCapabilityResult('channels.issueGrant', 'unavailable', reason)),
    openStream: () => Promise.resolve(createCapabilityResult('channels.openStream', 'unavailable', reason)),
    monitor: () => Promise.resolve(createCapabilityResult('channels.monitor', 'unavailable', reason)),
  };
}

function postChannelAction(
  fetchLike: FetchLike,
  baseUrl: string | null | undefined,
  channelId: string,
  action: string,
  body?: BodyInit | null,
  headers?: HeadersInit,
  options?: ChannelActionOptions,
): Promise<BackendResult<Record<string, unknown>>> {
  return getJson<Record<string, unknown>>(
    fetchLike,
    channelActionUrl(baseUrl, channelId, action, options),
    `channels.${action}`,
    {
      method: 'POST',
      ...(headers ? { headers } : {}),
      ...(body !== undefined ? { body } : {}),
    },
  );
}

function channelActionUrl(baseUrl: string | null | undefined, channelId: string, action: string, options?: ChannelActionOptions): string {
  return joinUrl(baseUrl, `/api/v1/channels/${encodeURIComponent(channelId)}/${action}${channelAccessQuerySuffix(options)}`);
}

function channelDetailUrl(baseUrl: string | null | undefined, channelId: string, options?: ChannelActionOptions): string {
  return joinUrl(baseUrl, `/api/v1/channels/${encodeURIComponent(channelId)}${channelAccessQuerySuffix(options)}`);
}

function channelAccessQuerySuffix(options?: ChannelActionOptions): string {
  const params = new URLSearchParams();
  if (options?.subject) params.set('subject', options.subject);
  if (options?.grantId) params.set('grantId', options.grantId);
  if (options?.visibility) params.set('visibility', options.visibility);
  return params.size > 0 ? `?${params.toString()}` : '';
}

function nativeStreamHeaders(body?: BodyInit | null): HeadersInit | undefined {
  if (body instanceof Uint8Array || body instanceof ArrayBuffer) {
    return { 'content-type': 'application/vnd.sdn.flatbuffers.stream' };
  }
  return undefined;
}

function normalizeChannelSummary(payload: unknown): ChannelSummary {
  const record = isRecord(payload) ? payload : {};
  const standardCode = pickString(record, 'standardCode') ?? '';
  return {
    channelId: pickString(record, 'channelId') ?? '',
    sourceId: pickString(record, 'sourceId') ?? '',
    standardCode,
    feedUuid: pickString(record, 'feedUuid') ?? null,
    visibility: pickString(record, 'visibility') ?? 'unknown',
    subscribed: pickBoolean(record, 'subscribed') ?? false,
    pnmVerified: pickBoolean(record, 'pnmVerified') ?? false,
    dpmVerified: pickBoolean(record, 'dpmVerified') ?? false,
    grantState: pickString(record, 'grantState') ?? 'unknown',
    encryptionState: pickString(record, 'encryptionState') ?? 'unknown',
  };
}

function normalizeChannelMonitor(payload: unknown): ChannelMonitor {
  const summary = normalizeChannelSummary(payload);
  const record = isRecord(payload) ? payload : {};
  return {
    ...summary,
    channelHead: pickString(record, 'channelHead') ?? '',
    providerPeer: pickString(record, 'providerPeer') ?? '',
    localRows: pickNumber(record, 'localRows') ?? 0,
    remoteRows: pickNumber(record, 'remoteRows') ?? 0,
    syncedRows: pickNumber(record, 'syncedRows') ?? 0,
    missingRows: pickNumber(record, 'missingRows') ?? 0,
    pinnedCount: pickNumber(record, 'pinnedCount') ?? pickNumber(record, 'pinnedRows') ?? 0,
    pinnedRows: pickNumber(record, 'pinnedRows') ?? pickNumber(record, 'pinnedCount') ?? 0,
    syncedBytes: pickNumber(record, 'syncedBytes') ?? 0,
    throughputBytesPerSecond: pickNumber(record, 'throughputBytesPerSecond') ?? 0,
    wireSpeedUtilization: pickNumber(record, 'wireSpeedUtilization'),
    timingsMs: normalizeChannelMonitorTimings(record.timingsMs),
    lastVerifiedUpdate: pickString(record, 'lastVerifiedUpdate') ?? '',
  };
}

function normalizeChannelMonitorTimings(payload: unknown): ChannelMonitorTimings {
  const record = isRecord(payload) ? payload : {};
  return {
    discovery: pickNumber(record, 'discovery') ?? 0,
    grantNegotiation: pickNumber(record, 'grantNegotiation') ?? 0,
    pnmDpmVerification: pickNumber(record, 'pnmDpmVerification') ?? 0,
    transfer: pickNumber(record, 'transfer') ?? 0,
    decrypt: pickNumber(record, 'decrypt') ?? 0,
    hashVerification: pickNumber(record, 'hashVerification') ?? 0,
    durableImport: pickNumber(record, 'durableImport') ?? 0,
  };
}

function pickString(record: Record<string, unknown>, key: string): string | null {
  const value = record[key];
  return typeof value === 'string' && value.trim() !== '' ? value : null;
}

function pickNumber(record: Record<string, unknown>, key: string): number | null {
  const value = record[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function pickBoolean(record: Record<string, unknown>, key: string): boolean | null {
  const value = record[key];
  return typeof value === 'boolean' ? value : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
