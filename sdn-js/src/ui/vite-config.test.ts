import { afterEach, describe, expect, it, vi } from 'vitest';

describe('admin vite config', () => {
  afterEach(() => {
    delete process.env.SDN_UI_PROXY_TARGET;
    vi.resetModules();
  });

  it('proxies the IPFS dashboard paths to the backend in dev', async () => {
    process.env.SDN_UI_PROXY_TARGET = 'http://127.0.0.1:5010';
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const server = typeof config.server === 'function' ? config.server({} as never) : config.server;
    const proxy = server?.proxy;

    expect(proxy).toBeDefined();
    expect(proxy).toHaveProperty('/webui');
    expect(proxy).toHaveProperty('/ipfs');
  });
});
