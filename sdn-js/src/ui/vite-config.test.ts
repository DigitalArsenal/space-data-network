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

  it('injects a browser process shim for upstream webui dependencies', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const build = typeof config.build === 'function' ? config.build({} as never) : config.build;
    const output = build?.rollupOptions?.output;
    const banner = Array.isArray(output)
      ? output[0]?.banner
      : output?.banner;

    expect(typeof banner).toBe('string');
    expect(String(banner)).toContain('globalThis.process');
    expect(String(banner)).toContain('var process = globalThis.process');
    expect(String(banner)).toContain('cwd: () => "/"');
    expect(String(banner)).toContain('browser: true');
  });

  it('pins copied upstream bootstrap dependencies to the upstream webui package tree', async () => {
    vi.resetModules();

    const { default: config } = await import('../../ui/vite.config.mts');
    const resolve = typeof config.resolve === 'function' ? config.resolve({} as never) : config.resolve;
    const alias = Array.isArray(resolve?.alias) ? resolve.alias : [];

    const reactAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^react$/');
    const bundlerAlias = alias.find((entry) => entry && typeof entry === 'object' && 'find' in entry && String(entry.find) === '/^redux-bundler-react$/');

    expect(String(reactAlias?.replacement)).toContain('/webui/node_modules/react');
    expect(String(bundlerAlias?.replacement)).toContain('/webui/node_modules/redux-bundler-react');
  });
});
