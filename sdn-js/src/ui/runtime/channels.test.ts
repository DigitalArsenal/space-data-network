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
      data: [expect.objectContaining({ channelId: 'spaceaware-OMM', standardCode: 'OMM' })],
    }));
    await expect(backend.channels.get('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({
      ok: true,
      data: expect.objectContaining({ channelId: 'spaceaware-OMM', pnmVerified: true }),
    }));
    await expect(backend.channels.monitor('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({
      ok: true,
      data: expect.objectContaining({
        channelHead: 'bafyhead',
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
});

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}
