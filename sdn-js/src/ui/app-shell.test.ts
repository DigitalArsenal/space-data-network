import { describe, expect, it, vi } from 'vitest';

import { renderAppShell } from '../../ui/src/app';

describe('renderAppShell', () => {
  it('renders the admin shell workspaces and top-level controls without eagerly mounting the wallet UI', async () => {
    const root = new FakeAppShellRoot();
    const mountWalletUI = vi.fn(async () => undefined);

    await renderAppShell(root, { mountWalletUI });

    expect(root.innerHTML).toContain('Network');
    expect(root.innerHTML).toContain('Directory');
    expect(root.innerHTML).toContain('Store');
    expect(root.innerHTML).toContain('Frontend');
    expect(root.innerHTML).toContain('Wallet');
    expect(root.innerHTML).toContain('Delivery');
    expect(root.innerHTML).toContain('Observed SDN peers');
    expect(root.innerHTML).toContain('id="sdn-mode-switch"');
    expect(root.innerHTML).toContain('id="sdn-connect-server"');
    expect(root.innerHTML).toContain('id="sdn-account-button"');
    expect(root.innerHTML).toContain('id="sdn-account-dialog"');
    expect(root.innerHTML).toContain('data-nav="ipfs-dashboard"');
    expect(root.innerHTML).toContain('id="sdn-wallet-panel"');
    expect(root.innerHTML).toContain('id="sdn-provider-url"');
    expect(root.innerHTML).toContain('id="sdn-marketplace-select"');
    expect(root.innerHTML).toContain('id="sdn-run-live-flow"');
    expect(root.innerHTML).toContain('id="sdn-address-lookup-value"');
    expect(root.innerHTML).toContain('id="sdn-wallet-load"');
    expect(root.innerHTML).toContain('id="sdn-frontend-tree"');
    expect(root.innerHTML).toContain('id="sdn-frontend-editor"');
    expect(root.innerHTML).toContain('id="sdn-frontend-upload"');
    expect(root.innerHTML).toContain('id="sdn-frontend-save"');
    expect(mountWalletUI).not.toHaveBeenCalled();
  });

  it('mounts the wallet UI only after the user explicitly requests it', async () => {
    const root = new FakeAppShellRoot();
    const mountWalletUI = vi.fn(async () => undefined);

    await renderAppShell(root, { mountWalletUI });
    await root.walletLoadButton.click();

    expect(mountWalletUI).toHaveBeenCalledWith(root.walletPanel);
  });
});

class FakeAppShellRoot {
  innerHTML = '';
  readonly walletPanel = { innerHTML: '' } as HTMLElement;
  readonly walletLoadButton = new FakeButtonElement();
  readonly accountButton = new FakeButtonElement();

  querySelector(selector: string): HTMLElement | null {
    if (selector === '#sdn-wallet-panel') {
      return this.walletPanel;
    }
    if (selector === '#sdn-wallet-load') {
      return this.walletLoadButton as unknown as HTMLElement;
    }
    if (selector === '#sdn-account-button') {
      return this.accountButton as unknown as HTMLElement;
    }
    return null;
  }
}

class FakeButtonElement {
  private onClick: (() => void | Promise<void>) | null = null;

  addEventListener(eventName: string, listener: () => void | Promise<void>): void {
    if (eventName === 'click') {
      this.onClick = listener;
    }
  }

  async click(): Promise<void> {
    await this.onClick?.();
  }
}
