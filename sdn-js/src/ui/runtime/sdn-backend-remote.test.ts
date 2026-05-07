import { describe, expect, it, vi } from 'vitest';
import { createBrowserNodeBackend } from './sdn-backend-browser';
import { createSdnBackend } from './sdn-backend-factory';
import { createRemoteSdnBackend } from './sdn-backend-remote';

describe('remote-sdn backend', () => {
  it('loads node profile and observed SDN peers from a remote server', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url === 'https://sdn.spaceaware.io/api/node/epm/json') {
        return jsonResponse({ dn: 'SDN Public Node', peer_id: '16Uiu2HAmRemote' });
      }
      if (url === 'https://sdn.spaceaware.io/api/peers/sdn') {
        return jsonResponse({
          peers: [
            {
              peer_id: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
              display_name: 'CelesTrak Provider',
              multiaddrs: ['/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4'],
              trust: 'trusted',
              agent_version: 'spacedatanetwork/1.0.3',
            },
          ],
        });
      }
      throw new Error(`unexpected ${url}`);
    });

    const backend = createRemoteSdnBackend({
      mode: 'remote-sdn',
      serverUrl: 'https://sdn.spaceaware.io',
      kuboApiUrl: null,
      gatewayUrl: null,
      desktopProxyUrl: null,
      fetch: fetchMock,
    });

    expect(backend.mode).toBe('remote-sdn');
    await expect(backend.getNodeProfile()).resolves.toMatchObject({
      ok: true,
      data: { peer_id: '16Uiu2HAmRemote' },
    });
    await expect(backend.listObservedPeers()).resolves.toMatchObject({
      ok: true,
      data: [{ id: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4', name: 'CelesTrak Provider' }],
    });
  });

  it('returns degraded results for missing remote endpoints', async () => {
    const backend = createRemoteSdnBackend({
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: vi.fn(async () => ({ ok: false, status: 404, json: async () => ({}) } as Response)),
    });

    await expect(backend.listObjects()).resolves.toMatchObject({
      ok: false,
      capability: {
        id: 'listObjects',
        state: 'degraded',
      },
    });
  });
});

describe('browser-node backend', () => {
  it('represents browser-node as an explicit deferred degraded adapter', async () => {
    await expect(createBrowserNodeBackend().getCapabilities()).resolves.toContainEqual({
      id: 'browser-node',
      state: 'degraded',
      reason: 'browser-node adapter is scheduled for Milestone 4',
    });
  });
});

describe('SDN backend factory', () => {
  it('selects the requested backend mode', () => {
    expect(createSdnBackend({ mode: 'remote-sdn', serverUrl: 'https://sdn.spaceaware.io' }).mode).toBe('remote-sdn');
    expect(createSdnBackend({ mode: 'browser-node' }).mode).toBe('browser-node');
    expect(createSdnBackend({ mode: 'desktop-local' }).mode).toBe('desktop-local');
  });
});

function jsonResponse(payload: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => payload,
  } as Response;
}
