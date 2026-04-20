import type { MountedWalletUI } from './wallet-ui';

export interface WalletModalController {
  openAccount(): Promise<void>;
  openLogin(): Promise<void>;
}

export interface CreateWalletModalControllerOptions {
  mountWalletUI: () => Promise<MountedWalletUI | void> | MountedWalletUI | void;
}

export function createWalletModalController(
  options: CreateWalletModalControllerOptions,
): WalletModalController {
  let mountedWallet: Promise<MountedWalletUI | void> | null = null;

  async function ensureWallet(): Promise<MountedWalletUI | void> {
    if (!mountedWallet) {
      mountedWallet = Promise.resolve(options.mountWalletUI());
    }
    return mountedWallet;
  }

  return {
    async openAccount(): Promise<void> {
      const wallet = await ensureWallet();
      await wallet?.openAccount?.();
    },
    async openLogin(): Promise<void> {
      const wallet = await ensureWallet();
      await wallet?.openLogin?.();
    },
  };
}
