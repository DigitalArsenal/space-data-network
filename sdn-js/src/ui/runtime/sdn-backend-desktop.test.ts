import { describe, expect, it, vi } from 'vitest';
import { createDesktopLocalBackend } from './sdn-backend-desktop';

describe('desktop-local SDN backend', () => {
  it('loads node profile and observed SDN peers through local desktop routes', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url === 'http://127.0.0.1:17890/api/node/epm/json') {
        return jsonResponse({
          dn: 'Space Data Network Desktop',
          peer_id: '12D3KooWLocal',
          agent_version: 'kubo/0.39.0/sdn-desktop',
        });
      }
      if (url === 'http://127.0.0.1:17890/api/peers/sdn') {
        return jsonResponse([
          {
            id: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
            name: 'CelesTrak Provider',
            addrs: ['/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4'],
            trust_level: 'observed',
            metadata: {
              agent_version: 'spacedatanetwork/1.0.3',
              protocols: '/space-data-network/module-delivery/1.0.0',
            },
          },
        ]);
      }
      throw new Error(`unexpected ${url}`);
    });

    const backend = createDesktopLocalBackend({
      desktopProxyUrl: 'http://127.0.0.1:17890',
      kuboApiUrl: 'http://127.0.0.1:5001',
      gatewayUrl: 'http://127.0.0.1:8081',
      fetch: fetchMock,
    });

    await expect(backend.getNodeProfile()).resolves.toMatchObject({
      ok: true,
      data: { peer_id: '12D3KooWLocal' },
    });
    await expect(backend.listObservedPeers()).resolves.toMatchObject({
      ok: true,
      data: [{ id: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4', trustLevel: 'observed' }],
    });
  });

  it('does not promote configured aliases or DNS seed labels into peer IDs', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url === 'http://127.0.0.1:17890/api/peers/sdn') {
        return jsonResponse([
          { id: 'space-data-network-01', name: 'space-data-network-01', addrs: [], trust_level: 'trusted' },
          { id: 'sdn.spaceaware.io', name: 'sdn.spaceaware.io', addrs: [], trust_level: 'trusted' },
          { id: 'celestrak.eth', name: 'celestrak.eth', addrs: [], trust_level: 'trusted' },
          {
            peer_id: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
            name: 'Public SDN Node',
            addrs: ['/ip4/104.131.11.220/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45'],
            metadata: {
              agent_version: 'spacedatanetwork/1.0.3',
            },
          },
        ]);
      }
      throw new Error(`unexpected ${url}`);
    });

    const backend = createDesktopLocalBackend({
      desktopProxyUrl: 'http://127.0.0.1:17890',
      fetch: fetchMock,
    });

    await expect(backend.listObservedPeers()).resolves.toMatchObject({
      ok: true,
      data: [{ id: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45' }],
    });
  });

  it('advertises the required desktop-local SDN route capabilities', async () => {
    const backend = createDesktopLocalBackend({
      desktopProxyUrl: 'http://127.0.0.1:17890',
      fetch: vi.fn(),
    });

    await expect(backend.getCapabilities()).resolves.toEqual(expect.arrayContaining([
      expect.objectContaining({ id: 'route:/api/peers/sdn', state: 'available' }),
      expect.objectContaining({ id: 'route:/api/peers', state: 'available' }),
      expect.objectContaining({ id: 'route:/api/peers/graph', state: 'available' }),
      expect.objectContaining({ id: 'route:/api/node/epm/json', state: 'available' }),
      expect.objectContaining({ id: 'route:/api/node/epm', state: 'available' }),
    ]));
  });

  it('loads and saves hosted EPMs through identity routes', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'http://127.0.0.1:17890/api/identity/epms') {
        return jsonResponse({ epms: [{ id: 'self', kind: 'node-self', epm_json: { dn: 'Local Node', peer_id: '12D3KooWNode' } }] });
      }
      if (url === 'http://127.0.0.1:17890/api/identity/epms/self') {
        expect(init?.method).toBe('PUT');
        return jsonResponse({ id: 'self', kind: 'node-self', epm_json: { dn: 'Updated Node', peer_id: '12D3KooWNode' } });
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    await expect(backend.listHostedEpms()).resolves.toMatchObject({ ok: true, data: [{ id: 'self', kind: 'node-self' }] });
    await expect(backend.saveHostedEpm({
      id: 'self',
      kind: 'node-self',
      label: 'Updated Node',
      peerId: '12D3KooWNode',
      epmJson: { dn: 'Updated Node', peer_id: '12D3KooWNode' },
    })).resolves.toMatchObject({ ok: true, data: { label: 'Updated Node' } });
    expect(calls.map((call) => call.url)).toContain('http://127.0.0.1:17890/api/identity/epms');
    expect(calls.map((call) => call.url)).toContain('http://127.0.0.1:17890/api/identity/epms/self');
  });

  it('searches node and person directory endpoints instead of the peers graph', async () => {
    const urls: string[] = [];
    const fetchMock = vi.fn(async (url: string) => {
      urls.push(url);
      if (url.includes('/api/directory/nodes')) return jsonResponse({ nodes: [{ peer_id: 'node-peer', dn: 'Node Alice' }] });
      if (url.includes('/api/directory/users')) return jsonResponse({ users: [{ peer_id: 'user-peer', dn: 'User Alice' }] });
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    await expect(backend.searchDirectory('alice')).resolves.toMatchObject({ ok: true });
    expect(urls.some((url) => url.includes('/api/directory/nodes?q=alice'))).toBe(true);
    expect(urls.some((url) => url.includes('/api/directory/users?q=alice'))).toBe(true);
    expect(urls.some((url) => url.includes('/api/peers/graph'))).toBe(false);
  });

  it('uses the configured gateway for CID resolution', async () => {
    const backend = createDesktopLocalBackend({
      gatewayUrl: 'http://127.0.0.1:8081',
      fetch: vi.fn(),
    });

    await expect(backend.resolveCid('bafy123')).resolves.toMatchObject({
      ok: true,
      data: {
        cid: 'bafy123',
        gatewayUrl: 'http://127.0.0.1:8081/ipfs/bafy123',
      },
    });
  });
});

function jsonResponse(payload: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => payload,
  } as Response;
}
