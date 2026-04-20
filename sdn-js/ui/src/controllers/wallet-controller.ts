import type { AppState } from '../state/app-state';

interface WalletControllerOptions {
  state: AppState;
}

export function createWalletController(options: WalletControllerOptions) {
  const { state } = options;

  return {
    async openWalletAccount(): Promise<void> {
      await state.walletModal?.openAccount();
    },
  };
}
