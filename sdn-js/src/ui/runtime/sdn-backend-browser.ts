import {
  createAvailableResult,
  createCapability,
  createDegradedResult,
  createUnavailableResult,
  type BackendCapability,
  type BackendResult,
  type LocalObjectSummary,
  type NodeSummary,
  type ObservedSdnPeer,
  type SdnBackend,
  type StorageSummary,
} from './sdn-backend';

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
    async getNodeProfile(): Promise<BackendResult<Record<string, unknown>>> {
      return createAvailableResult('getNodeProfile', { dn: summary.displayName, runtime: summary.runtime });
    },
    async saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>> {
      return createDegradedResult('saveNodeProfile', 'browser-node local profile persistence is scheduled for Milestone 4', profile);
    },
    async listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>> {
      return createDegradedResult('listObservedPeers', 'browser transports are scheduled for Milestone 4', []);
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
    async runSqlQuery(): Promise<BackendResult<Array<Record<string, unknown>>>> {
      return createUnavailableResult('runSqlQuery', 'SQL query requires a local or remote SDN data index');
    },
    async resolveCid(cid: string): Promise<BackendResult<{ cid: string; gatewayUrl: string }>> {
      return createUnavailableResult('resolveCid', `CID resolution for ${cid} requires a gateway or browser-node transport`);
    },
  };
}
