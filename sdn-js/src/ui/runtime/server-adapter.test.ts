import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  createServerAdapter,
  createUiRuntimeAdapter,
  getSharedUiRuntimeAdapter,
  resetSharedUiRuntimeAdapterForTests,
} from './server-adapter';

afterEach(() => {
  resetSharedUiRuntimeAdapterForTests();
});

describe('createServerAdapter', () => {
  it('maps an authenticated admin session from the server APIs', async () => {
    const fetch = vi.fn(async (input: string) => {
      if (input.endsWith('/api/node/info')) {
        return jsonResponse(200, {
          peer_id: '12D3KooWRemote',
          DISPLAY_NAME: 'Remote Node',
        });
      }
      if (input.endsWith('/api/auth/status')) {
        return jsonResponse(200, {
          admin_configured: true,
          users_configured: true,
          wallet_ui_configured: true,
        });
      }
      if (input.endsWith('/api/auth/me')) {
        return jsonResponse(200, {
          name: 'Remote Admin',
          trust_level: 'admin',
        });
      }
      throw new Error(`unexpected fetch ${input}`);
    });

    const adapter = createServerAdapter({
      target: {
        baseUrl: 'https://node.example',
        label: 'Node Example',
      },
      fetch,
    });

    const snapshot = await adapter.connect();

    expect(snapshot.mode).toBe('server');
    expect(snapshot.serverTarget?.baseUrl).toBe('https://node.example');
    expect(snapshot.nodeContext.peerId).toBe('12D3KooWRemote');
    expect(snapshot.permissions.role).toBe('admin');
    expect(snapshot.permissions.authenticated).toBe(true);
    expect(snapshot.permissions.canManageUsers).toBe(true);
  });

  it('falls back to guest permissions when auth/me is unauthorized', async () => {
    const fetch = vi.fn(async (input: string) => {
      if (input.endsWith('/api/node/info')) {
        return jsonResponse(200, {
          peer_id: '12D3KooWRemote',
        });
      }
      if (input.endsWith('/api/auth/status')) {
        return jsonResponse(200, {
          admin_configured: true,
          users_configured: true,
          wallet_ui_configured: true,
        });
      }
      if (input.endsWith('/api/auth/me')) {
        return jsonResponse(401, {
          code: 'unauthorized',
        });
      }
      throw new Error(`unexpected fetch ${input}`);
    });

    const adapter = createServerAdapter({
      target: {
        baseUrl: 'https://node.example',
      },
      fetch,
    });

    const snapshot = await adapter.connect();

    expect(snapshot.permissions.role).toBe('guest');
    expect(snapshot.permissions.authenticated).toBe(false);
    expect(snapshot.permissions.canOpenWallet).toBe(true);
  });

  it('builds the shared UI runtime from the configured serverBaseUrl instead of the current origin', async () => {
    const fetch = vi.fn(async (input: string) => {
      if (input.endsWith('/api/node/info')) {
        return jsonResponse(200, {
          peer_id: '12D3KooWConfigured',
          DISPLAY_NAME: 'Configured Node',
        });
      }
      if (input.endsWith('/api/auth/status')) {
        return jsonResponse(200, { wallet_ui_configured: true });
      }
      if (input.endsWith('/api/auth/me')) {
        return jsonResponse(401, { code: 'unauthorized' });
      }
      if (input.endsWith('/api/directory/nodes?q=Configured%20Node')) {
        return jsonResponse(200, [{ peer_id: '12D3KooWConfigured', dn: 'Configured Node' }]);
      }
      if (input.endsWith('/api/directory/users?q=Configured%20Node')) {
        return jsonResponse(200, [{ peer_id: '12D3KooWConfigured', dn: 'Configured Operator' }]);
      }
      throw new Error(`unexpected fetch ${input}`);
    });

    const runtime = createUiRuntimeAdapter({
      config: {
        __SDN_CONFIG__: {
          serverBaseUrl: 'https://configured.example',
        },
      },
      fetch,
    });

    const connected = await runtime.connect();
    const directory = await runtime.directory.search('Configured Node');

    expect(connected.nodeContext.displayName).toBe('Configured Node');
    expect(connected.nodeContext.descriptorUrl).toBe('https://configured.example/api/module-delivery/provider');
    expect(fetch).toHaveBeenCalledWith(
      'https://configured.example/api/node/info',
      expect.objectContaining({ credentials: 'include' }),
    );
    expect(directory.nodes[0]?.peer_id).toBe('12D3KooWConfigured');
    expect(directory.users[0]?.dn).toBe('Configured Operator');
  });

  it('reuses the shared runtime contract for local Helia-backed directory records', async () => {
    const runtime = createUiRuntimeAdapter({
      listDirectoryRecords: async () => [
        {
          kind: 'node',
          peer_id: '16Uiu2HAmExample',
          dn: 'Local Node Example',
          bitcoin_address: 'bc1qexample',
        },
        {
          kind: 'user',
          peer_id: '16Uiu2HAmUser',
          dn: 'Local Operator Example',
        },
      ],
    });

    const snapshot = await runtime.connect();
    const directory = await runtime.directory.search('example');

    expect(snapshot.mode).toBe('local');
    expect(directory.nodes[0]?.dn).toBe('Local Node Example');
    expect(directory.users[0]?.dn).toBe('Local Operator Example');
  });

  it.each([
    // [trust_level from server, expected role, expected authenticated, expected canManageStore]
    ['admin', 'admin', true, true],
    ['ultimate', 'admin', true, true], // new PGP name above admin; treated as admin-equivalent
    ['trusted', 'trusted', true, true], // legacy name
    ['full', 'trusted', true, true], // new PGP name replacing 'trusted'
    ['standard', 'standard', true, false], // unchanged across both vocabularies
    ['limited', 'limited', true, false], // legacy name
    ['marginal', 'limited', true, false], // new PGP name replacing 'limited'
    ['untrusted', 'guest', false, false], // legacy name
    ['unknown', 'guest', false, false], // new PGP name replacing 'untrusted'
    ['never', 'guest', false, false], // new PGP name; explicitly blocked
  ] as const)(
    'maps trust_level %j to role %j (authenticated=%s, canManageStore=%s)',
    async (trustLevel, expectedRole, expectedAuthenticated, expectedCanManageStore) => {
      const fetch = vi.fn(async (input: string) => {
        if (input.endsWith('/api/node/info')) {
          return jsonResponse(200, { peer_id: '12D3KooWRemote' });
        }
        if (input.endsWith('/api/auth/status')) {
          return jsonResponse(200, { wallet_ui_configured: true });
        }
        if (input.endsWith('/api/auth/me')) {
          return jsonResponse(200, {
            name: 'Remote Operator',
            trust_level: trustLevel,
          });
        }
        throw new Error(`unexpected fetch ${input}`);
      });

      const adapter = createServerAdapter({
        target: { baseUrl: 'https://node.example' },
        fetch,
      });

      const snapshot = await adapter.connect();

      expect(snapshot.permissions.role).toBe(expectedRole);
      expect(snapshot.permissions.authenticated).toBe(expectedAuthenticated);
      expect(snapshot.permissions.canManageStore).toBe(expectedCanManageStore);
    },
  );

  it('shares one hosted runtime adapter across pages without rebuilding local globals in each page', async () => {
    const listDirectoryRecords = vi.fn(async () => [
      {
        kind: 'node',
        peer_id: '16Uiu2HAmSharedNode',
        dn: 'Shared Node',
      },
      {
        kind: 'user',
        peer_id: '16Uiu2HAmSharedUser',
        dn: 'Shared User',
      },
    ]);
    const fetch = vi.fn(async (input: string) => {
      if (input.endsWith('/api/node/info')) {
        return jsonResponse(200, {
          peer_id: '12D3KooWConfigured',
          DISPLAY_NAME: 'Configured Node',
        });
      }
      if (input.endsWith('/api/auth/status')) {
        return jsonResponse(200, { wallet_ui_configured: true });
      }
      if (input.endsWith('/api/auth/me')) {
        return jsonResponse(401, { code: 'unauthorized' });
      }
      if (input.endsWith('/api/directory/nodes?q=Configured%20Node')) {
        return jsonResponse(200, [{ peer_id: '12D3KooWConfigured', dn: 'Configured Node' }]);
      }
      if (input.endsWith('/api/directory/users?q=Configured%20Node')) {
        return jsonResponse(200, [{ peer_id: '12D3KooWConfigured', dn: 'Configured Operator' }]);
      }
      throw new Error(`unexpected fetch ${input}`);
    });

    const first = getSharedUiRuntimeAdapter({
      source: {
        __SDN_CONFIG__: {
          serverBaseUrl: 'https://configured.example',
        },
        __SDN_DIRECTORY__: {
          listDirectoryRecords,
        },
      },
      fetch,
    });
    const second = getSharedUiRuntimeAdapter();

    expect(second).toBe(first);

    const connected = await first.connect();
    const directory = await second.directory.search('Configured Node');

    expect(connected.nodeContext.peerId).toBe('12D3KooWConfigured');
    expect(directory.nodes[0]?.dn).toBe('Configured Node');
    expect(listDirectoryRecords).not.toHaveBeenCalled();
  });
});

function jsonResponse(status: number, payload: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async json() {
      return payload;
    },
  };
}
