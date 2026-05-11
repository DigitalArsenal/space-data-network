import {
  createAvailableResult,
  createCapability,
  createCapabilityResult,
  createDegradedResult,
  createUnavailableResult,
  type BackendCapability,
  type BackendResult,
  type DataSummary,
  type LocalObjectSummary,
  type NodeAccessUserInput,
  type NodeSummary,
  type ObservedSdnPeer,
  type RawDataRecord,
  type RawDataRecordBytes,
  type SdnBackend,
  type StorageSummary,
} from './sdn-backend';
import type { HostedEpmRecord } from './identity';

export function createBrowserNodeBackend(): SdnBackend {
  const summary: NodeSummary = {
    displayName: 'Browser SDN Node',
    peerId: null,
    agentVersion: 'browser-node/deferred',
    online: false,
    runtime: 'browser-node',
  };

  return {
    mode: 'browser-node',
    async connect(): Promise<BackendResult<NodeSummary>> {
      return createDegradedResult('connect', 'browser-node adapter is scheduled for Milestone 4', summary);
    },
    async getCapabilities(): Promise<BackendCapability[]> {
      return [
        createCapability('browser-node', 'degraded', 'browser-node adapter is scheduled for Milestone 4'),
        createCapability('local-identity', 'available'),
        createCapability('kubo-rpc', 'unavailable', 'daemon Kubo RPC is unavailable in browser-node mode'),
      ];
    },
    async getNodeSummary(): Promise<BackendResult<NodeSummary>> {
      return createDegradedResult('getNodeSummary', 'browser-node adapter is scheduled for Milestone 4', summary);
    },
    async getHealth() {
      return createDegradedResult('getHealth', 'browser-node health checks are scheduled for Milestone 4', { healthy: false, details: {} });
    },
    async getNodeProfile(): Promise<BackendResult<Record<string, unknown>>> {
      return createAvailableResult('getNodeProfile', { dn: summary.displayName, runtime: summary.runtime });
    },
    async saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('saveNodeProfile', 'browser-node local profile persistence is scheduled for Milestone 4', profile);
    },
    async listWalletsAndEpms(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createDegradedResult('listWalletsAndEpms', 'browser wallet and EPM storage is scheduled for Milestone 4', []);
    },
    async beginClaimEpm(): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('beginClaimEpm', 'browser EPM claim flow is scheduled for Milestone 4');
    },
    async exportCore(): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('exportCore', 'permission-required', 'Core export requires an unlocked local browser identity');
    },
    async importCore(core: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('importCore', `browser Core import is scheduled for Milestone 4 (${Object.keys(core).length} fields)`);
    },
    async listNodeAccessUsers() {
      return createCapabilityResult('listNodeAccessUsers', 'remote-only', 'node access management requires a local or remote SDN server', []);
    },
    async saveNodeAccessUser(user: NodeAccessUserInput) {
      return createCapabilityResult('saveNodeAccessUser', 'remote-only', `granting ${user.xpub} requires a local or remote SDN server`);
    },
    async revokeNodeAdmin(xpub: string) {
      return createCapabilityResult('revokeNodeAdmin', 'remote-only', `revoking admin for ${xpub} requires a local or remote SDN server`);
    },
    async deleteNodeAccessUser(xpub: string) {
      return createCapabilityResult('deleteNodeAccessUser', 'remote-only', `removing ${xpub} requires a local or remote SDN server`);
    },
    async listHostedEpms(): Promise<BackendResult<HostedEpmRecord[]>> {
      return createDegradedResult('listHostedEpms', 'browser hosted EPM storage is scheduled for Milestone 4', []);
    },
    async saveHostedEpm(record: HostedEpmRecord): Promise<BackendResult<HostedEpmRecord>> {
      return createDegradedResult('saveHostedEpm', `browser hosted EPM persistence for ${record.id} is scheduled for Milestone 4`, record);
    },
    async importHostedEpm(input: { name: string }): Promise<BackendResult<HostedEpmRecord>> {
      return createDegradedResult('importHostedEpm', `browser hosted EPM import for ${input.name} is scheduled for Milestone 4`);
    },
    async deleteHostedEpm(id: string): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('deleteHostedEpm', `browser hosted EPM deletion for ${id} is scheduled for Milestone 4`);
    },
    async downloadHostedEpm(id: string, format: 'json' | 'epm' | 'vcard'): Promise<BackendResult<{ url: string; filename: string }>> {
      return createDegradedResult('downloadHostedEpm', `browser hosted EPM ${format} download for ${id} is scheduled for Milestone 4`);
    },
    async listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      return createDegradedResult('listObservedPeers', 'browser transports are scheduled for Milestone 4', []);
    },
    async listTrustedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      return createDegradedResult('listTrustedPeers', 'browser trusted peer storage is scheduled for Milestone 4', []);
    },
    async searchDirectory(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createDegradedResult('searchDirectory', 'browser directory search is scheduled for Milestone 4', []);
    },
    async connectPeer(peerId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('connectPeer', `browser peer connection for ${peerId} is scheduled for Milestone 4`);
    },
    async searchListings(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('searchListings', 'remote-only', 'marketplace search requires a remote SDN server', []);
    },
    async listOwnedItems(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createCapabilityResult('listOwnedItems', 'remote-only', 'owned marketplace items require a remote SDN server', []);
    },
    async requestGrant(listingId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createCapabilityResult('requestGrant', 'remote-only', `grant request for ${listingId} requires a remote SDN server`);
    },
    async installModule(moduleId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('installModule', `browser module install for ${moduleId} is scheduled for Milestone 4`);
    },
    async subscribeDataFeed(feedId: string): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('subscribeDataFeed', `browser data-feed subscription for ${feedId} is scheduled for Milestone 4`);
    },
    async getStorageSummary(): Promise<BackendResult<StorageSummary>> {
      return createDegradedResult('getStorageSummary', 'browser storage accounting is scheduled for Milestone 4', {
        usedBytes: null,
        pinnedBytes: null,
        cacheBytes: null,
        quotaBytes: null,
      });
    },
    async listObjects(): Promise<BackendResult<LocalObjectSummary[]>> {
      return createDegradedResult('listObjects', 'browser-node object cache is scheduled for Milestone 4', []);
    },
    async inspectObject(id: string): Promise<BackendResult<LocalObjectSummary | Record<string, unknown>>> {
      return createDegradedResult('inspectObject', `browser object inspection for ${id} is scheduled for Milestone 4`);
    },
    async getDataSummary(): Promise<BackendResult<DataSummary>> {
      return createUnavailableResult('getDataSummary', 'raw FlatSQL summary requires a local or remote SDN node');
    },
    async queryRawData(): Promise<BackendResult<RawDataRecord[]>> {
      return createUnavailableResult('queryRawData', 'raw FlatSQL query requires a local or remote SDN node');
    },
    async readRawDataRecord(): Promise<BackendResult<RawDataRecordBytes>> {
      return createUnavailableResult('readRawDataRecord', 'raw FlatBuffer record reads require a local or remote SDN node');
    },
    async pinObject(id: string): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('pinObject', `browser pinning for ${id} is scheduled for Milestone 4`);
    },
    async unpinObject(id: string): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('unpinObject', `browser unpinning for ${id} is scheduled for Milestone 4`);
    },
    async listRulesets(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createDegradedResult('listRulesets', 'browser rulesets are scheduled for Milestone 4', []);
    },
    async saveRuleset(ruleset: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('saveRuleset', `browser ruleset persistence is scheduled for Milestone 4 (${Object.keys(ruleset).length} fields)`);
    },
    async runSqlQuery(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createUnavailableResult('runSqlQuery', 'SQL query requires a local or remote SDN data index');
    },
    async getKuboStatus(): Promise<BackendResult<Record<string, unknown>>> {
      return createUnavailableResult('getKuboStatus', 'daemon Kubo RPC is unavailable in browser-node mode');
    },
    async listFiles(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createUnavailableResult('listFiles', 'MFS file browsing requires daemon Kubo RPC');
    },
    async resolveCid(cid: string): Promise<BackendResult<{ cid: string; gatewayUrl: string }>> {
      return createUnavailableResult('resolveCid', `CID resolution for ${cid} requires a gateway or browser-node transport`);
    },
    async readGatewayUrl(path: string): Promise<BackendResult<{ path: string; gatewayUrl: string }>> {
      return createUnavailableResult('readGatewayUrl', `gateway URL for ${path} requires a configured gateway`);
    },
  };
}
