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
            id: '16Uiu2HAmReal',
            name: '16Uiu2HAmReal',
            addrs: ['/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAmReal'],
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
      data: [{ id: '16Uiu2HAmReal', trustLevel: 'observed' }],
    });
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
