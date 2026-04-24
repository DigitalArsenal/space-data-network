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

  it('imports records into the local Helia directory overlay', async () => {
    const adapter = createHeliaDirectoryAdapter({
      listDirectoryRecords: async () => [],
    });

    const imported = await adapter.importRecord({
      kind: 'user',
      epm_json: {
        peer_id: '16Uiu2HAmUserUpload',
        dn: 'Uploaded Operator',
        legal_name: 'Uploaded Operator LLC',
      },
    });
    const snapshot = await adapter.search('uploaded');

    expect(imported.imported).toBe(1);
    expect(snapshot.users[0]?.peer_id).toBe('16Uiu2HAmUserUpload');
    expect(snapshot.users[0]?.legal_name).toBe('Uploaded Operator LLC');
  });

  it('infers imported node records from EPM entity_type without a selector kind', async () => {
    const adapter = createHeliaDirectoryAdapter({
      listDirectoryRecords: async () => [],
    });

    const imported = await adapter.importRecord({
      epm_json: {
        entity_type: 'node',
        peer_id: '16Uiu2HAmEntityTypedNode',
        dn: 'Entity Typed Node',
      },
    });
    const snapshot = await adapter.search('Entity Typed');

    expect(imported.nodes[0]?.peer_id).toBe('16Uiu2HAmEntityTypedNode');
    expect(snapshot.nodes[0]?.dn).toBe('Entity Typed Node');
    expect(snapshot.users).toHaveLength(0);
  });

  it('defaults imported records without entity_type to user records', async () => {
    const adapter = createHeliaDirectoryAdapter({
      listDirectoryRecords: async () => [],
    });

    await adapter.importRecord({
      epm_json: {
        peer_id: '16Uiu2HAmDefaultUser',
        dn: 'Default User',
      },
    });
    const snapshot = await adapter.search('Default User');

    expect(snapshot.nodes).toHaveLength(0);
    expect(snapshot.users[0]?.peer_id).toBe('16Uiu2HAmDefaultUser');
  });
});
