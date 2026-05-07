import {
  createAvailableResult,
  createCapabilityResult,
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
  recordsFromPayload,
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
        ...['/api/peers/sdn', '/api/peers', '/api/peers/graph', '/api/node/epm/json', '/api/node/epm'].map((route) => (
          createCapability(`route:${route}`, desktopBase ? 'available' : 'degraded', desktopBase ? undefined : 'desktop proxy URL is not configured')
        )),
      ];
    },
    getNodeSummary,
    async getHealth() {
      const health = await getJson<unknown>(fetchLike, joinUrl(desktopBase, '/api/v1/data/health'), 'getHealth');
      if (!health.ok) return createDegradedResult('getHealth', health.capability.reason ?? 'desktop health route unavailable', { healthy: false, details: {} });
      return createAvailableResult('getHealth', { healthy: true, details: isRecord(health.data) ? health.data : { value: health.data } });
    },
    getNodeProfile,
    async saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createUnavailableResult('saveNodeProfile', `profile editing is not wired in desktop-local yet (${Object.keys(profile).length} fields)`);
    },
    async listWalletsAndEpms(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      const epm = await getJson<unknown>(fetchLike, joinUrl(desktopBase, '/api/node/epm'), 'listWalletsAndEpms');
      if (!epm.ok) return epm as BackendResult<Array<Record<string, unknown>>>;
      return createAvailableResult('listWalletsAndEpms', recordsFromPayload(epm.data));
    },
    async beginClaimEpm(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('beginClaimEpm', 'permission-required', 'EPM claim requires the wallet/Core flow');
    },
    async exportCore(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('exportCore', 'permission-required', 'Core export requires local wallet confirmation');
    },
    async importCore(core: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('importCore', 'permission-required', `Core import requires local wallet confirmation (${Object.keys(core).length} fields)`);
    },
    async listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      const peers = await getJson<unknown>(fetchLike, joinUrl(desktopBase, '/api/peers/sdn'), 'listObservedPeers');
      if (!peers.ok) return peers as BackendResult<ObservedSdnPeer[]>;
      return createAvailableResult('listObservedPeers', normalizePeerPayload(peers.data));
    },
    async listTrustedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      const peers = await getJson<unknown>(fetchLike, joinUrl(desktopBase, '/api/peers'), 'listTrustedPeers');
      if (!peers.ok) return peers as BackendResult<ObservedSdnPeer[]>;
      return createAvailableResult('listTrustedPeers', normalizePeerPayload(peers.data));
    },
    async searchDirectory(query: string): Promise<BackendResult<Array<Record<string, unknown>>>> {
      const graph = await getJson<unknown>(fetchLike, joinUrl(desktopBase, `/api/peers/graph?q=${encodeURIComponent(query)}`), 'searchDirectory');
      if (!graph.ok) return graph as BackendResult<Array<Record<string, unknown>>>;
      return createAvailableResult('searchDirectory', recordsFromPayload(graph.data));
    },
    async connectPeer(peerId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createUnavailableResult('connectPeer', `peer connection for ${peerId} is not wired in the Svelte UI adapter yet`);
    },
    async searchListings(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createDegradedResult('searchListings', 'marketplace catalog adapter is not wired to the Svelte UI yet', []);
    },
    async listOwnedItems(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createDegradedResult('listOwnedItems', 'owned marketplace library is not wired to the Svelte UI yet', []);
    },
    async requestGrant(listingId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('requestGrant', 'permission-required', `grant request for ${listingId} requires an authenticated purchase flow`);
    },
    async installModule(moduleId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('installModule', `module install for ${moduleId} is pending marketplace wiring`);
    },
    async subscribeDataFeed(feedId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('subscribeDataFeed', `data feed subscription for ${feedId} is pending marketplace wiring`);
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
    async inspectObject(id: string): Promise<BackendResult<LocalObjectSummary | Record<string, unknown>>> {
      const objects = await this.listObjects();
      const object = objects.data?.find((entry) => entry.id === id || entry.cid === id);
      if (object) return createAvailableResult('inspectObject', object);
      return createDegradedResult('inspectObject', `object ${id} is not available in the local index`);
    },
    async pinObject(id: string): Promise<BackendResult<Record<string, unknown>>> {
      const result = await getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(kuboBase, `/api/v0/pin/add?arg=${encodeURIComponent(id)}`),
        'pinObject',
        { method: 'POST' },
      );
      if (!result.ok) return result;
      return createAvailableResult('pinObject', result.data ?? {});
    },
    async unpinObject(id: string): Promise<BackendResult<Record<string, unknown>>> {
      const result = await getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(kuboBase, `/api/v0/pin/rm?arg=${encodeURIComponent(id)}`),
        'unpinObject',
        { method: 'POST' },
      );
      if (!result.ok) return result;
      return createAvailableResult('unpinObject', result.data ?? {});
    },
    async listRulesets(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createDegradedResult('listRulesets', 'local data rulesets endpoint is not wired yet', []);
    },
    async saveRuleset(ruleset: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('saveRuleset', `local data ruleset persistence is not wired yet (${Object.keys(ruleset).length} fields)`, ruleset);
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
    async getKuboStatus(): Promise<BackendResult<Record<string, unknown>>> {
      return getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(kuboBase, '/api/v0/id'),
        'getKuboStatus',
        { method: 'POST' },
      );
    },
    async listFiles(path = '/'): Promise<BackendResult<Array<Record<string, unknown>>>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(kuboBase, `/api/v0/files/ls?arg=${encodeURIComponent(path)}`),
        'listFiles',
        { method: 'POST' },
      );
      if (!result.ok) return result as BackendResult<Array<Record<string, unknown>>>;
      return createAvailableResult('listFiles', recordsFromPayload(result.data));
    },
    async resolveCid(cid: string): Promise<BackendResult<{ cid: string; gatewayUrl: string }>> {
      return createAvailableResult('resolveCid', {
        cid,
        gatewayUrl: gatewayUrlForPath(gatewayBase, `/ipfs/${cid}`),
      });
    },
    async readGatewayUrl(path: string): Promise<BackendResult<{ path: string; gatewayUrl: string }>> {
      return createAvailableResult('readGatewayUrl', {
        path,
        gatewayUrl: gatewayUrlForPath(gatewayBase, path.startsWith('/ipfs/') ? path : `/ipfs/${path}`),
      });
    },
  };
}

function gatewayUrlForPath(gatewayBase: string, path: string): string {
  return `${gatewayBase.replace(/\/+$/, '')}/${path.replace(/^\/+/, '').split('/').map(encodeURIComponent).join('/')}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}
