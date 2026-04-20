import type { AdminSnapshot } from '../../../src/ui/runtime/admin-state';
import { query, queryAll } from '../dom/query';
import { bindFeatureCarousel } from './feature-carousel-controller';

export interface AdminShellControllerOptions {
  onConnectServer: () => void | Promise<void>;
  onOpenWallet: () => void | Promise<void>;
  onSetWorkspace: (workspaceId: string) => void | Promise<void>;
  onToggleMode: () => void | Promise<void>;
}

export function bindAdminShell(
  root: HTMLElement,
  options: AdminShellControllerOptions,
): void {
  query<HTMLButtonElement>(root, '#sdn-mode-switch')?.addEventListener('click', () => {
    void options.onToggleMode();
  });
  query<HTMLButtonElement>(root, '#sdn-connect-server')?.addEventListener('click', () => {
    void options.onConnectServer();
  });
  queryAll(root, '[data-nav]').forEach((item) => {
    if (!('addEventListener' in item)) {
      return;
    }
    item.addEventListener('click', () => {
      const target = item.getAttribute('data-nav');
      if (!target || target === 'ipfs-dashboard') {
        return;
      }
      if (target === 'wallet') {
        void options.onOpenWallet();
        return;
      }
      void options.onSetWorkspace(target);
    });
  });
  queryAll(root, '[data-workspace-link]').forEach((item) => {
    if (!('addEventListener' in item)) {
      return;
    }
    item.addEventListener('click', (event) => {
      event.preventDefault();
      const target = item.getAttribute('data-workspace-link');
      if (!target) {
        return;
      }
      if (target === 'wallet') {
        void options.onOpenWallet();
        return;
      }
      void options.onSetWorkspace(target);
    });
  });
  bindFeatureCarousel(root);
}

export function renderShellMeta(root: HTMLElement, snapshot: AdminSnapshot | null | undefined): void {
  const activeTarget = query<HTMLElement>(root, '#sdn-active-target');
  const modeSwitch = query<HTMLElement>(root, '#sdn-mode-switch');
  if (!snapshot) {
    if (activeTarget) {
      activeTarget.textContent = 'Local backend';
    }
    if (modeSwitch) {
      modeSwitch.textContent = 'Local';
    }
    return;
  }
  if (activeTarget) {
    activeTarget.textContent = snapshot.mode === 'local'
      ? 'Local backend'
      : (snapshot.serverTarget?.label
        ? `Server · ${snapshot.serverTarget.label}`
        : (snapshot.serverTarget?.baseUrl ? `Server · ${snapshot.serverTarget.baseUrl}` : 'Server backend'));
  }
  if (modeSwitch) {
    modeSwitch.textContent = snapshot.mode === 'local' ? 'Local' : 'Server';
  }
}
