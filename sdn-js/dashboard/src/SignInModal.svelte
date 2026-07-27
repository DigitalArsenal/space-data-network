<script>
  /**
   * Wallet sign-in (owner directive 2026-07-27: "the wallet sign in needs to
   * work like hd-wallet-ui, which can just be used directly with the styling
   * from our page"; contract nst-node-admin-contract §11).
   *
   * There is no credential field in this component. hd-wallet-ui is mounted
   * into `walletMount` and owns everything from the password/phrase entry
   * through derivation, the confirmation dialog and the signature. Its markup
   * is styled by wallet-theme.css with our tokens — the package ships none.
   *
   * OWNER LAW: "we do NOT load anything from a site." Both wallet packages
   * come from THIS node. If either is unstaged the node 404s and this dialog
   * says SIGN-IN UNAVAILABLE — there is no fallback and there must never be.
   *
   * The one screen we render is the legacy-profile choice: the package's own
   * chooser is not exported, and `sdn.auth.raw-challenge.v1` — the only
   * operation this node can verify today (§11.2/§12) — forces the choice.
   */
  import { onMount } from 'svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { apiFetch, describeApiError } from './api.js';
  import { signInWithWalletUI, loadWallet } from './wallet.js';
  import { walletUIAvailable, LEGACY_PROFILES } from './walletui.js';

  /** @type {{ rootAdminAvailable?: boolean|null, nodeName?: string, onClose: () => void, onSignedIn: (s: any) => void }} */
  let { rootAdminAvailable = null, nodeName = '', onClose, onSignedIn } = $props();

  let walletMount = $state(null);
  let stage = $state('checking'); // checking | unavailable | choose | running
  let error = $state('');
  /** Cancels an in-flight wallet transaction through the wallet's own path. */
  let cancelWallet = null;

  // There is no first-run dead end any more (§14.1): the node derives the
  // accepted admin keys from its OWN seed, so its root recovery phrase always
  // signs in as admin, with no error and no enrolment step. `users_configured`
  // is the flag for "has anyone been enrolled"; it changes nothing here,
  // because the chooser is the same either way.
  const busy = $derived(stage === 'running');

  onMount(() => {
    let cancelled = false;
    // Both trees must be present: the core (/wallet-wasm) AND the app
    // (/wallet-ui). Either missing is the same operator-visible state.
    Promise.all([loadWallet(), walletUIAvailable()])
      .then(([, uiOk]) => {
        if (cancelled) return;
        stage = uiOk ? 'choose' : 'unavailable';
      })
      .catch(() => !cancelled && (stage = 'unavailable'));
    return () => (cancelled = true);
  });

  async function run(profile) {
    if (busy || !walletMount) return;
    error = '';
    stage = 'running';
    try {
      const result = await signInWithWalletUI({
        mount: walletMount,
        profile,
        displayName: nodeName,
        post: (path, body) => apiFetch(path, { method: 'POST', body }),
        onCancelHandle: (fn) => (cancelWallet = fn),
      });
      cancelWallet = null;
      onSignedIn(result);
    } catch (err) {
      cancelWallet = null;
      walletMount?.replaceChildren?.();
      error =
        err?.message === 'USER_CANCELLED' || err?.code === 'USER_CANCELLED'
          ? ''
          : describeApiError(err);
      stage = 'choose';
    }
  }

  /**
   * Close/Esc work on EVERY wallet screen, including mid-transaction. The
   * in-flight wallet operation is cancelled through its own path first
   * (app.stop → controller.destroy), so the next open starts clean instead of
   * inheriting TRANSACTION_IN_PROGRESS or a STALE_CONTROLLER rejection.
   */
  function dismiss() {
    try {
      cancelWallet?.();
    } catch {
      /* the wallet may already have torn itself down */
    }
    cancelWallet = null;
    walletMount?.replaceChildren?.();
    stage = stage === 'unavailable' ? 'unavailable' : 'choose';
    error = '';
    onClose();
  }

  function onKeydown(e) {
    if (e.key === 'Escape') dismiss();
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
<div class="overlay" onclick={(e) => e.target === e.currentTarget && dismiss()}>
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    aria-label="Wallet sign in"
    style="background:{theme.panelRaised};border-color:{theme.panelBorder};color:{theme.textBody};"
  >
    <div class="head" style="border-color:{theme.divider};">
      <div class="ttl" style="color:{theme.textBright};">SIGN IN</div>
      <div class="chips">
        {#if stage === 'checking'}
          <StatusChip label="LOADING WALLET" color={theme.amber} />
        {:else if stage === 'unavailable'}
          <StatusChip label="SIGN-IN UNAVAILABLE" color={theme.red} />
        {:else}
          <StatusChip label="WALLET READY" color={theme.green} />
        {/if}
        <button
          type="button"
          class="close"
          style="color:{theme.textMuted};border-color:{theme.hairline};"
          onclick={dismiss}
          aria-label="Close"
        >✕</button>
      </div>
    </div>

    <div class="body">
      {#if stage === 'unavailable'}
        <div class="note" style="color:{theme.red};border-color:{theme.hairline};">
          This node does not serve the wallet. Stage it with
          <span class="mono">deployment/wallet-wasm/stage-wallet-wasm.sh</span> and reload.
          The dashboard will never load a wallet from anywhere but this node.
        </div>
      {:else}
        {#if stage === 'choose'}
          <!-- Same markup contract as the package's own screens, so
               wallet-theme.css styles this identically. -->
          <section class="wallet-login wallet-legacy-profile">
            <h1>Choose how to unlock</h1>
            <p>Account 0 · this node verifies the legacy signature profile</p>
            <div class="wallet-login-actions">
              {#each LEGACY_PROFILES as p (p.id)}
                <GBtn
                  title={p.hint}
                  variant={p.id === 'bip39-mnemonic-v1-legacy' ? 'primary' : 'neutral'}
                  onclick={() => run(p.id)}
                >{p.label}</GBtn>
              {/each}
            </div>
            {#if rootAdminAvailable}
              <p class="hint" style="color:{theme.textDim};">
                This node's own root recovery phrase signs in as
                <span class="mono" style="color:{theme.cyan};">ADMIN</span>.
              </p>
            {/if}
            <p class="hint" style="color:{theme.textFaint};">
              Your phrase or password is read by the wallet in this page, used to derive
              your key, and wiped. It is never sent anywhere.
            </p>
          </section>
        {/if}
      {/if}

      <!-- hd-wallet-ui renders here. It is never iframed: the app requires
           window.top === window before every sensitive step (§11.4). -->
      <div class="wallet-host" bind:this={walletMount}></div>

      {#if error}
        <div class="err" style="color:{theme.red};border-color:{theme.red};">{error}</div>
      {/if}
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(2, 5, 8, 0.72);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
    padding: 28px;
  }
  .modal {
    width: min(560px, 100%);
    max-height: min(88vh, 760px);
    border: 1px solid;
    display: flex;
    flex-direction: column;
    box-shadow: 0 18px 60px rgba(0, 0, 0, 0.55);
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 14px;
    padding: 15px 18px 12px;
    border-bottom: 1px solid;
  }
  .ttl {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: 22px;
    letter-spacing: 0.1em;
  }
  .chips { display: flex; gap: 6px; align-items: center; }
  .close {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-size: 14.5px;
    line-height: 1;
    padding: 4px 7px;
  }
  .close:disabled { opacity: 0.45; cursor: default; }
  .body { overflow: auto; padding: 15px 18px 18px; display: flex; flex-direction: column; gap: 14px; }
  .wallet-host { min-width: 0; }
  .hint { font-size: 12.5px; letter-spacing: 0.04em; line-height: 1.55; }
  .note {
    border: 1px solid;
    padding: 10px 12px;
    font-size: 13px;
    letter-spacing: 0.04em;
    line-height: 1.55;
  }
  .err {
    border: 1px solid;
    padding: 9px 11px;
    font-size: 13px;
    letter-spacing: 0.04em;
    line-height: 1.5;
  }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }

  @media (max-width: 760px) {
    .overlay { padding: 0; }
    .modal { width: 100%; max-height: 100dvh; height: 100dvh; }
  }
</style>
