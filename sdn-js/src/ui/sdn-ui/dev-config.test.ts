import { afterEach, describe, expect, it, vi } from 'vitest';

describe('SDN UI Vite development configuration', () => {
  afterEach(() => {
    delete process.env.SDN_UI_PROXY_TARGET;
    delete process.env.SDN_UI_KUBO_PROXY_TARGET;
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

  it('proxies Kubo through /kubo without forwarding the browser Origin header', async () => {
    process.env.SDN_UI_KUBO_PROXY_TARGET = 'http://127.0.0.1:5001';
    vi.resetModules();
    const { default: config } = await import('../../../ui/vite.config.mts');
    const server = typeof config.server === 'function'
      ? config.server({} as never)
      : config.server;
    const kuboProxy = server?.proxy?.['/kubo'];
    expect(kuboProxy).toEqual(expect.objectContaining({
      target: 'http://127.0.0.1:5001',
      rewrite: expect.any(Function),
      configure: expect.any(Function),
    }));

    const events = new Map<string, Function>();
    kuboProxy.configure({
      on(event: string, handler: Function) {
        events.set(event, handler);
      },
    });

    const removeHeader = vi.fn();
    events.get('proxyReq')?.({ removeHeader });
    expect(removeHeader).toHaveBeenCalledWith('origin');
    expect(removeHeader).toHaveBeenCalledWith('referer');
    expect(removeHeader).toHaveBeenCalledWith('user-agent');
    expect(kuboProxy.rewrite('/kubo/api/v0/id')).toBe('/api/v0/id');
  });
});
