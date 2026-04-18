declare module 'hd-wallet-ui' {
  export interface WalletUIController {
    openLogin?: () => void | Promise<void>;
    openAccount?: () => void | Promise<void>;
    destroy?: () => void | Promise<void>;
  }

  export function createWalletUI(
    rootElement: HTMLElement,
    options?: Record<string, unknown>,
  ): Promise<WalletUIController>;
}
