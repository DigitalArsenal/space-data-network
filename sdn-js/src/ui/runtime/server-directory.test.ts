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

  it('imports directory records through the authenticated daemon API', async () => {
    const fetch = vi.fn(async (input: string, init?: RequestInit) => {
      if (input.endsWith('/api/v1/admin/directory/import')) {
        expect(init?.method).toBe('POST');
        expect(init?.credentials).toBe('include');
        expect(JSON.parse(String(init?.body))).toEqual({
          kind: 'node',
          epm_json: {
            peer_id: '16Uiu2HAmUploaded',
            dn: 'Uploaded Node',
          },
        });
        return jsonResponse(200, {
          imported: 1,
          nodes: [{ kind: 'node', peer_id: '16Uiu2HAmUploaded', dn: 'Uploaded Node' }],
          users: [],
        });
      }
      throw new Error(`unexpected fetch ${input}`);
    });

    const adapter = createServerDirectoryAdapter({
      baseUrl: 'https://node.example',
      fetch,
    });

    const result = await adapter.importRecord({
      kind: 'node',
      epm_json: {
        peer_id: '16Uiu2HAmUploaded',
        dn: 'Uploaded Node',
      },
    });

    expect(result.imported).toBe(1);
    expect(result.nodes[0]?.peer_id).toBe('16Uiu2HAmUploaded');
  });

  it('does not require a selector kind when importing entity typed EPM JSON', async () => {
    const fetch = vi.fn(async (input: string, init?: RequestInit) => {
      if (input.endsWith('/api/v1/admin/directory/import')) {
        expect(JSON.parse(String(init?.body))).toEqual({
          epm_json: {
            entity_type: 'node',
            peer_id: '16Uiu2HAmEntityTypedNode',
            dn: 'Entity Typed Node',
          },
        });
        return jsonResponse(200, {
          imported: 1,
          nodes: [{ kind: 'node', peer_id: '16Uiu2HAmEntityTypedNode', dn: 'Entity Typed Node' }],
          users: [],
        });
      }
      throw new Error(`unexpected fetch ${input}`);
    });

    const adapter = createServerDirectoryAdapter({
      baseUrl: 'https://node.example',
      fetch,
    });

    const result = await adapter.importRecord({
      epm_json: {
        entity_type: 'node',
        peer_id: '16Uiu2HAmEntityTypedNode',
        dn: 'Entity Typed Node',
      },
    });

    expect(result.nodes[0]?.peer_id).toBe('16Uiu2HAmEntityTypedNode');
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
