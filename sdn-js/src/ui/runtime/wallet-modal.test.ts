import { describe, expect, it, vi } from 'vitest';

import { createWalletModalController } from './wallet-modal';

describe('createWalletModalController', () => {
  it('mounts the wallet once and reuses the account modal surface', async () => {
    const openAccount = vi.fn(async () => undefined);
    const mountWalletUI = vi.fn(async () => ({
      openAccount,
    }));

    const controller = createWalletModalController({ mountWalletUI });

    await controller.openAccount();
    await controller.openAccount();

    expect(mountWalletUI).toHaveBeenCalledTimes(1);
    expect(openAccount).toHaveBeenCalledTimes(2);
  });

  it('opens the login modal when explicitly requested', async () => {
    const openLogin = vi.fn(async () => undefined);
    const mountWalletUI = vi.fn(async () => ({
      openLogin,
    }));

    const controller = createWalletModalController({ mountWalletUI });

    await controller.openLogin();

    expect(mountWalletUI).toHaveBeenCalledTimes(1);
    expect(openLogin).toHaveBeenCalledTimes(1);
  });
});
