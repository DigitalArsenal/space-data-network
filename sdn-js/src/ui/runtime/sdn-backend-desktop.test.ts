import { describe, expect, it, vi } from 'vitest';
import * as flatbuffers from 'flatbuffers';
import { EPM } from 'spacedatastandards.org/lib/js/EPM/EPM.js';
import { EntityType } from 'spacedatastandards.org/lib/js/EPM/EntityType.js';
import { createDesktopLocalBackend } from './sdn-backend-desktop';

describe('desktop-local SDN backend', () => {
  it('loads node profile and observed SDN peers through local desktop routes', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url === 'http://127.0.0.1:17890/api/node/epm') {
        return flatbufferResponse(buildNodeEpm({
          dn: 'Space Data Network Desktop',
          peer_id: '12D3KooWLocal',
          email: 'desktop@example.invalid',
        }));
      }
      if (url === 'http://127.0.0.1:17890/api/node/epm/json') {
        throw new Error('desktop-local node profile must use the FlatBuffer EPM route before JSON fallback');
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
              ipfs_artifact_addrs: [
                '/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j',
              ],
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
      data: { peer_id: '12D3KooWLocal', email: 'desktop@example.invalid' },
    });
    await expect(backend.listObservedPeers()).resolves.toMatchObject({
      ok: true,
      data: [{
        id: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
        trustLevel: 'observed',
        artifactPeerAddrs: ['/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j'],
      }],
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

  it('saves the node-self profile through the node EPM route', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'http://127.0.0.1:17890/api/node/epm') {
        expect(init?.method).toBe('PUT');
        return flatbufferResponse(buildNodeEpm({ dn: 'Updated Node', email: 'node@example.com', peer_id: '12D3KooWNode' }));
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    await expect(backend.saveNodeProfile({
      dn: 'Updated Node',
      email: 'node@example.com',
      peer_id: '12D3KooWNode',
    })).resolves.toMatchObject({
      ok: true,
      data: { email: 'node@example.com' },
    });
    expect(calls[0]?.url).toBe('http://127.0.0.1:17890/api/node/epm');
  });

  it('manages node identity settings and applies wallet-derived node identity', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'http://127.0.0.1:17890/api/node/identity/settings') {
        if (init?.method === 'PUT') {
          expect(JSON.parse(String(init.body))).toEqual({
            ttl_ms: 900000,
            flatbuffer_storage_path: '/Volumes/SDN/flatbuffers',
          });
          return jsonResponse({
            ttl_ms: 900000,
            flatbuffer_storage_path: '/Volumes/SDN/flatbuffers',
            updated_at: '2026-05-13T00:00:00.000Z',
          });
        }
        return jsonResponse({
          ttl_ms: 3600000,
          flatbuffer_storage_path: '/Users/tj/Library/Application Support/Space Data Network/flatbuffers',
          updated_at: '2026-05-12T00:00:00.000Z',
        });
      }
      if (url === 'http://127.0.0.1:17890/api/node/identity/settings/flatbuffer-storage-location') {
        expect(init?.method).toBe('POST');
        expect(JSON.parse(String(init?.body))).toEqual({ current_path: '/Volumes/SDN/flatbuffers' });
        return jsonResponse({ canceled: false, path: '/Volumes/SDN/flatbuffers' });
      }
      if (url === 'http://127.0.0.1:17890/api/node/identity/session') {
        expect(init?.method).toBe('DELETE');
        return jsonResponse({ unlocked: false });
      }
      if (url === 'http://127.0.0.1:17890/api/node/identity/wallet') {
        expect(init?.method).toBe('PUT');
        const body = JSON.parse(String(init?.body));
        expect(body).toMatchObject({
          replace: true,
          wallet_identity: {
            peer_id: '12D3KooWWallet',
            signing_public_key: 'aa'.repeat(32),
            encryption_public_key: 'bb'.repeat(32),
          },
        });
        return jsonResponse({
          status: 'updated',
          profile: {
            dn: 'Desktop Node',
            peer_id: '12D3KooWWallet',
            signing_public_key: 'aa'.repeat(32),
            encryption_public_key: 'bb'.repeat(32),
          },
        });
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    await expect(backend.getNodeIdentitySettings()).resolves.toMatchObject({
      ok: true,
      data: {
        ttlMs: 3600000,
        flatbufferStoragePath: '/Users/tj/Library/Application Support/Space Data Network/flatbuffers',
      },
    });
    await expect(backend.saveNodeIdentitySettings({
      ttlMs: 900000,
      flatbufferStoragePath: '/Volumes/SDN/flatbuffers',
    })).resolves.toMatchObject({
      ok: true,
      data: {
        ttlMs: 900000,
        flatbufferStoragePath: '/Volumes/SDN/flatbuffers',
      },
    });
    await expect(backend.selectFlatbufferStorageLocation('/Volumes/SDN/flatbuffers')).resolves.toMatchObject({
      ok: true,
      data: {
        canceled: false,
        path: '/Volumes/SDN/flatbuffers',
      },
    });
    await expect(backend.applyWalletNodeIdentity({
      peerId: '12D3KooWWallet',
      xpub: 'xpub-wallet',
      walletAccountId: 'wallet-2',
      walletAccountLabel: 'Operations',
      identityPublicKey: 'cc'.repeat(33),
      signingPublicKey: 'aa'.repeat(32),
      encryptionPublicKey: 'bb'.repeat(32),
      signature: 'dd'.repeat(64),
      signaturePayload: 'wallet-payload',
      signatureTimestamp: 1778700000,
    }, { replace: true })).resolves.toMatchObject({
      ok: true,
      data: {
        status: 'updated',
        profile: { peer_id: '12D3KooWWallet' },
      },
    });
    await expect(backend.logoutNodeIdentity()).resolves.toMatchObject({
      ok: true,
      data: { unlocked: false },
    });
    expect(calls.map((call) => call.url)).toEqual([
      'http://127.0.0.1:17890/api/node/identity/settings',
      'http://127.0.0.1:17890/api/node/identity/settings',
      'http://127.0.0.1:17890/api/node/identity/settings/flatbuffer-storage-location',
      'http://127.0.0.1:17890/api/node/identity/wallet',
      'http://127.0.0.1:17890/api/node/identity/session',
    ]);
  });

  it('loads and saves persisted wallet localStorage entries through the desktop identity route', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'http://127.0.0.1:17890/api/node/identity/wallet-storage') {
        if (init?.method === 'PUT') {
          expect(JSON.parse(String(init.body))).toEqual({
            entries: {
              wallet_storage_metadata: '{"method":"pin"}',
              wallet_storage_encrypted: '{"ciphertext":"abc"}',
              passkey_credential: null,
            },
          });
          return jsonResponse({
            encrypted_at_rest: true,
            entries: {
              wallet_storage_metadata: '{"method":"pin"}',
              wallet_storage_encrypted: '{"ciphertext":"abc"}',
            },
          });
        }
        return jsonResponse({
          encrypted_at_rest: true,
          entries: {
            wallet_storage_metadata: '{"method":"passkey"}',
            wallet_storage_passkey_credential: '{"id":"credential"}',
          },
        });
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    await expect(backend.getWalletStorage()).resolves.toMatchObject({
      ok: true,
      data: {
        encryptedAtRest: true,
        entries: {
          wallet_storage_metadata: '{"method":"passkey"}',
          wallet_storage_passkey_credential: '{"id":"credential"}',
        },
      },
    });
    await expect(backend.saveWalletStorage({
      wallet_storage_metadata: '{"method":"pin"}',
      wallet_storage_encrypted: '{"ciphertext":"abc"}',
      passkey_credential: null,
    })).resolves.toMatchObject({
      ok: true,
      data: {
        encryptedAtRest: true,
        entries: {
          wallet_storage_metadata: '{"method":"pin"}',
          wallet_storage_encrypted: '{"ciphertext":"abc"}',
        },
      },
    });
    expect(calls.map((call) => call.url)).toEqual([
      'http://127.0.0.1:17890/api/node/identity/wallet-storage',
      'http://127.0.0.1:17890/api/node/identity/wallet-storage',
    ]);
  });

  it('surfaces wallet key mismatches from the desktop identity route so the UI can confirm replacement', async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === 'http://127.0.0.1:17890/api/node/identity/wallet') {
        expect(init?.method).toBe('PUT');
        return jsonResponse({
          status: 'mismatch',
          current: {
            peer_id: '12D3KooWCurrent',
            signing_public_key: 'aa'.repeat(32),
          },
          proposed: {
            peer_id: '12D3KooWProposed',
            signing_public_key: 'bb'.repeat(32),
          },
        }, 409);
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    await expect(backend.applyWalletNodeIdentity({
      peerId: '12D3KooWProposed',
      signingPublicKey: 'bb'.repeat(32),
    }, { replace: false })).resolves.toMatchObject({
      ok: true,
      data: {
        status: 'mismatch',
        current: { peer_id: '12D3KooWCurrent' },
        proposed: { peer_id: '12D3KooWProposed' },
      },
    });
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

  it('queries raw FlatSQL data records through protected data endpoints', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'http://127.0.0.1:17890/api/v1/data/summary') {
        return jsonResponse({
          total_records: 1,
          total_bytes: 128,
          schemas: [{ schema_name: 'EPM.fbs', count: 1, total_bytes: 128 }],
          sources: [{ schema_name: 'EPM.fbs', provider_id: 'local-node', source_name: 'local-epm', batch_id: 'local', count: 1, total_bytes: 128 }],
        });
      }
      if (url === 'http://127.0.0.1:17890/api/v1/data/query') {
        expect(init?.method).toBe('POST');
        expect(init?.credentials).toBe('include');
        if (acceptHeader(init).includes('application/vnd.sdn.flatbuffers.stream')) {
          return flatbufferStreamResponse([new Uint8Array([0, 1, 2, 3])]);
        }
        return jsonResponse({
          schema: 'EPM.fbs',
          count: 1,
          results: [{
            schema_name: 'EPM.fbs',
            cid: '12D3KooWEPM',
            peer_id: '12D3KooWEPM',
            provider_id: 'local-node',
            source_name: 'local-epm',
            size_bytes: 128,
          }],
        });
      }
      if (url === 'http://127.0.0.1:17890/api/v1/data/records/EPM.fbs/12D3KooWEPM') {
        return flatbufferResponse(new Uint8Array([0, 1, 2, 3]));
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    await expect(backend.getDataSummary()).resolves.toMatchObject({
      ok: true,
      data: { totalRecords: 1, schemas: [{ schemaName: 'EPM.fbs', count: 1 }] },
    });
    await expect(backend.queryRawData({
      schema: 'EPM.fbs',
      providerId: 'local-node',
      sourceName: 'local-epm',
      syncFilter: "FILE_ID LIKE 'celestrak:%'",
      limit: 10,
    })).resolves.toMatchObject({
      ok: true,
      data: [{ schemaName: 'EPM.fbs', cid: '12D3KooWEPM', dataBytes: new Uint8Array([0, 1, 2, 3]) }],
    });
    await expect(backend.readRawDataRecord('EPM.fbs', '12D3KooWEPM')).resolves.toMatchObject({
      ok: true,
      data: { schemaName: 'EPM.fbs', cid: '12D3KooWEPM', bytes: new Uint8Array([0, 1, 2, 3]) },
    });
    expect(calls).toEqual(expect.arrayContaining([
      expect.objectContaining({ url: 'http://127.0.0.1:17890/api/v1/data/summary', init: expect.objectContaining({ credentials: 'include' }) }),
      expect.objectContaining({ url: 'http://127.0.0.1:17890/api/v1/data/query', init: expect.objectContaining({ method: 'POST', credentials: 'include' }) }),
    ]));
    expect(calls.filter((call) => call.url === 'http://127.0.0.1:17890/api/v1/data/query')).toHaveLength(2);
    for (const call of calls.filter((entry) => entry.url === 'http://127.0.0.1:17890/api/v1/data/query')) {
      expect(JSON.parse(String(call.init?.body))).toMatchObject({ sync_filter: "FILE_ID LIKE 'celestrak:%'" });
    }
  });

  it('does not send raw SQL to the desktop or remote node HTTP APIs', async () => {
    const fetchMock = vi.fn(async () => {
      throw new Error('SQL fetch should not be called');
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    await expect(backend.runSqlQuery('SELECT * FROM OMM LIMIT 10')).resolves.toMatchObject({
      ok: false,
      capability: {
        id: 'runSqlQuery',
        state: 'local-only',
      },
      data: [],
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('scans raw data refs through the local desktop data endpoint', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'http://127.0.0.1:17890/api/v1/data/scan') {
        expect(init?.method).toBe('POST');
        expect(init?.credentials).toBe('include');
        return jsonResponse({
          schema: 'OMM.fbs',
          total_count: 31069,
          count: 1,
          limit: 1,
          offset: 0,
          cursor: 'MA',
          next_cursor: 'MQ',
          snapshot_id: 'snapshot-1',
          head: 'snapshot-1',
          high_water_mark: '1:2:3:31069',
          scan_hash: 'scan-hash',
          chunk_hash: 'scan-hash',
          query_profile: 'ordered-offset-v1',
          sync_protocol: '/space-data-network/flatsql-sync/1.0.0',
          max_chunk_size: 50000,
          transports: ['http', 'libp2p-websocket', 'libp2p-webrtc'],
          results: [{
            schema_name: 'OMM.fbs',
            cid: 'omm-cid-1',
            peer_id: 'source:celestrak',
            provider_id: 'space-data-network-02',
            source_name: 'celestrak-gp',
            size_bytes: 256,
          }],
        });
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    const result = await backend.scanRawData({ schema: 'OMM.fbs', providerId: 'space-data-network-02', sourceName: 'celestrak-gp', limit: 1 });

    expect(result).toMatchObject({
      ok: true,
      data: {
        schema: 'OMM.fbs',
        totalCount: 31069,
        count: 1,
        nextCursor: 'MQ',
        snapshotId: 'snapshot-1',
        head: 'snapshot-1',
        highWaterMark: '1:2:3:31069',
        scanHash: 'scan-hash',
        chunkHash: 'scan-hash',
        queryProfile: 'ordered-offset-v1',
        syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
        maxChunkSize: 50000,
        results: [{ schemaName: 'OMM.fbs', cid: 'omm-cid-1' }],
      },
    });
    expect(result.data?.results[0]).not.toHaveProperty('dataBytes');
    expect(calls).toHaveLength(1);
    expect(JSON.parse(String(calls[0]?.init?.body))).toMatchObject({
      schema: 'OMM.fbs',
      include_data: false,
      provider_id: 'space-data-network-02',
      source_name: 'celestrak-gp',
      limit: 1,
    });
  });

  it('routes desktop raw scan and stream requests to an isolated datastore namespace', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'http://127.0.0.1:17890/api/v1/data/scan') {
        return jsonResponse({
          schema: 'OMM.fbs',
          total_count: 1,
          count: 1,
          limit: 1,
          offset: 0,
          cursor: 'MA',
          next_cursor: '',
          scan_hash: 'namespace-scan-hash',
          chunk_hash: 'namespace-scan-hash',
          results: [{
            schema_name: 'OMM.fbs',
            cid: 'namespace-omm-cid',
            peer_id: 'source:namespace',
            size_bytes: 4,
          }],
        });
      }
      if (url === 'http://127.0.0.1:17890/api/v1/data/stream') {
        return flatbufferStreamResponse([new Uint8Array([1, 3, 3, 7])]);
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    const scan = await backend.scanRawData({
      schema: 'OMM.fbs',
      datastoreKey: 'sdn-ds-v1-history',
      limit: 1,
    });
    const stream = await backend.streamRawData({
      schema: 'OMM.fbs',
      datastoreKey: 'sdn-ds-v1-history',
      scanHash: scan.data?.scanHash,
      records: scan.data?.results ?? [],
    });

    expect(scan).toMatchObject({
      ok: true,
      data: { results: [{ cid: 'namespace-omm-cid' }] },
    });
    expect(stream).toMatchObject({
      ok: true,
      data: [{ cid: 'namespace-omm-cid', dataBytes: new Uint8Array([1, 3, 3, 7]) }],
    });
    expect(calls.map((call) => JSON.parse(String(call.init?.body)))).toEqual([
      expect.objectContaining({ datastore_key: 'sdn-ds-v1-history' }),
      expect.objectContaining({ datastore_key: 'sdn-ds-v1-history' }),
    ]);
  });

  it('streams local proxy raw FlatBuffers for scan-bound refs', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'http://127.0.0.1:17890/api/v1/data/stream') {
        expect(init?.method).toBe('POST');
        expect(init?.credentials).toBe('include');
        expect(acceptHeader(init)).toContain('application/vnd.sdn.flatbuffers.stream');
        expect(JSON.parse(String(init?.body))).toMatchObject({
          schema: 'OMM.fbs',
          scan_hash: 'scan-hash',
          records: [{
            schema_name: 'OMM.fbs',
            cid: 'omm-cid-1',
            peer_id: 'source:celestrak',
            provider_id: 'space-data-network-02',
            source_name: 'celestrak-gp',
          }],
        });
        return flatbufferStreamResponse([new Uint8Array([4, 3, 2, 1])]);
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    const result = await backend.streamRawData({
      schema: 'OMM.fbs',
      scanHash: 'scan-hash',
      records: [{
        schemaName: 'OMM.fbs',
        cid: 'omm-cid-1',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        sizeBytes: 4,
      }],
    });

    expect(result).toMatchObject({
      ok: true,
      data: [{ schemaName: 'OMM.fbs', cid: 'omm-cid-1', dataBytes: new Uint8Array([4, 3, 2, 1]) }],
    });
    expect(calls).toHaveLength(1);
  });

  it('manages node access grants through the wallet-cookie auth API', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'http://127.0.0.1:17890/api/auth/users') {
        if (init?.method === 'POST') return jsonResponse({ status: 'created' });
        return jsonResponse([
          { xpub: 'xpub-config-admin', name: 'Config Admin', trust_level: 'admin', source: 'config' },
          { xpub: 'xpub-operator', name: 'Operator', trust_level: 'standard', source: 'database' },
        ]);
      }
      if (url === 'http://127.0.0.1:17890/api/auth/users/xpub-operator') {
        if (init?.method === 'PUT') return jsonResponse({ status: 'updated' });
        if (init?.method === 'DELETE') return jsonResponse({ status: 'removed' });
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createDesktopLocalBackend({ desktopProxyUrl: 'http://127.0.0.1:17890', fetch: fetchMock });

    await expect(backend.listNodeAccessUsers()).resolves.toMatchObject({
      ok: true,
      data: expect.arrayContaining([
        expect.objectContaining({ xpub: 'xpub-config-admin', trustLevel: 'admin', source: 'config', configManaged: true }),
      ]),
    });
    await expect(backend.saveNodeAccessUser({ xpub: 'xpub-new-admin', name: 'New Admin', trustLevel: 'admin' })).resolves.toMatchObject({ ok: true });
    await expect(backend.revokeNodeAdmin('xpub-operator')).resolves.toMatchObject({ ok: true });
    await expect(backend.deleteNodeAccessUser('xpub-operator')).resolves.toMatchObject({ ok: true });
    expect(calls).toEqual(expect.arrayContaining([
      expect.objectContaining({
        url: 'http://127.0.0.1:17890/api/auth/users',
        init: expect.objectContaining({ credentials: 'include' }),
      }),
      expect.objectContaining({
        url: 'http://127.0.0.1:17890/api/auth/users',
        init: expect.objectContaining({ method: 'POST', credentials: 'include' }),
      }),
      expect.objectContaining({
        url: 'http://127.0.0.1:17890/api/auth/users/xpub-operator',
        init: expect.objectContaining({ method: 'PUT', credentials: 'include' }),
      }),
      expect.objectContaining({
        url: 'http://127.0.0.1:17890/api/auth/users/xpub-operator',
        init: expect.objectContaining({ method: 'DELETE', credentials: 'include' }),
      }),
    ]));
  });
});

function buildNodeEpm(profile: { dn: string; peer_id: string; email?: string }): Uint8Array {
  const builder = new flatbuffers.Builder(1024);
  const dn = builder.createString(profile.dn);
  const email = profile.email ? builder.createString(profile.email) : 0;
  const address = builder.createString(`/ip4/127.0.0.1/tcp/4001/p2p/${profile.peer_id}`);
  const addresses = EPM.createMultiformatAddressVector(builder, [address]);

  EPM.startEPM(builder);
  EPM.addDn(builder, dn);
  if (email) EPM.addEmail(builder, email);
  EPM.addMultiformatAddress(builder, addresses);
  EPM.addEntityType(builder, EntityType.Node);
  const epm = EPM.endEPM(builder);
  EPM.finishSizePrefixedEPMBuffer(builder, epm);
  return builder.asUint8Array();
}

function flatbufferResponse(payload: Uint8Array) {
  return new Response(payload, {
    status: 200,
    headers: { 'content-type': 'application/x-flatbuffers' },
  });
}

function jsonResponse(payload: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => payload,
  } as Response;
}

function flatbufferStreamResponse(records: Uint8Array[]) {
  const size = records.reduce((total, record) => total + 4 + record.byteLength, 0);
  const body = new Uint8Array(size);
  const view = new DataView(body.buffer);
  let offset = 0;
  for (const record of records) {
    view.setUint32(offset, record.byteLength, false);
    offset += 4;
    body.set(record, offset);
    offset += record.byteLength;
  }
  return new Response(body, {
    status: 200,
    headers: { 'content-type': 'application/vnd.sdn.flatbuffers.stream' },
  });
}

function acceptHeader(init?: RequestInit): string {
  const headers = init?.headers;
  if (!headers) return '';
  if (headers instanceof Headers) return headers.get('accept') ?? '';
  if (Array.isArray(headers)) return String(headers.find(([key]) => key.toLowerCase() === 'accept')?.[1] ?? '');
  return String((headers as Record<string, string>).accept ?? (headers as Record<string, string>).Accept ?? '');
}
