import {
  createAvailableResult,
  createCapability,
  createCapabilityResult,
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
  recordsFromPayload,
  resolveFetch,
  type BackendDeps,
} from './sdn-backend-adapter-utils';
import { normalizeHostedEpmRecord, type HostedEpmRecord } from './identity';

export type RemoteSdnBackendOptions = PartialSdnBackendConfig & Partial<SdnBackendConfig> & BackendDeps;

export function createRemoteSdnBackend(options: RemoteSdnBackendOptions): SdnBackend {
  const config = normalizeBackendConfig({ ...options, mode: 'remote-sdn' });
  const fetchLike = resolveFetch(options.fetch);
  const serverBase = config.serverUrl;

  async function getNodeProfile(): Promise<BackendResult<Record<string, unknown>>> {
    const publicInfo = await getJson<Record<string, unknown>>(fetchLike, joinUrl(serverBase, '/api/node/info'), 'getNodeProfile');
    if (publicInfo.ok) return publicInfo;
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
    async getHealth() {
      const health = await getJson<unknown>(fetchLike, joinUrl(serverBase, '/api/v1/data/health'), 'getHealth');
      if (!health.ok) return createDegradedResult('getHealth', health.capability.reason ?? 'remote health route unavailable', { healthy: false, details: {} });
      return createAvailableResult('getHealth', { healthy: true, details: isRecord(health.data) ? health.data : { value: health.data } });
    },
    getNodeProfile,
    async saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createUnavailableResult('saveNodeProfile', `remote profile editing requires an explicit permission flow (${Object.keys(profile).length} fields)`);
    },
    async listWalletsAndEpms(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('listWalletsAndEpms', 'local-only', 'wallet and EPM management must run on a local node', []);
    },
    async beginClaimEpm(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('beginClaimEpm', 'local-only', 'EPM claim must run on a local node');
    },
    async exportCore(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('exportCore', 'local-only', 'Core export must run on a local node');
    },
    async importCore(core: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('importCore', 'local-only', `Core import must run on a local node (${Object.keys(core).length} fields)`);
    },
    async listHostedEpms(): Promise<BackendResult<HostedEpmRecord[]>> {
      const epms = await getJson<unknown>(fetchLike, joinUrl(serverBase, '/api/identity/epms'), 'listHostedEpms');
      if (!epms.ok) {
        const profile = await getNodeProfile();
        if (profile.ok && profile.data) {
          return createAvailableResult('listHostedEpms', [normalizeHostedEpmRecord({
            id: 'self',
            kind: 'node-self',
            epm_json: profile.data,
          })]);
        }
        return epms as BackendResult<HostedEpmRecord[]>;
      }
      return createAvailableResult('listHostedEpms', recordsFromPayload(epms.data).map(normalizeHostedEpmRecord));
    },
    async saveHostedEpm(record: HostedEpmRecord): Promise<BackendResult<HostedEpmRecord>> {
      return createCapabilityResult('saveHostedEpm', 'local-only', `editing hosted EPM ${record.id} must run on a local node`);
    },
    async importHostedEpm(input: { name: string }): Promise<BackendResult<HostedEpmRecord>> {
      return createCapabilityResult('importHostedEpm', 'local-only', `importing hosted EPM ${input.name} must run on a local node`);
    },
    async deleteHostedEpm(id: string): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('deleteHostedEpm', 'local-only', `deleting hosted EPM ${id} must run on a local node`);
    },
    async downloadHostedEpm(id: string, format: 'json' | 'epm' | 'vcard'): Promise<BackendResult<{ url: string; filename: string }>> {
      const suffix = format === 'json' ? '' : `/${format === 'vcard' ? 'vcard' : 'epm'}`;
      const extension = format === 'vcard' ? 'vcf' : format;
      return createAvailableResult('downloadHostedEpm', {
        url: joinUrl(serverBase, `/api/identity/epms/${encodeURIComponent(id)}${suffix}`),
        filename: `${id}.${extension}`,
      });
    },
    async listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      const primary = await getJson<unknown>(fetchLike, joinUrl(serverBase, '/api/peers/sdn'), 'listObservedPeers');
      if (primary.ok) return createAvailableResult('listObservedPeers', normalizePeerPayload(primary.data));
      const fallback = await getJson<unknown>(fetchLike, joinUrl(serverBase, '/api/peers'), 'listObservedPeers');
      if (!fallback.ok) return primary as BackendResult<ObservedSdnPeer[]>;
      return createAvailableResult('listObservedPeers', normalizePeerPayload(fallback.data));
    },
    async listTrustedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      const peers = await getJson<unknown>(fetchLike, joinUrl(serverBase, '/api/peers'), 'listTrustedPeers');
      if (!peers.ok) return peers as BackendResult<ObservedSdnPeer[]>;
      return createAvailableResult('listTrustedPeers', normalizePeerPayload(peers.data));
    },
    async searchDirectory(query: string): Promise<BackendResult<Array<Record<string, unknown>>>> {
      const [nodes, users] = await Promise.all([
        getJson<unknown>(fetchLike, joinUrl(serverBase, `/api/directory/nodes?q=${encodeURIComponent(query)}`), 'searchDirectory:nodes'),
        getJson<unknown>(fetchLike, joinUrl(serverBase, `/api/directory/users?q=${encodeURIComponent(query)}`), 'searchDirectory:users'),
      ]);
      const records = [
        ...(nodes.ok ? recordsFromPayload(nodes.data).map((record) => ({ ...record, directoryKind: 'node' })) : []),
        ...(users.ok ? recordsFromPayload(users.data).map((record) => ({ ...record, directoryKind: 'person' })) : []),
      ];
      if (!nodes.ok && !users.ok) {
        return createDegradedResult('searchDirectory', nodes.capability.reason ?? users.capability.reason ?? 'directory search unavailable', records);
      }
      return createAvailableResult('searchDirectory', records);
    },
    async connectPeer(peerId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('connectPeer', 'remote-only', `remote peer connection for ${peerId} requires server-side permission`);
    },
    async searchListings(query: string): Promise<BackendResult<Array<Record<string, unknown>>>> {
      const listings = await getJson<unknown>(fetchLike, joinUrl(serverBase, `/api/storefront/listings?q=${encodeURIComponent(query)}`), 'searchListings');
      if (!listings.ok) return listings as BackendResult<Array<Record<string, unknown>>>;
      return createAvailableResult('searchListings', recordsFromPayload(listings.data));
    },
    async listOwnedItems(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('listOwnedItems', 'permission-required', 'owned item lookup requires an authenticated remote session', []);
    },
    async requestGrant(listingId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('requestGrant', 'permission-required', `grant request for ${listingId} requires an authenticated remote session`);
    },
    async installModule(moduleId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('installModule', 'local-only', `module ${moduleId} installation must run on a local node`);
    },
    async subscribeDataFeed(feedId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('subscribeDataFeed', 'permission-required', `data feed ${feedId} subscription requires an authenticated remote session`);
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
    async inspectObject(id: string): Promise<BackendResult<LocalObjectSummary | Record<string, unknown>>> {
      const object = await getJson<unknown>(fetchLike, joinUrl(serverBase, `/api/v1/data/objects/${encodeURIComponent(id)}`), 'inspectObject');
      if (!object.ok) return object as BackendResult<Record<string, unknown>>;
      return createAvailableResult('inspectObject', isRecord(object.data) ? object.data : { id, value: object.data });
    },
    async pinObject(id: string): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('pinObject', 'local-only', `pinning ${id} must run on a local node`);
    },
    async unpinObject(id: string): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('unpinObject', 'local-only', `unpinning ${id} must run on a local node`);
    },
    async listRulesets(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('listRulesets', 'local-only', 'retention rulesets must run on a local node', []);
    },
    async saveRuleset(ruleset: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('saveRuleset', 'local-only', `ruleset persistence must run on a local node (${Object.keys(ruleset).length} fields)`);
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
    async getKuboStatus(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('getKuboStatus', 'local-only', 'Kubo RPC status is only available for local desktop nodes');
    },
    async listFiles(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('listFiles', 'local-only', 'MFS file browsing is only available for local desktop nodes', []);
    },
    async resolveCid(cid: string): Promise<BackendResult<{ cid: string; gatewayUrl: string }>> {
      if (!serverBase) return createUnavailableResult('resolveCid', 'remote server URL is not configured');
      return createAvailableResult('resolveCid', {
        cid,
        gatewayUrl: joinUrl(serverBase, `/ipfs/${encodeURIComponent(cid)}`),
      });
    },
    async readGatewayUrl(path: string): Promise<BackendResult<{ path: string; gatewayUrl: string }>> {
      if (!serverBase) return createUnavailableResult('readGatewayUrl', 'remote server URL is not configured');
      const gatewayPath = path.startsWith('/ipfs/') ? path : `/ipfs/${path}`;
      return createAvailableResult('readGatewayUrl', {
        path,
        gatewayUrl: joinUrl(serverBase, gatewayPath),
      });
    },
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}
