export interface WalletUIOptions {
  [key: string]: unknown;
}

export interface MountedWalletUI {
  openLogin?: () => void | Promise<void>;
  openAccount?: () => void | Promise<void>;
  destroy?: () => void | Promise<void>;
}

interface EventTargetLike {
  addEventListener?: (eventName: string, listener: () => void) => void;
}

interface TextNodeLike {
  textContent?: string;
}

export async function mountWalletUI(
  host: HTMLElement,
  options: WalletUIOptions = {},
): Promise<MountedWalletUI> {
  if (!host) {
    throw new Error('wallet host element is required');
  }

  host.innerHTML = `
    <div class="sdn-wallet-shell">
      <div class="sdn-wallet-shell__header">
        <span class="sdn-wallet-shell__eyebrow">Identity</span>
        <h3>hd-wallet-ui</h3>
      </div>
      <p class="sdn-wallet-shell__copy">
        Reuse the canonical wallet and vCard identity surface directly inside the SDN UI.
      </p>
      <div class="sdn-wallet-shell__actions">
        <button type="button" data-wallet-action="login">Open Login</button>
        <button type="button" data-wallet-action="account">Open Account</button>
      </div>
      <div data-wallet-status>Starting hd-wallet-ui…</div>
    </div>
  `;

  const statusNode = host.querySelector('[data-wallet-status]') as TextNodeLike | null;
  statusNode && (statusNode.textContent = 'Initializing hd-wallet-ui…');

  const { createWalletUI } = await import('hd-wallet-ui');
  const wallet = await createWalletUI(host, options);

  bindClick(host, '[data-wallet-action="login"]', () => wallet.openLogin?.());
  bindClick(host, '[data-wallet-action="account"]', () => wallet.openAccount?.());

  statusNode && (statusNode.textContent = 'hd-wallet-ui ready');

  return wallet;
}

function bindClick(
  host: HTMLElement,
  selector: string,
  handler: () => void | Promise<void>,
): void {
  const element = host.querySelector(selector) as EventTargetLike | null;
  element?.addEventListener?.('click', () => {
    void handler();
  });
}
