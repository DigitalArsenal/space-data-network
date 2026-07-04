import {
  createAvailableResult,
  createCapability,
  createCapabilityResult,
  createDegradedResult,
  createUnavailableResult,
  normalizeBackendConfig,
  type BackendCapability,
  type BackendResult,
  type ConjunctionScreenRequest,
  type ConjunctionScreenResult,
  type DataScanResult,
  type DataSummary,
  type LocalObjectSummary,
  type NodeAccessUser,
  type NodeAccessUserInput,
  type NodeSummary,
  type ObservedSdnPeer,
  type PartialSdnBackendConfig,
  type RawDataQuery,
  type RawDataRecord,
  type RawDataRecordBytes,
  type RawDataStreamRequest,
  type SearchResult,
  type SharedSearchRequest,
  type DataSearchRow,
  type ProviderSearchRow,
  type SdnBackend,
  type SdnBackendConfig,
  type StorageSummary,
} from './sdn-backend';
import {
  getBytes,
  getJson,
  joinUrl,
  conjunctionScreenPayload,
  nodeSummaryFromProfile,
  normalizeConjunctionScreenResult,
  normalizeDataSearchResult,
  normalizeDataScanResult,
  normalizeDataSummary,
  normalizeNodeAccessPayload,
  normalizeObjectPayload,
  normalizePeerPayload,
  normalizeProviderSearchResult,
  attachRawFlatbufferStream,
  normalizeStorageSummary,
  authRawFlatbufferStreamRequest,
  queryRawDataViaStream,
  rawDataQueryPayload,
  rawDataStreamPayload,
  sharedSearchPayload,
  recordsFromPayload,
  resolveFetch,
  type BackendDeps,
} from './sdn-backend-adapter-utils';
import { createHttpChannelBackend } from './channel-backend';
import { decodeEpmFlatBuffer } from './epm-flatbuffer';
import { normalizeHostedEpmRecord, type HostedEpmRecord } from './identity';

export type RemoteSdnBackendOptions = PartialSdnBackendConfig & Partial<SdnBackendConfig> & BackendDeps;

export function createRemoteSdnBackend(options: RemoteSdnBackendOptions): SdnBackend {
  const config = normalizeBackendConfig({ ...options, mode: 'remote-sdn' });
  const fetchLike = resolveFetch(options.fetch);
  const serverBase = config.serverUrl;

  async function getNodeProfile(): Promise<BackendResult<Record<string, unknown>>> {
    const result = await getBytes(fetchLike, joinUrl(serverBase, '/api/node/epm'), 'getNodeProfile', {
      headers: { accept: 'application/x-flatbuffers' },
    });
    if (!result.ok || !result.data) {
      return createDegradedResult('getNodeProfile', result.capability.reason ?? 'node EPM FlatBuffer unavailable');
    }
    try {
      return createAvailableResult('getNodeProfile', decodeEpmFlatBuffer(result.data));
    } catch (error) {
      return createDegradedResult('getNodeProfile', error instanceof Error ? error.message : String(error));
    }
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
    channels: createHttpChannelBackend(fetchLike, serverBase),
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
      const result = await getBytes(
        fetchLike,
        joinUrl(serverBase, '/api/node/epm'),
        'saveNodeProfile',
        authJsonRequest('PUT', profile),
      );
      if (!result.ok || !result.data) {
        return createDegradedResult('saveNodeProfile', result.capability.reason ?? 'updated node EPM FlatBuffer unavailable');
      }
      try {
        return createAvailableResult('saveNodeProfile', decodeEpmFlatBuffer(result.data));
      } catch (error) {
        return createDegradedResult('saveNodeProfile', error instanceof Error ? error.message : String(error));
      }
    },
    async getNodeIdentitySettings() {
      return createCapabilityResult('getNodeIdentitySettings', 'local-only', 'node identity unlock settings are local to the desktop app', { ttlMs: 3_600_000 });
    },
    async saveNodeIdentitySettings(settings) {
      return createCapabilityResult('saveNodeIdentitySettings', 'local-only', 'node identity unlock settings are local to the desktop app', settings);
    },
    async selectFlatbufferStorageLocation() {
      return createCapabilityResult('selectFlatbufferStorageLocation', 'local-only', 'FlatBuffer storage locations are selected on the local desktop app', {
        canceled: true,
        path: null,
      });
    },
    async applyWalletNodeIdentity() {
      return createCapabilityResult('applyWalletNodeIdentity', 'local-only', 'wallet-backed node identity changes must run on the local desktop node');
    },
    async logoutNodeIdentity() {
      return createAvailableResult('logoutNodeIdentity', { ok: true });
    },
    async getWalletStorage() {
      return createCapabilityResult('getWalletStorage', 'local-only', 'wallet storage is local to the desktop app', {
        entries: {},
        encryptedAtRest: false,
      });
    },
    async saveWalletStorage(entries: Record<string, string | null>) {
      return createCapabilityResult('saveWalletStorage', 'local-only', 'wallet storage is local to the desktop app', {
        entries: Object.fromEntries(
          Object.entries(entries).filter((entry): entry is [string, string] => typeof entry[1] === 'string'),
        ),
        encryptedAtRest: false,
      });
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
    async listNodeAccessUsers(): Promise<BackendResult<NodeAccessUser[]>> {
      const users = await getJson<unknown>(
        fetchLike,
        joinUrl(serverBase, '/api/auth/users'),
        'listNodeAccessUsers',
        authRequest(),
      );
      if (!users.ok) return users as BackendResult<NodeAccessUser[]>;
      return createAvailableResult('listNodeAccessUsers', normalizeNodeAccessPayload(users.data));
    },
    async saveNodeAccessUser(user: NodeAccessUserInput): Promise<BackendResult<Record<string, unknown>>> {
      return getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(serverBase, '/api/auth/users'),
        'saveNodeAccessUser',
        authJsonRequest('POST', {
          xpub: user.xpub,
          name: user.name ?? '',
          trust_level: user.trustLevel,
          signing_pubkey_hex: user.signingPubKeyHex ?? '',
        }),
      );
    },
    async revokeNodeAdmin(xpub: string): Promise<BackendResult<Record<string, unknown>>> {
      return getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(serverBase, `/api/auth/users/${encodeURIComponent(xpub)}`),
        'revokeNodeAdmin',
        authJsonRequest('PUT', { trust_level: 'standard' }),
      );
    },
    async deleteNodeAccessUser(xpub: string): Promise<BackendResult<Record<string, unknown>>> {
      return getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(serverBase, `/api/auth/users/${encodeURIComponent(xpub)}`),
        'deleteNodeAccessUser',
        authRequest('DELETE'),
      );
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
    async getDataSummary(): Promise<BackendResult<DataSummary>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(serverBase, '/api/v1/data/summary'),
        'getDataSummary',
        authRequest(),
      );
      if (!result.ok) return createDegradedResult('getDataSummary', result.capability.reason ?? 'data summary unavailable');
      return createAvailableResult('getDataSummary', normalizeDataSummary(result.data));
    },
    async searchProviders(request: SharedSearchRequest): Promise<BackendResult<SearchResult<ProviderSearchRow>>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(serverBase, '/api/v1/search/providers'),
        'searchProviders',
        authJsonRequest('POST', sharedSearchPayload(request)),
      );
      if (!result.ok) return result as BackendResult<SearchResult<ProviderSearchRow>>;
      return createAvailableResult('searchProviders', normalizeProviderSearchResult(result.data));
    },
    async searchData(request: SharedSearchRequest): Promise<BackendResult<SearchResult<DataSearchRow>>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(serverBase, '/api/v1/search/data'),
        'searchData',
        authJsonRequest('POST', sharedSearchPayload(request)),
      );
      if (!result.ok) return result as BackendResult<SearchResult<DataSearchRow>>;
      return createAvailableResult('searchData', normalizeDataSearchResult(result.data));
    },
    async scanRawData(query: RawDataQuery): Promise<BackendResult<DataScanResult>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(serverBase, '/api/v1/data/scan'),
        'scanRawData',
        authJsonRequest('POST', rawDataQueryPayload(query)),
      );
      if (!result.ok) return result as BackendResult<DataScanResult>;
      return createAvailableResult('scanRawData', normalizeDataScanResult(result.data));
    },
    async streamRawData(request: RawDataStreamRequest): Promise<BackendResult<RawDataRecord[]>> {
      const stream = await getBytes(
        fetchLike,
        joinUrl(serverBase, '/api/v1/data/stream'),
        'streamRawData',
        authRawFlatbufferStreamRequest(rawDataStreamPayload(request)),
      );
      if (!stream.ok || !stream.data) {
        return createDegradedResult('streamRawData', stream.capability.reason ?? 'raw FlatBuffer stream unavailable');
      }
      return createAvailableResult('streamRawData', attachRawFlatbufferStream(request.records, stream.data));
    },
    async queryRawData(query: RawDataQuery): Promise<BackendResult<RawDataRecord[]>> {
      // ONE stream request (loop D.3): the aligned FlatBuffer record stream
      // plus its X-SDN-* headers replace the base64-era JSON metadata
      // round-trip that used to precede it.
      return queryRawDataViaStream(fetchLike, serverBase, 'queryRawData', query);
    },
    async readRawDataRecord(schemaName: string, cid: string): Promise<BackendResult<RawDataRecordBytes>> {
      const result = await getBytes(
        fetchLike,
        joinUrl(serverBase, `/api/v1/data/records/${encodeURIComponent(schemaName)}/${encodeURIComponent(cid)}`),
        'readRawDataRecord',
        authRequest(),
      );
      if (!result.ok || !result.data) {
        return createDegradedResult('readRawDataRecord', result.capability.reason ?? 'raw FlatBuffer record unavailable');
      }
      return createAvailableResult('readRawDataRecord', { schemaName, cid, bytes: result.data });
    },
    async screenConjunction(request: ConjunctionScreenRequest): Promise<BackendResult<ConjunctionScreenResult>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(serverBase, '/api/v1/conjunction/screen'),
        'screenConjunction',
        authJsonRequest('POST', conjunctionScreenPayload(request)),
      );
      if (!result.ok) return result as BackendResult<ConjunctionScreenResult>;
      return createAvailableResult('screenConjunction', normalizeConjunctionScreenResult(result.data));
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
    async runSqlQuery(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('runSqlQuery', 'local-only', 'SQL queries run against the selected local FlatSQL datastore after preview or sync', []);
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

function authRequest(method?: string): RequestInit {
  return {
    ...(method ? { method } : {}),
    credentials: 'include',
    headers: { 'x-requested-with': 'sdn-ui' },
  };
}

function authJsonRequest(method: string, body: Record<string, unknown>): RequestInit {
  return {
    method,
    credentials: 'include',
    headers: {
      'content-type': 'application/json',
      'x-requested-with': 'sdn-ui',
    },
    body: JSON.stringify(body),
  };
}

