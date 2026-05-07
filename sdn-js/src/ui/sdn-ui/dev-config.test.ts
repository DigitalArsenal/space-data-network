import { afterEach, describe, expect, it, vi } from 'vitest';

describe('SDN UI Vite development configuration', () => {
  afterEach(() => {
    delete process.env.SDN_UI_PROXY_TARGET;
    vi.resetModules();
  });

  it('only proxies local desktop paths when SDN_UI_PROXY_TARGET is configured', async () => {
    vi.resetModules();
    const withoutProxy = await import('../../../ui/vite.config.mts');
    const withoutProxyServer = typeof withoutProxy.default.server === 'function'
      ? withoutProxy.default.server({} as never)
      : withoutProxy.default.server;
    expect(withoutProxyServer?.proxy).toBeUndefined();

    process.env.SDN_UI_PROXY_TARGET = 'http://127.0.0.1:17890';
    vi.resetModules();
    const withProxy = await import('../../../ui/vite.config.mts');
    const withProxyServer = typeof withProxy.default.server === 'function'
      ? withProxy.default.server({} as never)
      : withProxy.default.server;

    expect(withProxyServer?.proxy).toEqual(expect.objectContaining({
      '/api': expect.objectContaining({ target: 'http://127.0.0.1:17890' }),
      '/ipfs': expect.objectContaining({ target: 'http://127.0.0.1:17890' }),
      '/webui': expect.objectContaining({ target: 'http://127.0.0.1:17890' }),
    }));
  });

  it('exposes SDN_UI_* variables to the Svelte app runtime', async () => {
    vi.resetModules();
    const { default: config } = await import('../../../ui/vite.config.mts');

    expect(config.envPrefix).toEqual(expect.arrayContaining(['VITE_', 'SDN_UI_']));
  });
});
