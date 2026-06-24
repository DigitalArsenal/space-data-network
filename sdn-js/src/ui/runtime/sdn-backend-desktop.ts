import {
  createAvailableResult,
  createCapabilityResult,
  createCapability,
  createDegradedResult,
  createUnavailableResult,
  normalizeBackendConfig,
  type BackendCapability,
  type BackendResult,
  type ConjunctionScreenRequest,
  type ConjunctionScreenResult,
  type DataScanResult,
  type DataSummary,
  type FlatbufferStorageLocationSelection,
  type LocalObjectSummary,
  type NodeAccessUser,
  type NodeAccessUserInput,
  type NodeIdentityApplyResult,
  type NodeIdentitySettings,
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
  type StorageSummary,
  type WalletNodeIdentityApplyOptions,
  type WalletNodeIdentityPayload,
  type WalletStorageSnapshot,
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
  normalizeRawDataRecords,
  normalizeProviderSearchResult,
  attachRawFlatbufferStream,
  normalizeStorageSummary,
  rawDataStreamPayload,
  sharedSearchPayload,
  RAW_FLATBUFFER_STREAM_CONTENT_TYPE,
  recordsFromPayload,
  resolveFetch,
  type BackendDeps,
} from './sdn-backend-adapter-utils';
import { createHttpChannelBackend } from './channel-backend';
import { decodeEpmFlatBuffer } from './epm-flatbuffer';
import { normalizeHostedEpmRecord, type HostedEpmRecord } from './identity';

export type DesktopLocalBackendOptions = PartialSdnBackendConfig & BackendDeps;

export function createDesktopLocalBackend(options: DesktopLocalBackendOptions = {}): SdnBackend {
  const config = normalizeBackendConfig({ ...options, mode: 'desktop-local' });
  const fetchLike = resolveFetch(options.fetch);
  const desktopBase = config.desktopProxyUrl;
  const kuboBase = config.kuboApiUrl;
  const gatewayBase = config.gatewayUrl ?? 'http://127.0.0.1:8081';

  async function getNodeProfile(): Promise<BackendResult<Record<string, unknown>>> {
    const result = await getBytes(fetchLike, joinUrl(desktopBase, '/api/node/epm'), 'getNodeProfile', {
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
    channels: createHttpChannelBackend(fetchLike, desktopBase),
    connect: getNodeSummary,
    async getCapabilities(): Promise<BackendCapability[]> {
      return [
        createCapability('kubo-rpc', kuboBase ? 'available' : 'unavailable', kuboBase ? undefined : 'Kubo RPC URL is not configured'),
        createCapability('desktop-proxy', desktopBase ? 'available' : 'degraded', desktopBase ? undefined : 'using relative desktop routes'),
        createCapability('browser-node', 'local-only', 'desktop-local uses daemon Kubo rather than browser-node'),
        ...['/api/peers/sdn', '/api/peers', '/api/peers/graph', '/api/directory/nodes', '/api/directory/users', '/api/identity/epms', '/api/node/epm/json', '/api/node/epm'].map((route) => (
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
      const result = await getBytes(
        fetchLike,
        joinUrl(desktopBase, '/api/node/epm'),
        'saveNodeProfile',
        {
          method: 'PUT',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify(profile),
        },
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
    async getNodeIdentitySettings(): Promise<BackendResult<NodeIdentitySettings>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/node/identity/settings'),
        'getNodeIdentitySettings',
      );
      if (!result.ok) return result as BackendResult<NodeIdentitySettings>;
      return createAvailableResult('getNodeIdentitySettings', normalizeNodeIdentitySettings(result.data));
    },
    async saveNodeIdentitySettings(settings: NodeIdentitySettings): Promise<BackendResult<NodeIdentitySettings>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/node/identity/settings'),
        'saveNodeIdentitySettings',
        {
          method: 'PUT',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify(nodeIdentitySettingsPayload(settings)),
        },
      );
      if (!result.ok) return result as BackendResult<NodeIdentitySettings>;
      return createAvailableResult('saveNodeIdentitySettings', normalizeNodeIdentitySettings(result.data));
    },
    async selectFlatbufferStorageLocation(currentPath?: string | null): Promise<BackendResult<FlatbufferStorageLocationSelection>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/node/identity/settings/flatbuffer-storage-location'),
        'selectFlatbufferStorageLocation',
        {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify(currentPath ? { current_path: currentPath } : {}),
        },
      );
      if (!result.ok) return result as BackendResult<FlatbufferStorageLocationSelection>;
      return createAvailableResult('selectFlatbufferStorageLocation', normalizeFlatbufferStorageLocationSelection(result.data));
    },
    async applyWalletNodeIdentity(
      payload: WalletNodeIdentityPayload,
      options: WalletNodeIdentityApplyOptions = {},
    ): Promise<BackendResult<NodeIdentityApplyResult>> {
      const url = joinUrl(desktopBase, '/api/node/identity/wallet');
      try {
        const response = await fetchLike(url, {
          method: 'PUT',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({
            replace: Boolean(options.replace),
            wallet_identity: walletNodeIdentityPayload(payload),
          }),
        });
        const body = await response.json().catch(() => null);
        if (response.ok || (response.status === 409 && isRecord(body) && body.status === 'mismatch')) {
          return createAvailableResult('applyWalletNodeIdentity', normalizeNodeIdentityApplyResult(body));
        }
        return createDegradedResult('applyWalletNodeIdentity', `${url} returned HTTP ${response.status}`);
      } catch (error) {
        return createDegradedResult('applyWalletNodeIdentity', error instanceof Error ? error.message : String(error));
      }
    },
    async logoutNodeIdentity(): Promise<BackendResult<Record<string, unknown>>> {
      const result = await getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(desktopBase, '/api/node/identity/session'),
        'logoutNodeIdentity',
        { method: 'DELETE' },
      );
      return result.ok ? createAvailableResult('logoutNodeIdentity', result.data ?? { ok: true }) : result;
    },
    async getWalletStorage(): Promise<BackendResult<WalletStorageSnapshot>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/node/identity/wallet-storage'),
        'getWalletStorage',
      );
      if (!result.ok) return result as BackendResult<WalletStorageSnapshot>;
      return createAvailableResult('getWalletStorage', normalizeWalletStorageSnapshot(result.data));
    },
    async saveWalletStorage(entries: Record<string, string | null>): Promise<BackendResult<WalletStorageSnapshot>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/node/identity/wallet-storage'),
        'saveWalletStorage',
        {
          method: 'PUT',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ entries }),
        },
      );
      if (!result.ok) return result as BackendResult<WalletStorageSnapshot>;
      return createAvailableResult('saveWalletStorage', normalizeWalletStorageSnapshot(result.data));
    },
    async listWalletsAndEpms(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      const epm = await getNodeProfile();
      if (!epm.ok) {
        return createDegradedResult('listWalletsAndEpms', epm.capability.reason ?? 'node EPM FlatBuffer unavailable', []);
      }
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
    async listNodeAccessUsers(): Promise<BackendResult<NodeAccessUser[]>> {
      const users = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/auth/users'),
        'listNodeAccessUsers',
        authRequest(),
      );
      if (!users.ok) return users as BackendResult<NodeAccessUser[]>;
      return createAvailableResult('listNodeAccessUsers', normalizeNodeAccessPayload(users.data));
    },
    async saveNodeAccessUser(user: NodeAccessUserInput): Promise<BackendResult<Record<string, unknown>>> {
      return getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(desktopBase, '/api/auth/users'),
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
        joinUrl(desktopBase, `/api/auth/users/${encodeURIComponent(xpub)}`),
        'revokeNodeAdmin',
        authJsonRequest('PUT', { trust_level: 'standard' }),
      );
    },
    async deleteNodeAccessUser(xpub: string): Promise<BackendResult<Record<string, unknown>>> {
      return getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(desktopBase, `/api/auth/users/${encodeURIComponent(xpub)}`),
        'deleteNodeAccessUser',
        authRequest('DELETE'),
      );
    },
    async listHostedEpms(): Promise<BackendResult<HostedEpmRecord[]>> {
      const epms = await getJson<unknown>(fetchLike, joinUrl(desktopBase, '/api/identity/epms'), 'listHostedEpms');
      if (!epms.ok) return epms as BackendResult<HostedEpmRecord[]>;
      return createAvailableResult('listHostedEpms', recordsFromPayload(epms.data).map(normalizeHostedEpmRecord));
    },
    async saveHostedEpm(record: HostedEpmRecord): Promise<BackendResult<HostedEpmRecord>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, `/api/identity/epms/${encodeURIComponent(record.id)}`),
        'saveHostedEpm',
        {
          method: 'PUT',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({
            id: record.id,
            kind: record.kind,
            epm_json: record.epmJson,
          }),
        },
      );
      if (!result.ok) return result as BackendResult<HostedEpmRecord>;
      return createAvailableResult('saveHostedEpm', normalizeHostedEpmRecord(result.data && isRecord(result.data)
        ? result.data
        : { id: record.id, kind: record.kind, epm_json: record.epmJson }));
    },
    async importHostedEpm(input: { name: string; bytes?: Uint8Array; text?: string }): Promise<BackendResult<HostedEpmRecord>> {
      let payload: unknown;
      try {
        const text = input.text ?? (input.bytes ? new TextDecoder().decode(input.bytes) : '');
        payload = JSON.parse(text);
      } catch (error) {
        return createDegradedResult('importHostedEpm', `unable to parse hosted EPM import: ${error instanceof Error ? error.message : String(error)}`);
      }
      const record = normalizeHostedEpmRecord({
        id: input.name.replace(/\.[^.]+$/, '') || undefined,
        kind: 'hosted',
        epm_json: isRecord(payload) ? payload : {},
      });
      return this.saveHostedEpm(record);
    },
    async deleteHostedEpm(id: string): Promise<BackendResult<Record<string, unknown>>> {
      return getJson<Record<string, unknown>>(
        fetchLike,
        joinUrl(desktopBase, `/api/identity/epms/${encodeURIComponent(id)}`),
        'deleteHostedEpm',
        { method: 'DELETE' },
      );
    },
    async downloadHostedEpm(id: string, format: 'json' | 'epm' | 'vcard'): Promise<BackendResult<{ url: string; filename: string }>> {
      const suffix = format === 'json' ? '' : `/${format === 'vcard' ? 'vcard' : 'epm'}`;
      const extension = format === 'vcard' ? 'vcf' : format;
      return createAvailableResult('downloadHostedEpm', {
        url: joinUrl(desktopBase, `/api/identity/epms/${encodeURIComponent(id)}${suffix}`),
        filename: `${id}.${extension}`,
      });
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
      const [nodes, users] = await Promise.all([
        getJson<unknown>(fetchLike, joinUrl(desktopBase, `/api/directory/nodes?q=${encodeURIComponent(query)}`), 'searchDirectory:nodes'),
        getJson<unknown>(fetchLike, joinUrl(desktopBase, `/api/directory/users?q=${encodeURIComponent(query)}`), 'searchDirectory:users'),
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
    async getDataSummary(): Promise<BackendResult<DataSummary>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/v1/data/summary'),
        'getDataSummary',
        authRequest(),
      );
      if (!result.ok) return createDegradedResult('getDataSummary', result.capability.reason ?? 'data summary unavailable');
      return createAvailableResult('getDataSummary', normalizeDataSummary(result.data));
    },
    async searchProviders(request: SharedSearchRequest): Promise<BackendResult<SearchResult<ProviderSearchRow>>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/v1/search/providers'),
        'searchProviders',
        authJsonRequest('POST', sharedSearchPayload(request)),
      );
      if (!result.ok) return result as BackendResult<SearchResult<ProviderSearchRow>>;
      return createAvailableResult('searchProviders', normalizeProviderSearchResult(result.data));
    },
    async searchData(request: SharedSearchRequest): Promise<BackendResult<SearchResult<DataSearchRow>>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/v1/search/data'),
        'searchData',
        authJsonRequest('POST', sharedSearchPayload(request)),
      );
      if (!result.ok) return result as BackendResult<SearchResult<DataSearchRow>>;
      return createAvailableResult('searchData', normalizeDataSearchResult(result.data));
    },
    async scanRawData(query: RawDataQuery): Promise<BackendResult<DataScanResult>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/v1/data/scan'),
        'scanRawData',
        authJsonRequest('POST', rawDataQueryPayload(query)),
      );
      if (!result.ok) return result as BackendResult<DataScanResult>;
      return createAvailableResult('scanRawData', normalizeDataScanResult(result.data));
    },
    async streamRawData(request: RawDataStreamRequest): Promise<BackendResult<RawDataRecord[]>> {
      const stream = await getBytes(
        fetchLike,
        joinUrl(desktopBase, '/api/v1/data/stream'),
        'streamRawData',
        authRawFlatbufferStreamRequest(rawDataStreamPayload(request)),
      );
      if (!stream.ok || !stream.data) {
        return createDegradedResult('streamRawData', stream.capability.reason ?? 'raw FlatBuffer stream unavailable');
      }
      return createAvailableResult('streamRawData', attachRawFlatbufferStream(request.records, stream.data));
    },
    async queryRawData(query: RawDataQuery): Promise<BackendResult<RawDataRecord[]>> {
      const payload = rawDataQueryPayload(query);
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/v1/data/query'),
        'queryRawData',
        authJsonRequest('POST', payload),
      );
      if (!result.ok) return result as BackendResult<RawDataRecord[]>;
      const records = normalizeRawDataRecords(result.data);
      const stream = await getBytes(
        fetchLike,
        joinUrl(desktopBase, '/api/v1/data/query'),
        'queryRawData:flatbufferStream',
        authRawFlatbufferStreamRequest(payload),
      );
      if (!stream.ok || !stream.data) return createAvailableResult('queryRawData', records);
      return createAvailableResult('queryRawData', attachRawFlatbufferStream(records, stream.data));
    },
    async readRawDataRecord(schemaName: string, cid: string): Promise<BackendResult<RawDataRecordBytes>> {
      const result = await getBytes(
        fetchLike,
        joinUrl(desktopBase, `/api/v1/data/records/${encodeURIComponent(schemaName)}/${encodeURIComponent(cid)}`),
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
        joinUrl(desktopBase, '/api/v1/conjunction/screen'),
        'screenConjunction',
        authJsonRequest('POST', conjunctionScreenPayload(request)),
      );
      if (!result.ok) return result as BackendResult<ConjunctionScreenResult>;
      return createAvailableResult('screenConjunction', normalizeConjunctionScreenResult(result.data));
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
    async runSqlQuery(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('runSqlQuery', 'local-only', 'SQL queries run against the selected local FlatSQL datastore after preview or sync', []);
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

function rawDataQueryPayload(query: RawDataQuery): Record<string, unknown> {
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

function normalizeNodeIdentitySettings(value: unknown): NodeIdentitySettings {
  const record = isRecord(value) ? value : {};
  const rawTtl = record.ttl_ms ?? record.ttlMs;
  const rawFlatbufferStoragePath = record.flatbuffer_storage_path ?? record.flatbufferStoragePath;
  const ttlMs = rawTtl === 'app'
    ? 'app'
    : Number.isFinite(Number(rawTtl)) && Number(rawTtl) > 0
      ? Number(rawTtl)
      : 3_600_000;
  return {
    ttlMs,
    flatbufferStoragePath: typeof rawFlatbufferStoragePath === 'string' && rawFlatbufferStoragePath.trim()
      ? rawFlatbufferStoragePath.trim()
      : undefined,
    updatedAt: typeof (record.updated_at ?? record.updatedAt) === 'string'
      ? String(record.updated_at ?? record.updatedAt)
      : undefined,
    session: normalizeNodeIdentitySession(record.session),
  };
}

function nodeIdentitySettingsPayload(settings: NodeIdentitySettings): Record<string, unknown> {
  return {
    ttl_ms: settings.ttlMs,
    ...(settings.flatbufferStoragePath ? { flatbuffer_storage_path: settings.flatbufferStoragePath } : {}),
  };
}

function normalizeFlatbufferStorageLocationSelection(value: unknown): FlatbufferStorageLocationSelection {
  const record = isRecord(value) ? value : {};
  const selectedPath = record.path;
  return {
    canceled: record.canceled === true,
    path: typeof selectedPath === 'string' && selectedPath.trim() ? selectedPath.trim() : null,
  };
}

function normalizeNodeIdentitySession(value: unknown) {
  const record = isRecord(value) ? value : {};
  return {
    unlocked: record.unlocked === true,
    expiresAt: typeof (record.expires_at ?? record.expiresAt) === 'string'
      ? String(record.expires_at ?? record.expiresAt)
      : null,
    profile: isRecord(record.profile) ? record.profile : null,
  };
}

function walletNodeIdentityPayload(payload: WalletNodeIdentityPayload): Record<string, unknown> {
  return {
    peer_id: payload.peerId,
    ...(payload.xpub ? { xpub: payload.xpub } : {}),
    ...(payload.walletAccountId ? { wallet_account_id: payload.walletAccountId } : {}),
    ...(payload.walletAccountLabel ? { wallet_account_label: payload.walletAccountLabel } : {}),
    ...(payload.identityPublicKey ? { identity_public_key: payload.identityPublicKey } : {}),
    signing_public_key: payload.signingPublicKey,
    ...(payload.encryptionPublicKey ? { encryption_public_key: payload.encryptionPublicKey } : {}),
    ...(payload.signature ? { signature: payload.signature } : {}),
    ...(payload.signaturePayload ? { signature_payload: payload.signaturePayload } : {}),
    ...(typeof payload.signatureTimestamp === 'number' ? { signature_timestamp: payload.signatureTimestamp } : {}),
  };
}

function normalizeNodeIdentityApplyResult(value: unknown): NodeIdentityApplyResult {
  const record = isRecord(value) ? value : {};
  const status = record.status === 'mismatch' || record.status === 'unchanged' ? record.status : 'updated';
  return {
    status,
    profile: isRecord(record.profile) ? record.profile : undefined,
    current: isRecord(record.current) ? record.current : undefined,
    proposed: isRecord(record.proposed) ? record.proposed : undefined,
  };
}

function normalizeWalletStorageSnapshot(value: unknown): WalletStorageSnapshot {
  const record = isRecord(value) ? value : {};
  const rawEntries = isRecord(record.entries) ? record.entries : {};
  const entries: Record<string, string> = {};
  for (const [key, item] of Object.entries(rawEntries)) {
    if (typeof item === 'string') entries[key] = item;
  }
  return {
    entries,
    encryptedAtRest: record.encrypted_at_rest === true || record.encryptedAtRest === true,
    storage: typeof (record.storage ?? record.encoding) === 'string'
      ? String(record.storage ?? record.encoding)
      : undefined,
    updatedAt: typeof (record.updated_at ?? record.updatedAt) === 'string'
      ? String(record.updated_at ?? record.updatedAt)
      : null,
  };
}

function authRawFlatbufferStreamRequest(body: Record<string, unknown>): RequestInit {
  const init = authJsonRequest('POST', body);
  return {
    ...init,
    headers: {
      ...(init.headers as Record<string, string>),
      accept: RAW_FLATBUFFER_STREAM_CONTENT_TYPE,
    },
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}
