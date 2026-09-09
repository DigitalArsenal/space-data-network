import { afterEach, describe, expect, it, vi } from 'vitest';
import { SDNClient } from './client';

afterEach(() => vi.unstubAllGlobals());

describe('SDNClient transport credentials', () => {
  it('exposes cookie omission through the public client factory', async () => {
    const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('{}'));
    vi.stubGlobal('fetch', fetch);
    await SDNClient.fromUrl('https://provider.example', { credentials: 'omit' })
      .publish('CZM', new Uint8Array([1]));
    expect((fetch.mock.calls[0]?.[1] as RequestInit).credentials).toBe('omit');
  });

  it('keeps the default same-origin session transport', async () => {
    const fetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('{}'));
    vi.stubGlobal('fetch', fetch);
    await SDNClient.fromUrl('https://provider.example').publish('CZM', new Uint8Array([1]));
    expect((fetch.mock.calls[0]?.[1] as RequestInit).credentials).toBe('include');
  });
});
