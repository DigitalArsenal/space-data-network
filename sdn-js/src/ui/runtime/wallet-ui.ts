/// <reference path="../../types/hd-wallet-ui.d.ts" />

import 'hd-wallet-ui/styles';
import type { SdnBackend } from './sdn-backend';
import { installWalletStorageDiskMirror } from './wallet-storage-bridge';

export interface WalletUIOptions {
  backend?: SdnBackend;
  onLogin?: (payload: WalletLoginPayload) => void | Promise<void>;
  openAccountAfterLogin?: boolean;
  autoOpenWallet?: boolean;
  [key: string]: unknown;
}

export interface WalletLoginPayload {
  xpub?: string;
  peerId?: string;
  publicKey?: string | Uint8Array | ArrayBuffer | number[];
  signingPublicKey?: string | Uint8Array | ArrayBuffer | number[];
  identityPublicKey?: string | Uint8Array | ArrayBuffer | number[];
  encryptionPublicKey?: string | Uint8Array | ArrayBuffer | number[];
  walletAccountId?: string | number;
  walletAccountLabel?: string;
  sign?: (message: string | Uint8Array) => Uint8Array | ArrayBuffer | number[] | string | Promise<Uint8Array | ArrayBuffer | number[] | string>;
}

export interface MountedWalletUI {
  openLogin?: () => void | Promise<void>;
  openAccount?: () => void | Promise<void>;
  destroy?: () => void | Promise<void>;
}

export async function mountWalletUI(
  host: HTMLElement,
  options: WalletUIOptions = {},
): Promise<MountedWalletUI> {
  if (!host) {
    throw new Error('wallet host element is required');
  }

  host.replaceChildren();

  const { backend, ...walletOptions } = options;
  const storageMirror = await installWalletStorageDiskMirror(backend);
  try {
    const { createWalletUI } = await import('hd-wallet-ui');
    const walletRoot = typeof document !== 'undefined' && document.body ? document.body : host;
    const mounted = await createWalletUI(walletRoot, walletOptions);
    return {
      ...mounted,
      async destroy() {
        try {
          await mounted.destroy?.();
        } finally {
          await storageMirror?.destroy();
        }
      },
    };
  } catch (error) {
    await storageMirror?.destroy();
    throw error;
  }
}
