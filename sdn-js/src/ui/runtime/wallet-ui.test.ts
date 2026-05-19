import { describe, expect, it, vi } from 'vitest';

const { createWalletUI, styleModuleLoaded } = vi.hoisted(() => ({
  createWalletUI: vi.fn(async () => ({
    openLogin: vi.fn(),
    openAccount: vi.fn(),
    destroy: vi.fn(),
  })),
  styleModuleLoaded: { current: false },
}));

vi.mock('hd-wallet-ui', () => ({
  createWalletUI,
}));

vi.mock('hd-wallet-ui/styles', () => {
  styleModuleLoaded.current = true;
  return {};
});

import { mountWalletUI } from './wallet-ui';

describe('mountWalletUI', () => {
  it('loads the hd-wallet-ui widget styles for hosted modal rendering', () => {
    expect(styleModuleLoaded.current).toBe(true);
  });

  it('mounts the hd-wallet-ui modal without an SDN wrapper menu', async () => {
    const host = new FakeWalletHost();

    await mountWalletUI(host as unknown as HTMLElement);

    expect(createWalletUI).toHaveBeenCalledWith(host, expect.any(Object));
    expect(host.replaceChildren).toHaveBeenCalledTimes(1);
    expect(host.innerHTML).toBe('');
  });
});

class FakeWalletHost {
  innerHTML = '';
  readonly replaceChildren = vi.fn(() => {
    this.innerHTML = '';
  });
}
