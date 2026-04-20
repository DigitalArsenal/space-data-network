import { describe, expect, it } from 'vitest';

import { renderAppShell } from '../../ui/src/app';

describe('renderAppShell', () => {
  it('renders the admin shell workspaces and top-level controls without a custom account dialog', async () => {
    const root = new FakeAppShellRoot();

    await renderAppShell(root);

    expect(root.innerHTML).toContain('Network');
    expect(root.innerHTML).toContain('Directory');
    expect(root.innerHTML).toContain('Store');
    expect(root.innerHTML).toContain('Pinning');
    expect(root.innerHTML).toContain('Frontend');
    expect(root.innerHTML).toContain('Wallet');
    expect(root.innerHTML).toContain('Observed SDN peers');
    expect(root.innerHTML).toContain('A peer-to-peer control plane for space data, software modules, and signed identity.');
    expect(root.innerHTML).toContain('id="sdn-feature-carousel"');
    expect(root.innerHTML).toContain('Marketplace Search');
    expect(root.innerHTML).toContain('Active Directory + Trust');
    expect(root.innerHTML).toContain('Browser Workspace');
    expect(root.innerHTML).toContain('Wallet + Identity');
    expect(root.innerHTML).toContain('Pinning Rules');
    expect(root.innerHTML).toContain('class="sdn-feature-carousel__viewport"');
    expect(root.innerHTML).toContain('class="sdn-feature-carousel__arrow sdn-feature-carousel__arrow--prev"');
    expect(root.innerHTML).toContain('class="sdn-feature-carousel__arrow sdn-feature-carousel__arrow--next"');
    expect(root.innerHTML).toContain('class="sdn-feature-carousel__indicators"');
    expect(root.innerHTML).toContain('data-feature-prev');
    expect(root.innerHTML).toContain('data-feature-next');
    expect(root.innerHTML).toMatch(/class="sdn-feature-carousel__indicator sdn-feature-carousel__indicator--active"[^>]*><\/button>/);
    expect(root.innerHTML).toMatch(/class="sdn-feature-carousel__indicator"[^>]*aria-label="Go to feature 2"[^>]*><\/button>/);
    expect(root.innerHTML).toContain('data-workspace-link="store"');
    expect(root.innerHTML).toContain('data-workspace-link="directory"');
    expect(root.innerHTML).toContain('data-workspace-link="pinning"');
    expect(root.innerHTML).toContain('data-workspace-link="frontend"');
    expect(root.innerHTML).toContain('data-workspace-link="wallet"');
    expect(root.innerHTML).toContain('id="sdn-mode-switch"');
    expect(root.innerHTML).toContain('id="sdn-connect-server"');
    expect(root.innerHTML).toContain('id="sdn-account-button"');
    expect(root.innerHTML).not.toContain('id="sdn-account-dialog"');
    expect(root.innerHTML).toContain('data-nav="ipfs-dashboard"');
    expect(root.innerHTML).toContain('data-nav="wallet"');
    expect(root.innerHTML).toContain('id="sdn-wallet-modal-host"');
    expect(root.innerHTML).toContain('id="sdn-provider-url"');
    expect(root.innerHTML).toContain('id="sdn-store-search"');
    expect(root.innerHTML).toContain('id="sdn-store-feed"');
    expect(root.innerHTML).toContain('id="sdn-store-spotlight"');
    expect(root.innerHTML).toContain('id="sdn-store-detail"');
    expect(root.innerHTML).toContain('id="sdn-pinning-rules"');
    expect(root.innerHTML).toContain('id="sdn-address-lookup-value"');
    expect(root.innerHTML).toContain('id="sdn-frontend-tree"');
    expect(root.innerHTML).toContain('id="sdn-frontend-editor"');
    expect(root.innerHTML).toContain('id="sdn-frontend-upload"');
    expect(root.innerHTML).toContain('id="sdn-frontend-save"');
  });
});

class FakeAppShellRoot {
  innerHTML = '';

  querySelector(selector: string): HTMLElement | null {
    void selector;
    return null;
  }
}
