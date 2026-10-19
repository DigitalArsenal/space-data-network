import { describe, expect, it, vi } from 'vitest';
import { FLATBUFFER_STREAM_TYPE, recordCid } from './qrp-frames';
import { decodeQrpRequest, encodeQrpRunAnswer, encodeQrpTablePage, type QrpCell } from './qrp-request';
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
              peer_id: '16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
              display_name: 'CelesTrak Provider',
              multiaddrs: ['/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3'],
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
      data: [{ id: '16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3', name: 'CelesTrak Provider' }],
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
        return qrpTableResponse([{ name: 'schema_name' }, { name: 'source_name' }, { name: 'provider_id' }, { name: 'batch_id' }, { name: 'producer_peer_id' }, { name: 'producer_public_key' }, { name: 'count', type: 'int64' }, { name: 'total_bytes', type: 'int64' }], [['EPM.fbs', '', '', '', '', '', 1, 256], ['EPM.fbs', 'local-epm', 'local-node', 'local', '16Uiu2HProducer', 'producer-public-key', 1, 256]]);
      }
      if (url === 'https://sdn.spaceaware.io/api/v1/data/query') {
        expect(init?.method).toBe('POST');
        expect(init?.credentials).toBe('include');
        expect(acceptHeader(init)).toContain(FLATBUFFER_STREAM_TYPE);
        return qrpQueryResponse([new Uint8Array([0, 1, 2, 3])], 'EPM.fbs', { providerId: 'local-node' });
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createRemoteSdnBackend({ serverUrl: 'https://sdn.spaceaware.io', fetch: fetchMock });

    await expect(backend.getDataSummary()).resolves.toMatchObject({
      ok: true,
      data: {
        totalRecords: 1,
        sources: [expect.objectContaining({
          producerPeerId: '16Uiu2HProducer',
          producerPublicKey: 'producer-public-key',
        })],
      },
    });
    await expect(backend.queryRawData({
      schema: 'EPM.fbs',
      providerId: 'local-node',
      syncFilter: "FILE_ID LIKE 'celestrak:%'",
    })).resolves.toMatchObject({
      ok: true,
      data: [{
        schemaName: 'EPM.fbs',
        // cid = the store's CIDv1 (raw codec, sha2-256) of the frame bytes.
        cid: await recordCid(new Uint8Array([0, 1, 2, 3])),
        providerId: 'local-node',
        sizeBytes: 4,
        dataBytes: new Uint8Array([0, 1, 2, 3]),
      }],
    });
    expect(calls).toEqual(expect.arrayContaining([
      expect.objectContaining({ url: 'https://sdn.spaceaware.io/api/v1/data/summary', init: expect.objectContaining({ credentials: 'include' }) }),
      expect.objectContaining({ url: 'https://sdn.spaceaware.io/api/v1/data/query', init: expect.objectContaining({ method: 'POST', credentials: 'include' }) }),
    ]));
    const queryCalls = calls.filter((call) => call.url === 'https://sdn.spaceaware.io/api/v1/data/query');
    expect(queryCalls).toHaveLength(1);
    expect(acceptHeader(queryCalls[0].init)).toContain('application/vnd.sdn.flatbuffers.stream');
    expect(decodeQrpRequest(queryCalls[0].init?.body as Uint8Array)).toMatchObject({ sql: "FILE_ID LIKE 'celestrak:%'" });
  });

  it('does not send raw SQL to remote peers', async () => {
    const fetchMock = vi.fn(async () => {
      throw new Error('remote SQL fetch should not be called');
    });
    const backend = createRemoteSdnBackend({ serverUrl: 'https://sdn.spaceaware.io', fetch: fetchMock });

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

  it('searches providers and data through the remote shared search routes', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'https://sdn.spaceaware.io/api/v1/search/providers') {
        expect(init?.method).toBe('POST');
        expect(init?.credentials).toBe('include');
        expect(JSON.parse(String(init?.body))).toMatchObject({
          query: 'celestrak',
          schema: 'OMM.fbs',
          provider_id: 'space-data-network-02',
          source_name: 'celestrak-gp',
          mode: 'daemon',
          limit: 25,
        });
        return jsonResponse({
          count: 1,
          results: [{
            peer_id: '16Uiu2HCelesTrak',
            dn: 'CelesTrak Provider',
            provider_id: 'space-data-network-02',
            schema_name: 'OMM.fbs',
            source_name: 'celestrak-gp',
            local_rows: 31069,
          }],
        });
      }
      if (url === 'https://sdn.spaceaware.io/api/v1/search/data') {
        expect(init?.method).toBe('POST');
        expect(init?.credentials).toBe('include');
        return jsonResponse({
          count: 1,
          results: [{
            schema_name: 'OMM.fbs',
            provider_id: 'space-data-network-02',
            source_name: 'celestrak-gp',
            local_rows: 31069,
          }],
        });
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createRemoteSdnBackend({ serverUrl: 'https://sdn.spaceaware.io', fetch: fetchMock });

    await expect(backend.searchProviders({
      query: 'celestrak',
      schema: 'OMM.fbs',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      mode: 'daemon',
      limit: 25,
    })).resolves.toMatchObject({
      ok: true,
      data: {
        count: 1,
        results: [expect.objectContaining({
          peerId: '16Uiu2HCelesTrak',
          displayName: 'CelesTrak Provider',
          providerId: 'space-data-network-02',
          schemaName: 'OMM.fbs',
          sourceName: 'celestrak-gp',
          localRows: 31069,
        })],
      },
    });
    await expect(backend.searchData({
      schema: 'OMM.fbs',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
    })).resolves.toMatchObject({
      ok: true,
      data: {
        count: 1,
        results: [expect.objectContaining({
          schemaName: 'OMM.fbs',
          providerId: 'space-data-network-02',
          sourceName: 'celestrak-gp',
          localRows: 31069,
        })],
      },
    });
    expect(calls.map((call) => call.url)).toEqual([
      'https://sdn.spaceaware.io/api/v1/search/providers',
      'https://sdn.spaceaware.io/api/v1/search/data',
    ]);
  });

  it('screens encrypted maneuver ephemeris through the remote conjunction route', async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url === 'https://sdn.spaceaware.io/api/v1/conjunction/screen') {
        expect(init?.method).toBe('POST');
        expect(init?.credentials).toBe('include');
        expect(JSON.parse(String(init?.body))).toMatchObject({
          primary_schema: 'MPE.fbs',
          secondary_schema: 'OMM.fbs',
          encrypted: true,
          grant_id: 'grant-private-mpe',
          channel_id: 'channel-private-ca',
          assessor_peer_id: '16Uiu2HAssessor',
          include_provenance: true,
          limit: 25,
        });
        return jsonResponse({
          workflow: 'encrypted-conjunction-assessment',
          mode: 'private-maneuver-ephemeris',
          primary_schema: 'MPE.fbs',
          secondary_schema: 'OMM.fbs',
          encrypted: true,
          grant_id: 'grant-private-mpe',
          channel_id: 'channel-private-ca',
          assessor_peer_id: '16Uiu2HAssessor',
          count: 0,
          events: [],
          provenance: {
            assessor_peer_id: '16Uiu2HAssessor',
            source_schemas: ['MPE.fbs', 'OMM.fbs'],
          },
        });
      }
      throw new Error(`unexpected ${url}`);
    });
    const backend = createRemoteSdnBackend({ serverUrl: 'https://sdn.spaceaware.io', fetch: fetchMock });

    await expect(backend.screenConjunction({
      primarySchema: 'MPE.fbs',
      secondarySchema: 'OMM.fbs',
      encrypted: true,
      grantId: 'grant-private-mpe',
      channelId: 'channel-private-ca',
      assessorPeerId: '16Uiu2HAssessor',
      includeProvenance: true,
      limit: 25,
    })).resolves.toMatchObject({
      ok: true,
      data: {
        workflow: 'encrypted-conjunction-assessment',
        mode: 'private-maneuver-ephemeris',
        encrypted: true,
        grantId: 'grant-private-mpe',
        channelId: 'channel-private-ca',
        assessorPeerId: '16Uiu2HAssessor',
        count: 0,
        events: [],
      },
    });
    expect(calls.map((call) => call.url)).toEqual(['https://sdn.spaceaware.io/api/v1/conjunction/screen']);
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

/** The node's $QRP answers: a COLUMNS + ROWS page, and /data/query runs. */
function qrpTableResponse(columns: Array<{ name: string; type?: string }>, rows: QrpCell[][]) {
  return new Response(encodeQrpTablePage({ columns, rows }) as BodyInit, { status: 200, headers: { 'content-type': FLATBUFFER_STREAM_TYPE } });
}

function qrpQueryResponse(records: Uint8Array[], schemaName: string, lane: { providerId?: string; sourceName?: string; originId?: string } = {}) {
  const frames = records.map((record) => {
    const frame = new Uint8Array(4 + record.byteLength);
    new DataView(frame.buffer).setUint32(0, record.byteLength, true);
    frame.set(record, 4);
    return frame;
  });
  return new Response(encodeQrpRunAnswer([{ schemaName, ...lane, records: frames }]) as BodyInit, { status: 200, headers: { 'content-type': FLATBUFFER_STREAM_TYPE } });
}

function acceptHeader(init?: RequestInit): string {
  const headers = init?.headers;
  if (!headers) return '';
  if (headers instanceof Headers) return headers.get('accept') ?? '';
  if (Array.isArray(headers)) return String(headers.find(([key]) => key.toLowerCase() === 'accept')?.[1] ?? '');
  return String((headers as Record<string, string>).accept ?? (headers as Record<string, string>).Accept ?? '');
}
