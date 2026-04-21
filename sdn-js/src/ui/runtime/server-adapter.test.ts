import { describe, expect, it, vi } from 'vitest';

import { createServerAdapter } from './server-adapter';

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
