import {
  createAvailableResult,
  createCapabilityResult,
  createCapability,
  createDegradedResult,
  createUnavailableResult,
  normalizeBackendConfig,
  type BackendCapability,
  type BackendResult,
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
  type SdnBackend,
  type StorageSummary,
} from './sdn-backend';
import {
  getBytes,
  getJson,
  joinUrl,
  nodeSummaryFromProfile,
  normalizeDataSummary,
  normalizeNodeAccessPayload,
  normalizeObjectPayload,
  normalizePeerPayload,
  normalizeRawDataRecords,
  normalizeStorageSummary,
  recordsFromPayload,
  resolveFetch,
  type BackendDeps,
} from './sdn-backend-adapter-utils';
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
    async queryRawData(query: RawDataQuery): Promise<BackendResult<RawDataRecord[]>> {
      const result = await getJson<unknown>(
        fetchLike,
        joinUrl(desktopBase, '/api/v1/data/query'),
        'queryRawData',
        authJsonRequest('POST', rawDataQueryPayload(query)),
      );
      if (!result.ok) return result as BackendResult<RawDataRecord[]>;
      return createAvailableResult('queryRawData', normalizeRawDataRecords(result.data));
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
    ...(query.providerId ? { provider_id: query.providerId } : {}),
    ...(query.sourceName ? { source_name: query.sourceName } : {}),
    ...(query.batchId ? { batch_id: query.batchId } : {}),
    ...(query.peerId ? { peer_id: query.peerId } : {}),
    ...(typeof query.limit === 'number' ? { limit: query.limit } : {}),
    ...(typeof query.offset === 'number' ? { offset: query.offset } : {}),
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}
