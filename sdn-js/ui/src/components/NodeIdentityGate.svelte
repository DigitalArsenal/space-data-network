<script lang="ts">
  import type { NodeIdentityApplyResult } from '../../../src/ui/runtime/sdn-backend';
  import type { NodeIdentitySessionController } from '../lib/node-identity-session';

  export let controller: NodeIdentitySessionController | null = null;
  export let status = 'Locked';
  export let mismatch: NodeIdentityApplyResult | null = null;
  export let loginPromptKey = 0;
  export let onUnlock: () => void | Promise<void> = () => {};

  let walletHost: HTMLElement | null = null;
  let mountedController: NodeIdentitySessionController | null = null;
  let gateState = '';
  let notifiedUnlock = false;
  let autoOpenedController: NodeIdentitySessionController | null = null;
  let lastLoginPromptKey = 0;

  $: if (walletHost && controller && mountedController !== controller) {
    mountedController = controller;
    void mountWallet();
  }
  $: if (status === 'Unlocked' && !notifiedUnlock) {
    notifiedUnlock = true;
    void onUnlock();
  }
  $: if (status !== 'Unlocked') {
    notifiedUnlock = false;
  }
  $: if (walletHost && controller && status !== 'Unlocked' && loginPromptKey !== lastLoginPromptKey) {
    lastLoginPromptKey = loginPromptKey;
    void openWalletLogin();
  }

  async function mountWallet(): Promise<void> {
    if (!controller || !walletHost) return;
    try {
      await controller.mountWallet(walletHost);
      if (autoOpenedController !== controller) {
        autoOpenedController = controller;
        await controller.openLogin();
      }
      gateState = '';
    } catch (error) {
      gateState = errorMessage(error);
    }
  }

  async function openWalletLogin(): Promise<void> {
    if (!controller || !walletHost) return;
    try {
      await controller.mountWallet(walletHost);
      await controller.openLogin();
      gateState = '';
    } catch (error) {
      gateState = errorMessage(error);
    }
  }

  function errorMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }
</script>

<div class="sdn-node-identity-gate" bind:this={walletHost} aria-hidden="true"></div>

{#if mismatch}
  <p class="sdn-status-line">Wallet key change needs confirmation before the node EPM is rewritten.</p>
{/if}
{#if gateState}
  <p class="sdn-status-line">{gateState}</p>
{/if}
