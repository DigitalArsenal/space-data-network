import { describe, expect, it, vi } from 'vitest';

const { createWalletUI } = vi.hoisted(() => ({
  createWalletUI: vi.fn(async () => ({
    openLogin: vi.fn(),
    openAccount: vi.fn(),
    destroy: vi.fn(),
  })),
}));

vi.mock('hd-wallet-ui', () => ({
  createWalletUI,
}));

import { mountWalletUI } from './wallet-ui';

describe('mountWalletUI', () => {
  it('wires the embedded hd-wallet-ui controls into the identity host', async () => {
    const host = new FakeWalletHost();

    await mountWalletUI(host as unknown as HTMLElement);

    expect(createWalletUI).toHaveBeenCalledWith(host, expect.any(Object));
    expect(host.innerHTML).toContain('hd-wallet-ui');

    host.loginButton.click();
    host.accountButton.click();

    const mountedWallet = await createWalletUI.mock.results[0]?.value;
    expect(mountedWallet.openLogin).toHaveBeenCalledTimes(1);
    expect(mountedWallet.openAccount).toHaveBeenCalledTimes(1);
  });
});

class FakeWalletHost {
  innerHTML = '';
  readonly loginButton = new FakeButton();
  readonly accountButton = new FakeButton();
  readonly status = new FakeNode();

  querySelector(selector: string): FakeButton | FakeNode | null {
    switch (selector) {
      case '[data-wallet-action="login"]':
        return this.loginButton;
      case '[data-wallet-action="account"]':
        return this.accountButton;
      case '[data-wallet-status]':
        return this.status;
      default:
        return null;
    }
  }
}

class FakeButton {
  #handlers = new Map<string, Array<() => void>>();

  addEventListener(eventName: string, handler: () => void): void {
    const handlers = this.#handlers.get(eventName) ?? [];
    handlers.push(handler);
    this.#handlers.set(eventName, handlers);
  }

  click(): void {
    for (const handler of this.#handlers.get('click') ?? []) {
      handler();
    }
  }
}

class FakeNode {
  textContent = '';
}
