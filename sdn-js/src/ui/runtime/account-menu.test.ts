import { describe, expect, it, vi } from 'vitest';

import type { AdminSnapshot } from './admin-adapter';
import { createAccountMenuController } from './account-menu';

describe('createAccountMenuController', () => {
  it('opens lazily, mounts the wallet once, and exposes sign out for authenticated server sessions', async () => {
    const mountWalletUI = vi.fn(async () => ({
      openAccount: vi.fn(),
    }));
    const signOut = vi.fn(async () => undefined);
    const controller = createAccountMenuController({
      mountWalletUI,
      onSignOut: signOut,
    });

    controller.setAdminSnapshot(createSnapshot({
      mode: 'server',
      permissions: {
        authenticated: true,
        role: 'admin',
      },
    }));

    expect(controller.snapshot().isOpen).toBe(false);
    expect(controller.snapshot().canSignOut).toBe(true);

    await controller.open();
    await controller.open();

    expect(controller.snapshot().isOpen).toBe(true);
    expect(mountWalletUI).toHaveBeenCalledTimes(1);

    await controller.signOut();
    expect(signOut).toHaveBeenCalledTimes(1);
    expect(controller.snapshot().isOpen).toBe(false);
  });

  it('keeps sign out unavailable for local sessions and still opens the wallet account surface', async () => {
    const openAccount = vi.fn(async () => undefined);
    const mountWalletUI = vi.fn(async () => ({
      openAccount,
    }));
    const signOut = vi.fn(async () => undefined);
    const controller = createAccountMenuController({
      mountWalletUI,
      onSignOut: signOut,
    });

    controller.setAdminSnapshot(createSnapshot({
      mode: 'local',
      permissions: {
        authenticated: true,
        role: 'local',
      },
    }));

    await controller.open();
    await controller.openWalletAccount();
    await controller.signOut();

    expect(controller.snapshot().canSignOut).toBe(false);
    expect(openAccount).toHaveBeenCalledTimes(1);
    expect(signOut).not.toHaveBeenCalled();
  });
});

function createSnapshot(
  overrides: Partial<AdminSnapshot> = {},
): AdminSnapshot {
  return {
    mode: overrides.mode ?? 'local',
    serverTarget: overrides.serverTarget ?? null,
    nodeContext: {
      displayName: overrides.nodeContext?.displayName ?? 'Local backend',
      peerId: overrides.nodeContext?.peerId ?? null,
      xpub: overrides.nodeContext?.xpub ?? null,
      transport: overrides.nodeContext?.transport ?? 'helia',
      descriptorUrl: overrides.nodeContext?.descriptorUrl ?? null,
    },
    permissions: {
      authenticated: overrides.permissions?.authenticated ?? false,
      role: overrides.permissions?.role ?? 'guest',
      canManageUsers: overrides.permissions?.canManageUsers ?? false,
      canManageFrontend: overrides.permissions?.canManageFrontend ?? false,
      canManageStore: overrides.permissions?.canManageStore ?? false,
      canOpenWallet: overrides.permissions?.canOpenWallet ?? true,
    },
    workspace: {
      activeId: overrides.workspace?.activeId ?? 'network',
      available: overrides.workspace?.available ?? ['network', 'frontend', 'wallet'],
    },
  };
}
