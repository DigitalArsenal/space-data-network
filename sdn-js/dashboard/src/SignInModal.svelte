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
  import ModalShell from './ModalShell.svelte';
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
  const nodeHost = typeof location !== 'undefined' ? location.host : 'this node';

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
      // A silent revert to the chooser cost a full round to diagnose. The
      // operator still gets the friendly line below; this puts the real
      // exception somewhere a developer can actually read it.
      console.error('[sdn] wallet sign-in failed:', err);
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

</script>

<!-- The ONE modal shell (IRIS ruling 2026-07-29 §3). `dismiss` is what the
     shell calls for Esc, the ✕ and an overlay click alike, so an in-flight
     wallet transaction is cancelled through the wallet's own path on every
     dismissal route rather than only on the one we remembered to wire. -->
<ModalShell
  title="SIGN IN"
  label="Wallet sign in"
  width="560px"
  onClose={dismiss}
>
  {#snippet chips()}
    {#if stage === 'checking'}
      <StatusChip label="LOADING WALLET" color={theme.amber} />
    {:else if stage === 'unavailable'}
      <StatusChip label="SIGN-IN UNAVAILABLE" color={theme.red} />
    {:else}
      <StatusChip label="WALLET READY" color={theme.green} />
    {/if}
  {/snippet}

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
            <!-- Plain instruction, not our internal vocabulary: the operator
                 needs to know WHICH credential to reach for, not the wallet's
                 account index or which signature profile the node admits. -->
            <p>Choose how you want to unlock your wallet on this device.</p>
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
            <p class="hint" style="color:{theme.textFaint};">
              Remembering a wallet with a passkey is coming with the modern sign-in.
              When it arrives the passkey will be tied to this node alone — one passkey
              per node, so a wallet remembered on <span class="mono">{nodeHost}</span>
              can only be unlocked here.
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
</ModalShell>

<style>
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
</style>
