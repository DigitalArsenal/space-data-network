import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';
import {
  FSM,
  FSMT,
  FieldStreamValueT,
  fieldStreamValueEncodingCategory,
  fieldStreamValueStateCategory,
} from 'spacedatastandards.org/lib/js/FSM/main.js';
import { createRemoteSdnBackend } from './sdn-backend-remote';

describe('SDN backend channel runtime surface', () => {
  it('lists, shows, and monitors channels through the remote API', async () => {
    const requested: string[] = [];
    const fetchMock = async (input: RequestInfo | URL): Promise<Response> => {
      const url = String(input);
      requested.push(url);
      if (url.endsWith('/api/v1/channels?standardCode=OMM')) {
        return jsonResponse({
          results: [{
            channelId: 'spaceaware-OMM',
            sourceId: 'spaceaware',
            standardCode: 'OMM',
            visibility: 'public',
            subscribed: true,
            grantState: 'verified',
          }],
        });
      }
      if (url.endsWith('/api/v1/channels/spaceaware-OMM/monitor')) {
        return jsonResponse({
          channelId: 'spaceaware-OMM',
          sourceId: 'spaceaware',
          standardCode: 'OMM',
            channelHead: 'bafyhead',
            subscribed: true,
            pnmVerified: true,
            dpmVerified: true,
            providerPeer: '12D3KooProvider',
          localRows: 10,
          remoteRows: 12,
          syncedRows: 10,
          missingRows: 2,
          pinnedCount: 8,
          pinnedRows: 8,
          syncedBytes: 4096,
          throughputBytesPerSecond: 2048,
          wireSpeedUtilization: 0.91,
          timingsMs: {
            discovery: 11,
            grantNegotiation: 12,
            pnmDpmVerification: 13,
            transfer: 14,
            decrypt: 15,
            hashVerification: 16,
            durableImport: 17,
          },
          grantState: 'verified',
          encryptionState: 'public',
          lastVerifiedUpdate: '2026-06-04T00:00:00Z',
        });
      }
      if (url.endsWith('/api/v1/channels/spaceaware-OMM')) {
        return jsonResponse({
          channelId: 'spaceaware-OMM',
          sourceId: 'spaceaware',
          standardCode: 'OMM',
          visibility: 'public',
          subscribed: true,
          pnmVerified: true,
          dpmVerified: true,
          grantState: 'verified',
          encryptionState: 'public',
        });
      }
      return jsonResponse({ error: `unexpected ${url}` }, 404);
    };

    const backend = createRemoteSdnBackend({
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: fetchMock,
    });

    await expect(backend.channels.list({ standardCode: 'OMM' })).resolves.toEqual(expect.objectContaining({
      ok: true,
      data: [expect.objectContaining({ channelId: 'spaceaware-OMM', standardCode: 'OMM', subscribed: true })],
    }));
    await expect(backend.channels.get('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({
      ok: true,
      data: expect.objectContaining({ channelId: 'spaceaware-OMM', pnmVerified: true, subscribed: true }),
    }));
    await expect(backend.channels.monitor('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({
      ok: true,
      data: expect.objectContaining({
        channelHead: 'bafyhead',
        subscribed: true,
        pnmVerified: true,
        dpmVerified: true,
        providerPeer: '12D3KooProvider',
        localRows: 10,
        remoteRows: 12,
        syncedRows: 10,
        missingRows: 2,
        pinnedCount: 8,
        pinnedRows: 8,
        syncedBytes: 4096,
        throughputBytesPerSecond: 2048,
        wireSpeedUtilization: 0.91,
        timingsMs: expect.objectContaining({
          discovery: 11,
          grantNegotiation: 12,
          pnmDpmVerification: 13,
          transfer: 14,
          decrypt: 15,
          hashVerification: 16,
          durableImport: 17,
        }),
        grantState: 'verified',
        encryptionState: 'public',
        lastVerifiedUpdate: '2026-06-04T00:00:00Z',
      }),
    }));
    expect(requested).toEqual([
      'https://sdn.spaceaware.io/api/v1/channels?standardCode=OMM',
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM',
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/monitor',
    ]);
  });

  it('fails closed when a private channel subscription lacks a verified grant', async () => {
    const backend = createRemoteSdnBackend({
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: async () => jsonResponse({ error: 'verified channel grant required' }, 403),
    });

    await expect(backend.channels.subscribe('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({
      ok: false,
      capability: expect.objectContaining({
        state: 'degraded',
        reason: expect.stringContaining('HTTP 403'),
      }),
    }));
  });

  it('passes private grant context through channel list requests', async () => {
    const requested: string[] = [];
    const fetchMock = async (input: RequestInfo | URL): Promise<Response> => {
      const url = String(input);
      requested.push(url);
      if (url.endsWith('/api/v1/channels?standardCode=OMM&visibility=private-listed&subject=peer-alpha&grantId=grant-1')) {
        return jsonResponse({
          results: [{
            channelId: 'spaceaware-OMM',
            sourceId: 'spaceaware',
            standardCode: 'OMM',
            visibility: 'private-listed',
            grantState: 'verified',
            encryptionState: 'encrypted',
            pnmVerified: true,
            dpmVerified: true,
          }],
        });
      }
      return jsonResponse({ error: `unexpected ${url}` }, 404);
    };

    const backend = createRemoteSdnBackend({
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: fetchMock,
    });

    await expect(backend.channels.list({
      standardCode: 'OMM',
      visibility: 'private-listed',
      subject: 'peer-alpha',
      grantId: 'grant-1',
    })).resolves.toEqual(expect.objectContaining({
      ok: true,
      data: [expect.objectContaining({
        channelId: 'spaceaware-OMM',
        visibility: 'private-listed',
        grantState: 'verified',
        encryptionState: 'encrypted',
        pnmVerified: true,
        dpmVerified: true,
      })],
    }));
    expect(requested).toEqual([
      'https://sdn.spaceaware.io/api/v1/channels?standardCode=OMM&visibility=private-listed&subject=peer-alpha&grantId=grant-1',
    ]);
  });

  it('passes private grant context through channel monitor requests', async () => {
    const requested: string[] = [];
    const fetchMock = async (input: RequestInfo | URL): Promise<Response> => {
      const url = String(input);
      requested.push(url);
      if (url.endsWith('/api/v1/channels/spaceaware-OMM/monitor?subject=peer-alpha&grantId=grant-1&visibility=private-listed')) {
        return jsonResponse({
          channelId: 'spaceaware-OMM',
          sourceId: 'spaceaware',
          standardCode: 'OMM',
          visibility: 'private-listed',
          grantState: 'verified',
          encryptionState: 'encrypted',
          pnmVerified: true,
          dpmVerified: true,
        });
      }
      return jsonResponse({ error: `unexpected ${url}` }, 404);
    };

    const backend = createRemoteSdnBackend({
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: fetchMock,
    });

    await expect(backend.channels.monitor('spaceaware-OMM', {
      subject: 'peer-alpha',
      grantId: 'grant-1',
      visibility: 'private-listed',
    })).resolves.toEqual(expect.objectContaining({
      ok: true,
      data: expect.objectContaining({
        channelId: 'spaceaware-OMM',
        visibility: 'private-listed',
        grantState: 'verified',
        encryptionState: 'encrypted',
        pnmVerified: true,
        dpmVerified: true,
      }),
    }));
    expect(requested).toEqual([
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/monitor?subject=peer-alpha&grantId=grant-1&visibility=private-listed',
    ]);
  });

  it('opens channel streams as native FlatBuffer bytes without JSON wrapping', async () => {
    const streamBytes = new Uint8Array([9, 0, 0, 0, 79, 77, 77, 49, 1, 2, 3, 4, 5]);
    const requests: Array<{ url: string; accept: string }> = [];
    const fetchMock = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url = String(input);
      requests.push({ url, accept: String(init?.headers && (init.headers as Record<string, string>).accept) });
      if (url.endsWith('/api/v1/channels/spaceaware-OMM/stream')) {
        return new Response(streamBytes, {
          status: 200,
          headers: { 'content-type': 'application/vnd.sdn.flatbuffers.stream' },
        });
      }
      return jsonResponse({ error: `unexpected ${url}` }, 404);
    };

    const backend = createRemoteSdnBackend({
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: fetchMock,
    });

    await expect(backend.channels.openStream('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({
      ok: true,
      data: streamBytes,
    }));
    expect(requests).toEqual([{
      url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/stream',
      accept: 'application/vnd.sdn.flatbuffers.stream',
    }]);
  });

  it('opens field stream messages as metadata-only field visibility rows', async () => {
    const messageBytes = encodeFieldStreamMessageFixture();
    const requests: Array<{ url: string; accept: string }> = [];
    const fetchMock = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url = String(input);
      requests.push({ url, accept: String(init?.headers && (init.headers as Record<string, string>).accept) });
      if (url.endsWith('/api/v1/channels/spaceaware-MPE/stream?subject=customer-alpha&grantId=grant-alpha&visibility=private-listed')) {
        return new Response(messageBytes, {
          status: 200,
          headers: { 'content-type': 'application/vnd.sdn.field-stream-message' },
        });
      }
      return jsonResponse({ error: `unexpected ${url}` }, 404);
    };

    const backend = createRemoteSdnBackend({
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: fetchMock,
    });

    const result = await (backend.channels as any).openFieldStream('spaceaware-MPE', {
      subject: 'customer-alpha',
      grantId: 'grant-alpha',
      visibility: 'private-listed',
    });

    expect(requests).toEqual([{
      url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-MPE/stream?subject=customer-alpha&grantId=grant-alpha&visibility=private-listed',
      accept: 'application/vnd.sdn.field-stream-message, application/vnd.sdn.flatbuffers.stream',
    }]);
    expect(result.ok).toBe(true);
    expect(result.data).toMatchObject({
      messageId: 'fsm-mpe-alpha-000001',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 3,
      keyEpoch: 'epoch-7',
      sequence: '1',
      subjectId: 'customer-alpha-peer',
    });
    expect(result.data?.fields.map((field: { fieldPath: string; state: string }) => [field.fieldPath, field.state])).toEqual([
      ['object_id', 'Public'],
      ['position', 'Encrypted'],
      ['maneuver_plan', 'Redacted'],
      ['covariance_detail', 'Unavailable'],
    ]);
    expect(result.data?.fields[1]).toMatchObject({
      fieldPath: 'position',
      encoding: 'FlatBuffer',
      keyId: 'field-key:alpha:position:epoch-7',
      ciphertextLength: 4,
      valueLength: 0,
      releaseTags: ['restricted', 'customer-alpha'],
      decision: 'allow-encrypted',
    });
    const payload = JSON.stringify(result.data);
    expect(payload).not.toContain('provider_signature');
    expect(payload).not.toContain('providerSignature');
    expect(payload).not.toContain('"ciphertext":');
    expect(payload).not.toContain('"nonce":');
    expect(payload).not.toContain('"tag":');
    expect(payload).not.toContain('"aadHash":');
  });

  it('passes private grant context through protected channel actions', async () => {
    const requests: Array<{ url: string; method: string; contentType: string; encryptedStream: string; encryptedStreamHeader: string; encryptedRecordIndex: string; bodyText: string }> = [];
    const fetchMock = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const body = init?.body;
      requests.push({
        url: String(input),
        method: init?.method ?? 'GET',
        contentType: String(init?.headers && (init.headers as Record<string, string>)['content-type']),
        encryptedStream: String(init?.headers && (init.headers as Record<string, string>)['X-SDN-Encrypted-Stream']),
        encryptedStreamHeader: String(init?.headers && (init.headers as Record<string, string>)['X-SDN-Encrypted-Stream-Header']),
        encryptedRecordIndex: String(init?.headers && (init.headers as Record<string, string>)['X-SDN-Encrypted-Record-Index']),
        bodyText: typeof body === 'string' ? body : body instanceof Uint8Array ? Array.from(body).join(',') : '',
      });
      if (String(input).includes('/stream')) {
        return new Response(new Uint8Array([1, 2, 3]), {
          status: 200,
          headers: { 'content-type': 'application/vnd.sdn.flatbuffers.stream' },
        });
      }
      return jsonResponse({ grantState: 'verified' });
    };

    const backend = createRemoteSdnBackend({
      serverUrl: 'https://sdn.spaceaware.io',
      fetch: fetchMock,
    });
    const encryptedStreamHeader = '{"algorithm":"x25519","context":"spaceaware-OMM","ephemeral_public_key":"pub","nonce_start":"nonce"}';
    const grant = { subject: 'peer-alpha', grantId: 'grant-1', visibility: 'private', encryptedStreamHeader, encryptedRecordIndex: 7 };
    const stream = new Uint8Array([7, 0, 0, 0, 79, 77, 77, 49, 1, 2, 3]);

    await backend.channels.get('spaceaware-OMM', grant);
    await backend.channels.subscribe('spaceaware-OMM', grant);
    await backend.channels.publish('spaceaware-OMM', stream, grant);
    await backend.channels.openStream('spaceaware-OMM', grant);
    await backend.channels.issueGrant('spaceaware-OMM', { to: 'peer-alpha', scopes: ['stream_open'] }, grant);
    await backend.channels.keyUnwrap('spaceaware-OMM', {
      recipientKeyId: 'peer-alpha-x25519',
      contentKeyId: 'channel-private-key',
    }, grant);

    expect(requests).toEqual([
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'GET',
        contentType: 'undefined',
        encryptedStream: 'undefined',
        encryptedStreamHeader: 'undefined',
        encryptedRecordIndex: 'undefined',
        bodyText: '',
      },
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/subscribe?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'POST',
        contentType: 'undefined',
        encryptedStream: 'undefined',
        encryptedStreamHeader: 'undefined',
        encryptedRecordIndex: 'undefined',
        bodyText: '',
      },
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/publish?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'POST',
        contentType: 'application/vnd.sdn.flatbuffers.stream',
        encryptedStream: 'true',
        encryptedStreamHeader,
        encryptedRecordIndex: '7',
        bodyText: Array.from(stream).join(','),
      },
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/stream?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'GET',
        contentType: 'undefined',
        encryptedStream: 'undefined',
        encryptedStreamHeader: 'undefined',
        encryptedRecordIndex: 'undefined',
        bodyText: '',
      },
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/grants?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'POST',
        contentType: 'application/json',
        encryptedStream: 'undefined',
        encryptedStreamHeader: 'undefined',
        encryptedRecordIndex: 'undefined',
        bodyText: JSON.stringify({ to: 'peer-alpha', scopes: ['stream_open'] }),
      },
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/key-unwrap?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'POST',
        contentType: 'application/json',
        encryptedStream: 'undefined',
        encryptedStreamHeader: 'undefined',
        encryptedRecordIndex: 'undefined',
        bodyText: JSON.stringify({
          recipientKeyId: 'peer-alpha-x25519',
          contentKeyId: 'channel-private-key',
        }),
      },
    ]);
  });
});

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function encodeFieldStreamMessageFixture(): Uint8Array {
  const message = new FSMT(
    'fsm-mpe-alpha-000001',
    'provider-peer',
    'listing-maneuver-ephemeris',
    'maneuver-ephemeris-live',
    'MPE',
    Array.from(new Uint8Array(32).fill(0x61)),
    'policy-mpe-alpha',
    3,
    'epoch-7',
    1n,
    1_800_000_100_000n,
    1_800_000_160_000n,
    'customer-alpha-peer',
    [
      new FieldStreamValueT(
        'object_id',
        [1],
        fieldStreamValueStateCategory.Public,
        fieldStreamValueEncodingCategory.TextUtf8,
        Array.from(new TextEncoder().encode('SAT-042')),
        [],
        [],
        [],
        null,
        [],
        ['releasable'],
        'allow-public',
      ),
      new FieldStreamValueT(
        'position',
        [3],
        fieldStreamValueStateCategory.Encrypted,
        fieldStreamValueEncodingCategory.FlatBuffer,
        [],
        [0xde, 0xad, 0xbe, 0xef],
        Array.from(new Uint8Array(12).fill(0x21)),
        Array.from(new Uint8Array(16).fill(0x22)),
        'field-key:alpha:position:epoch-7',
        Array.from(new Uint8Array(32).fill(0x23)),
        ['restricted', 'customer-alpha'],
        'allow-encrypted',
      ),
      new FieldStreamValueT(
        'maneuver_plan',
        [7],
        fieldStreamValueStateCategory.Redacted,
        fieldStreamValueEncodingCategory.JsonUtf8,
        [],
        [],
        [],
        [],
        null,
        [],
        ['maneuver', 'not-granted'],
        'redacted:not-granted',
      ),
      new FieldStreamValueT(
        'covariance_detail',
        [8],
        fieldStreamValueStateCategory.Unavailable,
        fieldStreamValueEncodingCategory.JsonUtf8,
        [],
        [],
        [],
        [],
        null,
        [],
        ['covariance'],
        'unavailable:not-published',
      ),
    ],
    Array.from(new Uint8Array(32).fill(0x31)),
    Array.from(new Uint8Array(32).fill(0x30)),
    Array.from(new Uint8Array(64).fill(0xa1)),
  );
  const builder = new flatbuffers.Builder(1024);
  const root = message.pack(builder);
  FSM.finishFSMBuffer(builder, root);
  return builder.asUint8Array();
}
