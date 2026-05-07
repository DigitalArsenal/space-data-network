import {
  createAvailableResult,
  createCapability,
  createDegradedResult,
  createUnavailableResult,
  normalizeBackendConfig,
  type BackendCapability,
  type BackendResult,
  type LocalObjectSummary,
  type NodeSummary,
  type ObservedSdnPeer,
  type PartialSdnBackendConfig,
  type SdnBackend,
  type SdnBackendConfig,
  type StorageSummary,
} from './sdn-backend';
import {
  getJson,
  joinUrl,
  nodeSummaryFromProfile,
  normalizeObjectPayload,
  normalizePeerPayload,
  normalizeStorageSummary,
  resolveFetch,
  type BackendDeps,
} from './sdn-backend-adapter-utils';

export type RemoteSdnBackendOptions = PartialSdnBackendConfig & Partial<SdnBackendConfig> & BackendDeps;

export function createRemoteSdnBackend(options: RemoteSdnBackendOptions): SdnBackend {
  const config = normalizeBackendConfig({ ...options, mode: 'remote-sdn' });
  const fetchLike = resolveFetch(options.fetch);
  const serverBase = config.serverUrl;

  async function getNodeProfile(): Promise<BackendResult<Record<string, unknown>>> {
    return getJson<Record<string, unknown>>(fetchLike, joinUrl(serverBase, '/api/node/epm/json'), 'getNodeProfile');
  }

  async function getNodeSummary(): Promise<BackendResult<NodeSummary>> {
    const profile = await getNodeProfile();
    if (profile.ok && profile.data) {
      return createAvailableResult('getNodeSummary', nodeSummaryFromProfile(profile.data, 'remote-sdn'));
    }
    return createDegradedResult('getNodeSummary', profile.capability.reason ?? 'remote node profile unavailable');
  }

  return {
    mode: 'remote-sdn',
    connect: getNodeSummary,
    async getCapabilities(): Promise<BackendCapability[]> {
      return [
        createCapability('remote-sdn', serverBase ? 'available' : 'unavailable', serverBase ? undefined : 'remote server URL is not configured'),
        createCapability('wallet-core', 'local-only', 'wallet and Core operations must run on a local node'),
      ];
    },
    getNodeSummary,
    getNodeProfile,
    async saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createUnavailableResult('saveNodeProfile', `remote profile editing requires an explicit permission flow (${Object.keys(profile).length} fields)`);
    },
    async listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      const primary = await getJson<unknown>(fetchLike, joinUrl(serverBase, '/api/peers/sdn'), 'listObservedPeers');
      if (primary.ok) return createAvailableResult('listObservedPeers', normalizePeerPayload(primary.data));
      const fallback = await getJson<unknown>(fetchLike, joinUrl(serverBase, '/api/peers'), 'listObservedPeers');
      if (!fallback.ok) return primary as BackendResult<ObservedSdnPeer[]>;
      return createAvailableResult('listObservedPeers', normalizePeerPayload(fallback.data));
    },
    async getStorageSummary(): Promise<BackendResult<StorageSummary>> {
      const summary = await getJson<unknown>(fetchLike, joinUrl(serverBase, '/api/v1/data/storage'), 'getStorageSummary');
      if (!summary.ok) return summary as BackendResult<StorageSummary>;
      return createAvailableResult('getStorageSummary', normalizeStorageSummary(summary.data));
    },
    async listObjects(): Promise<BackendResult<LocalObjectSummary[]>> {
      const objects = await getJson<unknown>(fetchLike, joinUrl(serverBase, '/api/v1/data/objects'), 'listObjects');
      if (!objects.ok) return objects as BackendResult<LocalObjectSummary[]>;
      return createAvailableResult('listObjects', normalizeObjectPayload(objects.data));
    },
    async runSqlQuery(query: string): Promise<BackendResult<Array<Record<string, unknown>>>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(serverBase, '/api/v1/data/query'),
        'runSqlQuery',
        {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ query }),
        },
      );
      if (!result.ok) return result as BackendResult<Array<Record<string, unknown>>>;
      if (Array.isArray(result.data)) return createAvailableResult('runSqlQuery', result.data.filter(isRecord));
      if (isRecord(result.data) && Array.isArray(result.data.results)) {
        return createAvailableResult('runSqlQuery', result.data.results.filter(isRecord));
      }
      return createAvailableResult('runSqlQuery', []);
    },
    async resolveCid(cid: string): Promise<BackendResult<{ cid: string; gatewayUrl: string }>> {
      if (!serverBase) return createUnavailableResult('resolveCid', 'remote server URL is not configured');
      return createAvailableResult('resolveCid', {
        cid,
        gatewayUrl: joinUrl(serverBase, `/ipfs/${encodeURIComponent(cid)}`),
      });
    },
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}
