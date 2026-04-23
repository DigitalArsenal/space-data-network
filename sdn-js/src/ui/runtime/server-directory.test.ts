import { describe, expect, it, vi } from 'vitest';

import { createServerDirectoryAdapter } from './server-directory';

describe('createServerDirectoryAdapter', () => {
  it('loads node and user directory results from the daemon APIs', async () => {
    const fetch = vi.fn(async (input: string) => {
      if (input.endsWith('/api/directory/nodes?q=')) {
        return jsonResponse(200, [{ peer_id: '16Uiu2HAmExample', dn: 'Node Example' }]);
      }
      if (input.endsWith('/api/directory/users?q=')) {
        return jsonResponse(200, [{ dn: 'Operator Example' }]);
      }
      throw new Error(`unexpected fetch ${input}`);
    });

    const adapter = createServerDirectoryAdapter({
      baseUrl: 'https://node.example',
      fetch,
    });

    const snapshot = await adapter.search('');

    expect(snapshot.nodes).toHaveLength(1);
    expect(snapshot.nodes[0]?.peer_id).toBe('16Uiu2HAmExample');
    expect(snapshot.users).toHaveLength(1);
    expect(snapshot.users[0]?.dn).toBe('Operator Example');
  });

  it('uses the encoded query for directory searches', async () => {
    const fetch = vi.fn(async (input: string) => {
      if (input.endsWith('/api/directory/nodes?q=bc1qexample%20node')) {
        return jsonResponse(200, {
          results: [{ kind: 'node', peer_id: '16Uiu2HAmExample', bitcoin_address: 'bc1qexample' }],
        });
      }
      if (input.endsWith('/api/directory/users?q=bc1qexample%20node')) {
        return jsonResponse(200, {
          results: [{ kind: 'user', dn: 'Example Operator' }],
        });
      }
      throw new Error(`unexpected fetch ${input}`);
    });

    const adapter = createServerDirectoryAdapter({
      baseUrl: 'https://node.example',
      fetch,
    });

    const snapshot = await adapter.search('bc1qexample node');

    expect(snapshot.query).toBe('bc1qexample node');
    expect(snapshot.nodes[0]?.bitcoin_address).toBe('bc1qexample');
    expect(snapshot.users[0]?.dn).toBe('Example Operator');
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
