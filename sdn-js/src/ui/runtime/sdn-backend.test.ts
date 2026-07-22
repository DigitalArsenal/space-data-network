import { describe, expect, it } from 'vitest';
import { createBrowserNodeBackend } from './sdn-backend-browser';
import {
  createCapability,
  createUnavailableResult,
  isBackendMode,
  normalizeBackendConfig,
} from './sdn-backend';

describe('SdnBackend contract helpers', () => {
  const requiredMethods = [
    'connect',
    'getCapabilities',
    'getNodeSummary',
    'getHealth',
    'getNodeProfile',
    'saveNodeProfile',
    'listNodeAccessUsers',
    'saveNodeAccessUser',
    'revokeNodeAdmin',
    'deleteNodeAccessUser',
    'listObservedPeers',
    'listTrustedPeers',
    'searchDirectory',
    'connectPeer',
    'searchListings',
    'listOwnedItems',
    'requestGrant',
    'installModule',
    'subscribeDataFeed',
    'getStorageSummary',
    'listObjects',
    'inspectObject',
    'pinObject',
    'unpinObject',
    'listRulesets',
    'saveRuleset',
    'runSqlQuery',
    'getKuboStatus',
    'listFiles',
    'resolveCid',
    'readGatewayUrl',
  ] as const;

  it('exposes every backend method required by the SDN UI design contract', () => {
    const backend = createBrowserNodeBackend();
    for (const method of requiredMethods) {
      expect(typeof backend[method]).toBe('function');
    }
  });

  it('normalizes desktop-local configuration with Kubo and proxy URLs', () => {
    expect(normalizeBackendConfig({
      mode: 'desktop-local',
      kuboApiUrl: 'http://127.0.0.1:5001/',
      gatewayUrl: 'http://127.0.0.1:8081/',
      desktopProxyUrl: 'http://127.0.0.1:17890/',
    })).toEqual({
      mode: 'desktop-local',
      kuboApiUrl: 'http://127.0.0.1:5001',
      gatewayUrl: 'http://127.0.0.1:8081',
      desktopProxyUrl: 'http://127.0.0.1:17890',
      serverUrl: null,
    });
  });

  it('normalizes injected desktop Kubo multiaddrs to HTTP URLs', () => {
    expect(normalizeBackendConfig({
      mode: 'desktop-local',
      kuboApiUrl: '/ip4/127.0.0.1/tcp/5001',
      gatewayUrl: '/ip4/127.0.0.1/tcp/8081',
    })).toMatchObject({
      kuboApiUrl: 'http://127.0.0.1:5001',
      gatewayUrl: 'http://127.0.0.1:8081',
    });
  });

  it('normalizes remote-sdn configuration with server URL only', () => {
    expect(normalizeBackendConfig({
      mode: 'remote-sdn',
      serverUrl: 'https://sdn.spaceaware.io/',
    })).toMatchObject({
      mode: 'remote-sdn',
      serverUrl: 'https://sdn.spaceaware.io',
    });
  });

  it('rejects unknown backend modes', () => {
    expect(isBackendMode('desktop-local')).toBe(true);
    expect(isBackendMode('remote-sdn')).toBe(true);
    expect(isBackendMode('browser-node')).toBe(true);
    expect(isBackendMode('webui')).toBe(false);
  });

  it('creates explicit degraded capability results', () => {
    expect(createCapability('runSqlQuery', 'degraded', 'local index unavailable')).toEqual({
      id: 'runSqlQuery',
      state: 'degraded',
      reason: 'local index unavailable',
    });
    expect(createUnavailableResult('exportCore', 'permission required')).toEqual({
      ok: false,
      capability: {
        id: 'exportCore',
        state: 'unavailable',
        reason: 'permission required',
      },
      data: null,
    });
  });
});
