import { describe, expect, it, vi } from 'vitest';
import * as flatbuffers from 'flatbuffers';
import { EPM } from 'spacedatastandards.org/lib/js/EPM/EPM.js';
import { EntityType } from 'spacedatastandards.org/lib/js/EPM/EntityType.js';
import { createBrowserNodeBackend } from './sdn-backend-browser';
import { createSdnBackend } from './sdn-backend-factory';
import { createRemoteSdnBackend } from './sdn-backend-remote';

describe('remote-sdn backend', () => {
  it('loads node profile and observed SDN peers from a remote server', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url === 'https://sdn.spaceaware.io/api/node/epm') {
        return flatbufferResponse(buildNodeEpm({ dn: 'SDN Public Node', peer_id: '16Uiu2HAmRemote' }));
      }
      if (url === 'https://sdn.spaceaware.io/api/node/info' || url === 'https://sdn.spaceaware.io/api/node/epm/json') {
        throw new Error('remote node profile must use the FlatBuffer EPM route before JSON fallback');
      }
      if (url === 'https://sdn.spaceaware.io/api/peers/sdn') {
        return jsonResponse({
          peers: [
            {
              peer_id: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
              display_name: 'CelesTrak Provider',
              multiaddrs: ['/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4'],
              trust: 'trusted',
              agent_version: 'spacedatanetwork/1.0.3',
            },
          ],
        });
      }
      throw new Error(`unexpected ${url}`);
    });

    const backend = createRemoteSdnBackend({
      mode: 'remote-sdn',
      serverUrl: 'https://sdn.spaceaware.io',
      kuboApiUrl: null,
      gatewayUrl: null,
      desktopProxyUrl: null,
      fetch: fetchMock,
    });

    expect(backend.mode).toBe('remote-sdn');
    await expect(backend.getNodeProfile()).resolves.toMatchObject({
      ok: true,
      data: { peer_id: '16Uiu2HAmRemote' },
    });
    await expect(backend.listObservedPeers()).resolves.toMatchObject({
      ok: true,
      data: [{ id: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4', name: 'CelesTrak Provider' }],
    });
  });

  it('falls back to public node info when hosted EPM routes are authenticated', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url === 'https://sdn.spaceaware.io/api/identity/epms') {
        return { ok: false, status: 302, json: async () => ({}) } as Response;
      }
      if (url === 'https://sdn.spaceaware.io/api/node/epm') {
        return flatbufferResponse(buildNodeEpm({ dn: 'SDN Public Node', peer_id: '16Uiu2HAmRemote' }));
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createRemoteSdnBackend({
      mode: 'remote-sdn',
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: fetchMock,
    });

    await expect(backend.listHostedEpms()).resolves.toMatchObject({
      ok: true,
      data: [{ id: 'self', kind: 'node-self', label: 'SDN Public Node', peerId: '16Uiu2HAmRemote' }],
    });
  });

  it('saves authenticated node profile edits through the node EPM route', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'https://sdn.spaceaware.io/api/node/epm') {
        expect(init?.method).toBe('PUT');
        return flatbufferResponse(buildNodeEpm({ dn: 'SDN Public Node', email: 'node@example.com', peer_id: '16Uiu2HAmRemote' }));
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createRemoteSdnBackend({
      mode: 'remote-sdn',
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: fetchMock,
    });

    await expect(backend.saveNodeProfile({
      dn: 'SDN Public Node',
      email: 'node@example.com',
    })).resolves.toMatchObject({
      ok: true,
      data: { email: 'node@example.com' },
    });
    expect(calls[0]?.init).toEqual(expect.objectContaining({
      credentials: 'include',
      headers: expect.objectContaining({ 'x-requested-with': 'sdn-ui' }),
    }));
  });

  it('returns degraded results for missing remote endpoints', async () => {
    const backend = createRemoteSdnBackend({
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: vi.fn(async () => ({ ok: false, status: 404, json: async () => ({}) } as Response)),
    });

    await expect(backend.listObjects()).resolves.toMatchObject({
      ok: false,
      capability: {
        id: 'listObjects',
        state: 'degraded',
      },
    });
  });

  it('queries remote raw FlatSQL data with wallet-cookie credentials', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'https://sdn.spaceaware.io/api/v1/data/summary') {
        return jsonResponse({
          total_records: 1,
          total_bytes: 256,
          schemas: [{ schema_name: 'EPM.fbs', count: 1, total_bytes: 256 }],
          sources: [{ schema_name: 'EPM.fbs', provider_id: 'local-node', source_name: 'local-epm', batch_id: 'local', count: 1, total_bytes: 256 }],
        });
      }
      if (url === 'https://sdn.spaceaware.io/api/v1/data/query') {
        expect(init?.method).toBe('POST');
        expect(init?.credentials).toBe('include');
        if (acceptHeader(init).includes('application/vnd.sdn.flatbuffers.stream')) {
          return flatbufferStreamResponse([new Uint8Array([0, 1, 2, 3])]);
        }
        return jsonResponse({
          schema: 'EPM.fbs',
          count: 1,
          results: [{ schema_name: 'EPM.fbs', cid: '16Uiu2HRemote', peer_id: '16Uiu2HRemote', size_bytes: 4 }],
        });
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createRemoteSdnBackend({ serverUrl: 'https://sdn.spaceaware.io', fetch: fetchMock });

    await expect(backend.getDataSummary()).resolves.toMatchObject({ ok: true, data: { totalRecords: 1 } });
    await expect(backend.queryRawData({ schema: 'EPM.fbs', providerId: 'local-node' })).resolves.toMatchObject({
      ok: true,
      data: [{ schemaName: 'EPM.fbs', cid: '16Uiu2HRemote', dataBytes: new Uint8Array([0, 1, 2, 3]) }],
    });
    expect(calls).toEqual(expect.arrayContaining([
      expect.objectContaining({ url: 'https://sdn.spaceaware.io/api/v1/data/summary', init: expect.objectContaining({ credentials: 'include' }) }),
      expect.objectContaining({ url: 'https://sdn.spaceaware.io/api/v1/data/query', init: expect.objectContaining({ method: 'POST', credentials: 'include' }) }),
    ]));
    expect(calls.filter((call) => call.url === 'https://sdn.spaceaware.io/api/v1/data/query')).toHaveLength(2);
    const queryCalls = calls.filter((call) => call.url === 'https://sdn.spaceaware.io/api/v1/data/query');
    const metadataQueryCall = queryCalls.find((call) => !acceptHeader(call.init).includes('application/vnd.sdn.flatbuffers.stream'));
    expect(JSON.parse(String(metadataQueryCall?.init?.body))).toMatchObject({ include_data: false });
  });

  it('scans remote raw data refs without downloading FlatBuffer bytes', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'https://sdn.spaceaware.io/api/v1/data/scan') {
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
          scan_hash: 'scan-hash',
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
    const backend = createRemoteSdnBackend({ serverUrl: 'https://sdn.spaceaware.io', fetch: fetchMock });

    const result = await backend.scanRawData({ schema: 'OMM.fbs', providerId: 'space-data-network-02', sourceName: 'celestrak-gp', limit: 1 });

    expect(result).toMatchObject({
      ok: true,
      data: {
        schema: 'OMM.fbs',
        totalCount: 31069,
        count: 1,
        nextCursor: 'MQ',
        scanHash: 'scan-hash',
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

  it('streams remote raw FlatBuffers for scan-bound refs', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'https://sdn.spaceaware.io/api/v1/data/stream') {
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
        return flatbufferStreamResponse([new Uint8Array([9, 8, 7, 6])]);
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createRemoteSdnBackend({ serverUrl: 'https://sdn.spaceaware.io', fetch: fetchMock });

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
      data: [{ schemaName: 'OMM.fbs', cid: 'omm-cid-1', dataBytes: new Uint8Array([9, 8, 7, 6]) }],
    });
    expect(calls).toHaveLength(1);
  });
});

describe('browser-node backend', () => {
  it('represents browser-node as an explicit deferred degraded adapter', async () => {
    await expect(createBrowserNodeBackend().getCapabilities()).resolves.toContainEqual({
      id: 'browser-node',
      state: 'degraded',
      reason: 'browser-node adapter is scheduled for Milestone 4',
    });
  });
});

describe('SDN backend factory', () => {
  it('selects the requested backend mode', () => {
    expect(createSdnBackend({ mode: 'remote-sdn', serverUrl: 'https://sdn.spaceaware.io' }).mode).toBe('remote-sdn');
    expect(createSdnBackend({ mode: 'browser-node' }).mode).toBe('browser-node');
    expect(createSdnBackend({ mode: 'desktop-local' }).mode).toBe('desktop-local');
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

function jsonResponse(payload: unknown) {
  return {
    ok: true,
    status: 200,
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
