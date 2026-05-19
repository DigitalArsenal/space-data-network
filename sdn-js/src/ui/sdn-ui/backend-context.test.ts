import { afterEach, describe, expect, it, vi } from 'vitest';
import * as flatbuffers from 'flatbuffers';
import { EPM } from 'spacedatastandards.org/lib/js/EPM/EPM.js';
import { EntityType } from 'spacedatastandards.org/lib/js/EPM/EntityType.js';

import { createBackendFromLocation } from '../../../ui/src/lib/backend-context';

describe('SDN UI backend context', () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
  });

  it('uses remote-sdn mode for hosted SDN pages with injected server config', () => {
    const backend = createBackendFromLocation(
      {
        origin: 'https://sdn.spaceaware.io',
        search: '',
      } as Location,
      {
        __SDN_CONFIG__: {
          serverBaseUrl: 'https://sdn.spaceaware.io',
        },
      },
    );

    expect(backend.mode).toBe('remote-sdn');
  });

  it('keeps desktop-local mode for desktop pages without hosted server config', () => {
    const backend = createBackendFromLocation(
      {
        origin: 'http://127.0.0.1:17890',
        search: '',
      } as Location,
      {},
    );

    expect(backend.mode).toBe('desktop-local');
  });

  it('routes desktop-local dev traffic through the Vite origin when proxy targets are configured', async () => {
    vi.stubEnv('SDN_UI_BACKEND', 'desktop-local');
    vi.stubEnv('SDN_UI_PROXY_TARGET', 'http://127.0.0.1:17890');
    vi.stubEnv('SDN_UI_KUBO_PROXY_TARGET', 'http://127.0.0.1:5001');
    vi.stubGlobal('fetch', vi.fn(async (url: string | URL | Request) => {
      const href = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
      if (href.endsWith('/api/node/epm')) {
        return new Response(buildNodeEpm({ dn: 'Dev Proxy Node', peer_id: '12D3KooWdevProxyPeer' }), {
          status: 200,
          headers: { 'content-type': 'application/x-flatbuffers' },
        });
      }
      if (href.endsWith('/kubo/api/v0/repo/stat')) {
        return Response.json({ RepoSize: 512, StorageMax: 1024 });
      }
      return new Response('not found', { status: 404 });
    }));

    const backend = createBackendFromLocation(
      {
        origin: 'http://127.0.0.1:5174',
        search: '',
      } as Location,
      {},
    );

    await backend.getNodeSummary();
    await backend.getStorageSummary();

    expect(vi.mocked(globalThis.fetch).mock.calls.map(([url]) => String(url))).toEqual([
      'http://127.0.0.1:5174/api/node/epm',
      'http://127.0.0.1:5174/kubo/api/v0/repo/stat',
    ]);
  });
});

function buildNodeEpm(profile: { dn: string; peer_id: string }): Uint8Array {
  const builder = new flatbuffers.Builder(1024);
  const dn = builder.createString(profile.dn);
  const address = builder.createString(`/ip4/127.0.0.1/tcp/4001/p2p/${profile.peer_id}`);
  const addresses = EPM.createMultiformatAddressVector(builder, [address]);

  EPM.startEPM(builder);
  EPM.addDn(builder, dn);
  EPM.addMultiformatAddress(builder, addresses);
  EPM.addEntityType(builder, EntityType.Node);
  const epm = EPM.endEPM(builder);
  EPM.finishSizePrefixedEPMBuffer(builder, epm);
  return builder.asUint8Array();
}
