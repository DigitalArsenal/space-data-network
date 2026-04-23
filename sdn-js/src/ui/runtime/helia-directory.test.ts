import { describe, expect, it } from 'vitest';

import { createHeliaDirectoryAdapter } from './helia-directory';

describe('createHeliaDirectoryAdapter', () => {
  it('searches locally indexed Helia directory records with the same result shape', async () => {
    const adapter = createHeliaDirectoryAdapter({
      listDirectoryRecords: async () => [
        {
          kind: 'node',
          peer_id: '16Uiu2HAmExample',
          dn: 'Node Example',
          bitcoin_address: 'bc1qexample',
        },
        {
          kind: 'user',
          peer_id: '16Uiu2HAmUser',
          dn: 'Operator Example',
        },
      ],
    });

    const snapshot = await adapter.search('example');

    expect(snapshot.nodes[0]?.peer_id).toBe('16Uiu2HAmExample');
    expect(snapshot.nodes[0]?.bitcoin_address).toBe('bc1qexample');
    expect(snapshot.users[0]?.dn).toBe('Operator Example');
  });

  it('filters records by the search query', async () => {
    const adapter = createHeliaDirectoryAdapter({
      listDirectoryRecords: async () => [
        {
          kind: 'node',
          peer_id: '16Uiu2HAmExample',
          dn: 'Node Example',
          bitcoin_address: 'bc1qexample',
        },
        {
          kind: 'user',
          peer_id: '16Uiu2HAmUser',
          dn: 'Operator Example',
        },
      ],
    });

    const snapshot = await adapter.search('bc1qexample');

    expect(snapshot.nodes).toHaveLength(1);
    expect(snapshot.users).toHaveLength(0);
  });
});
