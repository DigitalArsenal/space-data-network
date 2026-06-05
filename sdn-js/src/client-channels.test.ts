import { afterEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { SDNClient } from './client';

describe('SDNClient channel API', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('exposes the spec channel operations through client.channels', async () => {
    const stream = new Uint8Array([7, 0, 0, 0, 79, 77, 77, 49, 1, 2, 3]);
    const requests: Array<{ url: string; method: string; accept: string; contentType: string; encryptedStream: string; body: string }> = [];
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      const body = init?.body;
      requests.push({
        url: String(input),
        method: init?.method ?? 'GET',
        accept: String((init?.headers as Record<string, string> | undefined)?.Accept ?? ''),
        contentType: String((init?.headers as Record<string, string> | undefined)?.['Content-Type'] ?? ''),
        encryptedStream: String((init?.headers as Record<string, string> | undefined)?.['X-SDN-Encrypted-Stream'] ?? ''),
        body: body instanceof ArrayBuffer
          ? Array.from(new Uint8Array(body)).join(',')
          : body instanceof Uint8Array ? Array.from(body).join(',') : typeof body === 'string' ? body : '',
      });
      const url = String(input);
      if (url.endsWith('/api/v1/channels?standardCode=OMM')) {
        return jsonResponse({ results: [{ channelId: 'spaceaware-OMM', sourceId: 'spaceaware', standardCode: 'OMM' }] });
      }
      if (url.endsWith('/api/v1/channels/spaceaware-OMM')) {
        return jsonResponse({ channelId: 'spaceaware-OMM', sourceId: 'spaceaware', standardCode: 'OMM', pnmVerified: true });
      }
      if (url.endsWith('/api/v1/channels/spaceaware-OMM/monitor')) {
        return jsonResponse({
          channelId: 'spaceaware-OMM',
          standardCode: 'OMM',
          pinnedCount: 8,
          timingsMs: {
            discovery: 11,
            grantNegotiation: 12,
            pnmDpmVerification: 13,
            transfer: 14,
            decrypt: 15,
            hashVerification: 16,
            durableImport: 17,
          },
        });
      }
      if (url.endsWith('/api/v1/channels/spaceaware-OMM/stream')) {
        return new Response(stream, { headers: { 'content-type': 'application/vnd.sdn.flatbuffers.stream' } });
      }
      return jsonResponse({ ok: true, channelId: 'spaceaware-OMM', standardCode: 'OMM' });
    });

    const client = SDNClient.fromUrl('https://sdn.spaceaware.io');
    await expect(client.channels.list({ standardCode: 'OMM' })).resolves.toEqual([
      expect.objectContaining({ channelId: 'spaceaware-OMM', standardCode: 'OMM' }),
    ]);
    await expect(client.channels.get('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({ pnmVerified: true }));
    await expect(client.channels.subscribe('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({ ok: true }));
    await expect(client.channels.unsubscribe('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({ ok: true }));
    await expect(client.channels.monitor('spaceaware-OMM')).resolves.toEqual(expect.objectContaining({
      pinnedCount: 8,
      timingsMs: expect.objectContaining({
        discovery: 11,
        grantNegotiation: 12,
        pnmDpmVerification: 13,
        transfer: 14,
        decrypt: 15,
        hashVerification: 16,
        durableImport: 17,
      }),
    }));
    await expect(client.channels.openStream('spaceaware-OMM')).resolves.toEqual(stream);
    await expect(client.channels.publish('spaceaware-OMM', stream)).resolves.toEqual(expect.objectContaining({ ok: true }));
    await expect(client.channels.grant('spaceaware-OMM', { to: 'peer-alpha', scopes: ['stream_open'] })).resolves.toEqual(expect.objectContaining({ ok: true }));

    expect(requests.map((request) => request.url)).toEqual([
      'https://sdn.spaceaware.io/api/v1/channels?standardCode=OMM',
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM',
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/subscribe',
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/unsubscribe',
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/monitor',
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/stream',
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/publish',
      'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/grants',
    ]);
    expect(requests[5]).toEqual(expect.objectContaining({
      accept: 'application/vnd.sdn.flatbuffers.stream',
      method: 'GET',
    }));
    expect(requests[6]).toEqual(expect.objectContaining({
      contentType: 'application/vnd.sdn.flatbuffers.stream',
      encryptedStream: '',
      method: 'POST',
      body: Array.from(stream).join(','),
    }));
  });

  it('passes private grant context through channel requests', async () => {
    const requests: Array<{ url: string; encryptedStream: string; encryptedStreamHeader: string }> = [];
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      requests.push({
        url: String(input),
        encryptedStream: String((init?.headers as Record<string, string> | undefined)?.['X-SDN-Encrypted-Stream'] ?? ''),
        encryptedStreamHeader: String((init?.headers as Record<string, string> | undefined)?.['X-SDN-Encrypted-Stream-Header'] ?? ''),
      });
      return jsonResponse({ ok: true });
    });

    const client = SDNClient.fromUrl('https://sdn.spaceaware.io');
    const streamHeader = '{"algorithm":"x25519","context":"spaceaware-OMM","ephemeral_public_key":"pub","nonce_start":"nonce"}';
    const access = { subject: 'peer-alpha', grantId: 'grant-1', visibility: 'private-listed' };
    await client.channels.list({ standardCode: 'OMM', ...access });
    await client.channels.get('spaceaware-OMM', access);
    await client.channels.subscribe('spaceaware-OMM', access);
    await client.channels.openStream('spaceaware-OMM', access);
    await client.channels.publish('spaceaware-OMM', new Uint8Array([1, 2, 3]), {
      ...access,
      encryptedStreamHeader: streamHeader,
    });

    expect(requests).toEqual([
      { url: 'https://sdn.spaceaware.io/api/v1/channels?standardCode=OMM&visibility=private-listed&subject=peer-alpha&grantId=grant-1', encryptedStream: '', encryptedStreamHeader: '' },
      { url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM?subject=peer-alpha&grantId=grant-1&visibility=private-listed', encryptedStream: '', encryptedStreamHeader: '' },
      { url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/subscribe?subject=peer-alpha&grantId=grant-1&visibility=private-listed', encryptedStream: '', encryptedStreamHeader: '' },
      { url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/stream?subject=peer-alpha&grantId=grant-1&visibility=private-listed', encryptedStream: '', encryptedStreamHeader: '' },
      { url: 'https://sdn.spaceaware.io/api/v1/channels/spaceaware-OMM/publish?subject=peer-alpha&grantId=grant-1&visibility=private-listed', encryptedStream: 'true', encryptedStreamHeader: streamHeader },
    ]);
  });

  it('keeps public SDNClient examples on channel standardCode naming', () => {
    const source = readFileSync(new URL('./client.ts', import.meta.url), 'utf8');

    expect(source).toContain('client.channels.list({ standardCode:');
    expect(source).not.toContain('OMM.fbs');
  });

  it('keeps the public quick start on channel standardCode naming', () => {
    const readme = readFileSync(new URL('../README.md', import.meta.url), 'utf8');
    const quickStart = readme.slice(readme.indexOf('## Quick Start'), readme.indexOf('## Features'));

    expect(quickStart).toContain("channels.list({ standardCode: 'OMM' })");
    expect(quickStart).toContain("channels.publish('spaceaware-OMM'");
    expect(quickStart).not.toContain('OMM.fbs');
  });
});

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}
