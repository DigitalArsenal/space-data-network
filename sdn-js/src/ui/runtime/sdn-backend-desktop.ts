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

export type DesktopLocalBackendOptions = PartialSdnBackendConfig & BackendDeps;

export function createDesktopLocalBackend(options: DesktopLocalBackendOptions = {}): SdnBackend {
  const config = normalizeBackendConfig({ ...options, mode: 'desktop-local' });
  const fetchLike = resolveFetch(options.fetch);
  const desktopBase = config.desktopProxyUrl;
  const kuboBase = config.kuboApiUrl;
  const gatewayBase = config.gatewayUrl ?? 'http://127.0.0.1:8081';

  async function getNodeProfile(): Promise<BackendResult<Record<string, unknown>>> {
    return getJson<Record<string, unknown>>(fetchLike, joinUrl(desktopBase, '/api/node/epm/json'), 'getNodeProfile');
  }

  async function getNodeSummary(): Promise<BackendResult<NodeSummary>> {
    const profile = await getNodeProfile();
    if (profile.ok && profile.data) {
      return createAvailableResult('getNodeSummary', nodeSummaryFromProfile(profile.data, 'desktop-local'));
    }
    const kubo = await getJson<Record<string, unknown>>(
      fetchLike,
      joinUrl(kuboBase, '/api/v0/id'),
      'getNodeSummary',
      { method: 'POST' },
    );
    if (kubo.ok && kubo.data) {
      return createAvailableResult('getNodeSummary', nodeSummaryFromProfile(kubo.data, 'desktop-local'));
    }
    return createDegradedResult('getNodeSummary', profile.capability.reason ?? kubo.capability.reason ?? 'node summary unavailable');
  }

  return {
    mode: 'desktop-local',
    connect: getNodeSummary,
    async getCapabilities(): Promise<BackendCapability[]> {
      return [
        createCapability('kubo-rpc', kuboBase ? 'available' : 'unavailable', kuboBase ? undefined : 'Kubo RPC URL is not configured'),
        createCapability('desktop-proxy', desktopBase ? 'available' : 'degraded', desktopBase ? undefined : 'using relative desktop routes'),
        createCapability('browser-node', 'local-only', 'desktop-local uses daemon Kubo rather than browser-node'),
      ];
    },
    getNodeSummary,
    getNodeProfile,
    async saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createUnavailableResult('saveNodeProfile', `profile editing is not wired in desktop-local yet (${Object.keys(profile).length} fields)`);
    },
    async listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      const peers = await getJson<unknown>(fetchLike, joinUrl(desktopBase, '/api/peers/sdn'), 'listObservedPeers');
      if (!peers.ok) return peers as BackendResult<ObservedSdnPeer[]>;
      return createAvailableResult('listObservedPeers', normalizePeerPayload(peers.data));
    },
    async getStorageSummary(): Promise<BackendResult<StorageSummary>> {
      const summary = await getJson<unknown>(
        fetchLike,
        joinUrl(kuboBase, '/api/v0/repo/stat'),
        'getStorageSummary',
        { method: 'POST' },
      );
      if (!summary.ok) return summary as BackendResult<StorageSummary>;
      return createAvailableResult('getStorageSummary', normalizeStorageSummary(summary.data));
    },
    async listObjects(): Promise<BackendResult<LocalObjectSummary[]>> {
      const objects = await getJson<unknown>(fetchLike, joinUrl(desktopBase, '/api/v1/data/objects'), 'listObjects');
      if (!objects.ok) return objects as BackendResult<LocalObjectSummary[]>;
      return createAvailableResult('listObjects', normalizeObjectPayload(objects.data));
    },
    async runSqlQuery(query: string): Promise<BackendResult<Array<Record<string, unknown>>>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/v1/data/query'),
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
      return createAvailableResult('resolveCid', {
        cid,
        gatewayUrl: `${gatewayBase.replace(/\/+$/, '')}/ipfs/${encodeURIComponent(cid)}`,
      });
    },
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}
