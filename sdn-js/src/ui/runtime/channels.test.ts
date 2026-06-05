import { describe, expect, it } from 'vitest';
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
          providerPeer: '12D3KooProvider',
          localRows: 10,
          remoteRows: 12,
          syncedRows: 10,
          missingRows: 2,
          pinnedRows: 8,
          syncedBytes: 4096,
          throughputBytesPerSecond: 2048,
          wireSpeedUtilization: 0.91,
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
        providerPeer: '12D3KooProvider',
        localRows: 10,
        remoteRows: 12,
        syncedRows: 10,
        missingRows: 2,
        pinnedRows: 8,
        syncedBytes: 4096,
        throughputBytesPerSecond: 2048,
        wireSpeedUtilization: 0.91,
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

  it('passes private grant context through protected channel actions', async () => {
    const requests: Array<{ url: string; method: string; contentType: string; bodyText: string }> = [];
    const fetchMock = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const body = init?.body;
      requests.push({
        url: String(input),
        method: init?.method ?? 'GET',
        contentType: String(init?.headers && (init.headers as Record<string, string>)['content-type']),
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
    const grant = { subject: 'peer-alpha', grantId: 'grant-1', visibility: 'private' };
    const stream = new Uint8Array([7, 0, 0, 0, 79, 77, 77, 49, 1, 2, 3]);

    await backend.channels.subscribe('spaceaware-OMM', grant);
    await backend.channels.publish('spaceaware-OMM', stream, grant);
    await backend.channels.openStream('spaceaware-OMM', grant);
    await backend.channels.issueGrant('spaceaware-OMM', { to: 'peer-alpha', scopes: ['stream_open'] }, grant);

    expect(requests).toEqual([
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/subscribe?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'POST',
        contentType: 'undefined',
        bodyText: '',
      },
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/publish?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'POST',
        contentType: 'application/vnd.sdn.flatbuffers.stream',
        bodyText: Array.from(stream).join(','),
      },
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/stream?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'GET',
        contentType: 'undefined',
        bodyText: '',
      },
      {
        url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/grants?subject=peer-alpha&grantId=grant-1&visibility=private',
        method: 'POST',
        contentType: 'application/json',
        bodyText: JSON.stringify({ to: 'peer-alpha', scopes: ['stream_open'] }),
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
