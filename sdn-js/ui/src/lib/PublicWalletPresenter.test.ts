import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { render } from 'svelte/server';
import { describe, expect, it, vi } from 'vitest';

function fakeClient(status: 'connected' | 'dormant') {
  return {
    connect: vi.fn(async () => undefined as never),
    destroy: vi.fn(async () => undefined),
    disconnect: vi.fn(async () => undefined),
    getSnapshot: vi.fn(() => ({ identity: null, status })),
    openAccount: vi.fn(async () => undefined),
    subscribe: vi.fn(() => vi.fn()),
  };
}

describe('PublicWalletPresenter', () => {
  it.each([
    ['dormant', 'Login'],
    ['connected', 'Account'],
  ] as const)('renders %s state as %s', async (status, expectedLabel) => {
    const componentPath = fileURLToPath(new URL('./PublicWalletPresenter.svelte', import.meta.url));
    expect(existsSync(componentPath), 'missing lib/PublicWalletPresenter.svelte').toBe(true);
    const { default: PublicWalletPresenter } = await import('./PublicWalletPresenter.svelte');

    const { body } = render(PublicWalletPresenter, {
      props: { client: fakeClient(status) },
    });

    expect(body).toContain(`>${expectedLabel}</button>`);
  });

  it('subscribes and unsubscribes without owning client destruction', () => {
    const componentPath = fileURLToPath(new URL('./PublicWalletPresenter.svelte', import.meta.url));
    expect(existsSync(componentPath), 'missing lib/PublicWalletPresenter.svelte').toBe(true);
    const source = readFileSync(componentPath, 'utf8');

    expect(source).toContain('client.subscribe');
    expect(source).toContain('untrack(() => client.getSnapshot())');
    expect(source).toContain('unsubscribe();');
    expect(source).not.toContain('client.destroy()');
    expect(source).not.toContain('client.disconnect()');
  });
});
