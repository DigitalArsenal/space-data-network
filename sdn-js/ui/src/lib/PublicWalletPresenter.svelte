<script lang="ts">
  import type { createSdnWalletClient } from 'hd-wallet-ui/client/sdn';
  import { onMount, untrack } from 'svelte';

  type WalletPublicClient = Pick<
    ReturnType<typeof createSdnWalletClient>,
    'connect' | 'getSnapshot' | 'openAccount' | 'subscribe'
  >;
  type WalletClientSnapshot = ReturnType<WalletPublicClient['getSnapshot']>;

  let { client }: { client: WalletPublicClient } = $props();

  let snapshot = $state<WalletClientSnapshot>(untrack(() => client.getSnapshot()));
  let commandPending = $state(false);
  let failed = $state(false);
  const opening = $derived(commandPending || snapshot.status === 'opening');
  const label = $derived(
    snapshot.status === 'connected' ? 'Account' : opening ? 'Opening…' : 'Login',
  );

  onMount(() => {
    const unsubscribe = client.subscribe((next) => {
      snapshot = next;
      if (next.status === 'connected') failed = false;
    });

    return () => {
      unsubscribe();
    };
  });

  async function openWallet(): Promise<void> {
    if (opening) return;
    commandPending = true;
    failed = false;
    try {
      if (snapshot.status === 'connected') await client.openAccount();
      else await client.connect();
    } catch {
      failed = true;
    } finally {
      commandPending = false;
    }
  }
</script>

<div class="sdn-public-wallet-presenter">
  <button type="button" disabled={opening} onclick={openWallet}>{label}</button>
  {#if failed}
    <span role="status">Wallet sign-in did not complete. Try again.</span>
  {/if}
</div>

<style>
  .sdn-public-wallet-presenter {
    position: relative;
    display: grid;
    justify-items: end;
    gap: 6px;
  }

  button {
    min-width: 82px;
    height: 30px;
    border: 1px solid rgba(81, 191, 227, 0.42);
    border-radius: 0;
    background: rgba(81, 191, 227, 0.07);
    padding: 0 12px;
    color: #8fdcff;
    font: 600 11px/1 'IBM Plex Mono', ui-monospace, monospace;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    cursor: pointer;
  }

  button:enabled:hover {
    border-color: rgba(81, 191, 227, 0.72);
    background: rgba(81, 191, 227, 0.13);
  }

  button:disabled {
    cursor: progress;
    opacity: 0.72;
  }

  [role='status'] {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    z-index: 20;
    width: max-content;
    max-width: min(260px, 70vw);
    border: 1px solid rgba(223, 109, 109, 0.48);
    background: rgba(15, 20, 27, 0.97);
    padding: 7px 9px;
    color: #efb1b1;
    font: 500 11px/1.4 'IBM Plex Mono', ui-monospace, monospace;
    letter-spacing: 0.02em;
  }

  @media (max-width: 700px) {
    button {
      min-width: 72px;
      padding: 0 9px;
    }
  }
</style>
